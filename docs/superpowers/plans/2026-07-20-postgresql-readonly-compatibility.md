# PostgreSQL Read-Only Compatibility Implementation Plan

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/plans/2026-07-20-postgresql-readonly-compatibility.md)
<!-- /LANGUAGE-SWITCH -->


> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver safe PostgreSQL registration, discovery, topology, health, lag, and candidate evaluation without enabling mutation.

**Architecture:** Add a native read-only PostgreSQL adapter backed by `psql`, persist `system_identifier` atomically with discovery, and run independent engine-filtered discovery schedulers. Reuse the common registry, resource store, API, console, monitoring, and workflow gates.

**Tech Stack:** Go 1.22, PostgreSQL `psql`, native streaming-replication views, existing JSON snapshot store and Raft replication.

## Global Constraints

- No Patroni or external HA framework dependency.
- No PostgreSQL mutation is advertised or executed in this phase.
- Hostname, IP, and port are never resource identity.
- Missing or contradictory identity and topology evidence fails closed.
- Tests are written and observed failing before production changes.

---

### Task 1: PostgreSQL Query Runner And Probe

**Files:**
- Create: `adapters/postgresql/runner.go`
- Create: `adapters/postgresql/runner_test.go`
- Create: `adapters/postgresql/probe.go`
- Create: `adapters/postgresql/probe_test.go`

**Interfaces:**
- Produces `SQLRunner.Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)`.
- Produces `discover(context.Context, SQLRunner, adapter.DiscoverRequest) (adapter.DiscoveryResult, error)`.

- [x] Write failing tests for JSON rows, secret-safe command construction, identity validation, primary health, standby health, paused replay, unknown lag, and malformed LSN evidence.
- [x] Run `go test ./adapters/postgresql -count=1` and confirm failures are caused by missing implementation.
- [x] Implement the minimum runner and probe needed by the tests.
- [x] Run `go test ./adapters/postgresql -count=1` and confirm green.

### Task 2: Adapter Capabilities, Topology, Candidates, And Metadata

**Files:**
- Modify: `adapters/postgresql/postgresql.go`
- Create: `adapters/postgresql/postgresql_test.go`
- Create: `adapters/postgresql/candidates.go`
- Create: `adapters/postgresql/candidates_test.go`
- Modify: `pkg/identity/identity.go`
- Modify: `pkg/identity/identity_test.go`

**Interfaces:**
- Produces `postgresql.New(SQLRunner) *Adapter`.
- Implements discover, topology, health, candidates, and metadata methods.
- All mutating methods continue returning `adapter.ErrUnsupported`.

- [x] Write failing capability, topology, ranking, identity, and unsupported-operation tests.
- [x] Run targeted tests and confirm red.
- [x] Implement topology and timeline-aware candidate assessment.
- [x] Run targeted tests and confirm green.

### Task 3: Atomic Cluster Identity Publication

**Files:**
- Modify: `internal/discovery/service.go`
- Modify: `internal/discovery/service_test.go`
- Modify: `internal/store/repository.go`
- Modify: `internal/store/replication_test.go`

**Interfaces:**
- Adds `DiscoveryRefresh.ClusterIdentity model.EngineIdentity`.
- First publication binds an empty PostgreSQL cluster identity.
- Mismatched identities reject the complete refresh.

- [x] Write failing tests for first bind, mixed-system refresh, persisted mismatch, and endpoint rename without duplicate instance.
- [x] Run targeted tests and confirm red.
- [x] Implement validation and atomic publication.
- [x] Run targeted tests and confirm green.

### Task 4: Configuration And Independent Scheduling

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/runtime/runtime_test.go`
- Modify: `configs/clusterguard.example.json`

**Interfaces:**
- Adds `File.PostgreSQL` with read-only discovery configuration.
- Credential resolver switches on cluster engine.
- Starts one filtered scheduler per enabled engine.

- [x] Write failing configuration, credential-routing, and scheduler-filter tests.
- [x] Run targeted tests and confirm red.
- [x] Implement configuration loading and runtime wiring.
- [x] Run targeted tests and confirm green.

### Task 5: Engine-Safe Monitoring And Console

**Files:**
- Modify: `internal/api/metrics.go`
- Modify: `internal/api/metrics_test.go`
- Modify: `internal/api/monitoring.go`
- Modify: `internal/api/monitoring_test.go`
- Modify: `internal/api/console.html`
- Modify: `internal/api/console_test.go`

**Interfaces:**
- Metric names receive the cluster engine.
- PostgreSQL lifecycle and mutation controls remain disabled.
- Native identity display is engine-aware.

- [x] Write failing Prometheus, Zabbix, and static console contract tests.
- [x] Run targeted tests and confirm red.
- [x] Implement engine-aware monitoring and console safeguards.
- [x] Run targeted tests and confirm green.

### Task 6: Documentation And Full Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`

- [x] Document the PostgreSQL grants, identity settings, configuration, and unsupported mutation boundary.
- [x] Run `gofmt` on changed Go files.
- [x] Run `go test ./... -count=1`.
- [x] Run `go test -race ./... -count=1`.
- [x] Run `go vet ./...`.
- [x] Validate every `configs/*.json` file with `jq empty`.
- [x] Run `go mod verify` and `git diff --check`.
