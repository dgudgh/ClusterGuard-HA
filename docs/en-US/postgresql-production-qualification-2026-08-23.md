# PostgreSQL 16.4 Production Qualification Report

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/postgresql-production-qualification-2026-08-23.md)
<!-- /LANGUAGE-SWITCH -->

- Date: 2026-08-23
- Candidate line: ClusterGuard HA `2.2`
- Environment: `192.168.102.152`, `192.168.102.153`, `192.168.102.154`
- Database: PostgreSQL 16.4 on port 5432
- Control plane: three Raft voters and a restricted Agent on every data node
- Cluster: `pg16-ha`
- Writer endpoint: `192.168.102.155/24` on `ens160`
- Result: the laboratory matrix passed; site qualification is still required before production admission.

## Qualification Boundary

This report covers the native PostgreSQL streaming-replication adapter, not
Patroni, repmgr, or an external database controller. It verifies discovery,
identity, topology, planned switchover, automatic failover, writer-VIP
ownership, former-primary `pg_rewind` recovery, Raft quorum behavior, audit,
and report creation on the exact three-node lab above.

The result does not qualify another PostgreSQL build, operating system,
storage, network, interface, client driver, or fencing policy. Backups and
point-in-time recovery remain separate production requirements.

## Safety Invariants

1. At most one PostgreSQL instance is writable.
2. At most one host owns the writer VIP, and that host is the verified primary.
3. A controller without Raft majority cannot execute a mutation.
4. A data node without current majority authorization removes the VIP and
   stops serving as writer.
5. A recovered old primary does not restart as a competing writer; it rejoins
   only after a guarded `pg_rewind` workflow and verification.
6. Concurrent operations for one cluster serialize through the replicated
   operation lock and stale topology evidence blocks execution.

## Test Matrix

| Scenario | Result | Evidence |
| --- | --- | --- |
| Clean snapshot restore and PostgreSQL installation | Passed | Three PostgreSQL 16.4 services, controllers, and Agents started from restored VMs. |
| Identity, discovery, topology, and health | Passed | One `system_identifier`, three immutable node IDs, one primary, two streaming standbys, zero replay lag. |
| Planned switchover | Passed | Initial switch completed in about 8 seconds; five further rotations across all three nodes completed in about 10 seconds each. |
| Concurrent switch requests | Passed | One request completed and the competing request was blocked after lock/topology revalidation; only one role transition occurred. |
| Primary VM hard power-off | Passed | Writer interruption was 15.1 seconds; hypervisor marker to first successful write was about 24.97 seconds. No duplicate primary or VIP owner appeared. |
| Primary network partition | Passed | With PostgreSQL `connect_timeout=2`, writer interruption was 17.973 seconds and partition marker to first successful write was 17.942 seconds. |
| Unbounded client connection attempt | Observed constraint | One connection without a bounded connect timeout waited about 31.97 seconds although the control-plane operation completed earlier. |
| Controller quorum loss | Passed fail-closed | Mutation authority disappeared; the isolated primary Agent stopped PostgreSQL and removed the VIP. Recovery resumed only after quorum returned. |
| Raft Leader restart | Passed | A new Leader was elected while the database primary, VIP ownership, and row count remained stable. |
| Former-primary recovery | Passed | The old primary remained inactive after return, then completed guarded `pg_rewind`, started as standby, and caught up. |
| Operation audit and report | Passed | Planned and automatic operations produced durable operation IDs, stage progress, verification, audit events, and reports. |

## Automatic Failover Timing

The candidate configuration uses:

```text
discovery interval                 1 second
per-endpoint discovery timeout    1 second
stable failure evidence           3 observations spanning at least 3 seconds
Agent authorization-expiry fence  15 seconds
blocked-attempt retry backoff      30 seconds
PostgreSQL wal_receiver_timeout    5 seconds
PostgreSQL WAL retry interval      1 second
```

These timers are independent. The three-second value establishes stable
failure evidence; it does not bypass old-primary isolation. The 15-second
Agent window is a split-brain safety control. The 30-second value applies only
after a blocked or failed attempt and is not added to every successful
failover.

Application RTO depends on client behavior. The accepted baseline uses a
connection timeout no greater than two seconds plus retry and reconnect logic.
Measure RTO from the failed writer request to the first committed request on
the new writer endpoint.

## Final State

After cleanup and former-primary recovery:

| Host | Role | Validation rows | VIP |
| --- | --- | ---: | --- |
| `192.168.102.152` | streaming standby | 3512 | absent |
| `192.168.102.153` | streaming standby | 3512 | absent |
| `192.168.102.154` | writable primary | 3512 | sole owner |

All three PostgreSQL services were active, both standbys were streaming, and
the validation table row count matched on every node. Temporary partition
rules and workload processes were removed.

## Production Admission Requirements

Before production use:

1. Repeat the matrix with the exact site PostgreSQL package, Linux release,
   storage, switches, firewall, VIP interface, TLS trust, and client driver.
2. Set a bounded client connection timeout and retry policy, then measure the
   writer endpoint RTO under real application load.
3. Qualify asynchronous-replication RPO with committed transaction IDs. The
   current result does not claim synchronous zero data loss.
4. Qualify `pg_rewind`, WAL retention or archive behavior, full base backup,
   restore, and point-in-time recovery for the longest expected outage.
5. Replace or trust the laboratory self-signed console certificate before
   browser-based production operations.
6. Add BMC, PDU, cloud, hypervisor, or equivalent external fencing when the
   site threat model includes simultaneous Agent, operating-system, and
   management-network failure.

## Judgment

**The PostgreSQL 16.4 software path passed the current three-node laboratory
qualification, including sub-30-second writer recovery with bounded client
timeouts, split-brain prevention, Raft fail-closed behavior, and former-primary
rejoin. Production admission remains conditional on site-specific RPO/RTO,
storage, network, backup, TLS, and fencing qualification.**
