# ClusterGuard HA Architecture

## Design Goals

ClusterGuard HA is a database-neutral control plane with engine-specific
adapters. Platform identity, inventory authority, workflow gates, persistence,
API behavior, audit, and reports belong to the control kernel. Database
protocol details belong to adapters.

The sealed 2.1 line enables the MySQL control path. PostgreSQL delivery starts
in the 2.2 line. Oracle and SQL Server remain separately qualified future
product lines; code behind a capability gate is not a production support claim.
The active development tree contains discovery, health, native metrics,
deterministic candidate evaluation, guarded switchover and failover,
former-primary rejoin, allowlisted repair, Linux VIP ownership, and node
lifecycle. Mutation remains capability- and configuration-gated: a missing
majority, fence, restricted Agent policy, credential, endpoint provider, or
current observation blocks the operation before the unsafe step. Oracle and SQL
Server role transitions are integrated through their native HA control planes:
Oracle Data Guard Broker via DGMGRL and SQL Server Always On via sqlcmd/T-SQL.
SQL Server discovery, health, candidate, queue-metric, execution, and
verification paths are implemented. Oracle discovery and guarded Broker
execution are implemented through the restricted node Agent; Oracle metric
expansion remains roadmap work. Mutation stays unavailable unless every native
runner and safety dependency is configured.

## Resource Model

Every durable resource has an immutable platform UUID and revision metadata.
The main resources are:

| Resource | Responsibility |
| --- | --- |
| `Platform` | Top-level administrative boundary. |
| `Controller` | Control-plane member and service endpoint. |
| `DatabaseCluster` | Engine, display name, cluster identity, and aggregate health. |
| `DatabaseNode` | Host-level placement independent of database process identity. |
| `DatabaseInstance` | Engine-native database process identity and observed runtime state. |
| `Endpoint` | Mutable hostname, IP, port, and endpoint kind. |
| `EndpointAlias` | Historical or alternate coordinates for an endpoint. |
| `ReplicationLink` | Source-to-target relationship, lag, and link health. |
| `HAEndpoint` | Desired owner and health of a VIP, listener, or service endpoint. |
| `OperationRecord` / `OperationPlan` | Idempotent intent, durable stage progress, and immutable execution plan. |
| `ApprovalGrant` | Single-use, expiring authorization bound to one operation plan and target. |
| `PlatformUser` | Platform username, role, Argon2id password hash, `MustChangePassword`, and auth revision. |
| `PlatformSession` | Hash-only opaque session and CSRF identity with an eight-hour expiry. |
| `SecurityEvent` | Login, logout, password, and authorization security evidence. |
| `Execution` / `Verification` | Execution result and postcondition evidence. |
| `AuditEvent` / `Report` | Durable operator trace and human-readable outcome. |

`resource_id` is the stable reference used by APIs, persistence, links, metrics,
and workflows. Each physical node also has a globally unique immutable
`node_name` such as `cg-data-0001`. Hostname, IP address, port, display name, and
aliases can change without creating a new node or database instance.

## Engine Identity

Native identity binds observations to the stable platform resource:

| Engine | Native identity contract |
| --- | --- |
| MySQL | Instance identity is `server_uuid`; hostname and port are endpoints. |
| PostgreSQL | Cluster identity is `system_identifier`; each node supplies an immutable `clusterguard.node_id` UUID and each standby supplies its primary node UUID. |
| Oracle | Database identity uses `DBID + DB_UNIQUE_NAME`; RAC instances are separate resources. |
| SQL Server | Availability-group identity uses `group_id`; replica identity uses `replica_id`. |

When MySQL discovery sees a known `server_uuid` at new coordinates, the
existing resource UUID is retained. Previous coordinates become aliases.
Conflicting native identities, duplicate active endpoint ownership, and
ambiguous alias updates are blocked instead of merged heuristically.

