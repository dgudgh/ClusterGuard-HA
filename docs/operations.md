# ClusterGuard HA Operations

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](zh-CN/operations.md)
<!-- /LANGUAGE-SWITCH -->


Version boundary: `v2.1.45` is the sealed MySQL release. PostgreSQL operations
belong to the 2.2 line. Oracle and SQL Server procedures remain separately
gated until their own production qualification is complete.

> **Scope:** this document is the control-plane and API reference. Every example uses the
> **source-configuration defaults** (`http://127.0.0.1:8088`, plaintext loopback), while a
> production installation created by `scripts/install_clusterguard.sh` listens on
> `https://<host>:3000` with TLS enforced (certificate `/etc/clusterguard/tls/server.crt`,
> CA `/etc/clusterguard/tls/ca.crt`, API port defaulting to 3000). On a real deployment,
> replace the address with `https://<host>:3000`, add `--cacert /etc/clusterguard/tls/ca.crt`
> to curl commands, and rewrite each `cgctl <subcommand>` as
> `cgctl --server https://<host>:3000 --ca-file /etc/clusterguard/tls/ca.crt <subcommand>`
> (global flags must precede the subcommand). For day-to-day work follow the
> [Operations Manual](en-US/operations-manual.md).

## 1. Configure the Control Plane

Use `configs/clusterguard.example.json` as the configuration shape. The loader
rejects unknown keys and multiple JSON values.

Implemented keys:

| Key | Required | Meaning |
| --- | --- | --- |
| `http_address` | No | HTTP listen address; blank defaults to `127.0.0.1:8088`. |
| `tls_cert_file`, `tls_key_file` | For non-loopback API access | Server certificate and key. Non-loopback plaintext is rejected unless the unsafe lab override is explicit. |
| `tls_ca_file` | For HTTPS controller RPC | CA used to verify the configured Leader API address during mutation forwarding. |
| `allow_insecure_http` | Lab only | Explicitly permits non-loopback plaintext HTTP and emits a startup warning. Never enable where credentials or sessions cross an untrusted network. |
| `metadata_path` | Yes | Durable metadata snapshot path. |
| `control_token_env` | No | Environment variable containing the Bearer token for control API `POST` requests. Without it, all control `POST` routes fail closed with `503`. |
| `approval_token_env` | Deprecated | Legacy node-lifecycle approval only. It is ignored by database operations and automatic recovery; new database execution uses one-time grants. |
| `mysql.enabled` | No | Enables server-side MySQL discovery and operation credentials. |
| `mysql.discovery` | When enabled | Dedicated read-only discovery username and password environment reference. |
| `mysql.operation` | When enabled | Dedicated administrative operation username and password environment reference. |
| `mysql.replication` | When enabled | Dedicated replication username and password environment reference. |
| `mysql.automatic_failover_enabled` | No | Enables leader-only automatic failover. The raw configuration default is `false`; the supported multi-node installer enables it for VIP-backed MySQL HA with Agent quorum fencing. |
| `mysql.automatic_failover_interval_seconds` | No | Recovery-controller poll interval; defaults to 1 second. |
| `mysql.automatic_failover_retry_seconds` | No | Backoff after a blocked or failed incident attempt; defaults to 30 seconds. |
| `postgresql.enabled` | No | Enables native PostgreSQL discovery and configured HA capabilities; defaults to `false`. |
| `postgresql.discovery_interval_seconds` | No | PostgreSQL scheduler interval; defaults to 1 second and is independent of MySQL. |
| `postgresql.discovery_timeout_seconds` | No | Per-endpoint PostgreSQL probe timeout; defaults to 1 second. |
| `postgresql.automatic_failover_enabled` | No | Enables leader-only PostgreSQL automatic failover; defaults to `false`. |
| `postgresql.automatic_failover_interval_seconds` | No | PostgreSQL recovery-controller poll interval; defaults to 1 second. |
| `postgresql.automatic_failover_retry_seconds` | No | Backoff after a blocked or failed PostgreSQL incident attempt; defaults to 30 seconds. |
| `postgresql.discovery` | When enabled | Dedicated monitor username, database, and password environment reference. |
| `postgresql.operation` | For PostgreSQL mutation | Dedicated operation username, database, and password environment reference. Must be configured together with `postgresql.replication`. |
| `postgresql.replication` | For PostgreSQL mutation and node sync | Dedicated replication username, database, and password environment reference. Must be configured together with `postgresql.operation`. |
| `consensus` | For real HA mutation | Odd Raft controller membership, persistent state, and majority authority. |
| `consensus.snapshot_cas_enabled` | For replicated mutation | Explicitly activates snapshot content compare-and-swap on an all-upgraded controller set. Missing or `false` keeps metadata mutation fail-closed. |
| `consensus.replicated_log_compression_enabled` | Recommended after rolling upgrade | Compresses full-state Raft log entries while continuing to read legacy uncompressed entries. Activate only after every voter runs a supporting binary. |
| `consensus.tls_cert_file`, `tls_key_file`, `tls_ca_file` | For non-loopback Raft | Mutual-TLS identity and private CA for controller-to-controller Raft traffic. All three are required together. |
| `consensus.peers[].api_address` | Recommended | Trusted HTTPS address used to forward mutations to that controller when it is Leader. It is never derived from the inbound HTTP Host header. |
| `consensus.allow_insecure_transport` | Lab only | Explicitly permits non-loopback plaintext Raft and emits a startup warning. |
| `agent` | For VIP mutation | Restricted signed node command transport for VIP ownership, role status, and in-band self-isolation. |
| `fencing.agent_quorum_enabled` | For MySQL automatic failover | Uses a short Raft-majority authorization and local Agent fail-closed reconciliation before promotion. |
| `fencing` | Optional stronger isolation | Site-specific external fence/status command for BMC, PDU, cloud, or hypervisor isolation. |
| `mysql.semi_sync_required` | Recommended for production MySQL | Requires current semi-sync source acknowledgement and replica readiness evidence during discovery, candidate selection, precheck, and post-operation verification. Missing evidence blocks promotion. |

In a multi-controller deployment, `http_address` must listen on an address
reachable by the configured peer API addresses; a loopback-only listener cannot
receive follower-to-Leader mutation RPCs.

