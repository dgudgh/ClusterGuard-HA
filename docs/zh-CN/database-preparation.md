# ClusterGuard HA 数据库接入手册

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/database-preparation.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


本文说明 MySQL、PostgreSQL、Oracle Data Guard Broker 和 SQL Server Always On 在数据库侧需要完成的配置、最小权限和验证。示例中的网段、密码、服务名和资源 ID 必须替换为现场值。

版本边界：`2.1-45` 的正式数据库支持范围是 MySQL；PostgreSQL 从 2.2 开始。
Oracle 与 SQL Server 章节用于后续独立能力线准备，完成对应版本发布和现场验收前
不得据此宣称已进入 2.1 生产支持范围。

## 1. 通用要求

所有数据库节点必须满足：

- NTP/Chrony 正常，时钟偏差受控
- 主机名、IP、数据库端口和平台固定节点名已登记
- 平台 `resource_id` 永久不变，hostname、IP、port 只是可变 endpoint
- 数据库原生身份唯一且稳定
- 控制节点到数据库端口和 SSH/Agent 端口可达
- 发现账号与执行账号分离
- 密码只保存在受保护的环境文件或数据库原生安全存储中
- TLS 可用时强制 TLS，并限制来源网段
- 未通过拓扑、复制、身份和写入端点验证前不得启用自动故障切换

## 2. MySQL

### 2.1 复制基础

每台实例必须有唯一 `server_id`，同一数据目录的 `server_uuid` 不得复制到另一节点。

MySQL 8.0/8.4 推荐：

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

主库上线后由受控流程关闭 `read_only` 和 `super_read_only`。不要把 `auto.cnf` 复制到新节点，否则会产生重复 `server_uuid`。

验证：

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
SELECT @@global.gtid_mode, @@global.enforce_gtid_consistency;
SELECT @@global.read_only, @@global.super_read_only;
SHOW REPLICA STATUS\G
```

MySQL 5.7 使用 `SHOW SLAVE STATUS\G`，配置项 `log_slave_updates`。

### 2.2 ClusterGuard 安装实例的默认连接与内存参数

通过 ClusterGuard 离线安装器新建 MySQL 时，安装脚本会在目标数据库节点读取 `/proc/meminfo`，按物理内存生成保守的生产基线：

- `innodb_buffer_pool_size`：默认取物理内存的 70% 并向上取整到 4 GiB 的整数倍；若结果超过物理内存的 80%，则取不超过 80% 的最大 4 GiB 倍数，不设置固定容量上限
- `max_connections`：默认固定为 1000
- `table_open_cache`、`thread_cache_size`、临时表和 redo 容量随内存分档
- 排序、连接和读缓冲使用受控的小值，避免每连接大缓冲在高并发下耗尽内存

生成配置位于 `/etc/clusterguard/mysql/<PORT>.cnf`。参数是安装基线，不替代上线前按业务 SQL、连接池、存储延迟和容量做压测。

该规则要求 MySQL 数据节点至少具备 5 GiB 物理内存。低于此容量时不存在同时满足“4 GiB 的倍数”和“不超过 80%”的有效 buffer pool，安装器会阻断并要求扩容。

新实例默认监听 `0.0.0.0:<PORT>`。ClusterGuard 创建的发现、执行和复制账号允许通过 TCP 连接；主机防火墙仍只应放通数据库节点、控制节点和审批的业务网段。默认 root 只允许本机 socket 和 `127.0.0.1`，远程管理使用专用账号。只有安装命令显式传入 `--mysql-root-remote-host HOST` 时，安装器才创建或更新对应的 `root`@`HOST`，例如 `--mysql-root-remote-host '%'`；省略参数时不操作远程 root。

受管实例的运行时 socket 为 `/run/clusterguard/mysql/<PORT>/mysql.sock`，由 systemd `RuntimeDirectory` 创建，不应放入数据库数据目录。安装器会发布本机客户端默认配置；标准 3306 实例可直接使用 `mysql -uroot -p`，同时保留重启后自动恢复的 `/tmp/mysql.sock` 兼容链接。平台内部始终使用绝对二进制、明确的 defaults 文件或 TCP，不依赖该兼容链接。

认证插件按数据库版本处理：

- MySQL 8.0/5.7：配置 `default_authentication_plugin=mysql_native_password`
- MySQL 8.4：配置 `mysql_native_password=ON`，并为 ClusterGuard 管理账号显式指定 `mysql_native_password`
- MySQL 9.x：不写已经移除的 native password 参数，使用服务器支持的默认认证插件

验证自动生成的参数和账号：

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

### 2.3 最小权限账号

以下示例把控制节点网段限制为 `192.168.102.%`。生产环境应进一步收紧到具体控制节点地址，并使用随机长密码。

> **适用范围：** 本节是**手工为已有 MySQL 实例**准备账号的配方。由 ClusterGuard 安装器新建的托管实例
> 并不使用这套最小权限：`scripts/clusterguard-mysql-install.sh` 给执行账号的是
> `GRANT ALL PRIVILEGES ON *.* ... WITH GRANT OPTION`，发现账号额外有 `SELECT`，复制账号额外有
> `REPLICATION CLIENT`，且账号主机名一律是 `'%'`。按最小权限评估生产实例前请先确认账号是怎么来的。

MySQL 8.0/8.4：

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

MySQL 5.7 没有细分管理权限，执行账号需要 `SUPER`。这会扩大权限面，应限制来源、使用专用密码并开启审计：

```sql
GRANT PROCESS, REPLICATION CLIENT, SUPER ON *.* TO
  'cg_operator'@'192.168.102.%';
