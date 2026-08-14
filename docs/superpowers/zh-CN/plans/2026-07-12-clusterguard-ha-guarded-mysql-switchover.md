# ClusterGuard HA 受保护 MySQL 切换实施计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-12-clusterguard-ha-guarded-mysql-switchover.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **针对智能体工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 按任务逐步实施本计划。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标：** 构建一个持久化、幂等且可独立验证的 MySQL 计划内切换内核，同时在未配置真实写入端点提供者之前，保持默认生产执行状态为不支持。

**架构：** 扩展通用模型和原子存储库，引入不可变的操作记录和计划。在控制平面内部将基于 UUID 寻址的拓扑解析为适配器上下文，在现有工作流门控机制之后运行严格的 MySQL 预检和类型化执行步骤，并注入独立的端点提供者。默认提供者处于不支持状态；确定性测试通过注入模拟提供者来演练完整的角色转换过程。

**技术栈：** Go 1.22 标准库、原子 JSON 快照存储库、`net/http`、MySQL CLI 传输层、SHA-256 计划摘要、表驱动测试、竞态检测器。

## 全局约束

- 该代码库保持为独立的 ClusterGuard HA 实现，不依赖 Orchestrator，也不包含其 API、元数据表、配置、软件包、二进制文件或产品命名。
- 公共资源使用不可变 UUID；主机名、IP 地址和端口仅为可变端点。
- 工作流顺序为 `DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`。
- MySQL 故障转移、原主节点重新加入、节点生命周期执行、自动恢复以及真正的 Linux VIP 提供程序仍不受支持。
- PostgreSQL、Oracle 和 SQL Server 的变更行为仍不受支持，且处于故障安全（fail-closed）状态。
- 默认运行时不执行数据库变更，因为它没有可执行的写入端点提供程序。
- 密钥和原始变更 SQL 绝不会出现在持久化的操作、审计或报告记录中。

---

## 文件结构

- `pkg/model/workflow.go`：持久化操作、计划、步骤、尝试及验证数据契约。
- `pkg/adapter/adapter.go`：已解析的操作上下文与写入端点提供程序契约。
- `internal/store/operations.go`：操作创建、幂等性查找、CAS 转换及不可变计划持久化。
- `internal/store/operations_test.go`：针对持久性、冲突、不可变性以及竞态条件的存储库测试。
- `internal/workflow/resolver.go`：UUID 清单解析与配置的凭据注入。
- `internal/workflow/progress.go`：执行期间使用的阶段/步骤持久化适配器。
- `internal/workflow/workflow.go`：门控编排与保守的终端结果。
- `adapters/mysql/switchover.go`：严格预检、不可变计划、执行及验证。
- `adapters/mysql/dialect.go`：传统与现代复制语句选择。
- `adapters/mysql/endpoint.go`：默认运行时使用的受支持端点提供程序。
- `adapters/mysql/switchover_test.go`：确定性双节点转换与故障语义。
- `internal/api/operations.go`：操作创建/读取/预检/计划/执行/验证处理程序。
- `internal/api/operations_test.go`：幂等性、重启查找及默认故障安全 API 测试。
- `internal/runtime/runtime.go`：解析器接线与不受支持的默认端点提供程序。
- `docs/architecture.md`、`docs/operations.md`：能力与安全边界文档。

---

### 任务 1：持久化操作与不可变计划模型

**文件：**
- 修改：`pkg/model/workflow.go`
- 创建：`internal/store/operations.go`
- 创建：`internal/store/operations_test.go`
- 修改：`internal/store/repository.go`

**接口：**
- 生成：`model.OperationRecord`、`model.PlanStep`、`model.StepAttempt`、`Repository.CreateOperation`、`Repository.PutOperationPlan`、`Repository.TransitionOperation`、`Repository.Operation` 和 `Repository.OperationByIdempotencyKey`。

- [ ] **步骤 1：编写失败的存储库测试**

添加测试，创建带有幂等性键的操作，重新打开存储库，并断言返回相同的记录；对另一个目标重用该键并断言 `store.ErrConflict`；使用过期的元数据修订版本进行更新并断言冲突；持久化一个计划，并拒绝摘要不同的第二个计划。

```go
created, reused, err := repository.CreateOperation(model.OperationRecord{
    Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
    TargetID: targetID, IdempotencyKey: "switch-20260712-1",
})
if err != nil || reused { t.Fatalf("create operation: reused=%t err=%v", reused, err) }
same, reused, err := repository.CreateOperation(created)
if err != nil || !reused || same.ResourceID != created.ResourceID { t.Fatalf("idempotent create failed") }
```

