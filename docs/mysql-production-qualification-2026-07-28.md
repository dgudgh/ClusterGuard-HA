# ClusterGuard HA MySQL 8.0 / 8.4 Production Qualification

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](zh-CN/mysql-production-qualification-2026-07-28.md)
<!-- /LANGUAGE-SWITCH -->

Date: 2026-07-28
Environment: 192.168.102.152-154, three control nodes, three data nodes
Version: MySQL 8.0.44 (3306), MySQL 8.4.10 (3384)

## Conclusion

The current version has passed functional, failure, security, and data integrity verification and can enter controlled production trial operation. It is not defined as "unconditional production ready" until all the following deployment conditions are fully implemented:

1. In network-isolation scenarios without external STONITH or fencing, the system blocks takeover to prevent dual primaries. If the service must remain available in this scenario, integrate an independent power, virtualization, or switch-level fencing mechanism.
2. All six instances use semi-synchronous replication. Planned and unplanned failover verifies the new primary's semi-synchronous status and ACK clients before VIP migration. These tests found no loss of acknowledged transactions, but semi-synchronous replication does not guarantee an absolute RPO of zero across every failure mode.
3. The maximum measured client interruption for automatic fault recovery is 60.236 seconds (final concurrent fault validation, MySQL 8.0.44); the post-fix regression for cross-cluster head-of-line blocking measured 58.126 / 60.108 seconds, and the 15-minute fault-traversal run peaked at 64.625 seconds. If the production SLA requires automatic recovery RTO not to exceed 30 seconds, the current version does not meet the requirement and cannot be directly deployed.
4. Application connection pools must set connection and read/write timeouts, and must support reconnects and idempotent retries. VIP migration cannot repair an already established, stalled TCP session.
5. The laboratory console uses an untrusted self-signed certificate, and browser automation correctly refuses to bypass certificate validation. Production must use a client-trusted TLS certificate with the correct SANs, followed by a real console switchover acceptance test.

## Final Concurrent Failure Verification

Both primaries were hard-failed concurrently while 16 write streams remained active (eight per version). The former primaries were then restarted and recovered through the supported rejoin workflow.

| Version | Write Streams | Attempts | Confirmed | Failed Retries | Max Interruption | Integrity |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| MySQL 8.0.44 | 8 | 14,378 | 13,948 | 430 | 60.236s | Passed |
| MySQL 8.4.10 | 8 | 14,058 | 13,850 | 208 | 60.057s | Passed |

Integrity judgment:

- 16/16 reports were successfully generated.
- All streams `observed.count == acknowledged`.
- All stream numbers are continuous from 1 to `acknowledged`.
- 0 verification failures, 0 count inconsistencies, 0 sequence gaps.
- No interval was observed in which the VIP had moved while the former primary remained writable.

Final status:

- MySQL 8.0: 192.168.102.154 is the primary, VIP 192.168.102.155 is uniquely bound; the other two nodes are healthy replicas with 0 delay.
- MySQL 8.4: 192.168.102.153 is the primary, VIP 192.168.102.160 is uniquely bound; the other two nodes are healthy replicas with 0 delay.
- Both former primaries were restored through the supported rejoin workflow, and their replication IO/SQL threads are running.
- The three ClusterGuard control nodes are `active`, `live`, and `ready`.

## Defect Fixes and Regression in This Round

### Dual-Cluster Automatic-Recovery Head-of-Line Blocking

The recovery scheduler previously waited synchronously for one cluster's complete `RunOnce`, causing another cluster's recovery to wait behind it. The corrected behavior is:

- Each cluster independently executes the recovery cycle asynchronously.
- The same cluster avoids duplicate recovery with the `inFlight` status.
- Global recovery concurrency uses a bounded gate with a capacity of 4.
- A staggered dual-cluster failure test proves that the second cluster can enter its workflow before the first cluster completes.

Regression results after stopping both sets of primary databases post-fix:

| Version | Attempts | Confirmed | Failed Retries | Max Interruption | Integrity |
| --- | ---: | ---: | ---: | ---: | --- |
| MySQL 8.0.44 | 1,396 | 1,331 | 65 | 58.126s | Passed |
| MySQL 8.4.10 | 1,350 | 1,319 | 31 | 60.108s | Passed |

Both write streams satisfy `observed.count == acknowledged`, with uninterrupted sequence numbers from 1 through the last acknowledged transaction. After restart, each former primary remains at `read_only=1` and `super_read_only=1`, owns no VIP, and is then restored through the supported rejoin workflow.

### Semi-Synchronous Switch Gate

