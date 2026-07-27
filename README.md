# ClusterGuard HA

ClusterGuard HA requires Go 1.22 or newer.

**ClusterGuard HA 多数据库企业级高可用控制平台**

**ClusterGuard HA — Multi-Database High Availability Control Plane**

ClusterGuard HA is an independent, clean-room high-availability control plane.
The current release delivers guarded MySQL and PostgreSQL control paths, and
adds controlled role-transition adapters for Oracle Data Guard Broker and SQL
Server Always On when their native command runners are configured.

## Current Release

> **Qualification status:** The capabilities below are implemented and covered
> by automated tests. They are not a substitute for production qualification.
> PostgreSQL mutation must remain disabled until the exact database package,
> service layout, storage, network, fencing provider, VIP provider, and
> three-controller deployment pass the live acceptance matrix in
> [postgresql-ha.md](docs/postgresql-ha.md).

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
- fail-closed liveness/readiness separation, Raft role and quorum diagnostics,
  bounded history, paginated operation logs, and rate-limited incident reminders;
- an authenticated Chinese console with role-based access, mandatory bootstrap
  password change, CSRF protection, and the `cgctl` service CLI;
- durable operation UUIDs, idempotency keys, stage progress, audit, and reports;
- plan-bound, five-minute, single-use approval grants for manual high-risk
  database operations;
- tested MySQL 5.7/8.x/9.x mutation dialects behind an independent adapter and
  writer-endpoint contract.

The PostgreSQL adapter provides inventory-scoped discovery, immutable node
identity, `system_identifier` cluster binding, primary/standby topology,
streaming health, timeline and WAL evidence, native metrics, deterministic
candidate assessment, guarded planned switchover and failover, former-primary
`pg_rewind` recovery, allowlisted low-risk repair, writer-VIP coupling, and
`pg_basebackup`/`pg_rewind` node lifecycle, plus optional 30-second stable-failure
automatic failover. Mutations are advertised only when
the restricted Agent, operation credentials, endpoint provider, controller
quorum, and required fencing evidence are configured.

The Oracle adapter supports signed-Agent Data Guard Broker discovery, health,
topology, standby candidate assessment, precheck, plan, execute, and dual-node
verification for controlled switchover. A dedicated password-file
`SYSDG` account is used instead of `SYS`; the local Agent runs DGMGRL as the
Oracle operating-system account and accepts only configured Broker members.
ClusterGuard never edits Oracle data files directly. Failure failover remains
blocked until old-primary fencing is configured.

The SQL Server adapter supports Always On read-only discovery, health,
topology, synchronized-secondary candidate assessment, precheck, plan, execute,
verify, and send/redo queue metrics for planned failover to a synchronized
synchronous-commit secondary.
Discovery and execution are advertised only when `sqlcmd` is available or a SQL
Server runner is injected. Forced failover remains blocked by default unless a
future explicit data-loss approval policy is configured.

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
host network partition is eligible only when the configured external fencer
isolates the old primary and a separate status call proves that isolation.
Without that evidence the operation remains blocked. The platform prefers
temporary unavailability over a second writer or VIP owner.

The browser console authenticates against platform users stored in the
replicated metadata snapshot. A fresh installation creates `admin` with the
temporary password `admin123` and `MustChangePassword=true`. The first login
must replace it before any platform operation is accepted. Passwords are stored
only as Argon2id hashes. Sessions have an eight-hour absolute lifetime, use
HttpOnly SameSite cookies plus CSRF validation, and are revoked by password
change or logout.

For an authenticated administrator or operator, the server builds the exact
plan and internally issues and consumes a one-time grant; the browser never
sees an approval secret. External service automation keeps the explicit grant
API: only the grant hash is replicated, plaintext is returned once, and
consumption is atomic with the workflow approval stage. Automatic failover uses
an internal incident authorization path and does not depend on a reusable human
token.

The common workflow remains:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Every implemented mutation passes the same workflow. Oracle and SQL Server
execution remains fail-closed unless the native broker/AG command runner is
configured and the platform has current identity, role, health, lock, approval,
and verification evidence.

## Start

