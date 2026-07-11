# ClusterGuard HA MySQL Capability Migration Design

## Objective

Bring the proven MySQL product capabilities from the frozen prototype into
ClusterGuard HA as independently designed behavior. No source code, package,
API, configuration, persistence schema, binary, or runtime dependency is copied
or compatibility-wrapped. PostgreSQL, Oracle, and SQL Server retain explicit
extension points throughout the work.

The completed MySQL product path must let an operator discover a cluster,
inspect topology and replication risk, select a candidate, perform a guarded
planned switchover or failure recovery, move the writer endpoint with the new
primary, rejoin the former primary, add or replace nodes, verify the result, and
inspect the complete audit trail.

## Decision and Alternatives

Three migration approaches were considered:

1. A single MySQL-specific service containing discovery, switching, endpoint,
   and installation logic. This is quick initially but prevents other engines
   from sharing the control plane safely.
2. A compatibility facade around the frozen prototype. This preserves behavior
   fastest but violates the independent-kernel requirement and carries forward
   endpoint-based identity and hidden coupling.
3. Capability-oriented services coordinated by the common workflow, with
   engine behavior behind adapters and writer-endpoint behavior behind a
   separate endpoint provider.

The third approach is selected. It preserves MySQL behavior while keeping
database role changes, endpoint ownership, host provisioning, and platform
coordination independently testable.

## Architectural Boundaries

### Platform Core

The platform core owns immutable resource UUIDs, metadata revisions, operation
state, plans, locks, approvals, audit events, verification records, reports,
and public API contracts. It does not issue database-specific SQL or operating
system endpoint commands.

### Database Engine Adapter

`DatabaseHAAdapter` owns engine-specific discovery, topology interpretation,
health collection, candidate evaluation, precheck, plan construction, role
transition, replication reconfiguration, old-primary rejoin, node-sync
validation, and post-operation verification.

MySQL identifies an instance by `server_uuid`. Hostname, IP address, port,
server ID, and display name remain mutable metadata. A change to any endpoint
coordinate reuses the existing resource UUID.

### Writer Endpoint Provider

Writer endpoint ownership is not part of the MySQL adapter. A new
`HAEndpointProvider` contract manages VIP or future listener/service endpoints:

- `Capabilities()`
- `Inspect()`
- `Precheck()`
- `BuildPlan()`
- `AcquireLease()`
- `Assign()`
- `Withdraw()`
- `VerifyUniqueOwner()`

The first provider implements Linux VIP management. Future providers may
implement a database listener, load balancer, cloud private endpoint, or another
writer-address mechanism without changing database adapters.

### Node Provisioning Provider

A separate `NodeProvisioner` contract owns host preflight, database package
installation, instance initialization, data seeding, service registration, and
decommissioning. The MySQL adapter chooses and validates replication semantics;
the provisioner performs host-level work through an explicit task plan.

### Controller Coordination

Controller leadership, quorum, distributed operation locks, endpoint leases,
and fencing tokens belong to a coordination service. Single-controller mode
uses the same interface with a local durable implementation. Multi-controller
mode may replace it without changing workflow or adapters.

## MySQL Capability Set

### Discovery and Inventory

- Discover `server_uuid`, server ID, version, hostname, address, port, role,
  read-only flags, GTID mode, binary-log settings, and replication channels.
- Build replication links from source UUID to replica UUID.
- Detect endpoint aliases, duplicate identities, stale endpoints, unmanaged
  nodes, identity mismatches, and cross-cluster membership conflicts.
- Keep the configured cluster name stable across primary changes.
- Reject control actions for instances outside the selected cluster inventory.

### Health and Performance

- Collect reachability, primary writability, replica IO/SQL thread state,
  replication lag, GTID position, errant transactions, connection count, QPS,
  TPS, running threads, slow-query rate, and buffer-pool hit ratio.
- Publish engine-neutral health metrics through JSON and Prometheus text
  endpoints without requiring an external exporter.
