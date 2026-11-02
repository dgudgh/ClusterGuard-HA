#!/usr/bin/env python3
"""Opt-in real-process lab. Creates new empty databases; never accepts site hosts.

Requires native clusterguard/cg-gateway binaries in ROOT/bin and MySQL 8 binaries.
All credentials, data, TLS keys and logs stay under ignored .build; no fixtures.
Run: python3 tools/gateway-real-mysql-lab.py prepare ROOT
     python3 tools/gateway-real-mysql-lab.py cleanup ROOT
"""
import concurrent.futures
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import ssl
import subprocess
import sys
import time
import urllib.request
import urllib.error
import uuid

ROOT = Path(sys.argv[2]).resolve()
LAB_BASE = Path(__file__).resolve().parent.parent / '.build'
if not ROOT.is_relative_to(LAB_BASE.resolve()) or ROOT == LAB_BASE.resolve():
    raise SystemExit('lab ROOT must be a new subdirectory of this repository .build')
MYSQL = os.environ.get('CG_TEST_MYSQL', '/opt/homebrew/bin/mysql')
MYSQLD = os.environ.get('CG_TEST_MYSQLD', '/opt/homebrew/bin/mysqld')
os.umask(0o077)


def save(name, value):
    (ROOT / name).write_text(json.dumps(value, indent=2) + '\n')


def wait(fn, timeout=60):
    deadline = time.monotonic() + timeout
    error = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except Exception as exc:
            error = exc
        time.sleep(.25)
    raise RuntimeError(f'condition timed out: {error}')


def freeport():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def sql(index, query, timeout=25):
    config = ROOT / f'mysql-{index}/client.cnf'
    p = subprocess.run([MYSQL, f'--defaults-file={config}', '-NBr'], input=query,
                       text=True, capture_output=True, timeout=timeout)
    if p.returncode:
        # Queries may contain generated passwords: never include input in errors.
        raise RuntimeError(f'MySQL {index}: {p.stderr.strip()}')
    return p.stdout.strip()


def start(name, argv, env=None):
    with (ROOT / f'{name}.log').open('ab') as log:
        proc = subprocess.Popen(argv, stdout=log, stderr=log, env=env,
                                start_new_session=True)
    records = json.loads((ROOT / 'processes.json').read_text())
    records.append({'name': name, 'pid': proc.pid, 'argv': argv})
    save('processes.json', records)
    return proc


def api(base, path, payload=None):
    secret = json.loads((ROOT / 'secrets.json').read_text())
    request = urllib.request.Request(base + path,
        data=None if payload is None else json.dumps(payload).encode(),
        headers={'Authorization': 'Bearer ' + secret['control'],
                 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=60) as r:
            return json.load(r).get('result')
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f'API {path}: {exc.code} {exc.read().decode()}') from None


