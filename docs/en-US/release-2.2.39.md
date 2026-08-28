# ClusterGuard HA 2.2.39 Release Notes

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/release-2.2.39.md)
<!-- /LANGUAGE-SWITCH -->

Release date: 2026-08-28

`v2.2.39` is the first formal release of the ClusterGuard HA 2.2 line. It retains the MySQL HA capabilities of 2.1 and adds native PostgreSQL 16.4 HA, Docker Swarm database adoption, a Kubernetes writer-endpoint provider, graphical software updates, and runtime-aware node identity.

## Release Artifacts

- `clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz` for installation, reinstallation, and offline deployment;
- `clusterguard-ha-2.2-39.x86_64.rpm` containing the controllers, Agent, update helper, and operational scripts;
- `clusterguard-ha-2.2-update-signing-public.pem`, the update-package release public key established by this version;
- a `.sha256` file for every artifact and these release notes.

`.cgupgrade` is a complete signed rolling-update package, not a small binary delta. It contains the target RPM, rollback RPM, compatibility contract, SHA-256 checksums, and release signature. The full offline archive is for installation or reinstallation and cannot be uploaded directly to the Version Update page.

`2.2.39` establishes the managed-update protocol and the formal release key. This Release does not publish a generic `.cgupgrade` from historical laboratory media: the standard `2.2-38` package does not contain the managed-update framework, while the laboratory `2.2-38.field3` runtime does not match its RPM database contract. Beginning with the next formal version, Releases will include a `.cgupgrade` whose source version is `2.2.39`.

## Major Capabilities

- MySQL 5.7, 8.0, 8.4, and approved compatible distributions: discovery, planned switchover, automatic failover, exclusive VIP ownership, former-primary recovery, and node lifecycle;
- PostgreSQL 16.4: native identity, streaming replication, WAL/timeline evidence, planned switchover, failover, `pg_rewind`/`pg_basebackup`, and former-primary rejoin;
- three-node Raft control plane, mutual TLS, Leader quorum verification, durable operation state, approval, audit, and fail-closed Safety Guard;
- Docker Swarm MySQL and PostgreSQL with fixed service slots, host Agents, a host VIP, and runtime-specific operations;
- Kubernetes selectorless Service/EndpointSlice writer ingress, RBAC, runtime bindings, and a fail-closed start guard;
- graphical signed updates, read-only planning, Follower-to-Leader rolling order, resume, automatic rollback, and update history;
- authenticated console, mandatory first-login password change, role enforcement, CSRF, operation review, and bilingual offline documentation.

## Qualification Boundary

- Native MySQL and PostgreSQL 16.4 have three-node laboratory evidence covering switching, failure, former-primary recovery, network partition, quorum loss, and VIP uniqueness;
- Docker Swarm support is implemented and laboratory-tested, but each site must requalify its exact image, volume, port mapping, and network;
- Kubernetes support has code-level automated tests but no real-cluster production qualification report;
- Oracle Data Guard Broker and SQL Server Always On remain separate future product lines and are not formal capabilities of this release.

## Installation and Update

Install with the full offline archive:

```bash
tar -xzf clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-39-offline-linux-x86_64
./install_clusterguard.sh --help
```

Historical `2.2-38` and laboratory `2.2-38.field3` installations must first use the bridge maintenance procedure to install the formal `2.2.39` RPM one node at a time, or redeploy from the full offline archive. After every node passes version, Raft quorum, Agent, and database-ingress verification, later versions can be applied under **Settings → Version Update** with the corresponding `.cgupgrade`.

> **Important: automatic failover is unavailable while a system update is in progress. Monitor the platform and database service throughout the maintenance window.**

The laboratory-only `2.2-38.field3` binary is not a formal RPM release: the RPM database still reports `2.2-38`, so it does not satisfy the exact source-version contract. Restore that environment to a traceable RPM through the bridge procedure or redeploy `2.2.39`; do not bypass version checks.

See the [Offline Installation Guide](offline-rpm-install.md) and [Version Update and Rollback Guide](update-and-patch.md).

## Release Verification

Download formal artifacts from GitHub Release and verify each adjacent `.sha256` file. Tags, attachments, and checksums are immutable; any code, script, or documentation change requires a new version.
