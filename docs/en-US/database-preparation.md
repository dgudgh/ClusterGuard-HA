# ClusterGuard HA Database Integration Manual

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/database-preparation.md)
<!-- /LANGUAGE-SWITCH -->

This document describes the configuration, minimal permissions, and verification required on the database side for MySQL, PostgreSQL, Oracle Data Guard Broker, and SQL Server Always On. The network segments, passwords, service names, and resource IDs in the examples must be replaced with on-site values.

Version Boundary: The formal database support range for `2.1-45` is MySQL; PostgreSQL from version 2.2 onwards.
The Oracle and SQL Server sections are for subsequent independent capability line preparations. No claims can be made that it has entered the 2.1 production support range before the corresponding version is released and on-site acceptance is completed.

## 1. General Requirements

All database nodes must meet the following:

- NTP/Chrony is functioning normally, and clock deviation is controlled
- Hostnames, IPs, database ports, and platform fixed node names are registered
- Platform `resource_id` remains permanent; hostname, IP, and port are only variable endpoints
- Native database identity is unique and stable
- Control nodes must have access to the database port and SSH/Agent port
- Discovery account and execution account are separated
- Passwords are stored only in protected environment files or native database secure storage
- TLS is enforced when available, and source network segments are restricted
- Automatic failover must not be enabled before topology, replication, identity, and write endpoint verification are passed

## 2. MySQL

### 2.1 Replication Basics

Each instance must have a unique `server_id`. The `server_uuid` of the same data directory must not be replicated to another node.

Recommended for MySQL 8.0/8.4:

```ini
[mysqld]
server_id=1523306
log_bin=mysql-bin
binlog_format=ROW
gtid_mode=ON
enforce_gtid_consistency=ON
log_replica_updates=ON
relay_log_recovery=ON
read_only=ON
super_read_only=ON
```

After the primary database is online, the controlled process will disable `read_only` and `super_read_only`. Do not copy `auto.cnf` to new nodes, otherwise duplicate `server_uuid` will be generated.

Verification:

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
SELECT @@global.gtid_mode, @@global.enforce_gtid_consistency;
SELECT @@global.read_only, @@global.super_read_only;
SHOW REPLICA STATUS\G
```

MySQL 5.7 uses `SHOW SLAVE STATUS\G`, with configuration item `log_slave_updates`.

### 2.2 Default Connection and Memory Parameters for ClusterGuard Installation Instances

When creating a new MySQL instance through the ClusterGuard offline installer, the installation script will read `/proc/meminfo` on the target database node and generate a conservative production baseline based on physical memory:

- `innodb_buffer_pool_size`: By default, it takes 70% of the physical memory and rounds up to the nearest integer multiple of 4 GiB; if the result exceeds 80% of the physical memory, it takes the maximum multiple of 4 GiB that does not exceed 80%, without setting a fixed capacity upper limit
- `max_connections`: By default, it is fixed at 1000
- `table_open_cache`, `thread_cache_size`, temporary tables, and redo capacity vary with memory tiers
- Sorting, joining, and read buffer use controlled small values to avoid exhausting memory under high concurrency due to large buffers per connection

The generated configuration is located at `/etc/clusterguard/mysql/<PORT>.cnf`. These parameters are installation baselines and do not replace pre-deployment performance testing based on business SQL, connection pools, storage latency, and capacity.

This rule requires that MySQL data nodes have at least 5 GiB of physical memory. If the capacity is below this, there is no valid buffer pool that simultaneously meets "a multiple of 4 GiB" and "does not exceed 80%", and the installer will block and request expansion.

The new instance listens by default on `0.0.0.0:<PORT>`. The discovery, execution, and replication accounts created by ClusterGuard allow TCP connections; the host firewall should still only allow traffic from the database node, control node, and approved business network segments. The default root is only allowed to connect via local socket and `127.0.0.1`; remote management uses a dedicated account. The installer will only create or update the corresponding `root`@`HOST` when the `--mysql-root-remote-host HOST` is explicitly passed in the installation command, for example, `--mysql-root-remote-host '%'`; if the parameter is omitted, the remote root is not operated on.

The runtime socket for managed instances is `/run/clusterguard/mysql/<PORT>/mysql.sock`, created by systemd `RuntimeDirectory`, and should not be placed in the database data directory. The installer will publish the default client configuration on the local machine; standard 3306 instances can directly use `mysql -uroot -p`, while retaining the `/tmp/mysql.sock` compatibility link that automatically recovers after reboot. The platform always uses absolute binaries, explicit defaults files, or TCP, and does not rely on this compatibility link.

Authentication plugins are handled by the database version:

- MySQL 8.0/5.7: Configure `default_authentication_plugin=mysql_native_password`
- MySQL 8.4: Configure `mysql_native_password=ON`, and explicitly specify `mysql_native_password` for the ClusterGuard management account
- MySQL 9.x: Do not write the native password parameter that has been removed, and use the default authentication plugin supported by the server

Verify the automatically generated parameters and accounts:

```sql
SHOW VARIABLES WHERE Variable_name IN (
  'bind_address', 'max_connections', 'innodb_buffer_pool_size',
  'table_open_cache', 'thread_cache_size'
);
SELECT user, host, plugin
FROM mysql.user
WHERE user IN ('cg_discovery', 'cg_operator', 'cg_replication', 'root')
ORDER BY user, host;
```

### 2.3 Minimal Permission Accounts

The following example restricts the control node network segment to `192.168.102.%`. In production environments, it should be further tightened to specific control node addresses and use random long passwords.

> **Applies to preparing accounts by hand on an existing MySQL instance.** Instances created by the
> ClusterGuard installer do not use this least-privilege set: `scripts/clusterguard-mysql-install.sh`
> grants the operation account `GRANT ALL PRIVILEGES ON *.* ... WITH GRANT OPTION`, adds `SELECT`
> to the discovery account and `REPLICATION CLIENT` to the replication account, and always creates
> the accounts with the host name `'%'`. Before assessing a production instance against least
> privilege, confirm first how its accounts were created.

MySQL 8.0/8.4:

```sql
CREATE USER 'cg_discovery'@'192.168.102.%'
  IDENTIFIED BY '<DISCOVERY_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO
  'cg_discovery'@'192.168.102.%';

