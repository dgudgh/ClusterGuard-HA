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

`DatabaseInstance.resource_id` and MySQL `server_uuid` remain stable when a container is replaced. `RuntimeTarget` describes the Docker control boundary. `WorkloadBinding` binds an instance to one host and allowlisted Swarm Service while recording replaceable task and container observations.

Only one active binding is accepted for an instance. A container ID is never accepted from a mutation request.

## Fencing contract

The host Agent atomically writes `/etc/clusterguard/docker/mysql-fence.cnf` before changing the live server. The whole `/etc/clusterguard/docker` directory is mounted read-only at `/etc/mysql/conf.d`, so an atomic role-file replacement is visible through the directory mount instead of being hidden behind a file bind mount pinned to an old inode. The Agent then records the durable local role, applies MySQL `SET PERSIST_ONLY`, changes the live read-only flags, and releases the VIP. A Swarm-recreated former primary therefore starts read-only.

An unavailable Docker daemon, ambiguous container labels, an identity mismatch, or an unreachable role on a running task blocks failover.

## Operational constraints

- Use at least three odd-numbered Swarm Managers. Swarm quorum and ClusterGuard Raft quorum are separate safety domains.
- Pin one MySQL Service to each labeled node and publish 3306 in host mode.
- Store data on a durable bind mount or qualified block storage, never in the container writable layer.
- Run the restricted Agent on the host. Do not expose the Docker socket to the database container.
- Planned database stop changes Swarm desired replicas to zero. `docker stop` is not an accepted lifecycle operation.
- The VIP is attached to the current writer host, not to a container overlay interface.

## Deployment and validation

Deployment assets are under `deploy/docker-swarm/`. Install Docker from the pinned static archive, create a three-manager Swarm, prepare each host, and install an independent native client on every controller with `mysql/install-mysql-client.sh MYSQL_TAR`. The control plane must not borrow the client from its local database container because losing that task would also remove discovery and recovery access. Load the MySQL image, create the external secrets, and deploy `mysql-stack.yml`. Run `bootstrap-replication.sh 01` on host 01 first, followed by `bootstrap-replication.sh 02` and `03` on the corresponding hosts. Each invocation deliberately controls only the local Swarm task. Export `CG_MYSQL_ROOT_PASSWORD`, `CG_MYSQL_DISCOVERY_PASSWORD`, `CG_MYSQL_OPERATION_PASSWORD`, and `CG_MYSQL_REPLICATION_PASSWORD` on the host first (the script exits immediately when any is missing, and the replication password must be at most 32 characters); `CG_MYSQL_SOURCE_HOST` defaults to `192.168.102.152`, so override it outside that subnet. Register a new Docker MySQL cluster in the site's existing control plane, discover the instances, merge each Docker policy into the existing host Agent configuration, and then register RuntimeTarget and WorkloadBinding records. Do not create a second API or Raft port. MySQL automatic failover additionally requires working isolation evidence or the control plane refuses to start it: set `"fencing": { "agent_quorum_enabled": true, "agent_quorum_grace_seconds": 15 }` (valid range 15-60 seconds), or configure an external fencer, or enable `kubernetes`.

Qualification covers round-robin switchovers, primary task loss, Docker daemon loss, former-primary restart, duplicate container identity, Raft quorum loss, Swarm manager quorum loss, VIP uniqueness, replication convergence, and audit/report completeness. VIP migration after primary task loss was observed at roughly 30 seconds in the lab; that is a measured value, not a product commitment.

The executable lab plan and evidence checklist for `192.168.102.152-154` are in
[`docker-swarm-mysql-validation-plan.md`](docker-swarm-mysql-validation-plan.md).

## Separate Kubernetes mode

The Kubernetes provider binds an instance to StatefulSet ordinal, Pod UID, Node name, and PVC UID. ClusterGuard updates a selectorless Service EndpointSlice only after Raft authorization; a cloud load balancer or MetalLB can provide the external address. Pod labels and ordinary Service selectors do not own writer routing. See [`kubernetes-mysql.md`](kubernetes-mysql.md).
