"""Explicit, allowlisted three-host recovery acceptance; never prints credentials."""
import argparse
import base64
import hashlib
import json
import os
import shlex
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.request
import uuid

PG = '8938f553-c7ac-4589-aaf2-5d6823c4cc7e'
MYSQL = 'e81b83e3-1d4c-45d2-8bff-9d3d94edc4b3'
MEMBERS = {
    PG: [('cgpg16_postgresql01', 'a248d206-2d6d-4df7-b1c4-2cf79d5b1e2f', '192.168.102.152'),
         ('cgpg16_postgresql02', 'f936f3da-15b4-4b33-a189-bfc28b7aba76', '192.168.102.153'),
         ('cgpg16_postgresql03', '9cabbf9f-4971-4f01-9ce0-574606dc93bc', '192.168.102.154')],
    MYSQL: [('cgmysql_mysql01', '2e737774-6ee4-45af-8cf5-cc2d65368da7', '192.168.102.152'),
            ('cgmysql_mysql02', '06170cb8-66cc-4bdf-8d22-d7e9cf87ba4a', '192.168.102.153'),
            ('cgmysql_mysql03', 'e51cef59-41a1-4ade-97ab-244e1d58c3b2', '192.168.102.154')]}


def run(args, data=None):
    p = subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120)
    if p.returncode:
        raise RuntimeError('allowlisted command failed: ' + args[0])
    return p.stdout


def peer(host, args):
    if host == '192.168.102.153':
        return run(args)
    return run(['ssh', '-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                '-o', 'UserKnownHostsFile=/etc/clusterguard/ssh/controller_known_hosts',
                '-i', '/etc/clusterguard/ssh/controller_ed25519', 'root@' + host,
                ' '.join(shlex.quote(arg) for arg in args)])


