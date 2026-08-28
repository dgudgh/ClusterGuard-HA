# ClusterGuard HA for Kubernetes MySQL

## Delivered scope

ClusterGuard HA 2.2 initially enables planned switchover and guarded failover for MySQL on Kubernetes. MySQL GTID owns replication, Kubernetes owns Pod lifecycle, and ClusterGuard owns database roles, durable restart roles, old-primary fencing, the writer Service backend, Raft leases, audit, and verification.

ClusterGuard does not switch CoreDNS and does not place a host VIP in a Pod. In-cluster clients use one selectorless Service. External clients use a stable LoadBalancer address, optionally announced by MetalLB. A transition changes only the managed EndpointSlice backend.

Kubernetes PostgreSQL endpoint metadata can be registered, but the PostgreSQL Pod role controller is not executable yet and therefore remains fail-closed.

## Runtime boundaries

| Runtime | Process owner | Client entry | Transition action |
| --- | --- | --- | --- |
| Bare metal or VM | host service | host VIP | move the interface VIP |
| Docker Swarm | pinned Service | host VIP | persist the container role and move the host VIP |
| Kubernetes | dedicated one-replica StatefulSet | selectorless Service | persist StatefulSet roles and replace one EndpointSlice backend |

RuntimeTarget and WorkloadBinding are mandatory. ClusterGuard never infers a runtime from a port, hostname, or container name.

## Mandatory safety contract

- Each database instance has its own one-replica StatefulSet at ordinal zero and its own PVC.
- WorkloadBinding records StatefulSet, Pod name, immutable Pod UID, Node name, and PVC UID.
- The writer Service is selectorless, and its EndpointSlice is labeled as managed by `clusterguard.io/ha`.
- Each StatefulSet enables the start guard and records `clusterguard.io/mysql-role=primary|replica`.
- Inside the database namespace, the Controller receives only `get` for Service/Pod/PVC, `get/update` for EndpointSlice, `get/patch` for StatefulSet, and `get/update` for the scale subresource. Its only cluster-scoped permission is `get Node`.
- The Kubernetes API uses HTTPS with CA validation and a rotating token file or client certificate.
- Database Pods disable automatic ServiceAccount token mounting. A short-lived token is projected only into the start-guard init container and is not visible to the MySQL container.
- A NotReady old-primary Node is unknown, not fenced. An external cloud, BMC, or hypervisor fencer is required for that failure class.

## Transition protocol

A planned switchover verifies identity, GTID, replication, quorum, and one EndpointSlice owner; makes the old primary read-only; promotes the candidate; persists the old StatefulSet role as `replica` and the target as `primary`; atomically updates the EndpointSlice with its current `resourceVersion`; then verifies SQL role, endpoint uniqueness, and canonical ownership.

A failover first writes `clusterguard.io/fenced=true` and scales the dedicated old-primary StatefulSet to zero. Promotion is allowed only after the Pod is absent, scale status is zero, the recorded Node is still Ready, and the exact target lease remains backed by the Raft majority.

## Deployment

Assets are installed under `/usr/share/clusterguard/kubernetes/` and live in `deploy/kubernetes/` in the source tree.

```bash
kubectl apply -f deploy/kubernetes/clusterguard-rbac.yaml
kubectl apply -f deploy/kubernetes/mysql/fence-guard-rbac.yaml
kubectl apply -f deploy/kubernetes/mysql/writer-service.yaml

docker build -f deploy/kubernetes/fence-guard/Dockerfile \
  -t clusterguard/k8s-fence-guard:2.2.0 .
```

The example Role is scoped to the `database` namespace. Duplicate the Role and RoleBinding for another database namespace instead of broadening write access into a cluster-wide role.

Adapt `statefulset-fence-guard-patch.yaml` for each StatefulSet and MySQL container. Set the current writer annotation to `primary` and replicas to `replica`. Confirm that the MySQL image reads `/etc/mysql/conf.d/`, then bootstrap the initial endpoint:

```bash
deploy/kubernetes/mysql/bootstrap-writer-endpoint.sh \
  database mysql-a-0 mysql-writer-clusterguard
```

Enable the provider in every controller:

```json
"kubernetes": {
  "enabled": true,
  "request_timeout_seconds": 10,
  "fence_timeout_seconds": 60
}
```

Register one Kubernetes RuntimeTarget whose `endpoint` is the API HTTPS origin and whose `credential_ref` is an absolute local JSON profile containing `ca_file` and `bearer_token_file`. Register one Kubernetes WorkloadBinding per database instance, including live Pod UID and Node name. Finally register a `service` HAEndpoint with provider `kubernetes_service`, provider reference `namespace/service/endpoint-slice`, the Service DNS hostname, database port, and current primary owner ID.

Use per-Pod headless Service DNS names for the three database discovery endpoints. Never register all database instances through the writer Service.

## Acceptance boundary

Code tests cover API TLS, token rotation, selector rejection, immutable Pod identity, single-backend transfer, durable role annotations, persistent fencing, scale-down, and NotReady-node rejection. A real Kubernetes end-to-end qualification has not yet been run, so these tests are not production acceptance evidence.

After failover, the old StatefulSet remains fenced at zero replicas. A controlled maintenance workflow must verify the durable `replica` role, clear the fence, scale it to one, and then run GTID former-primary recovery. The graphical maintenance action is still pending; do not bypass data validation and restart it writable.
