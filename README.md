# ClusterGuard HA

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](README.zh-CN.md)
<!-- /LANGUAGE-SWITCH -->


ClusterGuard HA requires Go 1.22 or newer.

**ClusterGuard HA 多数据库企业级高可用控制平台**

**ClusterGuard HA — Multi-Database High Availability Control Plane**

ClusterGuard HA is an independent, clean-room high-availability control plane.
The stable 2.1 line is the MySQL HA product line. PostgreSQL delivery starts in
2.2. Oracle Data Guard Broker and SQL Server Always On remain separately gated
future product lines and are not part of the 2.1 support scope.

## Console Preview

![ClusterGuard HA operations workbench](docs/assets/screenshots/ha-operation-workbench.png)

The console keeps cluster context, primary and candidate selection, VIP state,
controlled execution, recovery, topology, and audit evidence in one operator
workflow. See the bilingual [product tour](docs/en-US/product-tour.md) for the
topology, node lifecycle, and operation-log views.

## Current Release

Release policy and support boundary:

- `v2.1.45` is the immutable final release of the 2.1 line.
- The supported 2.1 database engine is MySQL, including approved compatible
  MySQL distributions validated by the site acceptance matrix.
- `v2.2.39` is the first formal 2.2 release, delivering PostgreSQL 16.4,
  Docker Swarm adoption, and the Kubernetes writer-endpoint foundation.
- Any shipped feature or behavior change increments the package release; an
  existing RPM, offline archive, tag, or GitHub Release is never overwritten.
- **GitHub Releases carry complete offline installation media only.** Signed
  `.cgupgrade` update packages (including legacy `.cgpatch`) are delivered to
  contracted enterprise customers only and are **never** uploaded to GitHub or
  any other public channel; a public copy is treated as unauthorized
  distribution and is removed on sight. See
  [release policy §3.1](docs/en-US/version-release-policy.md).
- Oracle and SQL Server code may exist behind capability gates, but it is not a
  production claim for the sealed 2.1 line.

| Line | Status | Production support boundary |
| --- | --- | --- |
| `2.1.45` | Stable and sealed | MySQL HA control plane |
| `2.2.39` | Formal release | PostgreSQL 16.4, Docker Swarm, and retained MySQL HA capabilities |
| Later lines | Roadmap | Oracle Data Guard Broker and SQL Server Always On after separate qualification |

Download the current formal release from
[ClusterGuard HA 2.2.39](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39):

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.39/clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.39/clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz.sha256
```

The sealed 2.1 MySQL release remains available from
[ClusterGuard HA 2.1-45](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.1.45):

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.1.45/clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.1.45/clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
```

Published SHA-256 values:

```text
d4a46bdfa4c95bb641a7d19f063d2f43219177658b01b914a31a1cd5d06ec590  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
1bc70109b556e973744bb05b5be0f53b075260910ae5def25518631ff8629a1c  clusterguard-ha-2.1-45.x86_64.rpm
```

> **Qualification status:** Automated tests and laboratory acceptance do not
> replace site qualification. Before production use, validate the exact MySQL
> package, operating system, storage, network, fencing policy, VIP provider and
> three-controller deployment. PostgreSQL acceptance belongs to the 2.2 line
> and is documented separately in
> [postgresql-ha.md](docs/postgresql-ha.md).

The current MySQL adapter provides:

- inventory-scoped discovery for MySQL 5.7, 8.0, 8.4, and 9.x (the 9.x dialect
  path is unit-tested against 9.7.0 only; no 9.x site qualification is recorded);
- globally unique immutable node names, platform UUIDs, and native MySQL
  `server_uuid` identity;
- persisted topology, health, replication links, probe evidence, and metrics;
- promotion-candidate assessment with GTID and replication safety checks;
- guarded three-node planned switchover with primary and VIP ownership coupled;
- former-primary rejoin and allowlisted replication repair;
- four observations across a three-second stable-failure window and optional automatic failover;
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
- plan-bound, single-use approval grants for manual high-risk database
  operations (five-minute default lifetime, capped at fifteen minutes);
- tested MySQL 5.7/8.x/9.x mutation dialects behind an independent adapter and
  writer-endpoint contract.

## 2.2 Release Scope

