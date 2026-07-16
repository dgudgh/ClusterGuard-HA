# ClusterGuard HA One-Time Approval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace reusable workflow approval secrets with durable single-use grants, let the internal automatic-recovery controller execute without a human token, and remove the long-lived control token from normal Web-console switchovers.

**Architecture:** A new approval service issues random, plan-bound grants from an administrator-authenticated API. The repository stores only grant hashes in the existing Raft-replicated snapshot and atomically consumes a grant with the durable operation APPROVE transition. Manual HTTP execution can use only a matching grant; automatic recovery calls a separate internal workflow method that cannot be selected by JSON input.

**Tech Stack:** Go 1.22 standard library, existing JSON snapshot repository, HashiCorp Raft snapshot CAS, embedded HTML/CSS/JavaScript console, `flag`-based `cgctl`.

## Global Constraints

- Default grant TTL is 5 minutes; maximum TTL is 15 minutes.
- Plaintext approval grants are returned once and never persisted or logged.
- A grant is bound to one durable operation, cluster, engine, kind, target, observation, and plan digest.
- Manual execution consumes a grant once before adapter mutation.
- Automatic failover has no human grant but still passes Safety Guard, operation lock, fencing, execution, verification, audit, and report.
- Public HTTP input cannot select automatic authorization.
- Existing administrative mutation routes remain protected by `CG_CONTROL_TOKEN`.
- `CG_APPROVAL_TOKEN` is deprecated and is never accepted as a compatibility fallback.
- PostgreSQL, Oracle, and SQL Server mutations remain unsupported.

---

### Task 1: Approval Grant Resource and Durable Store

**Files:**
- Create: `pkg/model/approval.go`
- Create: `internal/store/approvals.go`
- Create: `internal/store/approvals_test.go`
- Modify: `internal/store/repository.go`
- Modify: `internal/store/replication_test.go`

**Interfaces:**
- Produces: `model.ApprovalGrant`, `model.ApprovalGrantStatus`.
- Produces: `Repository.PutApprovalGrant`, `Repository.ApprovalGrant`, `Repository.ApprovalGrants`.
- Produces: `Repository.ConsumeApprovalGrant`.

- [ ] **Step 1: Write failing model and repository tests**

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

Add tests that a replicated follower receives the same active grant and that invalid UUIDs, missing hashes, invalid expiry, and duplicate IDs fail closed.

- [ ] **Step 2: Run the focused tests and verify failure**

Run:

```bash
go test ./internal/store ./pkg/model -run ApprovalGrant -count=1
```

Expected: compilation fails because `model.ApprovalGrant` and repository methods do not exist.

- [ ] **Step 3: Add the resource model**

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

- [ ] **Step 4: Add grants to the snapshot and CRUD methods**

Add:

```go
ApprovalGrants map[model.ResourceID]model.ApprovalGrant `json:"approval_grants"`
```

Initialize the map in `emptySnapshot`, clone it during snapshot copies, validate every grant during decode, and implement sorted read methods. `PutApprovalGrant` must use `mutationMu`, clone the snapshot, increment metadata revision rules consistently, and call `commitSnapshotLocked`.

- [ ] **Step 5: Implement atomic consumption**

Define:

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

`ConsumeApprovalGrant` must, in one candidate snapshot:

1. load the active grant and operation;
2. compare token hash in constant time;
3. compare operation, cluster, engine, kind, target, observation, and plan digest;
4. reject expiry or prior consumption;
5. mark the grant consumed;
6. apply the APPROVE transition to the operation;
7. commit one snapshot.

- [ ] **Step 6: Run store and replication tests**

Run:

```bash
go test ./internal/store ./pkg/model -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/model/approval.go internal/store/approvals.go internal/store/approvals_test.go internal/store/repository.go internal/store/replication_test.go
git commit -m "feat: persist one-time approval grants"
```

---

### Task 2: Grant Issuer and Validator

**Files:**
- Create: `internal/approval/service.go`
- Create: `internal/approval/service_test.go`

**Interfaces:**
- Consumes: repository grant CRUD and atomic consumption from Task 1.
- Produces: `approval.Service.Issue`, `approval.Service.AuthorizeIntent`, `approval.Service.Consume`.
- Produces: `approval.ParseToken`.

- [ ] **Step 1: Write failing token and lifecycle tests**

Cover:

- token format `cgag_<uuid>.<base64url-secret>`;
- 32 random secret bytes;
- only SHA-256 hash reaches `PutApprovalGrant`;
- TTL defaults to 5 minutes and rejects values above 15 minutes;
- malformed, expired, revoked, consumed, wrong-target, wrong-plan, and wrong-operation grants are rejected;
- successful consumption cannot be repeated.

