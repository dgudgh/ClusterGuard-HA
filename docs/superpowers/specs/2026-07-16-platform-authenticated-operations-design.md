# ClusterGuard HA Platform Authentication Design

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/specs/2026-07-16-platform-authenticated-operations-design.md)
<!-- /LANGUAGE-SWITCH -->


## Goal

Replace manual approval-token entry in the ClusterGuard HA web console with
authenticated platform sessions. A signed-in operator starts a MySQL operation
with one click; the server creates and consumes the plan-bound one-time approval
grant internally. The browser never receives or stores that grant.

The initial platform administrator is:

- username: `admin`
- password: `generated-bootstrap-password`
- role: `admin`

The bootstrap password is stored only as a password hash. The initial
administrator must change it before any mutating platform operation is allowed.

## Chosen Approach

ClusterGuard will use server-side, Raft-replicated users and sessions.
Authentication is represented by an opaque session cookie; authorization is
represented by a platform role. Browser mutations additionally require a CSRF
token. High-risk database operations retain the existing single-use approval
grant internally, preserving the workflow invariant without asking the user to
paste a token.

This is preferred over:

1. Having the browser request and replay approval grants. That removes the
   manual prompt but still exposes the grant to JavaScript and browser tooling.
2. Using a permanent browser/API token. That is simpler but loses operation,
   target, topology, plan, expiry, and single-use binding.

## Security Model

### Passwords

Passwords are hashed with Argon2id using a per-password random salt. Stored user
records contain only the encoded Argon2id hash and password metadata. Password
plaintext is never written to the metadata snapshot, logs, audit messages, API
responses, or command arguments.

The production Argon2id profile is:

- memory: 64 MiB
- iterations: 3
- parallelism: 2
- salt: 16 random bytes
- key: 32 bytes

Tests use an injected lower-cost profile without changing production defaults.

New passwords must:

- contain at least 12 characters;
- differ from the current password;
- not equal the bootstrap password `generated-bootstrap-password`.

### Bootstrap Administrator

At startup, the leader ensures that a normalized `admin` user exists. If no
user with that username exists, ClusterGuard creates it with the bootstrap
password hash, role `admin`, and `must_change_password=true`.

Bootstrap is idempotent and committed through the replicated metadata store.
Existing users are never overwritten on restart.

### Sessions

A successful login returns an opaque random session token in the
`clusterguard_session` cookie. The cookie is:

- `HttpOnly`;
- `SameSite=Strict`;
- `Path=/`;
- `Secure` whenever the request is HTTPS;
- limited to an eight-hour absolute lifetime.

Only the SHA-256 hash of the session secret is stored. Session state includes
the user ID, user authentication revision, issue time, expiry, revocation time,
and CSRF-token hash. Sessions do not use sliding expiry, avoiding a replicated
metadata write on every request.

Password change increments the user's authentication revision and revokes all
existing sessions for that user.

### CSRF

Login also sets a `clusterguard_csrf` cookie that is readable by same-origin
JavaScript. Every state-changing browser request must send the same value in
`X-CSRF-Token`. The server validates the token against the hash stored with the
session.

Bearer-authenticated service API calls do not use cookies and therefore do not
require CSRF validation. A Bearer control credential cannot directly execute a
high-risk database operation; it can only use the existing explicit
plan-and-grant API contract.

### Roles

The first implementation defines three stable roles:

- `admin`: full platform access, password management, metadata, lifecycle, and
  all database operations;
- `operator`: read access and supported database operations;
- `viewer`: read-only access.

Only the default administrator and self-password change are exposed in this
delivery. The model and policy boundary support later user-management APIs
without changing stored identities or session semantics.

Users with `must_change_password=true` may call only:

- login;
- current-session inspection;
- password change;
- logout.

All other API calls are blocked until the bootstrap password is changed.

## Data Model

### PlatformUser

```text
resource_id
username
display_name
role
password_hash
must_change_password
disabled
auth_revision
password_changed_at
created_at
updated_at
metadata_revision
```

Usernames are trimmed and compared case-insensitively. `resource_id` is the
immutable identity.

### PlatformSession

```text
resource_id
user_id
token_hash
csrf_hash
user_auth_revision
issued_at
expires_at
revoked_at
created_at
updated_at
metadata_revision
```

Both records are stored in the existing replicated metadata snapshot and
therefore survive process restart and controller failover.

