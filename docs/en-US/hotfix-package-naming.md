# Hotfix Version, Package ID and Delivery Directory

Read the [gate workflow](../zh-CN/validation-gate-workflow.md), then the full [v2 contract](../zh-CN/upgrade-validation-chain.md) before building, signing, uploading or recovering. This page does not replace SOURCE/ART/FIELD gates.

## 1. Sealed version line

The product line starts at **3.1.1.1**. New packages bind one four-part identity to the filename, signed manifest, running binary and package ID.

```json
{"id":"3.1.1.4","patch_version":"3.1.1.4","rpm_version":"2.2","rpm_release":"105"}
```

File: `clusterguard-3.1.1.4.x86_64.cgpatch`. The builder injects buildinfo.ProductVersion; CLI/API product_version expose the running identity.

## 2. Four segments and RPM admission

MAJOR=stable product line, MINOR=capability stage, PATCH=sealed feature release, BUGFIX=bug correction starting at1. These identify the running product and delivery. RPM version/release and signed source remain admission fields, possibly2.2/105; never replace them with display metadata or derive the running version from uploaded/history packages.

Original3.1.1.1/3.1.1.2 omitted runtime binding and remain immutable.3.1.1.3 corrected runtime projection;3.1.1.4 corrects product package IDs and retired failures occupying the current summary.

## 3. Package ID equals product version

New spec id, signed hotfix_id, API patch_id, UI, typed confirmation, POST, Runner, lock and history all use **3.1.1.4**. The hotfix_id field name stays compatible; its value is the product version. A UI label must not disagree with the actual request identity.

Historical delivered HF IDs, signatures and operation logs retain their original values. Migration uses a new version and signed supersedes links to historical IDs. Different bytes under one version remain a conflict.

## 4. Retry and immutability

Retry uses the same bytes/ID/version. Signed or delivered artifacts cannot be rebuilt in place. A correction advances BUGFIX, allocates the same new version as ID, records revision and supersedes_artifact with the original file/SHA, and retains historical bytes and failed attempts. Directory relocation changes no signed bytes; record old/new paths and identical SHA in the ledger and update backlinks, leaving signed historical replacement paths unchanged.

## 5. Admission and history checks

Check actual RPM source, architecture, ID=patch_version, four numeric segments with BUGFIX>0, signature, sidecar and current private ledger. CLI/API runtime identity must match the signed product version. Targets/history use each package's own verified patch_version; missing legacy metadata may be enriched only from its hash- and identity-verified signed original. Only an installed, verified successor's signed supersedes declaration retires an old failure; historical results remain visible, never guessed from time/version ordering.

## 6. Directory and channel

```text
release/3.1.1.4/
├── clusterguard-3.1.1.4.x86_64.cgpatch
├── clusterguard-3.1.1.4.x86_64.cgpatch.sha256
├── 3.1.1.4.spec.json
├── 3.1.1.4-更新说明.md
└── 3.1.1.4-evidence/
```

New directories are release/<four-part-product-version>/. Legacy *-hotfixes directories retain history. Scan both layouts and allow at most one current artifact per RPM admission line across directories. Signed packages, sidecars and private ledgers remain local/contracted delivery files, never GitHub assets. Full cgupgrade naming remains independent.

## 7. Forbidden cases

No latest.cgpatch, hotfix.cgpatch, three-part versions, BUGFIX=0, mismatched new IDs/versions, changed bytes under one identity, or directory-order guesses. Signature/hash/identity/replacement-chain disagreement blocks the action and requires retained evidence.