- Classify health as healthy, degraded, unhealthy, or unknown. Unknown probe
  coverage is never represented as healthy.

### Candidate Evaluation

- Rank only inventory members that are reachable replicas and pass promotion
  eligibility.
- Evaluate lag, replication threads, GTID consistency, errant transactions,
  binary/relay log position, maintenance state, version compatibility,
  read-only state, and data-loss risk.
- Return blocking checks, warnings, recommended candidate, risk level, and
  estimated data-loss risk.
- Permit an operator to select another passing candidate; never silently replace
  an explicitly selected target.

### Planned Switchover

- Confirm the current primary is reachable and stable.
- Drain or block new writes through an explicit plan step.
- Wait for the target to catch up within configured thresholds.
- promote the selected target, redirect replicas, transfer the writer endpoint,
  verify unique endpoint ownership, and rejoin the former primary as a replica.
- Treat role transition and writer-endpoint transfer as one workflow with
  compensation and verification steps, not as two independent buttons.

### Failure Recovery

- Require controller leadership, quorum, a valid operation lock, and a current
  fencing token before automatic recovery.
- Confirm failure across a configurable observation window and multiple probes.
- Block automatic promotion when the old primary cannot be isolated and writer
  endpoint uniqueness cannot be proven.
- Promote the best passing candidate, redirect surviving replicas, assign the
  writer endpoint, verify topology, and report any node whose status remains
  unknown.
- Never claim success until the new primary is writable, replicas follow it,
  and the writer endpoint has exactly one confirmed owner.

### Former-primary Rejoin

- Identify the recovered node by `server_uuid`, even when its hostname, IP, or
  port changed.
- Keep it read-only during assessment and rejoin.
- Choose incremental GTID rejoin when histories are compatible.
- Choose a full seed workflow when histories diverged or local data cannot be
  trusted.
- Verify that it follows the current primary before making it eligible again.

### Node Add, Replace, and Remove

- Add a data node, controller node, or combined node from the web console and
  API.
- Permit any supported number of data nodes. Enforce an odd controller count
  for a production coordination group and reject changes that would lose
  quorum.
- Use one replacement workflow for a repaired physical node and a new node:
  preflight, install, initialize, seed, start replication, verify, update
  metadata, and mark eligible.
- Seed data through a capability-selected strategy. Clone is preferred where
  supported, physical backup is the second choice, and logical dump is the
  guarded fallback.
- Removal checks current role, writer-endpoint ownership, replication
  dependencies, and controller quorum before decommissioning.

### Operations Experience

- The console selects a stable cluster name, then scopes every topology,
  operation, log, and report request to that cluster UUID.
- Topology shows hostname, IP address, port, version, role, lag, health, and
  writer endpoint ownership for every inventory member.
- The primary operation surface presents only candidate selection, current
  primary, writer endpoint, lag, lock state, and the relevant execute action.
- Metadata editing is a modal on the topology view and runs through metadata
  reconciliation rather than directly changing stored rows.
- Operation logs show start/end time, source primary, target primary, execution
  mode, terminal status, and collapsed raw responses.

## Unified Operation Model

The generic `OperationKind` expands to support:

- `topology_refresh`
- `switchover`
- `failover`
- `former_primary_rejoin`
- `node_add`
- `node_replace`
- `node_remove`
- `metadata_reconciliation`
- `endpoint_reconcile`

Capabilities are advertised per operation and stage, rather than through one
coarse `execute` flag. Each mutating operation follows:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Long-running operations persist their stage, attempt count, lease/fencing token,
check results, plan, execution output, and verification result. Repeated API
requests use an idempotency key and return the existing operation.

## API Evolution

Existing `/api/v1` read routes remain valid. New generic routes use resource
UUIDs and operation kinds:

- `POST /api/v1/clusters/{id}/discover`
- `GET /api/v1/clusters/{id}/candidates`
- `POST /api/v1/operations`
- `GET /api/v1/operations/{id}`
- `POST /api/v1/operations/{id}/approve`
- `POST /api/v1/operations/{id}/execute`
- `GET /api/v1/operations/{id}/events`
- `GET /api/v1/clusters/{id}/metrics`
- `GET /api/v1/clusters/{id}/metrics/prometheus`

The API does not expose adapter-specific SQL or shell commands. Engine-specific
details appear only in structured checks, plan steps, metrics, and raw audit
attachments.

## Extensibility Rules

- The workflow core switches on operation kind and capability descriptors, not
  engine name.
- Engine-specific fields live in typed adapter detail objects or namespaced
  metadata, never in shared resource identity fields.
- Writer endpoints and node provisioning are provider interfaces independent of
  database engine.
- Public APIs use common resources and operations; adapters translate them into
  engine actions.
- An unsupported adapter stage fails before lock acquisition and before any
  external side effect.
- Capability versions allow a new adapter method or plan-step type to be added
  without changing existing stored operations.

## Error Handling and Recovery

- Every external action records start time, finish time, target resource,
  redacted input, output summary, and retry classification.
- Plans distinguish retryable probe failures, blocking safety failures, and
  irreversible execution failures.
- Workflow restart resumes from persisted state only when the stored plan,
  metadata revisions, lock lease, and fencing token are still valid.
- A stale plan is invalidated and must be rebuilt; it is never executed against
  changed topology.
- Secrets are referenced by secret IDs or environment variables and never
  written to plans, audit events, reports, or command arguments.

## Delivery Decomposition

The work is divided into four independently testable projects:

1. **MySQL topology and candidate intelligence**: complete discovery,
   replication links, health/performance metrics, inventory guards, candidate
   ranking, and console topology.
2. **Guarded MySQL role operations**: durable operation state, planned
   switchover, failure recovery, former-primary rejoin, verification, audit,
   and reports. Writer endpoint steps initially use a fake provider in tests.
3. **Writer endpoint and controller safety**: Linux VIP provider, durable lease,
   quorum/leadership checks, fencing token, uniqueness probes, compensation,
   and automatic recovery observation windows.
4. **Node lifecycle and delivery**: provisioner contract, host preflight,
   installation, seed strategy, add/replace/remove workflows, controller group
   changes, web experience, packaging, and upgrade documentation.

Each project produces a separate implementation plan and commit series. A later
project may depend only on committed public contracts from earlier projects.

## Verification Strategy

- Unit tests use fake SQL runners, endpoint providers, provisioners,
  coordinators, and deterministic clocks.
- Contract tests run every adapter against the same unsupported, safety-gate,
  idempotency, audit, and verification suite.
- Integration tests create multi-instance MySQL topologies for supported
  versions and validate discovery, candidate ranking, switching, rejoin, and
  endpoint ownership.
- Fault tests cover process restart, network partition, stale lock, stale plan,
  loss of quorum, ambiguous old-primary state, duplicate writer endpoints, and
  partial verification.
- Browser tests exercise cluster selection, candidate selection, topology,
  guarded execution, operation progress, metadata reconciliation, and logs.
- Clean-room scans remain mandatory for every release.

## Acceptance Criteria

- MySQL functionality is implemented entirely in the independent repository.
- Platform resources remain UUID-addressed across hostname, IP, and port
  changes.
- MySQL topology, health, candidate ranking, role operations, writer endpoint,
  former-primary rejoin, and node lifecycle are all represented by common
  capabilities and workflows.
- No mutating action bypasses safety, lock, approval, verification, audit, or
  reporting.
- Automatic failure recovery cannot proceed without leadership, quorum,
  fencing, old-primary isolation, and writer-endpoint uniqueness evidence.
- PostgreSQL, Oracle, and SQL Server continue to compile against the same SDK
  and return explicit unsupported results until their capabilities are added.
- The first implementation project can be released independently as a
  read-only MySQL topology and candidate-assessment upgrade.
