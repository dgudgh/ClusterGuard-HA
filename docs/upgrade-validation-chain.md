# Upgrade and Hotfix Validation Chain

> **Level: P0 — mandatory, must not be skipped.**
>
> This is not an explanatory document. It is the single mandatory validation contract for
> ClusterGuard upgrades, hotfixes, rollback, resume, re-execution, building, signing,
> releasing and field acceptance.
>
> **Every program, script, AI agent, CI job, release tool, console, API, runner, helper and
> hotfix builder must read and obey this document before performing any upgrade or hotfix
> action.**
>
> If code behaviour conflicts with this document, **the P0 rules here win and the program
> must fail closed. "Carry on as best it can" is not an option.**

This repository holds the contract at the path section 20 mandates,
`docs/upgrade-validation-chain.md`, with the Chinese copy at
`docs/zh-CN/upgrade-validation-chain.md`. Programs parse the machine-readable block in
section 21 out of this file; they do not re-derive the rules from anywhere else. The
repository-specific mapping from rule to enforcement point, and the obligations that are not
yet met, are in sections 22 and 23 — they are part of the contract, not commentary.

---

## 0. Absolute rules (a program must not ignore these)

### 0.1 Must be obeyed

These are hard gates that cannot be bypassed:

1. **Every upgrade or hotfix action must first resolve the target package identity.**
2. **The object shown in the UI, the object named in the confirmation, the object posted to,
   and the object the back end executes must be the same package.**
3. **A hotfix does not support `--resume`.**
4. **After a hotfix fails or is interrupted, the only recovery is to re-execute the same
   package; `--resume` must not be called.**
5. **Only a rolling upgrade that failed or was interrupted may be continued with `--resume`.**
6. **Only the same `patch_id`/`package_id` may adopt the maintenance lock it left behind.**
7. **Different packages must never adopt, overwrite or clear each other's maintenance lock.**
8. **A `.cgpatch` that has been signed, released, or given a SHA-256 must never be rebuilt in
   place.**
9. **Same file name + same version + different bytes / different SHA-256 = release incident.**
10. **A historical success must never be overwritten by a later failed action.**
11. **Package deployment state and operation execution history must be separated.**
12. **A change of leader is not in itself a failure.**
13. **Acceptance checks a unique leader, quorum, voters, readiness and rejoin — not that one
    fixed IP stayed leader.**
14. **Rollback must not overwrite a running binary with `cp`. It must use a temporary file and
    an atomic `mv` on the same filesystem, or stop the service first and then replace the
    file.**
15. **A built artifact must pass source-level tests, artifact-level checks, mutation
    verification and signature verification.**
16. **When a gate finds an inconsistency it must block the release. It must not be downgraded
    to a warning.**
17. **A program must not substitute an implicit default such as `packages[0]` or `latest` for
    the subject the operator actually selected.**
18. **Any fallback that would change the package identity must fail outright.**
19. **Every re-execute / resume / rollback action must re-verify the package kind and target
    identity on the back end. The front end must not be trusted.**
20. **These rules must not be skipped by a later program on the grounds of "compatibility with
    the old logic", a "temporary bypass", or "the site needs it now".**

---

## 1. The unified object model

An upgrade system must distinguish at least these three objects:

```text
Package
  ├─ package_id / patch_id
  ├─ kind: hotfix | rolling_upgrade
  ├─ build_commit
  ├─ base_commit
  ├─ fix_commits[]
  ├─ sha256
  ├─ signature
  └─ payload

Deployment State
  ├─ not_installed
  ├─ applying
  ├─ installed
  ├─ rollbacking
  ├─ rolled_back
  └─ recovery_required

Operation History
  ├─ operation_id
  ├─ package_id
  ├─ mode: plan | execute | retry | resume | rollback
  ├─ started_at
  ├─ finished_at
  ├─ status
  ├─ message
  └─ events[]
```

## 1.1 Overwriting state is forbidden

Forbidden:

```text
HF-05 09:30 execute succeeded
13:30 resume rejected
=> HF-05 status overwritten to failed
```

Required:

```text
Package: HF-05
Deployment State: installed

Operation #1
  mode=execute
  status=succeeded

Operation #2
  mode=resume
  status=rejected
```

**A later operation must not overwrite a completed historical state.**

---

## 2. Package identity rules

## 2.1 What defines an identity

An identity is defined by all of:

```text
package_id / patch_id
version
revision
sha256
signature
build_commit
base_commit
```

