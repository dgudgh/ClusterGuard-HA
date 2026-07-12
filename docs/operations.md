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
| `mysql.enabled` | No | Enables server-side MySQL discovery credentials. The adapter stays registered, but discover/refresh fails closed when omitted or `false`. |
| `mysql.username` | When enabled | Dedicated MySQL read-only discovery user. |
| `mysql.password_env` | When enabled | Environment variable containing that user's password. |

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

The first mutating release requires exactly two inventory members and blocks
unknown evidence. Checks require one healthy writable primary, a healthy direct
replica, zero lag, running IO/SQL threads, GTID mode `ON`, identical GTID sets,
binary logging, current complete probe coverage, compatible MySQL release
families, and a real writer-endpoint provider.

Inspect the durable state after a process restart:

```bash
curl -sS http://127.0.0.1:8088/api/v1/operations/<operation-uuid>
go run ./cmd/cgctl operation <operation-uuid>
```

The default runtime deliberately fails the execute action with HTTP `501`:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/execute \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{"approval_token":"<approval-token>"}'
```

This is not a simulated success. No production endpoint provider is installed,
so ClusterGuard HA persists `unsupported` before acquiring a lock or issuing a
mutating SQL statement. Tests inject a deterministic provider and exercise the
complete 5.7/8.x/9.x transition, step persistence, independent verification,
audit, report, failure classification, and idempotent retry.

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

## 9. Current Safety Boundary

Available production operations are read-only observation, durable switchover
precheck/planning, and platform metadata reconciliation. The following actions
are not enabled:

- planned-switchover execution without a real writer-endpoint provider;
- failover, automatic recovery, or former-primary rejoin;
- VIP, listener, service, or writer-endpoint mutation;
- replication repair or source rewiring;
- node installation, synchronization, replacement, or removal.

Requests to operation execute routes for those actions return HTTP `501` with
`status: unsupported`. Node synchronization routes also return `501`.
Capability rejection happens before workflow locks, approvals, or an adapter
mutation call, so unsupported does not mean partially executed.

The guarded kernel pins the exact topology observation used for precheck as
`cluster_id@observed_at` and revalidates it after acquiring the operation lock.
Discovery publication uses that same cluster lock, so it cannot replace the
validated observation during execution. A changed or invalidated observation
blocks approval and execution. Completed steps are durable and retries observe
their postconditions before issuing another mutation. If a journal write fails
before mutation, the workflow stops. If it fails after the mutation commit
point, verification still runs and the API returns an `indeterminate` execution
with HTTP `500`. An atomic metadata rename followed by a directory-sync warning
is also reported as committed but `indeterminate`, including the reconciled
instance and endpoint in the response. Treat `indeterminate` as a manual-review
state; do not automatically retry the operation.

Cluster registration and discovery publication follow the same rule. A
post-rename durability warning returns HTTP `500` plus the committed resource or
observation in `result`. Reconcile the returned cluster/endpoint UUIDs or the
observation token `cluster_id@observed_at` before retrying; creating another
cluster or publishing as though the observation were absent can duplicate user
intent. Reports first persist a conservative fallback and then replace it with
the terminal outcome under the same report UUID.

MySQL multi-source replication is detected but not modeled in this phase. If
`SHOW REPLICA STATUS` or its legacy equivalent returns more than one channel,
discovery fails closed and does not publish partial health, topology, or
promotion eligibility.

## 10. Adapter Roadmap

### MySQL

Next work is the real Linux VIP provider: durable lease, controller leadership,
quorum, fencing token, all-node owner probes, transfer compensation, and
network-partition tests. Only after that provider passes the same workflow may
the tested planned-switchover kernel be enabled in production. Failover,
former-primary rejoin, replica reparenting, repair, and node lifecycle remain
later independently gated projects.

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
