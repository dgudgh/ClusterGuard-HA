# ClusterGuard HA MySQL 功能一致性实现计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-13-mysql-feature-parity.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于代理工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项实施此计划任务。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标：** 交付之前原型的真正 MySQL HA、VIP、恢复、节点生命周期、监控、控制台和安装能力，作为独立的 ClusterGuard HA 实现。

**架构：** 控制平面拥有 UUID 范围的资源、持久工作流、多数派租约、安全门、审计和报告。MySQL 适配器拥有方言和复制变更。受限的 `clusterguard-agent` 拥有特权本地 VIP、角色持久性和生命周期操作；适配器从不绕过平台工作流。

**技术栈：** Go 1.22+，标准库 HTTP/JSON/crypto/process 包，MySQL CLI 协议适配器，Linux `ip`/`arping`，systemd，基于 shell 的安装阶段，HTML/CSS/JavaScript 控制台。

## 全局约束

- 不复制或依赖之前产品的源代码、API、元数据表、包、二进制文件、配置键或名称。
- 使用不可变平台 UUID 和 MySQL `server_uuid`；从不使用 `hostname:port` 作为持久键。
- 每个真实变更必须通过 `DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`。
- 写节点角色和 VIP 所有权是耦合的；成功需要恰好一个可写实例和恰好一个 VIP 所有者。
- 未知的多数派、隔离、拓扑、端点或探测证据默认关闭。
- 密钥在服务器端解析，用途特定，并且不在 API、CLI 参数、持久状态、审计和报告中出现。
- 必须测试 MySQL 5.7、8.0、8.4 和实验室的 9.x 兼容方言。
- PostgreSQL、Oracle 和 SQL Server 适配器的骨架行为必须保持不变，不支持的操作必须保持阻塞。

---

### 任务 1：用途特定的 MySQL 凭据

**文件：**
- 修改：`pkg/adapter/adapter.go`
- 修改：`internal/config/config.go`
- 修改：`internal/runtime/runtime.go`
- 修改：`configs/clusterguard.example.json`
- 修改：`packaging/systemd/clusterguard.env.example`
- 测试：`internal/config/config_test.go`
- 测试：`internal/runtime/runtime_test.go`

**接口：**
- 生成：`adapter.MySQLCredentials{Discovery, Operation, Replication Credentials}`。
- 生成：`config.MySQLConfig` 引用三个独立的环境密钥。
- 消耗：现有的服务器端环境解析和删除行为。

- [ ] **步骤 1：编写失败的凭据分离测试**

```go
func TestRuntimeResolvesIndependentMySQLCredentials(t *testing.T) {
	configuration := config.Config{MySQL: config.MySQLConfig{
		Enabled: true, Discovery: config.CredentialRef{Username: "discover", PasswordEnv: "CG_DISCOVERY"},
		Operation: config.CredentialRef{Username: "operator", PasswordEnv: "CG_OPERATION"},
		Replication: config.CredentialRef{Username: "replicator", PasswordEnv: "CG_REPLICATION"},
	}}
	// Assert all three resolved values differ and no password appears in serialized config.
}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/config ./internal/runtime -run 'Credential|Credentials' -count=1`
预期：FAIL，因为 `CredentialRef` 和独立运行时凭据不存在。

- [ ] **步骤 3：实现凭据合同**

```go
type MySQLCredentials struct {
	Discovery  Credentials
	Operation  Credentials
	Replication Credentials
}

type CredentialRef struct {
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
}
```

在启动时解析每个密码一次，拒绝缺失的必需值，仅将发现凭据传递给刷新，仅通过 `ResolvedOperation` 传递操作/复制凭据。

- [ ] **步骤 4：验证 GREEN 和回归**

运行：`go test ./internal/config ./internal/runtime ./pkg/adapter -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add pkg/adapter internal/config internal/runtime configs packaging/systemd
git commit -m "feat: separate MySQL control credentials"
```

### 任务 2：HA 端点清单和 API

**文件：**
- 修改：`pkg/model/model.go`
- 修改：`internal/store/repository.go`
- 创建：`internal/store/ha_endpoints.go`
- 修改：`internal/api/clusters.go`
- 修改：`internal/api/server.go`
- 测试：`internal/store/ha_endpoints_test.go`
- 测试：`internal/api/clusters_test.go`

**接口：**
- 生成：`Repository.PutHAEndpoint`, `Repository.HAEndpoint`, `Repository.HAEndpoints`。
- 生成：`GET/POST /api/v1/clusters/{id}/ha-endpoints`。
- 消耗：注册的集群和实例 UUID 清单。

