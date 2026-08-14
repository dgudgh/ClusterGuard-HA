# PostgreSQL Read-Only Compatibility Design

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/specs/2026-07-20-postgresql-readonly-compatibility-design.md)
<!-- /LANGUAGE-SWITCH -->


## Goal

Make PostgreSQL a usable first-class read-only engine in ClusterGuard HA while
keeping every mutating PostgreSQL action fail-closed. The first delivery covers
registration, scheduled discovery, immutable identity, topology, health,
replication lag, candidate assessment, monitoring namespaces, and console
presentation.

## Chosen Approach

Use the installed `psql` client through a small adapter-owned query runner. This
matches the existing deployment model, avoids introducing a database driver into
the control-plane binary, and keeps credentials out of command-line arguments.
The runner uses `PGPASSWORD`, `PGCONNECT_TIMEOUT`, `--no-password`, and
`--no-psqlrc`; SQL returns one JSON object per row so values cannot break a
delimiter parser.

Patroni is not required. Patroni, repmgr, and vendor-specific APIs may become
optional providers later, but PostgreSQL compatibility must work against native
streaming replication first.

## Identity Contract

- Cluster identity is PostgreSQL `system_identifier` from `pg_control_system()`.
- Each server must expose an immutable UUID in the custom setting
  `clusterguard.node_id`.
- A standby must expose its upstream node UUID in
  `clusterguard.primary_node_id`.
- The adapter publishes `system_identifier` and `resource_id` in
  `engine_identity`; hostname, IP, and port remain mutable endpoints.
- A refresh containing different `system_identifier` values is rejected before
  publication.
- The first successful refresh binds the cluster to its system identifier.
  Future mismatches are rejected without changing the persisted snapshot.

The database settings are read-only inputs to this phase. ClusterGuard does not
silently create or rewrite them.

## Discovery And Health

One identity query collects version, recovery state, read-only state, timeline,
WAL positions, replay pause state, receiver state, replay timestamp lag, and the
ClusterGuard identity settings.

- A primary is healthy only when it is out of recovery and writable.
- A standby is healthy only when it is in recovery, read-only, streaming,
  replaying, and has a valid upstream node identity.
- Unknown replay timestamp produces unknown lag; it is never reported as zero.
- A standby topology edge is built from `primary_node_id` to `node_id`.
- Both `replica` and `standby` roles participate in generic topology health and
  fleet replica counts.

## Candidate Assessment

Candidate assessment is read-only. It requires a healthy standby, matching
`system_identifier`, an upstream identity matching the current primary,
streaming receive and replay states, a parseable replay LSN, and lag within
policy. Candidates sort by highest replay LSN, then lowest lag, then immutable
resource ID. Unknown lag or missing evidence blocks eligibility.

PostgreSQL candidate assessment does not interpret the MySQL-only GTID policy.

## Capabilities And Safety

Available capabilities:

- discover
- topology
- health
- candidates
- metadata reconciliation precheck

Unavailable capabilities:

- metrics until a truthful counter contract is defined
- precheck, plan, execute, verify
- node synchronization

Every unavailable method returns `adapter.ErrUnsupported`. The console disables
execution and MySQL-specific lifecycle controls when a PostgreSQL cluster is
selected.

## Configuration And Scheduling

Add a `postgresql` configuration block with `enabled`, discovery interval,
discovery timeout, and a dedicated discovery credential. The credential may
name the maintenance database and defaults to `postgres`.

MySQL and PostgreSQL get independent engine-filtered schedulers, so disabling
one engine does not probe or degrade clusters belonging to that engine.

## Monitoring And Console

Prometheus and Zabbix metric names are selected from the cluster engine rather
than hard-coded to MySQL. PostgreSQL currently exports replication lag from the
topology snapshot; synthetic QPS/TPS values are forbidden.

The topology inventory renders the native identity appropriate to the selected
engine. PostgreSQL standbys use standby language, and unsupported execution or
node lifecycle controls explain the reason instead of accepting a request.

## Failure Semantics

Credential failure, malformed JSON, missing identity, invalid UUID, mixed
system identifiers, unknown upstream identity, paused replay, non-streaming WAL
receiver, and stale inventory all fail closed. A failed refresh does not publish
partial topology or mutate cluster identity.

## Verification

- Adapter unit tests use fixture rows and a fake runner.
- Runner tests verify password secrecy, timeout handling, JSON parsing, and
  cancellation.
- Discovery/store tests verify first identity binding, mismatch rejection, and
  hostname/port changes without duplicate instances.
- Runtime/config tests verify independent credentials and schedulers.
- API/console tests verify engine-specific monitoring names and disabled
  mutation controls.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, JSON validation, and
  `git diff --check` must pass.
