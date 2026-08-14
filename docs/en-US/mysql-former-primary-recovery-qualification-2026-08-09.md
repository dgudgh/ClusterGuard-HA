# MySQL Former Primary One-Click Recovery Production Acceptance Report

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/mysql-former-primary-recovery-qualification-2026-08-09.md)
<!-- /LANGUAGE-SWITCH -->

- Date: 2026-08-09
- Nodes: `192.168.102.152`, `192.168.102.153`, `192.168.102.154`
- Version: MySQL 8.0.44, MySQL 8.4.10, MySQL 9.7.1
- Conclusion: GTID incremental resynchronization after process failure, full rebuild after binlog gap, and full rebuild after errant GTID fork have all passed real three-node verification.

## 1. One-Click Recovery Behavior

The console "One-Click Recovery to Slave" executes the following fixed process:

1. Proactively refresh cluster discovery, read the latest read-only and replication status of the former primary after startup.
2. Validate the fixed resource ID of the former primary, MySQL `server_uuid`, cluster manifest, and current primary identity.
3. Compare the GTID sets of the former primary and the current primary.
4. Determine whether the binlog required for the former primary to catch up is still available from the current primary.
5. When GTID is a subset and binlog is complete, perform incremental resynchronization.
6. When binlog has been purged or there are errant GTID, automatically perform full rebuild and replication recovery.
7. Verify read-only status, replication threads, replication source, delay, single primary, and VIP uniqueness of the former primary.
8. Write audit, verification, and report; block if any critical evidence is missing.

The platform does not use `sql_slave_skip_counter`, GTID injection, or forced reset to mask data forks.

## 2. Real Test Matrix

| Version | Failure and Data Conditions | Recovery Path | Result |
|---|---|---|---|
| 8.0.44 | Current primary process stops directly | GTID incremental resynchronization after automatic failover | Passed; a new unique primary is formed in about 65-70 seconds, and the former primary remains read-only and completes incremental resynchronization after startup |
| 8.4.10 | Write continues during failure and binlog required for former primary to catch up is purged | Automatically detects `required_binlog_available=fail` and performs full rebuild | Passed; unsafe incremental resynchronization was not attempted, full synchronization, replication recovery, and verification were completed |
| 9.7.1 | Errant GTID is written on the former primary that does not exist on the current primary | Automatically detects GTID is not a subset and performs full rebuild | Passed; forked behavior was not introduced into the new topology, and the former primary was rebuilt based on the current primary |
| 8.4.10 | Data directory of the primary becomes read-only, systemd is in `activating/auto-restart` and no live PID exists | Agent determines the database has stopped and allows isolated failover | Passed; after fixing the false block of empty auto-restart state, failover and former primary resynchronization were completed |
| 8.0/9.7 | Current primary is network-isolated from the other two nodes | Writing stops without external fencing, and the former primary that is isolated but not confirmed is not promoted | Passed; all nodes are read-only and VIP is released, and after network recovery, it converges to a single primary |
| 8.0 | Current host is completely powered off | Automatic promotion is blocked without VMware/BMC fencing evidence | Passed through security gate; after restoring the virtual machine, the service automatically starts and keeps the former primary read-only |

## 3. Concurrency and Data Consistency

In two concurrent switch requests in the same cluster, only one obtains the operation lock and succeeds, and the other is blocked with HTTP 409. Switches for 8.0 and 8.4 in different clusters are executed simultaneously and both pass:

- 8.0 operation: `7b112de1-537a-4157-ae83-74d2e4372063`
- 8.4 operation: `e34b49d2-36b1-4948-b493-f202a051786b`
- Both executions are `succeeded`
- Both verifications are `passed=true`
- Each operation passes source read-only, target writable, replication resynchronization, semi-synchronous, single primary, and VIP owner verification

Final three-replica deterministic data verification. Summary is uniformly calculated as `SUM(CRC32(CONCAT(id,marker,HEX(payload))))`, which facilitates direct recalculation on any replica:

| Cluster | Rows per Node | Summary per Node | Current Primary | VIP Owner |
|---|---:|---:|---|---|
| 8.0 / 3306 | 2007 | `4323827613648` | `.153` | `.153` holds `.155` |
| 8.4 / 3384 | 2006 | `4341221981770` | `.154` | `.154` holds `.160` |
| 9.7 / 3397 | 2005 | `4304498633408` | `.152` | `.152` holds `.164` |

Other nodes in the three clusters are `read_only=1`, `super_read_only=1`, and there are no dual primaries, no dual VIPs, no read-only test mounts, and no network failure rules at the end of the test.

In the final closing phase, another 60 seconds and 7 sampling points of continuous observation were executed. Each round met the following: exactly one primary per cluster, two replicas, replication IO/SQL threads running, replication link healthy, maximum delay 0 seconds, and the primary identity did not drift in the background.

## 4. This Round of Code Fixes

1. MySQL probe collects `gtid_purged`, and the recovery pre-check adds `former_primary_gtid_subset`, `required_binlog_available`, and `rebuild_required`.
2. The console automatically selects incremental resynchronization or node full rebuild based on the pre-check.
3. The console proactively executes discover before recovery decision to eliminate the problem of reading old snapshots when the former primary is first clicked after startup.
4. MySQL 9.7 replication status collection uses compatible vertical output and does not send `\\G` to the client.
5. Agent identifies "systemd auto-restart, MainPID=0, ControlGroup is empty" as stopped; if any live PID or cgroup exists, it still fails closed.

## 5. Build and Testing

```text
go test ./... -count=1                                      PASS
go test -race ./adapters/mysql ./internal/api \
  ./internal/agent ./internal/coordination \
  ./internal/lifecycle ./internal/workflow -count=1          PASS
go vet ./...                                                 PASS
go build ./...                                               PASS
git diff --check                                             PASS
```

Linux binary SHA-256 after rolling deployment of three control planes:

```text
clusterguard       d9a4d49c2e02d624307b8d2219cc70911cde5bbf63a7e52f8a9d4e70af4c1214
clusterguard-agent 29791923d37286172f23c923a61d9227c1d9d98360959069ba843baa959434fb
```

During the rolling deployment, 3 voters, quorum confirmed, mutation authority, and `commit_index=applied_index` were maintained.

## 6. Production Boundary

This result proves the software's safe handling of the above failures in the current laboratory, but it does not indicate that infrastructure risks are zero. If the primary that is completely disconnected or completely powered off cannot be confirmed to have shut down through an independent channel, ClusterGuard HA will choose to stop writing rather than risk creating a dual primary. In production environments, independent BMC/IPMI, virtualization platforms, or storage fencing must be integrated while maintaining this gate to simultaneously achieve automatic takeover and split-brain protection.

Before going live, it is also necessary to retest I/O hang, full disk, full inode, and high fsync latency using production storage, and perform a bare-metal recovery and point-in-time recovery using production backup. Replication recovery cannot replace backup recovery acceptance.