A change to any one of them is a new release identity.

## 2.2 A released package is immutable

As soon as any of these holds:

```text
signed
present in release/
present in the ledger
has a sha256 sidecar
handed to a site
installed at a site
```

the bytes of that package are permanently immutable.

Forbidden:

```text
clusterguard-2.2-105.x86_64.cgpatch
old sha256 = AAA

rebuilt and still called:
clusterguard-2.2-105.x86_64.cgpatch
new sha256 = BBB
```

A new identity must be produced, for example:

```text
2.2-105-r1
2.2-105+repack1
HF-2026-0930-01-r1
```

and recorded:

```text
supersedes
replacement_reason
original_sha256
replacement_sha256
```

---

## 3. Splitting hotfixes from rolling upgrades

## 3.1 Rolling upgrade

Permitted state machine:

```text
plan
  ↓
execute
  ↓
failed/interrupted
  ↓
resume
  ↓
--resume
```

## 3.2 Hotfix

Permitted state machine:

```text
plan
  ↓
execute
  ↓
failed/interrupted
  ↓
retry same package
  ↓
execute same package again
```

**Forbidden:**

```text
hotfix + --resume
```

When the back end is given:

```text
kind=hotfix
mode=resume
```

it must:

```text
REJECT
error_code = HOTFIX_RESUME_NOT_SUPPORTED
```

It must not quietly turn that into an execute unless a future revision of this protocol says
so explicitly.

A hotfix "retry" is an execute of the same package; the API has no separate retry mode, and a
hotfix re-run is recorded as an execute. Adding a mode the runner does not have would put a
second vocabulary for the same action into the logs and the audit trail, which section 6.1
puts a stop to.

---

## 4. Same-package re-entry and the maintenance lock

## 4.1 When adoption is allowed

Only when:

```text
existing_lock.patch_id == current_package.patch_id
```

is this permitted:

```text
adopt_current_update_locks()
```

## 4.2 A foreign lock

If:

```text
existing_lock.patch_id != current_package.patch_id
```

then it must:

```text
BLOCK
FOREIGN_UPDATE_LOCK
```

Forbidden:

```text
clearing someone else's lock
overwriting someone else's lock
reusing someone else's operation
treating every hotfix as re-enterable
```

## 4.3 The principle

```text
same package → re-entry allowed
different package → blocked
```

---

## 5. Console object consistency

## 5.1 A single subject

There must be exactly one source:

```text
softwareUpdateSubject()
```

Every one of these must reference that same object:

```text
button visibility
button enabled/disabled
the confirmation
patch_id
the POST URL
rollback target
resume target
retry target
history row action
```

## 5.2 `packages[0]` is forbidden

Forbidden:

```js
patchID = latestSoftwareUpdate()?.package?.patch_id
```

as a substitute for the object the button belongs to.

In particular this is forbidden:

```text
button visibility derived from = HF-04
POST target = packages[0] = HF-05
```

## 5.3 Rollback is held to a stricter rule

A rollback must carry explicitly:

```text
selected_subject.package_id
```

and the back end must re-verify:

```text
request patch_id
==
resolved package_id
```

A mismatch is rejected.

---

## 6. The back end as the last line of defence

Whether or not the UI has already checked, the back end must re-check:

```text
package exists
package identity matches
kind matches operation mode
maintenance lock ownership matches
operation transition legal
signature valid
package not superseded/invalid
```

## 6.1 Legal mode matrix

| kind | plan | execute | retry | resume | rollback |
|---|---:|---:|---:|---:|---:|
| rolling_upgrade | ✅ | ✅ | ❌ / per protocol | ✅ | ✅ |
| hotfix | ✅ | ✅ | ✅ | ❌ | ✅ |

For any illegal combination:

```text
HTTP/API reject
do not start the helper
do not start the runner
do not write a maintenance lock
do not overwrite a historical operation
```

---

## 7. Leader and quorum rules

## 7.1 A fixed leader is forbidden

Forbidden:

```text
leader before the run = .153
leader after the run must still be .153
```

Correct:

```text
unique_leader = true
quorum = true
voters = expected
all_nodes_ready = true
target_node_rejoined = true
```

A leader moving from:

```text
.153 → .154
```

is not in itself a failure.

## 7.2 Leader discovery

While a node's local control plane is restarting:

```text
leader_known=false
```

the program must not fall back to:

```text
self
```

and then mis-report:

```text
leader_changed
```

It should prefer:

```text
leader_api_address
peer/voter rediscovery
cluster status
```

and distinguish:

```text
unknown != changed
```

---

## 8. Rollback rules

## 8.1 The ETXTBSY-prone form is forbidden

Forbidden:

```bash
cp backup_binary /usr/local/bin/clusterguard
```

overwriting a binary that is running.

## 8.2 The correct form

On the same filesystem:

```bash
install -m 0755 backup/clusterguard \
  /usr/local/bin/.clusterguard.rollback.$$

sync

mv -f \
  /usr/local/bin/.clusterguard.rollback.$$ \
  /usr/local/bin/clusterguard
```

or:

```text
stop service
replace file
start service
```

## 8.3 When a rollback is complete

Replacing the file successfully is not a successful rollback.

All of these must be verified:

```text
binary sha256
version
service active
ready=true
node rejoin
cluster quorum
unique leader
maintenance state
```

---

## 9. Backup manifest isolation

Forbidden:

```text
backup-*.txt
```

as the recovery logic for "take the newest backup".

It must be bound to:

```text
backup-<hotfix_id>-<stamp>.txt
```

and recovery must verify:

```text
manifest.hotfix_id == current_hotfix_id
```

Restoring another patch's backup is forbidden.

---

## 10. build_commit / base_commit / fix_commits

## 10.1 What `base_commit` is

`base_commit` must be:

> **the build tree the target site already has**

and not mechanically:

```text
the previous formal major version
some older hotfix
the base in the template
```

## 10.2 The gate

For:

```text
base_commit..build_commit
```

every commit is scanned.

If a commit:

```text
touches a production path
AND
is not in fix_commits
```

then:

```text
FAIL
```

Declaring someone else's historical fix as this package's own `fix_commit` in order to get
past the gate is forbidden. The correct move is to narrow `base_commit`.

---

## 11. The pre-build chain

All of this must pass before building:

```text
[1] source tree clean/known
[2] base_commit valid
[3] build_commit contains all declared fixes
[4] production changes fully accounted
[5] package identity unique
[6] previous released bytes immutable
[7] payload mapping correct
[8] ownership/mode correct
[9] restart units explicit
[10] rollback generation valid
```

If any fails:

```text
DO NOT BUILD
```

---

## 12. The post-build chain

Must be checked:

```text
signature=verified
kind correct
schema_version correct
base_commit correct
build_commit correct
fix_commits correct
payload list exact
target path exact
file owner exact
file mode exact
restart_unit exact
database_mutation expected
rollback available
sha256 sidecar matches
```

and:

```text
the runtime scripts inside the package
==
the corresponding files in the source tree
```

must be byte-identical.

---

## 13. Tests and the mutation gate

Passing ordinary tests is not enough.

All of these must exist:

```text
source tests
package tests
API tests
console tests
runner tests
mutation tests
```

A mutation test must prove:

```text
a real error → is caught
sensitive text in a comment only → is not falsely caught
moving a correct statement into a comment → fails
wrong package id → fails
hotfix resume → fails
foreign lock adoption → fails
packages[0] fallback → fails
```

and there must be at least one:

```text
no-bite control
```

proving the tests are not "fail no matter what is changed".

---

## 14. Checks before a field run

Before uploading at the site:

```text
sha256sum package
```

must match the release ledger exactly.

Check:

```text
cluster ready
unique leader
voters expected
active_operations=0
update_maintenance_active=false
```

If a maintenance window is already open:

```text
establish the lock owner first
do not upload a new package straight away
```

---

## 15. Checks during a field run

After each node completes, check:

```text
file digest
service state
ready state
node membership
leader discovery
cluster quorum
```

Do not check only "the command exited 0".

---

## 16. Checks after a field run

The order must be:

```text
1. the same sha256 for the target files on all three nodes
2. service active
3. helper active (where involved)
4. ready=true
5. voters=3
6. unique leader=1
7. peer links healthy
8. update_maintenance_active=false
9. active_operations=0
10. package deployment state correct
11. operation history append-only
12. what the UI shows matches the real state
```

---

## 17. Required console regression

After every change to upgrade/hotfix behaviour in the console, at least these must be
verified:

### Case A

```text
successful hotfix
→ 续跑 is not shown
```

### Case B

```text
failed hotfix
→ 重新执行 is shown
→ 续跑 is not shown
```

### Case C

```text
failed rolling upgrade
→ 续跑 is shown
```

### Case D

