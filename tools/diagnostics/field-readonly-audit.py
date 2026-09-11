"""Read-only, bounded field evidence. Feed over SSH; never export credentials."""
import datetime
import hashlib
import json
import os
import pwd
import re
import shlex
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
import urllib.parse

HOSTS = ('192.168.102.152', '192.168.102.153', '192.168.102.154')
SECRET_KEY = re.compile(r'password|passwd|secret|token|authorization|credential|conninfo|passfile|private.?key', re.I)
secrets = []


def scrub(value, key=''):
    if SECRET_KEY.search(key):
        return '[REDACTED]'
    if isinstance(value, dict):
        return {k: scrub(v, k) for k, v in value.items()}
    if isinstance(value, list):
        return [scrub(v) for v in value]
    if isinstance(value, str):
        for secret in secrets:
            if len(secret) >= 4:
                value = value.replace(secret, '[REDACTED]')
        return '\n'.join('[REDACTED sensitive diagnostic line]' if
                         re.search(r'(?:password|passwd|passfile|primary_conninfo|token|secret)\s*[=:]|Bearer\s+|://[^\s/]+:[^\s@]+@', line, re.I)
                         else line for line in value.split('\n'))
    return value


def run(args, timeout=15):
    start = time.monotonic()
    try:
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        return {'exit_code': result.returncode, 'seconds': round(time.monotonic() - start, 3),
                'stdout_bytes': len(result.stdout), 'stdout_truncated': len(result.stdout) > 200000,
                'stdout': result.stdout.decode('utf8', 'replace')[:200000]}
    except (OSError, subprocess.TimeoutExpired) as error:
        return {'exit_code': None, 'error_type': type(error).__name__}


def read_json(path):
    with open(path) as handle:
        return json.load(handle)


def environment(path):
    result = {}
    with open(path) as handle:
        for line in handle:
            key, sep, value = line.strip().partition('=')
            if sep and not key.startswith('#'):
                parts = shlex.split(value)
                if len(parts) == 1:
                    result[key] = parts[0]
                    if SECRET_KEY.search(key):
                        secrets.append(parts[0])
    return result


def inventory():
    commands = {
        'os': ['uname', '-srm'],
        'release': ['cat', '/etc/os-release'],
        'clock': ['timedatectl', 'show', '-p', 'Timezone', '-p', 'NTPSynchronized'],
        'package': ['rpm', '-q', '--qf', '%{NAME} %{VERSION}-%{RELEASE} %{ARCH}\n', 'clusterguard-ha'],
        'units': ['systemctl', 'list-units', '--all', '--no-pager', '--plain', 'clusterguard*', 'docker*', 'mysqld*', 'postgresql*'],
        'listeners': ['ss', '-lntp'],
        'containers': ['docker', 'ps', '-a', '--format', '{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'],
        'images': ['docker', 'images', '--format', '{{.Repository}}:{{.Tag}} {{.Size}}'],
        'services': ['docker', 'service', 'ls', '--format', '{{json .}}'],
        'docker': ['docker', 'info', '--format', '{{json .Swarm}}'],
    }
    result = {key: run(args) for key, args in commands.items()}
    result['cpu_count'] = os.cpu_count()
    result['load_average'] = list(os.getloadavg())
    with open('/proc/meminfo') as handle:
        result['memory'] = {line.split(':')[0]: line.split(':')[1].strip() for line in handle
                            if line.startswith(('MemTotal:', 'MemAvailable:', 'SwapTotal:', 'SwapFree:'))}
    result['disks'] = {}
    for directory in ('/', '/var', '/var/lib/docker', '/var/lib/clusterguard', '/var/tmp'):
        if os.path.isdir(directory):
            stat = os.statvfs(directory)
            result['disks'][directory] = {'available_bytes': stat.f_bavail * stat.f_frsize,
                                        'total_bytes': stat.f_blocks * stat.f_frsize}
    result['services_detail'] = {}
    for unit in ('clusterguard-ha', 'clusterguard-agent', 'clusterguard-agent-reconcile', 'clusterguard-update-helper', 'docker'):
        result['services_detail'][unit] = run(['systemctl', 'show', unit, '-p', 'ActiveState', '-p', 'SubState',
                                              '-p', 'MainPID', '-p', 'NRestarts', '-p', 'MemoryCurrent', '-p', 'ActiveEnterTimestamp'])
    config = read_json('/etc/clusterguard/clusterguard.json')
    safe_keys = ('listen', 'listen_address', 'http_address', 'data_file', 'state_file', 'tls_ca_file',
                 'control_token_env', 'discovery_interval', 'discovery_timeout', 'node_id')
    result['config'] = {key: config[key] for key in safe_keys if key in config}
    result['config_keys'] = sorted(config.keys())
    consensus = config.get('consensus', {})
    result['consensus'] = {key: value for key, value in consensus.items()
                           if key in ('enabled', 'node_id', 'bind_address', 'advertise_address', 'data_directory', 'peers', 'bootstrap')}
    result['binaries'] = {}
    for path in ('/usr/local/bin/clusterguard', '/usr/local/bin/clusterguard-agent'):
        if os.path.isfile(path):
            with open(path, 'rb') as handle:
                result['binaries'][path] = hashlib.sha256(handle.read()).hexdigest()
    return result