PostgreSQL discovery applies the same rule with the configured node UUID. A
hostname, IP, or port change updates the bound endpoint without creating a new
instance. The complete refresh is rejected if active endpoints report mixed
`system_identifier` values, if the value conflicts with the cluster's durable
identity, or if the first identity bind lacks full active-endpoint coverage.

## Adapter Registry

`DatabaseHAAdapter` defines engine, capability, discovery, topology, health,
precheck, plan, execute, verify, node synchronization, metadata reconciliation,
metrics, and candidate methods. The registry exposes a uniform capability map
for all four engines.

Capabilities are explicit. An unavailable capability returns `unsupported`;
there is no fallback that guesses an engine behavior. The MySQL adapter enables
native replication topology, health, metrics, candidates, metadata
reconciliation, guarded switchover/failover, former-primary rejoin, and
allowlisted repair. MySQL execution is advertised only when both a mutating SQL
executor and an executable writer-endpoint provider are configured. Node
lifecycle is owned by the platform task engine rather than bypassing the common
gates. The PostgreSQL adapter enables identity-safe discovery, topology, health,
native metrics, metadata checks, timeline-aware candidate assessment, guarded
switchover/failover, former-primary rewind and rejoin, allowlisted repair, and
base-backup node synchronization. PostgreSQL mutation is advertised only when
the required operation credentials, restricted node controller, endpoint
provider, and cluster safety evidence are available. The Oracle adapter
provides Data Guard Broker discovery, topology, health, standby candidate
assessment, precheck, plan, execute, and verify when DGMGRL is configured;
otherwise runner-backed reads and execution return `unsupported`. The SQL
Server adapter provides Always On discovery, topology, health,
synchronized-secondary candidate assessment, planned failover precheck, plan,
execute, verify, and send/redo queue metrics when sqlcmd is configured; forced
failover remains blocked without an explicit data-loss approval policy. Oracle
metrics and Oracle/SQL Server node sync continue to return `unsupported` until
their engine-specific implementations are added.

Adapter topology links use engine-native source and target identities. The
resource registry resolves those identities to immutable platform UUIDs before
publishing `ReplicationLink` resources. This keeps mutable host coordinates
out of the topology identity contract.

## Inventory Authority

Cluster registration creates the authoritative set of active database
endpoints. Discovery receives only a cluster UUID and probes that registered
inventory with server-side credentials. Registration and refresh are control
operations protected by a dedicated Bearer token. A refresh caller cannot add
an endpoint or supply credentials in its request.

Each cluster has a durable inventory generation. Any active database endpoint
change or metadata-coordinate reconciliation increments the generation and
invalidates the published topology. A refresh captures the generation before
probing and must commit against that exact nonzero generation. If inventory
changes while the probes are in flight, the complete refresh is rejected.

## Refresh Transaction

A MySQL refresh follows this sequence:

1. Resolve the cluster and its active registered database endpoints.
2. Capture the exact inventory generation and serialize refreshes per cluster.
3. Probe every endpoint with server-side read-only credentials.
4. Reconcile observations by MySQL `server_uuid`, preserving platform UUIDs and aliases.
5. Build instances, replication links, probe coverage, health, anomalies, and metric samples.
6. Atomically persist the complete observation and publish one topology snapshot.

The repository keeps a durable observation-time watermark. A refresh must be
strictly newer than that watermark; equal or older observations are rejected.
The watermark and inventory generation survive topology invalidation and
process restart. Persistence failure rolls back the whole refresh, so readers
never see a mixture of old links and new instances.

Multiple registered aliases may resolve to one instance. Their observations
share one platform UUID, and metrics persistence selects one deterministic,
complete sample for the resolved instance in each cycle.

A PostgreSQL refresh uses a separate engine-filtered scheduler and PostgreSQL
credential set. It reads `system_identifier`, primary/recovery state, read-only
state, WAL receiver status, receive/replay LSN, timeline, and replay lag from
each authoritative endpoint. Cluster identity, endpoint bindings, instances,
links, probes, and health are committed atomically. Missing node UUIDs, mixed
system identities, malformed timelines or LSNs, and partial first binding fail
closed without publishing a half-current topology.

