# Update Action Identity Mismatch and Missing Hotfix Recovery Semantics (2026-09-30)

## Summary

At 13:30 on 2026-09-30 (UTC+8) the site asked to *resume* a hotfix that had **already been installed successfully**. The result: of the three controllers, **only the one that actually ran the job** (Leader `.153`) had its installation record rewritten as a failure; the other two still recorded success. The console reads the Leader's copy, so the UI showed 升级失败 for as long as that record stood, while all three nodes ran byte-identical payloads.

This is not "the hotfix idempotent retry was implemented wrong". Three defects stacked:

1. **The console submitted the action against the wrong update record** - the record that gated the button and the `patch_id` actually posted were not the same object;
2. **Recovery semantics were not routed by package kind** - "failed rolling upgrade to resume" was applied to a hotfix too, but a hotfix has no resumable checkpoint: its correct recovery action is to **re-run the same patch**;
3. **The server had no last line of defence** - even with the console fixed, `kind=hotfix + mode=resume` must be refused *before any file is written*, or an attempt that could only ever be refused becomes the patch's own recorded outcome.

All three are fixed in `HF-2026-0930-01`. The third is this retrospective's most important finding: **a refusal must not write a record**. If an attempt that only ever gets refused overwrites the execution that actually completed the installation, the operator can no longer answer "is this patch installed" from the console at all.

## Site Evidence

### The three controllers disagree

`/var/lib/clusterguard/updates/HF-2026-0929-05/status.json`:

| Node | `mode` / `status` | Time | File mtime |
| --- | --- | --- | --- |
| `.152` | `execute` / `succeeded` | finished `01:30:59Z` | 09-30 09:30:59 |
| `.153` (Leader) | **`resume` / `failed`** | `05:30:29Z` to `05:30:30Z` | 09-30 **13:30:30** |
| `.154` | `execute` / `succeeded` | finished `01:30:59Z` | 09-30 09:31:01 |

The `.153` record reads `升级任务失败或被阻断：热修补丁不支持 --resume：应用本身是幂等的，直接重新执行同一个补丁即可`.

### The event logs are identical on all three

All three `events.jsonl` files hold 13 lines, all three mtimes stopped at 09-30 09:31, and all three end with `succeeded` (`all node digests and maintenance release verified`). **The refused attempt contributed no events at all.**

### Two conclusions

- The overwrite is **not cluster-wide**: package storage and job directories are local to each controller, `POST` is not forwarded to the Leader, and the job runs on whichever machine the operator is connected to - so only the Leader's record was rewritten. **The state the console shows is the Leader's state, not the cluster's.** Answering "is this patch installed" requires reading the disk on all three; trusting the console alone is not enough.
- A single record can carry a successful event chain and a failed status simultaneously, because the two have different sources: the status is written by the job wrapper and can be overwritten by a later operation, while events are only appended by the attempt that actually ran.

## Three Defects and Their Fixes

| Layer | Defect | Fix |
| --- | --- | --- |
| 1. Console object binding | The button's enabled state came from "the newest actionable record" while its `patch_id` came from "the newest row overall" - not the same object. So the panel said it was resuming HF-04 while the POST went to HF-05's `/resume`. The rollback button carried the same defect and a worse one: it could revert a package the operator never selected | A single `softwareUpdateSubject` now feeds the rendered subject, the action subject, the confirmation subject and the posted `patch_id`; `packages[0]` is no longer an action target (`9fdb0e7`) |
| 2. Recovery-semantics routing | `scripts/clusterguard-update-job.sh` is the real operation orchestrator: it translates `mode` into updater arguments but **never looked at the package kind**, so a hotfix was handed `--resume` like any rolling upgrade - an entry point that can only ever fail for a hotfix | Route by `kind` from the already-snapshotted signed metadata `package.json`: `resume + upgrade to --resume`; `resume + hotfix to re-run the same patch`, saying so in the output (`8be2e3d`) |
| 3. Server-side backstop | `platformupdate.Manager.Start` had no precondition on `ModeResume`: it wrote the job file and started the helper, and the helper rewrote the launch failure as `failed`, making the refusal the patch's own record | Reject `mode == ModeResume && kind == hotfix` **before any file is written**, returning `ErrResumeUnsupported`, which the API maps to 409; the record survives untouched (`9fdb0e7`) |

Layer 3 refuses outright rather than having the server normalise a resume into a retry: `resume != retry`, and the API must not quietly change the meaning of the operator's action. The console offers 「重新执行」 for a failed hotfix instead of 「续跑升级」, so the log, the audit trail, the API and the operator's mental model all agree.

### Gate ownership: the same patch may re-enter, a foreign one must be refused

A failed hotfix may re-run the same patch, but it **must not adopt the maintenance gate another patch left behind**: a lock left by `HF-04` may not be taken over by `HF-05` just because both are hotfixes. The only criterion is `patch_id`, not `mode` - a hotfix writes the same `rolling_update` into `/etc/clusterguard/update-maintenance.json`, so **the marker's `mode` was never the hotfix/rolling discriminator**. The two adoption paths in the updater already separated lock ownership by package identity (a lock is yours when the held id matches, and a failed *rolling* upgrade's lock requires the held id to differ). What this change adds is naming the holder in the hotfix flow; it relaxes no admission rule.