The JSON file contains environment-variable names only. Set secrets in the
service environment:

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
export CG_POSTGRESQL_DISCOVERY_PASSWORD='replace-with-the-pg-monitor-secret'
export CG_POSTGRESQL_OPERATION_PASSWORD='replace-with-the-pg-operation-secret'
export CG_POSTGRESQL_REPLICATION_PASSWORD='replace-with-the-pg-replication-secret'
export CG_ORACLE_DISCOVERY_PASSWORD='replace-with-the-dedicated-sysdg-secret'
export CG_ORACLE_OPERATION_PASSWORD='replace-with-the-dedicated-sysdg-secret'
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

The server rejects startup when an enabled engine lacks its required username,
`password_env`, or resolved password. MySQL and PostgreSQL passwords are passed
to their client processes through environment variables and are not placed in
command-line arguments, API payloads, or persisted metadata.

Production layout:

```text
/etc/clusterguard/clusterguard.json
/etc/clusterguard/clusterguard.env
/var/lib/clusterguard/metadata.json
/var/lib/clusterguard/admin-recovery.json  # one-time, normally absent
/var/log/clusterguard/
/usr/local/bin/clusterguard
/usr/local/bin/cgctl
```

### Platform Login

On an empty metadata store, the Leader creates one bootstrap administrator:

```text
username: admin
temporary password: admin123
role: admin
MustChangePassword: true
```

The first login succeeds only far enough to change the password. Every other
platform API returns `password_change_required` until that first password change
completes. The password is stored as an Argon2id hash in the replicated
metadata snapshot; plaintext is never written to configuration, environment
files, audit events, or reports.

Browser sessions have an eight-hour absolute lifetime. The session secret is
kept in an HttpOnly SameSite cookie, mutating requests require the matching
`clusterguard_csrf` cookie and `X-CSRF-Token` header, and password change or
logout revokes the session. Roles are:

| Role | Access |
| --- | --- |
| `admin` | Full platform access, including metadata and node lifecycle. |
| `operator` | Read access plus guarded database operations and discovery refresh. |
| `viewer` | Read-only access. |

The console never asks for a control token, lifecycle token, or one-time
approval token. A logged-in platform operation creates and consumes its
plan-bound one-time authorization inside the server.

The destructive matrix can exercise the same session path:

```bash
export CG_PLATFORM_USERNAME='admin'
export CG_PLATFORM_PASSWORD='<current-platform-password>'

scripts/clusterguard-ha-matrix.sh \
  --api http://127.0.0.1:8088 \
  --clusters '<cluster-uuid>' \
  --platform-session
```

For a fresh metadata store, also set
`CG_PLATFORM_NEW_PASSWORD='<replacement-password>'`. The matrix changes the
bootstrap password, logs in again, and then executes without an
`approval_token` field. Without `--platform-session`, the matrix retains the
explicit one-time grant path used to validate service-client replay rejection.

Password-loss recovery is deliberately local, one-time, and fail-closed. There
is no remote reset endpoint. First pause control-plane mutations and back up the
current metadata and Raft directories. On one controller, generate an artifact
as the service account:

```bash
sudo -u clusterguard /usr/local/bin/clusterguard admin prepare-recovery
```

The command prints a strong temporary password exactly once and writes only its
Argon2id hash to `/var/lib/clusterguard/admin-recovery.json`. Copy the exact same
artifact to every controller and install it as
`clusterguard:clusterguard` with mode `0600`. Restart the controller services
one at a time so quorum remains available. A controller reads an artifact only
when it was present at process startup; the current Raft Leader commits the
recovery ID once, revokes all administrator sessions, requires a password
change, writes a security event, and every controller then deletes its local
artifact. The artifact expires after 24 hours.

Log in as `admin` with the temporary password and replace it immediately. Do
not create a shared default password, delete `PlatformUser` records, edit password hashes, or
reset only one controller's metadata snapshot. If the one-time artifact cannot
be committed with quorum, restore a protected metadata backup through the
offline disaster-recovery procedure.

Use `packaging/systemd/clusterguard-ha.service` and
`packaging/systemd/clusterguard.env.example` as the service templates. The
server binary defaults to `/etc/clusterguard/clusterguard.json` when `--config`
is omitted. Validate the exact production file before restarting a controller:

```bash
/usr/local/bin/clusterguard \
  --config /etc/clusterguard/clusterguard.json \
  --check-config
```

The packaged systemd unit runs this validation as `ExecStartPre`; an invalid
configuration therefore fails before the serving process replaces a healthy
controller.

When upgrading an existing Raft controller set to a release that supports
snapshot content compare-and-swap, do not enable the protocol during a mixed
version rollout. Pause metadata mutations and periodic reconcilers, replace
the binary on every controller, then set `consensus.snapshot_cas_enabled` to
`true` on every controller and restart the full controller set. A new binary
with the key missing or set to `false` remains readable but rejects replicated
metadata mutation. Never enable the key while an older controller can still
become leader.

Use the same two-stage rollout for
`consensus.replicated_log_compression_enabled`: first replace the binary on
every voter while the key remains `false`, then enable the key on every
controller and restart followers before the current Leader. New controllers
continue to read uncompressed entries, but an older binary cannot read a newly
compressed entry. Raft stores larger than 256 MiB are compacted atomically
before they are opened at startup; keep enough free disk space for the compact
copy during the first rolling restart.

Controller state is capped at 16 MiB on disk, in Raft log entries, and in Raft
snapshots. Before upgrading, verify that the protected metadata file is below
that limit and back it up. Audit, report, session, approval, operation,
lifecycle, and security-event history is bounded automatically; export records
to an external retention system when longer history is required.

For Raft mTLS rollout, install the private CA and per-controller certificates
on every controller before changing configuration. Certificates must include
the advertised peer IP or DNS name and both client and server usages. Restart
one controller at a time and verify a writable majority after every restart.
Do not rotate the CA and all controller identities in one unverified step.

Build one self-verifying Linux bundle, then run the installer in its default
read-only preflight mode before permitting mutation:

