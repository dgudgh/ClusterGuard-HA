# PostgreSQL Agent: configuration, systemd and support boundaries

[简体中文](../zh-CN/postgresql-agent-systemd.md) · [PostgreSQL HA runbook](../postgresql-ha.md) · [Backend node entry](../development/backend/nodes.md)

Use this page for a native Linux PostgreSQL primary/standby Agent. Checked against source on 2026-11-02; this documentation work did not deploy or switch PG, and MySQL cloud results are not PG acceptance.

## Confirm the supported path

| Scope | Current implementation and boundary |
| --- | --- |
| Native PG HA | Discovery, candidates, controlled switching, failover, former-primary recovery and node lifecycle exist; enable them only after matching-version site qualification. |
| Linux Agent | Uses PG-specific service, OS user, PGDATA, tools, passfile and identities with host VIP, signed decisions and majority leases. |
| Enterprise gateway following PG promotion | Not currently supported. Gateway precheck requires MySQL, and gateway Agent configuration rejects PG. Changing the port to5432 or removing VIP fields does not enable it. |
| TCP forwarding | The independent component can relay TCP bytes; this does not prove PG authority publication, fencing, renewal or end-to-end switching. It provides no PG pooling, read/write splitting or transaction replay commitment. |

See gateway `Precheck` in `internal/endpoint/gateway.go`, gateway policy checks in `internal/agent/config.go`, and the PG-specific reconciliation branch. Retain those guards; documentation or manually publishing a route cannot establish PG support.

## Bind local identity and configuration

Use the [PG HA Agent policy](../postgresql-ha.md#restricted-agent-policy), not MySQL role settings. Verify:

- `cluster_id`, platform `instance_id`, separate `postgresql_node_id` and cluster `system_identifier`; platform and native UUIDs serve different purposes.
- `engine: "postgresql"`, actual `postgresql_service`, `postgresql_user`, `postgresql_port` and `postgresql_database`.
- `postgresql_data_directory`, `postgresql_binary_directory`, `postgresql_passfile` and allowed source identities/coordinates in `postgresql_peers`.
- `vip`, `interface`, `prefix` for the current native host-VIP deployment, trusted controller URLs/CA and signed-secret reference. Do not use the MySQL no-VIP gateway policy.

The default local executor uses `/usr/bin/setpriv --reuid ... --regid ... --init-groups` before calling PG tools. Some existing tests use runuser; that fixture is not the production default. Check the actual executable, user/group and filesystem access.

The configured PG OS user must own the 0600 passfile and be able to traverse its parent directories. Entries must match local management and permitted replication connections. Do not put passwords in browser forms, command arguments or logs. See the [PG16 password-file documentation](https://www.postgresql.org/docs/16/libpq-pgpass.html).

## Generate a narrow systemd sandbox

Within the authorized deployment procedure, run as root against the actual Agent configuration:

```bash
/usr/local/bin/clusterguard-agent \
  --config /etc/clusterguard/agent.json --configure-systemd-sandbox
systemctl daemon-reload
systemctl cat clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service \
  -p ReadWritePaths -p CapabilityBoundingSet -p AmbientCapabilities \
  -p ProtectSystem -p ProtectHome
```

The output is `/etc/systemd/system/clusterguard-agent-reconcile.service.d/20-postgresql-recovery.conf`. It adds only validated PGDATA and the adjacent private `<PGDATA>.clusterguard-recovery` receipt directory as writable paths, plus required DAC capability. Existing capability sets and filesystem protection are retained. This writes deployment files/receipt directories, not database role changes or writer authority, and does not need database credentials.

Paths must be canonical, sufficiently specific absolute directories without systemd specifiers, control characters, traversal or existing symlink ancestors. `/data/postgresql/5432/data` is illustrative and must match real PGDATA. Do not replace it with `/data`, `/var/lib` or the whole filesystem. The receipt directory must belong to the Agent execution user with mode 0700. Correct rejected configuration rather than widening `ReadWritePaths=/`.

RPM postinstall and configuration tools already call the generator. Still verify the effective unit after PGDATA/layout changes or upgrades. Successful generation does not prove actual reconcile permissions, SQL connectivity or isolation.

## Diagnose the real execution context

```bash
systemctl status clusterguard-agent-reconcile.timer --no-pager
systemctl show clusterguard-agent-reconcile.service -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 50 --no-pager
```

This is a oneshot service: `inactive` after success can be normal. Evaluate the timer, result and subsequent cycles together. Distinguish service/recovery state, credential/directory access, identity mismatch, controller transport/certificates/clocks and valid majority authority. Full-root SSH success is insufficient.

The MySQL vendor-RPM `CAP_SYS_NICE` failure is not evidence that PG needs that capability. Do not copy its drop-in or disable SELinux, signatures, leases or fencing to remove an alert.

Replace these illustrative variables with the actual policy. This reproduces only a local read-only query under the configured identity:

```bash
PG_OS_USER=postgres
PG_BIN=/usr/pgsql-16/bin
PG_PASSFILE=/etc/clusterguard/postgresql/5432.pass
PG_PORT=5432
PG_DATABASE=postgres
/usr/bin/setpriv --reuid "$PG_OS_USER" --regid "$PG_OS_USER" --init-groups -- \
  /usr/bin/env PGPASSFILE="$PG_PASSFILE" PGCONNECT_TIMEOUT=5 \
  "$PG_BIN/psql" --no-password --no-psqlrc --set=ON_ERROR_STOP=1 \
  --host=127.0.0.1 --port="$PG_PORT" --username="$PG_OS_USER" \
  --dbname="$PG_DATABASE" --tuples-only --no-align \
  --command="SELECT pg_is_in_recovery(), current_setting('transaction_read_only'), current_setting('default_transaction_read_only');"
```

See the [PG16 recovery functions](https://www.postgresql.org/docs/16/functions-admin.html#FUNCTIONS-RECOVERY-CONTROL). A session read-only setting alone does not prove former-primary isolation; require accepted stopped/recovery evidence and majority/lease authority. Configuration writes, start/stop, promotion, rewind and basebackup must use their controlled workflows, not ad hoc diagnostic commands.

## Sustained acceptance and remaining work

Verify native UUIDs, `system_identifier`, timeline, actual upstream identity, WAL receive/replay/lag, service state, unique writer and frontend ownership. Primary recovery=false and standby recovery=true are only part of the evidence. Check roles, TLS, reconnecting pools and uncertain transactions through the actual application entry, then observe multiple authorization cycles. Historical PG16.4 laboratory results retain their original date and scope.

This task performed no PG site permission repair, native round trip or PG enterprise-gateway test. PG gateway integration still needs a connected and verified PG authority/fencing/stable-renewal path plus actual database/browser, partition/quorum-loss, expiry/rollback and application acceptance. Keep unsupported/unverified states explicit until that work is complete.
