# ClusterGuard HA Operations Manual

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/operations-manual.md)
<!-- /LANGUAGE-SWITCH -->

This document is used for daily duty, changes, fault handling, and auditing. All high-availability operations must go through the platform's unified workflow:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK
-> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Adapters must not bypass this process and directly modify the database.

Version boundary: `2.1-45` is the MySQL sealed version; PostgreSQL operations start from 2.2. The content of Oracle and SQL Server in this document can only be executed after the corresponding official version is released and on-site acceptance is completed.

## 1. Login

Access:

```text
https://<ANY_CONTROLLER>:3000/
```

Temporary account for new metadata storage:

```text
Username: admin
Initial password: from bootstrap-admin-password (mode 0600)
```

The Leader generates that password into `/var/lib/clusterguard/bootstrap-admin-password`
beside the metadata file unless the deployment sets `bootstrap_admin_password_env`,
in which case that value is the initial password and no file is created. Read the
file on the control node where it appears.

Password must be changed on first login; before changing the password, all cluster, topology, audit read, and change operations except authentication interfaces are rejected. The default password is only stored as an Argon2id hash and is not written into configuration, environment files, audit, or reports. Operations initiated within the platform use authenticated sessions, CSRF, and role authorization; users do not need to manually input control tokens or one-time approval tokens. External APIs use Bearer control tokens; high-risk service calls still require a one-time Approval bound to the operation.

Do not edit the backend JSON or the password hash when the administrator password is forgotten. Follow the local one-time recovery process:

```bash
sudo -u clusterguard /usr/local/bin/clusterguard admin prepare-recovery
```

Place the generated recovery artifact with `0600` permissions on all control nodes, perform a rolling restart, and immediately change the password after logging in with a one-time temporary password.

## 2. Control Plane Health

Check the control plane daily, not directly switch:

```bash
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/healthz
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/readyz
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt status
```

Normal standards:

- Only one Raft Leader
- Voter count is odd
- quorum confirmed is yes
- mutation authority is yes
- active/indeterminate operation count is as expected
- Three-node metadata revision is eventually consistent

When a Follower receives a write request, it forwards it to the current Leader. If the Leader is unknown or has lost the majority, writes must be blocked and cannot be silently executed on the local machine.

## 3. Cluster Access

Go to "Cluster Management" on the console:

1. Select the database type.
2. Enter the fixed cluster name.
3. Add the complete instance list.
4. Select a fixed node name and platform UUID for each node.
5. Fill in the hostname, IP, port, and native identity of the database.
6. Configure VIP, Oracle service, or SQL Server Listener.
7. After saving, immediately perform discovery and health checks.

The database list is the control boundary. The platform must not operate on nodes outside the list.

Command line view:

```bash
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt clusters
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt topology <CLUSTER_UUID>
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt health <CLUSTER_UUID>
```

## 4. Metadata Changes

When modifying hostname, IP, or port:

1. Open "Modify Metadata" in the top right corner of "Topology".
2. Select existing platform resources, do not create new nodes.
3. Enter the new endpoint and reason for the change.
4. Run metadata pre-check.
5. Confirm that the native database identity matches existing resources.
6. Execute reconcile.
7. Verify that the old endpoint has been saved as an alias, and `resource_id` has not changed.

When the same engine identity is discovered, it must be merged into existing resources. If identity mismatch, duplicate resources, or cross-cluster node reuse are encountered, block the operation first, then have the administrator merge the alias or retire the incorrect endpoint.

## 5. Planned Switch

Applicable to scenarios where the current primary is healthy and a controlled switch is needed.

1. Select the target cluster in the top left.
2. Confirm the current primary, candidate primary, VIP/service, and delay.
3. Select the target node from the candidate list.
4. Click "Unlock Operation"; the lock is only valid for the current session and a short time window.
5. Click "Execute Switch".
6. The page should display the pre-check, plan, safety gate, execution, and verification stages.
7. Wait for the final state to be successful, do not consider "Submitted" as successful.

Success must be simultaneously satisfied:

- New primary is writable
- Old primary is read-only or isolated
- Replicas follow the new primary
- VIP/Oracle service/SQL Server Listener points to the new primary
- Operation lock has been released
- Audit and reports have been committed to the database

