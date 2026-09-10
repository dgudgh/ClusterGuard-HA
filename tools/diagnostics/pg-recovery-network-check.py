"""Read-only connectivity evidence during the authorized PG recovery test."""
import json
import re
import runpy
import subprocess
import time

m = runpy.run_path('/var/tmp/clusterguard-recovery-qa.9G6WJ7/field-acceptance.py')
with open('/etc/clusterguard/agent.json') as f:
    p = next(p for p in json.load(f)['clusters'] if p['cluster_id'] == m['PG'])
service = next(s for s in m['services'](m['PG']) if s['Spec']['Name'] == 'cgpg16_postgresql03')
image = service['Spec']['TaskTemplate']['ContainerSpec']['Image']
deadline = time.monotonic() + 300
while time.monotonic() < deadline:
    containers = m['peer']('192.168.102.154', ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=cgpg16_postgresql03', '--format', '{{.ID}}']).decode().splitlines()
    if len(containers) == 1:
        args = ['docker', 'run', '--rm', '--network=host', '--user=postgres', '--mount',
                'type=bind,src=' + p['postgresql_passfile'] + ',dst=/run/recovery-check.pass,readonly',
                '--env', 'PGPASSFILE=/run/recovery-check.pass', '--env', 'PGCONNECT_TIMEOUT=2',
                '--entrypoint', p['postgresql_binary_directory'] + '/psql', image,
                '-X', '-A', '-t', '-w', '-h', '192.168.102.154', '-p', '55432', '-U', 'postgres', '-d', 'postgres', '-c', 'SELECT 1']
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
        diagnostic = result.stderr.decode('utf-8', 'replace').replace(p['postgresql_passfile'], '[REDACTED]').replace('/run/recovery-check.pass', '[REDACTED]')
        diagnostic = re.sub(r'(?i)(password\s*=\s*)\S+', r'\1[REDACTED]', diagnostic)
        print(json.dumps({'time': time.time(), 'connection_succeeded': result.returncode == 0, 'diagnostic': diagnostic[:2000]}), flush=True)
        if result.returncode == 0 or 'FATAL:' in diagnostic or 'no password supplied' in diagnostic:
            break
    time.sleep(0.5)
else:
    raise RuntimeError('no primary startup observed during the read-only diagnostic window')
