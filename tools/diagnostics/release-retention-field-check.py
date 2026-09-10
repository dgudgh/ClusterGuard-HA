"""Read-only installed-version and audit-retention evidence for the authorized lab."""
import argparse
import json
from pathlib import Path
import runpy
import socket

helper = runpy.run_path(str(Path(__file__).with_name('field-acceptance.py')))
probe = r'''
import hashlib, json, pathlib, subprocess
result = {'rpm': subprocess.check_output(['rpm', '-q', 'clusterguard-ha'], universal_newlines=True).strip(), 'directories': {}}
for root in ('/var/lib/clusterguard/updates', '/var/lib/clusterguard/update-history'):
    if not pathlib.Path(root).is_dir(): continue
    for directory in pathlib.Path(root).iterdir():
        if directory.is_symlink() or not directory.is_dir(): continue
        files = {}
        for name in ('package.json', 'status.json', 'events.jsonl', 'output.log'):
            path = directory / name
            if path.is_file() and not path.is_symlink():
                data = path.read_bytes()
                files[name] = {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
        payloads = sorted(p.name for p in directory.iterdir() if p.is_file() and not p.is_symlink() and (p.name in ('package.cgpatch', 'package.cgupgrade') or p.suffix in ('.rpm', '.tgz') or p.name.endswith('.tar.gz')))
        result['directories'][str(directory)] = {'audit': files, 'payloads': payloads, 'pruned': (directory / 'artifacts-pruned').is_file()}
print(json.dumps(result))
'''

parser = argparse.ArgumentParser()
parser.add_argument('--version', required=True)
args = parser.parse_args()
if socket.gethostname() != 'orch-mysql02':
    raise RuntimeError('field probe is restricted to the authorized host')
results = {}
for host in ('192.168.102.152', '192.168.102.153', '192.168.102.154'):
    result = json.loads(helper['peer'](host, ['python3', '-c', probe]))
    if result['rpm'] != 'clusterguard-ha-' + args.version + '.x86_64':
        raise RuntimeError('installed release mismatch at ' + host)
    results[host] = result
print(json.dumps(results, indent=2))
