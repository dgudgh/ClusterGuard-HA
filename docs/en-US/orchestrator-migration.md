# Migrating from Orchestrator to ClusterGuard HA

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/orchestrator-migration.md)
<!-- /LANGUAGE-SWITCH -->

This guide migrates an existing MySQL cluster from Orchestrator management to ClusterGuard HA. The migration transfers control authority for the database cluster. It does not import the previous controller's code, APIs, or metadata.

ClusterGuard HA is an independent control plane. It does not read or preserve Orchestrator backend tables, configuration, recovery records, Raft state, hooks, CLI behavior, or APIs. Existing MySQL data does not need to be reinstalled. ClusterGuard discovers the registered inventory and binds each existing instance by MySQL `server_uuid`.

## 1. Non-negotiable migration rule

Only one system may hold mutation authority for a database cluster at any time. Mutation authority includes:

- automatic failover;
- planned switchover and primary promotion;
- replication source changes;
- former-primary recovery;
- VIP bind, move, or removal;
- database role mutation hooks.

Both controllers may perform read-only discovery during the observation period. They must never both execute recovery or VIP changes. If the old controller cannot be proven unable to mutate the cluster, ClusterGuard HA must remain in observation mode with automatic and manual mutations disabled.

## 2. Identify the application connection path

| Current application path | Migration treatment |
| --- | --- |
| Application connects to a VIP | Retain the VIP. The old system owns it before cutover; only ClusterGuard Agent owns it after cutover. |
| Application connects through a separate proxy or load balancer | Migrate database HA only. Change the proxy in a separate controlled change. |
| Application connects directly to a physical primary IP | Establish a stable HA endpoint first, then change connection strings in batches. Never use a controller address as the database endpoint. |

When the existing VIP is retained, application connection strings normally remain unchanged. The migration changes ownership of the database role and VIP, not the business endpoint.

## 3. Phase 0: freeze and back up the old environment

Create a migration inventory for each cluster:

- cluster name, full MySQL version, and port;
- current primary, all replicas, replication sources, and candidate priority;
- `server_uuid`, `server_id`, hostname, IP address, and port for every instance;
- GTID, binlog, replication thread, lag, and errant transaction state;
- VIP, interface, CIDR, current VIP owner, and ARP behavior;
- Orchestrator controllers, leader, backend database, and recovery settings;
- failover, graceful takeover, and recovery hooks;
- custom VIP scripts, systemd services and timers, cron jobs, Keepalived, and other automation;
- database mutation accounts and allowed source addresses used by Orchestrator.

Retain read-only rollback artifacts. Adjust paths for the site:

```bash
mysqldump --single-transaction <ORCHESTRATOR_BACKEND_DATABASE> \
  > orchestrator-backend-before-clusterguard.sql

tar -C / -czf orchestrator-config-before-clusterguard.tar.gz \
  etc/orchestrator.conf.json \
  etc/systemd/system
```

Do not move unencrypted passwords, private keys, or control tokens outside the protected site backup.

Capture database evidence as well:

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
SELECT @@global.gtid_mode, @@global.enforce_gtid_consistency;
SELECT @@global.read_only, @@global.super_read_only;
SHOW REPLICA STATUS\G
```

Use `SHOW SLAVE STATUS\G` on MySQL 5.7.

## 4. Phase 1: deploy an independent ClusterGuard control plane

For an existing production database, install only the three-controller control plane first. Do not invoke database installation or synchronization:

```bash
./install_clusterguard.sh \
  -l 192.0.2.21,192.0.2.22,192.0.2.23 \
  -u root \
  -ld /var/lib/clusterguard \
  --engine none \
  --control-only \
  --known-hosts /secure/clusterguard/control-plane/known_hosts \
  --state-file /secure/clusterguard/control-plane/deployment-state.json \
  --secrets-file /secure/clusterguard/control-plane/deployment-secrets.env \
  --work-dir /secure/clusterguard/control-plane/site \
  --plan
```

Review the plan, then replace `--plan` with `--execute`. The voter count must be odd and at least three.

Onboarding an existing database must not:

- initialize MySQL;
- erase or overwrite an existing data directory;
- copy `auto.cnf`;
- rebuild replication automatically;
- bind the VIP automatically;
- enable automatic failover immediately.

## 5. Phase 2: prepare the existing MySQL cluster

### 5.1 Verify native database identity

Run on every instance:

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
```

Requirements:

- each `server_uuid` is unique;
- each `server_id` is unique;
- exactly one primary is writable;
- every replica follows the same current primary;
- GTID configuration satisfies the site switchover policy;
- old and new endpoints for an instance resolve to the same `server_uuid`.

