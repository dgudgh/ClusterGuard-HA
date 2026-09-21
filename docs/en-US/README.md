# ClusterGuard HA English Documentation

The offline HTML documentation center starts at [`../html/index.html`](../html/index.html). It includes this manual, the migration runbook, qualification evidence, local search, and print-friendly pages without an external network dependency.

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/README.md)
<!-- /LANGUAGE-SWITCH -->

## Current Version

### Latest Installer

The newest installer kit is **2.2-102**, whose release notes are maintained in Simplified
Chinese only: [2.2-102 release notes](../zh-CN/release-2.2.102.md).
It is one kit containing MySQL 8.0.44 / PostgreSQL 16.4 media plus a standalone
ClusterGuard RPM, built from code baseline `7b36461` (the media `RELEASE-INFO` records
`2b9a449`, the commit that added this release note and the index entries without changing
any code, so the two are code-equivalent). The archive was built and verified locally but
was neither uploaded to GitHub nor accepted on site; production `.cgupgrade` and final
site installation, upgrade, and rollback acceptance remain outstanding.
`release_channel=stable` inside the package does not change that.

The previous kit,
[2.2-101 release notes](../zh-CN/release-2.2.101.md) ·
[GitHub v2.2.101](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101), was
verified and uploaded as a GitHub prerelease from commit `78dbdbf`.

English release notes are maintained through
[2.2.47](release-2.2.47.md); every later release note exists only in Simplified Chinese,
and the ones present today are 2.2.69-2.2.73, 2.2.86 and 2.2.88-2.2.102. The gaps in
between never had a release note generated, so they are not broken links.
Machine-verified checksums and the latest documentation corrections are governed by the
2.2-102 release notes. Updater fixes and remaining gates are recorded in the
[private execution workspace record](../zh-CN/updater-private-workspace-2026-09-11.md).

### Historical Formal Releases

| Version | Status | Database Support Boundary |
| --- | --- | --- |
| `2.1-45` | Officially Released | MySQL High Availability Control Platform |
| `2.2-39` | Formal Release | PostgreSQL 16.4, Docker Swarm, and retained MySQL 2.1 capability |
| Subsequent Versions | Planning | Oracle Data Guard Broker, SQL Server Always On independent acceptance |

Current Formal Release:

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39>

Do not use historical candidate packages with numbers higher than `2.1-45` in the local directory to replace the official release. The official deliverables must come from GitHub Release and be verified with the included SHA256 checksum.

## Recommended Reading Order

For the newest installer kit, start with the
[2.2-102 release notes](../zh-CN/release-2.2.102.md) (Simplified Chinese only).

The order below follows the formal `2.2-39` baseline:

1. [2.2.47 Release Notes](release-2.2.47.md)
   Confirm the official package, summary, support scope, and production admission boundary.
2. [2.2.46 Release Notes](release-2.2.46.md)
3. [2.2.45 Release Notes](release-2.2.45.md)
4. [2.2.44 Release Notes](release-2.2.44.md)
5. [2.2.43 Release Notes](release-2.2.43.md)
6. [2.2.42 Release Notes](release-2.2.42.md)
7. [2.2.41 Release Notes](release-2.2.41.md)
8. [2.2.40 Release Notes](release-2.2.40.md)
9. [2.2.39 Release Notes](release-2.2.39.md)
10. [Product Tour](product-tour.md)
   Understand the topology, operations, node lifecycle, and operation logs through actual control console screenshots.
11. [Offline Installation and Deployment Manual](offline-rpm-install.md)
   Complete the deployment of control nodes, data nodes, databases, Agent, Raft, VIP, and certificates.
12. [Database Preparation Manual](database-preparation.md)
   Prepare native database identity, minimal permissions, replication, and health check conditions.
13. [Migration from Orchestrator](orchestrator-migration.md)
   Onboard existing MySQL clusters without reinstalling data and transfer exclusive recovery and VIP authority safely.
14. [Operations Manual](operations-manual.md)
   Execute failover, old primary recovery, node expansion, planned shutdown, audit, and emergency handling.
15. [Version Update and Rollback Guide](update-and-patch.md)
   Verify signed update packages, review the plan, roll nodes, resume interrupted work, and perform controlled rollback.
16. [Version and Release Policy](version-release-policy.md)
   Use when building new versions, maintaining tags, and releasing PostgreSQL 2.2.
17. [Architecture](../architecture.md)
   Review the control plane, metadata storage, consensus, node agent and workflow stages before changing deployment topology.
18. [PostgreSQL HA](../postgresql-ha.md)
   Understand native PostgreSQL identity, streaming replication, controlled switchover, automatic takeover and former-primary rewind.
