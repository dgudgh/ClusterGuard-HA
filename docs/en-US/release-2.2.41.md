# ClusterGuard HA 2.2.41 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.41.md)

Release date: 2026-08-28  
Version: `2.2-41`

## Overview

`v2.2.41` completes the graphical rolling-update progress experience and preserves status visibility while control-plane leadership changes. This release updates only ClusterGuard controllers, agents, and update tooling. It does not modify database software, data directories, replication, or business ingress.

## Highlights

- Opens a progress dialog after an execute, resume, or rollback request is confirmed.
- Shows four evidence-based phases: safety preparation, node rollout, cluster verification, and completion.
- Shows the current node, verified-node count, maintenance gate, version transition, and recent events.
- Allows the dialog to run in the background and exposes a persistent View progress action in the maintenance banner.
- Restores progress from persisted job records after a page reload.
- Replicates update events and status to all controllers so a new Raft Leader can continue presenting progress after a controller restart or leadership change.
- Records a completed automatic rollback as Rolled back instead of a generic update failure.

## Safety Boundaries

- Automatic failover remains paused throughout update maintenance.
- The percentage represents completed safety steps, not estimated elapsed time.
- The rollout order remains controller followers, data-only nodes, then the Raft Leader.
- Success is reported only after node versions, readiness, Raft quorum, and maintenance release are verified.

## Upgrade Path

To upgrade from `2.2-40`, upload the signed package shipped with the same release:

```text
clusterguard-ha-2.2-40_to_2.2-41.x86_64.cgupgrade
```

Generate the read-only plan first, then execute the rolling update during a maintenance window while observing database replication, business ingress, VIP ownership, and Raft quorum.