ClusterGuard stores an immutable platform `resource_id` and identifies a MySQL instance by `server_uuid`. A hostname, IP address, or port change updates the endpoint and retains the previous address as an alias. It must not create another logical node.

### 5.2 Create purpose-specific accounts

Use separate discovery, operation, and replication accounts. Replace the source network with the approved controller and data-node ranges:

```sql
CREATE USER 'cg_discovery'@'192.0.2.%' IDENTIFIED BY '<DISCOVERY_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'cg_discovery'@'192.0.2.%';

CREATE USER 'cg_operator'@'192.0.2.%' IDENTIFIED BY '<OPERATION_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT, CONNECTION_ADMIN,
  SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.*
  TO 'cg_operator'@'192.0.2.%';

CREATE USER 'cg_replication'@'192.0.2.%' IDENTIFIED BY '<REPLICATION_PASSWORD>';
GRANT REPLICATION SLAVE ON *.* TO 'cg_replication'@'192.0.2.%';
```

The MySQL 5.7 operation account needs source-restricted `SUPER`. See the [Database Preparation Manual](database-preparation.md) for version differences and TLS requirements.

Store passwords in a protected controller environment file and reference them through `password_env` in `/etc/clusterguard/clusterguard.json`. The browser registration dialog does not collect database passwords, and the discovery API rejects request-body credential overrides.

## 6. Phase 3: register the authoritative inventory

In the console, open **Cluster Management -> Add Cluster**:

1. Select MySQL.
2. Enter a stable, unique display name.
3. Register every primary and replica endpoint, not only the current primary.
4. Register an immutable node name and platform UUID for each physical host.
5. Save and run discovery once.
6. Compare discovered `server_uuid`, role, source, version, and lag with the migration inventory.

Equivalent API example:

```bash
curl -sS -X POST https://<controller>:3000/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-mysql",
    "engine":"mysql",
    "endpoints":[
      {"hostname":"mysql-a","ip_address":"192.0.2.31","port":3306},
      {"hostname":"mysql-b","ip_address":"192.0.2.32","port":3306},
      {"hostname":"mysql-c","ip_address":"192.0.2.33","port":3306}
    ]
  }'
```

Refresh discovery with an empty object only:

```bash
curl -sS -X POST \
  https://<controller>:3000/api/v1/clusters/<CLUSTER_UUID>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

Do not import Orchestrator backend tables and do not use `hostname:port` as a ClusterGuard resource key.

## 7. Phase 4: run parallel read-only observation

While the old system still owns execution authority, ClusterGuard performs discovery, health, topology, and candidate assessment only. The observation period should cover at least one business peak and one backup window.

| Check | Required result |
| --- | --- |
| Current primary | Both systems identify the same `server_uuid`. |
| Replica set | No missing instance and no cross-cluster node. |
| Replication source | Every replica follows the current primary. |
| Replication state | Thread, GTID, and lag evidence agree. |
| Candidate ranking | Version, lag, GTID, and maintenance decisions are explainable. |
| VIP owner | Only the current primary owns the VIP. |
| Metadata | Hostname, IP address, and port match the authoritative inventory. |

Do not proceed when there are duplicate resources, identity mismatches, unregistered endpoints, multiple writable primaries, multiple VIP owners, or incomplete probe coverage.

## 8. Phase 5: transfer exclusive execution authority

During a maintenance window, migrate one cluster at a time:

1. Freeze application topology changes and planned switchovers.
2. Verify one ClusterGuard Raft leader and a confirmed majority.
3. Stop recovery authority on every Orchestrator process.
4. Stop and disable old VIP hooks, timers, cron jobs, Keepalived, and move scripts.
5. Prove the old system can no longer promote, reparent, or move the VIP.
6. Leave the current MySQL roles and VIP location unchanged.
7. Install the restricted ClusterGuard Agent on data nodes and register existing database paths and services. Do not initialize the database.
8. Register the existing VIP, interface, and CIDR as the ClusterGuard HA endpoint, then enable ownership coordination.
9. Rediscover and verify VIP uniqueness, primary writability, replica following, and Agent coverage.
10. Enable controlled manual switchover first. Enable automatic failover only after separate site qualification.

Old service and hook names vary. Inventory them before stopping anything:

```bash
systemctl list-unit-files | grep -Ei 'orchestrator|vip|reconcile|keepalived'
systemctl list-timers --all | grep -Ei 'orchestrator|vip|reconcile'
crontab -l
ps -ef | grep -Ei '[o]rchestrator|[v]ip.*(move|reconcile)'
```

Typical commands must be adjusted to local names:

```bash
systemctl stop orchestrator.service
systemctl disable orchestrator.service
systemctl stop <OLD_VIP_TIMER_OR_SERVICE>
systemctl disable <OLD_VIP_TIMER_OR_SERVICE>
```

Removing an old controller from a load balancer is not sufficient. Verify on every old controller that its process, hooks, timers, and cron jobs cannot mutate the cluster. Temporarily revoke its database mutation account or source network as a second barrier when appropriate.

## 9. Phase 6: qualify the first controlled switchover

Run the first switchover in a low-traffic window:

1. Refresh topology and pin all decisions to the same observation.
2. Select a candidate with no GTID gap, healthy replication threads, and acceptable lag.
3. Unlock the operation and execute the coupled primary and VIP switchover.
4. Wait for the full workflow. A submitted request is not a successful operation.
5. Verify the new primary is writable, the former primary is read-only, and every replica follows the new primary.
6. Verify exactly one VIP owner and that it is the new primary.
7. Verify lock release, audit persistence, and report creation.

The required workflow is:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK
-> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

For `blocked`, `failed`, or `indeterminate`, do not click repeatedly. Query the operation UUID and establish the real database state first.

## 10. Phase 7: enable automatic failover separately

Automatic failover is a separate production change. Confirm at least:

- three or more odd-numbered controllers form a stable Raft majority;
- Agent runs on every data node and enforces local read-only isolation after authority expires;
- current primary, candidate, and VIP owner are correct;
- the default three-observation, three-second stable-failure evidence window and the separate 15-second Agent isolation fence have been tested on site;
- application writer-endpoint RTO has been measured with bounded client connection timeouts and retries;
- network partitions, Agent loss, and controller quorum loss fail closed;
- a recovered old primary can only enter the former-primary recovery workflow;
- the site has decided whether BMC, PDU, cloud, or hypervisor fencing is required.

Keep `automatic_failover_enabled=false` until qualification passes. Change the controller configuration and perform a controlled rolling restart to enable it. Do not rerun the database installer to change this flag.

## 11. Rollback

### 11.1 ClusterGuard has not changed database roles

During observation-only rollback:

1. Disable ClusterGuard automatic failover and HA endpoint coordination.
2. Stop ClusterGuard Agent VIP reconciliation.
3. Verify the VIP and database roles still match the pre-cutover evidence.
4. Restore Orchestrator services, hooks, and VIP automation.
5. Let the old system rediscover the complete topology before restoring mutation authority.

### 11.2 ClusterGuard has already switched the primary

Do not simply start Orchestrator. Its stored view may still treat a historical primary as the recovery target or writer endpoint.

Complete one of these first:

- retain the ClusterGuard topology, clear stale old-system discovery, and rediscover every node;
- use ClusterGuard to switch back under control, then verify replication and VIP ownership; or
- reconcile old-system metadata in a maintenance window and verify it identifies the real current primary.

Restore old-system mutation authority only after database roles, replication sources, VIP ownership, and the old-system view agree. A rollback still permits only one mutation controller.

## 12. Acceptance checklist

- [ ] ClusterGuard shares no code, backend tables, or runtime state with the old controller.
- [ ] Every instance is bound by `server_uuid` to one platform `resource_id`.
- [ ] Hostname, IP address, and port are mutable endpoints; previous coordinates are aliases.
- [ ] The authoritative inventory contains every instance and blocks out-of-inventory control.
- [ ] Old controllers, hooks, timers, cron jobs, and VIP automation are stopped.
- [ ] Exactly one primary is writable.
- [ ] Every replica follows the current primary with healthy replication threads.
- [ ] Exactly one VIP owner exists and it is the current primary.
- [ ] ClusterGuard leader and quorum are healthy.
- [ ] The first planned switchover produces complete audit and report evidence.
- [ ] The former primary can be recovered as a replica.
- [ ] Automatic failover is enabled only after separate site qualification.
- [ ] Rollback steps and responsible operators have been rehearsed.

## 13. Capability mapping

| Historical Orchestrator capability | ClusterGuard HA capability |
| --- | --- |
| Topology discovery | Inventory-scoped discovery and immutable identity binding |
| Candidate ranking | Adapter candidate assessment with risk evidence |
| Graceful takeover | Coupled controlled primary and VIP switchover |
| Recovery | Unified failover workflow and former-primary recovery |
| Hook | Restricted Agent and signed execution contract |
| Audit | Raft-replicated audit, operation log, and reports |
| `hostname:port` identity | Platform UUID, native database identity, and endpoint aliases |

This is not an API replacement or an in-place upgrade. A historical project is migrated by registering and rediscovering its databases, validating identity and topology, and transferring exclusive execution authority during a controlled maintenance window.
