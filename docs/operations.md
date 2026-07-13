# ClusterGuard HA Operations

## 1. Configure the Control Plane

Use `configs/clusterguard.example.json` as the configuration shape. The loader
rejects unknown keys and multiple JSON values.

Implemented keys:

| Key | Required | Meaning |
| --- | --- | --- |
| `http_address` | No | HTTP listen address; blank defaults to `127.0.0.1:8088`. |
| `metadata_path` | Yes | Durable metadata snapshot path. |
| `control_token_env` | No | Environment variable containing the Bearer token for control API `POST` requests. Without it, all control `POST` routes fail closed with `503`. |
| `approval_token_env` | No | Environment variable containing the workflow approval token. |
| `mysql.enabled` | No | Enables server-side MySQL discovery and operation credentials. |
| `mysql.discovery` | When enabled | Dedicated read-only discovery username and password environment reference. |
| `mysql.operation` | When enabled | Dedicated administrative operation username and password environment reference. |
| `mysql.replication` | When enabled | Dedicated replication username and password environment reference. |
| `mysql.automatic_failover_enabled` | No | Enables leader-only automatic failover; defaults to `false`. |
| `mysql.automatic_failover_interval_seconds` | No | Recovery-controller poll interval; defaults to 5 seconds. |
| `mysql.automatic_failover_retry_seconds` | No | Backoff after a blocked or failed incident attempt; defaults to 30 seconds. |
| `consensus` | For real HA mutation | Odd Raft controller membership, persistent state, and majority authority. |
| `agent` | For VIP/fencing mutation | Restricted signed node command transport. |

The JSON file contains environment-variable names only. Set secrets in the
service environment:

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_APPROVAL_TOKEN='replace-with-a-local-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

The server rejects startup when MySQL is enabled but its username,
`password_env`, or resolved password is blank. MySQL passwords are passed to
the client process through its environment and are not placed in command-line
arguments, API payloads, or persisted metadata.

Production layout:

```text
/etc/clusterguard/clusterguard.json
/etc/clusterguard/clusterguard.env
/var/lib/clusterguard/metadata.json
/var/log/clusterguard/
/usr/local/bin/clusterguard
/usr/local/bin/cgctl
```

Use `packaging/systemd/clusterguard-ha.service` and
`packaging/systemd/clusterguard.env.example` as the service templates. The
server binary defaults to `/etc/clusterguard/clusterguard.json` when `--config`
is omitted.

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

Every `/api/v1/` `POST` requires `Authorization: Bearer <control-token>`.
Missing or invalid credentials return `401` before request parsing or database
access. Keep the default loopback listener for local operation. Before exposing
the API on another interface, terminate TLS in a trusted reverse proxy and
apply network access controls; never transmit a control token over plain HTTP.

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

## 5. Read Topology, Health, Candidates, and Metrics

```bash
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/topology
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/health
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/candidates
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics/prometheus
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

The Prometheus text endpoint can be used as a direct scrape target. Exported
metric names include:

```text
clusterguard_mysql_qps
clusterguard_mysql_tps
clusterguard_mysql_slow_queries_per_second
clusterguard_mysql_connections
clusterguard_mysql_running_threads
clusterguard_mysql_buffer_pool_hit_ratio
clusterguard_mysql_replication_lag_seconds
```

Every series is labeled with the stable platform `cluster_id` and
`instance_id`. No external exporter, monitoring agent, or metrics database is
required by the ClusterGuard HA runtime.

## 6. Use `cgctl`

`cgctl` defaults to `http://127.0.0.1:8088` and prints concise human-readable
output:

```bash
go run ./cmd/cgctl engines
go run ./cmd/cgctl clusters
go run ./cmd/cgctl topology <cluster-uuid>
go run ./cmd/cgctl health <cluster-uuid>
go run ./cmd/cgctl candidates <cluster-uuid>
go run ./cmd/cgctl metrics <cluster-uuid>
go run ./cmd/cgctl refresh <cluster-uuid>
go run ./cmd/cgctl operation <operation-uuid>
```

Global flags must appear before the command:

```bash
go run ./cmd/cgctl --json clusters
go run ./cmd/cgctl --json candidates <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 topology <cluster-uuid>
```

`refresh` sends `POST` with the exact body `{}`. The other cluster commands are
read-only `GET` requests. `refresh` reads the Bearer token from
`CG_CONTROL_TOKEN`; select another environment variable with
`--token-env <name>` before the command. Enter the token in the web console's
password field before refreshing; it stays in the current page only and is not
written to browser storage.

## 7. Prepare A Guarded MySQL Switchover