```text
HF-04 = pending/failed
HF-05 = latest/succeeded

click HF-04's action
→ the posted patch_id must be HF-04
```

### Case E

```text
click HF-04's rollback
→ the rollback target must be HF-04
→ falling back to HF-05 / packages[0] is forbidden
```

Cases A–E are executable in this repository:

```text
node tools/console-update-hotfix-recovery-acceptance.cjs
```

It drives the real console page over the DevTools protocol against a local fixture built from
the records the site actually held, and reads the id the page posted off the fixture's request
log. Without a browser it reports NOT RUN rather than passing.

---

## 18. The HF-05 / 2026-09-30 regression baseline

These facts must be kept permanently as a regression sample:

```text
HF-05 had actually succeeded
↓
resume was clicked by mistake
↓
the hotfix runner refused --resume immediately
↓
the real cluster was not damaged
↓
but the historical success was overwritten with failed
```

This proves three things must be defended against forever:

```text
1. the UI subject and the POST target disagreeing
2. an illegal hotfix resume routing
3. an operation overwriting the package deployment state
```

---

## 19. Release discipline

Forbidden:

```text
describing a package as a formal remote release when it has not been pushed or tagged
```

These stages must be distinguished:

```text
built
signed
validated
released locally
pushed
tagged
uploaded to field
installed
verified in field
```

Every report must state the true stage.

---

## 20. The program read requirement

Every future automation must confirm this document exists before performing upgrade-related
work:

```text
MANDATORY_DOC = docs/upgrade-validation-chain.md
```

If it does not exist:

```text
FAIL CLOSED
```

If the `VALIDATION_CONTRACT_VERSION` in this document is higher than the version a program
supports:

```text
FAIL CLOSED
```

Silently ignoring an unknown rule is forbidden.

---

## 21. Machine-readable contract

```yaml
VALIDATION_CONTRACT_VERSION: 1

mandatory: true
fail_closed: true

package:
  immutable_after_release: true
  identity_must_be_explicit: true
  forbid_same_identity_different_sha256: true

hotfix:
  resume_supported: false
  retry_same_package_supported: true
  same_package_lock_adoption_only: true

rolling_upgrade:
  resume_supported: true

console:
  subject_must_equal_action_target: true
  forbid_packages_0_fallback: true

backend:
  revalidate_package_kind: true
  revalidate_package_identity: true
  revalidate_lock_owner: true

history:
  package_state_separate_from_operation_history: true
  append_only_operations: true
  forbid_success_overwrite: true

leader:
  fixed_leader_required: false
  unique_leader_required: true
  quorum_required: true

rollback:
  direct_cp_over_running_binary_forbidden: true
  atomic_replace_required: true
  verify_after_restore: true

release:
  signed_artifact_immutable: true
  sha256_required: true
  mutation_gate_required: true

field:
  verify_cluster_health_before: true
  verify_cluster_health_after: true
```

---

## 22. Where each rule is enforced in this repository

The table is part of the contract: a rule with no enforcement point and no open obligation is
an unenforced rule. `tools/verify-upgrade-validation-chain.cjs` reads this table's rules as
code and re-checks the enforcement points every run.

