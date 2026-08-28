# PostgreSQL HA

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](zh-CN/postgresql-ha.md)
<!-- /LANGUAGE-SWITCH -->


This runbook describes the native PostgreSQL control path in ClusterGuard HA.
It uses PostgreSQL streaming replication directly and does not require Patroni,
repmgr, or a vendor control API.

This capability belongs to the 2.2 release line. It is not part of the sealed
`v2.1.45` MySQL support boundary. Do not enable PostgreSQL mutation until the
exact 2.2 package and site acceptance matrix have passed.

## Delivered Capability

| Area | Status |
| --- | --- |
| Identity and inventory | Immutable platform UUID plus `clusterguard.node_id`; `system_identifier` binds the cluster. |
| Discovery and topology | Primary/standby role, source identity, timeline, receive/replay LSN, replay lag, and probe coverage. |
| Health and candidates | Streaming/read-only checks, promotion eligibility, deterministic WAL/lag ranking. |
| Planned switchover | Restricted service stop, WAL catch-up, promotion, sibling repoint, VIP transfer, and verification. |
| Guarded failover | Stable failure, Raft majority, lease, source isolation or external fencing, promotion, endpoint transfer, and verification. |
| Former-primary recovery | Explicit `pg_rewind` workflow; unsafe rewind is blocked with a rebuild recommendation. |
| Node lifecycle | Staged install plus `pg_basebackup`, or `pg_rewind` for a registered rebuild target. |
| Repair | Allowlisted replay resume and configuration reload only. |
| Metrics | Connections, active connections, transactions, deadlocks, temporary bytes, block/cache activity, database size, replication clients, and longest transaction. |
| Audit and reports | Common durable operation, verification, audit, and report pipeline. |

Capability is configuration-derived. Missing operation credentials, Agent
policy, writer endpoint, quorum, lifecycle helpers, or fencing evidence keeps
the corresponding control disabled and returns an explicit blocking reason.

## Safety Invariants

- Hostname, IP address, and port are mutable coordinates. They are never the
  database resource key.
- Every instance has one immutable platform `resource_id` and one immutable
  `clusterguard.node_id` UUID.
- All members of a cluster must report the same PostgreSQL
  `system_identifier`.
- A standby must report the current primary's immutable node UUID through
  `clusterguard.primary_node_id`.
- Planned switchover stops and proves the old primary inactive before
  promotion. Read-only mode alone is not treated as a hard fence.
- Failover requires majority authority and verified old-primary isolation.
  Unreachability is not proof of fencing.
- Success requires one writable primary, one writer-VIP owner, the planned
  target identity, and verified follower state. Crossing promotion without
  complete verification yields `indeterminate`, never success.

## PostgreSQL Prerequisites

Use the same PostgreSQL major release across a primary and every synchronization
target. Enable settings required by streaming replication and rewind:

```conf
wal_level = replica
hot_standby = on
max_wal_senders = 10
max_replication_slots = 10
wal_log_hints = on
```

Data checksums may satisfy the rewind prerequisite instead of
`wal_log_hints`, but enabling both provides stronger evidence. Qualify WAL
retention or archive behavior for the largest expected outage and base-backup
window.

### Stable Node Identity

Generate a different UUID for each physical PostgreSQL instance and retain it
through hostname, IP, and port changes:

```sql
ALTER SYSTEM SET clusterguard.node_id = '11111111-1111-4111-8111-111111111111';
ALTER SYSTEM SET clusterguard.primary_node_id = '';
ALTER SYSTEM SET clusterguard.hostname = 'pg-01';
SELECT pg_reload_conf();
```

On a standby, set its own node UUID and the current primary UUID:

```sql
ALTER SYSTEM SET clusterguard.node_id = '22222222-2222-4222-8222-222222222222';
ALTER SYSTEM SET clusterguard.primary_node_id = '11111111-1111-4111-8111-111111111111';
ALTER SYSTEM SET clusterguard.hostname = 'pg-02';
SELECT pg_reload_conf();
```

