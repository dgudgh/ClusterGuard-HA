# ClusterGuard HA

ClusterGuard HA requires Go 1.22 or newer.

**ClusterGuard HA 多数据库企业级高可用控制平台**

**ClusterGuard HA — Multi-Database High Availability Control Plane**

ClusterGuard HA is an independent, clean-room high-availability control plane.
The current release delivers the guarded MySQL control path and keeps
first-class extension points for PostgreSQL, Oracle, and SQL Server.

## Current Release

The current MySQL adapter provides:

- inventory-scoped discovery for MySQL 5.7, 8.0, 8.4, and 9.7;
- globally unique immutable node names, platform UUIDs, and native MySQL
  `server_uuid` identity;
- persisted topology, health, replication links, probe evidence, and metrics;
- promotion-candidate assessment with GTID and replication safety checks;
- guarded three-node planned switchover with primary and VIP ownership coupled;
- former-primary rejoin and allowlisted replication repair;
- 30-second stable-failure detection and optional automatic failover;
- Raft-backed operation locks, endpoint leases, local self-isolation, and
  reboot-time VIP convergence through the restricted node agent;
- add/rebuild lifecycle tasks with fixed node-slot reuse, staged install,
  synchronization, verification, audit, and report output;
- endpoint metadata reconciliation without changing immutable resource identity;
- JSON, Prometheus, and monitoring-safe health output;
- a compact Chinese console and the `cgctl` CLI;
- durable operation UUIDs, idempotency keys, stage progress, audit, and reports;
- tested MySQL 5.7/8.x/9.x mutation dialects behind an independent adapter and
  writer-endpoint contract.

PostgreSQL, Oracle, and SQL Server adapters are registered and report their
capabilities, but their discovery and execution methods currently fail closed
as unsupported.

## Safety Boundary

The example configuration keeps the restricted node agent and automatic
failover disabled. Real MySQL role or VIP mutation becomes executable only when
the cluster has a complete HA endpoint inventory, an odd Raft controller set,
current leader-backed majority authority, purpose-specific MySQL credentials,
agent signing material, an operation lock, and approval. Missing or unknown
evidence blocks the operation; it never produces a simulated success.

Automatic failover is separately opt-in. It requires six follow-up failure
observations across 30 seconds, the rank-one eligible candidate, current
controller quorum, old-primary isolation, and an exclusive VIP lease. A whole-
host network partition that cannot prove old-primary fencing remains blocked.
The platform prefers temporary unavailability over a second writer or VIP
owner.

The common workflow remains:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Every implemented database mutation passes the same workflow. PostgreSQL,
Oracle, and SQL Server execution remains unsupported and fail-closed.

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

`scripts/build-clusterguard-bundle.sh` produces the controller, CLI, restricted
agent, lifecycle helpers, systemd units, log rotation, configuration samples,
and a full SHA-256 manifest. `scripts/clusterguard-install.sh` is preflight-only
unless `--execute` is supplied and installs protected TLS, SSH, and per-version
MySQL client assets from an explicit allowlisted runtime directory. Agent VIP
reconciliation is deferred by default and requires the explicit
`--activate-agent-reconcile` flag after endpoint metadata and majority leases
have been verified.

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
go run ./cmd/cgctl operation <operation-uuid>
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