```bash
./scripts/build-clusterguard-bundle.sh --output ./dist --version 1.0.0
tar -xzf ./dist/clusterguard-ha-1.0.0-linux-amd64.tar.gz
cd ./clusterguard-ha-1.0.0-linux-amd64
./scripts/clusterguard-install.sh \
  --bundle-dir "$PWD" --role mixed \
  --node-name cg-node-0001 --node-id <platform-node-uuid> \
  --config /secure/input/clusterguard.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent.json \
  --assets-dir /secure/input/assets
```

After reviewing the plan, repeat the command with `--execute`. Runtime assets
use an allowlisted layout: `tls/*.crt`, `tls/*.key`, `ssh/*_ed25519`,
`ssh/*known_hosts`, and `mysql/*-client.cnf`. The installer rejects symlinks,
unknown asset types, bad checksums, invalid JSON, and mutable node names before
changing the host. It installs service keys group-readable only where the
unprivileged controller requires them, while MySQL client credentials remain
root-only for the restricted data-node agent.

For data and mixed nodes, installation starts the restricted agent but keeps
the periodic VIP reconciler disabled. Register the cluster HA endpoint, prove
that its canonical owner is the current writable primary, establish the
majority ownership lease, and complete one successful reconcile on every
node. Only then repeat the installer with `--activate-agent-reconcile
--execute`, or enable the timer through the lifecycle workflow. This prevents
a partially configured deployment from changing MySQL role state during
bootstrap.

Browser `/api/v1/` requests require a valid platform session; mutating requests
also require CSRF and role authorization. External service clients use
`Authorization: Bearer <control-token>`. Explicit service database execution
also requires its matching one-time approval grant and deliberately does not
accept the control credential as an approval substitute. Missing or invalid
credentials fail before database access. Keep the default loopback listener for
local operation. Before exposing the API on another interface, enable TLS and
network access controls; never transmit credentials over plain HTTP.

## 2. Inspect Engines and Capabilities

```bash
curl -sS http://127.0.0.1:8088/api/v1/engines
curl -sS http://127.0.0.1:8088/api/v1/capabilities
```

Both routes list the registered engines and the explicit availability,
mutation flag, and reason for each capability.

## 3. Register Authoritative Inventory

Register each physical host once with a fixed globally unique `node_name` before
attaching database endpoints. The platform returns an immutable `resource_id`:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/nodes \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "resource_id":"<preallocated-platform-node-uuid>",
    "node_name":"cg-data-0001",
    "display_name":"MySQL host 1",
    "hostname":"mysql-a",
    "ip_address":"192.0.2.10",
    "kind":"data",
    "active":true
  }'
```

`resource_id` may be preallocated by the signed installation manifest; when it
is omitted, the control plane generates it. `node_name` and `resource_id` never
change. A hostname or IP change uses
`PUT /api/v1/nodes/{resource_id}` with the same `node_name`; the previous
coordinates become aliases. Rebuild requests must supply the original node UUID
and fixed name, so a repaired host reuses the existing slot instead of appearing
as an extra node.

Register the cluster display name, engine, and every database endpoint that the
controller is allowed to probe:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-mysql",
    "engine":"mysql",
    "endpoints":[
      {"hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},
      {"hostname":"mysql-b","ip_address":"192.0.2.11","port":3306},
      {"hostname":"mysql-c","ip_address":"192.0.2.12","port":3306}
    ]
  }'
```

The response returns the new cluster platform UUID and endpoint UUIDs. Cluster
display names are unique, endpoint coordinates cannot conflict with existing
inventory, and at least one endpoint is required.

List and inspect inventory:

```bash
curl -sS http://127.0.0.1:8088/api/v1/clusters
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>
```

The cluster detail route returns `cluster`, `instances`, and `endpoints`.

Administrators can perform the same registration from **Cluster Management**
beside the console cluster selector. The focused dialog registers one or more
discovery endpoints, immediately requests a discovery refresh, and selects the
new cluster. Database credentials remain server-side and are never collected
by the browser.

The dialog's **Retire Cluster** tab removes a cluster from active ClusterGuard
management after the administrator types its exact display name. Retirement
does not stop a database, delete database data, or mutate an operating-system
VIP. It removes live inventory and coordination state while preserving audit,
report, operation, and completed lifecycle history. A running database
operation, unexpired operation lock, active node lifecycle task, or any active
HA ownership lease blocks retirement. Before retiring a cluster with managed
HA endpoints, stop its agent reconciliation and wait for the ownership lease
to expire. This keeps retirement from indirectly triggering host-side VIP
changes after the inventory disappears.

The equivalent service API is:

```bash
curl -sS -X DELETE \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid> \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{"confirm_display_name":"payments-mysql"}'
```

## 4. Refresh MySQL Topology

Refresh only the registered inventory. Send exactly an empty JSON object:

```bash
curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

Do not send credentials or endpoint coordinates. The server rejects such
overrides and resolves MySQL credentials from configuration. A refresh probes
all active database endpoints and atomically commits instances, links, probe
evidence, health, anomalies, and metrics.

Concurrent refreshes are serialized per cluster. The commit requires both:

- an observation timestamp newer than the durable cluster watermark;
- the exact inventory generation captured before probing.

An equal/older observation or an inventory change during an in-flight refresh
returns `409` and publishes no partial result.

## 5. Check Control-Plane Health and Readiness

ClusterGuard exposes two minimal, unauthenticated probes for service managers
and load balancers:

```bash
curl -fsS http://127.0.0.1:8088/healthz
curl -fsS http://127.0.0.1:8088/readyz
```

`/healthz` proves only that the HTTP process is alive. `/readyz` returns HTTP
`503` when a Raft controller has no known Leader, a Leader cannot confirm
quorum, a follower cannot identify the trusted Leader API, or local metadata is
still catching up. Database health does not change either control-plane probe.
Both routes support `HEAD` and intentionally omit controller addresses,
resource IDs, counters, and configuration details.

Authenticated operators can inspect the full state through the console
Settings page, the API, or `cgctl`:

```bash
cgctl --server http://127.0.0.1:8088 status
cgctl --server http://127.0.0.1:8088 --json status
curl -sS http://127.0.0.1:8088/api/v1/control-plane/status \
  -H 'Cookie: clusterguard_session=<session>'
