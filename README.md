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
- an authenticated Chinese console with role-based access, mandatory bootstrap
  password change, CSRF protection, and the `cgctl` service CLI;
- durable operation UUIDs, idempotency keys, stage progress, audit, and reports;
- plan-bound, five-minute, single-use approval grants for manual high-risk
  database operations;
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

Every implemented database mutation passes the same workflow. PostgreSQL,
Oracle, and SQL Server execution remains unsupported and fail-closed.

## Start

Create a dedicated MySQL account with only the permissions needed by the
documented read-only queries. Keep secrets in environment variables, never in
the JSON configuration.

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

The console and API are served from `http://127.0.0.1:8088/` by default.
Open the console and sign in with `admin` / `admin123` on a new metadata store.
The console immediately requires a new password and does not load cluster data
until that change succeeds.

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
Stop writes to the control plane and recover a protected metadata backup with a
known administrator credential through the documented offline disaster-
recovery process. Do not delete user records, edit hashes, or re-enable
`admin123` in a live Raft set.

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