The PostgreSQL adapter in 2.2 provides inventory-scoped
discovery, immutable node identity, `system_identifier` cluster binding,
primary/standby topology,
streaming health, timeline and WAL evidence, native metrics, deterministic
candidate assessment, guarded planned switchover and failover, former-primary
`pg_rewind` recovery, allowlisted low-risk repair, writer-VIP coupling, and
`pg_basebackup`/`pg_rewind` node lifecycle, plus optional three-second stable-failure
automatic failover. Mutations are advertised only when
the restricted Agent, operation credentials, endpoint provider, controller
quorum, and required fencing evidence are configured. These capabilities are
not retroactively added to `v2.1.45`.

The Oracle adapter is a future gated integration for signed-Agent Data Guard
Broker discovery, health,
topology, standby candidate assessment, precheck, plan, execute, and dual-node
verification for controlled switchover. A dedicated password-file
`SYSDG` account is used instead of `SYS`; the local Agent runs DGMGRL as the
Oracle operating-system account and accepts only configured Broker members.
ClusterGuard never edits Oracle data files directly. Failure failover remains
blocked until old-primary fencing is configured.

The SQL Server adapter is a future gated integration for Always On read-only
discovery, health, topology, synchronized-secondary candidate assessment,
precheck, plan, execute, verify, and send/redo queue metrics for planned
failover to a synchronized synchronous-commit secondary.
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

Automatic failover is separately opt-in. It requires four current failure
observations spanning at least three seconds, the rank-one eligible candidate, current
controller quorum, old-primary isolation, and an exclusive VIP lease. MySQL can
use short-lived Raft-majority Agent authorization: stale authorization expires,
the old node fails closed to no VIP plus persistent read-only, and the Leader
revalidates the exact transition lease before promotion. An external BMC, PDU,
cloud, or hypervisor fencer remains an optional stronger layer. The platform
prefers temporary unavailability over a second writer or VIP owner.

The three-second interval is the controller's stable-failure evidence window,
not an end-to-end RTO promise. The restricted Agent retains a separate 15-second
authorization-expiry fence before an isolated old primary can no longer serve
as writer or VIP owner. PostgreSQL 16.4 laboratory qualification measured
17.973 seconds of writer-endpoint interruption with a two-second client connect
timeout; every production site must repeat that test with its own network,
storage, database package, client timeout, and fencing policy.

The browser console authenticates against platform users stored in the
replicated metadata snapshot. A fresh installation creates `admin` with
`MustChangePassword=true`. Unless the deployment sets
`bootstrap_admin_password_env`, the Leader generates the first-login password
and writes it to a root-only `bootstrap-admin-password` file (mode 0600) beside
the metadata file; read it on the control node where it appears. The platform
removes that file once the first password change succeeds. The first password
change is mandatory: until it succeeds, all cluster and metadata API reads and
every mutation are blocked. The password is stored only as an Argon2id hash; it
is never written to configuration, environment files, audit events, or reports. Sessions have an eight-hour absolute lifetime, use HttpOnly
SameSite cookies plus CSRF validation, and are revoked by password change or
logout.

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

When started directly from the source configuration, the console and API are
served from `http://127.0.0.1:8088/` by default. The supported RPM/offline
deployment serves HTTPS on port `3000`. On a new metadata store, sign in as
`admin` with the generated password from the root-only `bootstrap-admin-password`
file (or with the password configured through `bootstrap_admin_password_env`).
The console immediately requires a new password and
does not load cluster data until that first password change succeeds.

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
and a full SHA-256 manifest. `scripts/clusterguard-install.sh` is a per-node
lifecycle helper; it is not the initial multi-node production bootstrap tool.

Use `scripts/install_clusterguard.sh` and the complete offline kit for the
initial production deployment. It creates the control plane, fixed resource
identities, certificates, database topology, Agent configuration, and VIP
reconciliation policy as one audited workflow.

## Documentation

The [bilingual documentation center](docs/README.md) maps every maintained
English document to its Simplified Chinese counterpart. Recommended entry
points:

- [English documentation](docs/en-US/README.md) / [中文文档](docs/zh-CN/README.md)
- [English product tour](docs/en-US/product-tour.md) / [中文产品导览](docs/zh-CN/product-tour.md)
- [English offline installation](docs/en-US/offline-rpm-install.md) / [中文离线安装](docs/zh-CN/offline-rpm-install.md)
- [English operations manual](docs/en-US/operations-manual.md) / [中文运维手册](docs/zh-CN/operations-manual.md)
- [English version update guide](docs/en-US/update-and-patch.md) / [中文版本升级与回退手册](docs/zh-CN/update-and-patch.md)
- [English release policy](docs/en-US/version-release-policy.md) / [中文版本规范](docs/zh-CN/version-release-policy.md)

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
the complete procedure. Never edit password hashes or create a shared default
password in a live Raft set.

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