def prepare():
    if (ROOT / 'processes.json').exists():
        raise RuntimeError('existing lab: preserve it; use a fresh ROOT')
    ROOT.mkdir(parents=True, exist_ok=True)
    save('processes.json', [])
    secret = {k: secrets.token_hex(16) for k in ['db', 'control']}
    secret['bootstrap'] = 'Lab-Initial-9!' + secrets.token_hex(12)
    secret['password'] = 'Lab-Changed-9!' + secrets.token_hex(12)
    save('secrets.json', secret)
    ports = set()

    def port():
        while True:
            p = freeport()
            if p not in ports:
                ports.add(p)
                return p

    dbports = [port() for _ in range(3)]
    httpports = [port() for _ in range(3)]
    raftports = [port() for _ in range(3)]
    management = [port(), port()]
    frontends = [port(), port()]
    health = [port(), port()]
    manifest = {'db_ports': dbports, 'http_ports': httpports,
                'raft_ports': raftports, 'gateway_management': management,
                'gateway_frontends': frontends, 'gateway_health': health,
                'scope': 'isolated real MySQL, 3 Raft HA processes, 2 gateways; no cloud network'}
    save('manifest.json', manifest)
    basedir = str(Path(MYSQLD).resolve().parent.parent)

    def initialize(index):
        directory = ROOT / f'mysql-{index}'
        directory.mkdir()
        data = directory / 'data'
        data.mkdir()
        subprocess.run([MYSQLD, '--no-defaults', '--initialize-insecure',
                        f'--basedir={basedir}', f'--datadir={data}'],
                       stdout=(directory / 'initialize.log').open('w'),
                       stderr=subprocess.STDOUT, check=True, timeout=120)
        # TCP only: avoids socket length limits and other installed instances.
        (directory / 'my.cnf').write_text(f'''[mysqld]
basedir={basedir}
datadir={data}
port={dbports[index]}
bind_address=127.0.0.1
socket={directory}/mysql.sock
pid_file={directory}/mysql.pid
log_error={directory}/error.log
mysqlx=OFF
server_id={11000 + index}
gtid_mode=ON
enforce_gtid_consistency=ON
log_bin={directory}/binlog
binlog_format=ROW
log_replica_updates=ON
relay_log={directory}/relay
skip_replica_start=ON
innodb_buffer_pool_size=64M
read_only={'OFF' if index == 0 else 'ON'}
super_read_only={'OFF' if index == 0 else 'ON'}
''')
        (directory / 'client.cnf').write_text(f'[client]\nuser=root\nhost=127.0.0.1\nport={dbports[index]}\nprotocol=TCP\nconnect_timeout=2\n')
        return index

    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        list(executor.map(initialize, range(3)))
    for i in range(3):
        start(f'mysql-{i}', [MYSQLD, f'--defaults-file={ROOT / f"mysql-{i}/my.cnf"}'])
    for i in range(3):
        wait(lambda: sql(i, 'SELECT 1;') == '1')
        # Local lab users only. Disable session binlog so identities are common
        # without producing independent/errant GTIDs before seeding replication.
        sql(i, f"""SET SESSION sql_log_bin=0;
SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF;
CREATE USER 'cg_lab'@'%' IDENTIFIED WITH mysql_native_password BY '{secret['db']}';
GRANT ALL PRIVILEGES ON *.* TO 'cg_lab'@'%' WITH GRANT OPTION;
CREATE USER 'cg_app'@'%' IDENTIFIED WITH mysql_native_password BY '{secret['db']}';
GRANT SELECT,INSERT ON cg_acceptance.* TO 'cg_app'@'%';
""" + ("SET GLOBAL super_read_only=ON;" if i else ''))
    sql(0, 'CREATE DATABASE cg_acceptance; CREATE TABLE cg_acceptance.writes (id VARCHAR(64) PRIMARY KEY, value BIGINT NOT NULL);')
    source_gtid = sql(0, 'SELECT @@GLOBAL.gtid_executed;')
    for i in [1, 2]:
        sql(i, f"CHANGE REPLICATION SOURCE TO SOURCE_HOST='127.0.0.1', SOURCE_PORT={dbports[0]}, SOURCE_USER='cg_lab', SOURCE_PASSWORD='{secret['db']}', SOURCE_AUTO_POSITION=1; START REPLICA;")
        assert sql(i, f"SELECT WAIT_FOR_EXECUTED_GTID_SET('{source_gtid}',20);") == '0'
    print('PASS: three real MySQL servers and GTID replication', flush=True)
    tls = ROOT / 'tls'
    tls.mkdir()
    # Short-lived loopback-only lab certificates, not deployment trust material.
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(tls / 'key.pem'), '-out', str(tls / 'cert.pem'),
                    '-days', '1', '-subj', '/CN=lab-controller',
                    '-addext', 'subjectAltName=IP:127.0.0.1',
                    '-addext', 'extendedKeyUsage=serverAuth,clientAuth'],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    tlsfiles = {'cert_file': str(tls / 'cert.pem'), 'key_file': str(tls / 'key.pem'),
                'ca_file': str(tls / 'cert.pem')}
    ids = [str(uuid.uuid4()) for _ in range(3)]
    peers = [{'resource_id': ids[i], 'address': f'127.0.0.1:{raftports[i]}',
              'api_address': f'http://127.0.0.1:{httpports[i]}'} for i in range(3)]
    env = dict(os.environ, CG_LAB_DB=secret['db'], CG_LAB_CONTROL=secret['control'],
               CG_LAB_BOOTSTRAP=secret['bootstrap'])
    env['PATH'] = str(Path(MYSQL).parent) + ':' + env.get('PATH', '')
    for i in range(3):
        directory = ROOT / f'ha-{i}'
        directory.mkdir()
        config = {'http_address': f'127.0.0.1:{httpports[i]}',
            'metadata_path': str(directory / 'metadata.json'),
            'control_token_env': 'CG_LAB_CONTROL',
            'bootstrap_admin_password_env': 'CG_LAB_BOOTSTRAP',
            'consensus': dict(enabled=True, snapshot_cas_enabled=True,
                replicated_log_compression_enabled=True, local_id=ids[i],
                bind_address=peers[i]['address'], advertise_address=peers[i]['address'],
                data_directory=str(directory / 'raft'), bootstrap=i == 0,
                apply_timeout_seconds=10, peers=peers,
                tls_cert_file=tlsfiles['cert_file'], tls_key_file=tlsfiles['key_file'],
                tls_ca_file=tlsfiles['ca_file']),
            'mysql': dict(enabled=True, semi_sync_required=False,
                discovery_interval_seconds=1, discovery_timeout_seconds=3,
                automatic_failover_enabled=False,
                **{k: {'username': 'cg_lab', 'password_env': 'CG_LAB_DB'}
                   for k in ['discovery', 'operation', 'replication']}),
            'gateway': {'enabled': True, 'profiles': {'lab': {
                'management_urls': [f'https://127.0.0.1:{p}' for p in management],
                **tlsfiles}}}}
        save(f'ha-{i}/config.json', config)
        start(f'ha-{i}', [str(ROOT / 'bin/clusterguard'), '--config', str(directory / 'config.json')], env)
    bases = [f'http://127.0.0.1:{p}' for p in httpports]
    cid = str(uuid.uuid4())

    def register():
        for base in bases:
            try:
                control = api(base, '/api/v1/control-plane/status')
                if not control['mutation_authority'] or control['role'] != 'leader':
                    continue
                api(base, '/api/v1/clusters', {'resource_id': cid, 'engine': 'mysql',
                    'display_name': 'real-mysql-gateway-lab',
                    'endpoints': [{'ip_address': '127.0.0.1', 'port': p} for p in dbports]})
                return base
            except Exception:
                pass

    base = wait(register)
    manifest.update(cluster_id=cid, base_url=base)
    save('manifest.json', manifest)
    api(base, f'/api/v1/clusters/{cid}/discover', {})
    topology = api(base, f'/api/v1/clusters/{cid}/topology')
    save('initial-topology.json', topology)
    instances = topology['instances']
    assert len(instances) == 3, f'expected three discovered real instances, got {len(instances)}'
    backend = {x['resource_id']: f'127.0.0.1:{x["port"]}' for x in instances}
    primary = next(x for x in instances if x['role'] == 'primary')
    manifest['instances'] = instances
    save('manifest.json', manifest)
    for i in range(2):
        save(f'gateway-{i}.json', {'management_listen': f'127.0.0.1:{management[i]}',
            'health_listen': f'127.0.0.1:{health[i]}', 'tls': tlsfiles,
            'allowed_controller_names': ['lab-controller'],
            'state_directory': str(ROOT / f'gateway-state-{i}'),
            'max_grant_ttl_seconds': 30,
            'clusters': [{'id': cid, 'listen': f'127.0.0.1:{frontends[i]}', 'backends': backend}]})
        start(f'gateway-{i}', [str(ROOT / 'bin/cg-gateway'), '--config', str(ROOT / f'gateway-{i}.json')])
    bind()