def api_client(host):
    config = read_json('/etc/clusterguard/clusterguard.json')
    env = environment('/etc/clusterguard/clusterguard.env')
    token = env[config['control_token_env']]
    secrets.append(token)
    context = ssl.create_default_context(cafile=config['tls_ca_file'])

    def get(path):
        start = time.monotonic()
        request = urllib.request.Request('https://' + host + ':3000/api/v1/' + path,
                                         headers={'Authorization': 'Bearer ' + token})
        try:
            with urllib.request.urlopen(request, context=context, timeout=20) as response:
                raw = response.read(4 * 1024 * 1024 + 1)
                if len(raw) > 4 * 1024 * 1024:
                    return {'http': response.status, 'error': 'response exceeded read-only audit bound'}
                body = json.loads(raw)
                return {'http': response.status, 'seconds': round(time.monotonic() - start, 3),
                        'bytes': len(raw), 'body': body}
        except urllib.error.HTTPError as error:
            raw = error.read(16384)
            try:
                body = json.loads(raw)
            except ValueError:
                body = {'error': 'non-json error response'}
            return {'http': error.code, 'seconds': round(time.monotonic() - start, 3), 'body': body}
        except Exception as error:
            return {'error_type': type(error).__name__, 'seconds': round(time.monotonic() - start, 3)}
    return get


def api_inventory(host):
    get = api_client(host)
    result = {path: get(path) for path in ('platform/version', 'control-plane/status', 'capabilities',
                                          'clusters', 'nodes/sync/capabilities', 'platform/updates')}
    clusters = result['clusters'].get('body', {}).get('result', [])
    for cluster in clusters:
        identity = cluster['resource_id']
        if not re.match(r'^[a-f0-9-]{36}$', identity):
            continue
        for suffix in ('', '/topology', '/health', '/candidates', '/recovery/status'):
            route = 'clusters/' + identity + suffix
            result[route] = get(route)
    return result


def observations(host):
    get = api_client(host)
    clusters = get('clusters').get('body', {}).get('result', [])
    result = {'samples': {}, 'completed': False, 'duration_seconds': 0, 'peer_final': {}}
    start = time.monotonic()
    for cluster in clusters:
        result['samples'][cluster['resource_id']] = []
    while clusters and time.monotonic() - start < 780:
        for cluster in clusters:
            identity = cluster['resource_id']
            samples = result['samples'][identity]
            if len(samples) >= 50:
                continue
            base = 'clusters/' + identity
            response = get(base + '/topology')
            topology = response.get('body', {}).get('result', {})
            observed = topology.get('observed_at')
            if not observed or (samples and observed == samples[-1]['observed_at']):
                continue
            query = '?observation_id=' + urllib.parse.quote(observed)
            health, candidates = get(base + '/health' + query), get(base + '/candidates' + query)
            timings = {'topology': response.get('seconds'), 'health': health.get('seconds'),
                       'candidates': candidates.get('seconds')}
            context = get('operations?view=context&cluster_id=' + identity)
            page = get('operations?view=page&limit=20&cluster_id=' + identity)
            timings.update({'operation_context': context.get('seconds'), 'operation_page': page.get('seconds')})
            samples.append({'observed_at': observed, 'health': topology.get('health'),
                            'instances': [{'id': item['resource_id'], 'address': item.get('ip_address'),
                                           'role': item.get('role'), 'health': item.get('health', {}).get('state')}
                                          for item in topology.get('instances', [])],
                            'links': topology.get('links'), 'seconds': timings,
                            'http': {'topology': response.get('http'), 'health': health.get('http'),
                                     'candidates': candidates.get('http'), 'context': context.get('http'), 'page': page.get('http')},
                            'errors': {key: item.get('body') for key, item in [('health', health), ('candidates', candidates), ('context', context), ('page', page)] if item.get('http') != 200}})
        if all(len(samples) >= 50 for samples in result['samples'].values()):
            result['completed'] = True
            break
        time.sleep(2)
    result['duration_seconds'] = round(time.monotonic() - start, 3)
    for peer in HOSTS:
        peer_get = api_client(peer)
        result['peer_final'][peer] = {'control': peer_get('control-plane/status'),
                                    'topologies': {c['resource_id']: peer_get('clusters/' + c['resource_id'] + '/topology') for c in clusters}}
    return result