- [ ] **步骤 2：运行聚焦测试并确认失败**

运行：`go test ./internal/store -run 'TestOperation' -count=1`

预期结果：FAIL，因为操作模型和存储库方法尚不存在。

- [ ] **步骤 3：实现模型和存储库操作**

添加类型化的计划步骤和操作记录。规范计划字段为值类型，并在每次存储库边界处克隆。验证 UUID、非空幂等性键、合法的阶段/状态转换、终端状态的不可变性、单调递增的步骤尝试次数以及计划摘要的不可变性。向快照中添加 `Operations` 和 `OperationKeys` 映射，并在 `Open` 期间迁移缺失的映射。

```go
type OperationRecord struct {
    ResourceMeta
    Operation       Operation       `json:"operation"`
    TargetID        ResourceID      `json:"target_id"`
    IdempotencyKey  string          `json:"idempotency_key"`
    Stage           WorkflowStage   `json:"stage"`
    Status          OperationStatus `json:"status"`
    Observation     string          `json:"observation_token,omitempty"`
    Plan            OperationPlan   `json:"plan"`
    Attempts        []StepAttempt   `json:"attempts,omitempty"`
    FailureClass    string          `json:"failure_class,omitempty"`
    Message         string          `json:"message,omitempty"`
}
```

- [ ] **步骤 4：运行存储测试**

运行：`go test ./internal/store -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交**

```bash
git add pkg/model/workflow.go internal/store/repository.go internal/store/operations.go internal/store/operations_test.go
git commit -m "feat: persist idempotent operation records"
```

### 任务 2：已解析的上下文和端点提供者契约

**文件：**
- 修改：`pkg/adapter/adapter.go`
- 创建：`internal/workflow/resolver.go`
- 创建：`internal/workflow/resolver_test.go`
- 创建：`adapters/mysql/endpoint.go`
- 修改：`adapters/mysql/mysql.go`
- 修改：`pkg/adapter/registry_test.go`

**接口：**
- 消费：存储库拓扑快照和不可变的资源 ID。
- 产出：`adapter.ResolvedOperation`、`adapter.HAEndpointProvider`、`workflow.OperationResolver` 和 `workflow.RepositoryResolver.Resolve`。

- [ ] **步骤 1：编写失败的解析和能力测试**

证明解析器会拒绝集群外的目标、重复的主节点、无主节点、过期或缺失的快照，以及通过任意参数提供的目标端点。证明默认的 MySQL 适配器宣传 precheck/plan 能力，但不宣传 execute/verify 能力。

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./internal/workflow ./pkg/adapter ./adapters/mysql -run 'Test.*(Resolve|Capabilities)' -count=1`

预期结果：FAIL，因为合约缺失。

- [ ] **步骤 3：实现合约与解析器**

```go
type ResolvedOperation struct {
    Cluster     model.DatabaseCluster
    Snapshot    model.TopologySnapshot
    Primary     model.DatabaseInstance
    Target      model.DatabaseInstance
    Credentials Credentials
}

type HAEndpointProvider interface {
    Executable(context.Context) bool
    Precheck(context.Context, ResolvedOperation) []model.Check
    Transfer(context.Context, ResolvedOperation) error
    Verify(context.Context, ResolvedOperation) model.Check
}
```

解析器仅通过当前快照中的 UUID 查找源和目标，验证引擎和集群所有权，并通过服务端解析器注入凭据。不受支持的提供者返回阻塞检查，且永不执行副作用操作。

- [ ] **步骤 4：运行聚焦测试与包测试**

运行：`go test ./internal/workflow ./pkg/adapter ./adapters/mysql -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交代码**

```bash
git add pkg/adapter/adapter.go internal/workflow/resolver.go internal/workflow/resolver_test.go adapters/mysql/endpoint.go adapters/mysql/mysql.go pkg/adapter/registry_test.go
git commit -m "feat: resolve UUID scoped operation context"
```

### 任务 3：严格的 MySQL 切换预检查与计划

**文件：**
- 创建：`adapters/mysql/switchover.go`
- 创建：`adapters/mysql/switchover_test.go`
- 修改：`adapters/mysql/mysql.go`
- 修改：`pkg/model/workflow.go`

**接口：**
- 消费：`adapter.OperationRequest.Resolved` 和 `HAEndpointProvider.Precheck`。
- 生产：`Adapter.Precheck` 和 `Adapter.BuildPlan`，用于 `OperationSwitchover`。

- [ ] **步骤 1：编写基于表格的失败预检查测试**

覆盖健康的两节点 GTID 拓扑、错误的目标、非直接副本、未知或非零延迟、停止的线程、可写目标、维护状态、缺失的 GTID、错误/缺失的 GTID、不兼容的发布系列、不完整的探测、额外副本、过时的观察结果以及不受支持的端点提供者。

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./adapters/mysql -run 'TestSwitchover(Precheck|Plan)' -count=1`

