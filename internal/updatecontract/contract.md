# ClusterGuard Upgrade / Hotfix Validation Contract

> **Contract ID:** `CG-UPGRADE-CONTRACT`
>
> **Contract Version:** `2`
>
> **Severity:** `P0`
>
> **Mode:** `MANDATORY / FAIL-CLOSED`
>
> **Scope:** Upgrade / Hotfix / Retry / Resume / Rollback / Build / Sign / Release / Field Validation
>
> 本文是 **ClusterGuard 升级与热修链路的规范性契约（Normative Contract）**，不是说明性文档。
>
> 所有相关程序、脚本、控制台、API、Runner、Helper、Builder、CI、AI Agent 在执行相关动作前，必须加载并遵守本契约。
>
> **MUST / MUST NOT = 强制。任何违反必须阻断。**
>
> **SHOULD / SHOULD NOT = 建议。若偏离必须记录理由。**
>
> **MAY = 可选。**

---

# 0. Contract Bootstrap

任何升级相关入口执行前，必须完成：

```text
contract_id = CG-UPGRADE-CONTRACT
contract_version = 2
contract_loaded = true
contract_supported = true
```

若出现以下任一情况：

```text
contract missing
contract unreadable
contract version > program supported version
machine-readable section parse failed
```

必须：

```text
FAIL CLOSED
error_code = CG_CONTRACT_UNAVAILABLE
```

禁止：

```text
继续执行
降级为 warning
使用旧默认规则
静默忽略未知字段
```

---

# 1. Core Invariants

以下 Invariant 是整个系统的最高优先级规则。

后续章节只能细化，不得修改其语义。

---

## INV-001 — Exact Package Identity

**MUST**

每次操作必须显式绑定唯一 `package_id / patch_id`。

UI、确认框、API、Helper、Runner、Lock、Operation History 必须指向同一个 package。

### PASS

```text
render.package_id
==
confirm.package_id
==
request.package_id
==
resolved.package_id
==
runner.package_id
```

### FAIL

任意不一致。

### Error

```text
CG_PACKAGE_IDENTITY_MISMATCH
```

---

## INV-002 — No Implicit Package Fallback

**MUST NOT**

不得使用以下隐式值替代用户/状态机已经选定的对象：

```text
packages[0]
latest
first available
newest record
last successful package
```

除非该动作本身的协议明确就是“选择最新包”，且不存在已有 subject。

### Error

```text
CG_IMPLICIT_PACKAGE_FALLBACK
```

---

## INV-003 — Released Artifact Is Immutable

满足任一条件后，package 字节永久不可修改：

```text
signed
sha256 sidecar generated
entered release ledger
delivered
uploaded
installed
```

同 identity 不得出现不同 SHA256。

### Error

```text
CG_RELEASED_ARTIFACT_MUTATED
```

任何修改必须产生新 identity：

```text
revision++
or
new package_id
```

并声明：

```text
supersedes
replacement_reason
original_sha256
replacement_sha256
```

---

## INV-004 — Hotfix Never Uses Resume

对于：

```text
kind=hotfix
```

必须：

```text
resume_supported=false
```

失败/中断后的合法动作是：

```text
retry same package
```

而不是：

```text
--resume
```

### Error

```text
CG_HOTFIX_RESUME_FORBIDDEN
```

---

## INV-005 — Lock Ownership Is Package-Scoped

只有：

```text
existing_lock.package_id == current.package_id
```

才允许接管已有 lock。

### Same Package

```text
ALLOW adopt/re-entry
```

### Foreign Package

```text
BLOCK
error_code = CG_FOREIGN_UPDATE_LOCK
```

禁止清理、覆盖、复用其他 package 的 lock。

---

## INV-006 — Package State != Operation State

Package 的部署状态与每一次操作记录必须分离。

禁止后一次操作覆盖前一次成功部署结果。

### 正确

```text
Package HF-05
deployment_state=installed

Operation #1
mode=execute
status=succeeded

Operation #2
mode=resume
status=rejected
```

### 禁止

```text
Operation #2 failed
=> overwrite Package HF-05 as failed
```

### Error

```text
CG_HISTORY_OVERWRITE_FORBIDDEN
```

---

## INV-007 — Leader Identity Is Not Stable State

Leader IP/节点发生变化本身不是升级失败。

必须验证：

```text
unique_leader=true
quorum=true
voters=expected
nodes_ready=true
target_node_rejoined=true
```

禁止：

```text
leader_before == leader_after
```

作为成功条件。

### Error

```text
CG_INVALID_FIXED_LEADER_ASSUMPTION
```

---

## INV-008 — Rollback Must Be Atomic or Offline

运行中的二进制禁止：

```bash
cp backup target
```

必须：

```text
atomic replace on same filesystem
```

或：

