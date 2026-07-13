# ClusterGuard HA MySQL Feature Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the previous prototype's real MySQL HA, VIP, recovery, node lifecycle, monitoring, console, and installation capabilities as independent ClusterGuard HA implementations.

**Architecture:** The control plane owns UUID-scoped resources, durable workflows, quorum leases, safety gates, audit, and reports. The MySQL adapter owns dialect and replication mutations. A restricted `clusterguard-agent` owns privileged local VIP, role-persistence, and lifecycle actions; adapters never bypass the platform workflow.

**Tech Stack:** Go 1.22+, standard library HTTP/JSON/crypto/process packages, MySQL CLI protocol adapter, Linux `ip`/`arping`, systemd, shell-based installation stages, HTML/CSS/JavaScript console.

## Global Constraints

- Do not copy or depend on the previous product's source, APIs, metadata tables, packages, binaries, configuration keys, or names.
- Use immutable platform UUIDs and MySQL `server_uuid`; never use `hostname:port` as a durable key.
- Every real mutation must pass `DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`.
- Writer role and VIP ownership are coupled; success requires exactly one writable instance and exactly one VIP owner.
- Unknown quorum, fencing, topology, endpoint, or probe evidence fails closed.
- Secrets are server-resolved, purpose-specific, and absent from APIs, CLI arguments, durable state, audit, and reports.
- MySQL 5.7, 8.0, 8.4, and the lab's 9.x-compatible dialect must be tested.
- PostgreSQL, Oracle, and SQL Server adapter skeleton behavior must remain unchanged and unsupported actions must stay blocked.

---

### Task 1: Purpose-Specific MySQL Credentials

**Files:**
- Modify: `pkg/adapter/adapter.go`
- Modify: `internal/config/config.go`
- Modify: `internal/runtime/runtime.go`
- Modify: `configs/clusterguard.example.json`
- Modify: `packaging/systemd/clusterguard.env.example`
- Test: `internal/config/config_test.go`
- Test: `internal/runtime/runtime_test.go`

**Interfaces:**
- Produces: `adapter.MySQLCredentials{Discovery, Operation, Replication Credentials}`.
- Produces: `config.MySQLConfig` references three independent environment secrets.
- Consumes: existing server-side environment resolution and redaction behavior.

