# ClusterGuard HA Guarded MySQL Switchover Design

## Objective

Implement the first mutating MySQL operation in the independent ClusterGuard
HA control plane: a guarded planned switchover kernel. The increment must make
operation state durable, plans immutable, retries idempotent, and terminal
outcomes auditable. It must not expose a production role change until a real
writer-endpoint provider is configured.

Failure recovery, former-primary rejoin, node lifecycle execution, automatic
recovery, and the Linux VIP provider remain separate later projects and return
explicit `unsupported` results.

## Selected Delivery Boundary

The increment implements MySQL planned switchover behind capability-oriented
interfaces. Tests use a deterministic fake writer-endpoint provider to prove
the complete role-change sequence. The default server has no mutating endpoint
provider, so it advertises read-only precheck and planning while rejecting
execution before safety gates or database side effects.

This boundary prevents a state in which ClusterGuard HA changes the writable
primary but leaves an existing VIP or listener on the former primary.

## Components

### Durable Operation Registry

The platform repository stores an `OperationRecord` keyed by immutable resource
UUID. A record contains:

- operation and target resource IDs;
- caller-supplied idempotency key;
- current workflow stage and terminal status;
- topology observation token;
- immutable operation plan and digest;
- metadata revisions for every resource referenced by the plan;
- attempt count, timestamps, failure classification, and message;
- execution and verification summaries.

Creating the same operation with the same idempotency key returns the existing
record. Reusing the key with a different cluster, engine, kind, or target is a
conflict. Updates use metadata revision compare-and-swap and are persisted by
the repository's atomic snapshot protocol.

### Immutable Plan

The plan is built only from a current topology snapshot. It records the source
primary, selected target, observation time, source/target metadata revisions,
ordered typed steps, and a SHA-256 digest over canonical plan data. Execute
rejects a missing plan, changed digest, changed observation token, changed
resource revision, or different target.

The planned steps are:

1. revalidate topology and inventory membership;
2. confirm endpoint provider readiness;
3. fence writes on the current primary;
4. capture the primary GTID position;
5. wait for the selected target to execute that position;
6. stop target replication;
7. promote the target as writable;
8. transfer the writer endpoint through the provider;
9. keep the former primary read-only;
10. verify exactly one writable primary and one endpoint owner.

Redirecting additional replicas and rejoining the former primary are not hidden
inside this increment. The plan blocks execution when the observed topology has
additional replicas because completing that topology safely belongs to the
former-primary/reparent project.

### MySQL Operation Context

Public requests contain resource UUIDs only. A platform resolver loads the
selected cluster, current topology snapshot, current primary, selected target,
and configured MySQL credentials. The adapter receives this resolved context;
hostnames and ports are treated only as mutable connection endpoints.

The MySQL adapter never accepts primary or target endpoints from arbitrary API
parameters and never controls an instance outside the persisted cluster
inventory.

### Writer Endpoint Provider

`HAEndpointProvider` is independent of the database adapter and provides:

- capability and readiness inspection;
- plan validation;
- endpoint transfer;
- unique-owner verification.

The default unsupported provider has no side effects. A fake provider exists
only in tests. A real Linux VIP provider, lease, quorum, fencing token, and
network-partition behavior remain the next delivery project.

## MySQL Preconditions

A planned switchover passes only when all of the following have current probe
evidence:

- exactly one writable primary exists in the selected cluster;
- source and target are valid inventory UUIDs in that cluster;
- the target is a reachable direct replica of the current primary;
- source and target use GTID mode `ON`;
- source and target have binary logging enabled;
- the target is read-only, promotion eligible, and not in maintenance;
- replication IO and SQL threads are running;
- observed lag is exactly zero;
- target GTIDs contain no errant transactions and no missing transactions;
- source and target are in the same MySQL release family;
- every explicit inventory probe is current and healthy;
- no additional replica requires reparenting;
- the writer-endpoint provider can execute and verify the endpoint transfer.

Warnings never authorize this first mutating operation. Any unknown evidence is
blocking.

## Execution Semantics

The workflow order is fixed:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE ->
EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Before the first mutation, ClusterGuard HA persists the operation and plan.
After acquiring the cluster lock it revalidates the exact observation token,
plan digest, resource revisions, and live MySQL preconditions.

The execution path is context-cancellable before mutation. Once the source has
been fenced, cancellation is recorded but the executor continues through the
minimum sequence needed to reach a safe, verifiable state. Each completed step
is persisted before the next step begins. A retry skips a step only after
re-observing and proving its postcondition.

MySQL syntax is selected by server capability. Legacy 5.7 statements and modern
8.x/9.x replica statements are isolated behind a dialect helper and tested
separately.

## Failure Semantics

- Failure before source fencing is terminal `failed` with no database change.
- Failure after source fencing but before target promotion is `blocked`; both
  instances must remain read-only and manual review is required.
- Failure after target promotion is `indeterminate` until verification proves
  the writable primary and endpoint owner.
- Endpoint transfer failure keeps the new primary read-only when possible and
  never reports success.
- Journal persistence uncertainty after a database commit retains the existing
  conservative `indeterminate` protocol.
- No automatic rollback promotes the old primary after the target may have
  accepted writes.

Raw SQL, passwords, and command arguments are excluded from operations, audit
events, and reports.

## API Behavior

Existing read APIs remain stable. Operation precheck and plan responses use
resource UUIDs and structured checks. Execute requires an operation ID,
idempotency key, selected target UUID, and approval token.

With the default unsupported endpoint provider:

- MySQL precheck and plan explain the endpoint-provider blocker;
- MySQL execute returns HTTP 501 with status `unsupported`;
- PostgreSQL, Oracle, and SQL Server behavior is unchanged;
- no lock, SQL mutation, or endpoint command occurs.

The operation record and its audit/report timeline can be queried after restart.

## Verification

Verification independently probes source and target and asks the endpoint
provider for owner evidence. Success requires:

- target reachable, writable, and no longer configured as a replica;
- source reachable and read-only;
- exactly one writable instance among the two controlled members;
- endpoint provider reports exactly one owner and that owner is the target;
- the stored plan digest and target match the verified operation.

Any unknown probe produces a failed or indeterminate verification, never a
success.

## Testing

- repository tests cover idempotency conflict, CAS updates, restart recovery,
  immutable plans, and persistence failures;
- adapter tests cover every precheck and both MySQL syntax families;
- executor tests cover happy path, stale observation, stale metadata, failure
  before fencing, failure after fencing, failure after promotion, retry, and
  endpoint uniqueness failure;
- workflow tests prove gate order and no mutation before all gates pass;
- API tests prove default execution is fail-closed and operation records remain
  queryable;
- race tests cover concurrent duplicate requests and operation updates;
- clean-room scans and all existing multi-engine tests remain mandatory.

## Acceptance Criteria

1. Operation and plan state survive a process restart.
2. Duplicate idempotent requests cannot execute twice.
3. A stale observation, target, plan digest, or metadata revision is blocked.
4. MySQL planned switchover passes the full workflow in deterministic tests.
5. No success is reported without independent role and endpoint verification.
6. Default runtime execution remains unsupported without a real endpoint
   provider and performs no side effect.
7. Failover, former-primary rejoin, node lifecycle, and automatic recovery
   remain unsupported.
8. PostgreSQL, Oracle, and SQL Server behavior is unchanged.
9. `go test ./...`, `go test -race ./...`, `go vet ./...`, clean-room scans,
   and configuration validation pass.
