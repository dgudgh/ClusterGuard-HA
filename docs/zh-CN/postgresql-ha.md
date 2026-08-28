# PostgreSQL 高可用性

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../postgresql-ha.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

此操作手册描述了 ClusterGuard HA 中原生的 PostgreSQL 控制路径。
它直接使用 PostgreSQL 流式复制，不需要 Patroni、repmgr 或供应商控制 API。

此功能属于 2.2 版本发布线。它不属于 `v2.1.45` MySQL 支持边界。在确切的 2.2 包和站点接受矩阵通过之前，不要启用 PostgreSQL 的变更操作。

## 提供的功能

| 领域 | 状态 |
| --- | --- |
| 身份和清单 | 不可变平台 UUID 加 `clusterguard.node_id`；`system_identifier` 绑定集群。 |
| 发现和拓扑 | 主/备角色、源身份、时间线、接收/重放 LSN、重放延迟和探测覆盖。 |
| 健康和候选 | 流式/只读检查、晋升资格、确定性 WAL/延迟排名。 |
| 计划切换 | 限制服务停止、WAL 捕获、晋升、兄弟重定向、VIP 转移和验证。 |
| 受保护的故障转移 | 稳定故障、Raft 多数、租约、源隔离或外部隔离、晋升、端点转移和验证。 |
| 原主恢复 | 显式 `pg_rewind` 工作流；在缺少重绕前提条件时，阻止不安全重绕并推荐重建。 |
| 节点生命周期 | 阶段安装加 `pg_basebackup`，或 `pg_rewind` 用于注册的重建目标。 |
| 修复 | 仅允许重放恢复和配置重新加载。 |
| 指标 | 连接、活动连接、事务、死锁、临时字节、块/缓存活动、数据库大小、复制客户端和最长事务。 |
| 审计和报告 | 常见持久操作、验证、审计和报告流水线。 |

功能是配置派生的。缺少操作凭证、Agent 策略、写入端点、多数、生命周期助手或隔离证据会保持相应的控制禁用，并返回明确的阻塞原因。

## 安全不变量

- 主机名、IP 地址和端口是可变坐标。它们从不是数据库资源键。
- 每个实例都有一个不可变平台 `resource_id` 和一个不可变 `clusterguard.node_id` UUID。
- 集群的所有成员必须报告相同的 PostgreSQL `system_identifier`。
- 备用节点必须通过 `clusterguard.primary_node_id` 报告当前主节点的不可变节点 UUID。
- 计划切换在晋升前停止并证明旧主节点不活跃。只读模式本身不被视为硬隔离。
- 故障转移需要多数权限和验证的旧主节点隔离。不可达不是隔离的证明。
- 成功需要一个可写的主节点、一个写入 VIP 所有者、计划的目标身份和验证的跟随者状态。在没有完全验证的情况下进行晋升将导致 `indeterminate`，而不是成功。

## PostgreSQL 先决条件

在主节点和每个同步目标上使用相同的 PostgreSQL 主版本。启用流式复制和重绕所需的设置：
```conf
wal_level = replica
hot_standby = on
max_wal_senders = 10
max_replication_slots = 10
wal_log_hints = on
```

数据校验和可能满足重绕前提条件，而不是 `wal_log_hints`，但启用两者可以提供更强的证据。为最大预期停机时间和基础备份窗口合格 WAL 保留或归档行为。

### 稳定节点身份

为每个物理 PostgreSQL 实例生成不同的 UUID，并通过主机名、IP 和端口更改保留它：

```sql
ALTER SYSTEM SET clusterguard.node_id = '11111111-1111-4111-8111-111111111111';
ALTER SYSTEM SET clusterguard.primary_node_id = '';
ALTER SYSTEM SET clusterguard.hostname = 'pg-01';
SELECT pg_reload_conf();
```

在备用节点上，设置其自身的节点 UUID 和当前主节点 UUID：

```sql
ALTER SYSTEM SET clusterguard.node_id = '22222222-2222-4222-8222-222222222222';
ALTER SYSTEM SET clusterguard.primary_node_id = '11111111-1111-4111-8111-111111111111';
ALTER SYSTEM SET clusterguard.hostname = 'pg-02';
SELECT pg_reload_conf();
```