Verify the values from a new session. ClusterGuard rejects blank or malformed
UUIDs, mixed system identifiers, and a standby without a valid primary node
identity.

## Database Accounts

Use separate credentials for observation, administrative mutation, and
streaming replication. Restrict `pg_hba.conf` to controller and database-node
addresses, require TLS where available, and use SCRAM secrets.

### Discovery Account

```sql
CREATE ROLE cg_monitor LOGIN PASSWORD '<random-monitor-secret>';
GRANT CONNECT ON DATABASE postgres TO cg_monitor;
GRANT pg_monitor TO cg_monitor;
```

Test this account against every registered endpoint. It must read control
system/checkpoint functions, `pg_stat_activity`, `pg_stat_database`,
`pg_stat_replication`, `pg_stat_wal_receiver`, and WAL LSN functions.

### Operation Account

The current guarded path uses `ALTER SYSTEM` for the controlled read-only fence
and terminates client backends before stopping the source. Use a dedicated
administrative login. On PostgreSQL releases supporting parameter-level SET
privileges, grant only the required parameter and signaling rights; otherwise
the site must explicitly qualify a tightly held administrative role.

```sql
CREATE ROLE cg_operator LOGIN PASSWORD '<random-operation-secret>';
GRANT CONNECT ON DATABASE postgres TO cg_operator;
GRANT pg_monitor TO cg_operator;
GRANT pg_signal_backend TO cg_operator;
GRANT ALTER SYSTEM ON PARAMETER default_transaction_read_only TO cg_operator;
GRANT EXECUTE ON FUNCTION pg_reload_conf() TO cg_operator;
```

`SET` parameter privilege is not sufficient for ClusterGuard HA because the
planned switchover fence must persist across new sessions with `ALTER SYSTEM`.
The PostgreSQL precheck verifies these privileges with the configured operation
account and blocks execution before a lease or database state is changed when
any privilege is missing.

Do not silently replace a failed least-privilege qualification with a shared
application or replication account. An operation-account permission failure
must block precheck or execution.

### Replication Account

```sql
CREATE ROLE clusterguard_repl WITH REPLICATION LOGIN PASSWORD '<random-replication-secret>';
```

The same identity is used by managed `primary_conninfo` and
`pg_basebackup`. Keep the secret in protected environment files and PostgreSQL
passfiles, never in command arguments or browser fields.

## Controller Configuration

```json
"postgresql": {
  "enabled": true,
  "discovery_interval_seconds": 1,
  "discovery_timeout_seconds": 1,
  "automatic_failover_enabled": false,
  "automatic_failover_interval_seconds": 1,
  "automatic_failover_retry_seconds": 30,
  "discovery": {
    "username": "cg_monitor",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_DISCOVERY_PASSWORD"
  },
  "operation": {
    "username": "cg_operator",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_OPERATION_PASSWORD"
  },
  "replication": {
    "username": "clusterguard_repl",
    "database": "postgres",
    "password_env": "CG_POSTGRESQL_REPLICATION_PASSWORD"
  }
}
```

Operation and replication credentials are an all-or-nothing pair. Discovery
can remain read-only without them, but mutating capabilities stay unavailable.
Automatic failover additionally requires both credentials, a healthy odd Raft
control plane, and the restricted Agent. Invalid combinations fail at startup.

### Automatic Failover Contract

Set `automatic_failover_enabled` only after the destructive qualification
matrix below passes for the exact PostgreSQL packages, service units, network,
storage, and fencing provider used in production. At the default cadence the
controller requires three current failed-primary observations spanning at
least three seconds before it evaluates a takeover. This is an evidence window,
not an end-to-end RTO promise. A separate 15-second Agent authorization-expiry
fence protects against a disconnected old primary retaining writer or VIP
ownership.

The PostgreSQL 16.4 laboratory matrix measured 17.973 seconds of writer-endpoint
interruption when clients used `connect_timeout=2`. Without a bounded client
connect timeout, one blocked connection attempt lasted about 32 seconds even
though the control-plane operation completed earlier. Production connection
strings must therefore use a bounded connect timeout and retry policy, and the
site must measure RTO from the application endpoint rather than from an audit
timestamp alone.