If the status is `indeterminate` or "Needs Review", immediately stop repeating clicks and query the current status by operation UUID:

```bash
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt operation <OPERATION_UUID>
```

First inspect the persisted checks, live database roles, replication state, and unique Writer endpoint ownership. If the immutable plan still matches the current topology, run `verify` again; the record can become `succeeded` only when explicit verification evidence passes. If topology has moved on and the old plan can no longer be proven, but the site inspection is complete, use **Mark reviewed** in Operation Log and record the evidence. This stores an immutable reviewer, timestamp, note, audit event, and report while keeping the original status `indeterminate`; it never fabricates success.

## 6. Failover

Failover is used for scenarios where the original primary is unreachable or has lost write capability, and it is not equivalent to a planned switch.

Confirm before execution:

- Control nodes still have a Raft majority
- The original primary has been proven unable to continue writing through fencing, network isolation, or power isolation
- Candidate node replication status and data loss risk have been assessed
- Writer endpoint will not remain on the old primary
- There is no current cluster operation lock

Automatic failover does not require manual input of tokens, but it still must pass Safety Guard, Raft Leader, majority, idempotency key, Operation Lock, verification, and audit. If any of these conditions are not met, the operation should be blocked.

After network partition recovery, the old primary must not automatically resume writing. It can only enter the old primary recovery process as a pending repair node.

## 7. Old Primary Recovery

"Old Primary Recovery" is used to re-add the old primary to the current primary after a switch. MySQL 8.0, 8.4, and 9.7 use the same "One-Click Recovery to Slave" entry, and operators do not need to first determine whether incremental re-attachment or full rebuild is needed.

1. Confirm that the current primary and Writer endpoint are normal.
2. Select the registered out-of-cluster old primary.
3. Start the repaired database service; the old primary must maintain `read_only=ON` and
   `super_read_only=ON`, and must not manually restore write permissions or bind VIP.
4. Unlock the operation on the console, select the old primary, and click "One-Click Recovery to Slave".
5. The console first actively refreshes cluster discovery to avoid using old health snapshots when the service is just started.
6. The platform verifies fixed resource identity, current primary, read-only status, GTID set, required binlog,
   cluster list, and Writer endpoint uniqueness.
7. The platform automatically selects incremental re-attachment or full rebuild.
8. Finally, verify replication threads, delay, read-only status, replication source, single primary, and VIP uniqueness.

### 7.1 MySQL Automatic Recovery Decision

| Evidence | Automatic Action | Description |
|---|---|---|
| Old primary GTID is a subset of the current primary GTID, and the required binlog to catch up still exists | GTID incremental re-attachment | Re-point to the current primary and start replication, fastest speed |
| The current primary has already purged the binlog required for the old primary to catch up | Full rebuild | Resynchronize complete data from the current primary and establish replication |
| The old primary has errant GTID that the current primary does not have | Full rebuild | Do not attempt to skip transactions or forcibly merge forked data |
| The old primary is still writable, native identity does not match, is not on the cluster list, current primary or Writer endpoint is not unique | Block | Re-execute after fixing the facts, no speculative changes |

"Lost binlog" refers to the current primary having purged the binary logs required for the old primary to catch up. The platform cannot and will not fabricate missing transactions; the safe recovery method is to preserve audit evidence, clean up the old primary's data copy, use the current primary as the donor to complete full synchronization, and then rejoin as a read-only replica. Full rebuild requires platform administrator permissions, node lifecycle execution capability, and complete verification; if any condition is not met, it will fail-closed.

After recovery, you must simultaneously see: the current primary is the only writable, the recovered node is read-only, replication IO/SQL threads are running, delay is zero, data verification has passed, VIP is only on the current primary, and the operation report status is succeeded. Seeing "Service has started" does not equal recovery success.

Engine strategies:

- MySQL: Incremental re-attachment when GTID and binlog are complete; automatically enter the approved full rebuild process when there is a binlog gap or errant GTID.
- PostgreSQL: Prefer `pg_rewind` if conditions are met, otherwise `pg_basebackup`.
- Oracle: Reinstated/validated by Data Guard Broker.
- SQL Server: Recover synchronization based on AG replica status, cannot fake healthy replicas that are not synchronized.

