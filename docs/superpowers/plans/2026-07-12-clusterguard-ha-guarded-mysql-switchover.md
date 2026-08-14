# ClusterGuard HA Guarded MySQL Switchover Implementation Plan

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/plans/2026-07-12-clusterguard-ha-guarded-mysql-switchover.md)
<!-- /LANGUAGE-SWITCH -->


> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a durable, idempotent, independently verified MySQL planned-switchover kernel while keeping default production execution unsupported until a real writer-endpoint provider is configured.

**Architecture:** Extend the common model and atomic repository with immutable operation records and plans. Resolve UUID-addressed topology into adapter context inside the control plane, run strict MySQL prechecks and typed execution steps behind the existing workflow gates, and inject an independent endpoint provider. The default provider is unsupported; deterministic tests inject a fake provider to exercise the complete role transition.

**Tech Stack:** Go 1.22 standard library, atomic JSON snapshot repository, `net/http`, MySQL CLI transport, SHA-256 plan digests, table-driven tests, race detector.

## Global Constraints

- The repository remains a clean-room ClusterGuard HA implementation with no Orchestrator dependency, API, metadata table, configuration, package, binary, or product naming.
- Public resources use immutable UUIDs; hostname, IP address, and port are mutable endpoints only.
- Workflow order is `DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`.
- MySQL failover, former-primary rejoin, node lifecycle execution, automatic recovery, and the real Linux VIP provider remain unsupported.
- PostgreSQL, Oracle, and SQL Server mutation behavior remains unsupported and fail-closed.
- The default runtime performs no database mutation because it has no executable writer-endpoint provider.
- Secrets and raw mutating SQL never appear in persisted operation, audit, or report records.

---

## File Structure

- `pkg/model/workflow.go`: durable operation, plan, step, attempt, and verification data contracts.
- `pkg/adapter/adapter.go`: resolved operation context and writer-endpoint provider contracts.
- `internal/store/operations.go`: operation creation, idempotency lookup, CAS transition, and immutable-plan persistence.
- `internal/store/operations_test.go`: durability, conflict, immutability, and race-oriented repository tests.
- `internal/workflow/resolver.go`: UUID inventory resolution and configured credential injection.
- `internal/workflow/progress.go`: stage/step persistence adapter used during execution.
- `internal/workflow/workflow.go`: gate orchestration and conservative terminal outcomes.
- `adapters/mysql/switchover.go`: strict precheck, immutable plan, execution, and verification.
- `adapters/mysql/dialect.go`: legacy and modern replication statement selection.
- `adapters/mysql/endpoint.go`: unsupported endpoint provider used by the default runtime.
- `adapters/mysql/switchover_test.go`: deterministic two-node transition and failure semantics.
- `internal/api/operations.go`: operation create/read/precheck/plan/execute/verify handlers.
- `internal/api/operations_test.go`: idempotency, restart lookup, and default fail-closed API tests.
- `internal/runtime/runtime.go`: resolver wiring and unsupported default endpoint provider.
- `docs/architecture.md`, `docs/operations.md`: capability and safety boundary documentation.

---

### Task 1: Durable Operation And Immutable Plan Model

**Files:**
- Modify: `pkg/model/workflow.go`
- Create: `internal/store/operations.go`
- Create: `internal/store/operations_test.go`
- Modify: `internal/store/repository.go`

**Interfaces:**
- Produces: `model.OperationRecord`, `model.PlanStep`, `model.StepAttempt`, `Repository.CreateOperation`, `Repository.PutOperationPlan`, `Repository.TransitionOperation`, `Repository.Operation`, and `Repository.OperationByIdempotencyKey`.

- [ ] **Step 1: Write failing repository tests**

Add tests that create an operation with an idempotency key, reopen the repository, and assert the same record is returned; reuse the key with another target and assert `store.ErrConflict`; update with a stale metadata revision and assert conflict; persist a plan and reject a second plan whose digest differs.