def bind():
    manifest = json.loads((ROOT / 'manifest.json').read_text())
    base, cid = manifest['base_url'], manifest['cluster_id']
    primary = next(x for x in manifest['instances'] if x['role'] == 'primary')
    api(base, f'/api/v1/clusters/{cid}/ha-endpoints', {
        'kind': 'service', 'provider': 'gateway', 'provider_ref': 'lab',
        'hostname': '127.0.0.1', 'ip_address': '127.0.0.1',
        'port': manifest['gateway_frontends'][0],
        'owner_id': primary['resource_id'], 'active': True})
    wait(lambda: api(base, f'/api/v1/clusters/{cid}/gateway')['ready'], 45)
    save('initial-gateway.json', api(base, f'/api/v1/clusters/{cid}/gateway'))
    print('PASS: actual discovery, real Raft majority lease and dual authenticated gateway routes', flush=True)


def cleanup():
    # Only PIDs recorded by this invocation, checked for our absolute lab path.
    stopped = []
    for process in reversed(json.loads((ROOT / 'processes.json').read_text())):
        try:
            command = subprocess.check_output(['ps', '-p', str(process['pid']), '-o', 'command='], text=True)
            if str(ROOT) not in command:
                raise RuntimeError('PID identity changed; refusing to stop it')
            os.kill(process['pid'], signal.SIGTERM)
            stopped.append(process['pid'])
        except (ProcessLookupError, subprocess.CalledProcessError):
            pass
    def exited():
        for pid in stopped:
            state = subprocess.run(['ps', '-p', str(pid), '-o', 'stat='],
                                   text=True, capture_output=True).stdout.strip()
            if state and not state.startswith('Z'):
                return False
        return True

    wait(exited, 30)
    print('Verified recorded lab processes exited; data and evidence retained.')