- Planned switchover and failover activate and verify semi-synchronous replication on the new primary before VIP transfer.
- If semi-synchronous activation fails, the new primary reverts to read-only and blocks VIP transfer.
- Smoke tests verify the primary's semi-synchronous source state, ACK-client count, and replica state on each secondary.

### Candidate and Approval Timing

- After manually stopping the SQL thread of the candidate node, pre-checks block before authorization and execution; the primary and VIP remain unchanged.
- After generating a one-time approval for a healthy candidate, stop its SQL thread; using an old approval results in `stale_plan`, with no role or VIP change.
- Regression tests cover automatic recovery scheduling, candidate blocking, and invalidation of stale approvals.

## Covered Failure Matrix

- Planned switchover, dual cluster simultaneous switchover, and intra-cluster concurrent switchover mutual exclusion.
- Primary process hard stop, concurrent hard stops of two primaries, former-primary recovery, and supported rejoin.
- Primary planned restart, secondary restart, full site MySQL cold shutdown and restart.
- ClusterGuard process SIGKILL, Raft Leader stop and re-election.
- Control node majority loss, single node real network isolation, and convergence after recovery.
- VIP uniqueness, strong binding between primary and VIP, restart write protection, and self-isolation without majority.
- Replication IO/SQL thread stop, candidate node blocking, and re-evaluation after recovery.
- GTID, replication position, candidate eligibility, former-primary rejoin, and replication-link verification.
- Switchover under 273/300 connection pressure.
- Simultaneous failure switchover under 8 concurrent write paths per version.
- MySQL persistent parameters, memory configuration, systemd stop semantics, and cold start roles.
- Semi-synchronous source/replica, ACK client, and rollback on activation failure before switchover.
- Candidate SQL-thread stop and stale-approval invalidation after a topology change.
- Recovery scheduling concurrency for staggered failures in dual clusters, avoiding head-of-line blocking across clusters.
- Three-node logs, failed unit, panic/fatal, and InnoDB damage keyword inspection.

## Key Real-World Testing

- 15-minute dual version failure traversal:
  - 8.0: 9,602 confirmed writes, maximum interruption 64.625s, integrity passed.
  - 8.4: 9,721 confirmed writes, maximum interruption 58.061s, integrity passed.
- Planned primary restart:
  - 8.0: maximum interruption 22.601s, integrity passed.
  - 8.4: maximum interruption 18.924s, integrity passed.
- Connection pressure switchover:
  - 8.0: maximum interruption 6.503s.
  - 8.4: maximum interruption 7.130s.
- Control majority loss:
  - Both VIPs removed, original primary enters read-only; integrity passed after recovery.
- Three MySQL instances cold shutdown and restart simultaneously:
  - 8.0 maximum interruption 92.047s, 8.4 maximum interruption 75.364s, integrity passed.

## Data Persistence and Configuration

All six instances confirmed:

- `sync_binlog=1`
- `innodb_flush_log_at_trx_commit=1`
- `relay_log_recovery=1`
- `log_replica_updates=1`
- GTID enabled
- binlog format is ROW
- Semi-synchronous replication plugin is enabled
- Current primary's semi-synchronous source is ON, with 2 ACK clients
- Both secondaries report their semi-synchronous replica status as ON

When the old primary or node restarts, disk-persisted read-only configuration takes effect before service recovery; write permissions will not be automatically restored before topology convergence.

## Code Verification

```text
go test ./...          PASS
go test -race ./...    PASS
go vet ./...           PASS
go build ./...         PASS
git diff --check       PASS
```

Covered MySQL adapter, agent role persistence, Raft, lease, Safety Guard, VIP, workflow, API, installation scripts, and lifecycle testing.

## Pre-Production Actions

1. Clarify network partition availability policy: accept fail-closed, or integrate and practice external fencing.
2. Clarify RTO: current automatic failure recovery maximum interruption is about 60 seconds; if the hard requirement is 30 seconds, optimization and full matrix re-run must continue.
3. Clarify RPO: accept the boundary of semi-synchronous replication, and develop recovery strategies for storage, network, and dual-node failures.
4. Verify connection/read/write timeout, disconnection reconnection, and idempotent retry on the application side.
5. Deploy trusted TLS certificates and complete switchover, rollback, and audit viewing using the official browser console.
6. Review the 15 historical `indeterminate` audit records. They are not active operations, but their disposition should be documented and archived before go-live.
7. Monitor Raft data files, disk space, VIP uniqueness, semi-synchronous status, replication threads, and replication delay.
8. Begin with a phased rollout on a non-critical service, retain a rollback window, and expand production scope gradually.
