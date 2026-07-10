# ClusterGuard HA Architecture

## Control Layers

1. **API and Console** expose engine-neutral resources and workflows.
2. **Workflow Core** enforces precheck, plan, lock, approval, execution,
   verification, audit, and reporting in one state machine.
3. **Adapter SDK** defines engine capabilities and all database-specific calls.
4. **Metadata Core** owns stable resource identities, mutable endpoints, aliases,
   anomaly detection, and revisioned reconciliation.
5. **Persistence** stores platform resources and workflow records through an
   engine-neutral repository contract.

Adapters cannot acquire locks, issue approvals, or suppress verification and
audit. Mutating adapter methods are called only by the workflow core.

## Identity Rules

- Every managed resource has an immutable platform UUID.
- Hostname, IP address, and port are endpoint attributes and may change.
- MySQL instance identity is `server_uuid`.
- PostgreSQL cluster identity is `system_identifier`; a node keeps its platform
  UUID across endpoint changes.
- Oracle database identity is `DBID + DB_UNIQUE_NAME`; RAC instances are modeled
  separately.
- SQL Server availability-group identity is `group_id`; replica identity is
  `replica_id`.
- A rediscovered engine identity reuses the existing resource UUID and records
  prior endpoints as aliases.

## Workflow

```text
DISCOVER -> PRECHECK -> PLAN -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Unsupported capabilities fail closed before lock acquisition. Execution cannot
be reported successful without a verification record and an audit event.