def snapshot():
    states = []
    for i in range(3):
        states.append({'node': i,
            'identity_read_only_gtid': sql(i, 'SELECT @@server_uuid,@@read_only,@@super_read_only,@@GLOBAL.gtid_executed;'),
            'rows': sql(i, 'SELECT COUNT(*) FROM cg_acceptance.writes;'),
            'replication_io': sql(i, 'SELECT SERVICE_STATE,LAST_ERROR_NUMBER FROM performance_schema.replication_connection_status;'),
            'replication_sql': sql(i, 'SELECT SERVICE_STATE,LAST_ERROR_NUMBER FROM performance_schema.replication_applier_status_by_worker;')})
    gtids = [x['identity_read_only_gtid'].split('\t', 3)[3] for x in states]
    assert len(set(gtids)) == 1, 'real GTID sets differ'
    assert len({x['rows'] for x in states}) == 1, 'real row counts differ'
    assert sum('\t0\t0\t' in x['identity_read_only_gtid'] for x in states) == 1
    for state in states:
        if '\t1\t1\t' in state['identity_read_only_gtid']:
            assert state['replication_io'] == 'ON\t0', 'replica IO not healthy'
            assert state['replication_sql'] and all(x == 'ON\t0' for x in state['replication_sql'].splitlines()), 'replica SQL not healthy'
    save('final-real-mysql.json', states)
    print('PASS: real GTIDs, row counts, unique writer and replica IO/SQL')


def restart_ha():
    for process in json.loads((ROOT / 'processes.json').read_text()):
        if process['name'].startswith('ha-'):
            try:
                command = subprocess.check_output(['ps', '-p', str(process['pid']), '-o', 'command='], text=True)
                if str(ROOT) not in command:
                    raise RuntimeError('PID identity changed')
                os.kill(process['pid'], signal.SIGTERM)
            except (ProcessLookupError, subprocess.CalledProcessError):
                pass
    time.sleep(3)
    secret = json.loads((ROOT / 'secrets.json').read_text())
    env = dict(os.environ, CG_LAB_DB=secret['db'], CG_LAB_CONTROL=secret['control'],
               CG_LAB_BOOTSTRAP=secret['bootstrap'])
    env['PATH'] = str(Path(MYSQL).parent) + ':' + env.get('PATH', '')
    for i in range(3):
        start(f'ha-{i}', [str(ROOT / 'bin/clusterguard'), '--config', str(ROOT / f'ha-{i}/config.json')], env)
    manifest = json.loads((ROOT / 'manifest.json').read_text())

    def leader():
        for port in manifest['http_ports']:
            base = f'http://127.0.0.1:{port}'
            try:
                if api(base, '/api/v1/control-plane/status')['mutation_authority']:
                    return base
            except Exception:
                pass

    manifest['base_url'] = wait(leader)
    save('manifest.json', manifest)