CREATE USER 'cg_operator'@'192.168.102.%'
  IDENTIFIED BY '<OPERATION_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT, CONNECTION_ADMIN,
  SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.* TO
  'cg_operator'@'192.168.102.%';

CREATE USER 'cg_replication'@'192.168.102.%'
  IDENTIFIED BY '<REPLICATION_PASSWORD>';
GRANT REPLICATION SLAVE ON *.* TO
  'cg_replication'@'192.168.102.%';
```

MySQL 5.7 does not have detailed management permissions, and the execution account requires `SUPER`. This expands the permission surface, so it should be restricted in source, use a dedicated password, and enable auditing:

```sql
GRANT PROCESS, REPLICATION CLIENT, SUPER ON *.* TO
  'cg_operator'@'192.168.102.%';
```

If the server has enabled TLS:

```sql
ALTER USER 'cg_discovery'@'192.168.102.%' REQUIRE SSL;
ALTER USER 'cg_operator'@'192.168.102.%' REQUIRE SSL;
ALTER USER 'cg_replication'@'192.168.102.%' REQUIRE SSL;
```

### 2.4 Establishing Replication

MySQL 8:

```sql
CHANGE REPLICATION SOURCE TO
  SOURCE_HOST='192.168.102.152',
  SOURCE_PORT=3306,
  SOURCE_USER='cg_replication',
  SOURCE_PASSWORD='<REPLICATION_PASSWORD>',
  SOURCE_AUTO_POSITION=1,
  GET_SOURCE_PUBLIC_KEY=1;
START REPLICA;
```

MySQL 5.7:

```sql
CHANGE MASTER TO
  MASTER_HOST='192.168.102.152',
  MASTER_PORT=3306,
  MASTER_USER='cg_replication',
  MASTER_PASSWORD='<REPLICATION_PASSWORD>',
  MASTER_AUTO_POSITION=1;
START SLAVE;
```

Before integration, the following must be confirmed:

- Only one writable primary database
- No errant GTID on candidate nodes
- IO/SQL threads are normal
- Delay meets the switchover strategy
- Each cluster uses an independent VIP
- VIP is only bound to the current primary database

## 3. PostgreSQL

### 3.1 Identity and Replication Basics

ClusterGuard HA uses `system_identifier` to identify the database cluster and the platform UUID to identify the node. Do not use hostname:port as the primary key.

Primary database recommendation:

```conf
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
wal_log_hints = on
password_encryption = scram-sha-256
```

You can also use data checksums instead of `wal_log_hints` to meet the `pg_rewind` prerequisite.

Register a stable platform identity for each node:

```sql
ALTER SYSTEM SET clusterguard.node_id =
  '11111111-1111-4111-8111-111111111111';