```go
created, reused, err := repository.CreateOperation(model.OperationRecord{
    Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
    TargetID: targetID, IdempotencyKey: "switch-20260712-1",
})
if err != nil || reused { t.Fatalf("create operation: reused=%t err=%v", reused, err) }
same, reused, err := repository.CreateOperation(created)
if err != nil || !reused || same.ResourceID != created.ResourceID { t.Fatalf("idempotent create failed") }
```

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/store -run 'TestOperation' -count=1`

Expected: FAIL because operation model and repository methods do not exist.

- [ ] **Step 3: Implement model and repository operations**

Add typed plan steps and operation records. Canonical plan fields are value types and cloned on every repository boundary. Validate UUIDs, non-empty idempotency keys, legal stage/status transitions, terminal immutability, monotonic step attempts, and plan digest immutability. Add `Operations` and `OperationKeys` maps to the snapshot and migrate missing maps during `Open`.

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

- [ ] **Step 4: Run store tests**

Run: `go test ./internal/store -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/workflow.go internal/store/repository.go internal/store/operations.go internal/store/operations_test.go
git commit -m "feat: persist idempotent operation records"
```

### Task 2: Resolved Context And Endpoint Provider Contracts

**Files:**
- Modify: `pkg/adapter/adapter.go`
- Create: `internal/workflow/resolver.go`
- Create: `internal/workflow/resolver_test.go`
- Create: `adapters/mysql/endpoint.go`
- Modify: `adapters/mysql/mysql.go`
- Modify: `pkg/adapter/registry_test.go`

**Interfaces:**
- Consumes: repository topology snapshots and immutable resource IDs.
- Produces: `adapter.ResolvedOperation`, `adapter.HAEndpointProvider`, `workflow.OperationResolver`, and `workflow.RepositoryResolver.Resolve`.

- [ ] **Step 1: Write failing resolution and capability tests**

Prove that the resolver rejects a target outside the cluster, duplicate primaries, no primary, a stale/missing snapshot, and a target endpoint supplied through arbitrary parameters. Prove the default MySQL adapter advertises precheck/plan but not execute/verify.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/workflow ./pkg/adapter ./adapters/mysql -run 'Test.*(Resolve|Capabilities)' -count=1`

Expected: FAIL because the contracts are absent.

- [ ] **Step 3: Implement contracts and resolver**

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

The resolver finds source and target only by UUID in the current snapshot, verifies engine and cluster ownership, and injects credentials through a server-side resolver. The unsupported provider returns a blocking check and never performs a side effect.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./internal/workflow ./pkg/adapter ./adapters/mysql -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/adapter/adapter.go internal/workflow/resolver.go internal/workflow/resolver_test.go adapters/mysql/endpoint.go adapters/mysql/mysql.go pkg/adapter/registry_test.go
git commit -m "feat: resolve UUID scoped operation context"
```

### Task 3: Strict MySQL Switchover Precheck And Plan

**Files:**
- Create: `adapters/mysql/switchover.go`
- Create: `adapters/mysql/switchover_test.go`
- Modify: `adapters/mysql/mysql.go`
- Modify: `pkg/model/workflow.go`

**Interfaces:**
- Consumes: `adapter.OperationRequest.Resolved` and `HAEndpointProvider.Precheck`.
- Produces: `Adapter.Precheck` and `Adapter.BuildPlan` for `OperationSwitchover`.

- [ ] **Step 1: Write table-driven failing precheck tests**

Cover healthy two-node GTID topology, wrong target, non-direct replica, unknown or nonzero lag, stopped thread, writable target, maintenance, missing GTID, errant/missing GTIDs, incompatible release family, incomplete probes, extra replica, stale observation, and unsupported endpoint provider.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./adapters/mysql -run 'TestSwitchover(Precheck|Plan)' -count=1`

Expected: FAIL because switchover remains unsupported.

