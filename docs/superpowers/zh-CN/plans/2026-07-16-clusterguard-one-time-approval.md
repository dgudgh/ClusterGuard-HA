# ClusterGuard HA 一次性授权实施方案

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-16-clusterguard-one-time-approval.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于代理工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项执行此计划任务。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标：** 用持久的单次使用授权替换可重复使用的流程审批密钥，使内部自动恢复控制器无需人工令牌即可执行，并从正常 Web 控制台切换中移除长期有效的控制令牌。

**架构：** 新的授权服务通过管理员认证的 API 发布随机、绑定计划的授权。仓库仅在现有的 Raft 复制快照中存储授权哈希，并通过持久操作 APPROVE 转换原子地消耗一个授权。手动 HTTP 执行只能使用匹配的授权；自动恢复调用一个单独的内部工作流方法，不能通过 JSON 输入选择。

**技术栈：** Go 1.22 标准库，现有 JSON 快照仓库，HashiCorp Raft 快照 CAS，嵌入式 HTML/CSS/JavaScript 控制台，基于 `flag` 的 `cgctl`。

## 全局约束

- 默认授权 TTL 为 5 分钟；最大 TTL 为 15 分钟。
- 明文授权仅返回一次，且从不持久化或记录。
- 授权绑定到一个持久操作、集群、引擎、类型、目标、观察和计划摘要。
- 手动执行在适配器变更前仅消耗一次授权。
- 自动故障转移没有人工授权，但仍通过安全防护、操作锁定、隔离、执行、验证、审计和报告。
- 公共 HTTP 输入不能选择自动授权。
- 现有的管理变更路由仍受 `CG_CONTROL_TOKEN` 保护。
- `CG_APPROVAL_TOKEN` 已弃用，且从不作为兼容性回退接受。
- PostgreSQL、Oracle 和 SQL Server 变更仍不支持。

---

### 任务 1：授权资源和持久存储

**文件：**
- 创建：`pkg/model/approval.go`
- 创建：`internal/store/approvals.go`
- 创建：`internal/store/approvals_test.go`
- 修改：`internal/store/repository.go`
- 修改：`internal/store/replication_test.go`

**接口：**
- 生成：`model.ApprovalGrant`, `model.ApprovalGrantStatus`。
- 生成：`Repository.PutApprovalGrant`, `Repository.ApprovalGrant`, `Repository.ApprovalGrants`。
- 生成：`Repository.ConsumeApprovalGrant`。

- [ ] **步骤 1：编写失败的模型和仓库测试**

```go
func TestApprovalGrantRoundTripsWithoutPlaintextSecret(t *testing.T) {
    repository := NewMemory()
    grant := model.ApprovalGrant{
        ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
        TokenHash: "sha256:abc", OperationID: model.NewResourceID(),
        ClusterID: model.NewResourceID(), Engine: model.EngineMySQL,
        OperationKind: model.OperationSwitchover, TargetID: model.NewResourceID(),
        PlanDigest: "sha256:plan", ObservationDigest: "cluster@sha256:observation",
        IssuedBy: "admin", IssuedAt: time.Now().UTC(),
        ExpiresAt: time.Now().UTC().Add(5 * time.Minute), Status: model.ApprovalGrantActive,
    }
    if err := repository.PutApprovalGrant(grant); err != nil { t.Fatal(err) }
    stored, found := repository.ApprovalGrant(grant.ResourceID)
    if !found || stored.TokenHash != grant.TokenHash { t.Fatalf("stored=%+v", stored) }
    raw, err := repository.ReplicatedState()
    if err != nil { t.Fatal(err) }
    if bytes.Contains(raw, []byte("cgag_")) { t.Fatal("snapshot contains plaintext grant") }
}
```

添加测试，确保复制的从节点接收到相同的活动授权，并且无效的 UUID、缺失的哈希、无效的过期时间和重复的 ID 都会失败。

- [ ] **步骤 2：运行聚焦测试并验证失败**

运行：

```bash
go test ./internal/store ./pkg/model -run ApprovalGrant -count=1
```

预期：编译失败，因为 `model.ApprovalGrant` 和仓库方法不存在。

