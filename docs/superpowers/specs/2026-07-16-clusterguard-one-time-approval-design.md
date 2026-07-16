# ClusterGuard HA One-Time Approval and Automatic Recovery Authorization Design

## Status

Approved for implementation on 2026-07-16.

## Problem

ClusterGuard HA currently uses two long-lived shared secrets:

- `CG_CONTROL_TOKEN` authenticates every mutating HTTP request.
- `CG_APPROVAL_TOKEN` approves every workflow execution, including automatic
  failover.

This mixes API authentication, human approval, and internal controller
authority. It also forces the browser console to hold long-lived secrets and
causes normal switchovers to fail after a page refresh.

## Goals

1. Manual high-risk operations use a short-lived, single-use approval grant.
2. Only a privileged administrator or approver may issue a grant.
3. A grant is bound to one cluster, operation kind, target, and immutable plan.
4. The plaintext grant is returned once and is never stored.
5. Grant issuance, validation, consumption, rejection, and expiry are audited.
6. Automatic failover does not require a human approval grant.
7. Automatic failover remains fail-closed behind Leader, quorum, incident,
   fencing, Safety Guard, operation lock, verification, audit, and reporting.
8. Public HTTP requests cannot claim the automatic-recovery identity.
9. The normal Web console no longer needs the long-lived control token to
   execute an already-approved operation.

## Non-Goals

- Building an external IAM, LDAP, OIDC, or SSO provider in this increment.
- Allowing anonymous mutation.
- Removing authentication from administrative APIs.
- Weakening Safety Guard, operation locks, topology pinning, VIP leases,
  verification, audit, or report persistence.
- Making PostgreSQL, Oracle, or SQL Server mutations executable.

## Authorization Model

### Administrative Principal

The existing control credential becomes an administrative API credential. It
may register inventory, trigger privileged maintenance endpoints, and issue
approval grants. It is not entered into the normal operation console.

The administrative credential remains a deployment secret until a future
external identity provider replaces it.

### Manual Operator

A manual operator supplies a one-time approval grant when executing a
high-risk operation. The grant itself authorizes only its bound intent; it does
not become a general control API credential.

The HTTP API accepts the grant only on the matching execute endpoint. A grant
cannot authorize registration, arbitrary discovery publication, grant
issuance, or another operation.

### Automatic Recovery Principal

Automatic recovery uses an internal, typed system authorization created by the
runtime. It is not represented by a request parameter or HTTP header.

Only the recovery controller assembled inside the ClusterGuard process can
call the automatic execution entry point. The entry point verifies:

- automatic failover is enabled;
- the caller holds current mutation authority;
- the incident is stable and current;
- the operation is `failover`;
- the requested actor is the fixed automatic-recovery actor;
- the request contains the internally generated incident identity.

The automatic path enters the same workflow before Safety Guard and cannot
skip any gate after human approval.

## Approval Grant Model

Add a Raft-replicated `ApprovalGrant` resource:

```text
resource_id
token_hash
operation_id
cluster_id
engine
operation_kind
target_id
plan_digest
observation_digest
issued_by
issued_at
expires_at
consumed_at
consumed_by_operation_id
status
metadata_revision
```

Rules:

- Grant secret format: `cgag_<grant-id>.<256-bit-random-secret>`.
- Store only SHA-256 of the random secret.
- Default TTL: 5 minutes.
- Maximum TTL: 15 minutes.
- Status is `active`, `consumed`, `expired`, or `revoked`.
- The plaintext secret is returned only from the issue response.
- Validation uses constant-time hash comparison.
- Cluster, engine, kind, target, plan digest, and observation digest must match.
- Consumption is atomic with the APPROVE stage transition under the cluster
  operation lock.
- A consumed, expired, revoked, malformed, or mismatched grant fails closed.
- A grant is consumed before adapter mutation. Retrying requires a new grant
  unless the durable idempotency key resolves to the already-running or
  terminal operation that consumed it.

## Plan Binding

Grant issuance receives the intended cluster, operation kind, and target. The
server creates or reuses a durable planned operation, captures the current
topology observation, resolves the adapter request, builds the immutable plan,
and binds the resulting operation ID, plan digest, and observation digest to
the grant.

Execution independently rebuilds or resumes the durable plan. Approval
consumption compares the durable record against the grant. A topology or plan
change blocks execution and requires a new grant.

## API

### Issue a grant

```text
POST /api/v1/approvals
Authorization: Bearer <administrative-control-token>
```

Request:

```json
{
  "cluster_id": "uuid",
  "engine": "mysql",
  "operation_kind": "switchover",
  "target_id": "uuid",
  "issued_by": "dba-admin",
  "ttl_seconds": 300
}
```

Response returns grant metadata and the plaintext `approval_token` once.

### Inspect grants

```text
GET /api/v1/approvals
GET /api/v1/approvals/{id}
```

Responses never include `token_hash` or the plaintext token.

### Execute manually