- [ ] **Step 3: Implement strict checks and canonical plan digest**

Build ordered `PlanStep` values with source/target IDs and expected postconditions. Calculate SHA-256 over canonical JSON after clearing volatile resource metadata and the digest field itself. A plan with any failed check is returned as non-executable and cannot be persisted for execution.

```go
steps := []model.PlanStep{
    {Name: "fence_source", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true},
    {Name: "wait_target_gtid", Owner: "mysql", TargetID: resolved.Target.ResourceID},
    {Name: "promote_target", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true},
    {Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true},
    {Name: "verify_roles_and_endpoint", Owner: "platform", TargetID: resolved.Target.ResourceID},
}
```

- [ ] **Step 4: Run adapter tests**

Run: `go test ./adapters/mysql -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapters/mysql/switchover.go adapters/mysql/switchover_test.go adapters/mysql/mysql.go pkg/model/workflow.go
git commit -m "feat: plan guarded MySQL switchover"
```

### Task 4: MySQL Dialect, Execution, And Independent Verification

**Files:**
- Create: `adapters/mysql/dialect.go`
- Create: `adapters/mysql/dialect_test.go`
- Modify: `adapters/mysql/runner.go`
- Modify: `adapters/mysql/switchover.go`
- Modify: `adapters/mysql/switchover_test.go`

**Interfaces:**
- Produces: `SQLExecutor.Exec`, `mysqlDialect`, idempotent step postcondition probes, `Adapter.Execute`, and `Adapter.Verify`.

- [ ] **Step 1: Write failing dialect and execution tests**

Assert MySQL 5.7 uses `STOP SLAVE`/`RESET SLAVE ALL`, modern releases use `STOP REPLICA`/`RESET REPLICA ALL`, and all releases use explicit read-only transitions. Record every mutation in a fake SQL executor and assert exact ordering. Cover failure before fencing, after fencing, after promotion, retry with observed postconditions, cancellation after commit boundary, endpoint transfer failure, and duplicate endpoint owners.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./adapters/mysql -run 'Test(MySQLDialect|SwitchoverExecute|SwitchoverVerify)' -count=1`

Expected: FAIL because no mutation path exists.

- [ ] **Step 3: Implement minimal safe executor**

Use `SET GLOBAL super_read_only = ON`, `SET GLOBAL read_only = ON`, `WAIT_FOR_EXECUTED_GTID_SET`, version-selected stop/reset statements, and explicit target read-only transitions. After source fencing, do not abandon the safety sequence solely because the caller context was cancelled. Never log credentials or SQL. Return failure classes `pre_commit`, `fenced`, and `promoted_unverified`.

- [ ] **Step 4: Run adapter tests and race detector**

Run: `go test ./adapters/mysql -count=1`

Run: `go test -race ./adapters/mysql -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapters/mysql/dialect.go adapters/mysql/dialect_test.go adapters/mysql/runner.go adapters/mysql/switchover.go adapters/mysql/switchover_test.go
git commit -m "feat: execute and verify MySQL switchover kernel"
```

### Task 5: Durable Workflow Progress And Idempotent Retry

**Files:**
- Create: `internal/workflow/progress.go`
- Create: `internal/workflow/progress_test.go`
- Modify: `internal/workflow/workflow.go`
- Modify: `internal/workflow/workflow_test.go`

**Interfaces:**
- Consumes: operation repository, resolver, adapter and endpoint capabilities.
- Produces: stage CAS updates, step progress recording, terminal retry behavior, and conservative indeterminate outcomes.

- [ ] **Step 1: Write failing workflow tests**

Prove an operation is durable before precheck, every stage is persisted in order, a duplicate terminal request returns the terminal record without gates or mutation, a duplicate in-progress request cannot run concurrently, stale plans block before mutation, and persistence failure after a committed step returns indeterminate.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/workflow -run 'Test.*(Durable|Idempotent|Progress|StalePlan)' -count=1`