def refresh_timings(host):
    get = api_client(host)
    clusters = get('clusters').get('body', {}).get('result', [])
    result = {'requests': []}
    for attempt in range(10):
        for cluster in clusters:
            base = 'clusters/' + cluster['resource_id']
            for path in (base, base + '/metrics', base + '/power/status',
                         'operations?view=context&cluster_id=' + cluster['resource_id']):
                response = get(path)
                result['requests'].append({'round': attempt, 'path': path, 'http': response.get('http'),
                                           'seconds': response.get('seconds'), 'bytes': response.get('bytes'),
                                           'error': response.get('body') if response.get('http') != 200 else None})
        time.sleep(0.5)
    return result


PG_STATE_SQL = """SELECT row_to_json(s) FROM (
SELECT version(), pg_is_in_recovery() AS recovery,
(pg_control_system()).system_identifier::text AS system_identifier,
(pg_control_checkpoint()).timeline_id AS timeline,
current_setting('clusterguard.node_id', true) AS node_id,
current_setting('clusterguard.primary_node_id', true) AS bootstrap_primary_id,
current_setting('data_directory') AS data_directory,
current_setting('port') AS port,
pg_last_wal_receive_lsn()::text AS receive_lsn,
pg_last_wal_replay_lsn()::text AS replay_lsn,
CASE WHEN pg_is_in_recovery() THEN NULL ELSE pg_current_wal_lsn()::text END AS current_lsn) s"""
PG_RECEIVER_SQL = "SELECT coalesce(json_agg(s),'[]'::json) FROM (SELECT status, receive_start_lsn::text, received_tli, latest_end_lsn::text, slot_name, sender_host, sender_port FROM pg_stat_wal_receiver) s"
PG_SENDERS_SQL = "SELECT coalesce(json_agg(s),'[]'::json) FROM (SELECT application_name, client_addr, state, sync_state, sent_lsn::text, write_lsn::text, flush_lsn::text, replay_lsn::text FROM pg_stat_replication) s"
PG_SLOTS_SQL = "SELECT coalesce(json_agg(s),'[]'::json) FROM (SELECT slot_name, slot_type, active, restart_lsn::text FROM pg_replication_slots) s"


def pg_connection_summary(base):
    response = run(base + ['-c', "SELECT current_setting('primary_conninfo')"])
    summary = {'exit_code': response.get('exit_code')}
    if response.get('exit_code') != 0:
        return summary
    try:
        fields = dict(value.split('=', 1) for value in shlex.split(response['stdout']) if '=' in value)
    except ValueError:
        return {'error': 'connection settings parse failed; raw output withheld'}
    secrets.extend(value for key, value in fields.items() if SECRET_KEY.search(key))
    summary.update({key: value for key, value in fields.items() if key in ('host', 'hostaddr', 'port', 'user', 'application_name', 'sslmode')})
    summary['inline_auth_present'] = bool(fields.get('password'))
    summary['auth_file_configured'] = bool(fields.get('passfile'))
    return summary


