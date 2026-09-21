# Proven MySQL HA Methods

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](zh-CN/proven-mysql-ha-methods.md)
<!-- /LANGUAGE-SWITCH -->


## Purpose

This document records production behaviors and laboratory scenarios that were
validated in the previous prototype and are intentionally reimplemented in the
independent ClusterGuard HA control plane. It is a behavioral reference only.
No source package, API, table, configuration key, executable, or product name
is inherited.

## Stable Resource Identity

Every physical host receives two stable identities before it can be managed:

- an immutable platform `resource_id` UUID;
- a globally unique, immutable operator-facing `node_name`, for example
  `cg-data-0001`.

MySQL instances additionally bind to the native `server_uuid`. Hostname, IP,
port, display name, and endpoint aliases are mutable coordinates. A coordinate
change updates the existing resource and retains the old address as an alias;
it must not create another node or instance.

Current implementation:

- `internal/store/nodes.go` enforces global node-name uniqueness and
  immutability;
- discovery binds observations by MySQL `server_uuid` and then associates the
  registered node UUID;
- metadata reconciliation preserves both platform and native identity while
  updating the database endpoint atomically;
- the console displays `node_name`, resource UUID, `server_uuid`, and mutable
  coordinates separately.

## Cluster Inventory Boundary

ClusterGuard controls only resources in the selected cluster inventory. A
request cannot introduce an arbitrary endpoint during discovery or execution.
The operation plan carries cluster, source, target, topology observation, and
resource revisions. A coordinate or inventory change invalidates a stale plan.

Cluster display names are stable operator labels. Selecting a cluster never
depends on the current primary hostname, so switching the primary does not
rename or duplicate the cluster.

## Failure Observation Window

Automatic recovery uses a stable incident rather than a single failed probe.
The default evidence window is at least three seconds, represented by three
current observations. A successful or indeterminate recovery for the same source
primary and incident cannot be submitted again. Failed pre-commit attempts can
retry only after the configured backoff.

Automatic recovery additionally requires:

- the current Raft leader;
- controller majority;
- fresh complete topology evidence;
- exactly one failed or unknown recorded primary;
- an eligible rank-one candidate;
- fencing and endpoint lease evidence;
- the normal durable operation workflow.

## Writer And VIP Coupling

A writer-role change and VIP transfer are one operation. Success requires all
of the following after execution:

- exactly one writable MySQL instance;
- exactly one VIP owner;
- the VIP owner is the verified primary;
- every reachable follower is attached to that primary;
- incomplete host probes are not treated as success.

The endpoint lease is majority-backed and leader-issued. A node that cannot
renew a valid majority lease removes its local VIP and enforces read-only. A
recovered former primary therefore cannot return as a second writer or retain
the writer endpoint.

## Controlled Switch And Former-Primary Recovery

Planned switching follows the shared workflow:

`DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT`

The MySQL adapter fences the source, catches up the selected candidate,
promotes it, reparents followers, transfers the VIP, and verifies the final
invariants. A post-commit verification gap is `indeterminate`, never success.

Former-primary recovery always returns the node as a read-only replica of the
current primary. A safe GTID subset may use fast reattachment. Divergent,
missing, or physically replaced data uses the same rebuild pipeline as adding
a node.

## Node Add And Rebuild Pipeline

Adding a node and repairing a physically damaged node use one durable lifecycle
workflow:

1. validate registered cluster, fixed node identity, endpoint uniqueness,
   package availability, donor health, and operation concurrency;
2. install the selected MySQL version without creating an independent writer;
3. keep the target read-only;
4. choose Clone, XtraBackup, or logical dump from explicit capabilities;
5. configure version-compatible replication from the current primary;
6. verify native identity, source, threads, lag, read-only state, and absence of
   a local VIP;
7. commit node and instance metadata only after verification.

A rebuild reuses the original resource UUID and fixed node name. Data-node
count is unrestricted. Controller or mixed-node changes must end with an odd
membership of at least three and are committed as one verified membership
change.

The logical-dump fallback deliberately purges every non-system schema on the
target before donor import. This prevents a physically replaced or divergent
target from retaining target-only data after GTID state is reset. It is allowed
only inside the guarded rebuild workflow and never runs as an implicit repair.

## Planned Restart

A managed MySQL restart is an operation, not a raw service restart. It places
the instance in maintenance, suppresses automatic recovery for the approved
window, restarts MySQL, verifies readiness and discovery, verifies VIP
uniqueness, and only then removes maintenance. Failure leaves recovery
suppression visible and requires operator review rather than silently reopening
automatic actions.

## Installation And Configuration

The final installer must accept a prepared ClusterGuard bundle and target host
inventory, perform all preflight checks before mutation, and render one shared
configuration source for the server, agent, endpoint reconciliation, and
systemd units. Secrets are provided through protected server-side environment
references and never written to task history or API responses.

The installation flow is staged and idempotent: preflight, copy, install,
configure, start, register, synchronize, verify, and report. Re-running a
completed stage checks its postcondition instead of blindly replaying it.

## Operator Visibility

The console exposes separate views for fleet overview, selected-cluster
topology, guarded operations, node lifecycle, metrics, operation logs, product
information, and settings. Every visible execution action either calls a real
ClusterGuard API or is disabled with the backend capability reason. Raw
operation evidence is collapsed by default but remains available for audit.

## Validated Delivery

The `rc61` bundle (`clusterguard-ha-mysql-parity-rc61-linux-amd64.tar.gz`, the
artifact recorded in the acceptance matrix) contains the controller, CLI,
restricted agent, lifecycle executor, systemd units, timers, log rotation,
configuration templates, and installer. It was deployed to three controllers and three colocated data nodes.
The acceptance matrix verified six clusters, 50 repeated real switchovers,
quorum-loss self-isolation, primary network failure, former-primary return,
repeated restart, full-host reboot, divergent rebuild, and endpoint metadata
reconciliation. Detailed evidence is recorded in
`docs/mysql-feature-parity-acceptance.md`.

Production hardening that remains environment-specific:

- configure and exercise the external fencing provider against the deployment
  platform's hypervisor, cloud, PDU, or BMC API;
- qualify Clone or the exact XtraBackup build for each supported MySQL family;
- establish backup/restore, retention, and recovery-time objectives;
- integrate external alert delivery and long-term metrics retention;
- repeat the destructive matrix on the target kernel, network, storage, and
  MySQL packages before production enablement.
