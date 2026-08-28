# ClusterGuard HA English Documentation

The offline HTML documentation center starts at [`../html/index.html`](../html/index.html). It includes this manual, the migration runbook, qualification evidence, local search, and print-friendly pages without an external network dependency.

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/README.md)
<!-- /LANGUAGE-SWITCH -->

## Current Version

| Version | Status | Database Support Boundary |
| --- | --- | --- |
| `2.1-45` | Officially Released | MySQL High Availability Control Platform |
| `2.2-39` | Formal Release | PostgreSQL 16.4, Docker Swarm, and retained MySQL 2.1 capability |
| Subsequent Versions | Planning | Oracle Data Guard Broker, SQL Server Always On independent acceptance |

Current Formal Release:

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39>

Do not use historical candidate packages with numbers higher than `2.1-45` in the local directory to replace the official release. The official deliverables must come from GitHub Release and be verified with the included SHA256 checksum.

## Recommended Reading Order

1. [2.2.39 Release Notes](release-2.2.39.md)
   Confirm the official package, summary, support scope, and production admission boundary.
2. [Product Tour](product-tour.md)
   Understand the topology, operations, node lifecycle, and operation logs through actual control console screenshots.
3. [Offline Installation and Deployment Manual](offline-rpm-install.md)
   Complete the deployment of control nodes, data nodes, databases, Agent, Raft, VIP, and certificates.
4. [Database Preparation Manual](database-preparation.md)
   Prepare native database identity, minimal permissions, replication, and health check conditions.
5. [Migration from Orchestrator](orchestrator-migration.md)
   Onboard existing MySQL clusters without reinstalling data and transfer exclusive recovery and VIP authority safely.
6. [Operations Manual](operations-manual.md)
   Execute failover, old primary recovery, node expansion, planned shutdown, audit, and emergency handling.
7. [Version Update and Rollback Guide](update-and-patch.md)
   Verify signed update packages, review the plan, roll nodes, resume interrupted work, and perform controlled rollback.
8. [Version and Release Policy](version-release-policy.md)
   Use when building new versions, maintaining tags, and releasing PostgreSQL 2.2.

## Acceptance Evidence

- [MySQL Former-Primary Recovery Qualification](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [Production Chaos and Concurrency Test Report](production-chaos-test-report-2026-08-09.md)
- [Planned Shutdown and Automatic Recovery Report](power-lifecycle-test-report.md)
- [PostgreSQL 16.4 Production Qualification](postgresql-production-qualification-2026-08-23.md)
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
