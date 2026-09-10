"""Read-only acceptance on the pinned PG primary while its recovery guard is active."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import time
import uuid

CLUSTER = '8938f553-c7ac-4589-aaf2-5d6823c4cc7e'
INSTANCE = '9cabbf9f-4971-4f01-9ce0-574606dc93bc'
HOST = '192.168.102.154'
DATA = Path('/data/clusterguard-swarm/postgresql/55432')
GUARD = Path(str(DATA) + '.clusterguard-recovery/guard.json')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--task-id', required=True, type=uuid.UUID)
    parser.add_argument('--seconds', type=int, default=300)
    args = parser.parse_args()
    if not 1 <= args.seconds <= 600:
        raise RuntimeError('invalid bounded duration')
    agent = json.loads(Path('/etc/clusterguard/agent.json').read_text())
    if not any(p['cluster_id'] == CLUSTER and p['instance_id'] == INSTANCE for p in agent['clusters']):
        raise RuntimeError('this probe is pinned to PG member 154')
    config = json.loads(Path('/etc/clusterguard/clusterguard.json').read_text())
    account = config['postgresql']['discovery']
    if account['username'] != 'postgres':
        raise RuntimeError('administrative discovery identity changed')
    values = {}
    for line in Path('/etc/clusterguard/clusterguard.env').read_text().splitlines():
        name, sep, raw = line.strip().partition('=')
        if sep and not name.startswith('#'):
            parts = shlex.split(raw)
            if len(parts) == 1:
                values[name] = parts[0]
    password = values[account['password_env']]
    env = dict(os.environ, PGPASSWORD=password, PGCONNECT_TIMEOUT='2', LC_ALL='C')
    command = ['/usr/local/bin/psql', '-X', '-w', '-A', '-t', '-h', HOST, '-p', '55432',
               '-d', 'postgres', '-v', 'ON_ERROR_STOP=1']
    deadline = time.monotonic() + args.seconds
    while time.monotonic() < deadline:
        if not GUARD.exists():
            time.sleep(0.5)
            continue
        receipt = json.loads(GUARD.read_text())
        if receipt['task_id'] != str(args.task_id) or receipt.get('released'):
            time.sleep(0.5)
            continue
        before = (DATA / 'pg_hba.conf').read_bytes()
        if hashlib.sha256(before).hexdigest() != receipt['digest']:
            raise RuntimeError('guard HBA digest mismatch')
        expected = b'host all postgres 192.168.102.154/32 scram-sha-256\n'
        if expected not in before:
            time.sleep(0.5)
            continue
        query = 'SELECT json_build_object(\'source\',host(inet_client_addr()),\'standby\',pg_is_in_recovery())'
        result = subprocess.run(command + ['-U', 'postgres', '-c', query], env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
        if result.returncode:
            time.sleep(0.5)
            continue
        state = json.loads(result.stdout)
        if state != {'source': HOST, 'standby': False}:
            raise RuntimeError('unexpected source address or primary role')
        # An existing non-admin role must be rejected by HBA before password auth.
        rejected = subprocess.run(command + ['-U', 'cg_replication', '-c', 'SELECT 1'], env=env,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
        hba_rejected = b'pg_hba.conf rejects connection' in rejected.stderr
        after = (DATA / 'pg_hba.conf').read_bytes()
        after_receipt = json.loads(GUARD.read_text())
        if not rejected.returncode or not hba_rejected or before != after or after_receipt != receipt:
            raise RuntimeError('non-admin exclusion or guard stability verification failed')
        print(json.dumps({'task_id': str(args.task_id), 'primary': HOST, 'source': state['source'],
                          'guard_active': True, 'guard_digest_verified': True,
                          'administrative_connection': 'passed', 'non_admin_hba_rejected': True}), flush=True)
        return
    raise RuntimeError('no verified guarded same-host connection in bounded interval')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Unexpected command/config errors may contain secrets; report type only.
        report = {'status': 'failed', 'error_type': type(error).__name__}
        if isinstance(error, RuntimeError):
            report['check'] = str(error)
        print(json.dumps(report), flush=True)
        raise SystemExit(1)
