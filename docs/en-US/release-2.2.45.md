# ClusterGuard HA 2.2.45 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.45.md)

Release date: 2026-08-30
Version: `2.2-45`

## Overview

`v2.2.45` fixes a console rolling-update defect where job metadata was copied but the signed update artifact itself remained only on the upload Leader. Before execution, the console now distributes the complete package and metadata to every controller, verifies SHA-256 on each node, and publishes each copy atomically. Maintenance gates and RPM changes start only after all controller copies are ready.

The update no longer depends on the Leader that received the upload. If Raft leadership changes during an update, the new Leader can use its protected local copy for resume or controlled rollback.

This release updates only ClusterGuard controllers, agents, and update tooling. It does not modify database software, data directories, replication, or business ingress.

## Changes

- Distributes `package.cgpatch` and `package.json` to all controllers before a console execute, resume, or rollback.
- Re-verifies package and metadata SHA-256 before atomic publication on each node and normalizes files to `root:clusterguard` mode `0640`.
- Aborts immediately on copy, permission, or digest failure, explicitly before maintenance gates and RPM installation.
- Preserves the signed in-package bootstrap, rolling order, maintenance locks, resume, automatic rollback, and historical progress contracts.
- Adds three-controller regressions for distribution failure and resume after a Leader change.

## Upgrade Path

For a deployment currently running `2.2-44`, upload:

```text
clusterguard-ha-2.2-44_to_2.2-45.x86_64.cgupgrade
```

Generate the read-only plan first, then execute during a maintenance window. Planning does not modify nodes. Execution distributes the package to every controller before automatic failover is paused and rolling work begins.
