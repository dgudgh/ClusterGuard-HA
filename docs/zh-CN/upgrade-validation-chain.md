# 升级与热修统一校验链

> **级别：P0 —— 强制执行，不得跳过。**
>
> 本文不是说明性文档，而是 ClusterGuard 升级、热修、回退、续跑、重新执行、构建、签名、发布、现场验收的**统一强制校验契约**。
>
> **任何程序、脚本、AI Agent、CI、发布工具、控制台、API、Runner、Helper、Hotfix Builder 在执行升级/热修相关动作前，必须先读取并遵守本文。**
>
> 若代码行为与本文冲突：**以本文的 P0 规则为准，程序必须失败关闭（fail closed），禁止「尽量继续」。**

本仓库把契约放在第 20 节指定的路径 `docs/upgrade-validation-chain.md`，中文副本在
`docs/zh-CN/upgrade-validation-chain.md`。程序从本文件里解析第 21 节的机器可读块，不从
任何其他位置重新推导规则。本仓库「规则 → 落点」的映射，以及尚未履行的义务，在第 22 与
第 23 节——它们是契约的一部分，不是注释。

---

## 0. 绝对规则（程序不得忽略）

### 0.1 必须遵守

以下规则属于**不可绕过的硬门禁**：

1. **任何升级/热修动作必须先确定目标 package identity。**
2. **UI 展示对象、确认对象、POST 对象、后端执行对象必须是同一个 package。**
3. **Hotfix 不支持 `--resume`。**
4. **Hotfix 失败/中断后只能「重新执行同一个 package」，不能调用 `--resume`。**
5. **Rolling Upgrade 失败/中断后才允许走 `--resume`。**
6. **只有同一个 `patch_id`/`package_id` 才允许接管自己遗留的 maintenance lock。**
7. **不同 package 之间禁止接管、覆盖、清理对方的 maintenance lock。**
8. **已签名、已发布、已有 SHA256 的 `.cgpatch` 永远不可原地重建。**
9. **同文件名 + 同版本 + 不同字节/不同 SHA256 = 发布事故。**
10. **历史成功结果不得被后续失败动作覆盖。**
11. **Package deployment state 与 Operation execution history 必须分离。**
12. **Leader 发生变化本身不是失败。**
13. **升级验收应检查唯一 Leader、Quorum、Voters、Ready、Rejoin，而不是固定某个 IP 必须一直是 Leader。**
14. **Rollback 禁止用 `cp` 覆盖正在运行的二进制。必须使用临时文件 + 同文件系统原子 `mv`，或先停服务再替换。**
15. **构建产物必须经过源级测试 + 产物级检查 + 变异验证 + 签名验证。**
16. **门禁发现不一致时必须阻断发布，不得降级为 warning。**
17. **程序不得根据 `packages[0]`、`latest` 等隐式默认值替代用户实际选中的 subject。**
18. **任何 fallback 如果会改变 package identity，必须直接失败。**
19. **所有「重新执行/续跑/回退」动作都必须在后端重新验证 package kind 与 target identity，不能只信任前端。**
20. **本文规则不得被后续程序以「兼容旧逻辑」「临时绕过」「现场紧急」为理由跳过。**

---

## 1. 统一对象模型

升级系统必须至少区分以下三个对象：

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

## 1.1 禁止状态覆盖

禁止：

```text
HF-05 09:30 execute succeeded
13:30 resume rejected
=> HF-05 status 被覆盖成 failed
```

必须：

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

**后续操作不得覆盖历史完成状态。**

---

## 2. Package Identity 强制规则

## 2.1 Package identity

至少由以下字段共同定义：

```text
package_id / patch_id
version
revision
sha256
signature
build_commit
base_commit
```

任何一个发生变化，都视为新的发布身份。

## 2.2 已发布包不可变

一旦满足任一条件：

```text
已签名
已进入 release/
已进入台账
已生成 sha256 侧车
已交付现场
已被现场安装
```

则该 package 字节永久不可变。

禁止：