```text
stop service → replace → start service
```

推荐：

```bash
install -m 0755 backup "$target.tmp"
sync
mv -f "$target.tmp" "$target"
```

### Error

```text
CG_NONATOMIC_RUNNING_BINARY_RESTORE
```

---

## INV-009 — Backend Revalidates Everything

前端校验永远不是最终安全边界。

服务端执行前必须重新验证：

```text
package exists
package identity
package kind
requested mode
lock ownership
transition legality
signature
superseded status
```

不得信任客户端传入的 mode/package identity。

---

## INV-010 — Validation Failure Stops Release

任何 P0 gate 失败：

```text
FAIL
```

不得：

```text
WARN AND CONTINUE
skip
manual override
temporary bypass
```

除非产生**新的正式 Contract Version**明确修改规则。

---

# 2. Canonical State Model

---

## 2.1 Package

```yaml
Package:
  package_id: string
  kind: hotfix | rolling_upgrade
  version: string
  revision: integer
  base_commit: git_sha
  build_commit: git_sha
  fix_commits: [git_sha]
  sha256: hex
  signature: verified | invalid | unknown
  supersedes: package_id | null
```

---

## 2.2 Deployment State

仅描述“这个 package 在现场是否已经生效”。

```text
not_installed
applying
installed
rollbacking
rolled_back
recovery_required
```

---

## 2.3 Operation

每次动作创建独立 operation。

```yaml
Operation:
  operation_id: string
  package_id: string
  mode: plan | execute | retry | resume | rollback
  status: queued | running | succeeded | failed | rejected | interrupted
  started_at: timestamp
  finished_at: timestamp | null
  error_code: string | null
  message: string
  events: []
```

Operation 必须 append-only。

---

# 3. Legal Transition Matrix

| Package Kind | plan | execute | retry | resume | rollback |
|---|---:|---:|---:|---:|---:|
| `rolling_upgrade` | ✅ | ✅ | ❌* | ✅ | ✅ |
| `hotfix` | ✅ | ✅ | ✅ | ❌ | ✅ |

`rolling_upgrade retry` 若未来需要支持，必须通过新 Contract Version 明确定义。

非法组合必须：

```text
reject before helper/runner starts
do not acquire lock
do not modify deployment state
append rejected operation if audit requires
```

---

# 4. Hotfix Retry Contract

Hotfix 的恢复流程固定为：

```text
failed/interrupted
        ↓
retry
        ↓
same package_id
        ↓
check lock owner
        ↓
same package lock → adopt
no lock            → acquire
foreign lock       → reject
        ↓
execute idempotent apply
        ↓
verify
        ↓
release maintenance
```

禁止：

```text
retry Hotfix A
→ secretly execute Hotfix B
```

禁止：

```text
resume Hotfix A
→ auto-convert to execute without explicit protocol
```

当前规则：**resume 请求直接拒绝。**

---

# 5. Console Subject Contract

必须有单一对象：

```text
softwareUpdateSubject
```

它同时决定：

```text
display
button visibility
button enabled
confirmation
request URL
request package_id
rollback package_id
retry package_id
resume package_id
```

以下结构禁止存在：

```text
display subject = pending || latest
action package_id = packages[0]
```

### Required Assertion

```text
subject.package_id == outbound_request.package_id
```

---

# 6. Backend Operation Guard

后端必须在创建 job 前执行：

```text
resolve package
validate signature
validate kind
validate requested mode
validate lock owner
validate legal transition
validate not superseded
```

示意：

```text
if kind == hotfix && mode == resume:
    reject(CG_HOTFIX_RESUME_FORBIDDEN)

if lock.exists && lock.package_id != package.package_id:
    reject(CG_FOREIGN_UPDATE_LOCK)
```

---

# 7. Leader / Cluster Validation

---

## 7.1 Required Health

每次节点操作后至少确认：

```text
unique_leader=true
quorum=true
voters=expected
target_node_rejoined=true
cluster_ready=true
```

---

## 7.2 Leader Discovery

本机控制面暂时不可用：

```text
leader_known=false
```

不得解释成：

```text
leader=self
```

也不得解释成：

```text
leader_changed=true
```

必须：

```text
unknown != changed
```

允许使用：

```text
known leader_api_address
peer/voter rediscovery
authoritative cluster status
```

---

# 8. Rollback Contract

---

## RB-001 — Backup Identity

禁止使用：

```text
latest backup-*.txt
```

必须：

```text
backup-<package_id>-<stamp>.txt
```

恢复时验证：

```text
manifest.package_id == operation.package_id
```

不一致：

```text
CG_BACKUP_IDENTITY_MISMATCH
```

---

## RB-002 — Restore Method

运行文件必须：

```text
temp file
→ fsync/sync as required
→ atomic mv
```