def safety():
    manifest = json.loads((ROOT / 'manifest.json').read_text())
    cid, base = manifest['cluster_id'], manifest['base_url']
    context = ssl.create_default_context(cafile=str(ROOT / 'tls/cert.pem'))
    context.load_cert_chain(str(ROOT / 'tls/cert.pem'), str(ROOT / 'tls/key.pem'))

    def statuses():
        result = []
        for port in manifest['gateway_management']:
            with urllib.request.urlopen(f'https://127.0.0.1:{port}/v1/status?cluster_id={cid}',
                                        context=context, timeout=3) as response:
                result.append(json.load(response))
        return result

    before = statuses()
    assert all(x['active'] for x in before), 'routes must be active before authority fault'
    time.sleep(6)
    after = statuses()
    for old, new in zip(before, after):
        assert new['active'] and new['last_grant']['sequence'] > old['last_grant']['sequence']
        assert new['last_grant']['target_instance_id'] == old['last_grant']['target_instance_id']
    result = {'steady_renewal_before': before, 'steady_renewal_after': after}
    save('real-safety-result.json', result)
    print('PASS: stable real majority-backed grant renewal', flush=True)
    processes = json.loads((ROOT / 'processes.json').read_text())
    latest = {x['name']: x for x in processes if x['name'].startswith('ha-')}
    stopped = []
    for i, port in enumerate(manifest['http_ports']):
        status = api(f'http://127.0.0.1:{port}', '/api/v1/control-plane/status')
        if status['role'] == 'leader':
            base = f'http://127.0.0.1:{port}'
            continue
        process = latest[f'ha-{i}']
        command = subprocess.check_output(['ps', '-p', str(process['pid']), '-o', 'command='], text=True)
        assert str(ROOT) in command, 'PID identity changed'
        os.kill(process['pid'], signal.SIGTERM)
        stopped.append(i)
    assert len(stopped) == 2, 'expected one leader and two followers'
    started = time.monotonic()
    timeline = []
    while True:
        current = statuses()
        timeline.append({'elapsed_seconds': time.monotonic() - started,
                         'active': [x['active'] for x in current]})
        if not any(x['active'] for x in current):
            break
        if time.monotonic() - started > 20:
            raise RuntimeError('routes remained active beyond majority grant TTL')
        time.sleep(.25)
    control = api(base, '/api/v1/control-plane/status')
    assert not control['mutation_authority'] and not control['quorum_confirmed']
    # mysql client accepts configuration from the actual browser test; no replay.
    blocked = []
    for port in manifest['gateway_frontends']:
        client = ROOT / f'client-{port}-cg_app.cnf'
        p = subprocess.run([MYSQL, f'--defaults-file={client}', '-NBr', '--skip-reconnect'],
                           input='SELECT 1;', text=True, capture_output=True, timeout=5)
        assert p.returncode != 0, 'gateway accepted SQL after authority expiry'
        blocked.append({'port': port, 'returncode': p.returncode, 'error': p.stderr.strip()})
    result.update(passed=True, authority_loss_timeline=timeline,
                  seconds_to_all_routes_closed=timeline[-1]['elapsed_seconds'],
                  control_plane_after_loss=control, blocked_connections=blocked,
                  scope='real routing authority expiry; no Agent process, no database local fencing claim')
    save('real-safety-result.json', result)
    print('PASS: real quorum loss closes both routes and rejects SQL: ' +
          str(round(result['seconds_to_all_routes_closed'], 3)) + ' seconds', flush=True)


if __name__ == '__main__':
    if sys.argv[1] == 'prepare':
        prepare()
    elif sys.argv[1] == 'cleanup':
        cleanup()
    elif sys.argv[1] == 'bind':
        bind()
    elif sys.argv[1] == 'restart-ha':
        restart_ha()
    elif sys.argv[1] == 'safety':
        safety()
    elif sys.argv[1] == 'snapshot':
        snapshot()
    else:
        raise SystemExit('expected prepare or cleanup')