```

The detailed response includes the local role, Leader identity and address,
voter count, quorum and mutation authority, Raft indexes, durable metadata
revision, uptime, active operations, indeterminate operations, and active node
lifecycle tasks. Every HTTP response carries `X-Request-ID`; a caller-supplied
safe ID is preserved across follower-to-Leader forwarding and the same value is
included in JSON error envelopes.

`active_operations` counts only records whose status is `running`. A durable
`planned` record is historical work waiting for an explicit execution request;
it does not consume the active-operation limit and does not make readiness look
busy. `indeterminate_operations` counts only `indeterminate` records that have
not been reviewed. A reviewed record keeps its original status and evidence but
does not permanently inflate the current review-required counter.

Background ownership and automatic-recovery loops report a new failure
immediately. An unchanged failure is then suppressed and reminded every five
minutes; a changed failure is reported immediately, and one successful cycle
resets the suppression state. A retry-backoff cycle for the same stable
incident is not treated as success, so the 30-second safety retry remains
active without producing the same journal entry every 30 seconds. The reminder
state resets only after the incident clears. This limits journal noise without
hiding a persistent incident or delaying a recurrence after recovery.

## 6. Read Topology, Health, Candidates, and Metrics

```bash
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/topology
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/health
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/candidates
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics/prometheus
curl -sS http://127.0.0.1:8088/api/v1/monitoring/prometheus \
  -H "Authorization: Bearer ${CG_MONITORING_TOKEN}"
```

The candidate route accepts optional bounded policy values:

```text
?maximum_lag_seconds=10&require_gtid=true
```

Candidate evaluation requires exactly one current primary and complete probe
coverage. It checks reachability evidence, cluster membership, role,
maintenance, promotion eligibility, IO/SQL replication threads, source
identity, lag, GTID mode and consistency, errant and missing transactions, data
loss risk, and MySQL version family. It returns rankings only; it cannot promote
a node.

Snapshot-derived reads accept `observation_id=<RFC3339 timestamp>`. The web
console reads topology first and pins health, candidates, and metrics to that
same observation. If a concurrent refresh changes the snapshot, the complete
console read is retried instead of combining evidence from different cycles.

The MySQL read-only probe and metrics path is covered by 5.7, 8.0, 8.4, and 9.7
fixtures. Replication collection handles legacy and current terminology.

The per-cluster Prometheus endpoint exports database metrics for one cluster.
The protected `/api/v1/monitoring/prometheus` fleet endpoint exports all
cluster alert and database series plus the local control-plane state. Metric
names include:

```text
clusterguard_mysql_qps
clusterguard_mysql_tps
clusterguard_mysql_slow_queries_per_second
clusterguard_mysql_connections
clusterguard_mysql_running_threads
clusterguard_mysql_buffer_pool_hit_ratio
clusterguard_mysql_replication_lag_seconds
clusterguard_control_plane_ready
clusterguard_control_plane_leader
clusterguard_control_plane_quorum_confirmed
clusterguard_control_plane_metadata_revision
clusterguard_control_plane_operations
clusterguard_control_plane_lifecycle_tasks
```

Alert immediately when `clusterguard_control_plane_ready` is `0`. A Leader with
`clusterguard_control_plane_quorum_confirmed == 0` is not allowed to mutate
metadata. Track metadata revision per controller and investigate a follower
that does not converge. Active and indeterminate operation gauges distinguish
normal work from an operation that requires human review.

Database series are labeled with stable platform `cluster_id` and
`instance_id` values. Control-plane gauges are intentionally local to the
scraped controller and carry no mutable hostname label; assign the controller
identity in the Prometheus scrape target configuration. No external exporter,
monitoring agent, or metrics database is required by the ClusterGuard HA
runtime.

The console operation log loads 20 events initially and adds 20 events per
request when the operator selects **Load more**. Repeated blocked retries for
one automatic-recovery incident appear as one incident with an attempt count;
the durable operation records remain separate and auditable. Manual operations
and different incidents are never consolidated. Raw request and response data
is collapsed by default and an incident opens on its newest attempt. Durable
snapshots keep the newest 128 planned operations, 512 terminal operations,
1,024 audit events, 512 reports, and 2,048 security events. Export records
before those bounds when policy requires a longer audit-retention period.

For an immutable external archive, fetch the current audit window as NDJSON
with an authenticated control session or service bearer before it reaches the
bound:

```bash
curl --fail --silent --show-error \
  -H "Authorization: Bearer $CG_CONTROL_TOKEN" \
  https://controller.example:3000/api/v1/audits/export \
  >> /secure/archive/clusterguard-audit.ndjson
```

The exporter is read-only and never returns credentials, approval secrets, or
session tokens. The retention cap remains intentional protection for the
replicated control-state size; long-term retention belongs in a dedicated,
append-only archive.

## 7. Use `cgctl`

`cgctl` defaults to `http://127.0.0.1:8088` and prints concise human-readable
output:

```bash
go run ./cmd/cgctl status
go run ./cmd/cgctl engines
go run ./cmd/cgctl clusters
go run ./cmd/cgctl topology <cluster-uuid>
go run ./cmd/cgctl health <cluster-uuid>
go run ./cmd/cgctl candidates <cluster-uuid>
go run ./cmd/cgctl metrics <cluster-uuid>
go run ./cmd/cgctl refresh <cluster-uuid>
go run ./cmd/cgctl operation <operation-uuid>
go run ./cmd/cgctl approval issue --cluster <cluster-uuid> --engine mysql \
  --kind switchover --target <instance-uuid> --issued-by <administrator> --ttl 5m
go run ./cmd/cgctl approval list
go run ./cmd/cgctl approval show <grant-uuid>
```

Global flags must appear before the command:

```bash
go run ./cmd/cgctl --json clusters
go run ./cmd/cgctl --json candidates <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 topology <cluster-uuid>
```

`cgctl` attaches the configured control credential to authenticated reads and
writes. `refresh` sends `POST` with the exact body `{}`. Approval issuance builds the
durable plan and returns the plaintext grant once. Both commands read the
administrator Bearer credential from `CG_CONTROL_TOKEN`; select another
environment variable with `--token-env <name>` before the command. Approval
list/show never expose the persisted token hash. The console uses its platform
session and never receives either credential.

## 8. Prepare A Guarded MySQL Switchover

### Browser Console