```text
clusterguard-2.2-105.x86_64.cgpatch
旧 sha256 = AAA

重新构建后仍叫：
clusterguard-2.2-105.x86_64.cgpatch
新 sha256 = BBB
```

必须产生新 identity，例如：

```text
2.2-105-r1
2.2-105+repack1
HF-2026-0930-01-r1
```

并记录：

```text
supersedes
replacement_reason
original_sha256
replacement_sha256
```

---

## 3. Hotfix 与 Rolling Upgrade 分流

## 3.1 Rolling Upgrade

允许状态机：

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

允许状态机：

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

**禁止：**

```text
hotfix + --resume
```

后端遇到：

```text
kind=hotfix
mode=resume
```

必须：

```text
REJECT
error_code = HOTFIX_RESUME_NOT_SUPPORTED
```

不得偷偷转换成 execute，除非未来协议明确修改。

Hotfix 的「重新执行」就是对同一个 package 执行 execute；API 没有单独的 retry 模式，热修重跑
记录为 execute。给 runner 里并不存在的模式另造一个名字，会让同一个动作在日志与审计里出现
两套词汇，这正是第 6.1 节要阻止的。

---

## 4. 同包重入与 Maintenance Lock

## 4.1 接管条件

只有：

```text
existing_lock.patch_id == current_package.patch_id
```

才允许：

```text
adopt_current_update_locks()
```

## 4.2 外包锁

如果：

```text
existing_lock.patch_id != current_package.patch_id
```

必须：

```text
BLOCK
FOREIGN_UPDATE_LOCK
```

禁止：

```text
清掉别人的锁
覆盖别人的锁
复用别人的 operation
把 any hotfix 都认为可重入
```

## 4.3 核心原则

```text
same package → re-entry allowed
different package → blocked
```

---

## 5. UI / Console 对象一致性

## 5.1 单一 Subject

必须存在唯一来源：

```text
softwareUpdateSubject()
```

以下动作必须引用同一对象：

```text
按钮显示条件
按钮 enabled/disabled
确认框
patch_id
POST URL
rollback target
resume target
retry target
history row action
```

## 5.2 禁止默认 packages[0]

禁止：

```js
patchID = latestSoftwareUpdate()?.package?.patch_id
```

用于代替当前按钮对应对象。

特别禁止：

```text
按钮显示依据 = HF-04
POST target = packages[0] = HF-05
```

## 5.3 回退比续跑更严格

Rollback 必须显式携带：

```text
selected_subject.package_id
```

后端再次验证：

```text
request patch_id
==
resolved package_id
```

不一致则拒绝。

---

## 6. 后端最终防线

无论 UI 是否已经校验，后端都必须重新检查：

```text
package exists
package identity matches
kind matches operation mode
maintenance lock ownership matches
operation transition legal
signature valid
package not superseded/invalid
```

## 6.1 mode 合法矩阵

| kind | plan | execute | retry | resume | rollback |
|---|---:|---:|---:|---:|---:|
| rolling_upgrade | ✅ | ✅ | ❌/按协议 | ✅ | ✅ |
| hotfix | ✅ | ✅ | ✅ | ❌ | ✅ |

任何不合法组合：

```text
HTTP/API reject
不启动 helper
不启动 runner
不写 maintenance lock
不覆盖历史 operation
```

---

## 7. Leader / Quorum 校验规则

## 7.1 禁止固定 Leader

禁止：

```text
应用前 leader=.153
应用后 leader 必须仍然=.153
```

正确：

```text
unique_leader = true
quorum = true
voters = expected
all_nodes_ready = true
target_node_rejoined = true
```

Leader 从：

```text
.153 → .154
```

本身不构成失败。

## 7.2 leader discovery

节点本地控制面重启期间：

```text
leader_known=false
```

不得直接 fallback 到：

```text
self
```

并误判：

```text
leader_changed
```

应优先：

```text
leader_api_address
peer/voter rediscovery
cluster status
```

并区分：

```text
unknown != changed
```

---

## 8. Rollback 强制规则

## 8.1 禁止 ETXTBSY 风险写法