```

如果服务器启用了 TLS：

```sql
ALTER USER 'cg_discovery'@'192.168.102.%' REQUIRE SSL;
ALTER USER 'cg_operator'@'192.168.102.%' REQUIRE SSL;
ALTER USER 'cg_replication'@'192.168.102.%' REQUIRE SSL;
```

### 2.4 建立复制

MySQL 8：

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

MySQL 5.7：

```sql
CHANGE MASTER TO
  MASTER_HOST='192.168.102.152',
  MASTER_PORT=3306,
  MASTER_USER='cg_replication',
  MASTER_PASSWORD='<REPLICATION_PASSWORD>',
  MASTER_AUTO_POSITION=1;
START SLAVE;
```

接入前必须确认：

- 只有一个可写主库
- 候选节点无 errant GTID
- IO/SQL 线程正常
- 延迟满足切换策略
- 每套集群使用独立 VIP
- VIP 只绑定在当前主库

## 3. PostgreSQL

### 3.1 身份和复制基础

ClusterGuard HA 使用 `system_identifier` 识别数据库集群，使用平台 UUID 识别节点。不能把 hostname:port 当成主键。

主库建议：

```conf
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
wal_log_hints = on
password_encryption = scram-sha-256
```

也可以使用数据校验和代替 `wal_log_hints` 满足 `pg_rewind` 前提。

为每个节点登记稳定的平台身份：

```sql
ALTER SYSTEM SET clusterguard.node_id =
  '11111111-1111-4111-8111-111111111111';