- [ ] **步骤 3：添加资源模型**

```go
type ApprovalGrantStatus string

const (
    ApprovalGrantActive   ApprovalGrantStatus = "active"
    ApprovalGrantConsumed ApprovalGrantStatus = "consumed"
    ApprovalGrantExpired  ApprovalGrantStatus = "expired"
    ApprovalGrantRevoked  ApprovalGrantStatus = "revoked"
)

type ApprovalGrant struct {
    ResourceMeta
    TokenHash             string              `json:"token_hash"`
    OperationID           ResourceID          `json:"operation_id"`
    ClusterID             ResourceID          `json:"cluster_id"`
    Engine                Engine              `json:"engine"`
    OperationKind         OperationKind       `json:"operation_kind"`
    TargetID              ResourceID          `json:"target_id"`
    PlanDigest            string              `json:"plan_digest"`
    ObservationDigest     string              `json:"observation_digest"`
    IssuedBy              string              `json:"issued_by"`
    IssuedAt              time.Time           `json:"issued_at"`
    ExpiresAt             time.Time           `json:"expires_at"`
    ConsumedAt            time.Time           `json:"consumed_at,omitempty"`
    ConsumedByOperationID ResourceID          `json:"consumed_by_operation_id,omitempty"`
    Status                ApprovalGrantStatus `json:"status"`
}
```

- [ ] **步骤 4：将授权添加到快照和 CRUD 方法中**

添加：

```go
ApprovalGrants map[model.ResourceID]model.ApprovalGrant `json:"approval_grants"`
```

在 `emptySnapshot` 中初始化映射，在快照复制期间克隆它，在解码期间验证每个授权，并实现排序读取方法。`PutApprovalGrant` 必须使用 `mutationMu`，克隆快照，一致地递增元数据修订规则，并调用 `commitSnapshotLocked`。

- [ ] **步骤 5：实现原子消耗**

定义：

```go
type ConsumeApprovalGrantRequest struct {
    GrantID                  model.ResourceID
    TokenHash                string
    OperationID              model.ResourceID
    ExpectedOperationRevision uint64
    Now                      time.Time
    Transition               model.OperationTransition
}
```

`ConsumeApprovalGrant` 必须在一个候选快照中：

1. 加载活动授权和操作；
2. 以常量时间比较令牌哈希；
3. 比较操作、集群、引擎、类型、目标、观察和计划摘要；
4. 拒绝过期或之前消耗的授权；
5. 标记授权为已消耗；
6. 将 APPROVE 转换应用到操作；
7. 提交一个快照。

- [ ] **步骤 6：运行存储和复制测试**

运行：

```bash
go test ./internal/store ./pkg/model -count=1
```

预期：通过。

- [ ] **步骤 7：提交**

```bash
git add pkg/model/approval.go internal/store/approvals.go internal/store/approvals_test.go internal/store/repository.go internal/store/replication_test.go
git commit -m "feat: persist one-time approval grants"
```

---

### 任务 2：授权颁发者和验证者

**文件：**
- 创建：`internal/approval/service.go`
- 创建：`internal/approval/service_test.go`

**接口：**
- 消耗：任务 1 中的仓库授权 CRUD 和原子消耗。
- 生成：`approval.Service.Issue`, `approval.Service.AuthorizeIntent`, `approval.Service.Consume`。
- 生成：`approval.ParseToken`。

- [ ] **步骤 1：编写失败的令牌和生命周期测试**

覆盖：

- 令牌格式 `cgag_<uuid>.<base64url-secret>`；
- 32 个随机密钥字节；
- 仅 SHA-256 哈希达到 `PutApprovalGrant`；
- TTL 默认为 5 分钟，拒绝超过 15 分钟的值；
- 格式错误、过期、撤销、已消耗、目标错误、计划错误和操作错误的授权被拒绝；
- 成功消耗不能重复。

使用确定性随机读取器和时钟：

```go
service := approval.New(store, bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)), func() time.Time { return now })
```

- [ ] **步骤 2：运行测试并验证失败**

运行：

```bash
go test ./internal/approval -count=1
```

