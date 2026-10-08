# Hotfix Package Naming and Delivery Directory

> This page defines the filename, directory, and identification rules for signed `.cgpatch` hotfixes. Read the [gate workflow](../zh-CN/validation-gate-workflow.md) first; for build, signing, upload, retry, or rollback, also read the complete [v2 contract](../zh-CN/upgrade-validation-chain.md). This page does not replace signature, SHA-256, PRE/ART/FIELD, or site acceptance gates.

## Canonical filename

The `scripts/build-hotfix-patch.sh` builder emits this fixed form:

```text
clusterguard-ha-hotfix-HF-YYYY-MMDD-NN[-rREV]-<source-version>-<source-release>.<arch>.cgpatch
```

| Segment | Meaning | Example |
| --- | --- | --- |
| `clusterguard-ha` | Fixed product name | `clusterguard-ha` |
| `hotfix` | Fixed artifact kind; the archive root is `clusterguard-hotfix/` | `hotfix` |
| `HF-YYYY-MMDD-NN` | Immutable hotfix ID: year, month/day, and two-digit daily sequence | `HF-2026-1008-01` |
| `-rREV` | Artifact revision, only for a correction; `r0` is omitted | `-r1` |
| `<source-version>-<source-release>` | Installed RPM baseline, never the target | `2.2-105` |
| `<arch>` | RPM architecture | `x86_64`, `aarch64` |
| `.cgpatch` | Fixed hotfix suffix | `.cgpatch` |

The current package is therefore:

```text
clusterguard-ha-hotfix-HF-2026-1008-01-2.2-105.x86_64.cgpatch
```

It is hotfix `HF-2026-1008-01` for source baseline `2.2-105` on `x86_64`. The target comes from the signed manifest (`2.2-105+hf-2026-1008-01`); it is not encoded as the filename baseline and does not change the RPM release.

## Revision and retry rules

- Revision `0` is omitted from the filename. A correction uses `-r1`, `-r2`, and so on, while retaining the old file.
- A revised artifact must record the replaced filename, its SHA-256, and the reason in `supersedes_artifact` and must pass the applicable gates again.
- The practical artifact identity is `(hotfix_id, revision, SHA-256)`. A matching `hotfix_id` never authorizes overwriting bytes.
- A failed, not-yet-confirmed application retries the exact same file, ID, and revision. Retry does not create a new filename.

## Baseline, directory, and selection

The source baseline in the filename must match `source.version` and `source.release` in `HOTFIX-MANIFEST.json` and the running site. The manifest target may be `2.2-105+hf-2026-1008-01`; that target is not substituted into the filename.

Store the artifact under `release/<source-version>-<source-release>-hotfixes/`, for example `release/2.2-105-hotfixes/`, beside its same-basename `.sha256` file and delivery note. Signed packages, hashes, and the private ledger stay in the local or contracted delivery directory and are not published to GitHub.

Select the package from the private ledger's current artifact entry. Do not select by filesystem time, directory order, or a label such as `latest`. Verify the filename, manifest, sidecar digest, and status together.

## Boundary with `.cgupgrade`

`.cgupgrade` has its own builder and grammar: `clusterguard-ha-<source-version>-<source-release>-to-<target-version>-<target-release>-<arch>.cgupgrade`. Do not reuse the hotfix ID grammar for a rolling package, or put the `+hf-...` target into a hotfix filename.
