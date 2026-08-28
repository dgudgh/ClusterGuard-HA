# ClusterGuard HA 2.2.42 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.42.md)

Release date: 2026-08-28  
Version: `2.2-42`

## Overview

`v2.2.42` fixes graphical update-package upload permissions and completes resume, rollback, and status presentation after an interrupted rolling update. This release updates only ClusterGuard controllers, agents, and update tooling. It does not modify database software, data directories, replication, or business ingress.

## Changes

- The RPM configures the signed-package inspector as `root:clusterguard 0750`, allowing the control service to inspect packages without making the tool executable by ordinary local users.
- The control API now reports the underlying operating-system error when the inspector cannot start instead of returning an empty diagnostic.
- Update packages exclude build-host extended attributes, avoiding irrelevant Linux extraction warnings.
- Interrupted rolling updates can resume or roll back under control, with the live node version checked before a node is processed again.
- The settings page retains its compact two-column layout while update state, progress, and history continue to recover from durable job records.

## Safety Boundaries

- Automatic failover remains paused during update maintenance.
- Upload and signature verification do not enter maintenance or modify a database.
- Maintenance starts only when an update is executed and is released only after every node and the Raft majority pass verification.
- Each signed package contains both the target and rollback RPMs.

## Upgrade Paths

For a deployment currently running `2.2-40`, upload:

```text
clusterguard-ha-2.2-40_to_2.2-42.x86_64.cgupgrade
```

For a deployment already running `2.2-41`, upload:

```text
clusterguard-ha-2.2-41_to_2.2-42.x86_64.cgupgrade
```

Generate the read-only plan first, then execute the rolling update during a maintenance window. Continue monitoring database roles, business ingress, VIP ownership, and the Raft majority throughout the update.