从新会话中验证这些值。ClusterGuard 拒绝空白或格式错误的 UUID、混合系统标识符和没有有效主节点身份的备用节点。

## 数据库账户

为观察、管理变更和流式复制使用单独的凭证。将 `pg_hba.conf` 限制在控制器和数据库节点地址，可用时要求 TLS，并使用 SCRAM 密钥。

### 发现账户

```sql
CREATE ROLE cg_monitor LOGIN PASSWORD '<random-monitor-secret>';
GRANT CONNECT ON DATABASE postgres TO cg_monitor;
GRANT pg_monitor TO cg_monitor;
```

在每个注册端点上测试此账户。它必须读取控制系统/检查点函数、`pg_stat_activity`、`pg_stat_database`、`pg_stat_replication`、`pg_stat_wal_receiver` 和 WAL LSN 函数。

### 操作账户

当前受保护路径使用 `ALTER SYSTEM` 用于受控只读隔离，并在停止源之前终止客户端后端。使用专用的管理登录。在支持参数级 SET 权限的 PostgreSQL 版本中，仅授予所需的参数和信号权限；否则，站点必须显式限定一个严格持有的管理角色。

```sql
CREATE ROLE cg_operator LOGIN PASSWORD '<random-operation-secret>';
GRANT CONNECT ON DATABASE postgres TO cg_operator;
GRANT pg_monitor TO cg_operator;
GRANT pg_signal_backend TO cg_operator;
GRANT ALTER SYSTEM ON PARAMETER default_transaction_read_only TO cg_operator;
GRANT EXECUTE ON FUNCTION pg_reload_conf() TO cg_operator;
```

`SET` 参数权限不足以满足 ClusterGuard HA，因为计划切换隔离必须跨新会话与 `ALTER SYSTEM` 持续存在。PostgreSQL 预检查使用配置的操作账户验证这些权限，并在任何权限缺失时在租约或数据库状态更改之前阻止执行。

不要静默地用共享的应用或复制账户替换失败的最低权限资格。操作账户权限失败必须阻止预检查或执行。

### 复制账户

```sql
CREATE ROLE clusterguard_repl WITH REPLICATION LOGIN PASSWORD '<random-replication-secret>';
```

相同的标识用于管理 `primary_conninfo` 和 `pg_basebackup`。将密钥保存在受保护的环境文件和 PostgreSQL passfiles 中，而不是命令参数或浏览器字段中。

## 控制器配置

```json
"postgresql": {
  "enabled": true,
  "discovery_interval_seconds": 1,
  "discovery_timeout_seconds": 1,
  "automatic_failover_enabled": false,
  "automatic_failover_interval_seconds": 1,
  "automatic_failover_retry_seconds": 30,
  "discovery": {
    "username": "cg_monitor",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_DISCOVERY_PASSWORD"
  },
  "operation": {
    "username": "cg_operator",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_OPERATION_PASSWORD"
  },
  "replication": {
    "username": "clusterguard_repl",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_REPLICATION_PASSWORD"
  }
}
```

操作和复制凭证是一对全有或全无。没有它们，发现可以保持只读，但变更功能仍然不可用。自动故障转移还需要两个凭证、健康的奇数 Raft 控制平面和受限的 Agent。无效组合在启动时失败。

### 自动故障转移合同

只有生产现场使用的 PostgreSQL 包、服务单元、网络、存储和隔离方式通过下述破坏性验收矩阵后，才能设置 `automatic_failover_enabled`。默认节奏下，控制器需要连续 3 次主库失败观测且时间跨度不少于 3 秒，才会评估接管。这里的 3 秒是故障证据窗口，不是端到端 RTO 承诺。Agent 另有 15 秒授权失效隔离宽限，用来防止失联旧主继续持有写角色或 VIP。