预期：包或导出函数不存在。

- [ ] **步骤 3：实现密钥生成和解析**

```go
func ParseToken(token string) (model.ResourceID, []byte, error)
func tokenHash(secret []byte) string
```

使用 `crypto/rand.Reader`, `encoding/base64.RawURLEncoding`, `crypto/sha256`, 和 `crypto/subtle`。从不将令牌包含在错误中。

- [ ] **步骤 4：实现颁发**

```go
type IssueRequest struct {
    Operation model.OperationRecord
    IssuedBy  string
    TTL       time.Duration
}

type IssuedGrant struct {
    Grant model.ApprovalGrant `json:"grant"`
    Token string              `json:"approval_token"`
}
```

要求一个计划的持久操作，具有非空的观察和计划摘要。在返回密钥之前持久化哈希授权。

- [ ] **步骤 5：实现非消耗意图授权**

`AuthorizeIntent` 解析授权 ID，比较密钥哈希，拒绝非活动或过期的授权，并确认操作 ID 加上目标。它返回清理后的授权元数据，以便 API 在进入工作流前加载绑定的持久操作。

- [ ] **步骤 6：实现原子工作流消耗**

`Consume` 接受当前 `model.OperationRecord`，调用 `Repository.ConsumeApprovalGrant`，并返回已消耗的授权 ID 用于审计文本。将仓库冲突映射到稳定的错误：

```go
var (
    ErrRequired = errors.New("approval grant is required")
    ErrInvalid = errors.New("approval grant is invalid")
    ErrExpired = errors.New("approval grant has expired")
    ErrConsumed = errors.New("approval grant has already been consumed")
    ErrMismatch = errors.New("approval grant does not match this operation")
    ErrStalePlan = errors.New("approval grant plan is stale")
)
```

- [ ] **步骤 7：运行测试**

运行：

```bash
go test ./internal/approval -count=1
```

预期：通过。

- [ ] **步骤 8：提交**

```bash
git add internal/approval
git commit -m "feat: issue and consume scoped approval grants"
```

---

### 任务 3：工作流手动和自动授权路径

**文件：**
- 修改：`internal/workflow/workflow.go`
- 修改：`internal/workflow/durable.go`
- 修改：`internal/workflow/workflow_test.go`
- 修改：`internal/workflow/durable_test.go`
- 修改：`internal/recovery/controller.go`
- 修改：`internal/recovery/controller_test.go`

**接口：**
- 消耗：`approval.Service.Consume`。
- 生成：`workflow.Service.Execute` 用于手动授权。
- 生成：`workflow.Service.ExecuteAutomatic` 用于内部故障转移。

- [ ] **步骤 1：编写失败的工作流测试**

添加测试证明：

1. 手动执行调用带有持久计划的审批消耗；
2. 授权消耗和 APPROVE 转换不会执行两次；
3. 自动执行在空的人工令牌下成功；
4. 自动执行拒绝切换、非自动执行者或缺失的事件 ID；
5. 正常 `Execute` 不能通过参数选择自动授权。

- [ ] **步骤 2：运行聚焦测试**

运行：

```bash
go test ./internal/workflow ./internal/recovery -run 'Approval|Automatic' -count=1
```

预期：失败，因为自动入口点和授权消费者不存在。

- [ ] **步骤 3：替换验证器接口**

```go
type ApprovalConsumer interface {
    Consume(context.Context, model.OperationRecord, string) (model.ResourceID, error)
}
```

保持授权模式私有：

```go
type executionAuthorization struct {
    approvalToken string
    automatic     bool
    incidentID    string
}
```

- [ ] **步骤 4：添加单独的入口点**

```go
func (service *Service) Execute(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error)
func (service *Service) ExecuteAutomatic(ctx context.Context, request adapter.OperationRequest, incidentID string) (model.Execution, error)
```

`ExecuteAutomatic` 必须要求 `OperationFailover`，通过无环共享常量或工作流常量将 `RequestedBy` 覆盖为 `recovery.AutomaticRecoveryActor`，并拒绝空的事件 ID。

- [ ] **步骤 5：原子消耗手动审批**