预期结果：FAIL，因为切换功能仍不受支持。

- [ ] **步骤 3：实现严格检查与规范计划摘要**

构建包含源/目标 ID 和预期后置条件的有序 `PlanStep` 值。在清除易变资源元数据和摘要字段本身后，对规范 JSON 计算 SHA-256 哈希。任何包含失败检查的计划均返回为不可执行，且无法持久化以供执行。

```go
steps := []model.PlanStep{
    {Name: "fence_source", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true},
    {Name: "wait_target_gtid", Owner: "mysql", TargetID: resolved.Target.ResourceID},
    {Name: "promote_target", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true},
    {Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true},
    {Name: "verify_roles_and_endpoint", Owner: "platform", TargetID: resolved.Target.ResourceID},
}
```

- [ ] **步骤 4：运行适配器测试**

运行：`go test ./adapters/mysql -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交代码**

```bash
git add adapters/mysql/switchover.go adapters/mysql/switchover_test.go adapters/mysql/mysql.go pkg/model/workflow.go
git commit -m "feat: plan guarded MySQL switchover"
```

### 任务 4：MySQL 方言、执行与独立验证

**文件：**
- 创建：`adapters/mysql/dialect.go`
- 创建：`adapters/mysql/dialect_test.go`
- 修改：`adapters/mysql/runner.go`
- 修改：`adapters/mysql/switchover.go`
- 修改：`adapters/mysql/switchover_test.go`

**接口：**
- 产出：`SQLExecutor.Exec`、`mysqlDialect`、幂等步骤后置条件探测、`Adapter.Execute` 以及 `Adapter.Verify`。

- [ ] **步骤 1：编写失败的方言和执行测试**

断言 MySQL 5.7 使用 `STOP SLAVE`/`RESET SLAVE ALL`，现代版本使用 `STOP REPLICA`/`RESET REPLICA ALL`，且所有版本均使用显式只读转换。在模拟 SQL 执行器中记录每一次变更操作，并断言精确的执行顺序。覆盖以下场景：隔离前失败、隔离后失败、提升后失败、基于观测到的后置条件进行重试、提交边界后的取消、端点转移失败以及重复的端点所有者。

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./adapters/mysql -run 'Test(MySQLDialect|SwitchoverExecute|SwitchoverVerify)' -count=1`

预期结果：FAIL，因为不存在变更路径。

- [ ] **步骤 3：实现最小安全执行器**

使用 `SET GLOBAL super_read_only = ON`、`SET GLOBAL read_only = ON`、`WAIT_FOR_EXECUTED_GTID_SET`、按版本选择的停止/重置语句，以及显式目标只读转换。在源端隔离后，不得仅因调用者上下文被取消而放弃安全序列。严禁记录凭据或 SQL 语句。返回失败类别 `pre_commit`、`fenced` 和 `promoted_unverified`。

- [ ] **步骤 4：运行适配器测试和数据竞争检测器**

运行：`go test ./adapters/mysql -count=1`

运行：`go test -race ./adapters/mysql -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交**

```bash
git add adapters/mysql/dialect.go adapters/mysql/dialect_test.go adapters/mysql/runner.go adapters/mysql/switchover.go adapters/mysql/switchover_test.go
git commit -m "feat: execute and verify MySQL switchover kernel"
```

### 任务 5：持久化工作流进度与幂等重试

**文件：**
- 创建：`internal/workflow/progress.go`
- 创建：`internal/workflow/progress_test.go`
- 修改：`internal/workflow/workflow.go`
- 修改：`internal/workflow/workflow_test.go`

**接口：**
- 消费：操作存储库、解析器、适配器及端点能力。
- 产出：阶段 CAS 更新、步骤进度记录、终端重试行为以及保守的不确定结果。

- [ ] **步骤 1：编写失败的工作流测试**

在预检查之前证明操作的持久性，每个阶段按顺序持久化，重复的终止请求返回终止记录而不经过网关或产生变更，重复的进行中请求不能并发运行，过期的计划在变更前被阻塞，且在已提交的步骤之后持久化失败则返回不确定状态。

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./internal/workflow -run 'Test.*(Durable|Idempotent|Progress|StalePlan)' -count=1`