| Rule | Enforced at | State |
|---|---|---|
| 0.1.1 identity explicit | `scripts/clusterguard-upgrade.sh` reads the patch id out of the signed manifest and validates it; the API path is `/api/v1/platform/updates/{patchID}/{mode}` | enforced |
| 0.1.2 subject == action target | `softwareUpdateSubject()` in `internal/api/console.html`; verified in a real browser by Cases A–E | enforced |
| 0.1.3 hotfix has no resume (routing) | `update_mode_arguments()` in `scripts/clusterguard-update-job.sh`; pinned by `TestUpdateJobRoutesResumeByPackageKind` | enforced |
| 0.1.3 hotfix has no resume (back end) | `ErrResumeUnsupported` in `internal/platformupdate/manager.go`, mapped to HTTP 409, returned before any file is written | enforced |
| 0.1.4 hotfix retry re-runs the same package | the job wrapper emits `--execute --yes`; the console posts `/execute` for the same patch id | enforced |
| 0.1.5 rolling upgrade may resume | the job wrapper emits `--resume --execute --yes` for a non-hotfix | enforced |
| 0.1.6 same-package lock adoption | `current_update_lock_on_host()` requires the lock's own patch id (`grep -Fqx '${patch_id}'`) | enforced |
| 0.1.7 foreign lock blocked | `foreign_update_lock_on_host()` / `detect_foreign_update_lock()`; a foreign holder is refused by name | enforced |
| 0.1.8 published bytes immutable | `scripts/build-hotfix-patch.sh` refuses to overwrite an artifact; a never-delivered build is renamed aside with a stated reason | enforced |
| 0.1.9 same name, different bytes | `hotfixes/hotfix-publications.json` revision chain, re-checked by `tools/verify-hotfix-patch-catalog.cjs` | enforced |
| 0.1.10 no success overwrite | — | **open — see §23** |
| 0.1.11 state and history separated | — | **open — see §23** |
| 0.1.12 a leader change is not a failure | `resolve_leader_host()` re-resolves after the patched node restarts instead of pinning the pre-run leader | enforced |
| 0.1.13 unique leader, quorum, voters | `verify_cluster_idle()` | enforced |
| 0.1.14 rollback never `cp`s | the generated `rollback.sh` uses `mktemp` plus `mv -f` | enforced |
| 0.1.15 source + artifact + mutation + signature | `scripts/build-hotfix-patch.sh` and `tools/verify-hotfix-patch-catalog.cjs` (42 checks, no skips) | enforced |
| 0.1.16 a gate blocks the release | the gates exit non-zero; the catalogue gate reports `did not run` rather than passing when a check is skipped | enforced |
| 0.1.17 no implicit `packages[0]` / `latest` | `softwareUpdateSubject()`, `pendingSoftwareUpdate()` and `prepareSoftwareUpdateExecution()` in the console | enforced |
| 0.1.18 an identity-changing fallback fails | `startSoftwareUpdate()` returns false for an id that is not a record in the list; the back end rejects an unknown id | enforced |
| 0.1.19 the back end re-validates kind and identity | `internal/platformupdate/manager.go`, and the job wrapper reading the signed `package.json` kind | enforced |
| 0.1.20 no rule may be skipped | `tools/verify-upgrade-validation-chain.cjs` fails closed when this document is missing, unparsable or newer than it supports | enforced |
| 5.3 rollback carries its subject | the rollback URL carries the patch id and the confirmation asks the operator to type that same id | enforced |
| 7.2 unknown is not changed | `verify_cluster_idle()` reports an unobserved leader as `leader_unknown`, and only an actually different leader as `leader_changed` | enforced |
| 9 a backup is bound to its hotfix | the builder names the list `backup-<hotfix_id>-<stamp>.txt` and the generated `rollback.sh` only ever globs its own id | enforced |
| 10 `base_commit` is the site's tree | `tools/verify-hotfix-patch-catalog.cjs` scans `base_commit..build_commit` per commit; `docs/update-and-patch.md` states the rule | enforced |

---

## 23. Open obligations

These rules are in force and are **not yet implemented**. They are listed here, and printed on
every gate run, so that no reader can mistake the contract for fully satisfied. Each one names
the card that closes it.

```text
history.package_state_separate_from_operation_history
  card: UPDATE-OPERATION-HISTORY-P0
  today: one status.json per package holds the newest attempt, so a later attempt
  replaces an earlier result. Evidence: after the 2026-09-30 13:30 refused resume, the
  leader's status.json said resume/failed for a package whose own event log is a complete
  success, while the other two nodes still said execute/succeeded.

history.append_only_operations
  card: UPDATE-OPERATION-HISTORY-P0
  today: operations are not recorded as a list; only events.jsonl is append-only.

history.forbid_success_overwrite
  card: UPDATE-OPERATION-HISTORY-P0
  today: the general rule is not enforced. The specific route that caused the incident is
  closed - the back end now refuses a hotfix resume before a job exists, so the refused
  attempt writes nothing - but a later legitimate attempt can still replace a success.
```

`tools/verify-upgrade-validation-chain.cjs` reports these as `OPEN` on every run. Run it with
`--strict` to make them fail the run, which is what a release gate should use until the card
lands; the default is to report them without failing, so that a change unrelated to the
history model is not blocked by work the contract's own section 23 records as outstanding.

---

## 24. Final principle

The goal of the upgrade system is not:

> "the script eventually returned 0".

It is:

> **every change has an explicit identity, an explicit target, an explicit state machine, an
> explicit lock owner, an explicit rollback boundary and an explicit chain of evidence — and
> no program can operate the wrong package through a default value, a historical overwrite, a
> wrong recovery mode or a silent fallback.**

---

**END OF MANDATORY VALIDATION CONTRACT**
