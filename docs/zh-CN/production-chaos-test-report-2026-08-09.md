# ClusterGuard HA 生产前破坏性测试报告

- 日期：2026-08-09
- 环境：`192.168.102.152`、`192.168.102.153`、`192.168.102.154`
- 控制面：3 节点 Raft
- 重点集群：MySQL 8.0.44（3306）、MySQL 8.4.10（3384）和 MySQL 9.7.1（3397）
- 结论：本轮软件测试矩阵通过；生产上线仍应配置外部 fencing，并完成生产网络、存储和备份恢复专项验收。

## 1. 测试目标

本轮验证以下生产不变量：

1. 任意时刻每个数据库集群最多只有一个可写主库。
2. 每个 HA VIP 最多只由一台主机持有，并且必须跟随当前主库。
3. 控制面无 quorum、无法确认 fencing 或状态不确定时必须 fail-closed。
4. 故障节点恢复后先保持只读，经验证后才能重新加入复制拓扑。
5. 同一集群并发操作、重复请求和过期计划不能造成重复执行。
6. 节点重启后控制面、Agent、数据库角色、复制链路和 VIP 能自动收敛。

## 2. 测试矩阵与结果

| 场景 | 执行方式 | 结果 |
|---|---|---|
| 三节点整机关机与恢复 | 连续 3 轮关闭全部虚拟机，并使用不同开机顺序恢复 | 通过；Raft 恢复 3 voters，数据库角色、复制和 VIP 自动收敛，无双主、双 VIP |
| 当前主库网络分区 | 隔离 3306 当前主库约 90 秒 | 通过；节点约 34 秒后本地隔离并释放 VIP，因缺少外部 fencing 不进行不安全提升；网络恢复后约 16 秒重新取得唯一 VIP 和写权限 |
| 9.7 当前主库网络分区 | 将 3397 当前主库与另外两个节点双向隔离 | 通过；三实例均只读且 VIP 释放，没有提升无法确认已隔离的旧主；清理规则后恢复为唯一主库和唯一 VIP |
| Leader 与主库同时分区 | 隔离当时兼任 Raft Leader 和 3384 主库的 `.153` 约 75 秒 | 通过；`.154` 在 5 秒内成为 Leader，2 节点保持 quorum；孤立节点自隔离，恢复后安全收敛 |
| 副本存储不可用 | 停止 `.154:3306`，将 `/data/mysql8/data` 权限改为 `000` 后启动 | 通过；实例不可用并失去候选资格，预检查失败，真实执行在变更前返回 HTTP 409 |
| 8.4 主库存储只读 | 将当前主库数据目录 bind-remount 为只读，systemd 进入无 PID auto-restart | 通过；故障节点确认无存活数据库进程后完成安全接管；存储恢复后旧主增量回挂 |
| 存储恢复与追平 | 恢复目录权限和服务，故障期间在主库写入 500 行 | 通过；副本恢复后 IO/SQL 线程运行、延迟归零、500 行完整追平，临时库已清理 |
| 8.4 binlog 缺口 | 故障期间写入后 purge 旧主追平所需 binlog | 通过；`required_binlog_available` 和 `rebuild_required` 阻断增量回挂，自动全量重建成功 |
| 9.7 errant GTID | 在隔离旧主制造当前主库不存在的事务 | 通过；GTID 子集检查失败，自动全量重建，分叉事务未带回当前拓扑 |
| 同集群并发切换 | 同时向两个不同候选提交 3306 切换 | 通过；一个操作成功，一个被 HTTP 409 阻断，只发生一次主库变更 |
| 相同幂等键并发提交 | 两个请求使用同一 idempotency key | 通过；共享同一 operation ID，一个执行，一个返回 `operation is running`，仅生成一套执行/验证/报告 |
| 跨集群并发切换 | 3306 与 3384 同时操作 | 通过；8.0 operation `7b112de1-...` 与 8.4 operation `e34b49d2-...` 均 succeeded，执行后验证全部通过 |
| 真实控制台切换 | 在 Web 控制台锁定并执行 3306 主库和 VIP 联动切换 | 通过；五阶段全部成功，操作日志记录源/目标/时间，原始报告可展开，最终拓扑验证通过 |
| 最终稳定观察 | 连续 60 秒采集 8.0、8.4、9.7 的角色、只读状态、复制线程、链路和延迟 | 通过；7 个采样点均为单主、双只读副本、线程运行、链路健康、最大延迟 0 秒 |

## 3. 本轮发现并修复

### 控制台未处理安全的 `stale_plan`

跨集群并发时，拓扑版本可能在计划生成后、执行提交前发生变化。后端正确地以 `stale_plan` 阻断，但控制台原先只显示失败，必须由操作员重新点击。

修复后的控制台仅在以下条件全部满足时自动重建计划并重试：

- HTTP 409；
- `failure_class=stale_plan`；
- `status=blocked`；
- 阶段为 `precheck` 或 `plan`；
- `committed` 不为 `true`；
- 最多 3 次；
- 始终保留用户最初选择的目标节点；
- 每次使用新的幂等键。

执行阶段错误、已提交操作、`indeterminate`、一般 409 和其他失败不会自动重试。

部署后已通过真实控制台完成 3306 切换，结果为 `操作完成，验证通过`。操作日志记录：

- 时间：2026-08-09 19:18:59 CST
- 源节点：`orch-mysql03:3306`
- 目标节点：`orch-mysql01:3306`
- 状态：成功

### 旧主恢复首次点击读取旧快照

