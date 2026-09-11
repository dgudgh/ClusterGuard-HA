"""Run the latest helper in a private Linux fixture, never the installed socket."""
import hashlib
import json
import os
import pathlib
import pwd
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time


CLIENT = r'''
import json, socket, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(3)
phase = 'connect'
try:
    s.connect(sys.argv[1])
    phase = 'response'
    body = sys.argv[2].encode()
    request = b'POST /v1/jobs HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: ' + str(len(body)).encode() + b'\r\n\r\n' + body
    s.sendall(request)
    data = b''
    while len(data) < 8192:
        chunk = s.recv(4096)
        if not chunk:
            break
        data += chunk
    print(json.dumps({'phase': phase, 'http': int(data.split(b' ', 2)[1]) if data.startswith(b'HTTP/') else None, 'closed': not data}))
except (OSError, ValueError) as error:
    print(json.dumps({'phase': phase, 'http': None, 'error': type(error).__name__}))
finally:
    s.close()
'''


def run(args, timeout=90):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            universal_newlines=True, timeout=timeout)
    return {'exit_code': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr}


def describe(path):
    result = {'path': path, 'exists': os.path.lexists(path)}
    if result['exists']:
        info = os.lstat(path)
        result.update(mode=oct(stat.S_IMODE(info.st_mode)), uid=info.st_uid, gid=info.st_gid,
                      symlink=stat.S_ISLNK(info.st_mode), links=info.st_nlink)
        if stat.S_ISREG(info.st_mode):
            with open(path, 'rb') as handle:
                result['sha256'] = hashlib.sha256(handle.read()).hexdigest()
    return result


