# ClusterGuard HA for Docker Swarm MySQL

## Scope

This phase manages a MySQL 8.0 primary-replica cluster running on Docker Swarm and exposes the writer through a host VIP. Swarm owns container process recovery, MySQL GTID owns data replication, and ClusterGuard owns candidate evaluation, fencing, writer transition, verification, audit, and VIP leases.

Kubernetes MySQL now uses a separate Service/EndpointSlice provider documented in `kubernetes-mysql.md`; it must not be mixed with the host-VIP Swarm mode on this page.

## Unified control plane and cluster selection

Host, Docker Swarm, and Kubernetes are runtime properties of a database cluster; they are not reasons to split the control plane. A site runs one odd-sized ClusterGuard Raft control plane. Every database cluster uses the same HTTPS API and console ports, `3000` by default, and appears as a separate resource in the cluster selector, for example `pg16-ha` and `swarm-mysql-8.0`.

Each host also runs one `clusterguard-agent`. The `clusters` array in `/etc/clusterguard/agent.json` may contain host PostgreSQL, host MySQL, Docker MySQL, and Docker PostgreSQL policies together. Distinct `cluster_id`, `instance_id`, and `runtime_kind` values identify them; equal database ports do not imply equal clusters or runtimes.

A second port such as `3100` is permitted only for a temporary destructive lab with an explicit teardown. Qualification must finish by registering the tested cluster in the unified control plane, merging Agent policies and ownership leases, and disabling the temporary Raft group, Agent timer, and listener. A production site must not retain two ClusterGuard control planes for these runtimes.

## Host and container boundaries

The host runtime includes both bare metal and virtual machines: the operating system directly owns the database process. In the container runtime, Docker Swarm owns the database Service while the ClusterGuard Agent, VIP, and durable fencing evidence remain on the host. Runtime placement must be registered through RuntimeTarget and WorkloadBinding; it must never be inferred from an address or port.

| Boundary | Host (bare metal or VM) | Container (Docker Swarm) | Kubernetes |
| --- | --- | --- | --- |
| Database process | systemd service or controlled host process | task/container of one allowlisted Swarm Service | StatefulSet Pod |
| Stable database identity | `resource_id` plus MySQL `server_uuid` | Same; replacement containers do not replace database identity | Same; Pod UID is not database identity |
| Replaceable placement | hostname, IP, and process PID | host node, task ID, and container ID | Node and Pod UID |
| Durable data | host data directory or block storage | registered persistent volume, never the container writable layer | PVC UID |
| Lifecycle owner | systemd or host commands | Docker Swarm Service | Kubernetes controller |
| ClusterGuard Agent | database host | container host, outside the database container | restricted Kubernetes API plus a Pod start guard |
| Client entry | VIP on a host network interface | VIP on the current writer host network interface | Service/EndpointSlice, not a host VIP |
| Role and fencing | change the local database role and manage the VIP | write the host fence file, then change the database role in the fixed Service | durable StatefulSet role, scale-to-zero fencing, and EndpointSlice transfer |
| Current support | Supported | MySQL 8.0 Docker Swarm takeover | Initial MySQL support with constraints in the Kubernetes guide |

Container mode does not place the VIP inside a container. Applications still connect to `VIP:database-port`; the VIP moves between hosts while Swarm manages desired container state. The ClusterGuard control plane remains an independent host service, so stopping a database container must not also remove management access.

## Identity and placement

The platform stores three identity layers:

| Layer | Stable identity | Mutable attributes |
| --- | --- | --- |
| Database instance | `resource_id` plus MySQL `server_uuid` | hostname, IP, port, role |
| Runtime target | `RuntimeTarget.resource_id` | Docker endpoint, labels, credential reference |
| Workload | `WorkloadBinding.resource_id` | container ID, task ID, observation time |

`DatabaseInstance.resource_id` and MySQL `server_uuid` remain stable when a container is replaced. `RuntimeTarget` describes the Docker control boundary. `WorkloadBinding` binds an instance to one host and allowlisted Swarm Service while recording replaceable task and container observations.

Only one active binding is accepted for an instance. A container ID is never accepted from a mutation request.

## High-availability boundary

1. Swarm owns container process restart and placement on the pinned node.
2. Native MySQL GTID replication owns data synchronisation.
3. ClusterGuard owns candidate evaluation, the transition workflow, old-primary fencing, the VIP, audit, and verification.
4. The Raft majority decides who may execute a transition and issue VIP ownership leases.
5. The host Agent manages the pinned Service through the local Docker socket. A container ID or an arbitrary command passed in through the API is never accepted.

The database Service publishes 3306 with `mode: host`. The VIP is always bound to the network interface of
the host that currently runs the primary, so the client address does not change across a transition.

## Fencing contract

The host Agent atomically writes `/etc/clusterguard/docker/mysql-fence.cnf` before changing the live server. The whole `/etc/clusterguard/docker` directory is mounted read-only at `/etc/mysql/conf.d`, so an atomic role-file replacement is visible through the directory mount instead of being hidden behind a file bind mount pinned to an old inode. Old-primary fencing runs in this order:

1. Atomically write the fence file with `read_only=ON` and `super_read_only=ON`.
2. Write the Agent's durable local role record and apply MySQL `SET PERSIST_ONLY`.
3. Set the global read-only state of the running process and release the VIP.

Even if Swarm recreates the container after fencing, the new task reads the host fence file first and starts
read-only. An unavailable Docker daemon, ambiguous container labels, an identity mismatch, or an unreachable
role on a running task all fail the failover closed.

## Operational constraints

- Use at least three odd-numbered Swarm Managers. Swarm quorum and ClusterGuard Raft quorum are separate safety domains.
- Pin each database Service to a node carrying the `clusterguard.mysql.slot` label, and publish 3306 with `mode: host`.
- Store data on a durable bind mount or qualified block storage, never in the container writable layer.
- Run the restricted Agent on the host and do not expose the Docker socket to the database container; only root or a restricted sudo account may access that socket.
- Planned database stop changes Swarm desired replicas to zero with `docker service scale SERVICE=0`. `docker stop` is not an accepted lifecycle operation.
- The VIP is attached to the current writer host, not to a container overlay interface.
- A database failure is not a host failure. A failed 3306 probe can trigger database recovery, while a whole-host power loss follows the separate power workflow.

## Deployment and validation

Deployment assets are under `deploy/docker-swarm/`.

1. Copy the official Docker x86_64 static archive and the MySQL 8.0.44 image tarball into the offline environment.
2. Run `install-docker-static.sh` on all three hosts.
3. Initialise Swarm on 01 and join 02 and 03 as Managers.
4. Run `mysql/prepare-host.sh SLOT NODE_IP` on every host.
5. Run `mysql/install-mysql-client.sh MYSQL_TAR` on every controller node to install an independent client. The control plane must not borrow `mysql` from its local database container, because losing that task would also remove discovery and recovery access.
6. Create the two Swarm secrets `cg_mysql_root_password` and `cg_mysql_operator_cnf` on a Manager.
7. Export the cluster UUID and the three provisional instance UUIDs, then run `docker stack deploy -c mysql-stack.yml cgmysql`.
8. Run `bootstrap-replication.sh 01` on host 01 first, then `bootstrap-replication.sh 02` and `03` on the corresponding hosts, to create accounts, GTID replication and acceptance data. Each invocation deliberately controls only the local Swarm task and never mistakes a remote container for a local one. Export `CG_MYSQL_ROOT_PASSWORD`, `CG_MYSQL_DISCOVERY_PASSWORD`, `CG_MYSQL_OPERATION_PASSWORD`, and `CG_MYSQL_REPLICATION_PASSWORD` on the matching host first (the script exits immediately when any is missing, and the replication password must be at most 32 characters); `CG_MYSQL_SOURCE_HOST` defaults to `192.168.102.152`, so override it outside that subnet.
9. Register a new Docker MySQL cluster plus the three host endpoints in the site's existing unified ClusterGuard control plane and run discovery. Do not create a second API or Raft port.
10. Redeploy the Service with the real instance UUIDs discovered above, and merge the Docker policy into the `clusters` array of the three existing `/etc/clusterguard/agent.json` files.
11. Create RuntimeTarget, WorkloadBinding and HAEndpoint, run prechecks, then enable automatic failover. MySQL automatic failover additionally requires working isolation evidence or the control plane refuses to start it: set `"fencing": { "agent_quorum_enabled": true, "agent_quorum_grace_seconds": 15 }` (valid range 15-60 seconds), or configure an external fencer, or enable `kubernetes`.

| Scenario | Expected result |
| --- | --- |
| Planned switchover 01->02->03->01 | Primary, replication source and VIP converge together; the client address does not change |
| Primary container stopped | The old primary is durably fenced, the candidate is promoted and the VIP moves (measured end to end in the lab at about 21 seconds, of which workflow execution and verification took about 12.2 seconds; the 30-second figure is a target, not a product commitment) |
| Former-primary Service restarted | The container starts read-only and then rejoins replication automatically |
| Docker daemon stopped | Container state is never faked; the transition is blocked while isolation evidence is missing |
| Two container labels point at the same instance | The Agent reports an identity conflict and blocks the mutation |
| ClusterGuard Raft majority lost | Primary transition and VIP acquisition are refused |
| Swarm Manager majority lost | Service scaling is refused; the running databases and the VIP are not disturbed |

Qualification additionally covers duplicate-VIP detection, replication convergence after each transition, and
audit/report completeness.

The executable lab plan and evidence checklist for `192.168.102.152-154` are in
[`docker-swarm-mysql-validation-plan.md`](docker-swarm-mysql-validation-plan.md).

## Separate Kubernetes mode

The Kubernetes provider binds an instance to StatefulSet ordinal, Pod UID, Node name, and PVC UID. ClusterGuard updates a selectorless Service EndpointSlice only after Raft authorization; a cloud load balancer or MetalLB can provide the external address. Pod labels and ordinary Service selectors do not own writer routing. See [`kubernetes-mysql.md`](kubernetes-mysql.md).