## API

Public authentication routes:

```text
POST /api/v1/auth/login
```

Authenticated routes:

```text
GET  /api/v1/auth/me
POST /api/v1/auth/logout
POST /api/v1/auth/password
```

Login request:

```json
{
  "username": "admin",
  "password": "generated-bootstrap-password"
}
```

Login and current-session responses never include password hashes, session
hashes, or approval secrets. They return:

```json
{
  "status": "ok",
  "result": {
    "user": {
      "resource_id": "...",
      "username": "admin",
      "display_name": "Administrator",
      "role": "admin",
      "must_change_password": true
    }
  }
}
```

Login failures use one generic message to avoid username enumeration.

## Request Authorization

The root console HTML and login endpoint remain reachable without a session.
Monitoring endpoints retain their dedicated monitoring token. Agent reconciliation
retains its signed agent protocol.

All other `/api/v1` routes require one of:

- a valid platform session; or
- an existing Bearer service credential where that route explicitly supports
  service automation.

Role policy is evaluated before route execution. Cookie-authenticated mutation
requests require CSRF validation. Leader and quorum mutation authority checks
remain independent and run after authentication.

## Platform-Owned Approval

For a session-authenticated database execution:

1. Resolve the authenticated user and role.
2. Require `admin` or `operator`.
3. Run durable `PRECHECK` and `PLAN`.
4. Create a five-minute, plan-bound `ApprovalGrant` with
   `issued_by=<username>`.
5. Keep the plaintext grant only in server memory.
6. Enter the existing workflow.
7. Under the operation lock, atomically consume the grant and advance
   `APPROVE`.
8. Execute, verify, audit, and report.
9. Discard the plaintext grant before returning.

The operation API response contains operation and verification state, not the
grant token.

Explicit service automation retains:

```text
POST /api/v1/approvals
POST /api/v1/operations/{id}/execute
```

Those routes continue to require administrator authorization plus the explicit
single-use grant. They do not become a browser shortcut.

Automatic failover remains separate. It uses the existing internal incident
identity and cannot be selected through JSON input.

## Web Console

The console gains:

- a dedicated login screen;
- first-login password-change screen;
- current username and role in the shell;
- change-password and logout commands;
- an authentication-expired return-to-login flow.

The console removes:

- the administrator control-token field;
- the manual one-time approval-token dialog;
- approval-token JavaScript state.

The existing operator lock remains as the deliberate-action safeguard. Clicking
`执行切换` after unlocking sends the selected cluster and candidate; the server
owns approval issuance and consumption.

## Error Handling

- Invalid login: `401`, generic invalid-credentials message.
- Missing/expired/revoked session: `401`.
- Missing or invalid CSRF token: `403`.
- Insufficient role: `403`.
- Bootstrap password not changed: `403` with
  `password_change_required=true`.
- Duplicate login bootstrap race: idempotently return the persisted user.
- Approval creation or persistence failure: fail before database mutation.
- Session persistence or Raft failure: fail login or mutation closed.
- Password change persistence failure: keep the old password and sessions.

## Audit

Operation audit records use the authenticated username as `requested_by`.
Approval audit records identify that the grant was platform-issued for the
session user. Login success, login failure, logout, and password change are
recorded as security events without password, cookie, CSRF, or IP-secret data.

## Testing

Tests must cover:

- bootstrap administrator creation and restart idempotency;
- Argon2id hashing and plaintext absence;
- correct and incorrect login;
- disabled user and forced password change;
- session restart and Raft replication;
- session expiry, revocation, and auth-revision invalidation;
- CSRF required for cookie-authenticated mutation;
- role authorization;
- password change revokes all sessions;
- web execution creates and consumes an internal grant;
- browser response never contains a grant secret;
- Bearer control token cannot bypass high-risk approval;
- automatic failover remains tokenless and internal-only;
- console contains login/change-password/logout and no approval-token input;
- enterprise-disabled and unsupported adapter behavior remains fail-closed;
- full MySQL workflow, verification, audit, and report regression coverage.

## Scope

This delivery completes platform authentication for the current MySQL control
experience. PostgreSQL, Oracle, and SQL Server remain registered adapters with
their existing unsupported execution capabilities. User-administration screens,
SSO, LDAP, OIDC, MFA, and personal API tokens are intentionally deferred.