SELECT pg_reload_conf();
```

副本还需要由受控流程维护 `clusterguard.primary_node_id` 和 `primary_conninfo`。

### 3.2 最小权限账号
> **适用于接入已有实例。** 由 ClusterGuard 离线安装器新建的 PostgreSQL 实例当前以 `postgres`
> 账号接入（`discovery` 与 `operation` 均为 `postgres`），`pg_hba.conf` 条目由安装器按允许网段生成，
> 不是下面的 `hostssl` + 按角色规则。需要最小权限接入时，请对已有实例按本节配置。


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

`pg_hba.conf` 示例：

```conf
hostssl all         cg_monitor         192.168.102.152/32 scram-sha-256
hostssl all         cg_operator        192.168.102.152/32 scram-sha-256
hostssl replication clusterguard_repl  192.168.102.0/24   scram-sha-256
```

对另外两个控制节点增加等价规则，然后 reload：

```sql
SELECT pg_reload_conf();
```

### 3.3 验证

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

节点恢复优先使用 `pg_rewind`；不满足同一 system identifier、同一大版本、数据校验和或 `wal_log_hints` 时，必须使用完整 `pg_basebackup` 重建。

## 4. Oracle Data Guard Broker

### 4.1 Broker 前提

主备库都设置：

```sql
ALTER SYSTEM SET DG_BROKER_START=TRUE SCOPE=BOTH;
SELECT DBID, DB_UNIQUE_NAME, DATABASE_ROLE, OPEN_MODE FROM V$DATABASE;
```

要求：

- Data Guard Broker configuration 已创建且状态为 `SUCCESS`
- 主备使用一致的密码文件认证
- 每个库有稳定 `DB_UNIQUE_NAME`
- Broker connect identifier 在所有节点都可解析
- standby redo log、transport 和 apply 正常
- 切换服务名与数据库角色解耦

### 4.2 专用 SYSDG 账号

不要把 `SYS` 密码写入 ClusterGuard HA。创建专用账号：

```sql
CREATE USER CLUSTERGUARD_DG IDENTIFIED BY "<STRONG_PASSWORD>";
GRANT CREATE SESSION TO CLUSTERGUARD_DG;
GRANT SYSDG TO CLUSTERGUARD_DG;
```

主备密码文件中必须包含同一账号，并分别验证：

```bash
sqlplus 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB as sysdg'
sqlplus 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB_B as sysdg'
```

> 以上 `as sysdg` 子句仅用于**人工验证**。产品本身的连接串是 `user/password@connect`，不会追加角色子句；
> 因此该账号必须在密码文件中直接具备 `SYSDG` 权限，而不是依赖连接时提权。

Agent 模式在数据库节点以 `oracle` 操作系统账号运行 `dgmgrl`。数据库节点的受保护环境文件设置：

```bash
CG_ORACLE_BROKER_PASSWORD='<STRONG_PASSWORD>'
```

权限应为 `0600`，不得出现在命令行参数、审计原始返回或 shell history。

### 4.3 Broker 验证

```bash
dgmgrl 'CLUSTERGUARD_DG/<STRONG_PASSWORD>@MESDB as sysdg'
```

在 DGMGRL 中：

```text
SHOW CONFIGURATION;
SHOW DATABASE VERBOSE 'MESDB';
VALIDATE DATABASE VERBOSE 'MESDB';
VALIDATE DATABASE VERBOSE 'MESDB_B';
```

计划切换前，保护模式、transport lag、apply lag、switchover status 和目标库状态必须全部满足策略。ClusterGuard HA 不会把 Broker 报告失败的切换标记为成功。

业务访问应使用角色型服务名和 VIP/SCAN/Listener，不应把固定主机 SID 暴露给应用。切换后由服务角色和监听端点把连接引向新主库。

## 5. SQL Server Always On

### 5.1 Always On 前提

要求：

- Windows Failover Cluster 和 Availability Group 健康
- 每个副本 `replica_id` 稳定
- AG `group_id` 稳定
- 计划切换目标使用同步提交并处于 `SYNCHRONIZED`
- Listener 名称、IP 和端口已配置
- 各副本上的控制账号一致且来源受限

### 5.2 最小权限账号

在每个 SQL Server 实例执行，使用现场密码策略：

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

SQL Server 2022 如发现性能 DMV 权限不足，再按官方权限模型评估授予：

```sql
GRANT VIEW SERVER PERFORMANCE STATE TO cg_monitor;
GRANT VIEW SERVER PERFORMANCE STATE TO cg_operator;
```

不要直接授予 `sysadmin` 作为常规解决方案。

### 5.3 验证

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

计划切换只允许目标副本同步并健康时执行。强制故障转移可能造成数据丢失，当前产品策略会明确阻断不满足条件的动作，不提供假执行。

## 6. 控制节点环境变量

把数据库密码写入 `/etc/clusterguard/clusterguard.env`，权限 `0640`，所有者 `root:clusterguard`：

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

修改密码时先在数据库侧并行创建或更新账号，再滚动更新三个控制节点，最后撤销旧凭据。整个过程必须保持 Raft 多数可用。

## 7. 接入完成验收

每个集群至少验证：

1. 原生身份与平台资源一一对应。
2. 修改 hostname、IP、port 后 `resource_id` 不变，旧 endpoint 进入 alias。
3. 拓扑中的主库、副本和复制源正确。
4. 发现账号不能执行切换。
5. 执行账号不能绕过平台 Safety Guard、Lock、Approval 和 Verify。
6. Writer endpoint 只指向当前可写主库。
7. 操作日志包含时间、原主库、目标主库、结果、验证和报告。
8. 未实现或不安全的能力返回 `unsupported` 或 `blocked`，不能返回伪成功。