19. [Control-Plane and API Reference](../operations.md)
   Look up API paths, roles, approval grants, console behaviour and operational limits.
20. [Proven MySQL HA Methods](../proven-mysql-ha-methods.md)
   See which MySQL HA mechanisms have been validated for this product, and under what evidence window.
21. [MySQL Feature Parity Acceptance](../mysql-feature-parity-acceptance.md)
22. [MySQL Production Qualification](../mysql-production-qualification-2026-07-28.md)
   Confirm the measured client interruption and the conditions that must be met before production admission.

## Acceptance Evidence

- [MySQL Former-Primary Recovery Qualification](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [Production Chaos and Concurrency Test Report](production-chaos-test-report-2026-08-09.md)
- [Planned Shutdown and Automatic Recovery Report](power-lifecycle-test-report.md)
- [PostgreSQL 16.4 Production Qualification](postgresql-production-qualification-2026-08-23.md)
- [MySQL Feature Parity Acceptance](../mysql-feature-parity-acceptance.md)
- [MySQL Production Qualification](../mysql-production-qualification-2026-07-28.md)
- [Docker Swarm MySQL Lab Qualification](docker-swarm-mysql-validation-plan.md)
- [Kubernetes MySQL Guide](kubernetes-mysql.md)

These reports record results under specific laboratories, database packages, and dates. After changing the database minor version, Linux distribution, storage, network, VIP NIC, or isolation method, on-site acceptance must be re-executed.

## PostgreSQL 2.2

Starting from version 2.2, PostgreSQL will not be rolled back to `v2.1.45`. The 2.2 main offline package remains streamlined: when explicitly specifying `--engine postgresql`, it defaults to resolving source code compilation dependencies online within the isolated build root. When on-site internet access is unavailable, upload a separate PostgreSQL dependency package and use `--postgresql-dependencies`.

Database permissions, native identity, streaming replication, and recovery requirements for PostgreSQL are detailed in the [Database Preparation Manual](database-preparation.md); complete installation parameters are detailed in the [Offline Installation and Deployment Manual](offline-rpm-install.md).

The three-node PostgreSQL 16.4 matrix, including planned rotations, hard
power-off, network partition, quorum loss, concurrent operations, writer-VIP
uniqueness, and former-primary `pg_rewind`, is recorded in the
[PostgreSQL Production Qualification Report](postgresql-production-qualification-2026-08-23.md).

## Docker Swarm MySQL

The first Docker Swarm phase uses host Agents, fixed Service slots, MySQL GTID replication, and a host VIP. See the
[Docker Swarm MySQL Guide](docker-swarm-mysql.md) for architecture, deployment order, and safety constraints. The
[Docker Swarm MySQL Lab Qualification](docker-swarm-mysql-validation-plan.md) records three-node planned switchovers, automatic failover, former-writer rejoin, manager interruption, Raft quorum loss, and duplicate-VIP results from `192.168.102.152-154`.

## Kubernetes MySQL

Kubernetes mode does not move a host VIP or change CoreDNS. ClusterGuard uses a Raft-authorized selectorless Service/EndpointSlice, dedicated one-replica StatefulSets, durable role annotations, and a fail-closed start guard. See the [Kubernetes MySQL Guide](kubernetes-mysql.md) for RBAC, registration, deployment constraints, and the current acceptance boundary. This feature currently has code-level automated tests but no real Kubernetes production qualification report.

## Document Validity

The following documents are the production delivery entry points:

- `docs/en-US/release-*.md`
- `docs/en-US/offline-rpm-install.md`
- `docs/en-US/database-preparation.md`
- `docs/en-US/operations-manual.md`
- `docs/en-US/update-and-patch.md`
- `docs/en-US/version-release-policy.md`
- `docs/architecture.md`
- `docs/operations.md`
- `docs/postgresql-ha.md`
- `docs/proven-mysql-ha-methods.md`
- `docs/mysql-feature-parity-acceptance.md`
- `docs/mysql-production-qualification-2026-07-28.md`

`docs/superpowers/` preserves historical design and implementation plans and is only used for traceability, not as the current installation or production operation manual. When documents are inconsistent with the official release, the corresponding `RELEASE-INFO`, summary file, and release notes of that version shall prevail.

## Information Required for Issue Feedback

When submitting an issue, provide at least the following:

- Complete ClusterGuard version and Git commit;
- Database engine, complete version, port, and cluster UUID;
- Control node Leader, Raft members, and quorum status;
- Operation UUID, request ID, occurrence time, and operation type;
- De-identified operation report, audit records, and Agent/systemd logs;
- Primary, replication source, and VIP Owner before and after the issue occurred.

Do not submit database passwords, control tokens, approval tokens, private keys, or complete `deployment-secrets.env` in tickets, screenshots, or chat records.