- [ ] **步骤 1：编写失败的唯一性和 API 测试**

```go
func TestPutHAEndpointAllowsOneActiveVIPPerCluster(t *testing.T) {}
func TestPutHAEndpointRejectsVIPSharedByTwoClusters(t *testing.T) {}
func TestHAEndpointRejectsUnknownOwner(t *testing.T) {}
func TestClusterHAEndpointAPIRejectsSecretAndUnknownFields(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/store ./internal/api -run HAEndpoint -count=1`
预期：FAIL，因为 HA 端点持久性和路由不存在。

- [ ] **步骤 3：实现持久端点资源**

```go
type HAEndpointSpec struct {
	ClusterID  model.ResourceID
	Kind       model.EndpointKind
	IPAddress  string
	Interface  string
	Prefix     int
	OwnerID    model.ResourceID
	Active     bool
}
```

持久化接口和前缀作为显式的 HA 端点属性，验证 IPv4 地址和前缀，要求一个清单所有者，禁止跨集群的重复活动 VIP 地址，并保留资源修订。

- [ ] **步骤 4：验证 GREEN**

运行：`go test ./internal/store ./internal/api -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add pkg/model internal/store internal/api
git commit -m "feat: add durable HA endpoint inventory"
```

### 任务 3：受限 ClusterGuard 代理

**文件：**
- 创建：`cmd/clusterguard-agent/main.go`
- 创建：`internal/agent/config.go`
- 创建：`internal/agent/protocol.go`
- 创建：`internal/agent/vip.go`
- 创建：`internal/agent/role.go`
- 创建：`internal/agent/command.go`
- 创建：`internal/agent/protocol_test.go`
- 创建：`internal/agent/vip_test.go`
- 创建：`packaging/systemd/clusterguard-agent.service`
- 创建：`configs/clusterguard-agent.example.json`

**接口：**
- 生成：stdin/stdout 上的换行分隔 JSON 请求/响应合同。
- 生成命令 `vip_status`, `vip_acquire`, `vip_release`, `self_isolate`, 和 `persist_role`。
- 消耗：签名的操作上下文和允许的集群/VIP 配置。

- [ ] **步骤 1：编写失败的协议和命令验证测试**

```go
func TestAgentRejectsUnknownCommand(t *testing.T) {}
func TestAgentRejectsVIPOutsideAllowlist(t *testing.T) {}
func TestAgentAcquireIsIdempotentAndSendsGARP(t *testing.T) {}
func TestAgentSelfIsolationRemovesVIPBeforeReadOnly(t *testing.T) {}
func TestAgentRedactsSecretsFromErrors(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/agent ./cmd/clusterguard-agent -count=1`
预期：FAIL，因为代理包不存在。

- [ ] **步骤 3：实现狭窄的命令执行器**

```go
type Request struct {
	Command string `json:"command"`
	ClusterID model.ResourceID `json:"cluster_id"`
	OperationID model.ResourceID `json:"operation_id"`
	LeaseID model.ResourceID `json:"lease_id,omitempty"`
	PlanDigest string `json:"plan_digest"`
	VIP string `json:"vip,omitempty"`
	Interface string `json:"interface,omitempty"`
	Prefix int `json:"prefix,omitempty"`
}
```

使用绝对命令路径，验证每个参数，捕获有界输出，拒绝环境覆盖，并提供用于测试命令假的接口。

- [ ] **步骤 4：验证 GREEN、构建和审查**

