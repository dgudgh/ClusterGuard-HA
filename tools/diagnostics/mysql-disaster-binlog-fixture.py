#!/usr/bin/env python3
"""Create or stop fresh loopback-only MySQL recovery fixtures; never takes live data."""
import argparse
import json
import os
import pathlib
import secrets
import signal
import socket
import subprocess
import time
import uuid

BASE = pathlib.Path('/var/lib/clusterguard/diagnostics')
SOFTWARE = pathlib.Path('/opt/clusterguard/mysql/3306/software')
PORTS = (43306, 43307, 43308)


def private_file(path, value):
    with path.open('x') as stream:
        stream.write(value)
    os.chmod(str(path), 0o600)


def run(args, secret_values=(), sql=None, timeout=90):
    result = subprocess.run(args, input=sql, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, universal_newlines=True, timeout=timeout)
    if result.returncode:
        message = result.stderr
        for value in secret_values:
            message = message.replace(value, '[REDACTED]')
        raise RuntimeError('isolated fixture command failed: ' + message[:512])
    return result.stdout.strip()


def create():
    if os.getuid() != 0:
        raise RuntimeError('fixture requires root to manage its private scratch processes')
    for port in PORTS:
        with socket.socket() as connection:
            if connection.connect_ex(('127.0.0.1', port)) == 0:
                raise RuntimeError('reserved fixture port already in use: ' + str(port))
    mysqld, mysql = SOFTWARE / 'bin/mysqld', SOFTWARE / 'bin/mysql'
    if not mysqld.is_file() or not mysql.is_file():
        raise RuntimeError('expected native MySQL runtime not available')
    version = run([str(mysqld), '--no-defaults', '--version'])
    if 'Ver 8.0.' not in version:
        raise RuntimeError('native fixture currently qualifies MySQL 8.0 only')
    nonce = uuid.uuid4().hex
    root = BASE / ('dr-binlog-' + nonce[:12])
    root.mkdir(mode=0o700)
    # MySQL replication passwords are limited to 32 characters.
    admin, replication = secrets.token_hex(16), secrets.token_hex(16)
    private_file(root / 'admin-password', admin)
    private_file(root / 'replication-password', replication)
    private_file(root / 'fixture-owner', nonce)
    fixture = {'scope': 'fresh-loopback-mysql-binlog-recovery', 'nonce': nonce, 'root': str(root),
               'mysql_binary': str(mysql), 'mysqld_binary': str(mysqld),
               'administrative_username': 'cg_fixture_admin',
               'administrative_password_file': str(root / 'admin-password'),
               'replication_username': 'cg_fixture_repl',
               'replication_password_file': str(root / 'replication-password'),
               'members': []}
    private_file(root / 'fixture.json', json.dumps(fixture, indent=2) + '\n')
    for index, port in enumerate(PORTS):
        member = root / str(index)
        member.mkdir(mode=0o700)
        data = member / 'data'
        data.mkdir(mode=0o700)
        temp = member / 'tmp'
        temp.mkdir(mode=0o700)
        config, pid = member / 'my.cnf', member / 'mysqld.pid'
        private_file(config, '\n'.join([
            '[mysqld]', 'user=root', 'basedir=' + str(SOFTWARE), 'datadir=' + str(data),
            'socket=' + str(member / 'mysql.sock'), 'pid-file=' + str(pid),
            'log-error=' + str(member / 'mysqld.log'), 'tmpdir=' + str(temp),
            'secure-file-priv=' + str(temp), 'port=' + str(port), 'bind-address=127.0.0.1',
            'server-id=' + str(401 + index), 'skip-name-resolve=ON', 'mysqlx=OFF',
            'gtid-mode=ON', 'enforce-gtid-consistency=ON', 'log-bin=' + str(data / 'binlog'),
            'log-replica-updates=ON', 'skip-replica-start=ON', 'binlog-expire-logs-seconds=0',
            'innodb-buffer-pool-size=64M', 'innodb-redo-log-capacity=32M',
            'max-connections=30', 'performance-schema=ON', '']) )
        run([str(mysqld), '--no-defaults', '--initialize-insecure', '--user=root',
             '--basedir=' + str(SOFTWARE), '--datadir=' + str(data),
             '--log-error=' + str(member / 'initialize.log'),
             '--innodb-buffer-pool-size=64M', '--innodb-redo-log-capacity=32M'])
        run([str(mysqld), '--defaults-file=' + str(config), '--daemonize'])
        bootstrap = [str(mysql), '--no-defaults', '--protocol=SOCKET',
                     '--socket=' + str(member / 'mysql.sock'), '-uroot',
                     '--batch', '--raw', '--skip-column-names']
        deadline = time.monotonic() + 30
        while True:
            try:
                run(bootstrap, sql='SELECT 1;\n', timeout=5)
                break
            except (RuntimeError, subprocess.TimeoutExpired):
                if time.monotonic() > deadline:
                    raise RuntimeError('fresh MySQL member did not become ready')
                time.sleep(0.25)
        # These accounts and files belong only to these newly initialized empty instances.
        # Session logging is disabled for setup, so no unrelated bootstrap GTIDs need reset.
        sql = ("SET SESSION sql_log_bin=0; CREATE USER 'cg_fixture_admin'@'127.0.0.1' IDENTIFIED BY '" + admin + "';"
               "GRANT ALL PRIVILEGES ON *.* TO 'cg_fixture_admin'@'127.0.0.1' WITH GRANT OPTION;"
               "GRANT BACKUP_ADMIN,CLONE_ADMIN,CONNECTION_ADMIN,SYSTEM_VARIABLES_ADMIN,BINLOG_ADMIN,SESSION_VARIABLES_ADMIN "
               "ON *.* TO 'cg_fixture_admin'@'127.0.0.1';"
               "CREATE USER 'cg_fixture_repl'@'127.0.0.1' IDENTIFIED BY '" + replication + "';"
               "GRANT REPLICATION SLAVE ON *.* TO 'cg_fixture_repl'@'127.0.0.1';"
               "ALTER USER 'root'@'localhost' IDENTIFIED BY '" + admin + "';\n")
        run(bootstrap, secret_values=(admin, replication), sql=sql)
        client = member / 'client.cnf'
        private_file(client, '[client]\nuser=cg_fixture_admin\npassword=' + admin + '\n')
        query = [str(mysql), '--defaults-file=' + str(client), '--protocol=TCP',
                 '-h127.0.0.1', '-P' + str(port), '--batch', '--raw', '--skip-column-names']
        native_id = run(query, secret_values=(admin, replication), sql='SELECT @@server_uuid;\n')
        fixture['members'].append({'port': port, 'native_id': native_id, 'directory': str(member),
                                   'data_directory': str(data), 'option_file': str(config), 'pid_file': str(pid)})
        (root / 'fixture.json').write_text(json.dumps(fixture, indent=2) + '\n')
        print(json.dumps({'stage': 'initialized', 'index': index, 'port': port,
                          'native_id': native_id, 'fixture_file': str(root / 'fixture.json')}), flush=True)
    print(json.dumps({'status': 'ready', 'fixture_file': str(root / 'fixture.json'),
                      'scope': fixture['scope'], 'ports': list(PORTS)}), flush=True)