## `HF-2026-0930-01` r1 Artifact

| Item | Value |
| --- | --- |
| Artifact | `clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch` |
| SHA-256 | `9a5af9bda61e81f87af686fd551ee9b90d04808d1f160df259cc84f4253b8fcd` |
| Size | 8,758,333 |
| `--inspect` | `signature=verified` / `kind=hotfix` / `rollback=available` / `database_mutation=false` |
| Payload | 2 binaries + 2 runtime scripts (`clusterguard-update-job.sh`, `clusterguard-upgrade`), byte-identical to the source tree |
| Restarted units | `clusterguard-ha.service`, `clusterguard-update-helper.service` |

This package **restarts the control plane**, so the console being briefly unavailable while it applies is **expected**, not a failed upgrade. It supersedes `revision 0` (`baf16ebc`): r0 stopped the console from acting on the wrong record but left the orchestrator's resume semantics alone, and neither build was ever delivered to a site. The supersession reason and the rules for that field are in the [Hotfix Patch Catalogue](../hotfix-patches.md).

## Site Rollout and Acceptance

### Rollout

Upload `HF-2026-0930-01` r1, execute it normally, wait for the two services to restart, sign in again or refresh the console, then run the read-only verification below in order.

**Before this package is applied, do not press 续跑 on any failed hotfix**: the site still runs the old console and the old orchestrator, and the action lands on the newest record in the list.

### Read-only verification (check the real state first, not the colour of the history rows)

```bash
set -a; . /etc/clusterguard/agent.env; set +a
# 1. Processes and services on all three controllers
systemctl is-active clusterguard-ha.service clusterguard-update-helper.service
# 2. Control plane: readiness, voters, unique leader, maintenance gate, active operations
curl -sk -H "Authorization: Bearer $CG_CONTROL_TOKEN" https://127.0.0.1:3000/api/v1/control-plane/status
# 3. The two runtime scripts must hash identically on all three nodes
sha256sum /usr/local/libexec/clusterguard-update-job.sh /usr/local/sbin/clusterguard-upgrade
```

Expected: `clusterguard-ha.service` and `clusterguard-update-helper.service` are `active` on all three; `ready=true`; `voters=3`; `unique leader=1`; `update_maintenance_active=false`; `active_operations=0`; the two scripts hash identically on all three nodes; this package's installation record is `succeeded`.

### Console regressions (the first thing after rollout, not another upgrade)

| # | Scenario | Expected |
| --- | --- | --- |
| A | A hotfix that succeeded | **No** 「续跑」, and no 「重新执行」 either |
| B | A hotfix that failed | Shows 「重新执行」 and **not** 「续跑」 |
| C | A rolling upgrade that failed | Shows 「续跑」 |
| D | With `HF-04` failed and `HF-05` newest and succeeded, act on the older record | The posted `patch_id` must be that record's own, not the newest row's |
| E | Controlled rollback of `HF-04` | Must act on `HF-04`, and **must not** fall back to the newest row |

D and E are the incident's core regressions. The server side can be checked independently: when an action is refused, that patch's `status.json` mtime and content must be **completely unchanged** - this is layer 3's acceptance point and can be exercised against any failed hotfix:

```bash
curl -sk -X POST -H "Authorization: Bearer $CG_CONTROL_TOKEN" -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' -d '{"confirmation":"<patch-id>"}' \
  https://127.0.0.1:3000/api/v1/platform/updates/<patch-id>/resume   # expect 409
stat -c '%y' /var/lib/clusterguard/updates/<patch-id>/status.json    # expect unchanged
```

## Not Closed

1. **`UPDATE-OPERATION-HISTORY-P0`: separate package installation state from operation execution history.** The update record is an **overwriteable status model**: one job slot per package, and a later operation overwrites the earlier result. That is why `HF-05` renders as "complete successful event chain / failed status / a result column holding only the resume refusal", where it should render as "package installed" plus "latest operation: resume refused". The requirement: once a package deployment result is complete it must not be rewritten by later operations, and every operation must **append** an operation record instead of overwriting the package's installation state. This removes the *space in which the damage can be recorded* - the three fixes above stopped the damage from happening; this closes the model that let it be written down.
2. **When `kind` cannot be determined, recovery semantics are still a guess.** The refusal in `platformupdate.Manager.Start` keys on `Kind == PackageKindHotfix`, but `Manager.Package()` deserialises the on-disk `package.json` directly and **the read path has no fallback**; the `--inspect` fallback is "no `kind` means rolling upgrade". A hotfix whose `package.json` lacks `kind` therefore has its resume admitted into the orchestrator (the fallback in `inspector.go` applies to the Inspect return path and cannot help here). This is currently unreachable on site: `HF-2026-0928-06`, `HF-2026-0929-04` and `HF-2026-0929-05` all carry `kind=hotfix`, and the only package without `kind` is the rolling package `cgupgrade-2.2-103-to-2.2-104-x86_64` uploaded during the 2.2-103 era, which should indeed be treated as a rolling upgrade. To make this a structural guarantee rather than a coincidence, the **upload path** must refuse a package whose `kind` cannot be determined - the two recovery semantics are opposite, so an undecidable package cannot be recovered safely - after which the read-side decision becomes a wall.