## 8. Node Expansion and Repair

The "Add or Repair Node" page on the console's "Node" page uses the same lifecycle process:

1. Select data node, control node, or hybrid node.
2. Enter the permanent fixed node name and platform UUID.
3. Enter the SSH endpoint.
4. Select the database version and port.
5. Upload or select an approved package.
6. Pre-check disk, port, dependencies, identity, and source node.
7. Install the database.
8. Select clone, xtrabackup, mysqldump, pg_basebackup, or pg_rewind.
9. Synchronize data and establish replication.
10. Submit metadata after verification.

All of the above steps are built-in ClusterGuard workflows and do not require operators to manually execute installation or replication commands on the target node. Task details will separately display "Pre-check, Install, Data Synchronization, Replication Configuration, Verification, Metadata Submission"; if any step fails, subsequent submissions will be stopped, and sanitized logs and reports will be retained for review.

The number of data nodes is unlimited. Raft control nodes must maintain an odd number, commonly 3 or 5. When expanding from 3 control nodes, the console requires entering two different fixed node names, platform UUIDs, hostnames, and IPs in one controlled task and completing them:

1. Verify the SSH identity, fixed node list, and address uniqueness of the two target machines.
2. Install ClusterGuard control service and selected database Adapter Runtime.
3. Issue independent API and Raft mTLS certificates for each control node.
4. Start the service and verify the node identity.
5. The current Leader sequentially executes Raft `AddVoter`, and the final member count must be 5.
6. After verifying majority, Leader, member list, and metadata revision convergence, submit the node metadata.

If any node fails during paired installation, the platform will stop the new installation control role in reverse order; if Raft member submission fails midway, the already joined voter will be removed in reverse order, and even members will not be left. If the target state is unclear, the task is marked as "Needs Review" and will not falsely report success.

Control node dynamically issued materials are delivered through the offline installation asset directory:

```text
assets/pki/api-issuer.crt       0644
assets/pki/api-issuer.key       0640 root:clusterguard
assets/pki/raft-issuer.crt      0644
assets/pki/raft-issuer.key      0640 root:clusterguard
```

The API issuer must be trusted by the trust chain pointed to by `tls_ca_file`, and the Raft issuer must be trusted by the trust chain pointed to by `consensus.tls_ca_file`. All four paths must be configured simultaneously; private keys must not be placed in browsers, task requests, audit, reports, or regular logs. After physical damage to a node, the original platform UUID is reused; if the native database identity changes due to reconstruction, it must go through a clear replacement/reconciliation process.

## 9. Operation Logs

"Operation Logs" is an independent menu and is not embedded in the operation page. Each record must display at least:

- Time
- Cluster name and UUID
- Original primary
- Target primary
- Operation type
- Operator or automated identity
- Execution mode
- Risk level
- Status
- Operation UUID, report UUID

The original return is folded by default and displayed after clicking. The original return must not leak passwords, sessions, Bearer tokens, one-time approvals, or Agent secrets.

The cluster field in the logs must display the fixed cluster name and must not incorrectly display hostname:port. Historical records should be linked by resource UUID, and even if the hostname or port changes later, they should be correctly restored.

An unreviewed `indeterminate` entry exposes **Mark reviewed**. Before submitting it, verify the actual database roles, replication path, business endpoint, and endpoint owner. The reviewed entry displays the reviewer and timestamp and no longer contributes to the control-plane review-required count. Its note is immutable, and all original execution, failure classification, plan, verification checks, and audit evidence remain intact.

## 10. Metrics and Monitoring

Read-only monitoring uses an independent monitoring token and does not use control tokens. Monitoring must cover at least:

- Control service, Leader, quorum, metadata revision
- Cluster health, primary, candidate nodes
- Replication delay and thread/process status
- VIP/service/Listener owner
- Active, failed, and indeterminate operations
- Agent reconcile status

Prometheus can read the monitoring interface exposed by the platform and does not require additional deployment of database exporters. Zabbix pulls the same health and performance data through the read-only JSON API.

## 11. Scheduled Shutdown and Automatic Recovery