The PostgreSQL recovery controller is isolated from the MySQL controller. It
selects only a rank-one standby from the same `system_identifier`, with current
probe evidence, matching upstream node UUID and timeline, active WAL receive
and replay, and known zero replay lag. Before promotion it requires Raft leader
and majority authority plus verified old-primary isolation through the signed
Agent or an external fencer. It then follows the common safety guard, operation
lock, internal one-time incident approval, execution, verification, audit, and
report pipeline.

A blocked or failed pre-promotion attempt may be reconsidered only after the
configured retry interval. A running, succeeded, or indeterminate attempt is
never replayed blindly. Promotion waits until `pg_is_in_recovery()` is false;
repoint and former-primary recovery wait until the approved immutable source
UUID and `primary_conninfo` have both converged.

## Restricted Agent Policy

Every database node runs `clusterguard-agent` with a cluster-specific policy.
The policy fixes the cluster UUID, platform instance UUID, PostgreSQL native
node UUID, engine, service, OS user, data directory, binary directory,
database, passfile, port, VIP, and peer allowlist.
Signed requests are short-lived and plan-bound. The Agent exposes fixed command
vectors only; it is not a remote shell.

```json
{
  "cluster_id": "33333333-3333-4333-8333-333333333333",
  "instance_id": "44444444-4444-4444-8444-444444444444",
  "engine": "postgresql",
  "postgresql_node_id": "66666666-6666-4666-8666-666666666666",
  "vip": "192.0.2.110",
  "interface": "ens160",
  "prefix": 24,
  "postgresql_port": 5432,
  "postgresql_service": "clusterguard-postgresql-5432.service",
  "postgresql_user": "postgres",
  "postgresql_data_directory": "/var/lib/clusterguard/postgresql/5432/data",
  "postgresql_binary_directory": "/opt/clusterguard/postgresql/5432/software/bin",
  "postgresql_passfile": "/etc/clusterguard/postgresql/5432.pass",
  "postgresql_database": "postgres",
  "postgresql_replication_user": "clusterguard_repl",
  "postgresql_peers": [
    {
      "instance_id": "55555555-5555-4555-8555-555555555555",
      "node_id": "77777777-7777-4777-8777-777777777777",
      "hostname": "pg-02",
      "ip_address": "192.0.2.21",
      "port": 5432
    }
  ]
}
```

The passfile must be owned by the configured PostgreSQL OS user, mode `0600`,
and contain the required local administrative and peer replication entries.
`instance_id` is the immutable ClusterGuard platform resource used for
routing, leases, and responses. `postgresql_node_id` and each peer `node_id`
are the immutable database-native identities written to
`clusterguard.node_id`, `clusterguard.primary_node_id`, and replication
`application_name`. They are deliberately different identifiers. Unknown
service state, an unlisted platform or native source identity, changed source
coordinates, or a request/plan identity mismatch fails closed.

## Node Lifecycle

Enable the common lifecycle executor only on a three-or-more odd Raft control
plane and provide the PostgreSQL helpers and write-only secret environments:

```json
"node_lifecycle": {
  "enabled": true,
  "executor_path": "/usr/local/libexec/clusterguard-node-lifecycle.sh",
  "package_repository": "/opt/clusterguard/packages",
  "known_hosts_file": "/etc/clusterguard/known_hosts",
  "identity_file": "/etc/clusterguard/lifecycle_ed25519",
  "jq_binary": "/usr/local/libexec/jq-linux-amd64",
  "mysql_root_password_env": "CG_NODE_MYSQL_ROOT_PASSWORD",
  "replication_password_env": "CG_NODE_MYSQL_REPLICATION_PASSWORD",
  "postgresql_install_helper": "/usr/local/libexec/clusterguard-postgresql-install.sh",
  "postgresql_sync_helper": "/usr/local/libexec/clusterguard-postgresql-sync.sh",
  "postgresql_admin_password_env": "CG_NODE_POSTGRESQL_ADMIN_PASSWORD",
  "postgresql_replication_password_env": "CG_NODE_POSTGRESQL_REPLICATION_PASSWORD",
  "postgresql_basebackup_available": true,
  "postgresql_rewind_available": true
}
```

