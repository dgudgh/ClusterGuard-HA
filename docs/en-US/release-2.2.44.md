# ClusterGuard HA 2.2.44 Release Notes

> **Language:** English | [简体中文](../zh-CN/release-2.2.44.md)

Release date: 2026-08-28
Version: `2.2-44`

## Overview

`v2.2.44` completes cross-version metadata migration for the signed in-package updater. When an older release uploaded a package containing a valid bootstrap but could not record the new fields in `package.json`, the new control plane re-verifies and presents the contract correctly instead of treating it as a legacy package or blocking resume because the root-owned history directory cannot be rewritten.

This release updates only ClusterGuard controllers, agents, and update tooling. It does not modify database software, data directories, replication, or business ingress.

## Changes

- Re-verifies the release signature, RPM digests, and embedded bootstrap digest for `.cgupgrade` packages whose older metadata lacks the contract fields.
- Caches the verified contract in the control process. It persists the fields when directory ownership permits, retains root ownership when it does not, and re-verifies once after a control-plane restart.
- Upload, plan, execute, resume, and rollback share the same bootstrap contract, so stored legacy metadata cannot bypass enforcement.
- Completed jobs remain `succeeded` at `100%` with automatic failover available instead of falling back to an uploaded state.

## Upgrade Paths

For a deployment currently running `2.2-42`, upload:

```text
clusterguard-ha-2.2-42_to_2.2-44.x86_64.cgupgrade
```

For a deployment currently running `2.2-43`, upload:

```text
clusterguard-ha-2.2-43_to_2.2-44.x86_64.cgupgrade
```

Generate the read-only plan first, then execute the rolling update during a maintenance window. Automatic failover is paused during maintenance and is restored only after every node and the Raft majority pass verification.
