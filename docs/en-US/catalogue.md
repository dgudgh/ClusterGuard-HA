# ClusterGuard HA English Full Catalogue and Historical Links

> Full catalogue of manuals and historical release and acceptance links. This is a lookup index, not a reading order for each change.

**Before development or document changes, read the [repository rules](../../AGENTS.md), [gate workflow](../zh-CN/validation-gate-workflow.md), and [reading order and applicable rules](../development/rules/README.md), then select a [development module](../development/README.md).** Read the complete [mandatory v2 contract](../upgrade-validation-chain.md) before changes or actions within its scope.

## 当前完整介质 / Current Installation Kit

[3.1.2.8 双语发布说明 / release notes](release-3.1.2.8.md) · [GitHub v3.1.2.8](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.2.8)。产品版本 3.1.2.8，RPM 兼容基线 2.2-106；GitHub 预发布，FIELD OPEN。 / Product 3.1.2.8, RPM baseline 2.2-106; prerelease with FIELD OPEN. Historical entries below preserve their original scope.

## Find Documents by Role

| Document role | Entry and authority |
| --- | --- |
| Current mandatory rules | [Reading order and applicable rules](../development/rules/README.md), [mandatory contract](../upgrade-validation-chain.md), [release policy](version-release-policy.md), and [licensing](licensing.md); follow them before the triggering action |
| Operations instructions | [English task entry](README.md); use the matching version's installation, database preparation, operations, or update guide after the applicable rules |
| Design and reference | Product tours, architecture and API references explain design or usage boundaries; they do not replace mandatory rules |
| Historical acceptance and releases | Version records, dated reports and engineering records below describe their artifacts and validation scope; they do not establish acceptance of the current site |

## Recorded Source Repair and Acceptance

Recorded source repair baseline: `7b643f4` (2026-10-03), on the sole mainline `codex/2.2-postgresql`. That repair enforces the v2 contract: failed hotfixes use `retry`; interrupted rolling updates use `resume`. Its new-artifact and site ART/FIELD acceptance record remains **OPEN**; that repair produced no new package or production deployment. See the [implementation and acceptance record](upgrade-validation-chain-implementation-status.md). This does not replace verification of the current checkout, package, or site.

The offline HTML documentation center starts at [`../html/index.html`](../html/index.html). It includes this manual, the migration runbook, qualification evidence, local search, and print-friendly pages without an external network dependency.

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/README.md)
<!-- /LANGUAGE-SWITCH -->

## Recorded Versions and Installation Media

### 2.2-102 and 2.2-101 Media Records

The recorded **2.2-102** installer kit has release notes maintained in Simplified
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
[2.2.47](release-2.2.47.md); the subsequent historical 2.2 release notes exist only in Simplified Chinese,
and this historical list records 2.2.69-2.2.73, 2.2.86 and 2.2.88-2.2.102. The gaps in
between never had a release note generated, so they are not broken links.
Checksums and documentation corrections for that artifact are recorded in the
2.2-102 release notes; they cannot override current mandatory rules. Updater fixes and remaining gates are recorded in the
[private execution workspace record](../zh-CN/updater-private-workspace-2026-09-11.md).

### Historical Formal Releases

| Version | Status | Database Support Boundary |
| --- | --- | --- |
| `2.1-45` | Officially Released | MySQL High Availability Control Platform |
| `2.2-39` | Formal Release | PostgreSQL 16.4, Docker Swarm, and retained MySQL 2.1 capability |
| Subsequent Versions | Planning | Oracle Data Guard Broker, SQL Server Always On independent acceptance |

Recorded Formal `2.2.39` Release:

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39>

Do not use historical candidate packages with numbers higher than `2.1-45` in the local directory to replace the official release. The official deliverables must come from GitHub Release and be verified with the included SHA256 checksum.

## Historical Document List (Original 2.2-39 Baseline)

The 2.2-102 media record is in the
[2.2-102 release notes](../zh-CN/release-2.2.102.md) (Simplified Chinese only).

The list preserves links from the original formal `2.2-39` baseline. Its order is not the reading priority for a current change or action; use the rules and task entries above first.

- [2.2.47 Release Notes](release-2.2.47.md)
   Confirm the official package, summary, support scope, and production admission boundary.
- [2.2.46 Release Notes](release-2.2.46.md)
- [2.2.45 Release Notes](release-2.2.45.md)
- [2.2.44 Release Notes](release-2.2.44.md)
- [2.2.43 Release Notes](release-2.2.43.md)
- [2.2.42 Release Notes](release-2.2.42.md)
- [2.2.41 Release Notes](release-2.2.41.md)
- [2.2.40 Release Notes](release-2.2.40.md)
- [2.2.39 Release Notes](release-2.2.39.md)
- [Product Tour](product-tour.md)
   Understand the topology, operations, node lifecycle, and operation logs through actual control console screenshots.
- [Offline Installation and Deployment Manual](offline-rpm-install.md)
   Complete the deployment of control nodes, data nodes, databases, Agent, Raft, VIP, and certificates.
- [Database Preparation Manual](database-preparation.md)
   Prepare native database identity, minimal permissions, replication, and health check conditions.
- [Migration from Orchestrator](orchestrator-migration.md)
   Onboard existing MySQL clusters without reinstalling data and transfer exclusive recovery and VIP authority safely.