在获取锁和重新验证拓扑后，使用最新的持久记录调用 `approval.Consume`。不要调用旧的单独 APPROVE 转换。审计：

```text
one-time approval grant <grant-id> consumed
```

对于自动执行，使用：

```text
automatic recovery authorized for incident <incident-id>
```

不创建或消耗令牌。

- [ ] **步骤 6：更新恢复控制器**

将 `OperationExecutor` 更改为：

```go
type OperationExecutor interface {
    ExecuteAutomatic(context.Context, adapter.OperationRequest, string) (model.Execution, error)
}
```

从 `Controller`, `NewController`, 和 `configured` 中删除 `approvalToken`。将事件派生的稳定身份传递给 `ExecuteAutomatic`。

- [ ] **步骤 7：运行工作流和恢复测试**

运行：

```bash
go test ./internal/workflow ./internal/recovery -count=1
```

预期：通过。

- [ ] **步骤 8：提交**

```bash
git add internal/workflow internal/recovery
git commit -m "feat: separate manual approval from automatic recovery"
```

---

### 任务 4：审批 API 和执行路由授权

**文件：**
- 创建：`internal/api/approvals.go`
- 创建：`internal/api/approvals_test.go`
- 修改：`internal/api/server.go`
- 修改：`internal/api/operations.go`
- 修改：`internal/api/operations_test.go`

**接口：**
- 消耗：任务 2 中的审批颁发者和验证者。
- 生成：`POST/GET /api/v1/approvals`。
- 生成：由授权而非控制令牌手动操作执行的授权。

- [ ] **步骤 1：编写失败的 API 测试**

覆盖：

- 没有管理持有令牌的审批颁发返回 401；
- 从节点颁发返回 Leader/Quorum 信息；
- 计划操作并返回一个明文令牌；
- 列表/显示响应省略令牌哈希和明文；
- `/api/v1/operations/execute` 接受匹配的授权而无需
  `CG_CONTROL_TOKEN`；
- 重复使用或不匹配的授权返回阻止响应；
- 任意 POST 路由仍需要控制令牌；
- JSON 字段如 `automatic`, `authorization_mode` 或
  `requested_by=clusterguard-automatic-recovery` 不能选择自动
  工作流入口点。

- [ ] **步骤 2：运行聚焦的 API 测试**

运行：

```bash
go test ./internal/api -run 'Approval|ControlToken|Automatic' -count=1
```

预期：失败。

- [ ] **步骤 3：添加服务器选项和路由**

```go
func WithApprovalService(service *approval.Service) ServerOption
```

在通用操作路由之前路由 `/api/v1/approvals`。POST 仍受管理控制认证和变更权限控制。GET 返回清理后的元数据。

- [ ] **步骤 4：实现授权颁发**

解码集群、引擎、类型、目标、颁发者、TTL 和可选的幂等性键。调用 `workflow.Plan`，然后 `approval.Service.Issue`。响应为：

```json
{
  "status": "ok",
  "result": {
    "operation": {},
    "grant": {},
    "approval_token": "cgag_..."
  }
}
```

- [ ] **步骤 5：使认证路由感知**

将所有 POST 的全局控制检查替换为：

```go
func manualGrantExecutionRoute(method, path string) bool
```

仅操作执行端点绕过 `authorizeControl`；路由代码必须
在 `AuthorizeIntent` 之前调用 `workflow.Execute`。其他所有变更保持
管理认证和 Leader/Quorum 检查。

- [ ] **步骤 6：执行绑定授权的操作**

对于 `/api/v1/operations/execute`，使用授权加载其持久操作。
拒绝与授权绑定记录不同的负载集群、引擎、类型或目标值。这防止未经授权的调用者创建
带有随机令牌的操作记录。

- [ ] **步骤 7：保留安全的公共错误**

将审批错误映射到中文就绪的稳定英文 API 消息，不返回秘密值。确保操作日志和报告包含授权 ID，
而不是授权字符串。

- [ ] **步骤 8：运行 API 测试**

运行：

```bash
go test ./internal/api -count=1
```

预期：通过。

- [ ] **步骤 9：提交**

