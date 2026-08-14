# ClusterGuard HA MySQL Feature Parity Design

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/specs/2026-07-13-mysql-feature-parity-design.md)
<!-- /LANGUAGE-SWITCH -->


## Purpose

ClusterGuard HA will independently implement the production MySQL capabilities
proved by the previous prototype. The previous repository is a behavioral and
test-scenario reference only. No source package, API, metadata table, binary,
configuration key, command name, or product identity is reused.

The delivery target is a three-data-node MySQL HA cluster managed by an odd
number of ClusterGuard controllers. Operators must be able to discover a
cluster, choose a promotion candidate, switch or fail over the primary and VIP
as one operation, repair a recovered former primary, add or remove nodes,
correct mutable metadata, inspect performance, and audit every action from one
console.

## Non-Negotiable Safety Rules

- Platform UUIDs are the only durable resource keys. Hostname, IP, port, and
  display name are mutable coordinates.
- Native MySQL `server_uuid` binds an observation to an existing instance.
- A database mutation always follows `DISCOVER -> PRECHECK -> PLAN ->
  SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`.
- A writer-role change and its active VIP change are one operation. Success is
  impossible unless exactly one writable MySQL instance and exactly one VIP
  owner are independently verified.
- Unknown probe coverage, unknown controller quorum, stale topology, an
  unisolated old primary, or conflicting lease ownership blocks mutation.
- Operations after a possible commit point return `indeterminate` on incomplete
  verification. They are never reported as successful merely because a command
  exited zero.
- Secrets are resolved on the server, split by purpose, redacted from durable
  resources, and never accepted as CLI arguments or request fields.
- Unsupported behavior returns `unsupported`; no simulated success is exposed
  as a real action.

## Capability Parity Matrix

| Capability | ClusterGuard implementation | Completion evidence |
| --- | --- | --- |
| Inventory discovery | Registered endpoints probed by the MySQL adapter | Stable resource UUID after hostname/IP/port change |
| Topology and health | Native replication links, roles, threads, lag, GTID and probe coverage | Three-node snapshot with one primary and two replicas |
| Candidate evaluation | Deterministic eligibility and data-loss risk ranking | Selected target is a healthy direct replica with no errant GTID |
| Planned switchover | Fenced source, GTID catch-up, target promotion, all replicas reparented | New primary, all followers healthy, VIP target-only |
| Failure switchover | Stable failure window, quorum lease, fencing evidence, best candidate promotion | Old primary cannot write; one new writer and one VIP owner |
| Former-primary recovery | Fast GTID reattach when safe; rebuild path when divergent | Former primary read-only and following current primary |
| Low-risk repair | Refresh, collect status, start IO/SQL thread, maintenance state | Mutation allowlist plus verification and audit |
| VIP lifecycle | Status, acquire, release, transfer, verify, periodic reconcile | Cluster-wide unique owner matching current primary |
| Split-brain protection | Controller quorum, short lease, multi-observer probes, local self-isolation | Minority node removes VIP and remains read-only |
| Reboot convergence | Agent boot guard and periodic reconcile | VIP returns only to verified primary after restart |
| Metadata reconciliation | Coordinate update, aliases, duplicate/native-identity checks | No duplicate resource after hostname or port change |
| Data-node lifecycle | Install, initialize, synchronize, register, verify, remove | Added node appears as a healthy replica without manual SQL |
| Former-node rebuild | Same lifecycle workflow with a retained resource slot | Repaired host reuses resource UUID and rejoins as replica |
| Controller lifecycle | Atomic odd membership expansion/removal | No committed even controller set |
| Data synchronization | Clone, XtraBackup, logical dump fallback selected by capability | Method, source, progress, verification recorded |
| Metrics | Native JSON and Prometheus output plus monitoring-safe flat JSON | QPS, TPS, connection, thread, slow-query, buffer and lag metrics |
| Operations UI | Cluster selector, topology, controlled switch, former-primary repair, node lifecycle | Every visible action calls a real API or is disabled |
| Audit and reports | Operation timeline, raw evidence, JSON/HTML report | Source, target, time, operator, gates and verification visible |
| Installation | One bundle, preflight, systemd, logrotate, controller/data-node roles | Fresh three-host install passes smoke and recovery tests |

## Architecture

### Control Plane

