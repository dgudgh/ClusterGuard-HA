# ClusterGuard HA 2.2.43 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.43.md)

Release date: 2026-08-28
Version: `2.2-43`

## Overview

`v2.2.43` fixes rolling-update jobs falling back to an “uploaded” state after execution and establishes the signed in-package updater contract. Upgrade-orchestration fixes can now take effect before the target RPM is installed instead of leaving the full operation under source-version scripts that may already be stale.

This release updates only ClusterGuard controllers, agents, and update tooling. It does not modify database software, data directories, replication, or business ingress.

## Changes

- Each new `.cgupgrade` declares an embedded bootstrap updater, protocol, and SHA-256 in its release-signed manifest. A missing or modified bootstrap is rejected before maintenance begins.
- The source-node updater is limited to safe extraction plus signature and checksum verification, then delegates planning, rolling execution, convergence waits, resume, and rollback to the verified in-package updater.
- The console enforces the bootstrap contract at both upload and execution, so a previously stored `.cgupgrade` without a bootstrap cannot bypass the new check. Legacy `.cgpatch` remains a compatibility path only.
- If a durable job record exists but is unreadable, corrupt, or contains a mismatched package ID, the control API reports that the outcome requires verification and conservatively retains the maintenance warning instead of presenting the package as merely uploaded.
- Rolling progress is calculated from the current active-node inventory and remains at `100%` after every node completes.

## Upgrade Boundary

- The first upgrade from `2.2-42` to `2.2-43` is completed by the already hardened `2.2-42` updater, which includes retry, resume, and status-permission fixes.
- After `2.2-43` is installed, subsequent signed packages switch to their embedded updater before execution. The console identifies this as `bootstrap v1`.
- Automatic failover remains paused during update maintenance. Maintenance is released only after every node, database role, business ingress, and the Raft majority pass verification.

## Upgrade Path

For a deployment currently running `2.2-42`, upload:

```text
clusterguard-ha-2.2-42_to_2.2-43.x86_64.cgupgrade
```

Generate the read-only plan first, then execute the rolling update during a maintenance window. If the status says the outcome requires verification, do not start the update again; inspect the events and raw output and verify the live node versions first.