The console shows only methods enabled by this capability response:

- `pg_basebackup` is the baseline for a new or divergent node;
- `pg_rewind` is available only for a registered rebuild target with compatible
  system identity and rewind prerequisites.

The helper stops the target, stages data outside the live directory, validates
`PG_VERSION` and `system_identifier`, atomically swaps the data directory,
starts the service, and verifies read-only recovery, upstream UUID, streaming,
and absence of the writer VIP. Failed verification leaves the target stopped.

## Register, Observe, and Operate

Register only authoritative database endpoints with `engine: "postgresql"`,
then publish a current observation:

```bash
curl -sS -X POST https://controller.example/api/v1/clusters \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'content-type: application/json' \
  -d '{
    "display_name":"payments-postgresql",
    "engine":"postgresql",
    "endpoints":[
      {"hostname":"pg-01","ip_address":"192.0.2.20","port":5432},
      {"hostname":"pg-02","ip_address":"192.0.2.21","port":5432},
      {"hostname":"pg-03","ip_address":"192.0.2.22","port":5432}
    ]
  }'

curl -sS -X POST https://controller.example/api/v1/clusters/<cluster-uuid>/discover \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'content-type: application/json' -d '{}'
```

The first successful observation binds `system_identifier` only after full
active-endpoint coverage. A later hostname, IP, or port change reconciles the
same node resource and records the previous coordinate as an alias.

Manual switchover uses the common `/api/v1/operations/execute` route or the
authenticated console. The server builds the immutable plan and internally
consumes a one-time approval grant for an authorized platform user. Service
automation uses the explicit plan-bound grant API. Neither path accepts a
reusable approval password.

## Failover and Fencing

A planned switchover can prove isolation by stopping and rechecking the source
through the restricted Agent. An unreachable primary cannot provide that
proof. Guarded failover therefore also requires a site-specific external
fencer that isolates power, hypervisor, cloud instance, PDU, BMC, or equivalent
write access and then independently reports `fenced:true`.

Network reachability, ICMP failure, database timeout, read-only settings, and
VIP absence are not fencing. Without majority authority and verified isolation,
ClusterGuard chooses unavailability over a possible second writer.

## Metrics and Monitoring

Use these dependency-free endpoints:

```text
GET /api/v1/clusters/{id}/health
GET /api/v1/clusters/{id}/metrics
GET /api/v1/clusters/{id}/metrics/prometheus
GET /api/v1/monitoring/health
```

The console renders PostgreSQL-native names and cumulative values rather than
labeling them as MySQL QPS/TPS. Unknown lag or optional transaction age is
omitted instead of being synthesized as zero.

## Production Qualification

Before enabling mutation on a production cluster, prove all of the following
in an isolated environment built from the same PostgreSQL packages and service
layout:

1. Full discovery binds exactly one primary and all standby node identities.
2. Candidate rejection works for timeline mismatch, paused replay, unknown lag,
   stale probe evidence, and a foreign system identifier.
3. Planned switchover rotates through every node while preserving one writer
   and one VIP owner.
4. Old-primary recovery succeeds with rewind and blocks to rebuild when rewind
   prerequisites are absent.
5. `pg_basebackup` rebuild leaves no target-only data and rejoins streaming.
6. Controller minority cannot execute a mutation.
7. A primary network partition remains blocked until the external fencer proves
   isolation.
8. Process and host restarts preserve resource identity, operation history,
   audit, reports, endpoint lease, and control-plane quorum.
9. Failure after promotion is reported `indeterminate` and is never retried
   blindly.
10. Backups and point-in-time recovery remain independently tested; HA is not a
    substitute for backup.
