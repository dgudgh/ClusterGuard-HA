"""Pinned lab credential inventory and rotation; secret values never reach output."""
import argparse
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import runpy
import secrets
import shlex
import subprocess
import sys
import time
import uuid

HELPER = runpy.run_path(str(Path(__file__).with_name('field-acceptance.py')))
PG = HELPER['PG']
HOSTS = [item[2] for item in HELPER['MEMBERS'][PG]]
PASSFILE = '/etc/clusterguard/postgresql/55432.pass'


def peer(host, args, data=None):
    if host != '192.168.102.153':
        args = ['ssh', '-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                '-o', 'UserKnownHostsFile=/etc/clusterguard/ssh/controller_known_hosts',
                '-i', '/etc/clusterguard/ssh/controller_ed25519', 'root@' + host,
                ' '.join(shlex.quote(arg) for arg in args)]
    result = subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40)
    if result.returncode:
        raise RuntimeError('allowlisted secret operation failed; command output withheld')
    return result.stdout


def sql(host, statement):
    service = next(name for name, _, address in HELPER['MEMBERS'][PG] if address == host)
    containers = peer(host, ['docker', 'ps', '--filter', 'label=com.docker.swarm.service.name=' + service,
                             '--format', '{{.ID}}']).decode().splitlines()
    if len(containers) != 1:
        raise RuntimeError('PG member has no unique live container')
    return peer(host, ['docker', 'exec', '-i', '-u', 'postgres', containers[0],
                       '/usr/lib/postgresql/16/bin/psql', '-U', 'postgres', '-d', 'postgres',
                       '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1'], statement.encode()).decode().strip()


def pass_fields(line):
    fields, field, escaped = [], '', False
    for char in line:
        if escaped:
            field += char
            escaped = False
        elif char == '\\':
            escaped = True
        elif char == ':':
            fields.append(field)
            field = ''
        else:
            field += char
    fields.append(field)
    if escaped or len(fields) != 5:
        raise RuntimeError('unqualified passfile record')
    return fields


def inventory():
    topology = HELPER['api']('clusters/' + PG + '/topology')
    primary = [i['ip_address'] for i in topology['instances'] if i.get('role') == 'primary']
    if len(primary) != 1 or topology['health']['state'] != 'healthy' or len(topology['links']) != 2:
        raise RuntimeError('rotation requires a healthy PG cluster')
    files = [peer(host, ['cat', PASSFILE]) for host in HOSTS]
    if len(set(files)) != 1:
        raise RuntimeError('replication passfiles differ across members')
    passwords = {pass_fields(line)[4] for line in files[0].decode().splitlines()
                 if line and not line.startswith('#') and pass_fields(line)[3] == 'cg_replication'}
    if len(passwords) != 1:
        raise RuntimeError('replication credential identity is ambiguous')
    with open('/etc/clusterguard/clusterguard.json') as stream:
        config = json.load(stream)
    variable = config['postgresql']['replication']['password_env']
    values = {}
    with open('/etc/clusterguard/clusterguard.env') as stream:
        for line in stream:
            name, sep, value = line.strip().partition('=')
            if sep and not name.startswith('#'):
                pieces = shlex.split(value)
                if len(pieces) == 1:
                    values[name] = pieces[0]
    roles = json.loads(sql(primary[0], "SELECT json_agg(json_build_object('name',rolname,'replication',rolreplication,'login',rolcanlogin)) FROM pg_roles WHERE rolname IN ('cg_replication','clusterguard_repl');"))
    report = {'primary': primary[0], 'roles': roles, 'passfiles_consistent': True,
              'controller_replication_user': config['postgresql']['replication']['username'],
              'controller_shares_exposed_secret': values.get(variable) in passwords}
    return report, files[0]


def replace_pinned_file(host, path, original, replacement):
    # Keep existing bind-mounted passfile inodes, preserve uid/mode, and refuse
    # concurrent edits. Payload travels only over SSH stdin, never argv/logs.
    program = """import base64,fcntl,json,os,stat,sys
p=json.load(sys.stdin)
if p['path'] not in ['/etc/clusterguard/postgresql/55432.pass','/etc/clusterguard/clusterguard.json','/etc/clusterguard/clusterguard.env']:
 raise RuntimeError('path not allowlisted')
fd=os.open(p['path'],os.O_RDWR|os.O_NOFOLLOW)
try:
 fcntl.flock(fd,fcntl.LOCK_EX)
 info=os.fstat(fd)
 if not stat.S_ISREG(info.st_mode) or info.st_size>1048576: raise RuntimeError('invalid file')
 old=os.read(fd,1048577)
 if old!=base64.b64decode(p['old']): raise RuntimeError('concurrent configuration edit')
 new=base64.b64decode(p['new'])
 os.lseek(fd,0,os.SEEK_SET)
 offset=0
 while offset<len(new): offset+=os.write(fd,new[offset:])
 os.ftruncate(fd,len(new)); os.fsync(fd)
finally: os.close(fd)
"""
    payload = json.dumps({'path': path, 'old': base64.b64encode(original).decode(),
                          'new': base64.b64encode(replacement).decode()}).encode()
    peer(host, ['python3', '-c', program], payload)