Applicable to scenarios such as data center maintenance, whole machine migration, and business windows that require safely shutting down the entire MySQL master-slave cluster. Before shutdown, the platform automatically adds protection, and after reboot, systemd units automatically recover, with no manual intervention required throughout the process.

### 11.1 Two Shutdown Modes

| Mode | Behavior | Applicable Scenarios |
|------|----------|----------------------|
| `service` | Only stop MySQL service (first replicas, then primary), host remains on | Database software upgrade, configuration change, short downtime window |
| `poweroff` | All nodes power off in parallel | Data center power outage maintenance, rack migration |

### 11.2 Execute Shutdown

Daily operations use the console **Topology -> Power Lifecycle -> One-Click Shutdown**. The default selects
`service`, only stopping the database service, keeping the server on. Logged-in platform administrators are issued and
consume short-term one-time approvals by the backend, and the approval key is not exposed in the browser.

Automated commands also go through the unified Power API; actual execution must explicitly provide a one-time approval token:

```bash
cgctl --server https://127.0.0.1:3000 --ca-file /etc/clusterguard/tls/ca.crt \
  cluster shutdown --cluster <CLUSTER_DISPLAY_NAME> --mode service|poweroff \
  --approval-token <ONE_TIME_TOKEN>
cgctl --server https://127.0.0.1:3000 --ca-file /etc/clusterguard/tls/ca.crt \
  cluster shutdown --cluster <CLUSTER_DISPLAY_NAME> --mode service --dry-run
```

`--dry-run` only performs pre-checks, then automatically cancels the temporary lifecycle, without changing the database or host status.
There is no direct script entry that bypasses Safety Guard, locks, approvals, audit, and Agent whitelist.

Complete execution process (any step failure will interrupt and retain protection, see 11.4):

1. Refresh topology (discover), with each signed Agent atomically writing to
   `/etc/clusterguard/power-snapshots/<CLUSTER_UUID>.json` (directory 0700, file 0600).
   Multiple clusters on the same host will not overwrite each other's snapshots.
2. Freeze automatic recovery (recovery-freeze): automatic failover, reboot bootstrapping, manual switch all blocked.
3. Mark all instances as maintenance.
4. Execute `SET PERSIST_ONLY read_only=ON; SET PERSIST_ONLY super_read_only=ON` on all nodes: not effective during runtime, persisted after reboot, to prevent accidental writes.
5. Stop by mode: `service` stops replicas first, then the primary; `poweroff` powers off all nodes in parallel.

### 11.3 Automatic Recovery After Reboot

Two systemd units are enabled by default with installation. During boot, it scans each cluster's snapshot directory; if the directory is empty, it skips directly:

- `clusterguard-cluster-restore.service`: First confirms with the online control plane that this snapshot still corresponds to an ongoing
  planned recovery, then starts the local database service and waits for readiness. MySQL only allows the immutable instance ID
  specified in the snapshot to release the persistent and runtime read-only status of the primary; ordinary reboots encountering old snapshots will reject execution before changing roles.
  Then reports boot/recovering and triggers topology discovery.
- `clusterguard-cluster-finalize.service`: Waits for control plane health (`/healthz`, maximum 60 seconds),
  then polls the topology until the primary instance is healthy (every 5 seconds, maximum 600 seconds); after confirming health, completes verification,
  releases recovery freeze and cluster maintenance protection, and writes `recovered_at` only in the corresponding cluster snapshot.

Note: `poweroff` can only safely power off, software cannot power on a machine that has already physically powered off. Automatic power-on requires
VMware auto-start/API, IPMI/iDRAC/iLO, Wake-on-LAN, or BIOS power-on on resume; once the operating system is
started, ClusterGuard will automatically complete database, topology, replication, and protection status recovery.

### 11.4 Timeout and Fail-Closed

- If finalize times out and the primary is still not healthy: **Protection is not released**, the script outputs CRITICAL logs and exits normally, waiting for manual handling.
- After manually confirming that the problem has been resolved, protection can be manually released:

```bash
curl -sk -X POST -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"freeze":false}' https://127.0.0.1:3000/api/v1/clusters/<CLUSTER_UUID>/recovery-freeze
```

