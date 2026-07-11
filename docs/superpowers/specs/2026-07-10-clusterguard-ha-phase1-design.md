# ClusterGuard HA Phase 1 Design

## Goal

Build an independent multi-database high-availability control kernel for
MySQL, PostgreSQL, Oracle, and SQL Server. Phase 1 delivers stable resource
identity, read-only MySQL discovery and health, four registered adapters,
metadata reconciliation, guarded workflows, audit records, reports, and the
versioned HTTP API.

## Boundaries

- The repository contains no compatibility layer, imported package, API route,
  configuration key, table, binary name, or service name from the frozen
  prototype.
- Every persisted resource uses a platform UUID. Network coordinates are mutable
  endpoint data and are never a primary key.
- Adapters are not allowed to mutate a database without a workflow-issued,
  guarded execution context.
- Unimplemented mutation capabilities are explicit `unsupported` responses.

## Resource and Identity Model

The platform owns `Platform`, `Controller`, `DatabaseCluster`, `DatabaseNode`,
`DatabaseInstance`, `Endpoint`, `EndpointAlias`, `ReplicationLink`,
`HAEndpoint`, `Operation`, `OperationPlan`, `Execution`, `Verification`,
`AuditEvent`, and `Report` resources.

`DatabaseInstance` includes platform UUID, cluster UUID, engine, engine-native
identity, display name, hostname, IP address, port, aliases, role, health,
metadata revision, and lifecycle timestamps.

Engine-native keys are:

- MySQL: `server_uuid`.
- PostgreSQL: `system_identifier` for the cluster; platform UUID for a node.
- Oracle: `DBID + DB_UNIQUE_NAME`, with RAC instances represented separately.
- SQL Server: availability-group `group_id` and replica `replica_id`.

When a rediscovery sees the same engine identity with changed hostname, IP, or
port, the metadata repository updates that resource, increments its revision,
and records the old endpoint as an alias. It never creates a second resource.

## Adapter SDK

`DatabaseHAAdapter` supplies `Engine`, `Capabilities`, `Discover`, `Topology`,
`Health`, `Precheck`, `BuildPlan`, `Execute`, `Verify`, `NodeSyncPrecheck`,
`BuildNodeSyncPlan`, `ExecuteNodeSync`, `MetadataPrecheck`, and
`ReconcileMetadata`.

The registry contains MySQL, PostgreSQL, Oracle, and SQL Server adapters. The
MySQL adapter uses a password-safe local client invocation for discovery and
health only in Phase 1. The other adapters are skeletons that advertise their
unsupported capabilities and fail closed for every unimplemented operation.

## Unified Workflow

Every mutating path follows this fixed state machine:

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

The workflow core owns lock acquisition, approval validation, safety checks,
audit emission, verification, report creation, and terminal status. Adapters
only provide engine-specific evaluations and actions.

## API and Console

The HTTP service exposes the requested `/api/v1` engine, capability, cluster,
topology, health, operation, node synchronization, and metadata reconciliation
routes. A compact embedded console shows engines, selected cluster health,
topology, and metadata anomalies. It has no direct mutation path outside the
same API workflow.

## Validation

Tests cover UUID creation, native identity keys, endpoint reconciliation,
adapter registration, MySQL discovery/health, unsupported adapters, workflow
gates, audit/report generation, and API responses. Repository scans verify that
the independent codebase has no legacy dependency or compatibility naming.