## Candidate Intelligence

Candidate assessment uses only the latest persisted complete topology and
explicit probe evidence. It requires exactly one currently observed primary.
Each replica is evaluated for:

- selected-cluster inventory membership and healthy bound probe evidence;
- reachability and observed role;
- maintenance state and promotion eligibility;
- current read-only state and probe evidence from the exact published cycle;
- replication IO and SQL thread state;
- replication source identity matching the current primary;
- known, nonnegative lag within the configured policy;
- GTID mode and parseable executed sets;
- errant transactions and missing transactions/data-loss risk;
- compatible MySQL release family.

Blocking evidence produces an ineligible candidate. Warnings, missing
transaction count, lag, exact-version preference, and platform UUID provide a
deterministic ordering among eligible candidates. Candidate output is advisory
for API readers; the automatic recovery controller may consume only the rank-one
eligible result after a stable failure incident and all common gates.

PostgreSQL candidates are standbys from the same durable cluster identity and
timeline whose source UUID matches the observed primary. A candidate requires
fresh bound probe evidence, streaming WAL receive, active replay, known lag,
and promotion eligibility. Ranking prefers the greatest replay LSN, then lower
lag, then stable resource UUID. The selected resource UUID and observation are
bound into the immutable plan; execution revalidates both under the operation
lock before promotion.

## Metrics

MySQL discovery stores bounded cumulative and gauge samples. The metrics layer
derives QPS, TPS, slow queries per second, current connections, running threads,
buffer-pool hit ratio, and replication lag. It publishes:

- JSON from `/api/v1/clusters/{id}/metrics`;
- Prometheus text from `/api/v1/clusters/{id}/metrics/prometheus`.

The Prometheus endpoint uses stable `cluster_id` and `instance_id` labels and
can be scraped directly. ClusterGuard HA has no third-party monitoring runtime
dependency.

PostgreSQL stores native connection, transaction, deadlock, temporary-byte,
block-read, block-hit, database-size, replication-client, cache-hit, and
optional longest-transaction samples alongside topology-derived replication
lag. Prometheus series use the `clusterguard_postgresql_*` namespace and Zabbix
keys use `clusterguard.postgresql.*`. Unknown lag or optional transaction age
is omitted rather than reported as zero, and PostgreSQL data is never labeled
as MySQL.

## Platform Authentication

A new metadata store bootstraps `admin` with the first-login password
`admin123`, role `admin`, and `MustChangePassword=true`. Existing users are
never overwritten during restart or Leader change. The first password change is
required before any platform data is readable. Passwords use Argon2id and
sessions store only SHA-256 token and CSRF hashes in the same replicated
snapshot as other control metadata.

The browser receives an opaque HttpOnly SameSite session cookie and a separate
CSRF cookie. Mutating requests must present the cookie value in
`X-CSRF-Token`. An eight-hour absolute lifetime bounds a session; password
change and logout revoke it. `admin` has full access, `operator` can run guarded
database operations, and `viewer` remains read-only.

The browser never receives an approval secret. For a logged-in operation, the
API uses the authenticated actor to create the durable plan, issue a one-time
grant internally, and pass it directly to the unchanged approval-consumption
stage. Service clients remain separate: the control Bearer authenticates the
client, and an explicit one-time grant authorizes the exact database plan.

There is no online forgotten-password bypass. Recovery requires stopping
control-plane mutation and restoring protected metadata whose administrator
credential is known; live deletion of `PlatformUser` records or direct hash
editing is outside the safety model.

## Workflow Gates

All implemented database mutations use:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Adapters cannot acquire a platform lock, approve an operation, suppress audit,
or skip verification. Safety Guard is an explicit stage with its own audit
events and always runs before the operation lock. The workflow core checks
all required precheck, plan, execute, and verify capabilities before any gate
or adapter mutation. It pins the topology observation used by the operation,
records the cluster UUID and observation timestamp, and revalidates the same
token under the operation lock before approval. Discovery publication and
operation execution use the same per-cluster fence, so a refresh cannot publish
a new snapshot between revalidation and execution.

