"""Read-only, allowlisted database continuity checks around a console upgrade."""
import argparse
import datetime
import json
import os
import runpy
import time


def snapshot(helpers):
    result = {'captured_at': datetime.datetime.utcnow().isoformat() + 'Z', 'clusters': []}
    for cluster in (helpers['MYSQL'], helpers['PG']):
        helpers['services'](cluster)
        topology = helpers['api']('clusters/' + cluster + '/topology')
        instances = topology.get('instances', [])
        links = topology.get('links', [])
        if topology.get('health', {}).get('state') != 'healthy' or len(instances) != 3 or len(links) != 2:
            raise RuntimeError('database topology is not healthy: ' + cluster)
        if any(i.get('health', {}).get('state') != 'healthy' for i in instances) or any(not link.get('healthy') for link in links):
            raise RuntimeError('unhealthy database member or replication link: ' + cluster)
        primary = [i for i in instances if i.get('role') == 'primary']
        if len(primary) != 1:
            raise RuntimeError('database does not have exactly one primary: ' + cluster)
        entry = {'id': cluster, 'observed_at': topology['observed_at'],
                 'primary_native_id': primary[0]['engine_identity']['server_uuid' if cluster == helpers['MYSQL'] else 'resource_id'],
                 'roles': sorted((i['resource_id'], i['role']) for i in instances),
                 'links': sorted((link['source_instance_id'], link['target_instance_id']) for link in links),
                 'containers': [], 'vip_owners': []}
        vip = '192.168.102.156' if cluster == helpers['MYSQL'] else '192.168.102.157'
        for service, member, host in helpers['MEMBERS'][cluster]:
            ids = helpers['peer'](host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + service, '--format', '{{.ID}}']).decode().splitlines()
            if len(ids) != 1:
                raise RuntimeError('expected exactly one database container: ' + service)
            # Return only identity and lifecycle fields, never container environment.
            state = json.loads(helpers['peer'](host, ['docker', 'inspect', '--format', '{{json .State}}', ids[0]]))
            if not state.get('Running'):
                raise RuntimeError('database container is not running: ' + service)
            entry['containers'].append({'host': host, 'member': member, 'id': ids[0], 'started_at': state['StartedAt']})
            addresses = json.loads(helpers['peer'](host, ['ip', '-j', 'address', 'show']))
            if any(a.get('local') == vip for interface in addresses for a in interface.get('addr_info', [])):
                entry['vip_owners'].append(host)
        if entry['vip_owners'] != [primary[0]['ip_address']]:
            raise RuntimeError('VIP does not have exactly the primary as its owner: ' + cluster)
        result['clusters'].append(entry)
    return result


def monitor(helpers, baseline, seconds):
    clients = []
    for entry in baseline['clusters']:
        cluster = entry['id']
        service = next(name for name, _, host in helpers['MEMBERS'][cluster] if host == '192.168.102.153')
        ids = helpers['peer']('192.168.102.153', ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + service, '--format', '{{.ID}}']).decode().splitlines()
        if len(ids) != 1:
            raise RuntimeError('monitor client container is not unique')
        if cluster == helpers['MYSQL']:
            command = ['docker', 'exec', ids[0], 'mysql', '--defaults-extra-file=/run/secrets/clusterguard-operation.cnf',
                       '--connect-timeout=3', '-h', '192.168.102.156', '-P', '3306', '--batch', '--raw', '--skip-column-names',
                       '--execute', "SELECT JSON_OBJECT('native_id',@@server_uuid,'writer',@@read_only=0 AND @@super_read_only=0)"]
        else:
            command = ['docker', 'exec', '-u', 'postgres', '-e', 'PGPASSFILE=/etc/clusterguard/postgresql/55432.pass',
                       '-e', 'PGCONNECT_TIMEOUT=3', '-e', 'PGOPTIONS=-c statement_timeout=3000', ids[0],
                       '/usr/lib/postgresql/16/bin/psql', '-h', '192.168.102.157', '-p', '55432', '-U', 'postgres', '-d', 'postgres',
                       '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-c',
                       "SELECT json_build_object('native_id',current_setting('clusterguard.node_id'),'writer',NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off')"]
        clients.append((entry, command))
    deadline = time.monotonic() + seconds
    samples, failures = 0, 0
    while time.monotonic() < deadline:
        for entry, command in clients:
            result = {'at': datetime.datetime.utcnow().isoformat() + 'Z', 'cluster': entry['id'], 'ok': False}
            try:
                native = json.loads(helpers['peer']('192.168.102.153', command))
                result['ok'] = native['native_id'] == entry['primary_native_id'] and bool(native['writer'])
            except Exception:
                # Never expose client stderr, container environment or credentials.
                result['error'] = 'VIP SQL query failed'
            failures += int(not result['ok'])
            samples += 1
            print(json.dumps(result), flush=True)
        time.sleep(0.5)
    print(json.dumps({'summary': True, 'samples': samples, 'failures': failures}), flush=True)
    if failures:
        raise RuntimeError('database VIP continuity failed')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['baseline', 'verify', 'monitor'])
    parser.add_argument('--receipt', required=True)
    parser.add_argument('--seconds', type=int, default=180)
    args = parser.parse_args()
    helpers = runpy.run_path(os.path.join(os.path.dirname(__file__), 'field-acceptance.py'))
    if args.action == 'monitor':
        if not 1 <= args.seconds <= 900:
            raise RuntimeError('monitor duration must be between 1 and 900 seconds')
        with open(args.receipt) as handle:
            monitor(helpers, json.load(handle), args.seconds)
        return
    current = snapshot(helpers)
    if args.action == 'baseline':
        with open(args.receipt, 'x') as handle:
            json.dump(current, handle, indent=2)
        os.chmod(args.receipt, 0o600)
    else:
        with open(args.receipt) as handle:
            before = json.load(handle)
        # Round-trip normalizes tuples from the freshly collected observation.
        current = json.loads(json.dumps(current))
        for original, observed in zip(before['clusters'], current['clusters']):
            if observed['observed_at'] <= original['observed_at']:
                raise RuntimeError('topology has not refreshed after the upgrade')
            for key in ('id', 'primary_native_id', 'roles', 'links', 'containers', 'vip_owners'):
                if original[key] != observed[key]:
                    raise RuntimeError('database continuity changed: ' + original['id'] + ' / ' + key)
        if len(before['clusters']) != len(current['clusters']):
            raise RuntimeError('cluster inventory changed')
    print(json.dumps({'action': args.action, 'passed': True, 'snapshot': current}, indent=2))


if __name__ == '__main__':
    main()
