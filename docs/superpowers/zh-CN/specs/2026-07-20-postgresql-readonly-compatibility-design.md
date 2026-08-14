# PostgreSQL 只读兼容性设计

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../specs/2026-07-20-postgresql-readonly-compatibility-design.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 目标

使 PostgreSQL 在 ClusterGuard HA 中成为可用的一等只读引擎，同时确保所有修改 PostgreSQL 的操作都失败封闭。首次交付涵盖注册、计划发现、不可变身份、拓扑、健康、复制延迟、候选评估、监控命名空间和控制台展示。

## 选择的方法

通过一个小型适配器拥有的查询执行器使用已安装的 `psql` 客户端。这与现有的部署模型相匹配，避免将数据库驱动引入控制平面二进制文件，并将凭据保留在命令行参数之外。执行器使用 `PGPASSWORD`、`PGCONNECT_TIMEOUT`、`--no-password` 和 `--no-psqlrc`；SQL 返回每行一个 JSON 对象，因此值不会破坏分隔符解析器。

不需要 Patroni。Patroni、repmgr 和特定供应商的 API 可能成为以后的可选提供者，但 PostgreSQL 兼容性必须首先针对原生流复制工作。

## 身份契约

- 集群身份是来自 `pg_control_system()` 的 PostgreSQL `system_identifier`。
- 每个服务器必须在自定义设置 `clusterguard.node_id` 中暴露一个不可变的 UUID。
- 一个备用服务器必须在 `clusterguard.primary_node_id` 中暴露其上游节点的 UUID。
- 适配器在 `engine_identity` 中发布 `system_identifier` 和 `resource_id`；主机名、IP 和端口仍然是可变的端点。
- 包含不同 `system_identifier` 值的刷新在发布前被拒绝。
- 第一次成功的刷新将集群绑定到其系统标识符。未来的不匹配将被拒绝，而不会更改持久化的快照。

数据库设置是此阶段的只读输入。ClusterGuard 不会静默创建或重写它们。

## 发现与健康

一个身份查询收集版本、恢复状态、只读状态、时间线、WAL 位置、重放暂停状态、接收器状态、重放时间戳延迟和 ClusterGuard 身份设置。

- 一个主节点只有在不在恢复中且可写时才是健康的。
- 一个备用节点只有在恢复中、只读、流式传输、重放，并且具有有效的上游节点身份时才是健康的。
- 未知的重放时间戳产生未知的延迟；它永远不会报告为零。
- 一个备用拓扑边从 `primary_node_id` 到 `node_id` 构建。
- `replica` 和 `standby` 角色都参与通用拓扑健康和全局资源副本计数。

## 候选评估

候选评估是只读的。它需要一个健康的备用节点、匹配的 `system_identifier`、与当前主节点匹配的上游身份、流式接收和重放状态、可解析的重放 LSN，以及在策略范围内的延迟。候选者按最高的重放 LSN 排序，然后是最低的延迟，然后是不可变的资源 ID。未知的延迟或缺失的证据会阻止资格。

PostgreSQL 候选评估不解释仅适用于 MySQL 的 GTID 策略。

## 功能与安全性

可用的功能：

- discover
- topology
- health
- candidates
- metadata reconciliation precheck

不可用的功能：

- metrics 直到定义了真实的计数契约
- precheck, plan, execute, verify
- node synchronization

每个不可用的方法返回 `adapter.ErrUnsupported`。当选择 PostgreSQL 集群时，控制台会禁用执行和 MySQL 特定的生命周期控制。

## 配置与调度

添加一个带有 `enabled`、发现间隔、发现超时和专用发现凭据的 `postgresql` 配置块。凭据可以命名维护数据库，默认为 `postgres`。

MySQL 和 PostgreSQL 获得独立的引擎过滤调度器，因此禁用一个引擎不会探测或降级属于该引擎的集群。

## 监控与控制台

Prometheus 和 Zabbix 指标名称从集群引擎中选择，而不是硬编码为 MySQL。PostgreSQL 当前从拓扑快照中导出复制延迟；合成的 QPS/TPS 值被禁止。

拓扑清单渲染适合所选引擎的原生身份。PostgreSQL 备用节点使用备用语言，不支持的执行或节点生命周期控制会解释原因，而不是接受请求。

## 故障语义

凭据失败、格式错误的 JSON、缺少身份、无效的 UUID、混合的系统标识符、未知的上游身份、暂停的重放、非流式 WAL 接收器和过时的清单都会失败封闭。刷新失败不会发布部分拓扑或修改集群身份。

## 验证

- 适配器单元测试使用固定行和假执行器。
- 执行器测试验证密码保密性、超时处理、JSON 解析和取消。
- 发现/存储测试验证首次身份绑定、不匹配拒绝和主机名/端口更改而没有重复实例。
- 运行时/配置测试验证独立凭据和调度器。
- API/控制台测试验证引擎特定的监控名称和禁用的变更控制。
- `go test ./...`、`go test -race ./...`、`go vet ./...`、JSON 验证和 `git diff --check` 必须通过。