Create one durable operation intent. Use a unique idempotency key generated by
the calling system; retrying the same request returns the same operation UUID.

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "operation":{
      "cluster_id":"<cluster-uuid>",
      "engine":"mysql",
      "kind":"switchover",
      "requested_by":"dba"
    },
    "target_id":"<candidate-instance-uuid>",
    "idempotency_key":"change-20260712-001"
  }'
```

The target is an inventory UUID. Caller-supplied primary/target hostname, IP,
or port parameters are rejected. Run read-only precheck and plan actions on the
returned operation UUID:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/precheck \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" -d '{}'

curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/plan \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" -d '{}'
```

The guarded release supports one primary and multiple inventory replicas. Checks
require one healthy writable primary, a selected eligible replica, bounded lag,
running IO/SQL threads, GTID mode `ON`, compatible GTID history, binary logging,
current probe coverage, compatible MySQL release families, and a verified
writer-endpoint provider. The immutable plan includes every follower that must
be reparented.

Inspect the durable state after a process restart:

```bash
curl -sS http://127.0.0.1:8088/api/v1/operations/<operation-uuid>
go run ./cmd/cgctl operation <operation-uuid>
```

The operation resource response also includes `timeline.audits` and
`timeline.reports`, filtered by the immutable operation UUID. The collection
actions under `/api/v1/operations/precheck|plan|execute|verify` require an
`idempotency_key` and use the same durable UUID workflow; there is no direct
Adapter execution path.

Execute a reviewed operation with the approval token:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/execute \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{"approval_token":"<approval-token>"}'
```

When Agent, Raft, HA endpoint inventory, or operation credentials are absent,
ClusterGuard HA persists a blocked or unsupported result before issuing a
mutating SQL statement. With those dependencies configured, execution fences
the source, promotes the selected target, reparents reachable followers,
transfers the VIP, verifies postconditions, and writes the audit/report timeline.

Replication statement selection uses `STOP/RESET SLAVE` through MySQL 8.0.21
and `STOP/RESET REPLICA` from MySQL 8.0.22 onward. Before any write, the kernel
re-probes source and target identity, roles, GTID history, replication threads,
lag, binary logging, and release compatibility under the operation lock.

## 8. Reconcile Mutable Metadata

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

## 9. Automatic Failover and Safety Boundary

Automatic failover is disabled by default. Enabling it requires Raft consensus,
the restricted node agent, an approval secret, an active VIP resource, and all
three purpose-specific MySQL credentials. Startup rejects an incomplete
automatic-failover configuration.

The recovery controller executes only on the majority Leader. Discovery records
one incident after six follow-up failed-primary samples span 30 seconds. The
controller chooses only the rank-one eligible candidate and submits a normal
durable `failover` operation through every workflow stage. Incident-derived
idempotency keys prevent a Leader change from repeating a successful or
indeterminate failover. Blocked attempts wait at least the configured retry
period.

The operation lock is stored in the Raft-replicated metadata snapshot and
renewed while its holder remains the majority Leader. The Safety Guard checks
majority again before lock and approval. VIP ownership is separately protected
by a short exclusive endpoint lease and cluster-wide owner verification.

PostgreSQL, Oracle, and SQL Server mutation remains unsupported. A MySQL action
whose required Agent, endpoint, identity, topology, quorum, fencing, or approval
evidence is missing is blocked before the unsafe step. Unsupported or blocked
never means partially successful.

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

## 10. Adapter Roadmap

### MySQL

The independent MySQL path now includes discovery, candidate evaluation,
planned switchover, guarded failover, former-primary rejoin, allowlisted repair,
Linux VIP ownership, self-isolation, and staged node lifecycle. Remaining work
is delivery packaging and destructive three-host acceptance, including real
network fencing integration for whole-host partitions.

### PostgreSQL

Implement read-only discovery from `system_identifier`, primary/standby role,
streaming-replication links, timeline and WAL position, lag, and health. Then add
timeline-aware candidate evaluation. Promotion remains unsupported until
quorum/fencing, planning, and verification contracts exist.

### Oracle

Implement read-only database discovery from `DBID` and `DB_UNIQUE_NAME`, model
RAC instances separately, then collect Data Guard role, transport/apply lag,
archive destinations, and broker health. Role transitions remain unsupported
until RAC and Data Guard safety evidence is represented in common plans.

### SQL Server

Implement availability-group discovery from `group_id`, replicas from
`replica_id`, listener endpoints, synchronization state, redo/send queues, and
quorum health. Add candidate planning only after synchronous-commit and quorum
requirements can block unsafe failover.