- [Operations Manual](operations-manual.md)
   Execute failover, old primary recovery, node expansion, planned shutdown, audit, and emergency handling.
- [Version Update and Rollback Guide](update-and-patch.md)
   Verify signed update packages, review the plan, roll nodes, resume interrupted work, and perform controlled rollback.
- [Version and Release Policy](version-release-policy.md)
   Use when building new versions, maintaining tags, and releasing PostgreSQL 2.2.
- [Architecture](../architecture.md)
   Review the control plane, metadata storage, consensus, node agent and workflow stages before changing deployment topology.
- [PostgreSQL HA](../postgresql-ha.md)
   Understand native PostgreSQL identity, streaming replication, controlled switchover, automatic takeover and former-primary rewind.
- [Control-Plane and API Reference](../operations.md)
   Look up API paths, roles, approval grants, console behaviour and operational limits.
- [Proven MySQL HA Methods](../proven-mysql-ha-methods.md)
   See which MySQL HA mechanisms have been validated for this product, and under what evidence window.
- [MySQL Feature Parity Acceptance](../mysql-feature-parity-acceptance.md)
- [MySQL Production Qualification](../mysql-production-qualification-2026-07-28.md)
   Confirm the measured client interruption and the conditions that must be met before production admission.

## Historical Acceptance Evidence

- [MySQL Former-Primary Recovery Qualification](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [Production Chaos and Concurrency Test Report](production-chaos-test-report-2026-08-09.md)
- [Planned Shutdown and Automatic Recovery Report](power-lifecycle-test-report.md)
- [PostgreSQL 16.4 Production Qualification](postgresql-production-qualification-2026-08-23.md)
- [MySQL Feature Parity Acceptance](../mysql-feature-parity-acceptance.md)
- [MySQL Production Qualification](../mysql-production-qualification-2026-07-28.md)
- [Docker Swarm MySQL Lab Qualification](docker-swarm-mysql-validation-plan.md)

Related operations instructions: [Kubernetes MySQL Guide](kubernetes-mysql.md), which is not a historical acceptance report.

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

Kubernetes mode does not move a host VIP or change CoreDNS. ClusterGuard uses a Raft-authorized selectorless Service/EndpointSlice, dedicated one-replica StatefulSets, durable role annotations, and a fail-closed start guard. See the [Kubernetes MySQL Guide](kubernetes-mysql.md) for RBAC, registration, deployment constraints, and the recorded acceptance boundary. The guide records code-level automated tests and provides no real Kubernetes production qualification report.

## Document Validity

Current changes and actions first follow the repository rules and applicable mandatory contracts. Use the following records according to their role; they are not equivalent proof of production acceptance:

- Mandatory rules: `docs/en-US/version-release-policy.md`, `docs/en-US/licensing.md`, and the repository rules, applicable rules, and upgrade contract linked above.
- Operations instructions: `docs/en-US/offline-rpm-install.md`, `docs/en-US/database-preparation.md`, `docs/en-US/operations-manual.md`, and `docs/en-US/update-and-patch.md`; satisfy applicable rules before actions and verify the matching runtime version.
- Design and reference: `docs/architecture.md`, `docs/operations.md`, `docs/postgresql-ha.md`, `docs/proven-mysql-ha-methods.md`, product tours and engineering designs; use them to understand or trace behavior, not to replace mandatory rules or actual acceptance.
- Historical records: `docs/en-US/release-*.md`, `docs/mysql-feature-parity-acceptance.md`, `docs/mysql-production-qualification-2026-07-28.md`, and the reports above; they record their artifacts, source and validation scope only.

**The publication channel split is a hard rule.** GitHub Releases carry complete
offline installation media only; signed `.cgupgrade` update packages (including
legacy `.cgpatch`) are delivered to contracted enterprise customers and kept
locally only — they **must not** be uploaded to any public channel. The converse
holds as well: a release that carries no complete offline media **must not stay on
the public channel**. Run `node tools/verify-public-release-assets.cjs` before and
after uploading and require `status=passed`; an update package on a public channel
is treated as unauthorized distribution. See
[Version and Release Policy §3.1](version-release-policy.md).

**Licensing is a hard rule.** The delivery line uses `AGPL-3.0-only`; the authoritative text is
`LICENSE` at the repository root. The `license` field in `packaging/rpm/nfpm.yaml`, the file
`/usr/share/doc/clusterguard-ha/LICENSE` inside the RPM and the READMEs must all agree with it.
Run `node tools/verify-license-consistency.cjs` after any dependency or packaging change. The
source-publishing obligations per usage scenario are in
[Licensing and Compliance](licensing.md).

`docs/superpowers/` preserves historical design and implementation plans for traceability. A release’s `RELEASE-INFO`, checksums and release notes establish its delivered identity and historical behavior; they cannot override current mandatory rules or replace acceptance for the current task.

## Information Required for Issue Feedback

When submitting an issue, provide at least the following:

- Complete ClusterGuard version and Git commit;
- Database engine, complete version, port, and cluster UUID;
- Control node Leader, Raft members, and quorum status;
- Operation UUID, request ID, occurrence time, and operation type;
- De-identified operation report, audit records, and Agent/systemd logs;
- Primary, replication source, and VIP Owner before and after the issue occurred.

Do not submit database passwords, control tokens, approval tokens, private keys, or complete `deployment-secrets.env` in tickets, screenshots, or chat records.
