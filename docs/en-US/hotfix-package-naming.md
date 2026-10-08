# Hotfix Version and Filename Rules

> This page defines the version fields, filename, and delivery directory for new signed `.cgpatch` packages. Read the [gate workflow](../zh-CN/validation-gate-workflow.md) first; for build, signing, upload, retry, or rollback, also read the complete [v2 contract](../zh-CN/upgrade-validation-chain.md). This page does not replace signature, SHA-256, PRE/ART/FIELD, or site acceptance gates.

## Canonical filename

New hotfix specifications must set `patch_version`. The builder emits:

```text
clusterguard-MAJOR.CAPABILITY.INTERNAL.BUGFIX.<arch>.cgpatch
```

Example specification and output:

```json
{
  "id": "HF-2026-1008-01",
  "patch_version": "2.2.105.1",
  "rpm_version": "2.2",
  "rpm_release": "105"
}
```

```text
clusterguard-2.2.105.1.x86_64.cgpatch
```

`patch_version` is also signed into `HOTFIX-MANIFEST.json`. The filename and manifest must describe the same artifact.

## Four version segments

| Segment | Example | Meaning | Constraint |
| --- | --- | --- | --- |
| `MAJOR` | `2` | Production-stable major line | Production stable version |
| `CAPABILITY` | `2.2` | Capability line: MySQL expanded to PostgreSQL, Oracle, and SQL Server | Must match `rpm_version` `2.2` |
| `INTERNAL` | `105` | Internal feature release on that capability line | Must match `rpm_release` `105` |
| `BUGFIX` | `1` | Bug-fix sequence for `2.2.105` | Starts at `1`, never `0` |

Thus `2.2.105.1` is the hotfix version, while the installed RPM baseline remains `2.2-105`. The target, affected files, restart units, and operation identity still come from the signed manifest.

## Hotfix ID versus filename version

`HF-YYYY-MMDD-NN` remains the manifest `hotfix_id` used by the console, audit, retry, and maintenance-gate ownership. The new filename deliberately omits it:

| Location | Example | Purpose |
| --- | --- | --- |
| Outer filename | `clusterguard-2.2.105.1.x86_64.cgpatch` | Human-readable product and Bug-fix version |
| Signed manifest | `hotfix_id=HF-2026-1008-01` | Runtime operation and audit identity |
| Signed manifest | `patch_version=2.2.105.1` | Binds the filename version to the signature |

The confirmation dialog still asks for `HF-2026-1008-01`; verify the filename, manifest, and sidecar digest together.

## Retry, correction, and immutability

- A failed, not-yet-confirmed application retries the exact same file, ID, and `patch_version`. Retry does not create a new file.
- A signed file is never rebuilt or overwritten in place.
- The new filename does not append `-r1`. If signed bytes need correction, use the next Bug-fix version, such as `2.2.105.2`, and record `revision` plus `supersedes_artifact{file,sha256,reason}` in the new signed manifest.
- Keep the old file and digest as evidence; `.2` is a new traceable Bug-fix identity.

Historical specifications without `patch_version` retain their immutable `clusterguard-ha-hotfix-HF-...` names. They are frozen history, not candidates for cosmetic renaming. New specifications use this versioned form.

## Baseline, directory, and rolling-package boundary

For `clusterguard-2.2.105.1.x86_64.cgpatch`, verify `source.version=2.2`, `source.release=105`, and `patch_version=2.2.105.1` in the signed manifest. Store it under `release/2.2-105-hotfixes/` beside its same-basename `.sha256` and delivery note. Signed packages, hashes, and the private ledger stay in the local or contracted delivery directory.

`.cgupgrade` keeps its own rolling grammar:

```text
clusterguard-ha-<source-version>-<source-release>-to-<target-version>-<target-release>-<arch>.cgupgrade
```

Do not mix the two grammars or select a package by filesystem time or a label such as `latest`.