An authenticated `admin` or `operator` selects the candidate, unlocks the local
anti-mistake control, and clicks execute. The request contains cluster, engine,
operation kind, target UUID, idempotency key, and the logged-in username. It
contains no approval token. The server persists the plan, issues a one-time
grant internally, consumes it under the operation lock, and returns only the
operation result.

### External Service API

An administrator first selects the exact cluster and candidate and asks
ClusterGuard to build the durable plan and issue a single-use grant:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/approvals \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "cluster_id":"<cluster-uuid>",
    "engine":"mysql",
    "operation_kind":"switchover",
    "target_id":"<candidate-instance-uuid>",
    "issued_by":"platform-admin",
    "ttl_seconds":300,
    "idempotency_key":"change-20260712-001"
  }'
```

The response contains `result.operation`, sanitized `result.grant` metadata,
and `result.approval_token`. The plaintext token is returned once. ClusterGuard
stores only its SHA-256 hash and binds the grant to the operation UUID,
cluster, engine, kind, candidate UUID, topology observation, and plan digest.
The default TTL is five minutes and the maximum is fifteen minutes.

The target is an inventory UUID. Caller-supplied primary/target hostname, IP,
or port parameters are rejected. The issuance route has already completed
precheck and planning. Review the returned operation or retrieve it by UUID:

```bash
curl -sS http://127.0.0.1:8088/api/v1/operations/<operation-uuid>
go run ./cmd/cgctl operation <operation-uuid>
```

The guarded release supports one primary and multiple inventory replicas. Checks
require one healthy writable primary, a selected eligible replica, bounded lag,
running IO/SQL threads, GTID mode `ON`, compatible GTID history, binary logging,
current probe coverage, compatible MySQL release families, and a verified
writer-endpoint provider. The immutable plan includes every follower that must
be reparented.

The operation resource response also includes `timeline.audits` and
`timeline.reports`, filtered by the immutable operation UUID.

The DBA consumes the grant without sending the administrator Bearer credential:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/execute \
  -H 'content-type: application/json' \
  -d '{"approval_token":"cgag_<grant-uuid>.<secret>"}'
```

Grant consumption and the durable `APPROVE` transition are atomic under the
operation lock. The token cannot be reused. Expired, consumed, mismatched, or
stale-plan grants are blocked and require a newly issued grant. The web console
never receives the token and relocks the selected target after every attempt.

When Agent, Raft, HA endpoint inventory, or operation credentials are absent,
ClusterGuard HA persists a blocked or unsupported result before issuing a
mutating SQL statement. With those dependencies configured, execution fences
the source, promotes the selected target, reparents reachable followers,
transfers the VIP, verifies postconditions, and writes the audit/report timeline.

Replication statement selection uses `STOP/RESET SLAVE` through MySQL 8.0.21
and `STOP/RESET REPLICA` from MySQL 8.0.22 onward. Before any write, the kernel
re-probes source and target identity, roles, GTID history, replication threads,
lag, binary logging, and release compatibility under the operation lock.

## 9. Reconcile Mutable Metadata

Platform UUID and native engine identity are immutable. For MySQL, native
identity is `engine_identity.server_uuid`. Hostname, IP address, port, display
name, and aliases are mutable coordinates.

When a known MySQL server moves:

1. Read the current instance and endpoint UUIDs from the cluster detail route.
2. Submit the same platform instance UUID, cluster UUID, engine, and
   `server_uuid` with the new coordinates.
3. Include `endpoint_id` when the instance owns more than one database endpoint.
4. Run metadata `precheck`, `plan`, `execute`, then `verify` as required by the
   operator workflow.

Routes:

```text
POST /api/v1/metadata/reconcile/precheck
POST /api/v1/metadata/reconcile/plan
POST /api/v1/metadata/reconcile/execute
POST /api/v1/metadata/reconcile/verify
GET  /api/v1/metadata/anomalies
```

Reconciliation never changes the MySQL server. It validates the immutable
native identity, updates the selected endpoint and canonical coordinates in one
repository transaction, retains previous coordinates as aliases, increments
metadata revision and inventory generation, invalidates the old topology, and
requires a new refresh.

An observed known `server_uuid` automatically binds to its existing platform
resource UUID. A changed hostname, IP, or port must not create a duplicate
resource. Native-identity mismatches, duplicate endpoint ownership, cross-
cluster updates, and ambiguous endpoint selection are blocked.

## 10. Built-in Node Installation and Synchronization

The Console **Nodes** view drives one ClusterGuard lifecycle for adding a new
node and rebuilding a physically replaced node. This is a control-plane
workflow, not a collection of operator-run remote commands. It performs:

1. inventory, immutable node UUID, SSH host-key, privilege, port, and package
   preflight;
2. SHA-256 verified package selection from the approved offline repository;
3. MySQL or PostgreSQL installation, or reconciliation of a registered
   existing installation;
4. version-aware data copy and replication configuration;
5. native identity, read-only state, replication, lag, and VIP-absence
   verification; and
6. Raft-backed metadata commit, audit, and report generation.

MySQL chooses an allowed clone, xtrabackup, or logical-dump path. PostgreSQL
uses `pg_rewind` when its safety preconditions hold and otherwise uses
`pg_basebackup`. A task exposes separate preflight, install, synchronize,
configure-replication, verify, and metadata-commit states. No metadata is
committed after a failed or indeterminate target mutation.

Data-node cardinality is unrestricted. Controller membership must remain an
odd set of at least three. A 3-to-5 expansion therefore accepts two distinct
controller targets in one guarded operation, installs the controller and
adapter runtime on both, issues unique API and Raft mTLS identities, and asks
the current Leader to add both voters. A failed paired install stops newly
staged controller roles in reverse order. A partial Raft addition removes
voters added by that call before returning the failure.

For dynamic controller enrollment, deliver all four issuer files in the
offline runtime assets directory:

```text
assets/pki/api-issuer.crt       0644
assets/pki/api-issuer.key       0640 root:clusterguard
assets/pki/raft-issuer.crt      0644
assets/pki/raft-issuer.key      0640 root:clusterguard
```

The API issuer must chain to `tls_ca_file`; the Raft issuer must chain to
`consensus.tls_ca_file`. Configure all four issuer paths together. Issuer keys
must never be returned through the API, browser, audit stream, task log, or
report.

## 11. Automatic Failover and Safety Boundary