或停服务替换。

---

## RB-003 — Rollback Completion

rollback 成功必须同时满足：

```text
target sha256 restored
version restored
service active
ready=true
node rejoined
quorum=true
unique_leader=true
maintenance state correct
```

“文件复制成功”不等于 rollback 成功。

---

# 9. Build Commit Accounting

---

## BC-001 — base_commit

`base_commit` 定义为：

> 目标现场当前已经拥有的代码树。

不得机械复制旧 spec。

---

## BC-002 — Production Diff Accounting

对：

```text
base_commit..build_commit
```

逐提交分析。

每个触及 production path 的 commit：

```text
MUST be declared in fix_commits
```

否则：

```text
CG_UNDECLARED_PRODUCTION_CHANGE
```

禁止为了过 gate 把历史上已存在的修复伪装成本包 fix。

正确方式通常是修正 `base_commit`。

---

# 10. Build Preflight Gate

构建前必须全部通过：

| ID | Check |
|---|---|
| PRE-001 | Contract loaded |
| PRE-002 | Source tree known |
| PRE-003 | `base_commit` valid |
| PRE-004 | `build_commit` valid |
| PRE-005 | Production diffs accounted |
| PRE-006 | Package identity unique |
| PRE-007 | Released artifact not overwritten |
| PRE-008 | Payload mapping exact |
| PRE-009 | Owner/mode declared |
| PRE-010 | Restart units explicit |
| PRE-011 | Rollback generator valid |

任意 FAIL：

```text
DO NOT BUILD
```

---

# 11. Artifact Gate

构建后必须验证：

| ID | Check |
|---|---|
| ART-001 | signature verified |
| ART-002 | sha256 sidecar matches |
| ART-003 | kind correct |
| ART-004 | schema version correct |
| ART-005 | base/build/fix commits correct |
| ART-006 | payload exact |
| ART-007 | destination exact |
| ART-008 | owner exact |
| ART-009 | mode exact |
| ART-010 | restart units exact |
| ART-011 | rollback available/expected |
| ART-012 | database mutation declared |
| ART-013 | runtime scripts byte-identical to source |

任何 FAIL：

```text
DO NOT RELEASE
```

---

# 12. Test Gate

必须同时覆盖：

```text
unit
integration
console
API
runner
builder
artifact
mutation
```

---

## 12.1 Required Mutations

至少证明：

| Mutation | Expected |
|---|---|
| code 使用裸 `backup-*.txt` | FAIL |
| 注释出现 `backup-*.txt` | PASS |
| 把真实 `mv -f` 移入注释 | FAIL |
| UI subject 与 POST target 不一致 | FAIL |
| 使用 `packages[0]` fallback | FAIL |
| hotfix + resume | FAIL |
| foreign package adopts lock | FAIL |
| same identity different sha256 | FAIL |
| overwrite succeeded package state | FAIL |

必须有 no-bite control：

```text
合法无关修改 → PASS
```

---

# 13. Field Preflight

上传现场前：

```text
verify package sha256
verify signature
verify release ledger identity
```

集群必须：

```text
ready=true
unique_leader=true
voters=expected
active_operations=0
update_maintenance_active=false
```

若 maintenance 已存在：

```text
identify owner first
```

禁止直接上传新包。

---

# 14. Field Execution Checks

每个节点完成后检查：

```text
payload digest
service state
ready
membership
leader visibility
quorum
```

禁止只依赖 exit code。

---

# 15. Field Final Acceptance

按顺序执行：

```text
FIELD-001 target files sha256 consistent
FIELD-002 services active
FIELD-003 helper active when applicable
FIELD-004 all nodes ready
FIELD-005 voters expected
FIELD-006 exactly one leader
FIELD-007 peer links healthy
FIELD-008 update_maintenance_active=false
FIELD-009 active_operations=0
FIELD-010 deployment state correct
FIELD-011 operation history append-only
FIELD-012 UI state matches authoritative backend state
```

---

# 16. Mandatory Console Regression

---

## UI-001 Successful Hotfix

```text
installed hotfix
→ no Resume
```

---

## UI-002 Failed Hotfix

```text
failed/interrupted hotfix
→ Retry
→ no Resume
```

---

## UI-003 Failed Rolling Upgrade

```text
failed/interrupted rolling upgrade
→ Resume
```

---

## UI-004 Cross-Record Targeting

```text
HF-04 pending/failed
HF-05 latest/succeeded

click HF-04 action
→ outbound package_id == HF-04
```

---

## UI-005 Rollback Targeting

```text
click HF-04 rollback
→ target HF-04
→ MUST NOT target packages[0]
```

---

# 17. Permanent Regression Scenario

以下事故必须永久保留为测试 fixture：

