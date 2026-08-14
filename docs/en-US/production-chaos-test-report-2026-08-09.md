# ClusterGuard HA Pre-Production Chaos Test Report

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/production-chaos-test-report-2026-08-09.md)
<!-- /LANGUAGE-SWITCH -->

- Date: 2026-08-09
- Environment: `192.168.102.152`, `192.168.102.153`, `192.168.102.154`
- Control Plane: 3-node Raft
- Focus Clusters: MySQL 8.0.44 (3306), MySQL 8.4.10 (3384), and MySQL 9.7.1 (3397)
- Conclusion: This round of software test matrix passed; production deployment should still configure external fencing and complete production network, storage, and backup recovery special acceptance.

## 1. Test Objectives

This round verifies the following production invariants:

1. At any time, each database cluster can have at most one writable primary.
2. Each HA VIP can be held by at most one host, and must follow the current primary.
3. When the control plane has no quorum, cannot confirm fencing, or the state is uncertain, it must fail-closed.
4. After a failed node recovers, it should remain read-only until verified and then rejoin the replication topology.
5. Concurrent operations, duplicate requests, and expired plans in the same cluster must not cause duplicate execution.
6. After node restarts, the control plane, Agent, database roles, replication links, and VIPs should automatically converge.

## 2. Test Matrix and Results

| Scenario | Execution Method | Result |
|---|---|---|
| Power off and recovery of all three nodes | Continuously power off all VMs in three rounds and recover with different boot orders | Passed; Raft recovered 3 voters, database roles, replication, and VIPs automatically converged, no dual primary or dual VIP |
| Current primary network partition | Isolate the current primary of 3306 for about 90 seconds | Passed; the node locally isolated and released VIP after about 34 seconds; no unsafe promotion due to lack of external fencing; VIP and write permissions were regained after about 16 seconds upon network recovery |
| 9.7 current primary network partition | Bidirectionally isolate the current primary of 3397 from the other two nodes | Passed; all three instances were read-only and VIPs were released; no promotion occurred for the old primary that was confirmed to be isolated; after cleanup rules, it was restored as the sole primary and VIP |
| Leader and primary simultaneously partitioned | Isolate `.153`, which was concurrently serving as Raft Leader and 3384 primary, for about 75 seconds | Passed; `.154` became Leader within 5 seconds, 2 nodes maintained quorum; isolated node self-isolated, and converged safely upon recovery |
| Replica storage unavailable | Stop `.154:3306`, change `/data/mysql8/data`'s permissions to `000`, and then start | Passed; the instance became unavailable and lost candidacy, pre-check failed, and real execution returned HTTP 409 before the change |
| 8.4 primary storage read-only | Bind-remount the data directory of the current primary as read-only, and systemd enters no PID auto-restart | Passed; the faulty node confirmed no surviving database processes and completed a safe takeover; after storage recovery, the old primary incrementally reattached |
| Storage recovery and catch-up | Restore directory permissions and services, and write 500 rows on the primary during the outage | Passed; after replica recovery, IO/SQL threads ran, delay zeroed out, 500 rows fully caught up, and temporary databases were cleaned |
| 8.4 binlog gap | Purge old primary binlog required for catch-up during the outage | Passed; `required_binlog_available` and `rebuild_required` blocked incremental reattachment, and automatic full rebuild succeeded |
| 9.7 errant GTID | Create a transaction that does not exist on the current primary by isolating the old primary | Passed; GTID subset check failed, automatic full rebuild occurred, and forked transactions were not brought back into the current topology |
| Concurrent switchover within the same cluster | Submit a switchover to two different candidates for 3306 simultaneously | Passed; one operation succeeded, one was blocked by HTTP 409, and only one primary change occurred |
| Concurrent submission with the same idempotency key | Two requests use the same idempotency key | Passed; share the same operation ID, one executed, one returned `operation is running`, and only one set of execution/verification/report was generated |
| Cross-cluster concurrent switchover | 3306 and 3384 operated simultaneously | Passed; both 8.0 operation `7b112de1-...` and 8.4 operation `e34b49d2-...` succeeded, and all post-execution verifications passed |
| Real console switchover | Lock and execute a coupled switchover of the 3306 primary and VIP in the Web console | Passed; all five stages succeeded, operation logs recorded source/target/time, original reports could be expanded, and final topology verification passed |
| Final stable observation | Continuously collect roles, read-only status, replication threads, links, and delays for 8.0, 8.4, and 9.7 for 60 seconds | Passed; all seven sampling points were single primary, dual read-only replicas, threads running, links healthy, and maximum delay 0 seconds |

## 3. Issues Found and Resolved in This Round

### Console did not handle safe `stale_plan`

During cross-cluster concurrency, the topology version may change after the plan was generated but before execution was submitted. The backend correctly blocked with `stale_plan`, but the console previously only displayed failure, requiring the operator to click again.

After the fix, the console automatically rebuilds the plan and retries only if all the following conditions are met:

- HTTP 409;
- `failure_class=stale_plan`;
- `status=blocked`;
- Stage is `precheck` or `plan`;
- `committed` is not `true`;
- Maximum of 3 retries;
- Always retain the user's originally selected target node;
- Each time use a new idempotency key.

Execution stage errors, submitted operations, `indeterminate`, general 409, and other failures do not automatically retry.

After deployment, a real console switchover for 3306 completed successfully with the result `operation completed and verification passed`. Operation logs recorded:

- Time: 2026-08-09 19:18:59 CST
- Source node: `orch-mysql03:3306`
- Target node: `orch-mysql01:3306`
- Status: Success

