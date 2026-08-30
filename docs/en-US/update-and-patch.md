# ClusterGuard HA Version Update and Rollback Guide

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/update-and-patch.md)
<!-- /LANGUAGE-SWITCH -->

This guide defines how a customer site updates the ClusterGuard HA control plane. Control-plane software updates are strictly separate from database upgrades. The updater does not invoke MySQL, PostgreSQL, Oracle, or SQL Server clients and does not stop databases or modify database software, data directories, replication, or VIP configuration.

> **Artifact naming:** `.cgupgrade` is a complete signed rolling-update package, not a small binary delta. It contains both the target RPM and the current-version RPM for automatic rollback. `*-offline-linux-*.tar.gz` is intended for installation or reinstallation and cannot be uploaded directly to the rolling-update page. Legacy `.cgpatch` packages remain supported.

> **Version baseline:** `2.2.39` is the first formal managed-update baseline. Standard `2.2-38` does not include the managed-update framework, and the laboratory `2.2-38.field3` RPM record does not match its binary contract. Neither can enter `2.2.39` by uploading a generic update package. Migrate each node to the formal `2.2.39` RPM with the historical-version bridge procedure first; later formal versions can use a `.cgupgrade` whose source version is `2.2.39`.

> **Important: automatic failover is unavailable while a system update is in progress. Monitor the platform and database service throughout the maintenance window.**
>
> Once the maintenance marker is established, planned switchovers, failovers, automatic failover, and node mutations are blocked by Safety Guard. Read-only topology, health, metrics, and operation logs remain available. Automatic failover resumes only after every node passes update verification and the maintenance marker is released. An interrupted or failed update keeps the marker in place and remains fail-closed until the same update package is resumed or rolled back.

## Console-Based Update

An administrator can complete the workflow under **Settings → Version Update** without assembling shell commands:

1. Upload an officially released, signed `.cgupgrade`. The server validates format, SHA-256, release signature, architecture, source and target versions, and state protocol before accepting it.
2. Select **Generate Update Plan** and review the update package ID, source, target, rolling order, rollback availability, and node inventory.
3. Select **Execute Rolling Update**. The confirmation dialog repeats the automatic-failover warning and requires the complete update package ID. The runner first distributes the same signed package and metadata to every controller, verifies SHA-256 remotely, and publishes each copy atomically before creating maintenance gates.
4. A persistent banner remains visible for the entire maintenance transaction while node events, status, and raw output update in place.
5. After success, confirm the banner is gone and recheck topology, VIP, and replication. On failure, use **Resume Update** or **Controlled Rollback**; never delete maintenance markers manually.

Upload and orchestration endpoints require an authenticated administrator and enforce CSRF, Raft Leader forwarding, signature trust, a constrained root helper, and audit recording. An update package ID cannot be overwritten with different content.

Provide the release public key during initial installation. A formal offline kit may carry the public key; the private key must never be shipped to customer systems:

```bash
./install_clusterguard.sh \
  ... \
  --patch-trust-key ./trust/patch-signing-public.pem \
  --execute
```

The installer configures `/etc/clusterguard/update.json`, the trusted public key, and `clusterguard-update-helper.service` on every controller. Without a trusted key, the console reports Version Update as unavailable and never falls back to unsigned installation.

## 1. Update Architecture

Every release RPM embeds an immutable runtime contract containing product, version, release, Git commit, build time, platform, RPM architecture, metadata `state_format`, and `update_protocol`.

```bash
clusterguard --version-json
cgctl version --json
curl --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/api/v1/platform/version
```

A `.cgupgrade` contains the target RPM, rollback RPM, embedded bootstrap updater, compatibility manifest, SHA-256 checksums, and release signature. Sites retain only the release public key. The private signing key must remain offline and must never be shipped to customer systems. The legacy `.cgpatch` suffix is accepted only for compatibility.

```text
clusterguard-patch/
├── PATCH-MANIFEST.json
├── PATCH-MANIFEST.sig
├── SHA256SUMS
├── bootstrap/
│   └── clusterguard-upgrade.sh
└── payload/
    ├── clusterguard-ha-old.rpm
    └── clusterguard-ha-new.rpm
```

The updater already installed on the source node is limited to safe extraction, release-signature verification, and bootstrap SHA-256 verification. Planning, rolling execution, convergence waits, resume, and rollback are delegated to the verified updater embedded in the package. Orchestration fixes therefore take effect before the target RPM is installed instead of inheriting stale source-version behavior. A modified, missing, or incompatible bootstrap is rejected before maintenance gates are created.