禁止：

```bash
cp backup_binary /usr/local/bin/clusterguard
```

直接覆盖运行中二进制。

## 8.2 正确写法

同一文件系统：

```bash
install -m 0755 backup/clusterguard \
  /usr/local/bin/.clusterguard.rollback.$$

sync

mv -f \
  /usr/local/bin/.clusterguard.rollback.$$ \
  /usr/local/bin/clusterguard
```

或：

```text
stop service
replace file
start service
```

## 8.3 Rollback 完成条件

文件替换成功 ≠ rollback 成功。

必须同时验证：

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

## 9. Backup Manifest 隔离

禁止：

```text
backup-*.txt
```

作为「取最新备份」的恢复逻辑。

必须绑定：

```text
backup-<hotfix_id>-<stamp>.txt
```

恢复时必须验证：

```text
manifest.hotfix_id == current_hotfix_id
```

禁止恢复其他补丁的备份。

---

## 10. build_commit / base_commit / fix_commits

## 10.1 base_commit 定义

`base_commit` 必须是：

> **目标现场当前已经拥有的构建树**

而不是机械地取：

```text
上一个正式大版本
某个旧 hotfix
模板里的 base
```

## 10.2 Gate

对：

```text
base_commit..build_commit
```

逐提交扫描。

如果某提交：

```text
touches production path
AND
not in fix_commits
```

则：

```text
FAIL
```

禁止通过「把别人历史修复也声明成自己的 fix_commit」来绕过。应优先收窄正确 base。

---

## 11. 产物构建前校验链

构建前必须全部通过：

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

任一失败：

```text
DO NOT BUILD
```

---

## 12. 产物构建后校验链

必须检查：

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

并验证：

```text
包内 runtime_script
==
源码树对应文件
```

必须逐字节一致。

---

## 13. 测试 + Mutation Gate

普通测试通过仍不够。

必须同时有：

```text
source tests
package tests
API tests
console tests
runner tests
mutation tests
```

Mutation Test 必须证明：

```text
真正错误 → 会被抓
仅注释包含敏感文本 → 不应误抓
把正确语句移到注释 → 必须失败
错误 package id → 必须失败
hotfix resume → 必须失败
foreign lock adoption → 必须失败
packages[0] fallback → 必须失败
```

并有至少一个：

```text
no-bite control
```

证明测试不是「无论改什么都失败」。

---

## 14. 现场执行前校验

现场上传前：

```text
sha256sum package
```

必须与 release ledger 完全一致。

检查：

```text
cluster ready
unique leader
voters expected
active_operations=0
update_maintenance_active=false
```

若现场已有 maintenance：

```text
必须先确认 lock owner
禁止直接上传新包
```

---

## 15. 现场执行中校验

每个节点执行完成后检查：

```text
file digest
service state
ready state
node membership
leader discovery
cluster quorum
```

不要只检查「命令退出码」。

---

## 16. 现场执行后校验

顺序必须是：

```text
1. 三节点目标文件 sha256 一致
2. 服务 active
3. helper active（如涉及）
4. ready=true
5. voters=3
6. unique leader=1
7. peer links healthy
8. update_maintenance_active=false
9. active_operations=0
10. package deployment state correct
11. operation history append-only
12. UI 展示与真实状态一致
```

---

## 17. Console 回归必测

每次升级/热修相关 Console 修改后，至少验证：

### Case A

```text
successful hotfix
→ 不显示「续跑」
```

### Case B

```text
failed hotfix
→ 显示「重新执行」
→ 不显示「续跑」
```

### Case C

```text
failed rolling upgrade
→ 显示「续跑」
```

### Case D

```text
HF-04 = pending/failed
HF-05 = latest/succeeded

点击 HF-04 动作
→ POST patch_id 必须是 HF-04
```

### Case E

```text
点击 HF-04 rollback
→ rollback target 必须是 HF-04
→ 禁止 fallback 到 HF-05 / packages[0]
```

Case A–E 在本仓库是可执行的：

```text
node tools/console-update-hotfix-recovery-acceptance.cjs
```

