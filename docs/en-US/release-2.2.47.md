# ClusterGuard HA 2.2.47 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.47.md)

Release date: 2026-08-30
Version: `2.2-47`

## Overview

`v2.2.47` closes the admission race between a managed rolling update and automatic recovery. Even when the execute request observed zero active operations, a degraded cluster could admit a new recovery before update maintenance was established and safely block the update. A new recovery admitted immediately after maintenance release could also make an already completed update appear to have failed.

Without weakening quorum, identity, or version contracts, this release gates the current Raft Leader first and then the other controllers. Operations already admitted are allowed to drain before any RPM mutation. At completion, followers leave maintenance first and the current Leader leaves last, reopening automatic recovery only after every controller has exited software-update maintenance.

This release includes the job-status permission and terminal-state fixes from `2.2-46`. It updates only the ClusterGuard control plane, Agent, and update tools and does not modify database software, data directories, replication relationships, or the application endpoint.

## Changes

- Execute may establish maintenance under a stable topology and quorum without requiring a continuously empty pre-gate operation snapshot.
- The current Leader enters update maintenance first, blocking newly admitted switchover, recovery, and node-lifecycle mutations.
- After all gates exist, active, indeterminate, and lifecycle work must still drain completely before RPM mutation.
- Followers leave update maintenance first and the current Leader leaves last.
- Final acceptance still validates target versions, binary contracts, Leader, quorum, and gate release, while allowing normal recovery admitted after the gate is released.
- Added regressions for recovery admission races, Leader-first acquisition, and Leader-last release.

## Upgrade paths

Standard path:

```text
clusterguard-ha-2.2-46_to_2.2-47.x86_64.cgupgrade
```

Sites still running `2.2-44` should use the direct package shipped with this release:

```text
clusterguard-ha-2.2-44_to_2.2-47.x86_64.cgupgrade
```

Generate a read-only plan first, then execute the rolling update in a maintenance window. Automatic failover is paused during the update and mutation admission resumes only after existing work drains, every node is updated, and all contracts pass verification.
