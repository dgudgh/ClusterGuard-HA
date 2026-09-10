"""Read-only topology/phase trace for the explicitly authorized recovery lab."""
import argparse
import datetime
import json
import os
import runpy
import time

parser = argparse.ArgumentParser()
parser.add_argument('--engine', choices=['mysql', 'pg'], default='mysql')
parser.add_argument('--seconds', type=int, default=180)
parser.add_argument('--until-terminal', action='store_true')
args = parser.parse_args()
if not 1 <= args.seconds <= 900:
    raise RuntimeError('trace duration must be between 1 and 900 seconds')
helpers = runpy.run_path(os.path.join(os.path.dirname(__file__), 'field-acceptance.py'))
cluster = helpers['MYSQL' if args.engine == 'mysql' else 'PG']
deadline = time.monotonic() + args.seconds
last = None
active_task = None
while time.monotonic() < deadline:
    topology = helpers['api']('clusters/' + cluster + '/topology')
    status = helpers['api']('clusters/' + cluster + '/recovery/status')
    task = status['tasks'][0]
    if task.get('stage') not in ('planned', 'succeeded', 'blocked'):
        active_task = task['resource_id']
    key = (topology.get('observed_at'), task.get('stage'), task.get('metadata_revision'))
    if key != last:
        print(json.dumps({'at': datetime.datetime.utcnow().isoformat() + 'Z',
            'observed_at': topology.get('observed_at'), 'health': topology.get('health'),
            'links': len(topology.get('links', [])),
            'members': [{'id': i['resource_id'], 'role': i['role'], 'health': i.get('health'),
                         'read_only': i.get('engine_metadata', {}).get('read_only'),
                         'super_read_only': i.get('engine_metadata', {}).get('super_read_only'),
                         'lag': i.get('replication', {}).get('lag_seconds')}
                        for i in topology.get('instances', [])],
            'task': {k: task.get(k) for k in ('resource_id','stage','message','committed_at','verified_at','updated_at')}}), flush=True)
        last = key
    if args.until_terminal and task['resource_id'] == active_task and task.get('stage') in ('succeeded', 'blocked'):
        print(json.dumps({'summary': True, 'task_id': active_task, 'stage': task['stage']}), flush=True)
        break
    time.sleep(0.1)
