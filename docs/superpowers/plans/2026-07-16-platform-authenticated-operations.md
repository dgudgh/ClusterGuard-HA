# Platform Authenticated Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Raft-replicated platform users and sessions so a logged-in ClusterGuard HA operator can execute MySQL operations without manually entering an approval token, while the server still issues and atomically consumes a plan-bound one-time grant.

**Architecture:** Add `PlatformUser`, `PlatformSession`, and `SecurityEvent` resources to the existing replicated metadata snapshot. An `internal/auth` service owns Argon2id password hashing, opaque sessions, CSRF, bootstrap administration, password changes, and role policy. The API authenticates every platform request, then session-authenticated MySQL execution creates and consumes the existing `ApprovalGrant` entirely on the server.

**Tech Stack:** Go 1.22, `golang.org/x/crypto/argon2`, existing JSON snapshot repository, Hashicorp Raft snapshot replication, `net/http`, embedded HTML/CSS/JavaScript console.

## Global Constraints

- Default administrator username is exactly `admin`.
- Default administrator password is exactly `admin123`.
- The bootstrap password is never persisted or logged as plaintext.
- The bootstrap administrator must change the default password before mutations.
- Password storage uses Argon2id with a random salt.
- Session and CSRF secrets are stored only as SHA-256 hashes.
- Browser sessions use `HttpOnly`, `SameSite=Strict`, and HTTPS-aware `Secure` cookies.
- Browser mutations require `X-CSRF-Token`.
- The browser never receives an operation approval grant.
- Session-authenticated execution must still pass Safety Guard, Operation Lock, ApprovalGrant consumption, Verification, Audit, and Report.
- Automatic failover continues to use internal incident authorization.
- PostgreSQL, Oracle, and SQL Server execution capability remains unchanged and fail-closed.

---

### Task 1: Persist Platform Users, Sessions, And Security Events

**Files:**
- Create: `pkg/model/auth.go`
- Create: `internal/store/auth.go`
- Create: `internal/store/auth_test.go`
- Modify: `internal/store/repository.go`
- Modify: `internal/store/replication.go`
- Modify: `internal/store/replication_test.go`

**Interfaces:**
- Produces: `model.PlatformUser`, `model.PlatformSession`, `model.SecurityEvent`
- Produces: `Repository.CreatePlatformUser`, `PlatformUser`, `PlatformUserByUsername`, `PlatformUsers`
- Produces: `Repository.PutPlatformSession`, `PlatformSession`, `RevokePlatformSession`
- Produces: `Repository.ChangePlatformPassword`, `RecordSecurityEvent`, `SecurityEvents`