SELECT pg_reload_conf();
```

Replicas also need to be maintained by a controlled process for `clusterguard.primary_node_id` and `primary_conninfo`.

### 3.2 Minimal Permission Accounts
> **Applies to onboarding an existing instance.** PostgreSQL instances created by the ClusterGuard
> offline installer are currently onboarded with the `postgres` account (both `discovery` and
> `operation`), and the installer generates `pg_hba.conf` entries from the allowed CIDR rather than
> the `hostssl` per-role rules below. Configure an existing instance as described here when you
> need least-privilege access.


```sql
CREATE ROLE cg_monitor LOGIN PASSWORD '<DISCOVERY_PASSWORD>';
GRANT pg_monitor TO cg_monitor;

CREATE ROLE cg_operator LOGIN PASSWORD '<OPERATION_PASSWORD>';
GRANT pg_monitor, pg_signal_backend TO cg_operator;
GRANT ALTER SYSTEM ON PARAMETER default_transaction_read_only TO cg_operator;
GRANT EXECUTE ON FUNCTION pg_reload_conf() TO cg_operator;
GRANT EXECUTE ON FUNCTION pg_wal_replay_resume() TO cg_operator;

CREATE ROLE clusterguard_repl
  WITH LOGIN REPLICATION PASSWORD '<REPLICATION_PASSWORD>';
```

`pg_hba.conf` example:

```conf
hostssl all         cg_monitor         192.168.102.152/32 scram-sha-256
hostssl all         cg_operator        192.168.102.152/32 scram-sha-256
hostssl replication clusterguard_repl  192.168.102.0/24   scram-sha-256
```

Add equivalent rules for the other two control nodes and then reload:

```sql
SELECT pg_reload_conf();
```

### 3.3 Verification

```sql
SELECT system_identifier FROM pg_control_system();
SELECT pg_is_in_recovery();
SHOW default_transaction_read_only;
SELECT * FROM pg_stat_replication;
SELECT * FROM pg_stat_wal_receiver;
SELECT has_parameter_privilege(
  'cg_operator', 'default_transaction_read_only', 'ALTER SYSTEM');
SELECT has_function_privilege(
  'cg_operator', 'pg_reload_conf()', 'EXECUTE');
```

Node recovery prioritizes `pg_rewind`; if the system identifier, major version, data checksum, or `wal_log_hints` is not met, a full `pg_basebackup` must be used to rebuild.

## 4. Oracle Data Guard Broker

### 4.1 Broker Prerequisites

Both primary and standby databases are set:

```sql
ALTER SYSTEM SET DG_BROKER_START=TRUE SCOPE=BOTH;
SELECT DBID, DB_UNIQUE_NAME, DATABASE_ROLE, OPEN_MODE FROM V$DATABASE;
```

Requirements:

- Data Guard Broker configuration has been created and is in the state of `SUCCESS`
- Primary and standby use consistent password file authentication
- Each database has a stable `DB_UNIQUE_NAME`
- Broker connect identifier is resolvable on all nodes
- Standby redo log, transport, and apply are normal
- Switch service name is decoupled from the database role

### 4.2 Dedicated SYSDG Account

Do not write the `SYS` password into ClusterGuard HA. Create a dedicated account:

```sql
CREATE USER CLUSTERGUARD_DG IDENTIFIED BY "<STRONG_PASSWORD>";
GRANT CREATE SESSION TO CLUSTERGUARD_DG;
GRANT SYSDG TO CLUSTERGUARD_DG;
```

The same account must be included in the password files of the primary and standby databases and verified separately:

```bash
sqlplus 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB as sysdg'
sqlplus 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB_B as sysdg'
```

The Agent mode runs `dgmgrl` on the database node as the `oracle` operating system account. The protected environment file on the database node is set:

```bash
CG_ORACLE_BROKER_PASSWORD='<STRONG_PASSWORD>'
```

Permissions should be `0600` and must not appear in command line parameters, audit raw returns, or shell history.

### 4.3 Broker Verification

```bash
dgmgrl 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB as sysdg'
```

In DGMGRL:

```text
SHOW CONFIGURATION;
SHOW DATABASE VERBOSE 'MESDB';
VALIDATE DATABASE VERBOSE 'MESDB';
VALIDATE DATABASE VERBOSE 'MESDB_B';
```

Before planned switching, the protection mode, transport lag, apply lag, switchover status, and target database status must all meet the strategy. ClusterGuard HA will not mark a switch reported as failed by the Broker as successful.

Business access should use role-based service names and VIP/SCAN/Listener, and should not expose the fixed host SID to the application. After switching, the service role and listener endpoint will direct the connection to the new primary database.

## 5. SQL Server Always On

### 5.1 Always On Prerequisites

Requirements:

- Windows Failover Cluster and Availability Group are healthy
- Each replica `replica_id` is stable
- AG `group_id` is stable
- The planned switch target uses synchronous commit and is in `SYNCHRONIZED`
- Listener name, IP, and port are configured
- Control accounts are consistent and source-restricted on each replica

### 5.2 Minimal Permission Accounts

Execute on each SQL Server instance, using on-site password policies:

```sql
USE master;
CREATE LOGIN cg_monitor WITH PASSWORD = '<DISCOVERY_PASSWORD>';
CREATE USER cg_monitor FOR LOGIN cg_monitor;
GRANT VIEW SERVER STATE TO cg_monitor;
GRANT VIEW ANY DEFINITION TO cg_monitor;