def api(path, payload=None):
    with open('/etc/clusterguard/clusterguard.json') as f:
        config = json.load(f)
    env = {}
    with open('/etc/clusterguard/clusterguard.env') as f:
        for line in f:
            name, sep, value = line.strip().partition('=')
            if sep and not name.startswith('#'):
                parts = shlex.split(value)
                if len(parts) == 1:
                    env[name] = parts[0]
    request = urllib.request.Request('https://192.168.102.153:3000/api/v1/' + path,
        data=None if payload is None else json.dumps(payload).encode(),
        headers={'Authorization': 'Bearer ' + env[config['control_token_env']], 'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, context=ssl.create_default_context(cafile=config['tls_ca_file']), timeout=90) as response:
        body = json.load(response)
    if body.get('status') != 'ok':
        raise RuntimeError('API did not confirm request')
    return body.get('result')


def services(cluster):
    result = []
    for name, member, host in MEMBERS[cluster]:
        service = json.loads(run(['docker', 'service', 'inspect', name]))[0]
        labels = service['Spec']['TaskTemplate']['ContainerSpec']['Labels']
        if labels.get('clusterguard.cluster_id') != cluster or labels.get('clusterguard.instance_id') != member:
            raise RuntimeError('service ownership differs from acceptance allowlist')
        result.append(service)
    return result


def prepare_pg():
    inventory = services(PG)
    script_path = '/usr/share/clusterguard/docker-swarm/postgresql/clusterguard-postgres-entrypoint.sh'
    with open(script_path, 'rb') as f:
        new_script = f.read()
    if b'without using the bootstrap source as topology' not in new_script:
        raise RuntimeError('installed package lacks bootstrap-env fix')
    target = '/usr/local/bin/clusterguard-postgres-entrypoint.sh'
    refs = []
    for service in inventory:
        spec = service['Spec']['TaskTemplate']['ContainerSpec']
        if spec.get('Command') != [target]:
            raise RuntimeError('custom entrypoint requires review')
        matches = [c for c in spec.get('Configs', []) if c.get('File', {}).get('Name') == target]
        if len(matches) != 1:
            raise RuntimeError('entrypoint config is ambiguous')
        ref = matches[0]
        old = json.loads(run(['docker', 'config', 'inspect', ref['ConfigID']]))[0]
        data = base64.b64decode(old['Spec']['Data'])
        # Fingerprint obtained by the read-only field inspection, including jq's final newline.
        if hashlib.sha256(data + b'\n').hexdigest() != '5c25273db0ad71bc28db01972c67e1347aee8c4c10ba6a5bc3faa95975c95fcf' and data != new_script:
            raise RuntimeError('unreviewed entrypoint content; no replacement performed')
        refs.append(ref)
    api('clusters/' + PG + '/recovery-freeze', {'freeze': True})
    for _, _, host in MEMBERS[PG]:
        addresses = json.loads(peer(host, ['ip', '-j', 'address', 'show']))
        if any(a.get('local') == '192.168.102.157' for interface in addresses for a in interface.get('addr_info', [])):
            raise RuntimeError('unexpected PostgreSQL VIP owner; stop requires review')
    run(['docker', 'service', 'scale', '--detach=true'] + [s['ID'] + '=0' for s in inventory])
    deadline = time.monotonic() + 120
    while True:
        running = [name for name, _, host in MEMBERS[PG] if peer(host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + name, '--format', '{{.ID}}']).strip()]
        if not running:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('PostgreSQL services did not stop')
        time.sleep(2)
    digest = hashlib.sha256(new_script).hexdigest()
    name = 'cgpg16_postgres_entrypoint_' + digest[:20]
    existing = run(['docker', 'config', 'ls', '--filter', 'name=' + name, '--format', '{{.ID}}']).decode().splitlines()
    if existing:
        if len(existing) != 1:
            raise RuntimeError('new config identity ambiguous')
        config_id = existing[0]
        saved = json.loads(run(['docker', 'config', 'inspect', config_id]))[0]
        if base64.b64decode(saved['Spec']['Data']) != new_script:
            raise RuntimeError('new config content conflict')
    else:
        config_id = run(['docker', 'config', 'create', name, '-'], new_script).decode().strip()
    backup_dir = tempfile.mkdtemp(prefix='cg-pg-entrypoint-migration-', dir='/var/tmp')
    report = {'cluster': PG, 'new_config': config_id, 'sha256': digest, 'services': []}
    for service, ref in zip(inventory, refs):
        report['services'].append({'id': service['ID'], 'name': service['Spec']['Name'], 'original_configs': service['Spec']['TaskTemplate']['ContainerSpec']['Configs']})
    with open(os.path.join(backup_dir, 'config-references.json'), 'w') as f:
        json.dump(report, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    for service, ref in zip(inventory, refs):
        if ref['ConfigID'] != config_id:
            original = ref['File']
            addition = 'source={},target={},uid={},gid={},mode={:04o}'.format(name, target, original['UID'], original['GID'], original['Mode'])
            run(['docker', 'service', 'update', '--detach=true', '--config-rm', ref['ConfigName'], '--config-add', addition, service['ID']])
    for service in services(PG):
        if service['Spec']['Mode']['Replicated']['Replicas'] != 0 or not any(c['ConfigID'] == config_id for c in service['Spec']['TaskTemplate']['ContainerSpec']['Configs']):
            raise RuntimeError('stopped service config migration did not verify')
    print(json.dumps({'result': 'PG frozen and stopped; entrypoint migrated; data retained', 'config': config_id, 'sha256': digest, 'audit_directory': backup_dir}), flush=True)


def mysql_query(name, host, sql):
    ids = peer(host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + name, '--format', '{{.ID}}']).decode().splitlines()
    if len(ids) != 1:
        raise RuntimeError('MySQL service does not have one live container')
    return peer(host, ['docker', 'exec', ids[0], 'mysql', '--defaults-extra-file=/run/secrets/clusterguard-operation.cnf', '--batch', '--raw', '--skip-column-names', '--execute', sql]).decode().strip()


def prepare_pg_retest():
    inventory = services(PG)
    topology = api('clusters/' + PG + '/topology')
    if topology.get('health', {}).get('state') != 'healthy' or len(topology.get('links', [])) != 2 or sum(i['role'] == 'primary' for i in topology['instances']) != 1:
        raise RuntimeError('PG must be healthy before final full-stop retest')
    api('clusters/' + PG + '/recovery-freeze', {'freeze': True})
    run(['docker', 'service', 'scale', '--detach=true'] + [s['ID'] + '=0' for s in inventory])
    deadline = time.monotonic() + 120
    while any(peer(host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + name, '--format', '{{.ID}}']).strip() for name, _, host in MEMBERS[PG]):
        if time.monotonic() > deadline:
            raise RuntimeError('PG full-stop retest preparation did not complete')
        time.sleep(2)
    print(json.dumps({'result': 'PG frozen and all members stopped for final retest; data and rotated credentials retained'}), flush=True)


