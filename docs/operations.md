# ClusterGuard HA Operations

## Start the Control Plane

Create dedicated read-only credentials for MySQL discovery. Keep both that
password and the local approval token out of JSON files.

```bash
export CG_APPROVAL_TOKEN='replace-with-a-local-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-discovery-account-secret'
go run ./cmd/clusterguardd --config configs/clusterguard.example.json
```

The service binds to the configured HTTP address and exposes the web console at
`/`. The `cgctl` command reads the same API without sharing in-process state:

```bash
go run ./cmd/cgctl -- engines
go run ./cmd/cgctl -- clusters
go run ./cmd/cgctl -- topology <platform-cluster-uuid>
go run ./cmd/cgctl -- health <platform-cluster-uuid>
```

## MySQL Read-only Discovery

Discovery creates or refreshes an instance from its immutable `server_uuid`.
Network coordinates are deliberately not used as the resource key.

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/discovery \
  -H 'content-type: application/json' \
  -d '{
    "engine":"mysql",
    "cluster_id":"<platform-cluster-uuid>",
    "endpoint":{"hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},
    "credentials":{"username":"cg_discovery","password":"<secret>"}
  }'
```

The discovery password is accepted only for this request and is never persisted
in the metadata snapshot. The MySQL adapter passes it to the client process by
environment variable rather than process arguments.

## Metadata Reconciliation

When a host name, IP address, or port changes, submit the known engine identity
to the metadata reconciliation flow. A matching engine identity retains its
existing platform UUID, increments `metadata_revision`, and records the prior
endpoint as an alias. A collision between two distinct engine identities is
blocked.

The execution route is guarded by precheck, safety, lock, approval, audit, and
identity verification. It does not modify the database engine.

## Current Phase-one Capability Boundary

| Engine | Available now | Safely blocked |
| --- | --- | --- |
| MySQL | identity discovery, health, endpoint reconciliation | switchover, failover, replication repair, node synchronization |
| PostgreSQL | adapter registration and explicit capability response | discovery and all mutations |
| Oracle | adapter registration and explicit capability response | discovery and all mutations |
| SQL Server | adapter registration and explicit capability response | discovery and all mutations |

No adapter can bypass the platform workflow. Unsupported mutation requests
return `501` and never proceed to a database call.

## Next Adapter Milestones

### MySQL feature-parity gap

Add topology collection, GTID and replication diagnostics, candidate ranking,
guarded switchover, guarded failover, post-action verification, and a
database-native node seed workflow. Each mutation remains disabled until its
precheck, plan, lock, approval, audit, and verification implementation ships.

### PostgreSQL

Implement read-only discovery from `system_identifier`, primary/standby health,
replication lag collection, and timeline-aware candidate assessment. Add
promotion only after the preceding controls and rollback/verification contract
are defined.

### Oracle

Implement database discovery from `DBID` and `DB_UNIQUE_NAME`, then model RAC
instances separately. The next safe operation is read-only Data Guard health
collection before any role transition support.

### SQL Server

Implement availability-group discovery from `group_id` and replica identities
from `replica_id`, followed by read-only listener and synchronization health.
Failover planning stays unsupported until replica synchronization and quorum
checks are represented in the common workflow.
