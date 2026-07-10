# ClusterGuard HA Phase 1 Implementation Plan

> **For agentic workers:** Follow test-driven development for every behavior. Each completed step must have a failing test observed before its implementation.

**Goal:** Deliver the independent ClusterGuard HA Phase 1 control kernel.

**Architecture:** Use Go standard library packages, a revisioned JSON snapshot
repository, public resource and adapter contracts, a guarded workflow service,
and a small `net/http` API/console. Keep engine calls behind adapters and keep
all platform state independent of database network coordinates.

**Tech Stack:** Go 1.19+, standard library `net/http`, `encoding/json`,
`crypto/rand`, `os/exec`, and embedded static console assets.

## Global Constraints

- Use platform UUIDs as primary identities.
- Treat hostname, IP, and port as mutable endpoint data.
- Register MySQL, PostgreSQL, Oracle, and SQL Server.
- Only MySQL read-only discovery and health are implemented in Phase 1.
- Guard every mutation with safety, lock, approval, audit, and verification.
- Return `unsupported` for any unavailable execution capability.

### Task 1: Platform Models and Engine Identity

**Files:** `pkg/model/*`, `pkg/identity/*`

- [x] Write model and identity tests for UUIDs and all native engine keys.
- [x] Run package tests and observe compile failure.
- [x] Implement resource, workflow, and identity model types.
- [x] Run package tests and commit the green model layer.

### Task 2: Revisioned Metadata Repository

**Files:** `internal/store/*`

- [x] Write reconciliation tests for hostname, IP, and port changes.
- [x] Run tests and observe missing repository behavior.
- [x] Implement in-memory state plus atomic JSON snapshot persistence.
- [x] Verify aliases, revisions, duplicate detection, and reload behavior.

### Task 3: Adapter SDK and Registry

**Files:** `pkg/adapter/*`, `adapters/*`

- [x] Write registry and unsupported-capability tests.
- [x] Implement the adapter contract and registry.
- [x] Implement skeleton adapters and MySQL read-only discovery/health.
- [x] Verify credentials stay in process environment rather than arguments.

### Task 4: Guarded Workflow

**Files:** `internal/workflow/*`

- [x] Write workflow tests for gate order, fail-closed unsupported execution,
  verification, audit, and report creation.
- [x] Implement lock, approval, safety, execution, and report services.
- [x] Verify every supported mutation reaches audit and verification.

### Task 5: API and Console

**Files:** `internal/api/*`, `web/*`, `cmd/*`

- [x] Write endpoint tests for engines, clusters, health, workflows, and
  metadata reconciliation.
- [x] Implement the `/api/v1` routes and JSON error contract.
- [x] Add a compact operational console backed only by these routes.
- [x] Verify browser assets and endpoint tests.

### Task 6: Delivery Verification

**Files:** `configs/*`, `docs/*`, `scripts/*`

- [x] Add safe default configuration and operator documentation.
- [x] Run full Go tests, build both binaries, format checks, and clean-room scan.
- [x] Start the local service and exercise the read-only API.
- [x] Commit the Phase 1 baseline.