它通过 DevTools 协议驱动真实控制台页面，站点数据来自现场实际持有的那几条记录，并从
fixture 的请求日志里读取页面真正 POST 出去的 patch_id。没有浏览器时它报告 NOT RUN，
而不是通过。

---

## 18. HF-05 / 2026-09-30 事故回归基线

必须永久保留以下事实作为回归样本：

```text
HF-05 实际已经 succeeded
↓
错误点击 resume
↓
hotfix runner 立即拒绝 --resume
↓
真实集群未受损
↓
但历史成功记录被覆盖成 failed
```

这证明三件事必须永久防回归：

```text
1. UI subject 与 POST target 不一致
2. hotfix resume routing 不合法
3. operation 覆盖 package deployment state
```

---

## 19. 发布纪律

禁止：

```text
未 push/未 tag 却把包描述为正式远端发布
```

必须区分：

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

任何报告都必须写真实阶段。

---

## 20. 程序读取要求

所有未来自动化程序必须在执行升级相关任务前检查本文存在：

```text
MANDATORY_DOC = docs/upgrade-validation-chain.md
```

如果不存在：

```text
FAIL CLOSED
```

如果本文中的 `VALIDATION_CONTRACT_VERSION` 高于程序支持版本：

```text
FAIL CLOSED
```

禁止静默忽略未知规则。

---

## 21. Machine-Readable Contract

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

## 22. 本仓库各规则的落点

这张表是契约的一部分：一条没有落点、也没有未决义务的规则，就是一条没有被执行的规则。
`tools/verify-upgrade-validation-chain.cjs` 把表里的规则当作代码逐条重查落点。

| 规则 | 落点 | 状态 |
|---|---|---|
| 0.1.1 identity 显式 | `scripts/clusterguard-upgrade.sh` 从签名清单读出 patch id 并校验格式；API 路径为 `/api/v1/platform/updates/{patchID}/{mode}` | 已执行 |
| 0.1.2 subject == 动作对象 | `internal/api/console.html` 的 `softwareUpdateSubject()`；由 Case A–E 在真浏览器里验收 | 已执行 |
| 0.1.3 hotfix 无 resume（路由） | `scripts/clusterguard-update-job.sh` 的 `update_mode_arguments()`；由 `TestUpdateJobRoutesResumeByPackageKind` 钉住 | 已执行 |
| 0.1.3 hotfix 无 resume（后端） | `internal/platformupdate/manager.go` 的 `ErrResumeUnsupported`，映射为 HTTP 409，且在任何文件被写之前返回 | 已执行 |
| 0.1.4 热修重试 = 重跑同一个包 | job wrapper 下发 `--execute --yes`；控制台对同一个 patch id POST `/execute` | 已执行 |
| 0.1.5 滚动升级可续跑 | job wrapper 对非热修下发 `--resume --execute --yes` | 已执行 |
| 0.1.6 同包接管门禁 | `current_update_lock_on_host()` 要求锁的 patch id 与自己相同（`grep -Fqx '${patch_id}'`） | 已执行 |
| 0.1.7 异包拒绝 | `foreign_update_lock_on_host()` / `detect_foreign_update_lock()`；异包持有者会被点名拒绝 | 已执行 |
| 0.1.8 已发布字节不可变 | `scripts/build-hotfix-patch.sh` 拒绝原地覆盖；未交付的那次构建改名留档并写明原因 | 已执行 |
| 0.1.9 同名不同字节 | `hotfixes/hotfix-publications.json` 的 revision 链，由 `tools/verify-hotfix-patch-catalog.cjs` 复核 | 已执行 |
| 0.1.10 历史成功不被覆盖 | — | **未执行 —— 见第 23 节** |
| 0.1.11 状态与历史分离 | — | **未执行 —— 见第 23 节** |
| 0.1.12 Leader 变更不是失败 | `resolve_leader_host()` 在被补丁重启的节点回来后重新解析 Leader，而不是钉死运行前那台 | 已执行 |
| 0.1.13 唯一 Leader / Quorum / Voters | `verify_cluster_idle()` | 已执行 |
| 0.1.14 回退不用 `cp` | 生成的 `rollback.sh` 使用 `mktemp` + `mv -f` | 已执行 |
| 0.1.15 源码 + 产物 + 变异 + 签名 | `scripts/build-hotfix-patch.sh` 与 `tools/verify-hotfix-patch-catalog.cjs`（42 项、无跳过） | 已执行 |
| 0.1.16 门禁阻断发布 | 门禁非零退出；台账门禁在跳过检查时报 `did not run`，而不是通过 | 已执行 |
| 0.1.17 无隐式 `packages[0]` / `latest` | 控制台的 `softwareUpdateSubject()`、`pendingSoftwareUpdate()` 与 `prepareSoftwareUpdateExecution()` | 已执行 |
| 0.1.18 改变 identity 的 fallback 直接失败 | `startSoftwareUpdate()` 对不在列表中的 id 直接返回 false；后端拒绝未知 id | 已执行 |
| 0.1.19 后端重验 kind 与 identity | `internal/platformupdate/manager.go`，以及 job wrapper 读取签名的 `package.json` kind | 已执行 |
| 0.1.20 不得跳过本文 | `tools/verify-upgrade-validation-chain.cjs` 在本文缺失、不可解析或版本高于其支持版本时失败关闭 | 已执行 |
| 5.3 回退携带自己的 subject | 回退 URL 携带 patch id，确认框要求的正是同一个 id | 已执行 |
| 7.2 unknown 不等于 changed | `verify_cluster_idle()` 把观测不到 Leader 报成 `leader_unknown`，只有确实不同的 Leader 才是 `leader_changed` | 已执行 |
| 9 备份清单绑定自己的热修 | 构建器把清单命名为 `backup-<hotfix_id>-<stamp>.txt`，生成的 `rollback.sh` 只 glob 自己的 id | 已执行 |
| 10 `base_commit` 是现场已有的树 | `tools/verify-hotfix-patch-catalog.cjs` 逐提交扫描 `base_commit..build_commit`；`docs/update-and-patch.md` 写明该规则 | 已执行 |

