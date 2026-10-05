# Upgrade and Hotfix v2: Implementation and Acceptance Status

> **Document role: historical implementation and acceptance snapshot.** Results below apply to their recorded date and repair baseline `7b643f4`; they are not current site status or the gate definition. Use the [gate workflow](../zh-CN/validation-gate-workflow.md), current bindings, the applicable strict stage, and actual acceptance reports for this run. Historical results do not close unexecuted checks.

> Gate workflow update (2026-10-04): use `--stage source --strict` for source development. Artifact and field acceptance require `--stage artifact` or `--stage field`, `--strict`, and `--acceptance-report FILE` with actual evidence. The default remains field; real ART/FIELD evidence is still unavailable. See the [workflow](../zh-CN/validation-gate-workflow.md).

Updated 2026-10-03. Implementation commit: `7b643f42ed11901c5d73f7703f3e3f176d053bb4`, pushed to the sole local and remote mainline `codex/2.2-postgresql`. The mandatory [v2 contract](../upgrade-validation-chain.md), its Chinese copy, and the embedded copy remain identical to the supplied original. This status page does not change the contract.

## Implemented and Locally Verified

| Scope | Implementation and evidence |
| --- | --- |
| Strict contract loading | `internal/updatecontract`, Manager, Helper, Runner, and four build entry points reject missing, unknown, duplicate, incorrectly nested, weakened, or unsupported values |
| Recovery modes | Failed hotfixes use `retry`; hotfix `resume` is rejected; failed rolling updates retain `resume`; actual browser requests bind the confirmed package ID |
| Deployment and operation history | `deployment.json` is independent of the latest job; `requests.jsonl` and `operations.jsonl` preserve append-only attempts; failed plans and Helper launch failures preserve verified installation |
| Operation identity and uncertain responses | Explicit `operation_id` persists before launch; same-ID/same-action requests remain idempotent across restart; uncertain responses trigger durable result queries without automatic resubmission |
| Maintenance ownership | Runner and Raft Store reject foreign, partial-foreign, and mixed lock holders; same-package execution uses CAS |
| Signature and supersedes | Actions re-inspect signed packages and stored digests; only signed supersedes from verified installed successors establish replacement; signed fixtures cover installed, unrelated, rolled-back, and tampered successors |
| Rollback | The actual generated script is checked for atomic replacement; moving the command into a comment fails the gate; package ownership and missing-backup protections remain enforced |
| Leader history | Matching operation terminal history can be imported; different identities or conflicting terminal results block actions while retaining evidence |
| Console safety | Hotfix recovery, update confirmation, node safety, bootstrap, and engine-page suites passed in local Chrome using isolated API fixtures |

Default status reads omit complete operation history. Request it explicitly through `GET /api/v1/platform/updates/<patch_id>?history=1`. Corrupt history blocks new actions. A newer successful package does not automatically resolve a previous failed package by timestamp or version.

Runner requires an executable, non-symlink Helper supporting `contract`, normally `/usr/local/libexec/clusterguard-update-helper`; controlled environments may set `CG_UPDATE_CONTRACT_HELPER`. Deploy matching Manager, Helper, and Runner. Build entry points require Node and support `CG_NODE_BIN`; their `--contract-only` check is not complete release acceptance.

## Verified Source Results

The [repair record](../zh-CN/upgrade-hotfix-v2-repair-2026-10-03.md) records actual execution: full Go tests with package parallelism limited to two passed; focused platformupdate/API and Store race tests passed; focused vet passed. Hotfix browser recovery passed; update confirmation passed 49 checks; the three mandatory console audits passed. These are source and isolated-browser results, not native database or site acceptance.

The source gate reported **17 PASS / 1 OPEN / 0 failed**. `--strict` exited 1 because the OPEN obligation remained. Fourteen mutation cases were caught, with unchanged and comment-only controls passing. License consistency reported 68 checks passed. Rechecks during documentation synchronization are recorded in the [documentation audit](../zh-CN/documentation-sync-2026-10-03.md).

## OPEN: New Artifact and Site Acceptance

**Card: UPDATE-V2-FIELD-ACCEPTANCE**

- ART-001..013: no new delivery package was built; complete new-package signature, payload, source consistency, permissions, ownership, and restart-unit evidence is unavailable.
- FIELD-001..012: the production baseline was not rechecked, and no new-package upload, signature inspection, rolling update, hotfix, rollback, or cross-Leader acceptance was performed. Final three-node health, durable deployment history, and actual site UI agreement remain unverified.
- Different private and replicated operation histories are rejected while preserving both records; there is no automatic timestamp-based reconciliation.

`verify-upgrade-validation-chain.cjs --strict` must remain nonzero until these obligations are satisfied. No new package, signature, release tag, installation medium, or production installation was produced by this repair.

## Existing Package Documentation

The [hotfix catalogue](../hotfix-patches.md) is generated from existing signed artifacts. Its descriptions of resume-to-execute conversion and plan→execute for older packages are historical artifact behavior. Those artifacts were not rebuilt to include v2 source fixes. Use the [current update guide](update-and-patch.md) for current source behavior, and validate the installed delivery before site operations.