def stop(path):
    root = path.parent.resolve()
    if root.parent != BASE or not root.name.startswith('dr-binlog-') or path.name != 'fixture.json':
        raise RuntimeError('only this tool owned fixture roots may be stopped')
    fixture = json.loads(path.read_text())
    if fixture['scope'] != 'fresh-loopback-mysql-binlog-recovery' or (root / 'fixture-owner').read_text() != fixture['nonce']:
        raise RuntimeError('fixture ownership proof differs')
    for member in fixture['members']:
        pid_path = pathlib.Path(member['pid_file'])
        expected_config = pathlib.Path(member['option_file']).resolve()
        if expected_config.parent.parent != root or member['port'] not in PORTS:
            raise RuntimeError('member path or port outside fixture')
        if not pid_path.exists():
            continue
        pid = int(pid_path.read_text().strip())
        proc = pathlib.Path('/proc') / str(pid)
        if not proc.exists():
            continue
        arguments = (proc / 'cmdline').read_bytes().split(b'\0')
        if ('--defaults-file=' + str(expected_config)).encode() not in arguments:
            raise RuntimeError('PID is not the owned scratch mysqld; it will not be signaled')
        os.kill(pid, signal.SIGTERM)
        deadline = time.monotonic() + 30
        while proc.exists() and time.monotonic() < deadline:
            time.sleep(0.2)
        if proc.exists():
            raise RuntimeError('scratch process did not stop; no forced signal was sent')
    print(json.dumps({'status': 'stopped', 'fixture_file': str(path), 'data_and_logs_retained': True}), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['create', 'stop'])
    parser.add_argument('--fixture', type=pathlib.Path)
    args = parser.parse_args()
    if args.action == 'create':
        create()
    elif args.fixture is not None:
        stop(args.fixture)
    else:
        parser.error('stop requires the exact fixture file')