Use a deterministic random reader and clock:

```go
service := approval.New(store, bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)), func() time.Time { return now })
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/approval -count=1
```

Expected: package or exported functions do not exist.

- [ ] **Step 3: Implement secret generation and parsing**

```go
func ParseToken(token string) (model.ResourceID, []byte, error)
func tokenHash(secret []byte) string
```

Use `crypto/rand.Reader`, `encoding/base64.RawURLEncoding`, `crypto/sha256`, and `crypto/subtle`. Never include the token in errors.

- [ ] **Step 4: Implement issuance**

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

Require a planned durable operation with non-empty observation and plan digest. Persist the hash-only grant before returning the secret.

- [ ] **Step 5: Implement non-consuming intent authorization**

`AuthorizeIntent` parses the grant ID, compares the secret hash, rejects non-active or expired grants, and confirms operation ID plus target. It returns sanitized grant metadata so the API can load the bound durable operation before entering the workflow.

- [ ] **Step 6: Implement atomic workflow consumption**

`Consume` accepts the current `model.OperationRecord`, calls `Repository.ConsumeApprovalGrant`, and returns the consumed grant ID for audit text. Map repository conflicts to stable errors:

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

- [ ] **Step 7: Run tests**

Run:

```bash
go test ./internal/approval -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/approval
git commit -m "feat: issue and consume scoped approval grants"
```

---

### Task 3: Workflow Manual and Automatic Authorization Paths

**Files:**
- Modify: `internal/workflow/workflow.go`
- Modify: `internal/workflow/durable.go`
- Modify: `internal/workflow/workflow_test.go`
- Modify: `internal/workflow/durable_test.go`
- Modify: `internal/recovery/controller.go`
- Modify: `internal/recovery/controller_test.go`

**Interfaces:**
- Consumes: `approval.Service.Consume`.
- Produces: `workflow.Service.Execute` for manual grants.
- Produces: `workflow.Service.ExecuteAutomatic` for internal failover.

- [ ] **Step 1: Write failing workflow tests**

Add tests proving:

1. manual execution calls approval consumption with the durable plan;
2. grant consumption and APPROVE transition are not performed twice;
3. automatic execution succeeds with an empty human token;
4. automatic execution rejects switchover, a non-automatic actor, or a missing incident ID;
5. normal `Execute` cannot select automatic authorization through parameters.

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/workflow ./internal/recovery -run 'Approval|Automatic' -count=1
```

Expected: FAIL because the automatic entry point and grant consumer do not exist.

- [ ] **Step 3: Replace the validator interface**

```go
type ApprovalConsumer interface {
    Consume(context.Context, model.OperationRecord, string) (model.ResourceID, error)
}
```

Keep the authorization mode private:

```go
type executionAuthorization struct {
    approvalToken string
    automatic     bool
    incidentID    string
}
```

- [ ] **Step 4: Add separate entry points**

```go
func (service *Service) Execute(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error)
func (service *Service) ExecuteAutomatic(ctx context.Context, request adapter.OperationRequest, incidentID string) (model.Execution, error)
```

`ExecuteAutomatic` must require `OperationFailover`, overwrite `RequestedBy` with `recovery.AutomaticRecoveryActor` through a cycle-free shared constant or a workflow constant, and reject an empty incident ID.

- [ ] **Step 5: Consume manual approval atomically**

After lock acquisition and topology revalidation, call `approval.Consume` with the latest durable record. Do not call the old separate APPROVE transition. Audit:

```text
one-time approval grant <grant-id> consumed
```

For automatic execution, atomically advance APPROVE with:

```text
automatic recovery authorized for incident <incident-id>
```

No token is created or consumed.

- [ ] **Step 6: Update recovery controller**

Change `OperationExecutor` to:

```go
type OperationExecutor interface {
    ExecuteAutomatic(context.Context, adapter.OperationRequest, string) (model.Execution, error)
}
```

Remove `approvalToken` from `Controller`, `NewController`, and `configured`. Pass the incident-derived stable identity to `ExecuteAutomatic`.

- [ ] **Step 7: Run workflow and recovery tests**

Run:

```bash
go test ./internal/workflow ./internal/recovery -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/workflow internal/recovery
git commit -m "feat: separate manual approval from automatic recovery"
```

---

### Task 4: Approval API and Execute Route Authorization

**Files:**
- Create: `internal/api/approvals.go`
- Create: `internal/api/approvals_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/operations.go`
- Modify: `internal/api/operations_test.go`

**Interfaces:**
- Consumes: approval issuer and validator from Task 2.
- Produces: `POST/GET /api/v1/approvals`.
- Produces: manual operation execution authorized by a grant without the control token.

- [ ] **Step 1: Write failing API tests**

Cover:

- grant issuance without the administrative bearer token returns 401;
- follower issuance returns Leader/quorum information;
- issuance plans the operation and returns one plaintext token;
- list/show responses omit token hash and plaintext;
- `/api/v1/operations/execute` accepts the matching grant without
  `CG_CONTROL_TOKEN`;
- reused or mismatched grants return a blocked response;
- arbitrary POST routes still require the control token;
- JSON fields such as `automatic`, `authorization_mode`, or
  `requested_by=clusterguard-automatic-recovery` cannot select the automatic
  workflow entry point.

- [ ] **Step 2: Run focused API tests**

Run:

```bash
go test ./internal/api -run 'Approval|ControlToken|Automatic' -count=1
```

Expected: FAIL.

- [ ] **Step 3: Add server options and routes**

```go
func WithApprovalService(service *approval.Service) ServerOption
```

Route `/api/v1/approvals` before generic operation routes. POST remains under administrative control authentication and mutation authority. GET returns sanitized metadata.

- [ ] **Step 4: Implement grant issuance**

Decode cluster, engine, kind, target, issuer, TTL, and optional idempotency key. Call `workflow.Plan`, then `approval.Service.Issue`. The response is:

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

- [ ] **Step 5: Make authentication route-aware**

Replace the all-POST global control check with:

```go
func manualGrantExecutionRoute(method, path string) bool
```

Only operation execute endpoints bypass `authorizeControl`; route code must
call `AuthorizeIntent` before `workflow.Execute`. Every other mutation keeps
both administrative authentication and Leader/quorum checks.

- [ ] **Step 6: Execute the grant-bound operation**

For `/api/v1/operations/execute`, use the grant to load its durable operation.
Reject payload cluster, engine, kind, or target values that differ from the
grant-bound record. This prevents an unauthenticated caller from creating
operation records with random tokens.

- [ ] **Step 7: Preserve safe public errors**

Map approval errors to Chinese-ready stable English API messages without
returning secret values. Ensure operation logs and reports contain grant IDs,
not grant strings.

- [ ] **Step 8: Run API tests**

Run:

```bash
go test ./internal/api -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/api
git commit -m "feat: expose privileged one-time approval API"
```

---

### Task 5: Runtime, Configuration, and Automatic Failover Migration

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/runtime_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `configs/clusterguard.example.json`

**Interfaces:**
- Consumes: approval service and new workflow constructor.
- Produces: runtime automatic recovery without `CG_APPROVAL_TOKEN`.

- [ ] **Step 1: Write failing runtime/config tests**

Assert:

- automatic failover starts with consensus, agent fencing, and failure
  evidence even when the old approval token is empty;
- runtime wires one shared approval service into API and workflow;
- an existing `CG_APPROVAL_TOKEN` produces a deprecation warning but is not
  accepted by the validator;
- incomplete consensus/fencing configuration still blocks startup.

- [ ] **Step 2: Run tests**

Run:

```bash
go test ./internal/runtime ./internal/config -count=1
```

Expected: FAIL on old static-token assumptions.

- [ ] **Step 3: Wire the approval service**

Construct:

```go
approvalService := approval.New(repository, rand.Reader, time.Now)
```

Pass it to `workflow.New` and `api.WithApprovalService`.

- [ ] **Step 4: Remove automatic static approval dependency**

Delete the `configuration.ApprovalToken == ""` startup condition and remove the
token argument from `recovery.NewController`.

- [ ] **Step 5: Deprecate configuration**

Keep parsing the old field for one release only so startup can log:

```text
CG_APPROVAL_TOKEN is deprecated and ignored; use one-time approval grants
```

Do not construct `workflow.TokenApproval`.

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/runtime ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime internal/config configs/clusterguard.example.json
git commit -m "refactor: remove static approval from runtime"
```

---

### Task 6: cgctl Approval Commands

**Files:**
- Modify: `cmd/cgctl/main.go`
- Modify: `cmd/cgctl/main_test.go`

**Interfaces:**
- Produces: `cgctl approval issue`, `approval list`, and `approval show`.

- [ ] **Step 1: Write failing CLI tests**

Test request paths, JSON bodies, administrative Authorization header,
human-readable output, `--json`, TTL parsing, and the warning:

```text
Approval token is shown once and cannot be recovered.
```

- [ ] **Step 2: Run CLI tests**

Run:

```bash
go test ./cmd/cgctl -count=1
```

Expected: FAIL because approval subcommands are unknown.

- [ ] **Step 3: Add structured command parsing**

Support:

```text
cgctl approval issue --cluster UUID --engine mysql --kind switchover --target UUID --issued-by NAME --ttl 5m
cgctl approval list
cgctl approval show UUID
```

