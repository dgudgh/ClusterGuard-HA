# PostgreSQL Enterprise HA Design

## Goal

Promote PostgreSQL from a read-only compatibility adapter to a first-class
ClusterGuard HA engine. The delivered path must cover native discovery,
candidate selection, planned switchover, guarded failover, former-primary
rejoin, replication repair, node synchronization, writer VIP movement,
verification, metrics, audit, reports, and console operations.

The implementation uses native PostgreSQL streaming replication. Patroni,
repmgr, and vendor APIs are optional future providers, not dependencies.

## Safety Invariants

- `resource_id` and `clusterguard.node_id` identify a node; hostname, IP, and
  port are mutable endpoints.
- `system_identifier` must match across every member of one cluster.
- A PostgreSQL promotion and its writer VIP transfer are one durable operation.
- Planned switchover must prove the old primary is stopped before promotion.
  `default_transaction_read_only` is not accepted as a hard fence.
- Failover requires stable failure evidence, controller quorum, a target lease,
  and verified old-primary isolation or an external fence.
- Unknown service state, incomplete VIP probe coverage, timeline mismatch,
  paused replay, unknown lag, or an unverified postcondition fails closed.
- An operation that crosses promotion but cannot complete verification is
  `indeterminate`, never successful.
- Agent commands are signed, plan-bound, short-lived, and allowlisted. There is
  no general shell command endpoint.

## Components

### PostgreSQL Adapter

The adapter owns PostgreSQL evidence and state transitions. It uses `psql` for
queries and low-risk SQL, and a restricted node controller for service-level
actions. It implements:

- health and native topology from recovery, WAL receiver, replay, timeline,
  and LSN evidence;
- deterministic candidate ranking by eligibility, replay LSN, and lag;
- switchover, failover, former-primary rejoin, and replication repair plans;
- PostgreSQL metrics with no synthetic zero values;
- verification of one writable primary, streaming standbys, timeline
  convergence, endpoint ownership, and old-primary isolation.

### Restricted PostgreSQL Node Controller

The controller-to-agent contract exposes only these operations:

- service status, stop, and start;
- promote a configured data directory;
- repoint a standby to a signed inventory source;
- rejoin with `pg_rewind`;
- rebuild with `pg_basebackup` when rewind is not safe or possible.

Each node policy fixes the engine, cluster UUID, instance UUID, service name,
operating-system user, data directory, binary directory, passfile, and allowed
port. Source host and port are included in the signed request and are derived
from the immutable operation plan. The agent validates them against the
cluster policy before executing a fixed argument vector.

### Writer Endpoint

The existing engine-neutral VIP provider is reused. It proves complete owner
coverage, acquires a quorum-backed transition lease, removes the old owner,
proves zero owners, adds the VIP to the target, and verifies one target owner.
The database adapter cannot bypass this provider.

## Planned Switchover

1. Revalidate inventory, identities, timeline, WAL receiver/replay, candidate
   lag, controller authority, and VIP uniqueness.
2. Acquire the endpoint transition authorization and operation lock.
3. Wait for the candidate to reach the sampled primary WAL LSN.
4. Stop the old primary through the restricted agent and verify it is stopped.
5. Recheck the candidate replay LSN, then promote it and verify it is writable.
6. Repoint reachable siblings and the old primary to the new primary. The old
   primary uses rewind/rebuild recovery rather than unsafe direct attachment.
7. Transfer the VIP to the new primary.
8. Verify one writable primary, one VIP owner, healthy standbys, and bounded
   lag; then audit and report.

## Guarded Failover

Failover is separate from planned switchover. It requires the stable failure
window, controller quorum, selected lowest-risk candidate, known data-loss
risk, and old-primary fencing. The agent path stops PostgreSQL and removes the
VIP when reachable. If the host is unreachable, an independently configured
external fencer must prove isolation before promotion.

## Former-Primary Recovery

Recovery first verifies that the target is not writable and owns no VIP. It
then attempts `pg_rewind --write-recovery-conf` against the current primary.
The node is started and must return in recovery, stream from the current
primary identity, and remain read-only. If rewind prerequisites are absent or
timelines cannot converge, the operation is blocked with a rebuild
recommendation; destructive base-backup replacement is only available through
the explicit node-sync workflow.

## Node Synchronization

The common lifecycle API becomes engine-aware. PostgreSQL data nodes select:

1. `pg_rewind` for an existing compatible former primary;
2. `pg_basebackup` for a new or divergent node.

The lifecycle plan records engine, source, target, method, service policy, and
postconditions. Metadata is committed only after native identity, recovery
state, upstream identity, and streaming health are verified. Controller-node
odd-membership rules remain unchanged.

## Console Experience

PostgreSQL clusters use the same cluster selector, topology, health, candidate,
operation, node lifecycle, metadata, metrics, audit, and report surfaces as
MySQL. Buttons are capability-driven. A configured PG execution path exposes
real operations; an incomplete node policy shows the exact blocking reason and
never produces simulated success.

## Verification

- Unit tests cover SQL parsing, plan integrity, state-machine ordering,
  idempotent progress, signed agent requests, command allowlists, fencing,
  rewind/rebuild selection, metrics, and failure semantics.
- Workflow integration tests prove every mutation passes safety, lock,
  approval, verify, audit, and report stages.
- Runtime/API/console tests prove credentials, capabilities, engine-specific
  lifecycle, and user-facing controls.
- Full Go tests, race tests, vet, builds, JSON validation, and diff checks gate
  delivery. Live destructive qualification remains environment-specific and
  is never inferred from unit tests.
