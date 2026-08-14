# 平台认证操作实现计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-16-platform-authenticated-operations.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于代理工作者:** 必需子技能: 使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项实现此计划任务。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标:** 添加 Raft 复制的平台用户和会话，使得登录的 ClusterGuard HA 操作员可以在不手动输入审批令牌的情况下执行 MySQL 操作，同时服务器仍然生成并原子性地消耗一个与计划绑定的一次性授权。

**架构:** 向现有的复制元数据快照中添加 `PlatformUser`、`PlatformSession` 和 `SecurityEvent` 资源。`internal/auth` 服务负责 Argon2id 密码哈希、不透明会话、CSRF、引导管理、密码更改和角色策略。API 对每个平台请求进行认证，然后会话认证的 MySQL 执行在服务器上完全创建并消耗现有的 `ApprovalGrant`。

**技术栈:** Go 1.22、`golang.org/x/crypto/argon2`、现有的 JSON 快照仓库、Hashicorp Raft 快照复制、`net/http`、嵌入式 HTML/CSS/JavaScript 控制台。

## 全局约束

- 默认管理员用户名正好是 `admin`。
- 默认管理员密码正好是 `generated-bootstrap-password`。
- 引导密码从不以明文形式持久化或记录。
- 引导管理员必须在进行任何更改之前更改默认密码。
- 密码存储使用带有随机盐的 Argon2id。
- 会话和 CSRF 密钥仅以 SHA-256 哈希形式存储。
- 浏览器会话使用 `HttpOnly`、`SameSite=Strict` 和 HTTPS 意识的 `Secure` cookie。
- 浏览器更改需要 `X-CSRF-Token`。
- 浏览器从不接收操作审批授权。
- 会话认证执行仍需通过安全防护、操作锁、ApprovalGrant 消耗、验证、审计和报告。
- 自动故障转移继续使用内部事件授权。
- PostgreSQL、Oracle 和 SQL Server 执行能力保持不变，并且在故障时关闭。

---

### 任务 1: 持久化平台用户、会话和安全事件

**文件:**
- 创建: `pkg/model/auth.go`
- 创建: `internal/store/auth.go`
- 创建: `internal/store/auth_test.go`
- 修改: `internal/store/repository.go`
- 修改: `internal/store/replication.go`
- 修改: `internal/store/replication_test.go`

**接口:**
- 生成: `model.PlatformUser`, `model.PlatformSession`, `model.SecurityEvent`
- 生成: `Repository.CreatePlatformUser`, `PlatformUser`, `PlatformUserByUsername`, `PlatformUsers`
- 生成: `Repository.PutPlatformSession`, `PlatformSession`, `RevokePlatformSession`
- 生成: `Repository.ChangePlatformPassword`, `RecordSecurityEvent`, `SecurityEvents`

- [ ] **步骤 1: 编写失败的模型和存储测试**

```go
func TestPlatformUserAndSessionReplicateWithoutPlaintextSecrets(t *testing.T) {
    leader := NewMemory()
    follower := NewMemory()
    leader.consensus = snapshotReplicator{target: follower}

    user := model.PlatformUser{
        ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
        Username: "admin", DisplayName: "Administrator", Role: model.PlatformRoleAdmin,
        PasswordHash: "$argon2id$v=19$...", MustChangePassword: true, AuthRevision: 1,
    }
    if _, err := leader.CreatePlatformUser(user); err != nil {
        t.Fatal(err)
    }
    session := model.PlatformSession{
        ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
        UserID: user.ResourceID, TokenHash: "sha256:session", CSRFHash: "sha256:csrf",
        UserAuthRevision: 1, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
    }
    if err := leader.PutPlatformSession(session); err != nil {
        t.Fatal(err)
    }
    encoded, _ := leader.ReplicatedState()
    if bytes.Contains(encoded, []byte("generated-bootstrap-password")) || bytes.Contains(encoded, []byte("session-secret")) {
        t.Fatal("replicated auth state contains plaintext secret")
    }
    if _, found := follower.PlatformUser(user.ResourceID); !found {
        t.Fatal("user did not replicate")
    }
    if _, found := follower.PlatformSession(session.ResourceID); !found {
        t.Fatal("session did not replicate")
    }
}
```

- [ ] **步骤 2: 运行聚焦测试并验证 RED**

运行:

```bash
go test ./internal/store ./pkg/model -run 'PlatformUser|PlatformSession|SecurityEvent' -count=1
```