PostgreSQL 16.4 实验室验收中，客户端使用 `connect_timeout=2` 时，写入口中断实测为 17.973 秒。未设置有界连接超时时，曾有一次连接调用阻塞约 32 秒，尽管控制面操作更早完成。因此生产连接串必须设置有界连接超时和重试策略，并从应用写入口测量 RTO，不能只看审计时间。

PostgreSQL 恢复控制器与 MySQL 控制器隔离。它仅从相同的 `system_identifier` 中选择一个排名为一的备用节点，具有当前探测证据、匹配的上游节点 UUID 和时间线、活跃的 WAL 接收和重放、已知零重放延迟。在晋升前，它需要 Raft Leader和多数权限，以及通过签名的 Agent 或外部隔离验证的旧主节点隔离。然后它遵循通用的安全保护、操作锁、内部一次性事件审批、执行、验证、审计和报告流水线。

被阻止或失败的预晋升尝试只能在配置的重试间隔后重新考虑。运行中、成功或不确定的尝试从不盲目重放。晋升等待直到 `pg_is_in_recovery()` 为 false；重定向和原主恢复等待直到审批的不可变源 UUID 和 `primary_conninfo` 都已收敛。

## 受限 Agent 策略

每个数据库节点运行 `clusterguard-agent`，具有特定于集群的策略。该策略固定集群 UUID、平台实例 UUID、PostgreSQL 本地节点 UUID、引擎、服务、OS 用户、数据目录、二进制目录、数据库、passfile、端口、VIP 和对等允许列表。
签名请求是短暂的且受计划限制。Agent 仅暴露固定的命令向量；它不是一个远程 shell。

```json
{
  "cluster_id": "33333333-3333-4333-8333-333333333333",
  "instance_id": "44444444-4444-4444-8444-444444444444",
  "engine": "postgresql",
  "postgresql_node_id": "66666666-6666-4666-8666-666666666666",
  "vip": "192.0.2.110",
  "interface": "ens160",
  "prefix": 24,
  "postgresql_port": 5432,
  "postgresql_service": "clusterguard-postgresql-5432.service",
  "postgresql_user": "postgres",
  "postgresql_data_directory": "/var/lib/clusterguard/postgresql/5432/data",
  "postgresql_binary_directory": "/opt/clusterguard/postgresql/5432/software/bin",
  "postgresql_passfile": "/etc/clusterguard/postgresql/5432.pass",
  "postgresql_database": "postgres",
  "postgresql_replication_user": "clusterguard_repl",
  "postgresql_peers": [
    {
      "instance_id": "55555555-5555-4555-8555-555555555555",
      "node_id": "77777777-7777-4777-8777-777777777777",
      "hostname": "pg-02",
      "ip_address": "192.0.2.21",
      "port": 5432
    }
  ]
}
```

passfile 必须由配置的 PostgreSQL OS 用户拥有，模式为 `0600`，并包含所需的本地管理及对等复制条目。
`instance_id` 是用于路由、租约和响应的不可变 ClusterGuard 平台资源。`postgresql_node_id` 和每个对等 `node_id` 是写入 `clusterguard.node_id`、`clusterguard.primary_node_id` 和复制 `application_name` 的不可变数据库本地标识。它们是有意不同的标识符。未知的服务状态、未列出的平台或本地源标识、更改的源坐标或请求/计划标识不匹配会关闭失败。

## 节点生命周期

仅在三个或更多奇数 Raft 控制平面上启用通用生命周期执行器，并提供 PostgreSQL 助手和只写秘密环境：

```json
"node_lifecycle": {
  "enabled": true,
  "executor_path": "/usr/local/libexec/clusterguard-node-lifecycle.sh",
  "package_repository": "/opt/clusterguard/packages",
  "known_hosts_file": "/etc/clusterguard/known_hosts",
  "identity_file": "/etc/clusterguard/lifecycle_ed25519",
  "jq_binary": "/usr/local/libexec/jq-linux-amd64",
  "mysql_root_password_env": "CG_NODE_MYSQL_ROOT_PASSWORD",
  "replication_password_env": "CG_NODE_MYSQL_REPLICATION_PASSWORD",
  "postgresql_install_helper": "/usr/local/libexec/clusterguard-postgresql-install.sh",
  "postgresql_sync_helper": "/usr/local/libexec/clusterguard-postgresql-sync.sh",
  "postgresql_admin_password_env": "CG_NODE_POSTGRESQL_ADMIN_PASSWORD",
  "postgresql_replication_password_env": "CG_NODE_POSTGRESQL_REPLICATION_PASSWORD",
  "postgresql_basebackup_available": true,
  "postgresql_rewind_available": true
}
```