Existing execute payload keeps `approval_token`. The route accepts a matching
one-time grant without requiring the general control credential. Other
mutating routes remain protected by administrative authentication unless they
are explicitly converted to grant-authorized execution.

### CLI

Add:

```text
cgctl approval issue --cluster <uuid> --kind switchover --target <uuid> --ttl 5m
cgctl approval list
cgctl approval show <grant-id>
```

Issuance reads the administrative credential from the configured environment
variable. The CLI prints the secret once and warns that it cannot be recovered.

## Workflow Interfaces

Replace the string-only approval contract with an authorization envelope:

```go
type ExecutionAuthorization struct {
    Mode          AuthorizationMode
    ApprovalToken string
    IncidentID    string
}
```

The public API can construct only `AuthorizationManual`. The recovery package
uses a separate `ExecuteAutomatic` method that constructs
`AuthorizationAutomatic` internally. JSON input never controls `Mode`.

The approval validator receives the durable operation record so it can compare
the target, plan digest, and observation digest before atomically consuming the
grant.

## Storage and Consensus

Approval grants live in the repository snapshot and therefore use the existing
Raft snapshot CAS protocol. Issue, consume, revoke, and expire operations are
Leader-only mutations.

Grant consumption and the operation APPROVE transition must be one repository
mutation. Followers never independently consume a grant.

Expired grants may be marked lazily during lookup or by a bounded cleanup pass.
Expiry always uses the Leader clock and is rechecked at consumption.

## Web Console

- Remove the control-token field from normal console settings.
- Rename the remaining field to `一次性审批令牌`.
- Explain that it is generated by an administrator and consumed once.
- Clear the field and in-memory value after every execute attempt.
- If the API reports consumed, expired, mismatched, or invalid, show the exact
  Chinese reason and keep the switch locked.
- Automatic failover status is read-only and never asks for a token.

The existing explicit unlock button remains a local mistake-prevention
control; it is not an authorization mechanism.

## Automatic Failover

Remove the static approval-token requirement from automatic-recovery startup.
The controller calls `ExecuteAutomatic`, not the public manual execution
contract.

Every automatic operation records:

- actor `clusterguard-automatic-recovery`;
- trigger `stable_primary_failure`;
- incident identity and first stable observation time;
- Leader identity and current term when available;
- selected target and immutable plan digest;
- Safety Guard, lock, fencing, execution, verification, audit, and report
  outcomes.

No HTTP endpoint accepts an automatic authorization mode.

## Migration

1. Existing `CG_CONTROL_TOKEN` remains the administrative credential.
2. Existing `CG_APPROVAL_TOKEN` is deprecated and ignored for new executions.
3. Automatic failover no longer requires `CG_APPROVAL_TOKEN`.
4. Installers stop placing the approval secret in browser-facing instructions.
5. The first release logs a clear warning when the deprecated variable exists.
6. No compatibility fallback accepts the old static approval token.

## Error Semantics

- Missing manual grant: `approval grant is required`.
- Malformed grant: `approval grant is invalid`.
- Expired grant: `approval grant has expired`.
- Consumed grant: `approval grant has already been consumed`.
- Scope mismatch: `approval grant does not match this operation`.
- Plan mismatch: `approval grant plan is stale`.
- Non-Leader issue/consume: existing Leader/quorum blocked response.
- HTTP attempt to select automatic mode: rejected as invalid input.

Errors remain `blocked` before mutation and are audited without exposing token
material.

## Test Strategy

Unit tests:

- random token format and hash-only persistence;
- exact scope and plan matching;
- expiry, revocation, and single-use consumption;
- atomic consume plus APPROVE transition;
- restart and Raft replication persistence;
- no plaintext token in snapshots, audit, reports, or logs;
- automatic execution succeeds without a grant only through the internal API;
- public HTTP cannot forge automatic authorization;
- old static approval token is rejected.

API and console tests:

- privileged issuance requires the administrative credential;
- issue response reveals the secret once;
- manual execution accepts one matching grant and rejects reuse;
- console has no control-token field;
- console clears the one-time token after execution;
- follower responses retain Leader information.

Integration tests:

- planned switchover consumes one grant and moves Primary plus VIP;
- a second switch with the same grant is blocked;
- automatic failover completes without a grant under Leader and quorum;
- quorum loss, missing fencing, duplicate VIP, and stale topology still block
  automatic failover.

## Acceptance Criteria

1. Manual switchovers no longer fail because the browser lacks
   `CG_CONTROL_TOKEN`.
2. A manual high-risk operation cannot execute without a matching active
   one-time grant.
3. A grant cannot be reused or applied to another cluster, target, kind, or
   plan.
4. Automatic failover requires no human token and cannot be invoked as
   automatic through HTTP.
5. All existing safety, lock, fencing, verification, audit, report, Raft, and
   idempotency tests remain green.
6. The three-node test environment passes one manual switchover, one rejected
   grant-reuse attempt, and one automatic failover exercise.