```bash
git add internal/api
git commit -m "feat: expose privileged one-time approval API"
```

---

### 任务 5：运行时、配置和自动故障转移迁移

**文件：**
- 修改：`internal/runtime/runtime.go`
- 修改：`internal/runtime/runtime_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`configs/clusterguard.example.json`

**接口：**
- 消耗：审批服务和新工作流构造器。
- 生成：运行时自动恢复，无需 `CG_APPROVAL_TOKEN`。

- [ ] **步骤 1：编写失败的运行时/配置测试**

断言：

- 自动故障转移即使旧审批令牌为空，也从共识、代理隔离和故障
  证据开始；
- 运行时将一个共享的审批服务连接到 API 和工作流；
- 现有的 `CG_APPROVAL_TOKEN` 生成弃用警告但不被
  验证器接受；
- 不完整的共识/隔离配置仍阻止启动。

- [ ] **步骤 2：运行测试**

运行：

```bash
go test ./internal/runtime ./internal/config -count=1
```

预期：旧静态令牌假设失败。

- [ ] **步骤 3：连接审批服务**

构造：

```go
approvalService := approval.New(repository, rand.Reader, time.Now)
```

将其传递给 `workflow.New` 和 `api.WithApprovalService`。

- [ ] **步骤 4：删除自动静态审批依赖**

删除 `configuration.ApprovalToken == ""` 启动条件，并从 `recovery.NewController` 中删除
令牌参数。

- [ ] **步骤 5：弃用配置**

仅保留旧字段解析一个版本，以便启动可以记录：

```text
CG_APPROVAL_TOKEN is deprecated and ignored; use one-time approval grants
```

不要构造 `workflow.TokenApproval`。

- [ ] **步骤 6：运行测试**

运行：

```bash
go test ./internal/runtime ./internal/config -count=1
```

预期：通过。

- [ ] **步骤 7：提交**

```bash
git add internal/runtime internal/config configs/clusterguard.example.json
git commit -m "refactor: remove static approval from runtime"
```

---

### 任务 6：cgctl 审批命令

**文件：**
- 修改：`cmd/cgctl/main.go`
- 修改：`cmd/cgctl/main_test.go`

**接口：**
- 生成：`cgctl approval issue`, `approval list`, 和 `approval show`。

- [ ] **步骤 1：编写失败的 CLI 测试**

测试请求路径、JSON 正文、管理 Authorization 头、可读输出、`--json`、TTL 解析和警告：

```text
Approval token is shown once and cannot be recovered.
```

- [ ] **步骤 2：运行 CLI 测试**

运行：

```bash
go test ./cmd/cgctl -count=1
```

预期：失败，因为审批子命令未知。

- [ ] **步骤 3：添加结构化命令解析**

支持：

```text
cgctl approval issue --cluster UUID --engine mysql --kind switchover --target UUID --issued-by NAME --ttl 5m
cgctl approval list
cgctl approval show UUID
```

POST 发行读取 `CG_CONTROL_TOKEN`。GET 列表/显示不打印令牌哈希。

- [ ] **步骤 4：添加人类可读输出**

输出包括操作 ID、授权 ID、范围、过期时间和明文
令牌在单独一行。列表/显示包括状态和消耗元数据。

- [ ] **步骤 5：运行 CLI 测试**

运行：

```bash
go test ./cmd/cgctl -count=1
```

预期：通过。

- [ ] **步骤 6：提交**

```bash
git add cmd/cgctl
git commit -m "feat: add cgctl approval grant commands"
```

### 任务 7：Web 控制台一次性审批体验

**文件：**
- 修改：`internal/api/console.html`
- 修改：`internal/api/console_test.go`

**接口：**
- 消耗：grant-authorized 操作执行 API。
- 生成：无长期控制令牌的正常切换 UI。

- [ ] **步骤 1：编写失败的控制台合约测试**

要求：

- 控制台中没有 `id="control-token"`；
- 有一个名为 `一次性审批令牌` 的密码输入；
- 执行发送 `approval_token` 但不发送 Authorization 头；
- 在 `finally` 中清除令牌输入和状态；
- 缺少令牌时应弹出审批提示，而不是导航到通用设置；
- 已使用、过期、不匹配和旧计划错误具有简洁的中文翻译；
- 自动故障转移状态中不包含令牌控制。

- [ ] **步骤 2：运行控制台测试**

运行：

```bash
go test ./internal/api -run Console -count=1
```

预期：在旧控制令牌合约上失败。

- [ ] **步骤 3：更新设置和操作 UI**

移除正常控制令牌字段。保留一个内存中的审批输入，帮助文本为：

```text
由管理员生成，5 分钟内有效，执行一次后失效。
```

不要添加 localStorage 或 sessionStorage。

- [ ] **步骤 4：更新执行**

`controlOptions` 变为无 Authorization 的 JSON POST 辅助工具。执行负载包含一次性令牌。每次尝试后清除 DOM 值和 `state.approvalToken`，然后重新锁定切换。

- [ ] **步骤 5：运行控制台测试**

运行：

```bash
go test ./internal/api -run Console -count=1
```

预期：通过。

- [ ] **步骤 6：提交**

```bash
git add internal/api/console.html internal/api/console_test.go
git commit -m "feat: use one-time approval in HA console"
```

---

### 任务 8：文档、完整验证和三节点验收

**文件：**
- 修改：`docs/architecture.md`
- 修改：`docs/operations.md`
- 修改：`docs/mysql-feature-parity-acceptance.md`
- 修改：仍宣传 `CG_APPROVAL_TOKEN` 的部署环境模板

**接口：**
- 文档化最终操作员和自动恢复合约。

- [ ] **步骤 1：更新文档**

文档：

- 特权授权发放；
- 单次使用的手动执行；
- 授权过期和重试行为；
- 自动恢复内部授权；
- 废弃 `CG_APPROVAL_TOKEN`；
- 元数据或日志中没有明文授权。

- [ ] **步骤 2：运行源代码扫描**

运行：

```bash
rg -n 'TokenApproval|ExpectedToken|automatic-approval' --glob '*.go'
rg -n 'CG_APPROVAL_TOKEN' .
```

预期：无静态审批的运行时使用；只有迁移测试和弃用文档可能引用旧变量。

- [ ] **步骤 3：运行所有本地测试和构建**

运行：

```bash
gofmt -w pkg/model internal/approval internal/store internal/workflow internal/recovery internal/api internal/runtime internal/config cmd/cgctl
go test ./...
go build ./...
git diff --check
```

预期：所有命令通过。

- [ ] **步骤 4：部署到 192.168.102.152-154**

构建 `clusterguard`、`cgctl` 和 `clusterguard-agent`；在所有控制器上安装相同构件；逐个重启节点；在继续之前验证 Raft 多数派和授权快照复制。

- [ ] **步骤 5：手动切换验收**

1. 为一个选定的 MySQL 集群和候选者颁发一个五分钟的授权。
2. 通过等效于 Web 控制台的 API 执行，不使用 `CG_CONTROL_TOKEN`。
3. 验证一个可写主节点、一个 VIP 拥有者、健康的副本和一个仅包含授权 ID 的审计条目。
4. 重复使用相同的令牌并验证其被标记为已使用。
5. 颁发一个新的授权并切换回来。

- [ ] **步骤 6：自动故障转移验收**

1. 为选定的测试集群启用自动故障转移。
2. 停止或隔离当前主节点。
3. 等待配置的稳定事件窗口。
4. 验证 Leader 创建了一个无人工授权的自动操作。
5. 验证隔离、晋升、VIP 移动、副本修复和审计/报告记录。
6. 恢复旧主节点并验证其在显式重新加入工作流运行之前保持只读。

- [ ] **步骤 7：最终回归测试**

验证保留的两个 MySQL 环境、所有三个控制器、VIP 唯一性、Leader 变化、控制器重启和页面刷新。确认刷新后的浏览器在正常切换执行期间从不请求 `CG_CONTROL_TOKEN`。

- [ ] **步骤 8：提交**

```bash
git add docs configs packaging scripts
git commit -m "docs: document one-time approval operations"
```
