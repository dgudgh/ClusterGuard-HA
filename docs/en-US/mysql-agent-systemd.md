# MySQL Agent: systemd and vendor RPM capabilities

Use this guide for a Linux MySQL Agent after installing a vendor RPM, registering a cloud database, or promoting a replica. Apply it only when the evidence below matches; it is not a mandatory change for every node.

[简体中文](../zh-CN/mysql-agent-systemd.md) · [API operations reference](../operations.md) · [Backend node entry](../development/backend/nodes.md)

## Identify the failure

A console task can reach `succeeded` while subsequent Agent cycles alternate between `bootstrap_primary` and `release_and_read_only`. The writer becomes read-only repeatedly and the gateway route can expire, withdrawing the frontend. Task completion proves the checks at that moment; sustained availability also requires actual timer execution and SQL through the fixed frontend.

The reproduced case on 2026-11-02 used Alibaba Cloud `mysql-server-8.0.46`: `/usr/libexec/mysqld` had `cap_sys_nice=ep`, while the Agent reconcile service omitted it from `CapabilityBoundingSet`. A full-root SSH check passed, but the same command could not execute inside the systemd sandbox: `Operation not permitted` / `EPERM`, shell exit 126. The Agent then followed its existing isolation policy.

This is one confirmed cause. If the binary has no such file capability, the error is different, or another restriction remains, investigate that evidence instead of applying this capability change.

## Read-only diagnosis

Read the selected cluster's `mysql_server_binary` and `mysql_server_defaults_file` from `/etc/clusterguard/agent.json`; do not print its client credential file. Replace `MYSQLD` with that exact server binary, not a client or another installation.

```bash
MYSQLD=/usr/libexec/mysqld
getcap "$MYSQLD"
rpm -qf "$MYSQLD"
systemctl cat clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service \
  -p CapabilityBoundingSet -p AmbientCapabilities \
  -p ProtectSystem -p ProtectHome -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 50 --no-pager
```

Correlate `inspect writable MySQL restart state` / `inspect MySQL restart defaults` with the failure. Use a controlled diagnostic unit with the same user, filesystem restrictions and capabilities to compare the configured `mysqld --defaults-file=... --verbose --help`; do not start another database server. SSH success alone is insufficient. Keep diagnostics root-protected and do not publish full configuration or credentials.

Linux may reject execution when the file's effective capabilities cannot be obtained within the process bounding set. See [Linux capabilities(7)](https://man7.org/linux/man-pages/man7/capabilities.7.html).

## Add only the confirmed service capability

Within an authorized maintenance procedure, retain the original unit properties and logs. Only after confirming `cap_sys_nice=ep`, its absence from this service's bound, and a successful comparison with that single addition, manage this root-owned 0644 drop-in:

```ini
# /etc/systemd/system/clusterguard-agent-reconcile.service.d/30-mysql-vendor-capability.conf
[Service]
CapabilityBoundingSet=CAP_SYS_NICE
```

Inspect and back up an existing file rather than overwriting unknown site policy. Positive repeated capability assignments merge: do not reset the original set with an empty assignment or use `~` to permit everything. Verify the resulting bound adds only this capability. Preserve `AmbientCapabilities`, `ProtectSystem`, `ProtectHome` and the vendor binary's file capabilities. See [systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#CapabilityBoundingSet=).

After placing and reviewing the file:

```bash
systemctl daemon-reload
systemctl show clusterguard-agent-reconcile.service \
  -p CapabilityBoundingSet -p AmbientCapabilities -p ProtectSystem -p ProtectHome
```

An existing timer uses the new unit configuration on subsequent runs. Start one existing reconcile run only after confirming majority authority, signed policy and service state within the approved maintenance procedure:

```bash
systemctl start clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 20 --no-pager
```

This is a oneshot service: `inactive` after success can be normal. Check the timer, `Result=success`, `ExecMainStatus=0` and later cycles together. Do not enable reconciliation on an unenrolled node without trusted authority. Do not disable SELinux, signatures, fencing or read-only checks, erase leases/watermarks, or manually force a writer writable.

## Verify sustained state

- Actual timer runs stop alternating into isolation. The writer remains writable with both runtime read-only flags OFF; restart read-only protection remains, and replicas stay read-only.
- Verify every replication channel, lag, errors and GTID. SQL through the fixed frontend must identify the authorized writer's `server_uuid`.
- Gateway sequences continue increasing with the correct target and unique frontend ownership. Combine route readiness with real SQL; one gateway provides no frontend redundancy.
- Observe beyond multiple authorization TTLs and record the duration, samples and raw logs. One HTTP 200 or successful task is insufficient.

Use an existing protected observer file instead of putting a password on the command line:

```bash
mysql --defaults-file=/secure/mysql-observer.cnf \
  --protocol=TCP --host='<fixed-frontend-address>' --port=3306 --batch \
  --execute='SELECT @@server_uuid, @@global.read_only, @@global.super_read_only, @@global.gtid_executed'
```

Configure TLS verification in that client file according to site policy; do not disable certificate verification to make the connection work.

## Evidence and remaining scope

On 2026-11-02, an internal site used HA 3.1.5.1, compatible Agent 3.1.4.2 and standalone gateway 0.1.0-preview.3. The user performed one local-to-cloud switchover. Following the scoped capability correction, 16 actual frontend SQL/renewal samples over approximately 62 seconds passed; three local replicas were read-only with healthy IO/SQL and all four GTID sets matched. The earlier writer/frontend instability remains in the record.

This is a conditional site configuration fix. `ConfigureReconcileSandbox` does not automatically discover this MySQL file capability. Retain the drop-in with site configuration and reassess after RPM changes, upgrades or host rebuilds. No Agent business code or immutable signed patch was rebuilt. Return switching, application transactions/pools, fault injection and full production qualification remain unverified. See the [field history](../development/gateway-vip-frontend-2026-11-02.md); private evidence is not distributed with public documentation.
