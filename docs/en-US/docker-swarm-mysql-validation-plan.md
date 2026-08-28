# Docker Swarm MySQL Validation Plan

## Scope

Build a three-manager Docker Swarm on `192.168.102.152-154` and deploy MySQL 8.0.44 with one writer and two replicas. A temporary isolated control plane protects the running PostgreSQL cluster during destructive qualification. After qualification, the MySQL cluster must move into the site's unified ClusterGuard HA control plane so one API, Raft group, and host Agent manage both independent database clusters.

| Resource | Lab value |
| --- | --- |
| Swarm managers / MySQL hosts | `192.168.102.152-154` |
| MySQL | `8.0.44:3306` |
| Writer VIP | `192.168.102.156/24` on `ens160` |
| Temporary destructive-test console | `https://192.168.102.152:3100/`, test phase only |
| Temporary test Raft transport | `11009/tcp`, disabled after qualification |
| Final unified control plane | `https://192.168.102.152-154:3000/`, Raft `10009/tcp` |
| Console cluster entries | `pg16-ha` and `swarm-mysql-8.0` |

## Execution phases

1. Pin the Docker static archive and `mysql:8.0.44` linux/amd64 manifest and build identical offline media.
2. Install Docker and verify clock, firewall, SELinux, overlay networking, and three-manager quorum.
3. Label fixed slots and deploy three MySQL Services with durable host data.
4. Bootstrap host 01 first, then 02 and 03; verify GTID auto-position, both replication threads, and validation data.
5. Start an isolated three-node ClusterGuard Raft group, discover MySQL, and persist the `server_uuid -> resource_id -> Swarm Service` mapping.
6. Register RuntimeTarget, WorkloadBinding, and the Linux VIP HAEndpoint; start host Agents and converge the ownership lease.
7. Qualify planned switchovers, failover, former-writer recovery, VIP uniqueness, data convergence, audit, and reports.
8. Move Docker MySQL resources, runtime bindings, the HA endpoint, and Agent policies into the unified `3000` control plane. Verify both clusters and then shut down the temporary `3100/11009` control plane.

## Pass criteria

- All three Swarm managers are Ready and Reachable, and Service management survives one manager loss.
- Exactly one MySQL instance is writable and exactly its host owns the VIP.
- `01 -> 02 -> 03 -> 01` planned switchovers pass and both replicas follow the new writer.
- Scaling the writer Service to zero promotes a candidate only with ClusterGuard Raft majority and fencing evidence; target VIP recovery is within 30 seconds.
- A restarted former-writer task first starts with both read-only controls enabled, catches up, and rejoins the new writer.
- Duplicate VIP, dual writer, duplicate labels, runtime identity mismatch, Agent loss, and Raft quorum loss all fail closed.
- Every mutation produces an Operation, Execution, Verification, AuditEvent, and Report whose outcome agrees with the observed MySQL state.

## Evidence

Retain Swarm node/service/task output, MySQL server UUID and GTID state, replication and read-only status, VIP ownership, transition timing, former-writer recovery details, ClusterGuard audit/reports, and the exact reason for every injected failure that was blocked. A scenario without this evidence is not counted as passed.

## Lab results on 2026-08-27

This run used MySQL 8.0.44, three Swarm managers, three ClusterGuard Raft voters, and host Agents. At completion, host 01 was the only writable primary and owned `192.168.102.156/24`; hosts 02 and 03 had both read-only controls enabled and running replication threads.

| Scenario | Observed result | Outcome |
| --- | --- | --- |
| Planned `01 -> 02 -> 03 -> 01` switchovers | Each completed in about 8 seconds; writer, both replication sources, and VIP converged together | Pass |
| Planned restart of the writer Service | VIP did not drift while recovery was frozen; the task started from the durable role file and writable state converged after the stable lease returned | Pass |
| Abrupt removal of writer Service 02 | End-to-end recovery from 02 to 01 completed within 21 seconds; workflow execution and verification took about 12.2 seconds | Pass |
| Pre-failure data consistency | Marker `abrupt-failover-1787762799` existed on the promoted writer and both rejoined replicas | Pass |
| Former writer 02 rejoin | Built-in `former_primary_rejoin` completed in 3 seconds with four persisted verification checks passing | Pass |
| Repeated verification of a succeeded operation | `/operations/{id}/verify` returned the immutable stored evidence instead of applying the old plan to the new topology | Pass |
| One Docker manager stopped | Writer 01 and its VIP stayed in place while 03 was unavailable; 03 returned automatically as a read-only replica after Docker restarted | Pass |
| ClusterGuard Raft majority lost | The switchover plan returned HTTP 503 and no database mutation was created or executed | Pass |
| Unauthorized duplicate VIP | After the VIP was injected on host 153, its Agent removed it within 3 seconds while the authorized VIP on 152 remained | Pass |

Primary evidence includes automatic failover operation `eb8cd08d-11ad-4cc5-9326-069ad2aa2a52` and former-writer rejoin operation `63c026dc-fc6a-49bd-b6f9-3fffa26aa68b`. Both `go test ./... -count=1` and `go vet ./...` passed.

This qualifies the first Docker Swarm MySQL implementation phase; it is not a substitute for production-equivalent endurance testing. Full host power loss, cross-switch network partition, read-only or full storage, one hundred consecutive transitions, and MySQL 8.4 remain separate target-environment qualifications.

## Unified-control-plane handoff

The isolated `3100` lab was handed into the production-shaped `3000` control plane on the same date. The unified `/api/v1/clusters` response contains healthy `pg16-ha` and `swarm-mysql-8.0` resources. PostgreSQL VIP `192.168.102.155` is attached to its current primary host, Docker MySQL VIP `192.168.102.156` is attached to its current writer host, and one Agent configuration on each host reconciles both clusters.

A Docker MySQL `01 -> 02 -> 01` controlled-switchover regression passed after the handoff; writer role, both replicas, VIP placement, and the pre-transition validation row converged. The temporary `clusterguard-swarm-lab.service` and dedicated Agent timer are disabled, no host listens on `3100`, and all three unified controllers serve the console on `3000`.

The handoff exposed and fixed two delivery defects. Container labels must be updated to the instance UUIDs assigned by the unified control plane before the Agent takes ownership, otherwise its identity allowlist correctly rejects the task. In addition, the Agent systemd unit uses `ProtectSystem=strict` and therefore must explicitly allow writes below `/etc/clusterguard/docker` so it can atomically replace the restart-fence file. Configuration validation and regression tests now enforce these runtime contracts.