def native():
    environment('/etc/clusterguard/clusterguard.env')
    result = {'postgresql': [], 'mysql': [], 'service_bindings': []}
    ids = run(['docker', 'ps', '-q'])['stdout'].split()
    for identity in ids:
        raw = run(['docker', 'inspect', identity])
        if raw.get('exit_code') != 0:
            continue
        item = json.loads(raw['stdout'])[0]
        name = item['Name'].lstrip('/')
        env = dict(v.split('=', 1) for v in item['Config'].get('Env', []) if '=' in v)
        secrets.extend(value for key, value in env.items() if SECRET_KEY.search(key))
        if name.startswith('cgpg16_postgresql'):
            base = ['docker', 'exec', identity, 'psql', '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-U', env.get('POSTGRES_USER', 'postgres'), '-d', 'postgres']
            result['postgresql'].append({'runtime': 'docker', 'name': name, 'mounts': item['Mounts'], 'upstream': pg_connection_summary(base),
                                        'queries': {key: run(base + ['-c', sql]) for key, sql in (
                                            ('state', PG_STATE_SQL), ('receiver', PG_RECEIVER_SQL),
                                            ('senders', PG_SENDERS_SQL), ('slots', PG_SLOTS_SQL))}})
        if name.startswith('cgmysql_mysql'):
            base = ['docker', 'exec', identity, 'mysql', '--defaults-extra-file=/run/secrets/clusterguard-operation.cnf', '--batch', '--raw', '--skip-column-names']
            state_sql = "SELECT JSON_OBJECT('version',@@version,'uuid',@@server_uuid,'read_only',@@read_only,'super_read_only',@@super_read_only,'gtid_executed',@@global.gtid_executed,'gtid_purged',@@global.gtid_purged)"
            replica = run(base + ['-e', "SELECT JSON_OBJECT('source_host', c.HOST, 'source_port', c.PORT, 'source_uuid', s.SOURCE_UUID, 'io_running', s.SERVICE_STATE, 'sql_running', a.SERVICE_STATE, 'io_error_number', s.LAST_ERROR_NUMBER) FROM performance_schema.replication_connection_configuration c JOIN performance_schema.replication_connection_status s USING(CHANNEL_NAME) JOIN performance_schema.replication_applier_status a USING(CHANNEL_NAME)"])
            allowed = ('Source_Host', 'Source_Port', 'Source_UUID', 'Replica_IO_Running', 'Replica_SQL_Running',
                       'Seconds_Behind_Source', 'Retrieved_Gtid_Set', 'Executed_Gtid_Set', 'Last_IO_Errno',
                       'Last_SQL_Errno', 'Last_IO_Error', 'Last_SQL_Error', 'Auto_Position')
            allowed += ('Master_Host', 'Master_Port', 'Master_UUID', 'Slave_IO_Running', 'Slave_SQL_Running', 'Seconds_Behind_Master')
            fields = {}
            if replica.get('exit_code') == 0:
                fields = {'channels': [json.loads(line) for line in replica.get('stdout', '').splitlines() if line.strip()]}
            for line in replica.get('stdout', '').splitlines():
                key, sep, value = line.strip().partition(':')
                if sep and key in allowed:
                    fields[key] = value.strip()
            result['mysql'].append({'runtime': 'docker', 'name': name, 'state': run(base + ['-e', state_sql]),
                                    'replica': fields, 'replica_exit_code': replica['exit_code']})
    process = run(['systemctl', 'show', 'clusterguard-postgresql-5432', '-p', 'MainPID', '--value'])
    pid = process.get('stdout', '').strip()
    if pid.isdigit() and int(pid) > 0:
        binary = os.readlink('/proc/' + pid + '/exe')
        owner = pwd.getpwuid(os.stat('/proc/' + pid).st_uid).pw_name
        with open('/proc/' + pid + '/cmdline', 'rb') as handle:
            argv = handle.read().decode().split('\0')
        data_directory = argv[argv.index('-D') + 1]
        with open(os.path.join(data_directory, 'postmaster.pid')) as handle:
            socket_directory = handle.read().splitlines()[4]
        base = ['runuser', '-u', owner, '--', os.path.join(os.path.dirname(binary), 'psql'), '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-h', socket_directory, '-p', '5432', '-d', 'postgres']
        result['postgresql'].append({'runtime': 'host', 'binary': binary, 'owner': owner, 'data_directory': data_directory, 'upstream': pg_connection_summary(base),
                                    'queries': {key: run(base + ['-c', sql]) for key, sql in (
                                        ('state', PG_STATE_SQL), ('receiver', PG_RECEIVER_SQL),
                                        ('senders', PG_SENDERS_SQL), ('slots', PG_SLOTS_SQL))}})
    names = run(['docker', 'service', 'ls', '--format', '{{.Name}}'])['stdout'].split()
    for name in names:
        if not name.startswith(('cgpg16_', 'cgmysql_')):
            continue
        item = json.loads(run(['docker', 'service', 'inspect', name])['stdout'])[0]
        spec = item['Spec']['TaskTemplate']['ContainerSpec']
        env = dict(v.split('=', 1) for v in spec.get('Env', []) if '=' in v)
        result['service_bindings'].append({'name': name, 'replicas': item['Spec']['Mode'], 'updated_at': item.get('UpdatedAt'),
            'labels': spec.get('Labels', {}), 'mounts': spec.get('Mounts', []),
            'bootstrap': {k: v for k, v in env.items() if k in ('CG_PG_PRIMARY_NODE_ID', 'CG_PG_SOURCE_HOST', 'CG_PG_NODE_ID')},
            'configs': [{'name': c['ConfigName'], 'target': c['File']['Name']} for c in spec.get('Configs', [])]})
    result['vip_addresses'] = run(['ip', '-j', 'address', 'show'])
    result['top_processes'] = run(['ps', '-eo', 'pid,comm,pcpu,pmem,stat', '--sort=-pcpu'])
    return result