预期结果：FAIL，因为工作流未持久化操作状态。

- [ ] **步骤 3：实现持久的阶段和步骤记录**

在发现之前创建/复用记录。在服务端解析 UUID 上下文，在安全评估之前持久化计划，在锁下重新验证，并将进度记录器传递给适配器。保留不支持的能力检查，位于安全、锁定、审批、SQL 和端点副作用之前。

- [ ] **步骤 4：运行工作流测试和竞态检测器**

运行：`go test ./internal/workflow -count=1`

运行：`go test -race ./internal/workflow -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交**

```bash
git add internal/workflow/progress.go internal/workflow/progress_test.go internal/workflow/workflow.go internal/workflow/workflow_test.go
git commit -m "feat: persist guarded workflow progress"
```

### 任务 6：操作 API 与默认故障安全运行时

**文件：**
- 创建：`internal/api/operations.go`
- 创建：`internal/api/operations_test.go`
- 修改：`internal/api/server.go`
- 修改：`internal/api/server_test.go`
- 修改：`internal/runtime/runtime.go`
- 修改：`cmd/cgctl/main.go`
- 修改：`cmd/cgctl/main_test.go`

**接口：**
- 生成：`POST /api/v1/operations`、`GET /api/v1/operations/{id}`、阶段操作路由以及 `cgctl operation` 查找。

- [ ] **步骤 1：编写失败的 API 测试**

测试经过身份验证的创建/读取、缺少幂等性键、重复复用、冲突复用、仅 UUID 的目标验证、格式错误的输入、重启查找，以及在锁定或副作用之前默认执行返回 HTTP 501。

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./internal/api ./cmd/cgctl -run 'Test.*Operation' -count=1`

预期结果：FAIL，因为持久化操作路由不存在。

- [ ] **步骤 3：实现处理程序和运行时接线**

在运行时凭据解析器中保留机密信息。默认 MySQL 适配器接收 `UnsupportedHAEndpointProvider`，从而使执行能力为 false。将存储库冲突映射到 HTTP 409，验证失败映射到 400，不支持的执行映射到 501，并将不确定的提交映射到 500，并在响应中包含操作记录。

- [ ] **步骤 4：运行 API、CLI 和运行时测试**

运行：`go test ./internal/api ./cmd/cgctl ./internal/runtime -count=1`

预期结果：PASS。

- [ ] **步骤 5：提交**

```bash
git add internal/api/operations.go internal/api/operations_test.go internal/api/server.go internal/api/server_test.go internal/runtime/runtime.go cmd/cgctl/main.go cmd/cgctl/main_test.go
git commit -m "feat: expose durable operation API"
```

### 任务 7：文档与全面验证

**文件：**
- 修改：`docs/architecture.md`
- 修改：`docs/operations.md`
- 修改：`README.md`

**接口：**
- 记录确切的生产环境禁用边界以及下一个 Linux VIP 提供程序项目。

- [ ] **步骤 1：更新文档**

记录操作 ID、幂等性键、不可变计划、保守的终端状态、API 示例、默认的 501 行为，以及明确排除故障转移/重新加入/节点生命周期/VIP 执行的内容。

- [ ] **步骤 2：运行针对性回归测试套件**

运行：`go test ./internal/store ./internal/workflow ./adapters/mysql ./internal/api ./cmd/cgctl -count=1`

预期结果：PASS。

- [ ] **步骤 3：运行全面验证**

运行：`go test ./... -count=1`

运行：`go test -race ./... -count=1`

运行：`go build ./cmd/...`

运行：`go vet ./...`

运行：`git diff --check`

运行：`for f in configs/*.json; do jq empty "$f" || exit 1; done`

运行：`rg -n 'orchestrator-enterprise|orchctl|Orchestrator Enterprise|Enterprise HA|MySQL Control' --glob '!docs/superpowers/**' .`

预期结果：所有命令均通过，且洁净室扫描无匹配项。

- [ ] **步骤 4：提交文档**

```bash
git add README.md docs/architecture.md docs/operations.md
git commit -m "docs: describe guarded MySQL switchover"
```

- [ ] **步骤 5：审查最终差异和工作树**

运行：`git status --short --branch`

运行：`git log --oneline -8`

预期结果：在 `codex/mysql-topology-intelligence` 上工作树干净，且受保护的切换提交系列位于 HEAD。