def prepare_mysql():
    inventory = services(MYSQL)
    topology = api('clusters/' + MYSQL + '/topology')
    primary = [i['resource_id'] for i in topology.get('instances', []) if i.get('role') == 'primary']
    if topology.get('health', {}).get('state') != 'healthy' or len(primary) != 1 or len(topology.get('links', [])) != 2:
        raise RuntimeError('MySQL must be healthy before the explicit all-stop acceptance scenario')
    marker = str(uuid.uuid4())
    table = 'cg_recovery_acceptance_20260908.recovery_markers'
    for name, member, host in MEMBERS[MYSQL]:
        if member == primary[0]:
            mysql_query(name, host, "CREATE DATABASE IF NOT EXISTS cg_recovery_acceptance_20260908; CREATE TABLE IF NOT EXISTS " + table + " (id VARCHAR(36) PRIMARY KEY, note VARCHAR(80) NOT NULL); INSERT INTO " + table + " VALUES ('" + marker + "','before all-member stopped recovery acceptance')")
    deadline = time.monotonic() + 60
    while True:
        counts = []
        for name, _, host in MEMBERS[MYSQL]:
            exists = mysql_query(name, host, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='cg_recovery_acceptance_20260908' AND table_name='recovery_markers'")
            counts.append(mysql_query(name, host, "SELECT COUNT(*) FROM " + table + " WHERE id='" + marker + "'") if exists == '1' else '0')
        if counts == ['1', '1', '1']:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('acceptance marker has not replicated to every member')
        time.sleep(2)
    audit_dir = tempfile.mkdtemp(prefix='cg-mysql-all-stop-', dir='/var/tmp')
    with open(os.path.join(audit_dir, 'marker.json'), 'w') as f:
        json.dump({'cluster': MYSQL, 'table': table, 'marker': marker, 'original_primary': primary[0]}, f)
        f.flush()
        os.fsync(f.fileno())
    api('clusters/' + MYSQL + '/recovery-freeze', {'freeze': True})
    run(['docker', 'service', 'scale', '--detach=true'] + [s['ID'] + '=0' for s in inventory])
    deadline = time.monotonic() + 120
    while True:
        running = any(peer(host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + name, '--format', '{{.ID}}']).strip() for name, _, host in MEMBERS[MYSQL])
        if not running:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('MySQL all-stop did not complete')
        time.sleep(2)
    print(json.dumps({'result': 'MySQL frozen and all members stopped; data retained', 'marker': marker, 'audit_directory': audit_dir}), flush=True)


def observe(cluster):
    seen = set()
    first_primary = None
    count = 0
    deadline = time.monotonic() + 1800
    while count < 50:
        topology = api('clusters/' + cluster + '/topology')
        stamp = topology.get('observed_at')
        if stamp and stamp not in seen:
            instances = topology.get('instances', [])
            primary = [i['resource_id'] for i in instances if i.get('role') == 'primary']
            links = topology.get('links', [])
            if first_primary is None:
                first_primary = primary
            targets = {i['resource_id'] for i in instances if i.get('role') == ('standby' if cluster == PG else 'replica')}
            healthy = topology.get('health', {}).get('state') == 'healthy' and len(instances) == 3 and len(primary) == 1 and primary == first_primary and len(links) == 2 and len(targets) == 2 and {l.get('target_instance_id') for l in links} == targets and all(i.get('health', {}).get('state') == 'healthy' for i in instances) and all(l.get('healthy') and l.get('lag_seconds') == 0 and l.get('source_instance_id') == primary[0] for l in links)
            print(json.dumps({'cycle': count + 1, 'cluster': cluster, 'observed_at': stamp, 'healthy': healthy, 'primary': primary, 'links': len(links)}), flush=True)
            if not healthy:
                raise RuntimeError('field background discovery acceptance failed')
            count += 1
            seen.add(stamp)
        if time.monotonic() > deadline:
            raise RuntimeError('did not observe fifty distinct background cycles')
        if count < 50:
            time.sleep(0.2)