Seven independent contracts protect a site update:

1. **Version contract**: the RPM, running binary, and update-package manifest must agree on version, architecture, `state_format`, and `update_protocol`.
2. **Node identity contract**: the deployment inventory, immutable UUID in `/etc/clusterguard/node.json`, live Raft voters, and active data-node inventory must match exactly. Hostname, IP, and membership changes are never inferred optimistically.
3. **Maintenance transaction contract**: every controller receives a durable marker for the same `patch_id` before rolling work starts. Markers are released only after end-to-end verification; a partial release triggers compensating re-lock on every controller.
4. **Bootstrap execution contract**: every new `.cgupgrade` must contain a manifest-declared bootstrap updater covered by the release signature. The console rejects a new package without a verified bootstrap; `.cgpatch` remains a legacy compatibility path only.
5. **Package residency contract**: before a console execute, resume, or rollback starts, `package.cgpatch` and `package.json` must exist in the same protected update directory on every controller. Each remote SHA-256 must match the locally verified artifact before an atomic rename publishes it. Any distribution or digest failure stops before maintenance gates and RPM mutation; already published identical read-only copies are safe to reuse.
6. **Job evidence contract**: the update directory provides restricted cooperative write access to the root Helper and the `clusterguard` console, while status, output, and events remain group-readable. A planned, succeeded, failed, or rolled-back terminal state written by the runner must not be replaced by a generic process-exit error.
7. **Mutation admission contract**: execution gates the current Leader first and followers next, then waits for existing work to drain. Release removes follower gates first and the current Leader gate last, preventing automatic recovery from taking the half-complete gate acquisition or release window.

The initial installer registers immutable controller, data, and mixed-node identities before database discovery. Later expansion, retirement, or replacement must update the resource inventory through the node lifecycle workflow; the updater will not silently omit an active node.

## 2. Supported Boundaries

| Change | Method |
| --- | --- |
| Version update such as `2.2-28` to `2.2-29` | Signed `.cgupgrade` rolling update |
| Feature-line update with unchanged state contract | Signed update package after compatibility qualification |
| Incompatible `state_format` or `update_protocol` | Rejected; use a dedicated migration release |
| Database engine upgrade | Separate database upgrade workflow |
| No controller quorum, active operation, or lifecycle task | Update is blocked |

Update protocol v1 does not perform destructive metadata downgrade. A future state-format change must use an expand/read, switch/write, and contract/cleanup migration sequence rather than a regular update package.

## 3. Build a Signed Update Package

Build old and new RPMs from clean, qualified commits, then sign the update package with the offline release key:

```bash
scripts/build-clusterguard-patch.sh \
  --from-rpm dist/clusterguard-ha-2.2-28.x86_64.rpm \
  --to-rpm dist/clusterguard-ha-2.2-29.x86_64.rpm \
  --signing-key /secure/offline/clusterguard-patch-signing.key \
  --output dist/clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade
```

Ship the update package, its SHA-256 file, release notes, and a signing-key fingerprint through an independent channel. Never overwrite an existing update artifact.

## 4. Site Preparation

1. Install the trusted public key:

   ```bash
   install -d -m 0750 -o root -g clusterguard /etc/clusterguard/trust
   install -m 0640 -o root -g clusterguard clusterguard-patch-signing-public.pem \
     /etc/clusterguard/trust/patch-signing-public.pem
   ```

2. Keep the generated `clusterguard-deployment-state.json`. After expansion, retirement, or replacement, use the current inventory or pass complete `--controllers` and `--data-nodes` lists explicitly. Explicit lists take precedence over a stale state file.
3. Verify all controllers are online, the Raft voter set is odd and at least three, and exactly one Leader exists.
4. Verify there are no running or indeterminate database operations and no node add, rebuild, or synchronization tasks.
5. Back up `/etc/clusterguard/`, `/var/lib/clusterguard/`, and the deployment state.
6. Use reviewed SSH host keys and preferably a dedicated SSH key.

## 5. Four-Step Update

### Inspect

This step does not contact remote nodes:

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --inspect
```

Require `signature=verified`, the expected source and target, `rollback=available`, `database_mutation=false`, `bootstrap=available`, and `bootstrap_protocol=1`.

During plan or execution, the source updater logs that the signed bootstrap was verified and execution is being handed to the package updater. Do not proceed with a production change if this handoff record is absent.

### Plan

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /root/.ssh/clusterguard_update \
  --known-hosts /etc/clusterguard/ssh_known_hosts \
  --plan
```