The raw configuration-loader default remains disabled so that an incomplete
hand-written configuration cannot silently become destructive. The supported
multi-node installer defaults a VIP-backed MySQL HA deployment to automatic
failover with `fencing.agent_quorum_enabled=true`. Operators may explicitly
choose `--manual-failover-only`.
Automatic failover requires Raft consensus, the restricted node agent, an
active VIP resource, all three purpose-specific MySQL credentials, stable
database failure evidence, and verified old-primary isolation. It does not
require a human approval token. Startup rejects incomplete consensus or fencing
configuration.

Agent quorum fencing does not equate TCP failure with isolation. Each Agent
receives a leader-signed decision valid for at most 10 seconds and reconciles
every 5 seconds. After publishing the exact failover transition lease, the
controller waits at least 15 seconds, revalidates majority authority, stable
failure evidence, and the unchanged lease, and rejects promotion if the old
primary is reachable but still owns the VIP or remains writable. A node that
cannot obtain majority authorization releases the VIP and persists read-only
intent locally. `--fencer FILE` remains available as an optional stronger layer
when the Agent or whole operating system may fail while the database can still
serve traffic.

For production MySQL, set `mysql.semi_sync_required=true` on every controller
only after all managed instances load and enable the source/replica semi-sync
plugins. ClusterGuard then treats semi-sync as required evidence: the current
primary must have at least the configured number of acknowledging clients, a
candidate must be actively acknowledging its source and already have
promotion-side source settings, and the promoted primary must regain an active
acknowledging client during verification. Unknown, malformed, disabled, or
stale evidence fails closed. The managed MySQL installer configures one
acknowledging replica with `AFTER_SYNC` and a 10-second source timeout for
MySQL 8.x (and the equivalent master/slave names for 5.7).

Semi-sync substantially reduces acknowledged-transaction loss but does not by
itself prove strict RPO zero: after its configured timeout MySQL may fall back
to asynchronous commits, and storage, operating-system, and network failures
remain outside the database acknowledgement protocol. Production SLOs must
therefore document the timeout/fallback policy and validate it with client-side
transaction IDs during destructive failover tests.

The recovery controller executes only on the majority Leader. Discovery records
one incident after four current failed-primary samples span at least three seconds. The
controller chooses only the rank-one eligible candidate and submits a normal
durable `failover` operation through a private internal authorization path.
Public JSON cannot select this mode. The incident ID is audited at `APPROVE`;
Safety Guard, lock, fencing, execution, verification, audit, and report remain
mandatory. Incident-derived idempotency keys prevent a Leader change from
repeating a successful or indeterminate failover. Blocked attempts wait at
least the configured retry period.

The operation lock is stored in the Raft-replicated metadata snapshot and
renewed while its holder remains the majority Leader. The Safety Guard checks
majority again before lock and approval. VIP ownership is separately protected
by a short exclusive endpoint lease and cluster-wide owner verification.

### External fencing contract

Set `fencing.enabled=true` only after installing an absolute, regular,
executable provider path. Startup fails when the provider is missing or is not
executable. ClusterGuard invokes it with exactly one argument, either `fence`
or `status`, and writes one JSON object to stdin:

```json
{
  "cluster_id": "<cluster-uuid>",
  "operation_id": "<operation-uuid>",
  "lease_id": "<lease-uuid-when-fencing>",
  "instance": {
    "resource_id": "<old-primary-uuid>",
    "hostname": "mysql-01",
    "ip_address": "192.0.2.10",
    "port": 3306
  }
}
```

The provider must emit one JSON object and nothing else on stdout:

```json
{"status":"ok","fenced":true,"message":"power isolation confirmed"}
```

Logs belong on stderr. Output is capped at 64 KiB and each call is bounded by
`fencing.timeout_seconds`. A successful `fence` call is not sufficient:
ClusterGuard immediately calls `status` and proceeds only when that independent
call also returns `fenced:true`. Any timeout, malformed output, unknown field,
process failure, or ambiguous status blocks promotion. The provider should key
status by the immutable instance resource UUID and verify a real out-of-band
mechanism such as a hypervisor, cloud, PDU, or BMC; network reachability alone
is not fencing.

The provider does not inherit database passwords, control tokens, or the full
service environment. Put provider-specific credentials in variables prefixed
with `CG_FENCER_`; only that prefix plus `PATH`, locale, and timezone variables
is passed to the child process. Prefer a root-owned credential file when the
provider supports one.

Oracle and SQL Server mutation is available only through their native HA
control planes. Oracle role transition uses Data Guard Broker through DGMGRL.
SQL Server planned role transition uses Always On availability-group failover
through sqlcmd/T-SQL. A MySQL, PostgreSQL, Oracle, or SQL Server action whose
required runner, Agent, endpoint, identity, topology, quorum, fencing,
credential, lock, approval, or verification evidence is missing is blocked
before the unsafe step. Unsupported or blocked never means partially successful.

The guarded kernel pins the exact topology observation used for precheck as
`cluster_id@observed_at` and revalidates it after acquiring the operation lock.
Discovery publication uses that same cluster lock, so it cannot replace the
validated observation during execution. A changed or invalidated observation
blocks approval and execution. Completed steps are durable. An already-fenced
source is accepted only when this operation owns the durable `fence_source`
step; recovery then revalidates immutable source and target identities, fencing,
GTID history, binary logging, release compatibility, replication state, and
endpoint ownership before another mutation. If a journal write fails before
mutation, the workflow stops. If it fails after the mutation commit point,
verification still runs and the API returns an `indeterminate` execution with
HTTP `500`. An atomic metadata rename followed by a directory-sync warning is
also reported as committed but `indeterminate`, including the reconciled
instance and endpoint in the response. Treat `indeterminate` as a manual-review
state; do not automatically retry the operation.

Post-commit verification uses a bounded context detached from the caller. A
failed verification is persisted with its checks and remains `indeterminate`.
Verification keeps the immutable plan digest and source/target UUID scope but
accepts a newer topology observation and metadata revisions after promotion;
the refreshed endpoints must still return the planned native identities and
the expected live roles.
Calling the operation `verify` action again may reconcile that record to
`succeeded` only when explicit verification evidence passes; all other terminal
states remain immutable. The terminal operation record, terminal audit events,
and operation report are published in one repository snapshot, so readers
cannot observe a succeeded report with a running operation or the reverse.
Manual verification reconciliation uses the same atomic finalization path.

