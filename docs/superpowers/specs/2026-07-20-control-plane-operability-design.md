# ClusterGuard HA Control-Plane Operability Design

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/specs/2026-07-20-control-plane-operability-design.md)
<!-- /LANGUAGE-SWITCH -->


## Goal

Make controller availability, consensus safety, metadata freshness, and in-flight work observable through one consistent contract. This change does not alter database adapter execution or the HA workflow state machine.

## Status contract

The authenticated control-plane status contains:

- deployment mode, local controller identity and Raft role
- current leader identity and configured API address
- voter count, leader visibility, quorum confirmation, and mutation authority
- Raft term, log/applied indexes, snapshot CAS state, and metadata revision
- readiness reason, uptime, cluster count, active operations, indeterminate operations, and active lifecycle tasks

Standalone mode is ready after the metadata repository is open. In Raft mode a leader is ready only after quorum verification; a follower is ready only when the leader and its trusted API address are known. Candidate, shutdown, unknown-leader, and leader-without-quorum states fail closed.

## Exposure

- `GET|HEAD /healthz`: public liveness, minimal information, always independent of database health.
- `GET|HEAD /readyz`: public readiness, minimal reason code, HTTP 503 when unsafe.
- `GET /api/v1/control-plane/status`: authenticated detailed status.
- `/api/v1/monitoring/prometheus`: controller readiness, leadership, quorum, metadata revision, and work counters.
- `cgctl status`: operator-readable status with `--json` support.
- Console settings: restrained control-plane diagnostic panel.

## Request correlation

Every HTTP response carries `X-Request-ID`. A safe caller-provided ID is preserved; missing or malformed values are replaced. JSON errors include the same ID, and follower-to-leader mutation RPC preserves it.

## Delivery safeguards

- Tests are written before implementation.
- Public probes expose no controller addresses, resource IDs, inventory, or credentials.
- Detailed status remains behind existing platform authentication.
- Existing MySQL HA workflow, approval, lock, verification, and audit behavior is unchanged.