def rotate():
    report, original = inventory()
    if report['controller_shares_exposed_secret'] or report['roles'] != [{'name':'cg_replication','replication':True,'login':True}]:
        raise RuntimeError('credential inventory changed; rotation requires review')
    root = Path('/etc/clusterguard/rotation-pg-20260908')
    root.mkdir(mode=0o700, exist_ok=True)
    if root.is_symlink() or root.stat().st_mode & 0o777 != 0o700 or root.stat().st_uid != 0:
        raise RuntimeError('rotation state must be a root-owned private directory')
    state_path = root / 'state.json'
    if state_path.exists():
        raise RuntimeError('rotation receipt already exists; inspect before retrying')
    password = secrets.token_urlsafe(36)
    salt = secrets.token_bytes(16)
    salted = hashlib.pbkdf2_hmac('sha256', password.encode(), salt, 4096)
    client = hmac.new(salted, b'Client Key', hashlib.sha256).digest()
    stored = hashlib.sha256(client).digest()
    server = hmac.new(salted, b'Server Key', hashlib.sha256).digest()
    b64 = lambda value: base64.b64encode(value).decode()
    verifier = 'SCRAM-SHA-256$4096:' + b64(salt) + '$' + b64(stored) + ':' + b64(server)
    state = {'cluster': PG, 'primary': report['primary'], 'username':'cg_replication',
             'password': password, 'stage':'prepared', 'updated_hosts':[]}
    def save():
        fd = os.open(str(state_path), os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as stream:
            json.dump(state, stream)
            stream.flush()
            os.fsync(stream.fileno())
    save()
    lines = []
    for line in original.decode().splitlines():
        if not line or line.startswith('#'):
            lines.append(line)
            continue
        fields = pass_fields(line)
        if fields[3] == 'cg_replication':
            fields[4] = password
        lines.append(':'.join(field.replace('\\','\\\\').replace(':','\\:') for field in fields))
    replacement = ('\n'.join(lines) + '\n').encode()
    for host in HOSTS:
        replace_pinned_file(host, PASSFILE, original, replacement)
        state['updated_hosts'].append(host)
        save()
    # Only the SCRAM verifier crosses SQL; no plaintext PASSWORD statement.
    sql(report['primary'], "ALTER ROLE cg_replication PASSWORD '" + verifier + "';")
    state['stage'] = 'database_password_rotated'
    save()
    for host in HOSTS:
        config_path = '/etc/clusterguard/clusterguard.json'
        env_path = '/etc/clusterguard/clusterguard.env'
        old_config = peer(host, ['cat', config_path])
        config = json.loads(old_config)
        variable = config['postgresql']['replication']['password_env']
        if variable != 'CG_POSTGRESQL_REPLICATION_PASSWORD':
            raise RuntimeError('controller credential mapping changed')
        config['postgresql']['replication']['username'] = 'cg_replication'
        old_env = peer(host, ['cat', env_path])
        env_lines, replaced = [], 0
        for line in old_env.decode().splitlines():
            if line.startswith(variable + '='):
                line = variable + '=' + password
                replaced += 1
            env_lines.append(line)
        if replaced != 1:
            raise RuntimeError('controller environment mapping is ambiguous')
        replace_pinned_file(host, env_path, old_env, ('\n'.join(env_lines)+'\n').encode())
        replace_pinned_file(host, config_path, old_config, (json.dumps(config,indent=2)+'\n').encode())
    state['stage'] = 'files_and_database_rotated_controller_reload_pending'
    save()
    # Reconnect one allowlisted sender at a time to prove the mounted passfile
    # supplies the new credential; no database restart or VIP change is used.
    members = HELPER['api']('clusters/' + PG + '/topology')['instances']
    for member in members:
        if member['ip_address'] == report['primary']:
            continue
        native = member['engine_identity']['resource_id']
        before = json.loads(sql(report['primary'], "SELECT json_agg(pid) FROM pg_stat_replication WHERE application_name='"+native+"';"))
        if not before or len(before) != 1:
            raise RuntimeError('replication sender identity is ambiguous')
        sql(report['primary'], "SELECT pg_terminate_backend(pid) FROM pg_stat_replication WHERE application_name='"+native+"';")
        deadline = time.monotonic() + 30
        while True:
            current = json.loads(sql(report['primary'], "SELECT json_agg(pid) FROM pg_stat_replication WHERE application_name='"+native+"' AND state='streaming';"))
            if current and len(current) == 1 and current != before:
                break
            if time.monotonic() > deadline:
                raise RuntimeError('rotated credential did not establish a fresh streaming connection')
            time.sleep(1)
    state['stage'] = 'new_secret_streaming_verified_controller_reload_pending'
    save()
    print(json.dumps({'result':'replication secret rotated; both replicas freshly reconnected',
                      'controller_user':'cg_replication','controller_reload':'pending signed rolling upgrade',
                      'receipt_directory':str(root)}))


def prepare_retest():
    report, _ = inventory()
    state_path = Path('/etc/clusterguard/rotation-pg-20260908/state.json')
    state = json.loads(state_path.read_text())
    if state['stage'] != 'new_secret_streaming_verified_controller_reload_pending':
        raise RuntimeError('unexpected rotation stage')
    inventory_services = HELPER['services'](PG)
    refs = []
    for service in inventory_services:
        matches = [item for item in service['Spec']['TaskTemplate']['ContainerSpec']['Secrets']
                   if item.get('File',{}).get('Name') == 'postgres_replication_password']
        if len(matches) != 1:
            raise RuntimeError('bootstrap replication secret is ambiguous')
        refs.append(matches[0])
    primary_service = next(name for name, _, host in HELPER['MEMBERS'][PG] if host == report['primary'])
    container = peer(report['primary'], ['docker','ps','--filter','label=com.docker.swarm.service.name='+primary_service,'--format','{{.ID}}']).decode().strip()
    check = 'PGPASSWORD="$(tr -d "\\r\\n" </run/secrets/postgres_replication_password)"; export PGPASSWORD; /usr/lib/postgresql/16/bin/psql "host=127.0.0.1 port=5432 user=cg_replication replication=true dbname=replication connect_timeout=5" -X -A -t -c IDENTIFY_SYSTEM >/dev/null 2>/tmp/cg-old-secret-check; result=$?; if [ "$result" -ne 0 ] && grep -q "password authentication failed" /tmp/cg-old-secret-check; then rm /tmp/cg-old-secret-check; exit 0; fi; rm /tmp/cg-old-secret-check; exit 1'
    peer(report['primary'], ['docker','exec','-u','postgres',container,'sh','-c',check])
    marker = str(uuid.uuid4())
    table = 'cg_recovery_acceptance.recovery_markers'
    sql(report['primary'], "CREATE SCHEMA IF NOT EXISTS cg_recovery_acceptance; CREATE TABLE IF NOT EXISTS "+table+" (id text PRIMARY KEY, note text NOT NULL); INSERT INTO "+table+" VALUES ('"+marker+"','before final signed-release recovery acceptance');")
    for host in HOSTS:
        deadline = time.monotonic()+30
        while True:
            exists = sql(host,"SELECT to_regclass('"+table+"') IS NOT NULL;")
            if exists == 't' and sql(host,"SELECT count(*) FROM "+table+" WHERE id='"+marker+"';") == '1':
                break
            if time.monotonic()>deadline: raise RuntimeError('PG canary did not replicate')
            time.sleep(1)
    HELPER['api']('clusters/'+PG+'/recovery-freeze',{'freeze':True})
    peer('192.168.102.153', ['docker','service','scale','--detach=true']+[service['ID']+'=0' for service in inventory_services])
    deadline = time.monotonic()+90
    while any(peer(host,['docker','ps','--filter','label=com.docker.swarm.service.name='+name,'--format','{{.ID}}']).strip() for name,_,host in HELPER['MEMBERS'][PG]):
        if time.monotonic()>deadline: raise RuntimeError('PG members did not stop')
        time.sleep(2)
    secret_name='cgpg16_replication_rotated_20260908'
    secret_id=peer('192.168.102.153',['docker','secret','create',secret_name,'-'],state['password'].encode()).decode().strip()
    state.update({'stage':'frozen_for_final_recovery','marker':marker,'table':table,
                  'bootstrap_secret_id':secret_id,'bootstrap_original_references':refs})
    state_path.write_text(json.dumps(state))
    for service,ref in zip(inventory_services,refs):
        f=ref['File']
        addition='source={},target={},uid={},gid={},mode={:04o}'.format(secret_name,f['Name'],f['UID'],f['GID'],f['Mode'])
        peer('192.168.102.153',['docker','service','update','--detach=true','--secret-rm',ref['SecretName'],'--secret-add',addition,service['ID']])
    for service in HELPER['services'](PG):
        if service['Spec']['Mode']['Replicated']['Replicas']!=0 or not any(item['SecretID']==secret_id for item in service['Spec']['TaskTemplate']['ContainerSpec']['Secrets']):
            raise RuntimeError('stopped bootstrap secret migration did not verify')
    print(json.dumps({'result':'old bootstrap secret rejected; PG canary replicated; all PG stopped with rotated bootstrap secret',
                      'marker':marker,'new_secret_id':secret_id,'state':'recovery freeze retained'}))


def verify_retest():
    state_path=Path('/etc/clusterguard/rotation-pg-20260908/state.json')
    state=json.loads(state_path.read_text())
    report,_=inventory()
    topology=HELPER['api']('clusters/'+PG+'/topology')
    result=[]
    for member in topology['instances']:
        host=member['ip_address']
        native=json.loads(sql(host,"SELECT json_build_object('standby',pg_is_in_recovery(),'node_id',current_setting('clusterguard.node_id'),'primary_node_id',current_setting('clusterguard.primary_node_id'),'read_only',current_setting('transaction_read_only'),'marker',(SELECT count(*) FROM "+state['table']+" WHERE id='"+state['marker']+"'),'receiver',(SELECT json_agg(json_build_object('status',status,'host',sender_host,'slot',slot_name)) FROM pg_stat_wal_receiver),'slots',(SELECT json_agg(json_build_object('name',s.slot_name,'active',s.active,'application_name',r.application_name)) FROM pg_replication_slots s JOIN pg_stat_replication r ON r.pid=s.active_pid WHERE s.slot_type='physical'));") )
        is_primary=host==report['primary']
        primary=next(i for i in topology['instances'] if i['ip_address']==report['primary'])
        if native['node_id']!=member['engine_identity']['resource_id'] or native['primary_node_id']!=primary['engine_identity']['resource_id'] or native['standby']==is_primary or native['marker']!=1 or native['read_only']!=('off' if is_primary else 'on'):
            raise RuntimeError('PG native roles, identity or pre-recovery canary mismatch')
        if is_primary:
            if len(native['slots'] or [])!=2 or not all(slot['active'] for slot in native['slots']):
                raise RuntimeError('PG requires two active physical slots')
        elif len(native['receiver'] or [])!=1 or native['receiver'][0]['status']!='streaming' or native['receiver'][0]['host']!=report['primary']:
            raise RuntimeError('PG upstream streaming mismatch')
        service=next(item for item in HELPER['services'](PG) if item['Spec']['TaskTemplate']['ContainerSpec']['Labels']['clusterguard.instance_id']==member['resource_id'])
        if not any(item['SecretID']==state['bootstrap_secret_id'] for item in service['Spec']['TaskTemplate']['ContainerSpec']['Secrets']):
            raise RuntimeError('running PG service has obsolete bootstrap secret')
        cfg=json.loads(peer(host,['cat','/etc/clusterguard/clusterguard.json']))
        if cfg['postgresql']['replication']['username']!='cg_replication':
            raise RuntimeError('controller uses an obsolete replication account')
        native['rpm']=peer(host,['rpm','-q','clusterguard-ha']).decode().strip()
        result.append({'host':host,'native':native})
    owners=[]
    for host in HOSTS:
        addresses=json.loads(peer(host,['ip','-j','address','show']))
        if any(a.get('local')=='192.168.102.157' for interface in addresses for a in interface.get('addr_info',[])):
            owners.append(host)
    if owners!=[report['primary']]: raise RuntimeError('PG VIP is not unique on selected primary')
    service=next(name for name,_,host in HELPER['MEMBERS'][PG] if host=='192.168.102.153')
    container=peer('192.168.102.153',['docker','ps','--filter','label=com.docker.swarm.service.name='+service,'--format','{{.ID}}']).decode().strip()
    identity=peer('192.168.102.153',['docker','exec','-u','postgres','-e','PGPASSFILE='+PASSFILE,container,
        '/usr/lib/postgresql/16/bin/psql','-h','192.168.102.157','-p','55432','-U','postgres','-d','postgres','-X','-A','-t','-v','ON_ERROR_STOP=1',
        '-c',"SELECT current_setting('clusterguard.node_id');"]).decode().strip()
    primary=next(i for i in topology['instances'] if i['ip_address']==report['primary'])
    if identity!=primary['engine_identity']['resource_id']: raise RuntimeError('PG VIP query reached an unexpected identity')
    state['stage']='final_native_recovery_and_rotated_credentials_verified'
    state_path.write_text(json.dumps(state))
    print(json.dumps({'result':'PG final native recovery, retained canary, slots, rotated credentials and VIP verified','vip_owners':owners,'members':result}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['inspect','rotate','prepare-retest','verify-retest'])
    arguments = parser.parse_args()
    if arguments.action == 'rotate':
        rotate()
    elif arguments.action == 'prepare-retest':
        prepare_retest()
    elif arguments.action == 'verify-retest':
        verify_retest()
    else:
        report, _ = inventory()
        print(json.dumps(report))
