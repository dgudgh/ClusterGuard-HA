# ClusterGuard HA 2.2.40 Release Notes

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/release-2.2.40.md)
<!-- /LANGUAGE-SWITCH -->

Release date: 2026-08-28

`v2.2.40` fixes two blockers in graphical rolling updates for container data planes. It does not change database replication, business ingress, or HA policy.

## Fixes

- Raft quorum is confirmed by the current Leader. Followers must be ready and observe the same Leader, but are no longer required to report the Leader-only `quorum_confirmed=true` state;
- immutable host UUIDs and Docker Swarm or Kubernetes Agent logical UUIDs are validated independently;
- every controller must observe the same logical data-node set, and every logical-node `ip_address` must map to an explicitly supplied host;
- multiple container database instances may share one host, while unknown hosts, extra Raft members, duplicate logical UUIDs, and inconsistent observations remain rejected;
- the rolling order remains Followers, data-only nodes, and Leader, with automatic stop and embedded-RPM rollback on failure.

## Update Guidance

Formal `2.2.39` installations use the signed `.cgupgrade` supplied by this Release. The laboratory-only `2.2-38.field3` build has a different RPM/runtime contract and legacy update trust key, so it requires the site-restricted `.cgpatch` bridge and one-time update-channel bootstrap. A full offline installation archive must not be uploaded to the Version Update page.

Always generate a read-only plan first. Real execution enters the platform maintenance gate:

> **Automatic failover is unavailable while a system update is in progress. Monitor the platform throughout the maintenance window.**

The rolling ClusterGuard software update does not intentionally stop or rebuild database processes, replication, or the business VIP.

## Qualification Boundary

- automated updater tests cover native nodes, container logical identities, multiple instances on one host, Follower quorum semantics, extra controllers, and extra data hosts;
- the `192.168.102.152-154` laboratory site still requires page upload, read-only planning, rolling execution, version consistency, Raft quorum, database replication, and business-ingress verification;
- code-level test success is not production-site acceptance.
