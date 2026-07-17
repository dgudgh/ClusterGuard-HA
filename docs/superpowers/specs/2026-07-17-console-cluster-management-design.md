# ClusterGuard HA Console Cluster Management Design

## Goal

Add an administrator-only cluster management dialog to the ClusterGuard HA
console. Administrators can register a cluster from one or more seed database
endpoints and can retire the selected cluster from active management without
deleting database data or historical audit evidence.

## Chosen Approach

The cluster selector toolbar gains one compact `集群管理` button. The button
opens a focused modal with two views:

- `新增集群`: database engine, immutable display name, and one or more seed
  endpoints. Submission uses the existing `POST /api/v1/clusters` contract and
  immediately requests discovery for the new cluster.
- `退役集群`: a dependency summary and an exact display-name confirmation.
  Submission uses a new `DELETE /api/v1/clusters/{resource_id}` contract.

Retirement removes the cluster from the active resource registry and discovery
scheduler. It removes live instances, endpoints, HA endpoint metadata,
replication links, metrics, topology observations, and metadata anomalies. It
does not contact database hosts, stop MySQL, detach an operating-system VIP, or
delete database data.

Audit events, reports, completed lifecycle tasks, and operation records remain
stored so past actions remain reviewable. A retirement audit event records the
cluster UUID, display name, operator, and removed-resource counts.

## Safety Rules

Retirement is blocked when any of these conditions is true:

- the confirmation text does not exactly match the cluster display name;
- an unexpired operation lock exists for the cluster;
- an unexpired HA ownership lease exists for the cluster;
- a lifecycle task is planned, queued, running, or verifying;
- a database operation is currently running.

Expired coordination records are removed with active inventory. Historical
operations, audits, reports, and terminal lifecycle tasks remain intact.

Only an authenticated platform administrator may register or retire a cluster.
Existing session, CSRF, quorum-leader, and mutation-authority gates apply to
both endpoints.

## API

Existing registration:

```http
POST /api/v1/clusters
Content-Type: application/json

{
  "display_name": "mysql-orders",
  "engine": "mysql",
  "endpoints": [
    {"hostname": "mysql01", "ip_address": "192.168.102.152", "port": 3306}
  ]
}
```

New retirement:

```http
DELETE /api/v1/clusters/{resource_id}
Content-Type: application/json

{"confirm_display_name":"mysql-orders"}
```

Successful retirement returns the retired cluster identity and counts for
removed instances, endpoints, HA endpoints, links, metrics, and anomalies.
Conflicting active work returns HTTP 409. A missing cluster returns HTTP 404.

## Console Behavior

The modal is keyboard accessible, responsive, and keeps its footer visible on
small screens. Registration supports adding and removing endpoint rows before
submission. At least one endpoint and a valid port are required.

After registration, the console selects the new cluster, triggers discovery,
and refreshes all views. If registration succeeds but discovery fails, the
cluster remains registered and the modal reports that discovery must be
retried.

After retirement, the console closes the modal, refreshes the cluster list,
selects the next available cluster, and reports the result. The destructive
button remains disabled until the exact cluster display name is entered.

## Out Of Scope

- deleting database files, schemas, users, or MySQL services;
- detaching a VIP from an operating-system interface;
- deleting historical audits, reports, operations, or completed task records;
- bulk cluster deletion;
- automatic credential entry in the browser.