Create a dedicated MySQL account with only the permissions needed by the
documented read-only queries. Keep secrets in environment variables, never in
the JSON configuration.

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
export CG_POSTGRESQL_DISCOVERY_PASSWORD='replace-with-the-pg-monitor-secret'
export CG_POSTGRESQL_OPERATION_PASSWORD='replace-with-the-pg-operation-secret'
export CG_POSTGRESQL_REPLICATION_PASSWORD='replace-with-the-pg-replication-secret'
export CG_ORACLE_DISCOVERY_PASSWORD='replace-with-the-dgbroker-monitor-secret'
export CG_ORACLE_OPERATION_PASSWORD='replace-with-the-dgbroker-operation-secret'
export CG_SQLSERVER_DISCOVERY_PASSWORD='replace-with-the-ag-monitor-secret'
export CG_SQLSERVER_OPERATION_PASSWORD='replace-with-the-ag-operation-secret'
# Optional, only on controllers that execute these engines:
# export PATH="/opt/oracle/product/bin:/opt/mssql-tools18/bin:$PATH"
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

The console and API are served from `http://127.0.0.1:8088/` by default.
Open the console and sign in with `admin` / `admin123` on a new metadata store.
The console immediately requires a new password and does not load cluster data
until that change succeeds.

Service managers should use `/healthz` for liveness and `/readyz` for
fail-closed control-plane readiness. Authenticated operators can use
`cgctl status` or `/api/v1/control-plane/status` for Raft role, Leader, quorum,
metadata revision, active work, and uptime diagnostics. Every API response has
an `X-Request-ID` that is preserved when a follower forwards a mutation to the
current Leader.

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

See `docs/offline-install.md` for the air-gapped build, transfer, dependency,
preflight, installation, Raft rollout, and rollback procedure.

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

## Register and Refresh a PostgreSQL Cluster

Each PostgreSQL instance must have a unique immutable
`clusterguard.node_id`. Every standby also declares the current primary's node
UUID in `clusterguard.primary_node_id`. ClusterGuard binds the cluster to
`pg_control_system().system_identifier`; changing hostname, IP, or port updates
the endpoint and retains the same platform instance UUID.

Enable the `postgresql` configuration block, set the dedicated discovery,
operation, and replication credential environments, and register only
authoritative endpoints:

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-postgresql",
    "engine":"postgresql",
    "endpoints":[
      {"hostname":"pg-a","ip_address":"192.0.2.20","port":5432},
      {"hostname":"pg-b","ip_address":"192.0.2.21","port":5432}
    ]
  }'

curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

The console exposes PostgreSQL topology, health, native metrics, candidate
evidence, guarded operations, node synchronization, audit, and reports. Every
mutating control remains disabled until the corresponding runtime capability
and safety evidence are real; the console never presents simulated success.
See [postgresql-ha.md](docs/postgresql-ha.md) for database grants, restricted
Agent policy, lifecycle, fencing, and production qualification.

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
go run ./cmd/cgctl approval issue --cluster <cluster-uuid> --engine mysql \
  --kind switchover --target <instance-uuid> --issued-by <administrator> --ttl 5m
go run ./cmd/cgctl approval list
go run ./cmd/cgctl approval show <grant-uuid>
```

`cgctl` uses the control token from `CG_CONTROL_TOKEN` for authenticated reads
and writes. Use
`--token-env <name>` before the command to select a different environment
variable; the secret is never accepted as a command-line value. Approval
issuance also reads this administrator credential. The returned approval token
is printed once and is never persisted by `cgctl`.

If the administrator password is lost, there is no online bypass or reset API.
After backing up metadata and pausing mutations, create a private one-time
artifact with `clusterguard admin prepare-recovery`, distribute that same
artifact to all controllers as `clusterguard:clusterguard` mode `0600`, and
restart them one at a time. The Raft Leader applies its recovery ID once,
revokes existing sessions, forces a password change, audits the action, and
removes the artifact on every node. See [operations.md](docs/operations.md) for
the complete procedure. Never edit password hashes or re-enable `admin123` in
a live Raft set.

The HA matrix supports browser-equivalent session execution:

```bash
export CG_PLATFORM_USERNAME='admin'
export CG_PLATFORM_PASSWORD='<current-platform-password>'
scripts/clusterguard-ha-matrix.sh \
  --api http://127.0.0.1:8088 \
  --clusters '<cluster-uuid>' \
  --platform-session
```

On a fresh metadata store, set `CG_PLATFORM_NEW_PASSWORD` as well. Omitting
`--platform-session` keeps the explicit one-time grant mode for service
automation tests.

Place global flags before the command:

```bash
go run ./cmd/cgctl --json topology <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 clusters
```

See [operations.md](docs/operations.md) for the complete API workflow and
operator procedures. See [architecture.md](docs/architecture.md) for resource
identity, adapter, persistence, candidate, and safety design.