The control plane owns resource identity, operation state, immutable plans,
controller quorum, leases, approval, locking, audit, reports, and API behavior.
It never runs engine-specific SQL directly. A durable operation references a
cluster UUID, source and target instance UUIDs, the exact topology observation,
resource revisions, and a plan digest.

The controller set uses an odd membership rule. One leader serializes mutating
operations and lease grants. Followers serve read-only requests and reject or
redirect mutations. A quorum loss blocks new mutations and causes VIP-capable
agents without a valid renewable lease to self-isolate.

### MySQL Adapter

The adapter owns MySQL dialect differences and engine evidence:

- MySQL 5.7 uses `SLAVE`, `MASTER`, and explicit role persistence through the
  node agent.
- MySQL 8.x/9.x uses the available `REPLICA`, `SOURCE`, and persisted-variable
  syntax discovered at runtime.
- GTID mode is mandatory for automatic switch, failover, and fast reattach.
- The adapter builds deterministic plans but cannot grant locks, approvals,
  leases, or audit exemptions.
- Replication credentials are distinct from discovery and operation
  credentials and are supplied only for `CHANGE ... SOURCE/MASTER TO`.

### ClusterGuard Agent

`clusterguard-agent` is a small privileged helper installed on every managed
data node. It exposes only an authenticated, allowlisted command contract:

- report VIP ownership and interface state;
- add or remove one configured cluster VIP;
- emit gratuitous ARP after acquisition;
- persist MySQL read-only role state across restart;
- run a local self-isolation action;
- execute lifecycle stages from a signed controller plan.

The agent validates cluster UUID, operation UUID, lease UUID, VIP address,
interface, prefix, instance UUID, and plan digest. It does not expose a general
shell command endpoint. Controller-to-agent transport uses pinned host keys and
a dedicated service identity. An unreachable agent is unknown evidence and
blocks a new VIP owner.

### Writer Endpoint Provider

The Linux VIP provider resolves the selected cluster's active `HAEndpoint` and
all inventory hosts. `Precheck` probes every host and passes only when the
current owner set is known and consistent with the planned source state.
`Transfer` performs:

1. verify the operation holds the active cluster lock and endpoint lease;
2. remove the VIP from every observed non-target owner;
3. verify zero owners;
4. acquire the VIP on the target and send gratuitous ARP;
5. verify target-only ownership from all observers.

If target acquisition fails during a planned switch before target write
enablement, the provider may restore the VIP to the still-fenced source. After
the target becomes writable, failures are indeterminate and require the normal
verification/reconciliation path.

## Credentials

Each MySQL cluster references secret names, never secret values:

- `discovery`: process list, replication status, variables, metrics;
- `operation`: read-only fencing, role transitions, stop/start/reset replica;
- `replication`: replication channel authentication;
- `install`: host bootstrap and node synchronization.

The default environment secret store resolves named credentials. The API
rejects username/password fields. Later secret-store providers can implement
the same interface without changing adapters.

## Planned Switchover State Machine

The three-node planned switch is:

1. revalidate inventory, native identities, revisions, probe coverage, GTID,
   replication links, target eligibility, controller quorum, and VIP ownership;
2. fence the source with `super_read_only` and `read_only`, then persist it;
3. capture the fenced source GTID and wait for every healthy replica required by
   policy, with the selected target required to reach it;
4. stop/reset the target replication channel and make it writable;
5. configure the former source and every sibling replica to follow the target
   with GTID auto-position, then start and verify both threads;
6. transfer the VIP under the endpoint lease;
7. refresh discovery and verify one writable primary, every follower attached to
   its native UUID, bounded lag, and one VIP owner;
8. publish the terminal audit events and report atomically.

The operation journal records each idempotent step. A restart resumes by
checking the step postcondition, not by replaying mutation blindly.

## Failover and Automatic Recovery

Failure recovery is a separate operation kind. It requires a stable 30-second
observation window by default, six five-second checks, controller quorum, a
healthy candidate, a fresh exclusive endpoint lease, and fencing or equivalent
proof that the old primary cannot serve writes. The candidate with the lowest
data-loss risk is promoted. Remaining reachable replicas are reparented and the
VIP is transferred. An unfenced or unobservable old primary keeps the operation
blocked even if that means temporary unavailability.

Every data-node agent periodically reconciles local state. A node without
majority visibility or without the active lease for its local instance removes
the VIP and enforces read-only. A recovered old primary therefore cannot return
as a second writer or VIP owner.