The fixed order is controller followers, data-only Agent nodes, then the current Leader. Every node must run either the declared source or target and pass the embedded binary contract check. The updater reads each data node's immutable UUID, compares controller UUIDs with live Raft voters, and compares data-node UUIDs with the live active inventory. Divergent controller views, duplicate UUIDs, role mismatches, or stale membership are rejected before maintenance locks are created, preventing missed nodes, cross-cluster targeting, and partial gating.

### Execute

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /root/.ssh/clusterguard_update \
  --known-hosts /etc/clusterguard/ssh_known_hosts \
  --execute
```

Before every node, the updater rechecks quorum, Leader identity, readiness, active operations, and lifecycle tasks. Before writing any maintenance marker, a console update distributes the complete signed package and metadata to all controllers and re-verifies SHA-256 remotely. Copy interruption, insufficient disk space, permission failure, or digest mismatch therefore cannot enter maintenance or install an RPM. If the Raft Leader changes during the update, the new Leader can resume or roll back from the same protected local artifact.

The updater then writes `/etc/clusterguard/update-maintenance.json` on every controller:

- mutating UI and API requests return `423 Locked`;
- automatic failover is blocked by the same Safety Guard;
- node add, rebuild, and synchronization are blocked;
- topology, health, metrics, logs, and control status remain readable;
- only the ClusterGuard service on the current node is restarted; databases are not restarted.

Node readiness is more than an RPM version check. A controller must rejoin and observe a stable Leader. A data node must restore both `clusterguard-agent.service` and `clusterguard-agent-reconcile.timer`. A mixed node must satisfy both sets of checks; failure stops forward progress and enters rollback.

### Verify

```bash
cgctl version --json
cgctl status --json
systemctl --no-pager --full status clusterguard-ha
test ! -e /etc/clusterguard/update-maintenance.json
```

The execution directory receives a current-state JSON and an append-only event stream:

```text
clusterguard-update-PATCH-ID.json
clusterguard-update-PATCH-ID.events.jsonl
```

Archive them with the patch, release notes, and change ticket.

## 6. Resume and Rollback

An ordinary error, termination signal, or node failure stops forward progress and rolls nodes updated by this run back in reverse order with the embedded old RPM. The failed node is included in the rollback set.

If the updater host loses power or the process is killed, the maintenance marker intentionally remains and keeps the platform fail-closed. Resume with the same signed update package:

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /root/.ssh/clusterguard_update \
  --known-hosts /etc/clusterguard/ssh_known_hosts \
  --resume --execute
```

`--resume` accepts only the same `patch_id` stored in the lock. A console update has already copied the package to every controller before creating that lock, so a new Leader resumes from its local artifact after leadership changes; a package or metadata digest mismatch is still rejected. Do not manually remove maintenance markers or locks. On normal completion, the updater preflights every controller marker before releasing any of them. If a release fails partway through, it writes the same lock back to every controller and remains fail-closed until connectivity is repaired and the run is resumed.

To deliberately return to the old RPM:

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /root/.ssh/clusterguard_update \
  --known-hosts /etc/clusterguard/ssh_known_hosts \
  --rollback --execute
```

## 7. First Adoption

Versions predating the managed update protocol do not expose `--version-json` or the unified maintenance gate and cannot consume `.cgupgrade` directly. Use the bridge maintenance procedure from that release, disable automatic mutation ingress, install the bridge RPM one controller at a time, and verify on every node:

```bash
clusterguard --version-json
cgctl status --json | jq '{maintenance:.result.update_maintenance_active, controllers:.result.controller_members, data_nodes:.result.data_node_members}'
```

Managed patching starts only after every node has the same `state_format` and `update_protocol`. The updater fails closed when it encounters an older binary; it never silently drops version verification.

## 8. Production Admission Checklist

- Patch is from an official release; signature and SHA-256 pass.
- Public-key fingerprint is verified out of band.
- Source, target, architecture, state format, and protocol match.
- At least three odd-numbered controllers have stable quorum.
- Immutable node UUIDs, live Raft voters, active data nodes, and update targets match exactly.
- No running or indeterminate operation and no lifecycle task exists.
- Control service, Agent, and reconcile timer are healthy on every mixed node.
- Update, rollback, resume, and Leader-last order passed in a production-like staging environment.
- Recovery procedures exist for updater-host loss, node reboot, low disk space, and network interruption.
- After update, sample topology, VIP uniqueness, automatic failover, and former-primary recovery again.