Each operation has a durable UUID and caller-supplied idempotency key. The
repository atomically persists its observation token, immutable plan digest,
referenced metadata revisions, workflow stage, completed step attempts,
execution, and verification. Reusing a key for the same intent returns the
existing operation; reusing it for another target or operation kind is a
conflict. A same-process duplicate cannot terminalize the active operation, and
a restart can resume from observed step postconditions.

High-risk database execution uses a one-time `ApprovalGrant`. A platform
session creates and consumes it entirely inside the server. An explicit service
request instead returns a random `cgag_...` token once. The replicated
repository stores only its SHA-256 hash. A grant defaults to five minutes,
cannot exceed fifteen minutes, and is bound to the operation UUID, cluster,
engine, operation kind, target UUID, observation, and plan digest. Under the
operation lock, grant consumption and the durable `APPROVE` transition commit
atomically. Reuse, expiry, target mismatch, or a stale plan fails closed. The
service execute route does not accept the administrator control credential as
an approval substitute.

MySQL switchover supports one primary with multiple replicas. It requires an
eligible selected target, compatible GTID history, current probe evidence,
running replication threads, compatible release families, and an executable
endpoint provider. The source is fenced before promotion, every reachable
follower is reparented, and success requires independent proof of one writable
instance and one target VIP owner.

Automatic failover uses a 30-second stable incident recorded by discovery. Only
the majority Leader may submit the durable operation. The same incident cannot
be repeated after success or an indeterminate outcome. Operation locks are
Raft-replicated and renewed, Safety Guard rechecks majority, and endpoint
mutation requires a separate short lease. The restricted data-node agent
removes a stale VIP and persists both MySQL read-only flags when the node cannot
obtain a valid signed keep decision.

Automatic recovery enters the workflow through a separate internal method. It
does not mint or consume a human approval grant and cannot be selected by an
HTTP field. The incident ID is recorded at `APPROVE`, while consensus, Safety
Guard, the replicated operation lock, fencing, execution, verification, audit,
and report remain mandatory.

Recovery never treats a pre-existing source fence as sufficient by itself. The
durable operation must own the completed `fence_source` step, and the adapter
rechecks source and target native identities, source fencing, GTID history,
binary logging, version compatibility, target replication state, and writer
endpoint postconditions before it can continue.

Post-commit verification binds to the immutable plan digest and planned
source/target UUIDs rather than the pre-mutation observation timestamp. This
allows a refreshed post-promotion topology to be verified without weakening
live native-identity, role, read-only, replication, or endpoint-owner checks.

Audit and report persistence is fail-closed before the mutation commit point.
After a mutation has committed, journal failure never skips verification. The
workflow completes verification and returns `indeterminate`, preserving that
the action may have changed database state and must not be retried blindly.
If the atomic metadata rename succeeds but directory synchronization cannot
confirm crash durability, the committed result is retained, verification still
runs, and the API returns the reconciled resources with an `indeterminate`
execution. Terminal reports use a crash-recoverable two-phase protocol under one
report UUID for non-operation metadata workflows. Durable database operations
instead publish their terminal record, final audit events, and report in one
repository snapshot. A failed publication exposes none of those terminal
records; a successful publication exposes all of them.

Cluster registration and topology publication use the same post-rename
semantics. If directory synchronization cannot confirm crash durability, the
API returns HTTP `500` with committed cluster and endpoint UUIDs, or the
topology observation token `cluster_id@observed_at`, instead of discarding them
and inviting an unsafe retry.

Platform metadata reconciliation is distinct from a database mutation. It may
update a known resource's mutable coordinates after native identity validation,
records old coordinates as aliases, advances inventory generation, and forces
a fresh observation before topology is trusted again.
