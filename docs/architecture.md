# ClusterGuard HA Architecture

## Design Goals

ClusterGuard HA is a database-neutral control plane with engine-specific
adapters. Platform identity, inventory authority, workflow gates, persistence,
API behavior, audit, and reports belong to the control kernel. Database
protocol details belong to adapters.

The current release enables MySQL discovery, health, metrics, candidate
evaluation, guarded switchover and failover, former-primary rejoin, allowlisted
repair, Linux VIP ownership, and node lifecycle. Mutation remains capability-
and configuration-gated. PostgreSQL, Oracle, and SQL Server are registered
through the same adapter contract and remain unsupported skeletons until their
read-only implementations are complete.

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
| PostgreSQL | Cluster identity will use `system_identifier`; node identity remains a platform UUID. |
| Oracle | Database identity will use `DBID + DB_UNIQUE_NAME`; RAC instances are separate resources. |
| SQL Server | Availability-group identity will use `group_id`; replica identity will use `replica_id`. |

When MySQL discovery sees a known `server_uuid` at new coordinates, the
existing resource UUID is retained. Previous coordinates become aliases.
Conflicting native identities, duplicate active endpoint ownership, and
ambiguous alias updates are blocked instead of merged heuristically.

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
gates. The other three adapters currently return unsupported for database
mutation.

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

## Metrics

MySQL discovery stores bounded cumulative and gauge samples. The metrics layer
derives QPS, TPS, slow queries per second, current connections, running threads,
buffer-pool hit ratio, and replication lag. It publishes:

- JSON from `/api/v1/clusters/{id}/metrics`;
- Prometheus text from `/api/v1/clusters/{id}/metrics/prometheus`.

The Prometheus endpoint uses stable `cluster_id` and `instance_id` labels and
can be scraped directly. ClusterGuard HA has no third-party monitoring runtime
dependency.

## Workflow Gates

All future database mutations must use:

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
