# ClusterGuard HA Control-Plane Operability Plan

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/plans/2026-07-20-control-plane-operability.md)
<!-- /LANGUAGE-SWITCH -->


1. Add failing API tests for liveness, fail-closed readiness, detailed status, and request correlation.
2. Add failing Raft tests for stable local identity, role, leader, quorum, term, and index reporting.
3. Implement the Raft status snapshot and runtime status provider.
4. Add authenticated status routing, public probes, request IDs, and operational counters.
5. Add control-plane Prometheus metrics and `cgctl status` human/JSON output.
6. Add a compact console diagnostics panel and systemd preflight configuration validation.
7. Run focused tests after every change, then full tests, build, formatting, diff checks, and scope scans.
