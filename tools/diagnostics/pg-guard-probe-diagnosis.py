"""Read-only, bounded HBA rejection evidence from the authorized PG lab."""
import json
import os
import re
import runpy
from urllib.parse import urlparse

h = runpy.run_path(os.path.join(os.path.dirname(__file__), 'field-acceptance.py'))
control = h['api']('control-plane/status')
print(json.dumps({'control_plane': {key: control.get(key) for key in ('role', 'leader_id', 'leader_address', 'node_id')}}), flush=True)
for service, member, host in h['MEMBERS'][h['PG']]:
    controller = json.loads(h['peer'](host, ['cat', '/etc/clusterguard/clusterguard.json']))
    agent = json.loads(h['peer'](host, ['cat', '/etc/clusterguard/agent.json']))
    policy = next(p for p in agent['clusters'] if p['cluster_id'] == h['PG'])
    out = {'host': host, 'discovery_user': controller.get('postgresql', {}).get('discovery', {}).get('username'),
           'administrative_user': policy.get('postgresql_user'),
           'postgresql_hostname': policy.get('postgresql_hostname'),
           'controller_hosts': [urlparse(url).hostname for url in agent.get('controller_urls', [])],
           'peer_addresses': [p.get('ip_address') for p in policy.get('postgresql_peers', [])],
           'rejections': []}
    ids = h['peer'](host, ['docker', 'ps', '-a', '--filter', 'label=com.docker.swarm.service.name=' + service,
                         '--format', '{{.ID}}']).decode().splitlines()
    out['container_count'] = len(ids)
    data = policy['postgresql_data_directory']
    out['log_files'] = h['peer'](host, ['find', data, '-maxdepth', '2', '-type', 'f', '-name', '*.log']).decode().splitlines()
    if host == '192.168.102.154':
        out['local_route'] = json.loads(h['peer'](host, ['ip', '-j', 'route', 'get', host]))
    for container in ids[:4]:
        if not re.fullmatch(r'[0-9a-f]{12,64}', container):
            raise RuntimeError('invalid container identity')
        logs = h['peer'](host, ['sh', '-c', 'docker logs --tail 500 --since 2026-09-08T07:04:00Z ' + container + ' 2>&1']).decode()
        for line in logs.splitlines():
            if 'FATAL' not in line:
                continue
            match = re.search(r'(?:pg_hba.conf rejects connection|no pg_hba.conf entry) for host "([0-9a-fA-F:.]+)", user "([A-Za-z0-9_]+)", database "([A-Za-z0-9_]+)"', line)
            if match:
                out['rejections'].append(dict(zip(('source', 'user', 'database'), match.groups())))
    out['rejections'] = [json.loads(value) for value in sorted({json.dumps(value, sort_keys=True) for value in out['rejections']})]
    print(json.dumps(out), flush=True)