def logs():
    environment('/etc/clusterguard/clusterguard.env')
    result = {}
    for unit in ('clusterguard-ha', 'clusterguard-agent-reconcile', 'clusterguard-update-helper', 'clusterguard-postgresql-5432'):
        result[unit] = run(['journalctl', '-u', unit, '--since', '-10min', '-n', '60', '--no-pager', '-o', 'short-iso'])
    result['clock_tracking'] = run(['chronyc', 'tracking'])
    result['clock_sources'] = run(['chronyc', 'sources'])
    return result


def bindings():
    config = read_json('/etc/clusterguard/agent.json')
    env = environment('/etc/clusterguard/agent.env')
    result = {'agent': config, 'environment_presence': {}, 'metadata_files': {}, 'tooling': {}, 'host_pg_auth_files': []}
    for policy in config.get('clusters', []):
        if policy.get('engine') != 'postgresql' or policy.get('runtime_kind', 'host') == 'docker':
            continue
        path = policy.get('postgresql_passfile', '')
        status = {'cluster_id': policy['cluster_id'], 'port': policy.get('postgresql_port'), 'exists': os.path.isfile(path)}
        if status['exists']:
            stat = os.stat(path)
            status.update({'mode': oct(stat.st_mode & 0o777), 'owner': pwd.getpwuid(stat.st_uid).pw_name,
                           'readable_by_db_user': run(['runuser', '-u', policy['postgresql_user'], '--', 'test', '-r', path])['exit_code'] == 0})
            parent = os.stat(os.path.dirname(path))
            status.update({'parent_mode': oct(parent.st_mode & 0o777), 'parent_owner': pwd.getpwuid(parent.st_uid).pw_name})
        result['host_pg_auth_files'].append(status)
    def walk(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key.endswith('_env') and isinstance(child, str):
                    result['environment_presence'][child] = bool(env.get(child))
                walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)
    walk(config)
    control = read_json('/etc/clusterguard/clusterguard.json')
    for path in (control.get('metadata_path'), '/var/lib/clusterguard/raft/raft.db'):
        if path and os.path.isfile(path):
            stat = os.stat(path)
            result['metadata_files'][path] = {'size_bytes': stat.st_size, 'modified_at': stat.st_mtime}
    for name in ('go', 'docker', 'rpm', 'psql'):
        result['tooling'][name] = run(['which', name])
    return result


def cleanup_check():
    return {name: run(args) for name, args in (
        ('recovery_containers', ['docker', 'ps', '-a', '--filter', 'label=clusterguard.test=recovery', '--format', '{{.ID}} {{.Names}}']),
        ('entrypoint_containers', ['docker', 'ps', '-a', '--filter', 'label=clusterguard.test=entrypoint', '--format', '{{.ID}} {{.Names}}']),
        ('recovery_networks', ['docker', 'network', 'ls', '--filter', 'name=cg-recovery-test-', '--format', '{{.ID}} {{.Name}}']),
        ('business_services', ['docker', 'service', 'ls', '--format', '{{.Name}} {{.Replicas}}']),
        ('selinux', ['getenforce']),
    )}


def main():
    mode, host = sys.argv[1:3]
    if host not in HOSTS or mode not in ('inventory', 'api', 'native', 'logs', 'bindings', 'observations', 'refresh', 'cleanupcheck'):
        raise ValueError('unapproved audit target or mode')
    result = {'inventory': inventory, 'api': lambda: api_inventory(host), 'native': native, 'logs': logs, 'bindings': bindings, 'observations': lambda: observations(host), 'refresh': lambda: refresh_timings(host), 'cleanupcheck': cleanup_check}[mode]()
    print(json.dumps(scrub({'host': host, 'hostname': socket.gethostname(), 'mode': mode,
                           'utc': datetime.datetime.utcnow().isoformat() + 'Z', 'result': result})))


if __name__ == '__main__':
    main()