```text
HF-05 execute succeeded
↓
a later Resume request targeted HF-05
↓
hotfix rejected --resume
↓
cluster remained healthy
↓
operation failure overwrote displayed package status
```

必须永久防止：

```text
REG-001 wrong UI subject/action target
REG-002 hotfix resume routing
REG-003 operation overwrites package deployment result
```

---

# 18. Release Lifecycle

必须显式区分：

```text
built
signed
validated
released_local
pushed
tagged
uploaded_field
installed_field
verified_field
```

禁止把：

```text
built/signed locally
```

描述为：

```text
released/pushed/installed
```

---

# 19. Error Code Registry

| Error Code | Meaning |
|---|---|
| `CG_CONTRACT_UNAVAILABLE` | Contract missing/unreadable/unsupported |
| `CG_PACKAGE_IDENTITY_MISMATCH` | Operation targets inconsistent package |
| `CG_IMPLICIT_PACKAGE_FALLBACK` | Implicit latest/packages[0] fallback |
| `CG_RELEASED_ARTIFACT_MUTATED` | Released bytes changed |
| `CG_HOTFIX_RESUME_FORBIDDEN` | Resume requested for hotfix |
| `CG_FOREIGN_UPDATE_LOCK` | Package attempted to adopt foreign lock |
| `CG_HISTORY_OVERWRITE_FORBIDDEN` | Later operation overwrote prior deployment result |
| `CG_INVALID_FIXED_LEADER_ASSUMPTION` | Fixed leader identity used as success requirement |
| `CG_NONATOMIC_RUNNING_BINARY_RESTORE` | Unsafe rollback overwrite |
| `CG_BACKUP_IDENTITY_MISMATCH` | Backup belongs to another package |
| `CG_UNDECLARED_PRODUCTION_CHANGE` | Build range contains undeclared production commit |

新增错误码必须更新本表。

---

# 20. AI / Agent Rules

任何 Codex / Claude / Trae / AI Agent 在修改以下范围前：

```text
upgrade
hotfix
rollback
release
package builder
console update flow
operation manager
maintenance lock
```

必须先读取本文，并在工作记录中输出：

```text
CG-UPGRADE-CONTRACT
contract_version=2
contract_loaded=true
```

AI MUST NOT：

```text
weaken MUST to SHOULD
convert FAIL to WARN
skip mutation gate
skip artifact gate
overwrite released package
delete historical failure evidence
change package identity without revision
invent a fallback target
assume latest == selected
assume leader must remain unchanged
```

如果无法证明符合某条 P0：

```text
STOP
mark unresolved
do not claim completion
```

---

# 21. Machine-Readable Contract

> 程序应优先解析本节。字段未知时必须 fail closed。

```yaml
contract:
  id: CG-UPGRADE-CONTRACT
  version: 2
  mandatory: true
  fail_closed: true

invariants:
  explicit_package_identity: true
  implicit_package_fallback_forbidden: true
  released_artifact_immutable: true
  package_state_separate_from_operations: true
  backend_revalidation_required: true

hotfix:
  resume_supported: false
  retry_same_package_supported: true
  same_package_lock_adoption_only: true

rolling_upgrade:
  resume_supported: true

console:
  subject_equals_action_target: true
  packages_0_fallback_forbidden: true

leader:
  fixed_identity_required: false
  unique_leader_required: true
  quorum_required: true
  unknown_is_not_changed: true

rollback:
  direct_cp_over_running_binary_forbidden: true
  atomic_replace_required: true
  backup_identity_bound_to_package: true
  post_restore_verification_required: true

build:
  production_diff_accounting_required: true
  base_commit_is_field_baseline: true

release:
  signed_artifact_immutable: true
  sha256_required: true
  mutation_gate_required: true

history:
  operation_append_only: true
  deployment_success_overwrite_forbidden: true

field:
  health_check_before: true
  health_check_after: true
```

---

# 22. Completion Definition

任何程序、AI、工程师不得仅以以下条件宣布完成：

```text
command exit 0
tests green
package built
```

完成必须意味着：

```text
identity correct
state transition legal
lock ownership correct
artifact immutable
rollback bounded
tests meaningful
mutation tests bite
field state healthy
history auditable
UI and backend agree
```

---

# 23. Change Control

修改本契约必须：

```text
1. increment contract version
2. record reason
3. update machine-readable block
4. update error codes if needed
5. update regression tests
6. update every parser/consumer that declares supported version
```

禁止原地修改规则而不升版本。

---

# 24. Final Rule

> **升级系统的正确性，不由“脚本有没有跑完”定义，而由：明确对象、合法状态迁移、锁归属、不可变发布物、可验证回滚、集群健康、完整审计链共同定义。**

任何程序无法证明上述条件时：

```text
FAIL CLOSED
```

---

**END OF CG-UPGRADE-CONTRACT v2**