## Former-Primary Recovery and Repair

Recovery first proves the current primary, target native identity, no local VIP,
read-only state, GTID relation, and absence of errant transactions. If the
former primary's executed set is a subset of the current primary, it is attached
with GTID auto-position. If it is divergent, the workflow blocks fast reattach
and offers the node synchronization/rebuild plan. Replica IO and SQL thread
start are the only direct low-risk replication mutations; transaction skipping,
resetting data, or changing source outside a signed repair/rebuild plan remains
blocked.

## Node Lifecycle

Data-node and former-node rebuild use one lifecycle workflow:

`PREFLIGHT -> INSTALL -> INITIALIZE -> SYNC -> CONFIGURE_REPLICATION -> REGISTER
-> DISCOVER -> VERIFY -> COMMIT_METADATA`.

Synchronization selection is capability-driven:

1. MySQL Clone when source/target versions and plugin state are compatible;
2. XtraBackup when an installed matching binary supports the release;
3. logical dump as the slow fallback.

Data-node count is unrestricted. Controller and mixed-node membership must end
at an odd number and is committed only after every new controller is healthy.
Removing a primary, active VIP owner, synchronization source, or controller that
would lose quorum is blocked.

## Metadata Reconciliation

The metadata API and topology modal accept mutable hostname, IP, port, display
name, aliases, and endpoint corrections. The precheck probes the proposed
endpoint and requires the same native `server_uuid`. A matching native identity
updates the existing resource and retires the previous coordinate as an alias.
Duplicate active coordinates, duplicate native identities assigned to different
resources, server UUID changes, or unregistered targets are blocked.

## API Extensions

The existing `/api/v1` resource and operation routes remain the canonical
surface. The MySQL parity work adds resource-oriented routes:

- `GET/POST /api/v1/clusters/{id}/ha-endpoints`
- `GET /api/v1/clusters/{id}/vip/status`
- `POST /api/v1/clusters/{id}/vip/reconcile`
- `POST /api/v1/operations/former-primary-rejoin/*`
- `POST /api/v1/operations/repair/*`
- `POST /api/v1/nodes/sync/{precheck,plan,execute,verify}`
- `POST /api/v1/nodes/{precheck,plan,execute,verify}`
- `GET /api/v1/operations/{id}/timeline`
- `GET /api/v1/reports/{id}` and `/api/v1/reports/{id}/html`
- `GET /api/v1/monitoring/health` and `/api/v1/monitoring/metrics`

All POST routes require control authentication. Mutating operation routes also
require the workflow lock, approval policy, and plan digest; callers never
invoke the agent directly.

## Console

The console has six work surfaces:

1. Overview: fleet-level cluster health, risk, lag, writers, VIP ownership, and
   active operations without rendering a topology graph.
2. Topology: selected-cluster nodes, immutable identity, mutable coordinates,
   role, version, lag, replication edges, VIP owner, and metadata modal.
3. Operations: selected cluster, current primary, selectable candidate, VIP,
   lag, one controlled switch action, and former-primary recovery.
4. Nodes: add/remove/rebuild data and controller nodes with stage progress.
5. Metrics: built-in MySQL performance, replication health, alert thresholds,
   JSON and Prometheus endpoints.
6. Operation Log: source, target, operator, time, status, verification, report,
   and collapsed raw evidence.

Visible controls are capability-driven. A blocked action explains the exact
failed check; it never silently does nothing.

## Verification Strategy

Every behavior is developed test-first. Unit tests cover dialects, plans,
leases, endpoint uniqueness, candidate and GTID logic. Workflow tests cover
gate order, idempotent resume, crash points, audit/report atomicity, and
indeterminate results. HTTP tests cover authentication and rejection of secret
fields. Agent tests use isolated network namespaces or command fakes. Lab tests
run on `192.168.102.152-154` and include repeated round-robin and random
switches, controller/database restart, network partition, stale VIP, former
primary recovery, metadata changes, and node rebuild.

## Delivery Order

The complete scope is implemented as independently releasable increments:

1. credential separation, HA endpoint inventory, agent, and real VIP provider;
2. three-node planned switchover and former-primary rejoin;
3. failover, quorum lease, self-isolation, and automatic recovery;
4. repair actions and node lifecycle;
5. metrics, operation timeline, console, installer, and destructive lab matrix.

An increment is not called production-ready until its real lab path succeeds
and all unsupported paths still fail closed.