- [ ] **Step 1: Write failing tests for credential separation**

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

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/config ./internal/runtime -run 'Credential|Credentials' -count=1`
Expected: FAIL because `CredentialRef` and independent runtime credentials do not exist.

- [ ] **Step 3: Implement the credential contract**

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

Resolve each password once during startup, reject missing required values, pass
discovery credentials only to refresh, and operation/replication credentials
only through `ResolvedOperation`.

- [ ] **Step 4: Verify GREEN and regressions**

Run: `go test ./internal/config ./internal/runtime ./pkg/adapter -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/adapter internal/config internal/runtime configs packaging/systemd
git commit -m "feat: separate MySQL control credentials"
```

### Task 2: HA Endpoint Inventory and API

**Files:**
- Modify: `pkg/model/model.go`
- Modify: `internal/store/repository.go`
- Create: `internal/store/ha_endpoints.go`
- Modify: `internal/api/clusters.go`
- Modify: `internal/api/server.go`
- Test: `internal/store/ha_endpoints_test.go`
- Test: `internal/api/clusters_test.go`

**Interfaces:**
- Produces: `Repository.PutHAEndpoint`, `Repository.HAEndpoint`, `Repository.HAEndpoints`.
- Produces: `GET/POST /api/v1/clusters/{id}/ha-endpoints`.
- Consumes: registered cluster and instance UUID inventory.

- [ ] **Step 1: Write failing uniqueness and API tests**

```go
func TestPutHAEndpointAllowsOneActiveVIPPerCluster(t *testing.T) {}
func TestPutHAEndpointRejectsVIPSharedByTwoClusters(t *testing.T) {}
func TestHAEndpointRejectsUnknownOwner(t *testing.T) {}
func TestClusterHAEndpointAPIRejectsSecretAndUnknownFields(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/store ./internal/api -run HAEndpoint -count=1`
Expected: FAIL because HA endpoint persistence and routes do not exist.

- [ ] **Step 3: Implement durable endpoint resources**

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

Persist the interface and prefix as explicit HA endpoint properties, validate
IPv4 address and prefix, require an inventory owner, prohibit duplicate active
VIP addresses across clusters, and preserve resource revisions.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/store ./internal/api -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model internal/store internal/api
git commit -m "feat: add durable HA endpoint inventory"
```

### Task 3: Restricted ClusterGuard Agent

**Files:**
- Create: `cmd/clusterguard-agent/main.go`
- Create: `internal/agent/config.go`
- Create: `internal/agent/protocol.go`
- Create: `internal/agent/vip.go`
- Create: `internal/agent/role.go`
- Create: `internal/agent/command.go`
- Create: `internal/agent/protocol_test.go`
- Create: `internal/agent/vip_test.go`
- Create: `packaging/systemd/clusterguard-agent.service`
- Create: `configs/clusterguard-agent.example.json`

**Interfaces:**
- Produces: newline-delimited JSON request/response contract on stdin/stdout.
- Produces commands `vip_status`, `vip_acquire`, `vip_release`, `self_isolate`, and `persist_role`.
- Consumes signed operation context and an allowlisted cluster/VIP configuration.

- [ ] **Step 1: Write failing protocol and command validation tests**

```go
func TestAgentRejectsUnknownCommand(t *testing.T) {}
func TestAgentRejectsVIPOutsideAllowlist(t *testing.T) {}
func TestAgentAcquireIsIdempotentAndSendsGARP(t *testing.T) {}
func TestAgentSelfIsolationRemovesVIPBeforeReadOnly(t *testing.T) {}
func TestAgentRedactsSecretsFromErrors(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/agent ./cmd/clusterguard-agent -count=1`
Expected: FAIL because the agent packages do not exist.

- [ ] **Step 3: Implement a narrow command executor**

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

Use absolute command paths, validate every argument, capture bounded output,
reject environment overrides, and provide interfaces for test command fakes.

- [ ] **Step 4: Verify GREEN, build, and vet**

Run: `go test ./internal/agent ./cmd/clusterguard-agent -count=1 && go build ./cmd/clusterguard-agent && go vet ./internal/agent/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/clusterguard-agent internal/agent packaging/systemd/clusterguard-agent.service configs/clusterguard-agent.example.json
git commit -m "feat: add restricted data node agent"
```

### Task 4: Real Linux VIP Provider

**Files:**
- Create: `internal/endpoint/transport.go`
- Create: `internal/endpoint/agent_transport.go`
- Create: `internal/endpoint/linux_vip.go`
- Create: `internal/endpoint/lease.go`
- Create: `internal/endpoint/linux_vip_test.go`
- Create: `internal/endpoint/lease_test.go`
- Modify: `pkg/adapter/adapter.go`
- Modify: `internal/runtime/runtime.go`

**Interfaces:**
- Produces: `endpoint.LinuxVIPProvider` implementing `adapter.HAEndpointProvider`.
- Produces: cluster-wide `Owners`, `Transfer`, and `Verify` evidence.
- Consumes: HA endpoint inventory, agent transport, and active endpoint lease.

- [ ] **Step 1: Write failing provider tests**

```go
func TestVIPPrecheckFailsOnUnknownProbeCoverage(t *testing.T) {}
func TestVIPPrecheckFailsWithTwoOwners(t *testing.T) {}
func TestVIPTransferReleasesEveryNonTargetBeforeAcquire(t *testing.T) {}
func TestVIPTransferDoesNotAcquireWithoutActiveLease(t *testing.T) {}
func TestVIPVerifyRequiresExactlyOneTargetOwner(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/endpoint -count=1`
Expected: FAIL because the endpoint provider does not exist.

- [ ] **Step 3: Implement fail-closed ownership transfer**

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

Probe all inventory instances concurrently with bounded timeouts. Refuse an
acquire when any result is unknown. Release non-target owners, prove zero
owners, acquire target, send GARP, and prove target-only ownership.

- [ ] **Step 4: Verify GREEN and adapter integration**

Run: `go test ./internal/endpoint ./internal/runtime ./adapters/mysql -count=1`
Expected: PASS and MySQL execute capability becomes available only when the
provider and mutating credentials are configured.

- [ ] **Step 5: Commit**

```bash
git add internal/endpoint internal/runtime pkg/adapter
git commit -m "feat: provide verified Linux VIP ownership"
```

### Task 5: Three-Node Planned Switchover

**Files:**
- Modify: `adapters/mysql/switchover.go`
- Create: `adapters/mysql/reparent.go`
- Create: `adapters/mysql/role_persistence.go`
- Modify: `pkg/adapter/adapter.go`
- Modify: `internal/workflow/resolver.go`
- Test: `adapters/mysql/switchover_test.go`
- Test: `adapters/mysql/switchover_execution_test.go`
- Create: `adapters/mysql/reparent_test.go`

**Interfaces:**
- Produces: a plan containing every follower UUID and reparent postcondition.
- Produces: `ReplicaReparenter` with version-aware GTID source syntax.
- Consumes: operation and replication credentials plus real endpoint provider.

- [ ] **Step 1: Replace the two-node expectation with failing three-node tests**

```go
func TestSwitchoverPrecheckAcceptsHealthyThreeNodeTopology(t *testing.T) {}
func TestSwitchoverPlanIncludesFormerPrimaryAndSiblingReparent(t *testing.T) {}
func TestSwitchoverBlocksSiblingWithErrantGTID(t *testing.T) {}
func TestSwitchoverExecutesReparentBeforeVIPTransfer(t *testing.T) {}
func TestSwitchoverVerifyRequiresEveryFollowerOnNewPrimary(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./adapters/mysql -run 'ThreeNode|Sibling|Reparent' -count=1`
Expected: FAIL on the existing exactly-two-member scope check.

- [ ] **Step 3: Implement immutable follower planning and reparenting**

```go
type PlannedFollower struct {
	InstanceID model.ResourceID `json:"instance_id"`
	ServerUUID string `json:"server_uuid"`
	Endpoint adapter.Endpoint `json:"endpoint"`
	MetadataRevision uint64 `json:"metadata_revision"`
}
```

Include all followers in the plan digest. Fence source, capture GTID, wait target,
promote target, attach former source and siblings with GTID auto-position, verify
threads/source UUID, then transfer VIP. Persist role state through the agent.

- [ ] **Step 4: Verify GREEN and race behavior**

Run: `go test -race ./adapters/mysql ./internal/workflow -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapters/mysql pkg/adapter internal/workflow
git commit -m "feat: execute three node MySQL switchover"
```

### Task 6: Former-Primary Rejoin and Low-Risk Repair

**Files:**
- Create: `adapters/mysql/repair.go`
- Create: `adapters/mysql/rejoin.go`
- Create: `adapters/mysql/repair_test.go`
- Create: `adapters/mysql/rejoin_test.go`
- Modify: `pkg/model/workflow.go`
- Modify: `internal/api/operations.go`
- Modify: `internal/api/server.go`

**Interfaces:**
- Produces operation kinds `former_primary_rejoin` and `replication_repair`.
- Produces allowlisted repair actions `refresh`, `collect_replication_status`, `start_io_thread`, `start_sql_thread`, `begin_maintenance`, `end_maintenance`.
- Consumes current topology, native identities, GTID relation, and operation gates.

- [ ] **Step 1: Write failing repair safety tests**

```go
func TestFormerPrimaryFastRejoinRequiresSubsetGTID(t *testing.T) {}
func TestFormerPrimaryWithErrantGTIDRequiresRebuild(t *testing.T) {}
func TestFormerPrimaryRejoinRefusesLocalVIP(t *testing.T) {}
func TestRepairRejectsSkipTransactionAndResetReplica(t *testing.T) {}
func TestRepairStartsOnlyRequestedStoppedThread(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./adapters/mysql ./internal/api -run 'FormerPrimary|Repair' -count=1`
Expected: FAIL because these operation kinds are unsupported.

- [ ] **Step 3: Implement the repair allowlist and rejoin plan**

Fast rejoin makes the former primary read-only, proves no VIP, configures the
current primary as GTID source, starts threads, and verifies source UUID and lag.
Divergent GTID returns a structured rebuild recommendation without mutation.

- [ ] **Step 4: Verify GREEN**

Run: `go test -race ./adapters/mysql ./internal/api ./internal/workflow -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapters/mysql pkg/model internal/api
git commit -m "feat: repair and rejoin MySQL replicas"
```

### Task 7: Quorum Lease, Failover, and Self-Isolation

**Files:**
- Create: `internal/coordination/membership.go`
- Create: `internal/coordination/lease.go`
- Create: `internal/coordination/membership_test.go`
- Create: `internal/coordination/lease_test.go`
- Create: `adapters/mysql/failover.go`
- Create: `adapters/mysql/failover_test.go`
- Modify: `internal/workflow/gates.go`
- Modify: `internal/runtime/runtime.go`

**Interfaces:**
- Produces: leader-only, quorum-backed cluster and endpoint leases.
- Produces: 30-second stable failure observation policy.
- Consumes: fencing provider, candidate assessment, endpoint provider, agent self-isolation.

- [ ] **Step 1: Write failing quorum and failover tests**

```go
func TestLeaseRejectedWithoutControllerMajority(t *testing.T) {}
func TestConflictingEndpointLeaseIsRejected(t *testing.T) {}
func TestMinorityAgentSelfIsolates(t *testing.T) {}
func TestFailoverWaitsForSixStableChecks(t *testing.T) {}
func TestFailoverBlocksUnfencedOldPrimary(t *testing.T) {}
func TestFailoverPromotesLowestDataLossCandidate(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/coordination ./adapters/mysql -run 'Lease|Failover|Minority' -count=1`
Expected: FAIL because coordination and failover do not exist.

- [ ] **Step 3: Implement durable coordination and failover plan**

Use an odd controller membership, monotonic lease term/index, one active lease
per cluster/VIP, a 30-second TTL, and leader-only grants. Failover requires
fencing evidence and uses the common execute/verify/audit/report workflow.

- [ ] **Step 4: Verify GREEN including restart recovery**

Run: `go test -race ./internal/coordination ./adapters/mysql ./internal/workflow -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/coordination adapters/mysql/failover.go adapters/mysql/failover_test.go internal/workflow internal/runtime
git commit -m "feat: coordinate guarded MySQL failover"
```

### Task 8: Node Synchronization and Lifecycle

**Files:**
- Create: `internal/lifecycle/model.go`
- Create: `internal/lifecycle/planner.go`
- Create: `internal/lifecycle/executor.go`
- Create: `internal/lifecycle/planner_test.go`
- Create: `internal/lifecycle/executor_test.go`
- Create: `adapters/mysql/node_sync.go`
- Create: `adapters/mysql/node_sync_test.go`
- Create: `scripts/clusterguard-node-install.sh`
- Create: `scripts/clusterguard-mysql-sync.sh`
- Modify: `internal/api/server.go`

**Interfaces:**
- Produces node plans for data, controller, mixed, and former-node rebuild roles.
- Produces sync selection `clone`, `xtrabackup`, or `logical_dump`.
- Consumes install credentials only through executor environment and redacted task state.

- [ ] **Step 1: Write failing lifecycle policy tests**

```go
func TestDataNodeCountIsUnlimited(t *testing.T) {}
func TestControllerFinalMembershipMustBeOdd(t *testing.T) {}
func TestFormerNodeRebuildReusesResourceSlot(t *testing.T) {}
func TestMySQL57SkipsClone(t *testing.T) {}
func TestMetadataCommitsOnlyAfterReplicationVerification(t *testing.T) {}
func TestLifecycleTaskNeverPersistsSecrets(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/lifecycle ./adapters/mysql -run 'Node|Membership|Sync|Rebuild' -count=1`
Expected: FAIL because lifecycle is unsupported.

- [ ] **Step 3: Implement staged, resumable lifecycle tasks**

Each stage records start/end/status/bounded output. Retries replace ephemeral
credentials, recheck postconditions, and resume from the first incomplete stage.
Metadata commit is the final step after healthy replication verification.

- [ ] **Step 4: Verify GREEN and shell tests**

Run: `go test -race ./internal/lifecycle ./adapters/mysql ./internal/api -count=1 && bash -n scripts/clusterguard-node-install.sh scripts/clusterguard-mysql-sync.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lifecycle adapters/mysql/node_sync.go adapters/mysql/node_sync_test.go scripts internal/api/server.go
git commit -m "feat: manage MySQL node lifecycle"
```

### Task 9: Monitoring, Audit Timeline, and Reports

**Files:**
- Modify: `adapters/mysql/metrics.go`
- Modify: `internal/metrics/service.go`
- Create: `internal/metrics/alerts.go`
- Create: `internal/metrics/alerts_test.go`
- Modify: `internal/store/operations.go`
- Modify: `internal/api/metrics.go`
- Modify: `internal/api/operations.go`
- Create: `internal/report/html.go`
- Create: `internal/report/html_test.go`

**Interfaces:**
- Produces monitoring-safe JSON, Prometheus text, alerts, operation timeline, JSON and HTML reports.
- Consumes durable operation, audit, verification, metric sample, and report records.

- [ ] **Step 1: Write failing output tests**

```go
func TestMonitoringMetricsContainStableUUIDLabels(t *testing.T) {}
func TestAlertsClassifyStoppedThreadLagAndVIPMismatch(t *testing.T) {}
func TestOperationTimelineShowsSourceTargetAndCollapsedEvidence(t *testing.T) {}
func TestHTMLReportEscapesEvidenceAndIncludesVerification(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/metrics ./internal/api ./internal/report -count=1`
Expected: FAIL because alert/report surfaces are incomplete.

- [ ] **Step 3: Implement outputs without third-party dependencies**

Add QPS, TPS, connection, running-thread, slow-query, buffer-hit, temp-table,
row-lock, lag, replication-thread, VIP-owner, and operation-state metrics. Keep
raw evidence bounded and collapsed by default in HTML.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/metrics ./internal/api ./internal/report -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapters/mysql/metrics.go internal/metrics internal/store/operations.go internal/api internal/report
git commit -m "feat: expose MySQL operations observability"
```

### Task 10: MySQL Operations Console

**Files:**
- Modify: `internal/api/console.html`
- Modify: `internal/api/console.go`
- Modify: `internal/api/console_test.go`

**Interfaces:**
- Produces overview, topology, operations, nodes, metrics, operation log, about, and settings views.
- Consumes only `/api/v1` resources and capability flags.

- [ ] **Step 1: Write failing DOM contract tests**

```go
func TestConsoleOverviewDoesNotRenderTopologyGraph(t *testing.T) {}
func TestConsoleOperationsHasOneControlledSwitchButton(t *testing.T) {}
func TestConsoleTopologyHasMetadataModalAndAllNodeIdentityFields(t *testing.T) {}
func TestConsoleNodeActionsAreCapabilityDriven(t *testing.T) {}
func TestConsoleOperationLogCollapsesRawEvidence(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/api -run Console -count=1`
Expected: FAIL against the current compact read-only console.

- [ ] **Step 3: Implement the complete console**

Use one restrained layout system with 4-8px radii, stable grid tracks, Chinese
default copy, English toggle, compact node cards, aligned topology connectors,
centered command buttons, and explicit disabled reasons. Do not add fake action
handlers.

- [ ] **Step 4: Verify GREEN and browser screenshots**

Run: `go test ./internal/api -run Console -count=1`
Expected: PASS. Then use browser automation at desktop and mobile widths and
confirm no overlap, blank canvas, unreachable button, or layout shift.

- [ ] **Step 5: Commit**

```bash
git add internal/api/console.html internal/api/console.go internal/api/console_test.go
git commit -m "feat: deliver MySQL HA operations console"
```

### Task 11: Installation, Three-Host Deployment, and Destructive Matrix

**Files:**
- Create: `scripts/clusterguard-install.sh`
- Create: `scripts/clusterguard-preflight.sh`
- Create: `scripts/clusterguard-smoke.sh`
- Create: `scripts/clusterguard-ha-matrix.sh`
- Create: `packaging/logrotate/clusterguard-ha`
- Modify: `packaging/systemd/clusterguard-ha.service`
- Modify: `README.md`
- Modify: `docs/operations.md`
- Create: `docs/mysql-runbook.md`
- Create: `scripts/scripts_test.go`

**Interfaces:**
- Produces one bundle installer for controller, data-node agent, or mixed role.
- Produces repeatable acceptance matrix and rollback records.
- Consumes built binaries, configuration templates, inventory, and lab SSH access.

- [ ] **Step 1: Write failing installer and smoke contract tests**

```go
func TestInstallerDefaultsToPreflightAndRequiresExplicitExecute(t *testing.T) {}
func TestInstallerWritesSecretsOnlyToMode0640EnvironmentFile(t *testing.T) {}
func TestInstallerEnablesAgentReconcileOnEveryDataNode(t *testing.T) {}
func TestSmokeChecksOneWriterOneVIPAndAllReplicaThreads(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./scripts -count=1`
Expected: FAIL because delivery scripts do not exist.

- [ ] **Step 3: Implement delivery tooling**

Build both binaries, install systemd units, render role-specific config, preserve
MySQL data by default, create restricted identities, verify paths/ports/time
sync, start services, register clusters, and run smoke checks.

- [ ] **Step 4: Run full local verification**

Run:

```bash
gofmt -w $(rg --files -g '*.go')
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/clusterguard ./cmd/cgctl ./cmd/clusterguard-agent
git diff --check
rg -n 'orchestrator|orchctl|orchestrator-client|Orchestrator Enterprise|Enterprise HA|MySQL Control' --glob '!docs/superpowers/**' .
```

Expected: all commands pass and the clean-room name scan has no matches outside
historical design records.

- [ ] **Step 5: Deploy to `192.168.102.152-154`**

Install an odd controller set and agents, register all six lab clusters and one
VIP per cluster, refresh topology, and verify every MySQL instance by native
UUID before enabling mutations.

- [ ] **Step 6: Run destructive acceptance**

Execute:

- 20 round-robin planned switches across three nodes;
- 30 random switches across different clusters while distributing primaries;
- simultaneous switches in two clusters with distinct VIPs;
- primary restart, recovered-old-primary restart, and new-primary restart loops;
- network isolation with stale old-primary VIP and minority self-isolation;
- former-primary fast rejoin and divergent-node rebuild;
- hostname, IP alias, and port metadata reconciliation;
- MySQL 5.7, 8.0, 8.4, and 9.x-compatible syntax paths.

Each case must prove one writable primary, one VIP owner, healthy follower
threads, bounded lag, complete operation timeline, and report output.

- [ ] **Step 7: Commit final delivery assets**

```bash
git add scripts packaging README.md docs
git commit -m "feat: deliver ClusterGuard MySQL HA platform"
```

## Self-Review

- Every parity requirement in the design maps to at least one task.
- The plan has no simulated execution or compatibility layer.
- Credential, endpoint, operation, and resource types have one consistent name.
- Node lifecycle is separated from topology mutation but uses the same gates.
- Monitoring and console tasks consume real durable APIs and cannot create an
  action that the backend does not support.
- The final lab matrix verifies state, not only command exit codes.