- After deleting the snapshot, the two recovery units automatically become no-ops; repeating restore/finalize on an already finalized snapshot is also an idempotent no-op.

### 11.5 View Recovery Status

```bash
cgctl --server https://127.0.0.1:3000 --ca-file /etc/clusterguard/tls/ca.crt \
  cluster restore-status [--cluster <CLUSTER_UUID>]
```

Output whether the snapshot exists, cluster information, `recovered_at`, recovery freeze status (`frozen`/`active`), each instance's role/health/replication delay/maintenance mark, and recovery advice based on status. If `--cluster` is not specified, the local snapshot's cluster UUID is used by default.

## 12. Backup and Recovery

Daily backup:

- `/etc/clusterguard/`, key and password files separately encrypted
- `/var/lib/clusterguard/metadata.json`
- Raft data directory
- Operation audit and report export
- Current RPM, SHA-256, configuration version, and BUILD-INFO

The database itself must still use the formal backup system of each engine; control plane metadata backup cannot replace database backup.

Recovery requirements:

- Single control node configuration recovery can use `config-backups`
- Raft metadata recovery must stop all control nodes and use a consistent recovery point
- Do not hot copy an old Raft directory from one node to a running cluster
- After recovery, first verify Leader/quorum, then enable any automatic switch or VIP reconcile

## 13. Emergency Handling

### 13.1 Unknown Leader or Lost Quorum

- Stop all manual and automatic database switches
- Check 10009/TCP, certificates, clocks, and voter status
- After recovering the majority of nodes, confirm mutation authority
- Prohibit bypassing gates on followers

### 13.2 Dual VIP or Writer endpoint Misalignment

- Immediately isolate business write traffic
- Execute endpoint owner detection on all listed nodes
- Remove VIP/Listener ownership on non-primary nodes
- Confirm the unique writable primary and reacquire the majority lease
- Restore business after completing verification

### 13.3 Operation Timeout or Uncertain Status

- Do not repeatedly click continuously
- Query using operation UUID or idempotency key
- Check the real database role and endpoint owner
- Handle based on verification failed checks
- Only re-initiate after confirming that the previous operation has not executed or has safely ended
- Prefer running `verify` again while the live topology still matches the original plan
- When the old plan is stale but the site inspection is complete, record an operator review in Operation Log; review is not success

### 13.4 Missing Database Client

- Control plane should return a clear block
- Install a matching version client from an approved offline source
- Execute `clusterguard --check-config`
- Restart follower for verification and then roll out other control nodes

### 13.5 Lost Administrator Password

- Pause control plane changes
- Backup metadata and Raft
- Use local one-time recovery artifact
- Keep majority nodes using the same artifact
- Immediately change the password after logging in and check security audit

## 14. Duty Checklist

Daily:

- Leader, quorum, readyz
- All cluster primaries and Writer endpoints are consistent
- Replication delay and abnormal threads
- Failed or indeterminate operations
- Agent reconcile and certificate expiration

Weekly:

- Execute read-only health check and candidate evaluation
- Export audit, report
- Verify backup readability
- Check database account and SSH key expiration time

Monthly:

- Execute planned switch and old primary recovery in test environment
- Verify control node rolling restart
- Verify metadata hostname/IP/port reconcile
- Conduct a recovery drill with one control plane backup and one database backup
- Review least privilege, network ACL, and automatic switch strategy

## 15. Software Updates and Patches

Do not run `rpm -Uvh` concurrently on all controllers. An official update uses
a signed `.cgupgrade` update package, rolls followers, data-only nodes, and the Leader in that
order, and rechecks the version contract, Raft quorum, active work, and
maintenance state at every step. Databases continue running while all
ClusterGuard mutation ingress is held by the shared maintenance gate.

```bash
sudo clusterguard-upgrade \
  --package ./clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /root/.ssh/clusterguard_update \
  --known-hosts /etc/clusterguard/ssh_known_hosts \
  --execute
```

After host loss or network interruption, rerun the same patch with `--resume`.
Maintenance is released only after a complete update or complete rollback is
verified. See the [Version Update and Rollback Guide](update-and-patch.md) for preparation,
inspection, planning, rollback, and production admission requirements.
