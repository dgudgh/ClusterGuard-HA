# PostgreSQL Enterprise HA Implementation Plan

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/plans/2026-07-20-postgresql-enterprise-ha.md)
<!-- /LANGUAGE-SWITCH -->


> **For agentic workers:** Follow TDD. Observe each focused test fail before
> adding production behavior, then run the complete gate before delivery.

**Goal:** Deliver PostgreSQL as an executable, fail-closed ClusterGuard HA
engine with parity across HA operations, lifecycle, monitoring, and console.

**Architecture:** Extend the native PostgreSQL adapter with a restricted signed
node-controller provider. Reuse the common workflow, consensus authority,
operation lock, one-time approval, VIP lease, audit, report, and resource store.

## Task 1: Execution Contracts And Agent Policy

- [x] Add PostgreSQL SQL execution without exposing credentials in arguments.
- [x] Add a PostgreSQL node-controller interface and unsupported provider.
- [x] Extend signed agent requests with engine and inventory-derived source.
- [x] Add strict PostgreSQL node policy validation and fixed commands.
- [x] Test signature binding, expiry, allowlist checks, and command ordering.

## Task 2: Planned Switchover

- [x] Test candidate, identity, timeline, WAL, lag, endpoint, and controller
      prechecks.
- [x] Build a deterministic immutable plan with hard-stop-before-promote.
- [x] Execute idempotent stop, promotion, sibling reparent, VIP transfer steps.
- [x] Verify one writer, one VIP owner, and healthy streaming followers.

## Task 3: Failover And Former-Primary Recovery

- [x] Reuse stable failure, quorum lease, and external-fence safety evidence.
- [x] Add engine-aware agent self-isolation for PostgreSQL.
- [x] Promote only the lowest-risk candidate after isolation is proven.
- [x] Implement rewind-based former-primary rejoin and fail closed to rebuild.
- [x] Test indeterminate states after the promotion commit point.

## Task 4: Repair, Metrics, And Node Synchronization

- [x] Add read-only status collection and low-risk receiver/replay repair.
- [x] Add truthful PostgreSQL connection, transaction, WAL, checkpoint,
      conflict, and replication metrics.
- [x] Generalize lifecycle plans and secrets by engine.
- [x] Add PostgreSQL rewind/base-backup synchronization and verification.

## Task 5: Runtime, API, Console, And Documentation

- [x] Add separate operation and replication credentials for PostgreSQL.
- [x] Wire node controller, failover safety, endpoint provider, and lifecycle.
- [x] Expose accurate capabilities and engine-specific blocking reasons.
- [x] Enable PG operation and lifecycle controls in the console only when real.
- [x] Document grants, node policy, install, recovery, and production fencing.

## Task 6: Automated Delivery Gate

- [x] Run targeted PostgreSQL, agent, workflow, lifecycle, runtime, and API tests.
- [x] Run `gofmt` on changed Go files.
- [x] Run `go test ./... -count=1`.
- [x] Run `go test -race ./... -count=1`.
- [x] Run `go vet ./...`.
- [x] Build `clusterguard`, `cgctl`, and `clusterguard-agent`.
- [x] Validate `configs/*.json`, `go mod verify`, and `git diff --check`.

## Task 7: Three-Node Live Qualification

- [ ] Build and install one versioned, checksummed ClusterGuard bundle on all
      three controller nodes.
- [ ] Install the exact production PostgreSQL package and create one primary
      plus two streaming standbys from the console lifecycle flow.
- [ ] Prove planned switchover, failover, VIP transfer, and former-primary
      rejoin with data-integrity checks after every transition.
- [ ] Prove fail-closed behavior for whole-host network isolation, quorum loss,
      unknown fencing state, duplicate VIP ownership, and stale Leader routing.
- [ ] Repeat controller, database, and host restarts and confirm one writer,
      one VIP owner, stable replication, durable audit, and durable reports.
- [ ] Archive the exact package versions, topology, timing, operation reports,
      and acceptance evidence before enabling PostgreSQL mutation in production.