预期: 编译失败，因为认证模型和仓库方法不存在。

- [ ] **步骤 3: 添加认证模型**

```go
type PlatformRole string

const (
    PlatformRoleAdmin    PlatformRole = "admin"
    PlatformRoleOperator PlatformRole = "operator"
    PlatformRoleViewer   PlatformRole = "viewer"
)

type PlatformUser struct {
    ResourceMeta
    Username           string       `json:"username"`
    DisplayName        string       `json:"display_name"`
    Role               PlatformRole `json:"role"`
    PasswordHash       string       `json:"password_hash"`
    MustChangePassword bool         `json:"must_change_password"`
    Disabled           bool         `json:"disabled"`
    AuthRevision       uint64       `json:"auth_revision"`
    PasswordChangedAt  time.Time    `json:"password_changed_at,omitempty"`
}

type PlatformSession struct {
    ResourceMeta
    UserID           ResourceID `json:"user_id"`
    TokenHash        string     `json:"token_hash"`
    CSRFHash         string     `json:"csrf_hash"`
    UserAuthRevision uint64     `json:"user_auth_revision"`
    IssuedAt         time.Time  `json:"issued_at"`
    ExpiresAt        time.Time  `json:"expires_at"`
    RevokedAt        time.Time  `json:"revoked_at,omitempty"`
}

type SecurityEvent struct {
    ResourceMeta
    UserID   ResourceID `json:"user_id,omitempty"`
    Username string     `json:"username,omitempty"`
    Kind     string     `json:"kind"`
    Outcome  string     `json:"outcome"`
    Message  string     `json:"message"`
}
```

- [ ] **步骤 4: 添加快照映射、验证、克隆和原子密码更改**

`ChangePlatformPassword` 必须更新密码哈希，清除
`MustChangePassword`，增加 `AuthRevision`，并撤销用户的所有活动会话
在一个快照提交中。

- [ ] **步骤 5: 运行聚焦和复制测试**

运行:

```bash
go test ./internal/store ./pkg/model -run 'PlatformUser|PlatformSession|SecurityEvent|Replicate' -count=1
```

预期: 通过。

- [ ] **步骤 6: 提交**

```bash
git add pkg/model/auth.go internal/store/auth.go internal/store/auth_test.go internal/store/repository.go internal/store/replication.go internal/store/replication_test.go
git commit -m "feat: persist platform users and sessions"
```

---

### 任务 2: 实现 Argon2id 密码和不透明会话

**文件:**
- 修改: `go.mod`
- 修改: `go.sum`
- 创建: `internal/auth/password.go`
- 创建: `internal/auth/password_test.go`
- 创建: `internal/auth/service.go`
- 创建: `internal/auth/service_test.go`

**接口:**
- 生成: `auth.Argon2Hasher.Hash`, `auth.Argon2Hasher.Verify`
- 生成: `auth.Service.EnsureBootstrapAdmin`
- 生成: `auth.Service.Login`, `Authenticate`, `Logout`, `ChangePassword`
- 生成: `auth.Principal`

- [ ] **步骤 1: 添加失败的密码测试**