旧主服务刚恢复时，控制台可能仍持有停机前的拓扑快照，导致第一次点击“一键恢复为从库”错误读取旧主只读状态并被预检查阻断。

修复后，旧主恢复固定先调用当前集群的 discover，再重新加载所选集群，随后才执行恢复预检查。该顺序由控制台契约测试覆盖；discover 或重新加载失败时不会继续执行恢复。

### 手工部署产物架构检查

测试中曾误将 macOS 构建产物复制到 Linux 节点，systemd 以 `203/EXEC` 拒绝启动。Raft 多数派和数据库服务未受影响，随后使用以下参数重新构建并滚动部署：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o clusterguard ./cmd/clusterguard
```

三节点最终滚动部署后二进制 SHA-256 一致：

```text
clusterguard       d9a4d49c2e02d624307b8d2219cc70911cde5bbf63a7e52f8a9d4e70af4c1214
clusterguard-agent 29791923d37286172f23c923a61d9227c1d9d98360959069ba843baa959434fb
```

正式交付必须使用 RPM/离线包构建流程，并在部署前校验 ELF 架构和摘要，禁止直接复制开发机默认产物。

## 4. 最终现场状态

### 控制面

- 当前 Leader：`192.168.102.154`
- Raft：3 voters，quorum confirmed
- `mutation_authority=true`
- `snapshot_cas_active=true`
- `commit_index=applied_index`
- `ready=true`
- 活动操作：0
- 活动节点任务：0
- `clusterguard-ha.service`：三台均 active/enabled
- `clusterguard-agent.service`：三台均 active/enabled
- `clusterguard-agent-reconcile.timer`：三台均 active/waiting/enabled
- `systemctl --failed`：三台均为空

测试元数据中仍保留 15 条历史 `indeterminate` operation 和 14 条历史
`indeterminate` 节点任务，当前没有活动执行。它们应保留审计或在生产迁移前
逐条完成处置，不应通过删除审计记录伪装为零。

### MySQL 8.0.44 / 3306

- 主库：`192.168.102.153`（`orch-mysql02:3306`），可写
- 从库：`.152`、`.154`，均 `read_only=true`、`super_read_only=true`
- 两个从库 IO/SQL 线程均 running，延迟 0
- 半同步主端状态正常，客户端数 2
- VIP：`192.168.102.155`，仅 `.153` 持有
- VIP 握手：MySQL protocol 10，server `8.0.44`

### MySQL 8.4.10 / 3384

- 主库：`192.168.102.154`（`orch-mysql03:3384`），可写
- 从库：`.152`、`.153`，均 `read_only=true`、`super_read_only=true`
- 两个从库 IO/SQL 线程均 running，延迟 0
- 半同步主端状态正常，客户端数 2
- VIP：`192.168.102.160`，仅 `.154` 持有
- VIP 握手：MySQL protocol 10，server `8.4.10`

### MySQL 9.7.1 / 3397

- 主库：`192.168.102.152`（`orch-mysql01:3397`），可写
- 从库：`.153`、`.154`，均 `read_only=true`、`super_read_only=true`
- 两个从库复制线程均 running，延迟 0
- VIP：`192.168.102.164`，仅 `.152` 持有
- 三节点确定性数据均为 2005 行，按
  `SUM(CRC32(CONCAT(id,marker,HEX(payload))))` 计算的摘要均为 `4304498633408`

同一公式下，8.0 三副本均为 2007 行、摘要 `4323827613648`；8.4 三副本
均为 2006 行、摘要 `4341221981770`。

平台同时注册的 Oracle、PostgreSQL 和 UPSQL 当前健康；SQL Server 集群当前为 degraded（database probe failed），不属于本轮 MySQL 生产矩阵，正式平台上线前必须单独处置。

## 5. 代码门禁

以下检查均通过：

```bash
go test ./... -count=1
go test -race ./adapters/mysql ./internal/api ./internal/agent \
  ./internal/coordination ./internal/lifecycle ./internal/workflow -count=1
go build ./...
go vet ./...
git diff --check
```

控制台契约测试同时覆盖 `stale_plan` 安全重试边界和旧主恢复前强制刷新发现；执行阶段、已提交、不确定操作以及 discover 失败均不会继续执行变更。

## 6. 生产上线边界

本轮证明 ClusterGuard HA 在当前三节点实验室中满足安全优先的不变量，但不能据此声明所有生产风险为零。

上线前必须完成：

1. 配置独立于业务网络的 BMC/IPMI、存储侧或虚拟化侧 fencing。当前没有可靠外部 fencing 时，完全隔离的主库会选择停止写服务，不会冒险提升新主；这避免脑裂，但会牺牲可用性。
2. 在生产交换机、防火墙和真实链路上复跑单向丢包、双向隔离、抖动和延迟测试。
3. 在生产存储类型上复跑只读文件系统、I/O hang、空间耗尽、inode 耗尽和 fsync 延迟测试。
4. 用生产备份执行一次裸机恢复和时间点恢复，验证 RPO/RTO，而不只是复制恢复。
5. 使用全新的生产元数据存储，或对 15 条历史 `indeterminate` 测试记录逐条形成处置结论。
6. 单独修复并验收当前 SQL Server probe degraded，避免平台总览带病上线。

## 7. 判定

**MySQL 8.0、8.4 和 9.7 当前软件版本在本实验室的关机恢复、网络分区、存储故障、并发操作、旧主增量回挂和全量重建测试通过。生产准入判定为“软件门禁通过，外部 fencing 与生产基础设施专项验收待完成”。**