When later topology changes make the immutable historical plan impossible to
verify, an operator may call `POST /api/v1/operations/{operation_uuid}/review`
with `{"note":"site verification evidence"}`. The route accepts only
`indeterminate` records, requires a non-empty note, and makes the first review
immutable. It atomically stores review metadata, an audit event, and a report
without changing the operation status. The console's **Mark reviewed** action
uses the same route.

Cluster registration and discovery publication follow the same rule. A
post-rename durability warning returns HTTP `500` plus the committed resource or
observation in `result`. Reconcile the returned cluster/endpoint UUIDs or the
observation token `cluster_id@observed_at` before retrying; creating another
cluster or publishing as though the observation were absent can duplicate user
intent. Non-operation metadata workflows first persist a conservative report
fallback and then replace it with the terminal outcome under the same report
UUID.

MySQL multi-source replication is detected but not modeled in this phase. If
`SHOW REPLICA STATUS` or its legacy equivalent returns more than one channel,
discovery fails closed and does not publish partial health, topology, or
promotion eligibility.

## 12. Planned Shutdown and Automatic Recovery

Use this for maintenance windows, rack moves, and full power-down of a MySQL
primary/replica cluster. The platform applies protection before shutdown and
systemd units restore the cluster automatically after boot, without operator
intervention.

### 12.1 Shutdown modes

| Mode | Behavior | Use case |
|------|----------|----------|
| `service` | Stops MySQL only (replicas first, primary last); hosts stay up | Software upgrade, config change, short maintenance window |
| `poweroff` | Power-off every node in parallel | Rack power maintenance, relocation |

### 12.2 Initiating shutdown

Use **Topology -> Power lifecycle -> One-click shutdown** in the Web console.
The default `service` mode stops the database but leaves the hosts running. A
logged-in platform administrator receives a short-lived, single-use approval
inside the server; no approval secret is exposed to the browser.

CLI automation uses the same API workflow and requires an explicitly issued
one-time approval token:

```bash
cgctl cluster shutdown --cluster <display name> --mode service|poweroff \
  --approval-token <single-use-token>
cgctl cluster shutdown --cluster <display name> --mode service --dry-run
```

`--dry-run` performs precheck and immediately cancels the temporary lifecycle;
it changes no database or host state. There is no direct-shell bypass.

The full flow (any failure interrupts and keeps protection, see 11.4):

1. Refresh topology (`discover`) and have every signed Agent atomically persist
   its cluster snapshot at `/etc/clusterguard/power-snapshots/<cluster-uuid>.json`
   (directory 0700, file 0600). Multiple clusters on one host cannot overwrite
   each other.
2. Freeze automatic recovery (`recovery-freeze`): automatic failover, reboot
   bootstrap, and manual switchover are all blocked.
3. Mark every instance in maintenance.
4. Run `SET PERSIST_ONLY read_only=ON; SET PERSIST_ONLY super_read_only=ON`
   on every node — no runtime effect, but durable across reboot so the
   restored cluster cannot accept stray writes.
5. Stop per mode: `service` stops replicas then the primary; `poweroff`
   powers off all nodes in parallel.

### 12.3 Automatic restore after reboot

Both units ship enabled. They scan the per-cluster snapshot directory at boot
and are no-ops when it is empty:

- `clusterguard-cluster-restore.service` — first confirms that the live control
  plane still records an active planned-recovery state, starts the local
  database service, and waits for readiness. For MySQL, only the immutable-ID
  designated primary clears persisted and runtime read-only flags. It then
  reports boot/recovery and triggers discovery. A stale snapshot on an ordinary
  reboot is rejected before any role mutation.
- `clusterguard-cluster-finalize.service` — waits for control-plane health
  (`/healthz`, up to 60 s), then polls the topology until the primary instance
  reports healthy (every 5 s, up to 600 s); on success it unfreezes automatic
  recovery, clears cluster maintenance protection, and writes `recovered_at`
  to that cluster's snapshot.

`poweroff` cannot turn a physically powered-off server back on by itself.
Automatic power-on requires VMware autostart/API, IPMI/iDRAC/iLO, Wake-on-LAN,
or firmware restore-on-AC. Once the OS boots, ClusterGuard recovery is automatic.

### 12.4 Timeout and failure fallback (fail-closed)

- If finalize times out with the primary still unhealthy, protection is
  **not** released: the script logs CRITICAL, exits cleanly, and waits for an
  operator.
- After the problem is confirmed resolved, release protection manually:

```bash
curl -sk -X POST -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"freeze":false}' https://127.0.0.1:3000/api/v1/clusters/<cluster-uuid>/recovery-freeze
```

- Deleting the snapshot makes both units no-ops; re-running restore/finalize
  against an already-finalized snapshot (`recovered_at` present) is also an
  idempotent no-op.

### 12.5 Inspecting restore state

```bash
cgctl cluster restore-status [--cluster <cluster-uuid>]
```

Prints snapshot presence, cluster identity, `recovered_at`, recovery-freeze
state (`frozen`/`active`), and per-instance role/health/replication
lag/maintenance, plus mode-specific advice. Without `--cluster`, the local
snapshot's cluster UUID is used.

## 13. PostgreSQL HA

PostgreSQL is an engine-native ClusterGuard HA implementation. It provides
identity-safe discovery, primary/standby topology, health, native metrics,
timeline-aware candidate evaluation, controlled switchover, guarded failover,
former-primary rewind/rejoin, allowlisted repair, Linux VIP coupling, and
`pg_basebackup` node synchronization.

Optional automatic failover runs in a PostgreSQL-only recovery controller. It
requires four consecutive primary-failure observations spanning at least
three seconds at the default cadence, a current topology snapshot, a rank-one standby with known
zero replay lag, Raft leader and majority authority, restricted-Agent or
external-fencer proof that the old primary cannot write, and the complete
common workflow through verification, audit, and report. The controller never
uses network unreachability as fencing evidence and never retries an
indeterminate post-promotion result.

The three-second evidence window is separate from the 15-second Agent
authorization-expiry fence and from the configured 30-second retry backoff.
None of those values alone is an application RTO. PostgreSQL clients must use a
bounded connection timeout and retry policy; measure site RTO at the writer
endpoint.