def main():
    root = pathlib.Path(sys.argv[1])
    if (os.geteuid() != 0 or root.parent != pathlib.Path('/root') or
            not root.name.startswith('cg-hb.') or root.is_symlink() or root.stat().st_uid != 0):
        raise ValueError('requires root and a dedicated root-owned /root/cg-hb.* directory')
    account, nobody = pwd.getpwnam('clusterguard'), pwd.getpwnam('nobody')
    if account.pw_uid == 0 or nobody.pw_uid in (0, account.pw_uid):
        raise ValueError('distinct unprivileged identities are required')
    result = {'hostname': socket.gethostname(), 'uid': os.geteuid(), 'service_uid': account.pw_uid,
              'service_gid': account.pw_gid, 'nobody_uid': nobody.pw_uid, 'tests': {}, 'checks': []}
    os.chown(str(root), 0, account.pw_gid)
    os.chmod(str(root), 0o751)
    result['installed_paths'] = [describe(p) for p in (
        '/var', '/var/lib', '/var/lib/clusterguard', '/var/lib/clusterguard/updates',
        '/usr/local/bin/clusterguard-update-helper', '/usr/local/libexec/clusterguard-update-job.sh',
        '/run/clusterguard/update-helper.sock')]
    result['selinux'] = run(['getenforce'])
    result['installed_service'] = run(['systemctl', 'show', 'clusterguard-update-helper', '-p', 'User', '-p', 'Group', '-p', 'MainPID'])
    properties = dict(line.split('=', 1) for line in result['installed_service']['stdout'].splitlines() if '=' in line)
    pid = properties.get('MainPID', '0')
    if pid.isdigit() and int(pid) > 0:
        result['installed_helper_binary'] = describe(os.readlink('/proc/' + pid + '/exe'))
    private = pathlib.Path(tempfile.mkdtemp(prefix='fixture-', dir=str(root)))
    os.chown(str(private), 0, account.pw_gid)
    os.chmod(str(private), 0o751)
    private_root = private / 'private-root'
    private_root.mkdir(mode=0o700)
    os.chown(str(private_root), 0, 0)
    os.chmod(str(private_root), 0o700)
    daemon = None

    def check(name, condition, evidence=None):
        result['checks'].append({'name': name, 'pass': bool(condition), 'evidence': evidence})

    try:
        for user in ('root', 'clusterguard'):
            temp = private / ('temp-' + user)
            temp.mkdir(mode=0o700)
            if user != 'root':
                os.chown(str(temp), account.pw_uid, account.pw_gid)
            for binary in ('platformupdate.test', 'peercred.test'):
                args = ['env', 'TMPDIR=' + str(temp), str(root / binary), '-test.v', '-test.count=1', '-test.timeout=90s']
                if user != 'root':
                    args = ['runuser', '-u', user, '--'] + args
                label = user + '/' + binary
                result['tests'][label] = run(args, 100)
                check(label, result['tests'][label]['exit_code'] == 0)

        stage = private / 'stage'
        stage.mkdir(mode=0o750)
        os.chown(str(stage), 0, account.pw_gid)
        runner = private / 'runner.sh'
        runner.write_text('#!/bin/sh\nprintf "%s\\n" "$*"\n[ "$4" != fixture-fail ] || exit 17\n')
        runner.chmod(0o700)
        socket_path = str(private / 'helper.sock')
        daemon = subprocess.Popen([str(root / 'helper'), '--socket', socket_path,
                                   '--root', str(stage), '--runner', str(runner),
                                   '--private-root', str(private_root)],
                                  env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'},
                                  preexec_fn=lambda: os.setgid(account.pw_gid),
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 5
        while not os.path.exists(socket_path) and daemon.poll() is None and time.monotonic() < deadline:
            time.sleep(0.02)
        if not os.path.exists(socket_path):
            raise RuntimeError('private helper failed to start')
        result['private_socket'] = describe(socket_path)
        check('socket owner and mode', result['private_socket']['uid'] == 0 and
              result['private_socket']['gid'] == account.pw_gid and result['private_socket']['mode'] == '0o660')

        def request(body, user='clusterguard'):
            args = ['/usr/bin/python3', '-c', CLIENT, socket_path, body]
            if user == 'nobody':
                # Same socket group permits connect; SO_PEERCRED must still reject this UID.
                args = ['runuser', '-u', 'nobody', '-g', 'clusterguard', '--'] + args
            elif user != 'root':
                args = ['runuser', '-u', user, '--'] + args
            response = run(args, 8)
            if response['exit_code'] != 0:
                raise RuntimeError('private client failed: ' + response['stderr'][:200])
            return json.loads(response['stdout'])

        def make_package(identity):
            directory = stage / identity
            directory.mkdir(mode=0o770)
            os.chown(str(directory), account.pw_uid, account.pw_gid)
            (directory / 'package.cgpatch').write_text('fixture only, not an upgrade package')
            return directory

        denied = request('{"mode":"plan","patch_id":"fixture-denied"}', 'nobody')
        check('untrusted UID with permitted socket group rejected', denied['phase'] == 'response' and denied['http'] is None, denied)
        for user, body in (
            ('root', '{"mode":"plan","patch_id":"missing"}'),
            ('clusterguard', '{"mode":"plan","patch_id":"missing"}'),
        ):
            response = request(body, user)
            check(user + ' reaches authorized HTTP handler', response['http'] == 404, response)
        for label, body in (
            ('unknown mode', '{"mode":"shell","patch_id":"fixture"}'),
            ('path traversal', '{"mode":"plan","patch_id":"../outside"}'),
            ('trailing JSON', '{"mode":"plan","patch_id":"fixture"} {}'),
            ('unknown field', '{"mode":"plan","patch_id":"fixture","command":"false"}'),
        ):
            response = request(body)
            check(label, response['http'] == 400, response)
        outside = private / 'outside'
        outside.mkdir(mode=0o700)
        protected = outside / 'package.cgpatch'
        protected.write_text('protected')
        protected.chmod(0o600)
        (stage / 'fixture-link').symlink_to(outside)
        response = request('{"mode":"plan","patch_id":"fixture-link"}')
        check('linked job directory rejected', response['http'] == 404 and protected.read_text() == 'protected' and
              stat.S_IMODE(protected.stat().st_mode) == 0o600, response)

        for mode in ('plan', 'execute', 'resume', 'rollback'):
            identity = 'fixture-' + mode
            directory = make_package(identity)
            response = request(json.dumps({'mode': mode, 'patch_id': identity}))
            check('private runner ' + mode, response['http'] == 202, response)
            output = directory / 'output.log'
            deadline = time.monotonic() + 3
            while (not output.exists() or output.stat().st_size == 0) and time.monotonic() < deadline:
                time.sleep(0.02)
            observed = run(['runuser', '-u', 'clusterguard', '--', 'cat', str(output)])
            check(mode + ' output and directory permissions', observed['exit_code'] == 0 and
                  observed['stdout'].strip() == '--mode ' + mode + ' --patch-id ' + identity and
                  stat.S_IMODE(output.stat().st_mode) == 0o640 and
                  stat.S_IMODE(directory.stat().st_mode) == 0o770,
                  {'read_exit': observed['exit_code'], 'output': observed['stdout'],
                   'file': describe(str(output)), 'directory': describe(str(directory))})
        directory = make_package('fixture-fail')
        response = request('{"mode":"plan","patch_id":"fixture-fail"}')
        status_file = directory / 'status.json'
        deadline = time.monotonic() + 3
        while not status_file.exists() and time.monotonic() < deadline:
            time.sleep(0.02)
        job = json.loads(status_file.read_text()) if status_file.exists() else {}
        check('runner failure publishes failed state', response['http'] == 202 and job.get('status') == 'failed' and
              job.get('patch_id') == 'fixture-fail', job.get('status'))
    except Exception as error:
        result['error'] = {'type': type(error).__name__, 'message': str(error)[:300]}
    finally:
        if daemon is not None:
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=5)
            result['private_helper_stopped'] = daemon.poll() is not None
        shutil.rmtree(str(private))
        result['private_directory_removed'] = not private.exists()
    result['passed'] = not result.get('error') and bool(result['checks']) and all(c['pass'] for c in result['checks'])
    print(json.dumps(result))
    return 0 if result['passed'] else 1


if __name__ == '__main__':
    sys.exit(main())