控制台仅显示此功能响应启用的方法：

- `pg_basebackup` 是新节点或发散节点的基线；
- `pg_rewind` 仅适用于具有兼容系统身份和重绕前提条件的注册重建目标。

助手停止目标，将数据移出活动目录，验证 `PG_VERSION` 和 `system_identifier`，原子地交换数据目录，启动服务，并验证只读恢复、上游 UUID、流式传输和写主 VIP 的缺失。验证失败会将目标停止。

## 注册、观察和操作

仅使用 `engine: "postgresql"` 注册权威数据库端点，然后发布当前观察：

```bash
curl -sS -X POST https://controller.example/api/v1/clusters \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'content-type: application/json' \
  -d '{
    "display_name":"payments-postgresql",
    "engine":"postgresql",
    "endpoints":[
      {"hostname":"pg-01","ip_address":"192.0.2.20","port":5432},
      {"hostname":"pg-02","ip_address":"192.0.2.21","port":5432},
      {"hostname":"pg-03","ip_address":"192.0.2.22","port":5432}
    ]
  }'

curl -sS -X POST https://controller.example/api/v1/clusters/<cluster-uuid>/discover \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'content-type: application/json' -d '{}'
```

第一次成功的观察在完全覆盖活动端点后绑定 `system_identifier`。之后的主机名、IP 或端口更改会协调相同的节点资源，并将之前的坐标记录为别名。

手动切换使用通用 `/api/v1/operations/execute` 路由或认证控制台。服务器构建不可变计划，并为授权平台用户内部消耗一次性审批授予。服务自动化使用显式计划绑定的授予 API。这两条路径都不接受可重复使用的审批密码。

## 故障转移和隔离

计划切换可以通过受限 Agent 停止并重新检查源来证明隔离。不可达的主节点无法提供该证明。因此，受保护的故障转移还需要一个特定于站点的外部隔离，隔离电源、虚拟机管理程序、云实例、PDU、BMC 或等效写入访问，然后独立报告 `fenced:true`。

网络可达性、ICMP 失败、数据库超时、只读设置和 VIP 缺失不是隔离。没有多数权限和验证的隔离，ClusterGuard 会选择不可用而不是可能的第二个写主。

## 指标和监控

使用这些无依赖的端点：

```text
GET /api/v1/clusters/{id}/health
GET /api/v1/clusters/{id}/metrics
GET /api/v1/clusters/{id}/metrics/prometheus
GET /api/v1/monitoring/health
```

控制台渲染 PostgreSQL 本地名称和累积值，而不是将它们标记为 MySQL QPS/TPS。未知延迟或可选事务年龄被省略，而不是合成零。

## 生产资格

在生产集群上启用变更之前，在由相同 PostgreSQL 包和服务布局构建的隔离环境中证明以下所有内容：

1. 完整发现正好绑定一个主节点和所有备用节点身份。
2. 候选拒绝适用于时间线不匹配、暂停重放、未知延迟、过时探测证据和外系统标识符。
3. 计划切换在保留一个写主和一个 VIP 所有者的同时，轮换通过每个节点。
4. 旧主恢复在重绕前提条件缺失时成功重绕并阻止重建。
5. `pg_basebackup` 重建不会留下仅目标数据并重新加入流式传输。
6. 控制器少数不能执行变更。
7. 主节点网络分区在外部隔离证明隔离之前保持阻塞。
8. 进程和主机重启保留资源身份、操作历史、审计、报告、端点租约和控制平面多数。
9. 晋升后的失败报告为 `indeterminate`，从不盲目重试。
10. 备份和时间点恢复保持独立测试；HA 不是备份的替代。