```go
func TestArgon2HasherDoesNotExposePlaintextAndVerifiesPassword(t *testing.T) {
    hasher := testHasher()
    encoded, err := hasher.Hash("correct horse battery staple")
    if err != nil {
        t.Fatal(err)
    }
    if strings.Contains(encoded, "correct horse battery staple") {
        t.Fatal("hash contains plaintext")
    }
    if !hasher.Verify(encoded, "correct horse battery staple") {
        t.Fatal("correct password was rejected")
    }
    if hasher.Verify(encoded, "wrong password") {
        t.Fatal("wrong password was accepted")
    }
}
```

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/auth -run Argon2 -count=1
```

预期: 缺少包或符号。

- [ ] **步骤 3: 添加官方加密依赖**

```bash
go get golang.org/x/crypto@v0.17.0
```

- [ ] **步骤 4: 实现编码的 Argon2id 哈希**

使用:

```go
type Argon2Params struct {
    Memory      uint32
    Iterations  uint32
    Parallelism uint8
    SaltLength  uint32
    KeyLength   uint32
}
```

以标准自描述形式编码哈希:

```text
$argon2id$v=19$m=65536,t=3,p=2$<salt-base64>$<key-base64>
```

解析必须在分配内存之前拒绝未知版本、字段格式错误、成本值过大，
以及错误的盐/密钥长度。

- [ ] **步骤 5: 添加失败的认证服务测试**

覆盖引导、重复引导、登录、通用无效凭证、
禁用用户、过期、撤销、认证版本不匹配和密码更改。

```go
func TestLoginIssuesHashedSessionAndPasswordChangeRevokesIt(t *testing.T) {
    service := newTestService(t)
    user, err := service.EnsureBootstrapAdmin(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    login, err := service.Login(context.Background(), "admin", "generated-bootstrap-password")
    if err != nil {
        t.Fatal(err)
    }
    if login.SessionToken == "" || login.CSRFToken == "" {
        t.Fatal("login did not return opaque secrets")
    }
    if _, err := service.ChangePassword(context.Background(), login.SessionToken, "generated-bootstrap-password", "A-new-secure-password-123"); err != nil {
        t.Fatal(err)
    }
    if _, err := service.Authenticate(context.Background(), login.SessionToken); !errors.Is(err, auth.ErrUnauthenticated) {
        t.Fatalf("old session survived password change: %v", err)
    }
    if user.Username != "admin" {
        t.Fatalf("unexpected bootstrap user: %+v", user)
    }
}
```

- [ ] **步骤 6: 运行并验证 RED**

```bash
go test ./internal/auth -run 'Bootstrap|Login|Session|Password' -count=1
```

预期: 服务符号缺失。

- [ ] **步骤 7: 实现服务**

令牌使用:

```text
cgs_<session-uuid>.<32-byte-base64url-secret>
```

`Login` 仅存储 SHA-256 哈希。`Authenticate` 执行常时间哈希
比较并检查用户状态、过期、撤销和认证版本。

- [ ] **步骤 8: 运行聚焦测试并提交**

```bash
go test ./internal/auth ./internal/store -count=1
git add go.mod go.sum internal/auth internal/store
git commit -m "feat: add platform authentication service"
```

---

### 任务 3: 在运行时引导认证

**文件:**
- 修改: `internal/runtime/runtime.go`
- 修改: `internal/runtime/runtime_test.go`
- 修改: `internal/config/config.go`
- 修改: `internal/config/config_test.go`

**接口:**
- 消耗: `auth.Service.EnsureBootstrapAdmin`
- 生成: 传递给 API 的共享运行时认证服务

- [ ] **步骤 1: 添加失败的运行时引导测试**

```go
func TestRuntimeBootstrapsDefaultAdministratorOnce(t *testing.T) {
    configuration := minimalRuntimeConfig(t)
    first, err := New(configuration)
    if err != nil {
        t.Fatal(err)
    }
    first.Close()

    repository, err := store.Open(configuration.MetadataPath)
    if err != nil {
        t.Fatal(err)
    }
    users := repository.PlatformUsers()
    if len(users) != 1 || users[0].Username != "admin" || users[0].Role != model.PlatformRoleAdmin || !users[0].MustChangePassword {
        t.Fatalf("unexpected bootstrap users: %+v", users)
    }

    second, err := New(configuration)
    if err != nil {
        t.Fatal(err)
    }
    second.Close()
    if len(repository.PlatformUsers()) != 1 {
        t.Fatal("restart duplicated bootstrap administrator")
    }
}
```

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/runtime -run BootstrapDefaultAdministrator -count=1
```

- [ ] **步骤 3: 将一个认证服务连接到存储和 API**

在达成共识后构建服务，使引导提交通过
与其他元数据相同的 Raft 快照路径。

- [ ] **步骤 4: 移除浏览器对配置控制凭证的依赖**

保留 `control_token_env` 用于服务自动化。不要将其用作平台
管理员密码或浏览器会话密钥。

- [ ] **步骤 5: 运行运行时/配置测试并提交**

```bash
go test ./internal/runtime ./internal/config -count=1
git add internal/runtime internal/config
git commit -m "feat: bootstrap platform administrator"
```

---

### 任务 4: 添加登录、会话、CSRF 和角色中间件

**文件:**
- 创建: `internal/api/auth.go`
- 创建: `internal/api/auth_test.go`
- 修改: `internal/api/server.go`
- 修改: `internal/api/server_test.go`

**接口:**
- 消耗: `auth.Service`
- 生成: `WithAuthentication`
- 生成: `POST /api/v1/auth/login`
- 生成: `GET /api/v1/auth/me`
- 生成: `POST /api/v1/auth/logout`
- 生成: `POST /api/v1/auth/password`
- 生成: 请求上下文 `auth.Principal`

- [ ] **步骤 1: 编写失败的 HTTP 认证测试**

覆盖:

```go
func TestPlatformAPILoginRequiresCSRFForMutation(t *testing.T)
func TestPlatformAPIBlocksMutationsUntilBootstrapPasswordChanges(t *testing.T)
func TestPlatformAPIPasswordChangeRevokesSession(t *testing.T)
func TestPlatformAPIRolePolicyBlocksViewerMutation(t *testing.T)
func TestLoginFailureDoesNotRevealWhetherUserExists(t *testing.T)
```

CSRF 测试必须发出三个请求: 无头、错误头、正确头。

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/api -run 'Login|Session|CSRF|PasswordChange|RolePolicy' -count=1
```

- [ ] **步骤 3: 实现认证 cookie 和路由**

Cookie 常量:

```go
const (
    sessionCookieName = "clusterguard_session"
    csrfCookieName    = "clusterguard_csrf"
)
```

仅从显式信任的配置中使用请求 TLS 状态或转发的 HTTPS 信息；否则从 `request.TLS != nil` 推导 `Secure`。

- [ ] **步骤 4: 实现中间件策略**

未认证的例外仅限于:

- `GET /`；
- `HEAD /`；
- `POST /api/v1/auth/login`；
- 签名 `/api/v1/agent/reconcile`；
- 专用 `/api/v1/monitoring/*`。

Cookie 认证的更改需要 CSRF。Bearer 服务自动化遵循
现有的控制令牌策略。必须更改密码的主体只能访问
认证检查、密码更改和注销。

- [ ] **步骤 5: 记录清理后的安全事件**

记录 `login_success`, `login_failed`, `logout`, `password_changed`, 和
`authorization_denied`。从不包括提交的凭证或 cookie 值。

- [ ] **步骤 6: 运行 API 测试并提交**

```bash
go test ./internal/api ./internal/auth -count=1
git add internal/api internal/auth
git commit -m "feat: require authenticated platform sessions"
```

---

### 任务 5: 为会话 MySQL 操作自动发放一次性授权

**文件:**
- 修改: `internal/api/operations.go`
- 修改: `internal/api/operations_test.go`
- 修改: `internal/approval/service.go`
- 修改: `internal/approval/service_test.go`

**接口:**
- 消耗: 认证的 `auth.Principal`
- 消耗: `approval.Service.Issue`
- 生成: 会话拥有的执行，无请求 `approval_token`

- [ ] **步骤 1: 添加失败的执行测试**

```go
func TestAuthenticatedAdminExecutionAutoIssuesAndConsumesGrant(t *testing.T)
func TestAuthenticatedOperatorExecutionAutoIssuesAndConsumesGrant(t *testing.T)
func TestViewerCannotExecuteOperation(t *testing.T)
func TestPlatformExecutionResponseNeverContainsApprovalSecret(t *testing.T)
func TestControlBearerCannotBypassExplicitGrant(t *testing.T)
func TestExternalAutomaticAuthorizationFieldIsRejected(t *testing.T)
```

断言存储的授权是 `consumed`，其 `IssuedBy` 是会话
用户名，且响应体中不包含 `cgag_` 或 `token_hash`。

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/api ./internal/approval -run 'AutoIssues|Viewer|ApprovalSecret|ControlBearer|AutomaticAuthorization' -count=1
```

- [ ] **步骤 3: 实现平台执行**

对于没有显式授权的会话请求:

```go
record, _, err := server.workflow.Plan(ctx, adapterRequest)
issued, err := server.approvals.Issue(ctx, approval.IssueRequest{
    Operation: record,
    IssuedBy: principal.Username,
    TTL: 5 * time.Minute,
})
execution, err := server.workflow.Execute(ctx, adapterRequest, issued.Token)
```

从不将令牌添加到响应状态、日志、审计消息或请求
上下文中。现有的显式授权执行仍对授权
服务自动化可用。

- [ ] **步骤 4: 保留自动故障转移分离**

运行:

```bash
go test ./internal/recovery ./internal/workflow -run Automatic -count=1
```

预期: 在没有平台会话或人工授权的情况下通过。

- [ ] **步骤 5: 运行 API/工作流测试并提交**

```bash
go test ./internal/api ./internal/approval ./internal/workflow ./internal/recovery -count=1
git add internal/api internal/approval
git commit -m "feat: auto-approve authenticated mysql operations"
```

---

### 任务 6: 通过平台角色授权 MySQL 元数据和节点生命周期

**文件:**
- 修改: `internal/workflow/workflow.go`
- 修改: `internal/workflow/workflow_test.go`
- 修改: `internal/lifecycle/manager.go`
- 修改: `internal/lifecycle/manager_test.go`
- 修改: `internal/api/node_sync.go`
- 修改: `internal/api/node_sync_test.go`
- 修改: `internal/api/server.go`
- 修改: `internal/api/server_test.go`

**接口:**
- 生成: 内部主体授权的元数据执行
- 生成: 内部主体授权的生命周期执行
- 保留: 非浏览器自动化显式服务令牌方法

- [ ] **步骤 1: 添加失败的角色路径测试**

测试认证管理员可以协调 MySQL 元数据并执行节点
同步而无需输入第二个令牌。测试操作员和查看者角色
被阻止执行这些管理操作。

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/api ./internal/workflow ./internal/lifecycle -run 'PlatformAdmin|PlatformRole|SessionLifecycle|SessionMetadata' -count=1
```

- [ ] **步骤 3: 添加显式的内部授权入口点**

不要从 API 传递配置的静态密钥。添加方法，其签名
使受信任的执行者显式:

```go
func (service *Service) ExecuteMetadataAuthorized(
    ctx context.Context,
    operation model.Operation,
    request adapter.MetadataRequest,
    actor string,
    commit func() error,
) (model.Execution, error)

func (manager *Manager) ExecuteAuthorized(
    ctx context.Context,
    request Request,
    plan Plan,
    secrets ExecutionSecrets,
    actor string,
) (Task, error)
```

这些方法仍需运行安全、锁、审计、执行、验证和
报告阶段；它们仅替换外部凭证验证步骤。

- [ ] **步骤 4: 将会话管理员路由到内部入口点**

保留显式外部自动化路径的令牌门控。会话管理员使用
主体授权的入口点。浏览器不再发送生命周期或
控制令牌。

- [ ] **步骤 5: 运行测试并提交**

```bash
go test ./internal/api ./internal/workflow ./internal/lifecycle -count=1
git add internal/api internal/workflow internal/lifecycle
git commit -m "feat: authorize mysql administration by platform role"
```

---

### 任务 7: 用登录和密码管理替换控制台令牌输入

**文件:**
- 修改: `internal/api/console.html`
- 修改: `internal/api/console_test.go`

**接口:**
- 消耗: `/api/v1/auth/login`, `/me`, `/logout`, `/password`
- 生成: 无手动审批输入的认证控制台

- [ ] **步骤 1: 编写失败的控制台契约测试**

```go
func TestConsoleHasLoginAndForcedPasswordChange(t *testing.T)
func TestConsoleSendsCSRFOnMutatingRequests(t *testing.T)
func TestConsoleOperationDoesNotHandleApprovalToken(t *testing.T)
func TestConsoleShowsAuthenticatedUserAndLogout(t *testing.T)
```

禁止的字符串包括:

```text
id="approval-token"
state.approvalToken
approval_token: state.approvalToken
id="admin-token"
id="lifecycle-token"
```

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./internal/api -run Console -count=1
```

- [ ] **步骤 3: 添加登录外壳**

第一个视口是一个聚焦的登录表单，包含产品身份、用户名、
密码、提交状态和通用错误。不要添加营销首页。

- [ ] **步骤 4: 添加认证的获取行为**

从 `document.cookie` 读取 `clusterguard_csrf` 并在
修改请求上设置 `X-CSRF-Token`。在 `401` 上，清除内存状态并显示登录。在
`password_change_required` 上，加载平台数据前显示密码更改对话框。

- [ ] **步骤 5: 简化 MySQL 执行**

操作负载包含:

```js
{
  operation: {
    cluster_id: cluster.resource_id,
    engine: cluster.engine,
    kind,
    requested_by: state.currentUser.username
  },
  target_id: targetID,
  idempotency_key: idempotencyKey
}
```

它不包含审批令牌。本地解锁按钮仍然存在。

- [ ] **步骤 6: 添加账户控制**

在设置和外壳中显示用户名、角色、更改密码和注销。不要
在本地存储中持久化密码、会话令牌或 CSRF 值。

- [ ] **步骤 7: 运行控制台测试和 JavaScript 语法验证**

```bash
go test ./internal/api -run Console -count=1
node -e 'const fs=require("fs"),vm=require("vm"); const h=fs.readFileSync("internal/api/console.html","utf8"); const s=[...h.matchAll(/<script>([\\s\\S]*?)<\\/script>/g)].map(x=>x[1]).join("\\n"); new vm.Script(s);'
```

- [ ] **步骤 8: 提交**

```bash
git add internal/api/console.html internal/api/console_test.go
git commit -m "feat: add authenticated HA console"
```

---

### 任务 8: 更新 CLI、文档、打包和验收

**文件:**
- 修改: `README.md`
- 修改: `docs/architecture.md`
- 修改: `docs/operations.md`
- 修改: `docs/mysql-feature-parity-acceptance.md`
- 修改: `configs/clusterguard.example.json`
- 修改: `packaging/systemd/clusterguard.env.example`
- 修改: `scripts/clusterguard-ha-matrix.sh`
- 修改: `scripts/scripts_test.go`

**接口:**
- 文档: 默认管理员、强制首次密码更改、cookie/CSRF 行为
- 保留: 服务自动化的显式授权 API

- [ ] **步骤 1: 添加失败的脚本/文档契约测试**

断言浏览器矩阵登录、在新环境中更改引导密码、获取 CSRF，并执行无审批令牌字段。断言显式服务自动化授权矩阵仍测试重放拒绝。

- [ ] **步骤 2: 运行并验证 RED**

```bash
go test ./scripts -run 'Authentication|Approval|Matrix' -count=1
```

- [ ] **步骤 3: 更新操作员文档**

文档:

- 初始 `admin/generated-bootstrap-password`；
- 强制首次密码更改；
- 会话生命周期；
- 注销和会话撤销；
- 浏览器自动审批；
- 显式 API 授权工作流；
- 当管理员密码丢失时的恢复程序。

- [ ] **步骤 4: 更新矩阵测试**

使用 cookie jar 和 CSRF 头进行控制台风格执行。保留一个单独的显式授权测试用于服务客户端。

- [ ] **步骤 5: 运行脚本测试并提交**

```bash
go test ./scripts -count=1
git add README.md docs configs packaging scripts
git commit -m "docs: document authenticated mysql operations"
```

---

### 任务 9: 全面验证和发布候选版本

**文件：**
- 仅在验证暴露回归时进行修改。

**接口：**
- 产出：已验证的 Linux 发布包

- [ ] **步骤 1：运行完整测试**

```bash
go test ./... -count=1
go test -race ./... -count=1
```

- [ ] **步骤 2：运行构建和静态验证**

```bash
go build ./...
go vet ./...
git diff --check
for file in configs/*.json; do jq empty "$file" || exit 1; done
for file in scripts/*.sh; do bash -n "$file" || exit 1; done
```

- [ ] **步骤 3：运行安全扫描**

```bash
rg -n 'generated-bootstrap-password|password_hash|token_hash|csrf_hash' . \
  --glob '!docs/**' --glob '!**/*_test.go'
rg -n 'approval-token|admin-token|lifecycle-token|state\\.approvalToken' \
  internal/api/console.html
```

预期：

- `generated-bootstrap-password` 仅出现在引导常量和迁移/引导逻辑中，从不在生成的元数据中出现；
- 哈希值从不出现于公共 API 响应类型中；
- 控制台 token 输入扫描返回无匹配项。

- [ ] **步骤 4：构建 Linux 二进制文件和打包**

```bash
mkdir -p /tmp/clusterguard-platform-auth-rc
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/clusterguard ./cmd/clusterguard
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/cgctl ./cmd/cgctl
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/clusterguard-agent ./cmd/clusterguard-agent
tar -C /tmp/clusterguard-platform-auth-rc -czf /tmp/clusterguard-ha-platform-auth-rc1-linux-amd64.tar.gz .
shasum -a 256 /tmp/clusterguard-ha-platform-auth-rc1-linux-amd64.tar.gz
```

- [ ] **步骤 5：部署并测试三节点 MySQL 实验室**

验证：

1. 使用引导管理员登录；
2. 在更改密码之前阻止变更操作；
3. 更改密码并重新登录；
4. 一键切换且无需审批输入；
5. VIP 跟随新的主节点；
6. 旧主节点重新加入；
7. 会话注销和重放拒绝；
8. 显式的外部授权保持单次使用；
9. 自动故障转移仍不需要人工令牌；
10. 所有审计和报告记录标识已认证的用户名。

- [ ] **步骤 6：提交任何接受证据**

```bash
git add docs/mysql-feature-parity-acceptance.md
git commit -m "test: verify authenticated mysql operations"
```