运行：`go test ./internal/agent ./cmd/clusterguard-agent -count=1 && go build ./cmd/clusterguard-agent && go vet ./internal/agent/...`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add cmd/clusterguard-agent internal/agent packaging/systemd/clusterguard-agent.service configs/clusterguard-agent.example.json
git commit -m "feat: add restricted data node agent"
```

### 任务 4：真正的 Linux VIP 提供者

**文件：**
- 创建：`internal/endpoint/transport.go`
- 创建：`internal/endpoint/agent_transport.go`
- 创建：`internal/endpoint/linux_vip.go`
- 创建：`internal/endpoint/lease.go`
- 创建：`internal/endpoint/linux_vip_test.go`
- 创建：`internal/endpoint/lease_test.go`
- 修改：`pkg/adapter/adapter.go`
- 修改：`internal/runtime/runtime.go`

**接口：**
- 生成：实现 `adapter.HAEndpointProvider` 的 `endpoint.LinuxVIPProvider`。
- 生成：集群范围的 `Owners`, `Transfer`, 和 `Verify` 证据。
- 消耗：HA 端点清单、代理传输和活动端点租约。

- [ ] **步骤 1：编写失败的提供者测试**

```go
func TestVIPPrecheckFailsOnUnknownProbeCoverage(t *testing.T) {}
func TestVIPPrecheckFailsWithTwoOwners(t *testing.T) {}
func TestVIPTransferReleasesEveryNonTargetBeforeAcquire(t *testing.T) {}
func TestVIPTransferDoesNotAcquireWithoutActiveLease(t *testing.T) {}
func TestVIPVerifyRequiresExactlyOneTargetOwner(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/endpoint -count=1`
预期：FAIL，因为端点提供者不存在。

- [ ] **步骤 3：实现关闭失败的所有权转移**

```go
type OwnerObservation struct {
	InstanceID model.ResourceID
	Reachable bool
	OwnsVIP bool
}

type LeaseStore interface {
	Acquire(context.Context, LeaseRequest) (model.EndpointLease, error)
	Validate(context.Context, model.EndpointLease) error
	Release(context.Context, model.ResourceID) error
}
```

使用有界超时并发探测所有清单实例。当任何结果未知时拒绝获取。释放非目标所有者，证明零所有者，获取目标，发送 GARP，并证明目标唯一所有权。

- [ ] **步骤 4：验证 GREEN 和适配器集成**

运行：`go test ./internal/endpoint ./internal/runtime ./adapters/mysql -count=1`
预期：PASS，并且当提供者和变更凭据配置时，MySQL 执行能力才可用。

- [ ] **步骤 5：提交**

```bash
git add internal/endpoint internal/runtime pkg/adapter
git commit -m "feat: provide verified Linux VIP ownership"
```

### 任务 5：三节点计划切换

**文件：**
- 修改：`adapters/mysql/switchover.go`
- 创建：`adapters/mysql/reparent.go`
- 创建：`adapters/mysql/role_persistence.go`
- 修改：`pkg/adapter/adapter.go`
- 修改：`internal/workflow/resolver.go`
- 测试：`adapters/mysql/switchover_test.go`
- 测试：`adapters/mysql/switchover_execution_test.go`
- 创建：`adapters/mysql/reparent_test.go`

**接口：**
- 生成：包含每个跟随者 UUID 和重新父节点后置条件的计划。
- 生成：带有版本感知 GTID 源语法的 `ReplicaReparenter`。
- 消耗：操作和复制凭据以及真实端点提供者。

- [ ] **步骤 1：将双节点期望替换为失败的三节点测试**

```go
func TestSwitchoverPrecheckAcceptsHealthyThreeNodeTopology(t *testing.T) {}
func TestSwitchoverPlanIncludesFormerPrimaryAndSiblingReparent(t *testing.T) {}
func TestSwitchoverBlocksSiblingWithErrantGTID(t *testing.T) {}
func TestSwitchoverExecutesReparentBeforeVIPTransfer(t *testing.T) {}
func TestSwitchoverVerifyRequiresEveryFollowerOnNewPrimary(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./adapters/mysql -run 'ThreeNode|Sibling|Reparent' -count=1`
预期：在现有恰好两个成员范围检查上失败。

- [ ] **步骤 3：实现不可变跟随者计划和重新父节点**

```go
type PlannedFollower struct {
	InstanceID model.ResourceID `json:"instance_id"`
	ServerUUID string `json:"server_uuid"`
	Endpoint adapter.Endpoint `json:"endpoint"`
	MetadataRevision uint64 `json:"metadata_revision"`
}
```

在计划摘要中包含所有跟随者。隔离源，捕获 GTID，等待目标，提升目标，使用 GTID 自动定位附加前源和兄弟节点，验证线程/源 UUID，然后转移 VIP。通过代理持久化角色状态。

- [ ] **步骤 4：验证 GREEN 和竞争行为**

运行：`go test -race ./adapters/mysql ./internal/workflow -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add adapters/mysql pkg/adapter internal/workflow
git commit -m "feat: execute three node MySQL switchover"
```

### 任务 6：前主节点重新加入和低风险修复

**文件：**
- 创建：`adapters/mysql/repair.go`
- 创建：`adapters/mysql/rejoin.go`
- 创建：`adapters/mysql/repair_test.go`
- 创建：`adapters/mysql/rejoin_test.go`
- 修改：`pkg/model/workflow.go`
- 修改：`internal/api/operations.go`
- 修改：`internal/api/server.go`

**接口：**
- 生成操作类型 `former_primary_rejoin` 和 `replication_repair`。
- 生成允许的修复操作 `refresh`, `collect_replication_status`, `start_io_thread`, `start_sql_thread`, `begin_maintenance`, `end_maintenance`。
- 消耗：当前拓扑、原生标识、GTID 关系和操作门。

- [ ] **步骤 1：编写失败的修复安全测试**

```go
func TestFormerPrimaryFastRejoinRequiresSubsetGTID(t *testing.T) {}
func TestFormerPrimaryWithErrantGTIDRequiresRebuild(t *testing.T) {}
func TestFormerPrimaryRejoinRefusesLocalVIP(t *testing.T) {}
func TestRepairRejectsSkipTransactionAndResetReplica(t *testing.T) {}
func TestRepairStartsOnlyRequestedStoppedThread(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./adapters/mysql ./internal/api -run 'FormerPrimary|Repair' -count=1`
预期：FAIL，因为这些操作类型不被支持。

- [ ] **步骤 3：实现修复允许列表和重新加入计划**

快速重新加入使前主节点只读，证明没有 VIP，将当前主节点配置为 GTID 源，启动线程，并验证源 UUID 和延迟。GTID 发散返回结构化重建建议，不进行变更。

- [ ] **步骤 4：验证 GREEN**

运行：`go test -race ./adapters/mysql ./internal/api ./internal/workflow -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add adapters/mysql pkg/model internal/api
git commit -m "feat: repair and rejoin MySQL replicas"
```

### 任务 7：多数派租约、故障转移和自我隔离

**文件：**
- 创建：`internal/coordination/membership.go`
- 创建：`internal/coordination/lease.go`
- 创建：`internal/coordination/membership_test.go`
- 创建：`internal/coordination/lease_test.go`
- 创建：`adapters/mysql/failover.go`
- 创建：`adapters/mysql/failover_test.go`
- 修改：`internal/workflow/gates.go`
- 修改：`internal/runtime/runtime.go`

**接口：**
- 生成：仅Leader、多数派支持的集群和端点租约。
- 生成：30 秒的稳定故障观察策略。
- 消耗：隔离提供者、候选人评估、端点提供者、代理自我隔离。

- [ ] **步骤 1：编写失败的多数派和故障转移测试**

```go
func TestLeaseRejectedWithoutControllerMajority(t *testing.T) {}
func TestConflictingEndpointLeaseIsRejected(t *testing.T) {}
func TestMinorityAgentSelfIsolates(t *testing.T) {}
func TestFailoverWaitsForSixStableChecks(t *testing.T) {}
func TestFailoverBlocksUnfencedOldPrimary(t *testing.T) {}
func TestFailoverPromotesLowestDataLossCandidate(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/coordination ./adapters/mysql -run 'Lease|Failover|Minority' -count=1`
预期：FAIL，因为协调和故障转移不存在。

- [ ] **步骤 3：实现持久协调和故障转移计划**

使用奇数控制器成员，单调租约期限/索引，每个集群/VIP 一个活动租约，30 秒 TTL，仅Leader授予。故障转移需要隔离证据，并使用通用执行/验证/审计/报告工作流。

- [ ] **步骤 4：验证 GREEN 包括重启恢复**

运行：`go test -race ./internal/coordination ./adapters/mysql ./internal/workflow -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add internal/coordination adapters/mysql/failover.go adapters/mysql/failover_test.go internal/workflow internal/runtime
git commit -m "feat: coordinate guarded MySQL failover"
```

### 任务 8：节点同步和生命周期

**文件：**
- 创建：`internal/lifecycle/model.go`
- 创建：`internal/lifecycle/planner.go`
- 创建：`internal/lifecycle/executor.go`
- 创建：`internal/lifecycle/planner_test.go`
- 创建：`internal/lifecycle/executor_test.go`
- 创建：`adapters/mysql/node_sync.go`
- 创建：`adapters/mysql/node_sync_test.go`
- 创建：`scripts/clusterguard-node-install.sh`
- 创建：`scripts/clusterguard-mysql-sync.sh`
- 修改：`internal/api/server.go`

**接口：**
- 生成数据、控制器、混合和前节点重建角色的节点计划。
- 生成同步选择 `clone`, `xtrabackup`, 或 `logical_dump`。
- 消耗：仅通过执行器环境和删除任务状态的安装凭据。

- [ ] **步骤 1：编写失败的生命周期策略测试**

```go
func TestDataNodeCountIsUnlimited(t *testing.T) {}
func TestControllerFinalMembershipMustBeOdd(t *testing.T) {}
func TestFormerNodeRebuildReusesResourceSlot(t *testing.T) {}
func TestMySQL57SkipsClone(t *testing.T) {}
func TestMetadataCommitsOnlyAfterReplicationVerification(t *testing.T) {}
func TestLifecycleTaskNeverPersistsSecrets(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/lifecycle ./adapters/mysql -run 'Node|Membership|Sync|Rebuild' -count=1`
预期：FAIL，因为生命周期不被支持。

- [ ] **步骤 3：实现分阶段、可恢复的生命周期任务**

每个阶段记录开始/结束/状态/有界输出。重试替换临时凭据，重新检查后置条件，并从第一个未完成阶段恢复。元数据提交是在健康复制验证后的最后一步。

- [ ] **步骤 4：验证 GREEN 和 shell 测试**

运行：`go test -race ./internal/lifecycle ./adapters/mysql ./internal/api -count=1 && bash -n scripts/clusterguard-node-install.sh scripts/clusterguard-mysql-sync.sh`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add internal/lifecycle adapters/mysql/node_sync.go adapters/mysql/node_sync_test.go scripts internal/api/server.go
git commit -m "feat: manage MySQL node lifecycle"
```

### 任务 9：监控、审计时间线和报告

**文件：**
- 修改：`adapters/mysql/metrics.go`
- 修改：`internal/metrics/service.go`
- 创建：`internal/metrics/alerts.go`
- 创建：`internal/metrics/alerts_test.go`
- 修改：`internal/store/operations.go`
- 修改：`internal/api/metrics.go`
- 修改：`internal/api/operations.go`
- 创建：`internal/report/html.go`
- 创建：`internal/report/html_test.go`

**接口：**
- 生成监控安全的 JSON、Prometheus 文本、警报、操作时间线、JSON 和 HTML 报告。
- 消耗：持久操作、审计、验证、度量样本和报告记录。

- [ ] **步骤 1：编写失败的输出测试**

```go
func TestMonitoringMetricsContainStableUUIDLabels(t *testing.T) {}
func TestAlertsClassifyStoppedThreadLagAndVIPMismatch(t *testing.T) {}
func TestOperationTimelineShowsSourceTargetAndCollapsedEvidence(t *testing.T) {}
func TestHTMLReportEscapesEvidenceAndIncludesVerification(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/metrics ./internal/api ./internal/report -count=1`
预期：FAIL，因为警报/报告表面不完整。

- [ ] **步骤 3：实现无第三方依赖的输出**

添加 QPS、TPS、连接、运行线程、慢查询、缓冲命中、临时表、行锁、延迟、复制线程、VIP 所有者和操作状态指标。默认在 HTML 中保持原始证据有界并折叠。

- [ ] **步骤 4：验证 GREEN**

运行：`go test ./internal/metrics ./internal/api ./internal/report -count=1`
预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add adapters/mysql/metrics.go internal/metrics internal/store/operations.go internal/api internal/report
git commit -m "feat: expose MySQL operations observability"
```

### 任务 10：MySQL 操作控制台

**文件：**
- 修改：`internal/api/console.html`
- 修改：`internal/api/console.go`
- 修改：`internal/api/console_test.go`

**接口：**
- 生成概述、拓扑、操作、节点、指标、操作日志、关于和设置视图。
- 仅消耗 `/api/v1` 资源和能力标志。

- [ ] **步骤 1：编写失败的 DOM 合同测试**

```go
func TestConsoleOverviewDoesNotRenderTopologyGraph(t *testing.T) {}
func TestConsoleOperationsHasOneControlledSwitchButton(t *testing.T) {}
func TestConsoleTopologyHasMetadataModalAndAllNodeIdentityFields(t *testing.T) {}
func TestConsoleNodeActionsAreCapabilityDriven(t *testing.T) {}
func TestConsoleOperationLogCollapsesRawEvidence(t *testing.T) {}
```

- [ ] **步骤 2：验证 RED**

运行：`go test ./internal/api -run Console -count=1`
预期：与当前紧凑只读控制台失败。

- [ ] **步骤 3：实现完整的控制台**

使用一个受约束的布局系统，具有4-8像素的圆角、稳定的网格轨道、中文默认复制、英文切换、紧凑的节点卡片、对齐的拓扑连接器、居中的命令按钮和明确的禁用原因。不要添加虚假的操作处理程序。

- [ ] **步骤4：验证GREEN和浏览器截图**

运行：`go test ./internal/api -run Console -count=1`
预期：通过。然后在桌面和移动宽度下使用浏览器自动化，并确认没有重叠、空白画布、无法到达的按钮或布局偏移。

- [ ] **步骤5：提交**

```bash
git add internal/api/console.html internal/api/console.go internal/api/console_test.go
git commit -m "feat: deliver MySQL HA operations console"
```

### 任务11：安装、三主机部署和破坏性矩阵

**文件：**
- 创建：`scripts/clusterguard-install.sh`
- 创建：`scripts/clusterguard-preflight.sh`
- 创建：`scripts/clusterguard-smoke.sh`
- 创建：`scripts/clusterguard-ha-matrix.sh`
- 创建：`packaging/logrotate/clusterguard-ha`
- 修改：`packaging/systemd/clusterguard-ha.service`
- 修改：`README.md`
- 修改：`docs/operations.md`
- 创建：`docs/mysql-runbook.md`
- 创建：`scripts/scripts_test.go`

**接口：**
- 为控制器、数据节点代理或混合角色生成一个捆绑安装程序。
- 生成可重复的验收矩阵和回滚记录。
- 使用已构建的二进制文件、配置模板、清单和实验室SSH访问。

- [ ] **步骤1：编写失败的安装程序和烟雾合同测试**

```go
func TestInstallerDefaultsToPreflightAndRequiresExplicitExecute(t *testing.T) {}
func TestInstallerWritesSecretsOnlyToMode0640EnvironmentFile(t *testing.T) {}
func TestInstallerEnablesAgentReconcileOnEveryDataNode(t *testing.T) {}
func TestSmokeChecksOneWriterOneVIPAndAllReplicaThreads(t *testing.T) {}
```

- [ ] **步骤2：验证RED**

运行：`go test ./scripts -count=1`
预期：失败，因为交付脚本不存在。

- [ ] **步骤3：实现交付工具**

构建两个二进制文件，安装systemd单元，渲染特定角色的配置，按默认方式保留MySQL数据，创建受限身份，验证路径/端口/时间同步，启动服务，注册集群，并运行烟雾检查。

- [x] **步骤4：运行完整的本地验证**

运行：

```bash
gofmt -w $(rg --files -g '*.go')
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/clusterguard ./cmd/cgctl ./cmd/clusterguard-agent
git diff --check
rg -n 'orchestrator|orchctl|orchestrator-client|Orchestrator Enterprise|Enterprise HA|MySQL Control' --glob '!docs/superpowers/**' .
```

预期：所有命令通过，并且清洁室名称扫描在历史设计记录之外没有匹配项。

- [x] **步骤5：部署到`192.168.102.152-154`**

安装一个奇数控制器集和代理，注册所有六个实验室集群和每个集群的一个VIP，刷新拓扑，并在启用更改之前通过本机UUID验证每个MySQL实例。

- [x] **步骤6：运行破坏性验收**

执行：

- 在三个节点上进行20次轮询计划切换；
- 在不同集群上进行30次随机切换，同时分配主节点；
- 在两个具有不同VIP的集群中同时切换；
- 主节点重启、恢复的旧主节点重启和新主节点重启循环；
- 网络隔离，使用过时的旧主节点VIP和少数自隔离；
- 前主节点快速重新加入和分歧节点重建；
- 主机名、IP别名和端口元数据协调；
- MySQL 5.7、8.0、8.4和9.x兼容的语法路径。

每个案例必须证明一个可写的主节点、一个VIP所有者、健康的跟随线程、有限的延迟、完整的操作时间线和报告输出。

验收证据：`docs/mysql-feature-parity-acceptance.md`。

- [x] **步骤7：提交最终交付资产**

```bash
git add scripts packaging README.md docs
git commit -m "feat: deliver ClusterGuard MySQL HA platform"
```

## 自我审查

- 设计中的每个对等要求都映射到至少一个任务。
- 该计划没有模拟执行或兼容性层。
- 凭据、端点、操作和资源类型具有一致的名称。
- 节点生命周期与拓扑变更分离，但使用相同的门控。
- 监控和控制台任务使用真实的持久API，不能创建后端不支持的操作。
- 最终的实验室矩阵验证状态，而不仅仅是命令退出代码。
