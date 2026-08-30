# ClusterGuard HA 2.2.46 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.46.md)

Release date: 2026-08-30
Version: `2.2-46`

## Overview

`v2.2.46` fixes the graphical rolling updater incorrectly showing "operation outcome requires verification" or "uploaded" after a safe preflight block or an automatic rollback. The privileged Helper was replacing the authoritative `status.json` written by the update runner after any non-zero process exit and publishing it with permissions unreadable by the console service.

This release defines one permission contract for update job directories and evidence files and preserves terminal states written by the update runner. Safety checks continue to fail closed, while the console now reports the actual reason, maintenance state, and automatic-failover availability instead of an unknown outcome.

This release updates only the ClusterGuard control plane, Agent, and update tools. It does not modify database software, data directories, replication relationships, or the application endpoint.

## Changes

- Update job directories use cooperative mode `0770`; packages, metadata, status, output, and events use mode `0640`.
- Existing directories with legacy permissions are normalized during upload, job startup, package distribution, and progress replication.
- Runner terminal states `planned`, `succeeded`, `failed`, and `rolled_back` are no longer replaced by a generic Helper process error.
- The Helper writes a fallback failure only when the child process leaves no terminal state.
- After a Leader change, the new Leader can read the same signed package and write queued, resume, or rollback status.
- Added regressions for Helper startup failure, safe preflight blocks, automatic rollback terminal state, and directory permissions.

## Upgrade paths

Standard path:

```text
clusterguard-ha-2.2-45_to_2.2-46.x86_64.cgupgrade
```

Sites still running `2.2-44` may use the controlled bridge package shipped with this release:

```text
clusterguard-ha-2.2-44_to_2.2-46.x86_64.cgupgrade
```

Generate a read-only plan first, then execute the rolling update in a maintenance window. Planning does not modify any node. Execution publishes and verifies the same signed package on every controller before it establishes maintenance gates and upgrades nodes one at a time.