Expected: FAIL because the workflow does not persist operation state.

- [ ] **Step 3: Implement durable stage and step recording**

Create/reuse the record before discovery. Resolve UUID context server-side, persist the plan before safety evaluation, revalidate under lock, and pass a progress recorder to the adapter. Keep unsupported capability checks before safety, lock, approval, SQL, and endpoint effects.

- [ ] **Step 4: Run workflow tests and race detector**

Run: `go test ./internal/workflow -count=1`

Run: `go test -race ./internal/workflow -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/workflow/progress.go internal/workflow/progress_test.go internal/workflow/workflow.go internal/workflow/workflow_test.go
git commit -m "feat: persist guarded workflow progress"
```

### Task 6: Operation API And Default Fail-Closed Runtime

**Files:**
- Create: `internal/api/operations.go`
- Create: `internal/api/operations_test.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Modify: `internal/runtime/runtime.go`
- Modify: `cmd/cgctl/main.go`
- Modify: `cmd/cgctl/main_test.go`

**Interfaces:**
- Produces: `POST /api/v1/operations`, `GET /api/v1/operations/{id}`, stage action routes, and `cgctl operation` lookup.

- [ ] **Step 1: Write failing API tests**

Test authenticated create/read, missing idempotency key, duplicate reuse, conflicting reuse, UUID-only target validation, malformed input, restart lookup, and default execute returning HTTP 501 before lock or side effects.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/api ./cmd/cgctl -run 'Test.*Operation' -count=1`

Expected: FAIL because durable operation routes do not exist.

- [ ] **Step 3: Implement handlers and runtime wiring**

Keep secrets in the runtime credential resolver. The default MySQL adapter receives `UnsupportedHAEndpointProvider`, making execute capability false. Map repository conflicts to HTTP 409, validation failures to 400, unsupported execution to 501, and indeterminate commits to 500 with the operation record in the response.

- [ ] **Step 4: Run API, CLI, and runtime tests**

Run: `go test ./internal/api ./cmd/cgctl ./internal/runtime -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/operations.go internal/api/operations_test.go internal/api/server.go internal/api/server_test.go internal/runtime/runtime.go cmd/cgctl/main.go cmd/cgctl/main_test.go
git commit -m "feat: expose durable operation API"
```

### Task 7: Documentation And Full Verification

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`
- Modify: `README.md`

**Interfaces:**
- Documents the exact production-disabled boundary and next Linux VIP provider project.

- [ ] **Step 1: Update documentation**

Document operation IDs, idempotency keys, immutable plans, conservative terminal states, API examples, the default 501 behavior, and the explicit exclusion of failover/rejoin/node lifecycle/VIP execution.

- [ ] **Step 2: Run focused regression suites**

Run: `go test ./internal/store ./internal/workflow ./adapters/mysql ./internal/api ./cmd/cgctl -count=1`

Expected: PASS.

- [ ] **Step 3: Run full verification**

Run: `go test ./... -count=1`

Run: `go test -race ./... -count=1`

Run: `go build ./cmd/...`

Run: `go vet ./...`

Run: `git diff --check`

Run: `for f in configs/*.json; do jq empty "$f" || exit 1; done`

Run: `rg -n 'orchestrator-enterprise|orchctl|Orchestrator Enterprise|Enterprise HA|MySQL Control' --glob '!docs/superpowers/**' .`

Expected: every command passes and the clean-room scan returns no matches.

- [ ] **Step 4: Commit documentation**

```bash
git add README.md docs/architecture.md docs/operations.md
git commit -m "docs: describe guarded MySQL switchover"
```

- [ ] **Step 5: Review final diff and worktree**

Run: `git status --short --branch`

Run: `git log --oneline -8`

Expected: clean worktree on `codex/mysql-topology-intelligence` with the guarded-switchover commit series at HEAD.