CREATE LOGIN cg_operator WITH PASSWORD = '<OPERATION_PASSWORD>';
CREATE USER cg_operator FOR LOGIN cg_operator;
GRANT VIEW SERVER STATE TO cg_operator;
GRANT VIEW ANY DEFINITION TO cg_operator;
GRANT ALTER ANY AVAILABILITY GROUP TO cg_operator;
```

If SQL Server 2022 detects insufficient performance DMV permissions, it will assess and grant according to the official permission model:

```sql
GRANT VIEW SERVER PERFORMANCE STATE TO cg_monitor;
GRANT VIEW SERVER PERFORMANCE STATE TO cg_operator;
```

Do not directly grant `sysadmin` as a regular solution.

### 5.3 Verification

```sql
SELECT group_id, name FROM sys.availability_groups;
SELECT replica_id, replica_server_name, availability_mode_desc,
       failover_mode_desc
FROM sys.availability_replicas;

SELECT ar.replica_server_name, rs.role_desc,
       rs.connected_state_desc, rs.recovery_health_desc,
       rs.synchronization_health_desc
FROM sys.dm_hadr_availability_replica_states rs
JOIN sys.availability_replicas ar
  ON ar.replica_id = rs.replica_id;
```

Planned switching is only allowed to execute when the target replica is synchronized and healthy. Forced failover may cause data loss, and the current product strategy will explicitly block actions that do not meet the conditions, without providing fake execution.

## 6. Control Node Environment Variables

Write the database password into `/etc/clusterguard/clusterguard.env`, with permissions `0640`, and owner `root:clusterguard`:

```bash
CG_MYSQL_DISCOVERY_PASSWORD='<...>'
CG_MYSQL_OPERATION_PASSWORD='<...>'
CG_MYSQL_REPLICATION_PASSWORD='<...>'
CG_POSTGRESQL_DISCOVERY_PASSWORD='<...>'
CG_POSTGRESQL_OPERATION_PASSWORD='<...>'
CG_POSTGRESQL_REPLICATION_PASSWORD='<...>'
CG_ORACLE_DISCOVERY_PASSWORD='<...>'
CG_ORACLE_OPERATION_PASSWORD='<...>'
CG_SQLSERVER_DISCOVERY_PASSWORD='<...>'
CG_SQLSERVER_OPERATION_PASSWORD='<...>'
```

When changing passwords, first create or update the account in parallel on the database side, then roll out the three control nodes, and finally revoke the old credentials. The entire process must maintain Raft majority availability.

## 7. Integration Acceptance Completion

Each cluster must verify at least the following:

1. Native identity corresponds one-to-one with platform resources.
2. After modifying hostname, IP, and port, `resource_id` remains unchanged, and the old endpoint enters the alias.
3. The primary database, replicas, and replication source in the topology are correct.
4. The discovery account cannot perform switching.
5. The execution account cannot bypass the platform Safety Guard, Lock, Approval, and Verify.
6. The writer endpoint only points to the current writable primary database.
7. Operation logs include time, original primary database, target primary database, result, verification, and report.
8. Capabilities that are not implemented or unsafe return `unsupported` or `blocked`, and cannot return pseudo-success.
