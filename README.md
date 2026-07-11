# ClusterGuard HA

**ClusterGuard HA 多数据库企业级高可用控制平台**

**ClusterGuard HA — Multi-Database High Availability Control Plane**

ClusterGuard HA is an independent, clean-room high-availability control plane.
The current release delivers read-only MySQL topology intelligence and keeps
first-class extension points for PostgreSQL, Oracle, and SQL Server.

## Current Release

The current MySQL adapter provides:

- inventory-scoped discovery for MySQL 5.7, 8.0, 8.4, and 9.7;
- immutable platform UUIDs backed by native MySQL `server_uuid` identity;
- persisted topology, health, replication links, probe evidence, and metrics;
- promotion-candidate assessment with GTID and replication safety checks;
- JSON metrics and a Prometheus text endpoint that can be scraped directly;
- a compact Chinese topology console and the `cgctl` read-only CLI;
- endpoint metadata reconciliation without changing database state.

PostgreSQL, Oracle, and SQL Server adapters are registered and report their
capabilities, but their discovery and execution methods currently fail closed
as unsupported.

## Safety Boundary

This release does **not** execute switchover, failover, VIP or listener
mutation, replication repair, node installation, node synchronization, or node
lifecycle actions. Unsupported execution requests return HTTP `501` before an
adapter can mutate a database.

The common workflow remains:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Only implemented read-only capabilities and platform metadata reconciliation
are available. Future database mutations must pass every workflow gate.

## Start

Create a dedicated MySQL account with only the permissions needed by the
documented read-only queries. Keep secrets in environment variables, never in
the JSON configuration.

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_APPROVAL_TOKEN='replace-with-a-local-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

The console and API are served from `http://127.0.0.1:8088/` by default.

The production binary is `clusterguard`; the CLI is `cgctl`. The distribution
uses `/etc/clusterguard/`, `/var/lib/clusterguard/`, and
`/var/log/clusterguard/`. The systemd unit is
`packaging/systemd/clusterguard-ha.service`.

## Register and Refresh a MySQL Cluster

Registration creates the authoritative endpoint inventory. Hostname, IP, and
port are coordinates, not resource identifiers.

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

Use the returned platform cluster UUID to refresh the registered inventory.
The body is exactly an empty JSON object; credentials and endpoint overrides
are rejected because credentials are resolved on the server.

```bash
curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

## CLI

```bash
go run ./cmd/cgctl engines
go run ./cmd/cgctl clusters
go run ./cmd/cgctl topology <cluster-uuid>
go run ./cmd/cgctl health <cluster-uuid>
go run ./cmd/cgctl candidates <cluster-uuid>
go run ./cmd/cgctl metrics <cluster-uuid>
go run ./cmd/cgctl refresh <cluster-uuid>
```

`refresh` reads the control token from `CG_CONTROL_TOKEN`. Use
`--token-env <name>` before the command to select a different environment
variable; the secret is never accepted as a command-line value.

Place global flags before the command:

```bash
go run ./cmd/cgctl --json topology <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 clusters
```

See [operations.md](docs/operations.md) for the complete API workflow and
operator procedures. See [architecture.md](docs/architecture.md) for resource
identity, adapter, persistence, candidate, and safety design.