Execution is never inferred from the engine name alone. ClusterGuard advertises
each mutation capability only when dedicated operation and replication
credentials, a restricted signed Agent policy, an executable endpoint provider,
current topology evidence, and the required controller quorum are present.
Failover additionally requires stable failure evidence and successful external
fencing when the old primary cannot prove isolation.

Use [PostgreSQL HA Operations](postgresql-ha.md) for the complete identity SQL,
least-privilege account model, `pg_hba.conf` requirements, controller and Agent
configuration, node lifecycle settings, operation flow, metrics, and destructive
qualification checklist. Unknown lag and optional metrics remain unknown; the
platform never converts missing evidence to a synthetic zero or reports a
simulated success.

## 14. Adapter Roadmap

### MySQL

The independent MySQL path now includes discovery, candidate evaluation,
planned switchover, guarded failover, former-primary rejoin, allowlisted repair,
Linux VIP ownership, self-isolation, staged node lifecycle, delivery packaging,
and destructive three-host acceptance. The laboratory acceptance covers six
clusters, repeated real switchovers, quorum loss, a primary network partition,
former-primary rejoin, a full host reboot, divergent-node rebuild, and mutable
hostname/IP/port reconciliation. See `docs/mysql-feature-parity-acceptance.md`
for the evidence and bundle hashes.

Every MySQL Agent policy used for automatic failover must declare
`mysql_service`, `mysql_server_binary`, and `mysql_server_defaults_file`.
ClusterGuard inspects the local systemd unit and the effective `mysqld
--verbose --help` output before accepting in-band fencing. Both
`read_only=ON` and `super_read_only=ON` must be effective restart defaults on
every managed instance. The Agent records the isolation intent before it tries
the live SQL mutation, so a stopped old primary can be fenced and verified
without making network unreachability a fencing signal. A recovered instance
therefore starts read-only and becomes writable only through a leader-backed
ClusterGuard role transition.

Production deployments must configure and exercise a site-specific out-of-band
provider through the fencing contract above, and qualify a physical-copy method
such as Clone or XtraBackup. The bundled logical
dump rebuild is a destructive fallback: before importing donor data it removes
all non-system schemas on the target so target-only data cannot survive behind
a reset GTID history.

### PostgreSQL

Discovery, stable identity, primary/standby topology, native monitoring,
timeline-aware candidate evaluation, controlled switchover/failover,
promotion/repoint, independent verification, former-primary rewind/rejoin,
allowlisted repair, VIP coupling, and base-backup node lifecycle are implemented.
The next production step is destructive qualification across supported
PostgreSQL release families and site-specific fencing, service, storage, TLS,
backup, and restore layouts; capabilities that have not passed local policy
remain disabled rather than emulated.

### Oracle

ClusterGuard supports Data Guard Broker controlled switchover through the
restricted signed node Agent. The Agent combines local SQLPlus identity
evidence with Broker state, so `DBID + DB_UNIQUE_NAME` stays stable even when a
hostname, IP address, listener endpoint, or role changes. It runs DGMGRL as the
Oracle operating-system account and connects with a dedicated password-file
`SYSDG` user. Do not configure `SYS` for routine platform operation.

Every switchover requires a healthy primary, a broker-healthy standby, zero
transport and apply lag, `Ready for Switchover`, a frozen topology revision,
controller quorum, Safety Guard, a durable operation lock, and a plan-bound
one-time approval. The mutation is accepted only by the source node Agent for
the configured Broker member allowlist. Completion requires independent status
checks on both nodes: the target must be `PRIMARY`, the former primary must be
a standby, Broker status must be `SUCCESS`, and lag must converge to zero.

The controller configuration enables Oracle and references secrets:

```json
{
  "oracle": {
    "enabled": true,
    "discovery_interval_seconds": 15,
    "discovery_timeout_seconds": 15,
    "discovery": {
      "username": "CLUSTERGUARD_DG",
      "database": "DB_UNIQUE_NAME",
      "password_env": "CG_ORACLE_DISCOVERY_PASSWORD"
    },
    "operation": {
      "username": "CLUSTERGUARD_DG",
      "database": "DB_UNIQUE_NAME",
      "password_env": "CG_ORACLE_OPERATION_PASSWORD"
    }
  }
}
```

Each Oracle node uses its own Agent policy with the same platform cluster UUID,
its platform instance UUID, local `DB_UNIQUE_NAME`, connect identifier, Oracle
home, SID, Broker configuration, and complete member allowlist. Store the
database secret only in `CG_ORACLE_BROKER_PASSWORD` in the Agent environment.
The same account and password-file entry must exist on every Broker member.

Failure failover deliberately remains blocked until external old-primary
fencing is configured. ClusterGuard does not edit Oracle data files, archive
logs, or RAC resources directly. Remaining Oracle expansion work is RAC
instance modeling, archive destination checks, listener endpoint correction,
and a destructive acceptance matrix with site-specific fencing rules.

### SQL Server

ClusterGuard supports a guarded Always On planned-failover path for SQL Server.
When `sqlcmd` is present on the controller or a SQL Server runner is injected,
the SQL Server adapter can discover the local Always On replica from AG DMVs,
read synchronization health and send/redo queue metrics, model
primary-to-secondary topology, assess synchronized synchronous-commit
candidates, and precheck, plan, execute, and verify
`ALTER AVAILABILITY GROUP [name] FAILOVER`. The immutable operation plan pins
the AG and replica identities, topology observation, resource revisions, and
plan digest before the common safety, lock, approval, execution, verification,
audit, and report stages.

Execution requires a stable AG `group_id`, replica `replica_id`, healthy
current primary evidence, a healthy promotion-eligible secondary, synchronous
commit, synchronized target state, and a durable operation lease. Verification
polls until it observes exactly one primary, confirms the selected target owns
that role, and confirms every AG replica reports healthy. An asynchronous or
synchronizing replica remains visible and healthy when appropriate, but is not
promotion eligible. Forced failover remains blocked by default because it can
lose data; it needs a separate explicit data-loss approval policy before
execution is allowed.

Remaining SQL Server production work is native Listener endpoint ownership
modeling, WSFC quorum evidence, and site-specific fencing for forced failover.