---

## 23. 未决义务

以下规则已经生效，**但尚未实现**。它们被列在这里，并在每次门禁运行时打印出来，以免任何
读者把契约误当成已被完全满足。每条都点名了收口它的卡片。

```text
history.package_state_separate_from_operation_history
  card: UPDATE-OPERATION-HISTORY-P0
  现状：每个包一个 status.json 只保存最新一次尝试，因此后来的尝试会替换先前的结果。
  证据：2026-09-30 13:30 那次被拒绝的 resume 之后，Leader 的 status.json 对一个人
  事件日志完整成功的包写着 resume/failed，而另外两台仍写着 execute/succeeded。

history.append_only_operations
  card: UPDATE-OPERATION-HISTORY-P0
  现状：操作没有按列表记录；只有 events.jsonl 是追加式的。

history.forbid_success_overwrite
  card: UPDATE-OPERATION-HISTORY-P0
  现状：通用规则未执行。造成事故的那条具体路径已封死——后端现在在作业产生之前就拒绝热修
  续跑，被拒绝的尝试什么都不写——但后来一次合法尝试仍可能替换掉先前的成功。
```

`tools/verify-upgrade-validation-chain.cjs` 每次运行都会把它们报成 `OPEN`。加 `--strict`
可让它们使整次运行失败，发布门禁在卡片落地之前应当使用它；默认只报不拦，这样与历史模型
无关的改动不会被契约自己第 23 节记为未完成的工作卡住。

---

## 24. 最终原则

升级系统的目标不是：

> 「脚本最后返回 0」。

而是：

> **每一次变更都拥有明确身份、明确目标、明确状态机、明确锁归属、明确回滚边界、明确证据
> 链，并且任何程序都无法通过默认值、历史覆盖、错误恢复模式或静默 fallback 操作错误的包。**

---

**END OF MANDATORY VALIDATION CONTRACT**