### Old primary recovery first click reads old snapshot

When the old primary service just recovers, the console may still hold the topology snapshot from before the outage, leading to the first click on "One-click recovery to slave" erroneously reading the old primary's read-only status and being blocked by pre-check.

After the fix, old primary recovery now always first calls the current cluster's discover, then reloads the selected cluster, and only then performs the recovery pre-check. This sequence is covered by console contract testing; if discover or reload fails, recovery does not proceed.

### Manual deployment artifact architecture check

During testing, macOS build artifacts were mistakenly copied to Linux nodes, and systemd rejected startup with `203/EXEC`. Raft majority and database services were unaffected, and the following parameters were used to rebuild and roll out again:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o clusterguard ./cmd/clusterguard
```

After the three-node final rolling deployment, the binary SHA-256 was consistent:

```text
clusterguard       d9a4d49c2e02d624307b8d2219cc70911cde5bbf63a7e52f8a9d4e70af4c1214
clusterguard-agent 29791923d37286172f23c923a61d9227c1d9d98360959069ba843baa959434fb
```

Formal delivery must use the RPM/offline package build process and verify ELF architecture and digest before deployment, prohibiting direct copying of default artifacts from development machines.

## 4. Final On-site Status

### Control Plane

- Current Leader: `192.168.102.154`
- Raft: 3 voters, quorum confirmed
- `mutation_authority=true`
- `snapshot_cas_active=true`
- `commit_index=applied_index`
- `ready=true`
- Active operations: 0
- Active node tasks: 0
- `clusterguard-ha.service`: All three are active/enabled
- `clusterguard-agent.service`: All three are active/enabled
- `clusterguard-agent-reconcile.timer`: All three are active/waiting/enabled
- `systemctl --failed`: All three are empty

There are still 15 historical `indeterminate` operations and 14 historical
`indeterminate` node tasks in the test metadata, with no active execution currently. They should be retained for audit or completed one by one before production migration, and should not be disguised as zero by deleting audit records.

### MySQL 8.0.44 / 3306

- Primary: `192.168.102.153` (`orch-mysql02:3306`), writable
- Replicas: `.152`, `.154`, both `read_only=true`, `super_read_only=true`
- Both replicas' IO/SQL threads are running, delay 0
- Semi-synchronous master status is normal, client count 2
- VIP: `192.168.102.155`, only held by `.153`
- VIP handshake: MySQL protocol 10, server `8.0.44`

### MySQL 8.4.10 / 3384

- Primary: `192.168.102.154` (`orch-mysql03:3384`), writable
- Replicas: `.152`, `.153`, both `read_only=true`, `super_read_only=true`
- Both replicas' IO/SQL threads are running, delay 0
- Semi-synchronous master status is normal, client count 2
- VIP: `192.168.102.160`, only held by `.154`
- VIP handshake: MySQL protocol 10, server `8.4.10`

### MySQL 9.7.1 / 3397

- Primary: `192.168.102.152` (`orch-mysql01:3397`), writable
- Replicas: `.153`, `.154`, both `read_only=true`, `super_read_only=true`
- Both replicas' replication threads are running, delay 0
- VIP: `192.168.102.164`, only held by `.152`
- Deterministic data on all three nodes is 2005 rows, and the digest calculated by
  `SUM(CRC32(CONCAT(id,marker,HEX(payload))))` is all `4304498633408`

Under the same formula, the three replicas of 8.0 are all 2007 rows, with digest `4323827613648`; the three replicas of 8.4 are all 2006 rows, with digest `4341221981770`.

Oracle, PostgreSQL, and UPSQL currently registered on the platform are healthy; SQL Server cluster is currently degraded (database probe failed), not part of this round's MySQL production matrix, and must be addressed separately before formal platform deployment.

## 5. Code Gatekeeping

All the following checks passed:

```bash
go test ./... -count=1
go test -race ./adapters/mysql ./internal/api ./internal/agent \
  ./internal/coordination ./internal/lifecycle ./internal/workflow -count=1
go build ./...
go vet ./...
git diff --check
```

Console contract testing also covers the safety retry boundary of `stale_plan` and the forced refresh discovery before old primary recovery; execution stage, submitted, uncertain operations, and discover failure will not continue to execute changes.

## 6. Production Deployment Boundaries

This round proves that ClusterGuard HA meets the safety-priority invariants in the current three-node lab, but this does not mean all production risks are zero.

Before deployment, the following must be completed:

1. Configure BMC/IPMI, storage-side, or virtualization-side fencing independent of the business network. When there is no reliable external fencing, fully isolated primary will choose to stop write services and will not risk promoting a new primary; this avoids brain split but sacrifices availability.
2. Re-run unidirectional packet loss, bidirectional isolation, jitter, and delay tests on production switches, firewalls, and real links.
3. Re-run read-only file system, I/O hang, space exhaustion, inode exhaustion, and fsync delay tests on production storage types.
4. Perform one full machine recovery and point-in-time recovery using production backups to verify RPO/RTO, not just replication recovery.
5. Use a new production metadata storage or form a disposal conclusion for each of the 15 historical `indeterminate` test records.
6. Repair and accept the current SQL Server probe degraded status separately to avoid platform-wide deployment with issues.

## 7. Judgment

**The current software versions of MySQL 8.0, 8.4, and 9.7 passed the shutdown recovery, network partition, storage failure, concurrent operation, old primary incremental reattachment, and full rebuild tests in this lab. The production access judgment is "software gate passed, external fencing and production infrastructure special acceptance pending."**
