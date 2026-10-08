# Hotfix Version and Filename Rules

> This page defines the version fields, filename, and delivery directory for new signed `.cgpatch` packages. Read the [gate workflow](../zh-CN/validation-gate-workflow.md) first; for build, signing, upload, retry, or rollback, also read the complete [v2 contract](../zh-CN/upgrade-validation-chain.md). This page does not replace signature, SHA-256, PRE/ART/FIELD, or site acceptance gates.

## 1. The new sealed release line

All earlier 2.x packages and HF-named files are legacy history. Keep their bytes and digests unchanged; do not rename them for cosmetic consistency. The new sealed release line starts at **`3.1.1.1`** and uses four numeric segments:

```text
clusterguard-MAJOR.MINOR.PATCH.BUGFIX.<arch>.cgpatch
```

Example of a new specification:

```json
{
  "id": "HF-2026-1008-02",
  "patch_version": "3.1.1.1",
  "rpm_version": "2.2",
  "rpm_release": "105"
}
```

The builder emits:

```text
clusterguard-3.1.1.1.x86_64.cgpatch
```

`patch_version` is signed into `HOTFIX-MANIFEST.json`. The filename and manifest must describe the same version; changing only the filename is not a valid correction.

## 2. What the four segments mean

| Segment | Current starting value | Meaning |
| --- | --- | --- |
| `MAJOR` | `3` | New production-stable major line |
| `MINOR` | `1` | Current capability stage |
| `PATCH` | `1` | Current sealed feature release |
| `BUGFIX` | `1` | First bug revision of that release |

These four segments are the **delivery version identity**. They do not have to equal the source RPM's `rpm_version` and `rpm_release`. A package is still built for the installed baseline — for example `2.2-105` — so the signed manifest keeps `source.version=2.2` and `source.release=105`; those describe the baseline the package can be applied to, while `patch_version=3.1.1.1` identifies the new sealed identity.

The bug-fix segment starts at `1`. If signed bytes need correction, use a new identity such as `3.1.1.2` and record `revision` plus `supersedes_artifact{file,sha256,reason}` in the signed manifest. Never overwrite `3.1.1.1`.

## 3. The `HF-...` identity and the versioned filename

`HF-YYYY-MMDD-NN` remains in the specification and the signed manifest as `hotfix_id`, used by the console, audit, retry, and maintenance-gate ownership, but it is no longer part of the new filename:

| Location | Example | Purpose |
| --- | --- | --- |
| Outer filename | `clusterguard-3.1.1.1.x86_64.cgpatch` | Delivery version identity |
| `HOTFIX-MANIFEST.json` | `hotfix_id=HF-2026-1008-02` | Runtime operation, audit, and same-package retry |
| `HOTFIX-MANIFEST.json` | `patch_version=3.1.1.1` | Binds the filename to the signature |

The confirmation dialog still asks for the package id `HF-2026-1008-02`. Before upload, verify that the filename, the signed manifest, and the `.sha256` sidecar describe one artifact.

The first migration from a legacy name to the sealed release line must allocate a new `hotfix_id`: for example, migrate `HF-2026-1008-01` to `HF-2026-1008-02` and record the relationship in signed `supersedes`. The console identifies packages by `hotfix_id` and digest. Reusing the old ID with different bytes triggers a package conflict even when the versioned filename is correct.

## 4. Retry, correction, and immutability

- A failed application whose installed result is not yet confirmed retries the exact same file, `hotfix_id`, and `patch_version`; a retry does not create a new file.
- A signed file is never rebuilt in place, overwritten, or made into an apparently new version by renaming it only.
- A correction increments the bug-fix segment, for example from `3.1.1.1` to `3.1.1.2`, and keeps the replaced file and digest.
- Earlier 2.x/HF filenames are frozen history. They serve as evidence only and cannot be republished as a new current entry.

## 5. Checks before upload

```text
Filename:         clusterguard-3.1.1.1.x86_64.cgpatch
Manifest source:  source.version=2.2, source.release=105
Manifest version: patch_version=3.1.1.1
Manifest target:  target.version=2.2, target.release=105+hf-2026-1008-02
```

Check in this order:

1. The source baseline matches the version actually running at the site;
2. `patch_version` has four numeric segments and a bug-fix segment greater than `0`;
3. The filename architecture equals the manifest's `target.rpm_architecture`;
4. `hotfix_id`, `patch_version`, `revision`, `source`, `target`, and the affected files all come from one signed manifest;
5. The `.sha256` matches the file's actual bytes, and the private ledger marks this identity as the current entry.

Do not select a package by directory time, sort order, or a `latest` label. A correct filename does not prove signature, compatibility, or site acceptance.

## 6. Directory and `.cgupgrade` boundary

The delivery directory stays keyed by the **source baseline**, not by the four-segment bug-fix identity:

```text
release/<source-version>-<source-release>-hotfixes/
├── clusterguard-3.1.1.1.x86_64.cgpatch
├── clusterguard-3.1.1.1.x86_64.cgpatch.sha256
└── <version>-update-notes.md
```

A package's directory remains `release/2.2-105-hotfixes/` as long as its applicable baseline is `2.2-105`. Signed `.cgpatch` files, `.sha256` sidecars, and the private ledger stay in the local or contracted delivery directory; they are not uploaded to GitHub.

`.cgupgrade` keeps its own rolling-upgrade grammar:

```text
clusterguard-ha-<source-version>-<source-release>-to-<target-version>-<target-release>-<arch>.cgupgrade
```

Do not apply the `.cgupgrade` source-to-target grammar to a hotfix package, and do not build `HF-...` or `+hf-...` into a new hotfix filename.

## 7. Forbidden names

A new specification must not use these names:

```text
clusterguard-ha-hotfix-HF-2026-1008-02-2.2-105.x86_64.cgpatch  # a new spec still using the old HF grammar
clusterguard-3.1.1.x86_64.cgpatch                              # only three segments
clusterguard-3.1.1.0.x86_64.cgpatch                            # bug-fix segment starts at 0
latest.cgpatch
hotfix.cgpatch
```

If the filename and the manifest disagree, one version has more than one digest, the supersede relationship is missing, or the current ledger entry cannot be confirmed, stop the upload and keep the original files and logs. Renaming is never a way forward.