POST issuance reads `CG_CONTROL_TOKEN`. GET list/show does not print token hash.

- [ ] **Step 4: Add human output**

Issue output includes operation ID, grant ID, scope, expiry, and the plaintext
token on a separate line. List/show includes status and consumption metadata.

- [ ] **Step 5: Run CLI tests**

Run:

```bash
go test ./cmd/cgctl -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/cgctl
git commit -m "feat: add cgctl approval grant commands"
```

---

### Task 7: Web Console One-Time Approval Experience

**Files:**
- Modify: `internal/api/console.html`
- Modify: `internal/api/console_test.go`

**Interfaces:**
- Consumes: grant-authorized operation execute API.
- Produces: normal switchover UI without a long-lived control token.

- [ ] **Step 1: Write failing console contract tests**

Require:

- no `id="control-token"` in the console;
- one password input named `一次性审批令牌`;
- execution sends `approval_token` but no Authorization header;
- token input and state are cleared in `finally`;
- missing token opens the approval prompt instead of navigating to generic
  settings;
- consumed, expired, mismatched, and stale-plan errors have concise Chinese
  translations;
- automatic failover status contains no token control.

- [ ] **Step 2: Run console tests**

Run:

```bash
go test ./internal/api -run Console -count=1
```

Expected: FAIL on the old control-token contract.

- [ ] **Step 3: Update settings and operation UI**

Remove the normal control-token field. Keep a single in-memory approval input
with help text:

```text
由管理员生成，5 分钟内有效，执行一次后失效。
```

Do not add localStorage or sessionStorage.

- [ ] **Step 4: Update execution**

`controlOptions` becomes a JSON POST helper without Authorization. The execute
payload includes the one-time token. Clear both DOM value and
`state.approvalToken` after every attempt, then relock the switch.

- [ ] **Step 5: Run console tests**

Run:

```bash
go test ./internal/api -run Console -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/console.html internal/api/console_test.go
git commit -m "feat: use one-time approval in HA console"
```

---

### Task 8: Documentation, Full Verification, and Three-Node Acceptance

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`
- Modify: `docs/mysql-feature-parity-acceptance.md`
- Modify: deployment environment templates that still advertise
  `CG_APPROVAL_TOKEN`

**Interfaces:**
- Documents the final operator and automatic-recovery contracts.

- [ ] **Step 1: Update documentation**

Document:

- privileged grant issuance;
- single-use manual execution;
- grant expiry and retry behavior;
- automatic recovery internal authorization;
- deprecation of `CG_APPROVAL_TOKEN`;
- no plaintext grants in metadata or logs.

- [ ] **Step 2: Run source scans**

Run:

```bash
rg -n 'TokenApproval|ExpectedToken|automatic-approval' --glob '*.go'
rg -n 'CG_APPROVAL_TOKEN' .
```

Expected: no runtime use of static approval; only migration tests and
deprecation documentation may reference the old variable.

- [ ] **Step 3: Run all local tests and build**

Run:

```bash
gofmt -w pkg/model internal/approval internal/store internal/workflow internal/recovery internal/api internal/runtime internal/config cmd/cgctl
go test ./...
go build ./...
git diff --check
```

Expected: all commands pass.

- [ ] **Step 4: Deploy to 192.168.102.152-154**

Build `clusterguard`, `cgctl`, and `clusterguard-agent`; install the same
artifacts on all controllers; restart one node at a time; verify Raft quorum
and grant snapshot replication before proceeding.

- [ ] **Step 5: Manual switchover acceptance**

1. Issue a five-minute grant for one selected MySQL cluster and candidate.
2. Execute through the Web-console-equivalent API without `CG_CONTROL_TOKEN`.
3. Verify one writable primary, one VIP owner, healthy replicas, and an audit
   entry containing only the grant ID.
4. Reuse the same token and verify it is blocked as consumed.
5. Issue a new grant and switch back.

- [ ] **Step 6: Automatic failover acceptance**

1. Enable automatic failover for the selected test cluster.
2. Stop or isolate the current primary.
3. Wait for the configured stable incident window.
4. Verify the Leader creates an automatic operation with no human grant.
5. Verify fencing, promotion, VIP movement, replica repair, and audit/report
   records.
6. Restore the old primary and verify it remains read-only until the explicit
   rejoin workflow runs.

- [ ] **Step 7: Final regression**

Verify both retained MySQL environments, all three controllers, VIP uniqueness,
Leader changes, controller restart, and page refresh. Confirm a refreshed
browser never asks for `CG_CONTROL_TOKEN` during normal switch execution.

- [ ] **Step 8: Commit**

```bash
git add docs configs packaging scripts
git commit -m "docs: document one-time approval operations"
```