def verify_mysql(audit_directory):
    if not audit_directory or not audit_directory.startswith('/var/tmp/cg-mysql-all-stop-') or '/' in audit_directory[len('/var/tmp/'):]:
        raise RuntimeError('the exact acceptance audit directory is required')
    with open(os.path.join(audit_directory, 'marker.json')) as stream:
        marker = json.load(stream)
    if marker.get('cluster') != MYSQL or marker.get('table') != 'cg_recovery_acceptance_20260908.recovery_markers':
        raise RuntimeError('acceptance receipt ownership differs')
    uuid.UUID(marker['marker'])
    topology = api('clusters/' + MYSQL + '/topology')
    primary = [i['resource_id'] for i in topology['instances'] if i.get('role') == 'primary']
    if len(primary) != 1 or topology['health']['state'] != 'healthy' or len(topology['links']) != 2:
        raise RuntimeError('MySQL recovered topology is not healthy')
    reports = []
    for name, member, host in MEMBERS[MYSQL]:
        sql = "SELECT JSON_OBJECT('uuid',@@server_uuid,'read_only',@@read_only,'super_read_only',@@super_read_only,'offline_mode',@@offline_mode,'gtid',@@global.gtid_executed,'io_running',(SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE SERVICE_STATE='ON'),'sql_running',(SELECT COUNT(*) FROM performance_schema.replication_applier_status WHERE SERVICE_STATE='ON'),'errors',(SELECT COUNT(*) FROM performance_schema.replication_applier_status_by_worker WHERE LAST_ERROR_NUMBER<>0),'marker',(SELECT COUNT(*) FROM " + marker['table'] + " WHERE id='" + marker['marker'] + "'))"
        native = json.loads(mysql_query(name, host, sql))
        is_primary = member == primary[0]
        instance = next(i for i in topology['instances'] if i['resource_id'] == member)
        if native['uuid'] != instance['engine_identity']['server_uuid'] or native['marker'] != 1 or native['offline_mode'] != 0 or native['errors'] != 0:
            raise RuntimeError('MySQL native identity, business marker or guard release did not verify')
        if native['read_only'] != int(not is_primary) or native['super_read_only'] != int(not is_primary) or native['io_running'] != int(not is_primary) or native['sql_running'] != int(not is_primary):
            raise RuntimeError('MySQL native roles and replication threads did not verify')
        reports.append({'host': host, 'primary': is_primary, 'native': native})
    if len({r['native']['gtid'] for r in reports}) != 1:
        raise RuntimeError('MySQL GTID histories have not converged')
    print(json.dumps({'result': 'MySQL native roles, UUIDs, full-stop marker and GTIDs verified', 'members': reports}), flush=True)


def verify_state():
    reports = []
    for cluster in (PG, MYSQL):
        status = api('clusters/' + cluster + '/power/status')
        topology = status['topology']
        primary = [i for i in topology['instances'] if i['role'] == 'primary']
        recovery = status['cluster'].get('recovery', {})
        classification = status['outage_classification']
        if len(primary) != 1 or topology['health']['state'] != 'healthy' or len(topology['links']) != 2:
            raise RuntimeError('final topology is not healthy')
        if status['recovery_freeze'] or status['protected'] or status['instances_in_maintenance']:
            raise RuntimeError('final recovery protections have not been released')
        if recovery.get('current_primary_id') != primary[0]['resource_id'] or recovery.get('expected_state') != 'running' or recovery.get('actual_state') != 'running' or recovery.get('incident_active') or not recovery.get('incident_recovered') or recovery.get('last_recovery_status') != 'succeeded':
            raise RuntimeError('Recovery Commit state is incomplete')
        if classification.get('kind') != 'normal' or classification.get('database_state') != 'running' or classification.get('automatic_failover_suppressed'):
            raise RuntimeError('historical shutdown is still affecting current power state')
        if any(i.get('desired_role') != i['role'] for i in topology['instances']):
            raise RuntimeError('desired roles differ from native roles')
        reports.append({'cluster': cluster, 'primary': primary[0]['ip_address'], 'recovery': recovery, 'outage_classification': classification, 'links': len(topology['links'])})
    print(json.dumps({'result': 'Both Recovery Commits and current power states verified', 'clusters': reports}), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['prepare-pg', 'prepare-pg-retest', 'prepare-mysql', 'observe-pg', 'observe-mysql', 'verify-mysql', 'verify-state'])
    parser.add_argument('--audit-directory')
    arguments = parser.parse_args()
    if socket.gethostname() != 'orch-mysql02':
        raise RuntimeError('acceptance helper is pinned to the authorized lab host')
    if arguments.action == 'prepare-pg':
        prepare_pg()
    elif arguments.action == 'prepare-pg-retest':
        prepare_pg_retest()
    elif arguments.action == 'prepare-mysql':
        prepare_mysql()
    elif arguments.action == 'verify-mysql':
        verify_mysql(arguments.audit_directory)
    elif arguments.action == 'verify-state':
        verify_state()
    else:
        observe(PG if arguments.action == 'observe-pg' else MYSQL)