- [ ] **Step 1: Write failing model and store tests**

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
    if bytes.Contains(encoded, []byte("admin123")) || bytes.Contains(encoded, []byte("session-secret")) {
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

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/store ./pkg/model -run 'PlatformUser|PlatformSession|SecurityEvent' -count=1
```

Expected: compilation failure because the auth models and repository methods do not exist.

- [ ] **Step 3: Add the auth models**

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

- [ ] **Step 4: Add snapshot maps, validation, cloning, and atomic password change**

`ChangePlatformPassword` must update the password hash, clear
`MustChangePassword`, increment `AuthRevision`, and revoke all active sessions
for the user in one snapshot commit.

- [ ] **Step 5: Run focused and replication tests**

Run:

```bash
go test ./internal/store ./pkg/model -run 'PlatformUser|PlatformSession|SecurityEvent|Replicate' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/auth.go internal/store/auth.go internal/store/auth_test.go internal/store/repository.go internal/store/replication.go internal/store/replication_test.go
git commit -m "feat: persist platform users and sessions"
```

---

### Task 2: Implement Argon2id Passwords And Opaque Sessions

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/auth/password.go`
- Create: `internal/auth/password_test.go`
- Create: `internal/auth/service.go`
- Create: `internal/auth/service_test.go`

**Interfaces:**
- Produces: `auth.Argon2Hasher.Hash`, `auth.Argon2Hasher.Verify`
- Produces: `auth.Service.EnsureBootstrapAdmin`
- Produces: `auth.Service.Login`, `Authenticate`, `Logout`, `ChangePassword`
- Produces: `auth.Principal`

- [ ] **Step 1: Add failing password tests**

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

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/auth -run Argon2 -count=1
```

Expected: package or symbol missing.

- [ ] **Step 3: Add the official crypto dependency**

```bash
go get golang.org/x/crypto@v0.17.0
```

- [ ] **Step 4: Implement encoded Argon2id hashes**

Use:

```go
type Argon2Params struct {
    Memory      uint32
    Iterations  uint32
    Parallelism uint8
    SaltLength  uint32
    KeyLength   uint32
}
```

Encode hashes in standard self-describing form:

```text
$argon2id$v=19$m=65536,t=3,p=2$<salt-base64>$<key-base64>
```

Parsing must reject unknown versions, malformed fields, oversized cost values,
and incorrect salt/key lengths before allocating memory.

- [ ] **Step 5: Add failing authentication service tests**

Cover bootstrap, duplicate bootstrap, login, generic invalid credentials,
disabled users, expiry, revocation, auth-revision mismatch, and password change.

```go
func TestLoginIssuesHashedSessionAndPasswordChangeRevokesIt(t *testing.T) {
    service := newTestService(t)
    user, err := service.EnsureBootstrapAdmin(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    login, err := service.Login(context.Background(), "admin", "admin123")
    if err != nil {
        t.Fatal(err)
    }
    if login.SessionToken == "" || login.CSRFToken == "" {
        t.Fatal("login did not return opaque secrets")
    }
    if _, err := service.ChangePassword(context.Background(), login.SessionToken, "admin123", "A-new-secure-password-123"); err != nil {
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

- [ ] **Step 6: Run and verify RED**

```bash
go test ./internal/auth -run 'Bootstrap|Login|Session|Password' -count=1
```

Expected: service symbols missing.

- [ ] **Step 7: Implement the service**

Tokens use:

```text
cgs_<session-uuid>.<32-byte-base64url-secret>
```

`Login` stores only SHA-256 hashes. `Authenticate` performs constant-time hash
comparison and checks user state, expiry, revocation, and auth revision.

- [ ] **Step 8: Run focused tests and commit**

```bash
go test ./internal/auth ./internal/store -count=1
git add go.mod go.sum internal/auth internal/store
git commit -m "feat: add platform authentication service"
```

---

### Task 3: Bootstrap Authentication In Runtime

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/runtime_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `auth.Service.EnsureBootstrapAdmin`
- Produces: a shared runtime auth service passed to the API

- [ ] **Step 1: Add a failing runtime bootstrap test**

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

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/runtime -run BootstrapDefaultAdministrator -count=1
```

- [ ] **Step 3: Wire one auth service to store and API**

Construct the service after consensus is attached so bootstrap commits through
the same Raft snapshot path as other metadata.

- [ ] **Step 4: Remove browser dependence on configured control credentials**

Keep `control_token_env` for service automation. Do not use it as the platform
administrator password or browser session secret.

- [ ] **Step 5: Run runtime/config tests and commit**

```bash
go test ./internal/runtime ./internal/config -count=1
git add internal/runtime internal/config
git commit -m "feat: bootstrap platform administrator"
```

---

### Task 4: Add Login, Session, CSRF, And Role Middleware

**Files:**
- Create: `internal/api/auth.go`
- Create: `internal/api/auth_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`

**Interfaces:**
- Consumes: `auth.Service`
- Produces: `WithAuthentication`
- Produces: `POST /api/v1/auth/login`
- Produces: `GET /api/v1/auth/me`
- Produces: `POST /api/v1/auth/logout`
- Produces: `POST /api/v1/auth/password`
- Produces: request-context `auth.Principal`

- [ ] **Step 1: Write failing HTTP authentication tests**

Cover:

```go
func TestPlatformAPILoginRequiresCSRFForMutation(t *testing.T)
func TestPlatformAPIBlocksMutationsUntilBootstrapPasswordChanges(t *testing.T)
func TestPlatformAPIPasswordChangeRevokesSession(t *testing.T)
func TestPlatformAPIRolePolicyBlocksViewerMutation(t *testing.T)
func TestLoginFailureDoesNotRevealWhetherUserExists(t *testing.T)
```

The CSRF test must make three requests: no header, wrong header, correct header.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api -run 'Login|Session|CSRF|PasswordChange|RolePolicy' -count=1
```

- [ ] **Step 3: Implement auth cookies and routes**

Cookie constants:

```go
const (
    sessionCookieName = "clusterguard_session"
    csrfCookieName    = "clusterguard_csrf"
)
```

Use request TLS state or forwarded HTTPS information only from explicitly
trusted configuration; otherwise derive `Secure` from `request.TLS != nil`.

- [ ] **Step 4: Implement middleware policy**

Unauthenticated exceptions are limited to:

- `GET /`;
- `HEAD /`;
- `POST /api/v1/auth/login`;
- signed `/api/v1/agent/reconcile`;
- dedicated `/api/v1/monitoring/*`.

Cookie-authenticated mutation requires CSRF. Bearer service automation follows
the existing control-token policy. A must-change-password principal can reach
only auth inspection, password change, and logout.

- [ ] **Step 5: Record sanitized security events**

Record `login_success`, `login_failed`, `logout`, `password_changed`, and
`authorization_denied`. Never include submitted credentials or cookie values.

- [ ] **Step 6: Run API tests and commit**

```bash
go test ./internal/api ./internal/auth -count=1
git add internal/api internal/auth
git commit -m "feat: require authenticated platform sessions"
```

---

### Task 5: Auto-Issue One-Time Grants For Session MySQL Operations

**Files:**
- Modify: `internal/api/operations.go`
- Modify: `internal/api/operations_test.go`
- Modify: `internal/approval/service.go`
- Modify: `internal/approval/service_test.go`

**Interfaces:**
- Consumes: authenticated `auth.Principal`
- Consumes: `approval.Service.Issue`
- Produces: session-owned execution with no request `approval_token`

- [ ] **Step 1: Add failing execution tests**

```go
func TestAuthenticatedAdminExecutionAutoIssuesAndConsumesGrant(t *testing.T)
func TestAuthenticatedOperatorExecutionAutoIssuesAndConsumesGrant(t *testing.T)
func TestViewerCannotExecuteOperation(t *testing.T)
func TestPlatformExecutionResponseNeverContainsApprovalSecret(t *testing.T)
func TestControlBearerCannotBypassExplicitGrant(t *testing.T)
func TestExternalAutomaticAuthorizationFieldIsRejected(t *testing.T)
```

Assert that the stored grant is `consumed`, its `IssuedBy` is the session
username, and the response body contains neither `cgag_` nor `token_hash`.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api ./internal/approval -run 'AutoIssues|Viewer|ApprovalSecret|ControlBearer|AutomaticAuthorization' -count=1
```

- [ ] **Step 3: Implement platform execution**

For a session request with no explicit grant:

```go
record, _, err := server.workflow.Plan(ctx, adapterRequest)
issued, err := server.approvals.Issue(ctx, approval.IssueRequest{
    Operation: record,
    IssuedBy: principal.Username,
    TTL: 5 * time.Minute,
})
execution, err := server.workflow.Execute(ctx, adapterRequest, issued.Token)
```

Never add the token to response state, logs, audit messages, or request
contexts. Existing explicit grant execution remains available to authorized
service automation.

- [ ] **Step 4: Preserve automatic failover separation**

Run:

```bash
go test ./internal/recovery ./internal/workflow -run Automatic -count=1
```

Expected: PASS without a platform session or human grant.

- [ ] **Step 5: Run API/workflow tests and commit**

```bash
go test ./internal/api ./internal/approval ./internal/workflow ./internal/recovery -count=1
git add internal/api internal/approval
git commit -m "feat: auto-approve authenticated mysql operations"
```

---

### Task 6: Authorize MySQL Metadata And Node Lifecycle Through Platform Roles

**Files:**
- Modify: `internal/workflow/workflow.go`
- Modify: `internal/workflow/workflow_test.go`
- Modify: `internal/lifecycle/manager.go`
- Modify: `internal/lifecycle/manager_test.go`
- Modify: `internal/api/node_sync.go`
- Modify: `internal/api/node_sync_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`

**Interfaces:**
- Produces: internal principal-authorized metadata execution
- Produces: internal principal-authorized lifecycle execution
- Retains: explicit service-token methods for non-browser automation

- [ ] **Step 1: Add failing role-path tests**

Test that an authenticated admin can reconcile MySQL metadata and execute node
sync without entering a second token. Test that operator and viewer roles are
blocked from these administrative actions.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api ./internal/workflow ./internal/lifecycle -run 'PlatformAdmin|PlatformRole|SessionLifecycle|SessionMetadata' -count=1
```

- [ ] **Step 3: Add explicit internal authorization entry points**

Do not pass configured static secrets from the API. Add methods whose signatures
make the trusted actor explicit:

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

These methods must still run safety, lock, audit, execution, verification, and
reporting stages; they replace only the external credential-validation step.

- [ ] **Step 4: Route session administrators to internal entry points**

Keep explicit external automation paths token-gated. Session administrators use
the principal-authorized entry points. The browser no longer sends lifecycle or
control tokens.

- [ ] **Step 5: Run tests and commit**

```bash
go test ./internal/api ./internal/workflow ./internal/lifecycle -count=1
git add internal/api internal/workflow internal/lifecycle
git commit -m "feat: authorize mysql administration by platform role"
```

---

### Task 7: Replace Console Token Inputs With Login And Password Management

**Files:**
- Modify: `internal/api/console.html`
- Modify: `internal/api/console_test.go`

**Interfaces:**
- Consumes: `/api/v1/auth/login`, `/me`, `/logout`, `/password`
- Produces: authenticated console with no manual approval input

- [ ] **Step 1: Write failing console contract tests**

```go
func TestConsoleHasLoginAndForcedPasswordChange(t *testing.T)
func TestConsoleSendsCSRFOnMutatingRequests(t *testing.T)
func TestConsoleOperationDoesNotHandleApprovalToken(t *testing.T)
func TestConsoleShowsAuthenticatedUserAndLogout(t *testing.T)
```

Forbidden strings include:

```text
id="approval-token"
state.approvalToken
approval_token: state.approvalToken
id="admin-token"
id="lifecycle-token"
```

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/api -run Console -count=1
```

- [ ] **Step 3: Add the login shell**

The first viewport is a focused login form with product identity, username,
password, submit state, and generic error. Do not add a marketing landing page.

- [ ] **Step 4: Add authenticated fetch behavior**

Read `clusterguard_csrf` from `document.cookie` and set `X-CSRF-Token` on
mutating requests. On `401`, clear in-memory state and show login. On
`password_change_required`, show the password-change dialog before loading
platform data.

- [ ] **Step 5: Simplify MySQL execution**

The operation payload contains:

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

It contains no approval token. The local unlock button remains.

- [ ] **Step 6: Add account controls**

Show username, role, change password, and logout in Settings and the shell. Do
not persist passwords, session tokens, or CSRF values in local storage.

- [ ] **Step 7: Run console tests and JavaScript syntax validation**

```bash
go test ./internal/api -run Console -count=1
node -e 'const fs=require("fs"),vm=require("vm"); const h=fs.readFileSync("internal/api/console.html","utf8"); const s=[...h.matchAll(/<script>([\\s\\S]*?)<\\/script>/g)].map(x=>x[1]).join("\\n"); new vm.Script(s);'
```

- [ ] **Step 8: Commit**

```bash
git add internal/api/console.html internal/api/console_test.go
git commit -m "feat: add authenticated HA console"
```

---

### Task 8: Update CLI, Documentation, Packaging, And Acceptance

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`
- Modify: `docs/mysql-feature-parity-acceptance.md`
- Modify: `configs/clusterguard.example.json`
- Modify: `packaging/systemd/clusterguard.env.example`
- Modify: `scripts/clusterguard-ha-matrix.sh`
- Modify: `scripts/scripts_test.go`

**Interfaces:**
- Documents: default admin, forced first password change, cookie/CSRF behavior
- Preserves: explicit grant API for service automation

- [ ] **Step 1: Add failing script/document contract tests**

Assert the browser matrix logs in, changes the bootstrap password in a fresh
environment, obtains CSRF, and executes without an approval-token field. Assert
the explicit service-automation grant matrix still tests replay rejection.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./scripts -run 'Authentication|Approval|Matrix' -count=1
```

- [ ] **Step 3: Update operator documentation**

Document:

- initial `admin/admin123`;
- mandatory first password change;
- session lifetime;
- logout and session revocation;
- browser auto-approval;
- explicit API grant workflow;
- recovery procedure when the administrator password is lost.

- [ ] **Step 4: Update matrix tests**

Use a cookie jar and CSRF header for console-style execution. Keep one separate
explicit grant test for service clients.

- [ ] **Step 5: Run script tests and commit**

```bash
go test ./scripts -count=1
git add README.md docs configs packaging scripts
git commit -m "docs: document authenticated mysql operations"
```

---

### Task 9: Full Verification And Release Candidate

**Files:**
- Modify only if verification exposes a regression.

**Interfaces:**
- Produces: verified Linux release bundle

- [ ] **Step 1: Run complete tests**

```bash
go test ./... -count=1
go test -race ./... -count=1
```

- [ ] **Step 2: Run build and static validation**

```bash
go build ./...
go vet ./...
git diff --check
for file in configs/*.json; do jq empty "$file" || exit 1; done
for file in scripts/*.sh; do bash -n "$file" || exit 1; done
```

- [ ] **Step 3: Run security scans**

```bash
rg -n 'admin123|password_hash|token_hash|csrf_hash' . \
  --glob '!docs/**' --glob '!**/*_test.go'
rg -n 'approval-token|admin-token|lifecycle-token|state\\.approvalToken' \
  internal/api/console.html
```

Expected:

- `admin123` appears only in the bootstrap constant and migration/bootstrap
  logic, never in generated metadata;
- hashes never appear in public API response types;
- console token-input scan returns no matches.

- [ ] **Step 4: Build Linux binaries and bundle**

```bash
mkdir -p /tmp/clusterguard-platform-auth-rc
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/clusterguard ./cmd/clusterguard
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/cgctl ./cmd/cgctl
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/clusterguard-platform-auth-rc/clusterguard-agent ./cmd/clusterguard-agent
tar -C /tmp/clusterguard-platform-auth-rc -czf /tmp/clusterguard-ha-platform-auth-rc1-linux-amd64.tar.gz .
shasum -a 256 /tmp/clusterguard-ha-platform-auth-rc1-linux-amd64.tar.gz
```

- [ ] **Step 5: Deploy and test the three-node MySQL lab**

Verify:

1. login with the bootstrap administrator;
2. mutations blocked before password change;
3. password change and re-login;
4. one-click switchover with no approval input;
5. VIP follows the new primary;
6. old primary rejoin;
7. session logout and replay rejection;
8. explicit external grant remains single-use;
9. automatic failover still needs no human token;
10. all audit and report records identify the authenticated username.

- [ ] **Step 6: Commit any acceptance evidence**

```bash
git add docs/mysql-feature-parity-acceptance.md
git commit -m "test: verify authenticated mysql operations"
```
