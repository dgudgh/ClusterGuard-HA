# 热修补丁台账

> 本文件由 `scripts/render-hotfix-catalog.cjs` 依据磁盘上真实存在的签名补丁包生成，
> 请勿手工编辑：重新构建补丁包后重新生成本文件。
> `tools/verify-hotfix-patch-catalog.cjs` 会在“修复提交没有补丁包”或“本文件与产物不一致”时失败。

`.cgupgrade` 携带完整 RPM，只能由滚动升级执行器应用；热修补丁包只携带它声明的那些 bug
修复真正改动的东西：重新构建的二进制、被修改的 systemd 单元、作为证据的源码差异，以及
一对 apply/rollback 脚本。针对已发布版本的每一个 bug 修复都必须被某个补丁包覆盖，否则
现场只能等下一个完整版本才能拿到修复。

补丁按“一次现场处理”打包，不按提交拆分：两个都替换 `/usr/local/bin/clusterguard` 的
补丁如果叠加，结果取决于安装顺序——后装旧的会把新修复盖掉。**只装你所在基线版本的最新一个包，
不要混装不同基线版本的包**——装错基线的包会把二进制悄悄降级回它自己的发布线。

已签名的补丁**永不原地重建**：任何修订都会产生**新身份**（`-r1`、`-r2` …），旧身份的字节
留在目录里，作为“现场到底运行过什么”的唯一记录。所以一个目录里可能同时存在同一个补丁的
多份文件。**下表列出的才是每个补丁应当安装的那一份**，其余身份只是历史。

| 补丁编号 | 严重级别 | 覆盖修复提交 | 构建树 | 应当安装的产物 |
| --- | --- | --- | --- | --- |
| HF-2026-0930-01 | P0 | `9fdb0e7`、`8be2e3d` | `8be2e3d` | `release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch` |
| HF-2026-0929-05 | P0 | `7252ecf`、`c05f8b2` | `c05f8b2` | `release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-05-2.2-105.x86_64.cgpatch` |
| HF-2026-0929-04 | P1 | `39ef673`、`4d80125`、`3a61103`、`d0f63e6`、`d2e5d85` | `d2e5d85` | `release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-04-2.2-105.x86_64.cgpatch` |
| HF-2026-0929-03 | P0 | `de14249`、`7a1ba82`、`ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47`、`9303e9d`、`18d738e`、`81fe3c8`、`ac6f3f7`、`abf5782`、`fce48b7` | `de14249` | `release/2.2-104-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-03-2.2-104.x86_64.cgpatch` |
| HF-2026-0928-02 | P0 | `914c6c5`、`3c88289`、`4015f97`、`0e8ab48`、`f90f995` | `f90f995` | `release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-02-r5-2.2-103.x86_64.cgpatch` |

## 应用补丁

**首选：控制台「版本更新 → 上传升级包」**。上传签名包后由受限 Helper 逐节点应用：热修补丁不改动 RPM 版本，只替换签名清单声明的文件、逐节点核对落地摘要，并按清单重启受影响单元；任一步失败都会用包内 `rollback.sh` 自动回退并保留维护门禁。控制台的包详情会标明该包是「热修补丁」还是「滚动升级」。

**备选：控制节点命令行**。同样受验签与 SHA-256 保护，但**不会自动重启服务**，需按输出自行重启，否则进程仍运行旧代码：

```bash
tar -xzf release/<基线版本>-hotfixes/<产物文件名>.cgpatch
cd clusterguard-hotfix
bash apply.sh            # 备份、校验 SHA-256、安装、daemon-reload
systemctl restart <单元> # apply.sh 只打印需要重启的单元，不自动重启
bash rollback.sh         # 按最新备份清单回滚
```

## HF-2026-0930-01 — 控制台把操作打到了错的升级记录上、给热修提供了一个永远失败的续跑入口，而执行入口又把「热修重试」统一当成「续跑」下发；异包持有维护门禁时也不指名持有者——2026-09-30 13:30 现场「续跑 HF-2026-0929-05 失败」，而那份热修当天 09:30 已经在三台逐文件校验通过

- 严重级别：P0
- 覆盖修复提交：`9fdb0e7`、`8be2e3d`
- 构建树：`8be2e3de1cb0f994b4b0e8990db8b9828293b394`（基线 `c05f8b248bb5689f46ebebc77e900852c71085a9` + 上述修复，不含其它提交）
- 适用版本：2.2-105 → 2.2-105+hf-2026-0930-01（x86_64）
- 产物：`release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch`
- SHA-256：`9a5af9bda61e81f87af686fd551ee9b90d04808d1f160df259cc84f4253b8fcd`
- 产物身份：第 1 修订，替代 `baf16ebca3f7ea5f4003c1669887ffa66d1626e589990c4a6d8e5798168892c3`（`release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0930-01-2.2-105.x86_64.cgpatch`）——首次构建只含控制台与执行入口的拒止，同一现场处理还必须让热修重试真正重执行同一个补丁；而且它的清单把起点记在一个更早的包上，于是另一份补丁的生产修复被算进了本包的范围。在执行入口补齐这条路由之前，该身份的字节从未交付任何现场，因此不修正旧字节，而以新身份重建并取代它。
- 源码差异：`src/HF-2026-0930-01-8be2e3d.patch`
- 交付内容：
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/bin/clusterguard-update-helper` → `/usr/local/libexec/clusterguard-update-helper`（0755）
  - `payload/scripts/clusterguard-update-job.sh` → `/usr/local/libexec/clusterguard-update-job.sh`（0750）
  - `payload/scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750）
- 需要重启：`clusterguard-ha.service`、`clusterguard-update-helper.service`

### 本包概要

本包替换两个二进制：`cmd/clusterguard` → `/usr/local/bin/clusterguard`（控制面，`internal/api/console.html` 与 `internal/api/updates.go` 都编译进它）和 `cmd/clusterguard-update-helper` → `/usr/local/bin/clusterguard-update-helper`（`internal/platformupdate/manager.go` 被两者共同引用）。数据库变更：无。重启单元：`clusterguard-ha.service` 与 `clusterguard-update-helper.service`——因此应用本包期间控制台页面会短暂不可用，这是本包唯一的可感知影响。修复内容：控制台的「滚动升级 / 续跑升级 / 受控回退」三个按钮改为作用于**启用它们的同一条记录**（此前按钮状态取自 `pending` 而提交却回退到 `packages[0]`，实测把 HF-2026-0929-05 的 succeeded 记录覆盖成 failed）；manager 对热修的续跑请求在写任何状态之前直接拒绝；升级记录的状态徽标改为回答「这个补丁到底生效了没有」，并在最近一次尝试是空拒绝时同时说明两件事。 本条修订（revision 1）在执行入口补齐第四与第五处修复，并因此新增两个载荷文件：`scripts/clusterguard-update-job.sh` → `/usr/local/libexec/clusterguard-update-job.sh`（0755 root:root，Helper 每次作业都直接执行它，所以它是「这次到底给升级器下发什么参数」的唯一决定点，没有 restart_unit）与 `scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750 root:clusterguard，除本次新增的具名门禁判定外，与 HF-2026-0929-05 已应用在现场的那份一致）。第四处修复：执行入口决定下发什么参数时只看 mode、不看包的种类，于是「重试一个热修」被翻译成 `--resume`——而热修没有可续跑的 journal，升级器在 `run_hotfix_update` 第一行就拒绝 `--resume`。它现在从自己已经快照好的签名元数据里读 `kind`：热修的 resume 被改写为重新执行同一个补丁并在 output.log 里说明，滚动升级仍然 `--resume`。第五处修复：维护门禁的归属做成具名判定——同一个补丁可以接管自己留下的门禁，另一个补丁持有门禁时被指名拒绝，而不是只报一句「控制面升级门禁未通过」。

### HF-2026-0930-01.1 动作按钮作用于「最新的记录」而不是「启用它的那条记录」——对失败的热修点「续跑」，请求却打到了另一个已经成功的补丁上；「受控回退」同理，会把操作者根本没选中的那个包回退掉（`9fdb0e7`，P0）

- 现象：2026-09-30 13:30:30，控制台「历史升级记录」里 HF-2026-0929-05 一行变成「升级失败 / 续跑升级」，结果栏写「升级任务失败或被阻断：热修补丁不支持 --resume：应用本身是…」。而同一时刻三台 `/usr/local/sbin/clusterguard-upgrade` 摘要一致、`update_maintenance_active=false`、`active_operations=0`：控制台在说一个已经生效的补丁失败了。此前可点的按钮来自失败记录 HF-2026-0929-04（它的状态是 failed），提交出去的动作却落在 HF-2026-0929-05 上。

- 根因：`openSoftwareUpdateConfirmation(mode, patchID = latestSoftwareUpdate()?.package?.patch_id)` 与 `startSoftwareUpdate(..., patchID = latestSoftwareUpdate()?.package?.patch_id)` 的默认值取的是 `packages[0]`，而 `resume-software-update` / `rollback-software-update` 两个监听器调用时**不传** patchID。按钮的显隐与禁用状态却由 `subject = pending || latest`（当时 = 失败的 HF-2026-0929-04）决定。同一段代码上方的注释已经写明「the identity grid, the job status, the action gates and the confirmation id must all describe the package the buttons act on」——前三项改成了 `subject`，确认 ID 这一项漏了。

- 修复：新增唯一解析器 `softwareUpdateSubject() = pendingSoftwareUpdate() || latestSoftwareUpdate()`，身份网格、任务状态、三个动作门禁、目标摘要与两个提交函数的默认 patchID 全部改用它。测试同时钉住旧形状不得回归（`latestSoftwareUpdate()?.package?.patch_id`、`latest && latest.package && latest.package.patch_id`）。

- 何时需要应用：
  - 对任一失败记录点「续跑升级」或「受控回退」，而列表里存在比自己更新的记录
  - 同一条记录已经是 succeeded、而被其它记录占用操作位时的任何动作
  - 现场 2026-09-30 13:30 的「续跑 HF-2026-0929-05 失败」

### HF-2026-0930-01.2 热修的续跑请求被 manager 接受并交给 Helper，于是「必然被拒绝」的尝试被记成了这个补丁自己的结果，把已生效的成功记录覆盖掉（`9fdb0e7`，P0）

- 现象：HF-2026-0929-05 的作业记录从 `mode=execute / status=succeeded / finished_at=01:30:59Z`（事件结尾为 `execute succeeded · all node digests and maintenance release verified`）变成 `mode=resume / status=failed / started_at=finished_at=05:30:30Z`，失败的尝试没有产生任何事件。结果是控制台上的「升级失败」与磁盘上三台一致的载荷长期互相矛盾。

- 根因：`clusterguard-upgrade.sh` 在 `run_hotfix_update()` 的**第一条语句**就 `die "热修补丁不支持 --resume"`（理由是热修幂等、重跑即可），但 `platformupdate.Manager.Start` 对 `ModeResume` 没有任何前置条件，于是它先写 `job` 文件、再启动 Helper；Helper 记录启动失败时按 `helper.go` 的既有逻辑把 `job.PatchID/Mode/Status` 改成 `failed`。拒绝本身是对的，错的是它被写成了补丁的结果。

- 修复：在 `Manager.Start` 里、写 job 文件**之前**拒绝 `mode == ModeResume && softwarePackage.Kind == PackageKindHotfix`，返回新错误 `ErrResumeUnsupported`，由 API 层映射为 409。滚动升级的续跑路径不受影响（逐节点升级半途失败时它仍是唯一出路），两个方向各有一个用例钉住。

- 何时需要应用：
  - 对任意热修补丁（.cgpatch）发起续跑
  - 控制台在失败的热修记录上显示「续跑升级」按钮
  - 任何希望从控制台验证「这个补丁到底装上没有」的操作者

### HF-2026-0930-01.3 升级记录的状态徽标回答的是「最近一次尝试成功了吗」，却被当成「这个补丁生效了吗」显示，并且对热修不提供本来就该提供的重跑路径（`9fdb0e7`，P1）

- 现象：HF-2026-0929-05 在「历史升级记录」中的状态是「升级失败」（danger 徽标），结果栏只写那句续跑拒绝；同一份热补丁在 failed 之后既不能续跑（必然被拒）也没有「重新执行」入口，只有「受控回退」，而回退恰好是当时最危险的动作。

- 根因：`renderSoftwareUpdateHistory` 与详情面板都直接 `softwareUpdateStatusText(job.status)`，而 `job.status` 只描述最近一次尝试；被拒绝的空尝试既不产生事件也不改载荷，却改写了这个字段。同时 `execute-software-update` 的可用条件写死 `['uploaded','planned'].includes(status)`，失败的热修落不进这个集合，于是现场唯一的自动出路消失了——尽管 `run_hotfix_update` 里的 `detect_current_update_lock` 正是为了让「重跑同一个补丁」可行而加的。

- 修复：新增 `softwareUpdateCompletedAttempt(job)`（从 `job.events` 取最近一条终态：succeeded / failed / rolled_back / rollback_failed）与 `softwareUpdateOutcome(job)`：当最近尝试为 failed 而最近完成的是 succeeded 时，徽标显示「已生效 · 本次尝试失败」，结果栏前置「补丁已生效：最近一次完成的执行于 <时间> 逐文件校验通过」。控制台不再对热修显示续跑，改为对「确实没生效」的热修（判据取自 outcome 而非原始 status）显示「重新执行」，走与首次应用相同的 plan→execute。**动作门禁仍一律读原始 status**：被拒绝的尝试不能因为更早的一次成功就显得干净。

- 何时需要应用：
  - 升级记录里任何「最近一次尝试失败、但更早完成的一次成功」的补丁
  - 失败的热修补丁需要重跑同一个补丁时
  - 需要从控制台判断「现场到底在跑哪一版」的任何复核

### HF-2026-0930-01.4 执行入口按 mode 决定下发参数、不看包的种类——「重试一个热修」被翻译成 `--resume`，而升级器第一行就拒绝它，于是重试必然失败，并把拒绝写成这个补丁自己的结果（`8be2e3d`，P0）

- 现象：2026-09-30 13:30 现场对 HF-2026-0929-05 点「续跑升级」，控制台红字「升级任务失败或被阻断：热修补丁不支持 --resume：应用本身是幂等的，直接重新执行同一个补丁即可」，历史行记为「升级失败 · HF-2026-0929-05 · 续跑升级 · 13:30:30 · 192.168.102.153」。而这份补丁的载荷当天 09:30:15→09:30:41 已在三台控制节点逐文件校验通过（01:30:59Z 事件 `succeeded`，`message: all node digests and maintenance release verified`）；13:30:29Z 那条新记录 `mode=resume / status=failed / started_at=finished_at=05:30:30Z` 没有产生任何事件——它根本没有碰到任何一个节点。

- 根因：`scripts/clusterguard-update-job.sh` 决定 `arguments` 时只有一个 `case "${mode}"`：`resume)` 无条件追加 `--resume --execute --yes`。这个脚本要回答的是「给升级器下发什么参数」，而参数并不只由 mode 决定：`--resume` 的含义是从滚动升级持久化的 journal 断点续跑，热修刻意没有这种 journal，`run_hotfix_update` 的第一行就是 `die "热修补丁不支持 --resume"`。判断依据其实**就在手上**——脚本早已把签名元数据 `package.json`（含 `kind`）快照到 `${job_dir}`——但从来没有人读它。控制台那一侧把「续跑按钮」收掉之后这条缺口仍然存在：控制面之外（旧控制面、直接 POST `/api/v1/platform/updates/<id>/resume`、手工调用 `/usr/local/libexec/clusterguard-update-job.sh`）仍然可以把 `--mode resume` 交到升级器面前，而结果是一条被写进记录、看起来像补丁自己失败的拒绝。

- 修复：`package_kind` 取自本脚本已经快照的签名元数据（`jq -r '.kind // "upgrade"' "${job_dir}/package.json"`；没有 `kind` 的老包按滚动升级处理，与 Go 侧一致），路由抽成 `update_mode_arguments` 这个纯函数：`resume` + `hotfix` 输出 `--execute --yes`，并在 stderr 打印一行「热修补丁 … 不支持续跑：应用本身是幂等的，改为重新执行同一个补丁」让它随 output.log 留在现场；`resume` + 其他仍然输出 `--resume --execute --yes`；`plan`/`execute`/`rollback` 不变。抽成函数是为了让这个决定**可以被单独执行**——该脚本需要 root、runuser 与 flock，其余部分都无法就地运行。验收用例 1 与用例 2 直接执行它：`resume/upgrade` 必须产出 `--resume`，`resume/hotfix` 必须不产出。变异验证（`/tmp/mutate-hotfix-retry-routing.py`，含 S0「没咬到」控制与逐字节还原）：把该臂改回无条件的 `--resume`、把 kind 硬编码为 upgrade、把两臂互换、去掉那行告知、把消费改成管道（appends 落进子 shell、标志全丢）——五个变异全部让断言变红。另外 `write_status` 的措辞也按 kind 取值，热修的记录不再自称「滚动升级完成」。

- 何时需要应用：
  - 控制台对失败的热修提供「续跑」入口，或从控制面之外（旧控制面、直接 POST resume、手工调用执行入口）对热修请求续跑
  - 任何「热修重试必然失败，失败原因只出现在日志里，而记录只显示失败」的现场
  - 升级历史里出现 mode=resume 而包的种类是 hotfix 的记录
  - 一条升级记录自称「滚动升级完成」而它其实是一个热修补丁

### HF-2026-0930-01.5 「维护门禁归谁」在热修路径上不可见——异包持有门禁时只报「控制面升级门禁未通过」，操作手无从知道该用哪个补丁重跑（`8be2e3d`，P1）

- 现象：热修失败会刻意保留维护门禁，而门禁只能由同一个补丁接管。可现场能看到的是通用准入失败：`verify_cluster_idle "" false any` 以「控制面升级门禁未通过；以上诊断逐项列出实际违反条件」结束，维护态只是那串违反项里的一行。当门禁属于**另一个**补丁时，操作手拿到的是这条通用拒绝，而唯一能让他往前走的事实——「门禁属于 X，只有重跑 X 才行，重跑本包永远不行」——从未出现。

- 根因：`current_update_lock_on_host()` 只回答一个问题：「这台机器上的锁是不是我的」（`grep -Fqx '${patch_id}'`），所以「锁属于别人」与「没有锁」在它眼里完全一样；热修路径于是直接落到通用准入检查，而那个检查并不知道门禁的归属。同文件里的 `failed_update_lock_on_host()` 与 `detect_recoverable_failed_update()` 覆盖的是「接管一次失败的滚动升级」，两者都要求 marker 的 `.mode == "rolling_update"`，与热修无关——顺带说明一个容易看错的点：热修写进 `/etc/clusterguard/update-maintenance.json` 的 `mode` 也是 `rolling_update`（`acquire_update_locks` 不分包种类），所以 marker 的 `mode` 从来不是区分热修与滚动升级的判据，真正的判据只有 `patch_id`。

- 修复：新增 `foreign_update_lock_on_host()` 与 `detect_foreign_update_lock()`：前者要求锁里的 `patch-id` **不等于**本次补丁、且 `/etc/clusterguard/update-maintenance.json` 自报同一持有者；后者要求全部控制节点一致报同一个持有者，然后 `die` 出双方编号（`维护门禁当前由 ${previous} 持有，与本次补丁 ${patch_id} 不同；热修补丁只能接管自己留下的门禁……`）。它只在 `current_patch_maintenance_active` 为假时才被询问，因此不会把自己残留的门禁也拒掉；它产生的拒绝与通用检查本来就产生的拒绝是同一个（都是拒绝），只是把话说清楚，所以它不会放松门禁。滚动升级路径**刻意不加**这条：那条路径有两个合法接管场景（`recoverable_previous_patch_id` 与自己的锁），按编号一刀切会把「接管一次失败的滚动升级」误杀。验收用例 3 与用例 4 需要在三台控制节点各自的文件系统上留锁，无法离线执行，因此按本文件既有做法在源码层钉住：两把判据必须分别以 `grep -Fqx '${patch_id}'` 与 `!= '${patch_id}'` 锚定，从而构成「我的 / 不是我的」的划分；并钉住热修路径确实调用了它、且在 `acquire_update_locks` 之前。变异验证：去掉 `!=`、去掉 marker 自报持有者、把检查改成无条件、删掉热修路径上的调用、把 `die` 降成 `log`——五个变异全部让断言变红。

- 何时需要应用：
  - 一个热修失败后，现场想用另一个热修补丁往前走：应当被指名拒绝，而不是只报「门禁未通过」
  - 想确认「同一个补丁重跑允许、换一个补丁不行」这条规则是否真的成立
  - 排查「控制面升级门禁未通过」时，需要先知道门禁到底归谁

### 验证

- `sha256sum /usr/local/bin/clusterguard   # 三台必须一致，且等于包内 HOTFIX-MANIFEST.json 中 payload/bin/clusterguard 的 sha256`
- `sha256sum /usr/local/bin/clusterguard-update-helper   # 三台必须一致，且等于清单中 payload/bin/clusterguard-update-helper 的 sha256`
- `systemctl is-active clusterguard-ha clusterguard-update-helper   # 应用完成后两者都必须 active`
- `curl -sk -H 'Authorization: Bearer <token>' https://127.0.0.1:3000/api/v1/control-plane/status | jq -c '{maintenance:.result.update_maintenance_active,active_ops:.result.active_operations,ready:.result.ready}'   # 必须 maintenance:false、active_ops:0、ready:true`
- `ls /etc/clusterguard/update-maintenance.json   # 成功后必须不存在`
- `curl -sk -X POST -H 'Authorization: Bearer <token>' -H 'X-CSRF-Token: <csrf>' -H 'Content-Type: application/json' -d '{"confirmation":"HF-2026-0929-05"}' https://127.0.0.1:3000/api/v1/platform/updates/HF-2026-0929-05/resume   # 必须返回 409 且错误文本为「热修补丁不支持续跑…」；返回后该补丁的作业记录必须原样不变（这是本包最关键的一条：拒绝不得再改写记录）`
- `curl -sk -H 'Authorization: Bearer <token>' https://127.0.0.1:3000/api/v1/platform/updates | jq -r '.result.packages[] | "\(.package.patch_id) \(.job.mode) \(.job.status)"'   # HF-2026-0929-05 一行必须仍为 execute succeeded（应用本包之后新发起的续跑不得再改写它）`
- `浏览器：设置 → 版本更新 → 历史升级记录   # HF-2026-0929-05 一行必须显示「已生效 · 本次尝试失败」，结果栏以「补丁已生效：最近一次完成的执行于 … 逐文件校验通过」开头`
- `浏览器：同上   # 选中失败的热修补丁时不得出现「续跑升级」按钮；确实未生效的热修必须出现「重新执行」，且确认对话框中要求输入包 ID、文案为「确认重新执行」`
- `浏览器：同上   # 若把操作位切到另一条记录，按钮文案与随后的确认对话框必须描述同一条记录（这是本次事故的直接回归项）`
- `grep -n 'update_mode_arguments' /usr/local/libexec/clusterguard-update-job.sh   # 必须出现：执行入口按包种类分派下发参数`
- `grep -c '不支持续跑' /usr/local/libexec/clusterguard-update-job.sh   # 必须 ≥1：热修续跑被改写为重新执行，并说明改写`
- `jq -r '.kind' /var/lib/clusterguard/updates/HF-2026-0930-01/package.json   # 必须为 hotfix：执行入口读的就是这份签名元数据`
- `jq -r '.base_commit,.build_commit,(.fix_commits|join(" "))' /var/lib/clusterguard/updates/HF-2026-0930-01/package.json   # 清单声称的范围内，除本包 fix_commits 之外不得有触及生产路径的提交；本包首次构建与 revision 0 都把基线记成了更早那个包的构建树，于是 HF-2026-0929-05 的生产修复被算进本包的范围，门禁当场判红。基线已收窄到 c05f8b2（上一个已交付补丁的构建树），范围内除 9fdb0e7 与 8be2e3d 外只剩测试、门禁与文档提交`
- `grep -c 'detect_foreign_update_lock' /usr/local/sbin/clusterguard-upgrade   # 必须 ≥3（定义 + 门禁判据里的调用 + 热修路径上的调用）`
- `实测（正向）：对一个已生效的热修点「重新执行」→ 必须走 plan → execute，输出里出现「跳过已达到目标内容的节点」，并且**不得**出现「热修补丁 … 不支持续跑」`
- `实测（异包）：在三台控制节点各留一把属于另一个补丁的 /var/lib/clusterguard-update-private/history/.cluster-update.lock 与对应的 /etc/clusterguard/update-maintenance.json，再执行本包 → 必须被指名拒绝（维护门禁当前由 <另一个补丁> 持有），且不得修改任何文件`
- `本次是「含二进制的热修包」，因此同时执行 HF-2026-0929-05 验收清单里那条下一步验收：逐节点应用后不得再出现 leader_changed，失败时自动回退必须成功、门禁必须被正常释放`

### 回滚

执行 rollback.sh 恢复旧的 /usr/local/bin/clusterguard 与 /usr/local/bin/clusterguard-update-helper，然后 systemctl daemon-reload 并按清单重启 clusterguard-ha.service 与 clusterguard-update-helper.service。回滚会把本次修掉的三处行为带回来：(1) 动作按钮重新作用于 packages[0] 而不是启用它的那条记录，于是对失败记录点「续跑」会再次打到最新那个补丁上，「受控回退」会回退操作者没有选中的包——后者会真的把一份已生效的载荷改回旧版本；(2) manager 重新接受热修续跑，于是那次必然被拒绝的尝试会再次把该补丁自己的成功记录覆盖成 failed；(3) 升级记录徽标重新只反映最近一次尝试，已生效的补丁会被显示成「升级失败」，同时失败的热修重新失去「重新执行」入口。注意本包不取代 HF-2026-0929-05：那份热修只替换 /usr/local/sbin/clusterguard-upgrade，与本包的两个载荷没有交集，回滚本包不会也不能把它带走。 本修订新增两个载荷文件，回滚同样会恢复它们：`/usr/local/libexec/clusterguard-update-job.sh` 与 `/usr/local/sbin/clusterguard-upgrade`（后者除本次新增的具名门禁判定外与 HF-2026-0929-05 的载荷一致）。回滚会把本次修掉的第四处与第五处行为带回来：(4) 执行入口重新只看 mode，于是任何对热修的续跑请求——旧控制面、直接 POST resume、手工调用执行入口——都会再次被下发成 `--resume`，在升级器第一行被拒绝，再把拒绝写成这个补丁自己的结果；`update_mode_arguments` 消失；热修的记录重新自称「滚动升级完成」。(5) 异包持有维护门禁时重新只报通用的「控制面升级门禁未通过」，不再指名持有者。注意回滚**不会**把 HF-2026-0929-05 的载荷带回旧版本，也不会撤销控制台的记录修复：本包与它改的是不同的文件。

### 该补丁的身份历史

上面一节描述的是 `clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch`。下列身份按不可变规则留在交付目录里，
**不是安装入口**，只作为「现场到底运行过什么」或「本补丁早先构建成了什么」的证据。
完整时间线见 `hotfixes/hotfix-publications.json`。

- **首次发布** `baf16ebca3f7ea5f4003c1669887ffa66d1626e589990c4a6d8e5798168892c3`（8,735,057 字节，已被取代）

## HF-2026-0929-05 — 2.2-105 交付线：热修补丁自己的失败路径把现场锁死——逐节点应用后仍把 Leader 钉在应用前、回退用 cp 就地覆盖运行中的二进制（ETXTBSY）、失败之后既不能续跑也不能回退

- 严重级别：P0
- 覆盖修复提交：`7252ecf`、`c05f8b2`
- 构建树：`c05f8b248bb5689f46ebebc77e900852c71085a9`（基线 `d2e5d850e20d432b92e0c04f963a8381b0044606` + 上述修复，不含其它提交）
- 适用版本：2.2-105 → 2.2-105+hf-2026-0929-05（x86_64）
- 产物：`release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-05-2.2-105.x86_64.cgpatch`
- SHA-256：`259c23646eaac488da5bad24d7c8ed98dac95046c40b22d379dd85ba03d03bb6`
- 源码差异：`src/HF-2026-0929-05-c05f8b2.patch`
- 交付内容：
  - `payload/scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750）
- 需要重启：无

> **本身份已冻结。** 它是现场实际执行过的那一份，作为证据保留，**不是该交付线的上传入口**。
> 它仍带着下列缺陷，且不会按今天的规则回炉重造——那会毁掉这份证据。

### 本包概要

本包只替换一个运行时脚本：`scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750 root:clusterguard）。**它不含任何二进制，因此清单里没有任何 `restart_unit`，应用过程不会重启任何服务**——这不是省事，而是本包必须成立的前提：本包修的正是「热修重启节点会把流程自己打翻」，而本次执行的仍然是现场已装的那份旧脚本，所以只有让本次应用根本不重启，它才能在踩到那个缺陷之前先把修复装上去。安装完成后，下一次热修（以及未来任何会重启节点的包）才真正受这条修复保护。本包依附 HF-2026-0929-04 的构建树（`base_commit = d2e5d85`）——现场已经应用过 HF-04 并正在运行它的载荷，本包是它的增量，不取代它，也不重复交付它的二进制。本包覆盖 2026-09-29 16:22 现场的第三次事故：在滚动升级到 2.2-105 成功、HF-04 也已应用之后，操作手又执行了一次同一个热修包，这一次它失败了，并且**自己把自己锁死**。三条缺陷是一条链：① 热修按 `ordered_nodes` 逐台应用，而 Leader 被刻意排在最后；每台应用完都要等控制面恢复一致，而这个等待把「应用前那台 Leader」当成必须不变的期望——最后那台恰恰就是 Leader，重启它必然触发重新选举，于是 60 秒等待必然超时（现场 `leader_changed`），作业被判失败。② 判失败后走自动回退，而生成的 `rollback.sh` 用 `cp` 就地覆盖目标文件；被替换的文件里包含正在运行的二进制，就地写入必然 `ETXTBSY`（现场三台全部回退失败）。③ 回退失败的设计后果是保留维护门禁，而门禁只能由「重跑同一个补丁」接管——可热修路径在 `detect_current_update_lock` 之前就 `exit` 了，`current_patch_maintenance_active` 永远是 false，重跑又被「必须没有维护态」这条准入检查拒绝。三条合起来，现场没有任何一条自动路径可走，只能改文件系统解封（08:24:47 记录 `automatic rollback incomplete; maintenance gate retained`，实际处置是 16:42 备份 `updates/` 记录、在 Leader 上调 `gate/release`、三台改名移走锁与 `/etc/clusterguard/update-maintenance.json`）。顺带修掉第四条同源缺陷：`rollback.sh` 此前取「最新的」`backup-*.txt`，而不是本补丁自己的清单——`restore_backup()` 把「清单里没有这条记录」解释成「补丁前不存在」并直接删除目标文件，所以一旦取错清单，它会删除在役的系统文件。现在备份清单名带上补丁编号，回滚只认本补丁的清单。

### HF-2026-0929-05.1 热修逐节点应用后仍要求 Leader 是应用前那一台——被重启的节点恰好就是 Leader 时，60 秒等待必然超时并触发回退（`7252ecf`，P0）

- 现象：2026-09-29 16:22 现场执行 HF-2026-0929-04，逐台应用看似正常，随后在 `.154` 上报 `hotfix failed; automatic rollback started`，`status.json` 最终停在 `failed` / `phase: rollback` / `current: 0`。日志里的判据是 `leader_changed`：控制面在应用期间发生过一次 Leader 变更，而流程要求它不能变。三台 `clusterguard-ha` 都在 16:23 前后重启过一次——那是本次应用本身做的。

- 根因：`run_hotfix_update` 按 `ordered_nodes` 逐台应用，Leader 被刻意排在最后（`for host in controllers; do [[ host == leader_host ]] || ordered_nodes+=…; done; ordered_nodes+=leader_host`）。每台应用完都执行 `if ! wait_cluster_idle "${leader_host}" true idle`——把「应用开始前解析出来的那台 Leader」当作**必须保持不变**的期望传进去。`verify_cluster_idle` 在 `expected_leader` 非空时会比对 `observed_leader`，不等就判 `leader_changed`。热修是要重启被处理节点的（`apply_hotfix_on_node` → `restart_hotfix_units`），而最后一台就是 Leader：重启它必然触发重新选举，`observed_leader` 由此变成别的节点，于是这个等待在**先成功后失败**的路径上必然超时（现场 60 秒）。同一个 `leader_host` 还被 `acquire_replicated_update_gate`（`:802`）与 `release_replicated_update_gate`（`:809`）用来找 Leader 下发门禁，所以缓存在应用前的值还会让门禁释放打向一台已经不是 Leader 的节点——这是同一条缺陷的第二处隐患。

- 修复：新增 `resolve_leader_host()`：只问「现在谁持有领导权」这一个窄问题——遍历控制节点取各自的 status，第一个报 `role == leader` 的即为答案并写入 `leader_host`，与缓存值不同时打印一行变更日志；它不做 `verify_cluster_idle` 那一整套成员/活动校验，因此不会因为无关不变式而 `die`（`verify_cluster_idle` 顺带推导同一件事，但它是被用于等待的，不能在流程中途被这样询问）。逐节点等待改为 `wait_cluster_idle "" true idle`——只要求维护态成立且集群空闲，不再对 Leader 身份提要求；随后立即 `resolve_leader_host`，让 `finish_update_maintenance` 里的门禁释放指向**当前**的 Leader。准入阶段那次 `wait_cluster_idle "${leader_host}" true idle`（`acquire_update_locks` 之后、任何节点被改动之前）保持不变：那时还没有重启，要求 Leader 稳定是合理的。断言：`TestHotfixFlowTreatsLeaderMovementAsExpectedAfterRestart` 同时钉住 `resolve_leader_host` 的存在与实现（必须问逐个控制节点、必须把应答者写回 `leader_host`）、`run_hotfix_update` 必须含 `wait_cluster_idle "" true idle`、必须含 `resolve_leader_host`，且**不得**再出现 `if ! wait_cluster_idle "${leader_host}" true idle`。变异验证：把该处换回 `"${leader_host}"` 时断言变红。

- 何时需要应用：
  - 热修执行到最后一个节点之后失败，日志里出现 leader_changed 或「控制面升级门禁未通过」，而每个节点其实都已经应用成功
  - 热修应用期间控制面发生重启，随后等待超时
  - 升级列表里最后一个节点恰好是 Leader 的热修包

### HF-2026-0929-05.2 失败的热修无法接管自己残留的维护门禁——检测代码在热修路径上永远到不了，于是续跑被自己的门禁拒绝、回退又被 ETXTBSY 挡住，现场没有任何自动出路（`7252ecf`，P0）

- 现象：上一条的失败把维护门禁留在了三台控制节点上。此后现场做任何一件事都撞墙：重跑同一个补丁被「必须没有维护态」拒绝；受控回退在同一份门禁下也走不通；控制台横幅显示「升级维护未闭环 · 维护门禁仍生效 / 自动故障切换已暂停」，而对话框给出的建议是「确认维护门禁状态后再续跑或回退」——两条路都指向一个走不通的动作。最终只能人工作业：备份 `updates/` 下的记录、在 Leader 上调 `gate/release`、三台改名移走 `.cluster-update.lock` 与 `/etc/clusterguard/update-maintenance.json`。

- 根因：门禁的接管路径本来就有，只是热修走不到。`detect_current_update_lock()` 会遍历控制节点、在**全部**节点都带本补丁的维护锁时把 `current_patch_maintenance_active` 置为 true；`acquire_update_locks` 见到它为真就调 `adopt_current_update_locks` 接管已有锁；`build_order`（滚动升级路径）也据此放宽维护态期望。但主流程里 `detect_current_update_lock` 的调用在 `:2086`，而热修分支在 `:2037` 就 `run_hotfix_update; exit 0` 了——**热修路径从未调用它**，`current_patch_maintenance_active` 恒为 false，接管分支永远不可达。与此同时 `run_hotfix_update` 的准入检查是无条件的 `verify_cluster_idle "" false any`：要求维护态为 false。两者合起来正好把唯一的补救动作挡在门外——门禁只能靠重跑接管，而重跑要求门禁不存在。

- 修复：在热修流程里、`load_nodes` 之后（任何准入检查之前）调用 `detect_current_update_lock`，让同一条接管路径对热修也可达；准入检查据此分支：`current_patch_maintenance_active` 为真时用 `verify_cluster_idle "" true any` 接受本补丁自己的门禁（随后由 `acquire_update_locks` → `adopt_current_update_locks` 接管），否则维持原来的 `verify_cluster_idle "" false any`。回退分支（`rollback_requested`）不变。断言：`TestHotfixFlowCanAdoptItsOwnMaintenanceGate` 要求 `run_hotfix_update` 里出现 `detect_current_update_lock` 的**调用语句本身**（`\n  detect_current_update_lock`，而不是函数名出现在别处或注释里——这一点是被变异验证逼出来的：早期版本只断言名字包含，结果把调用换成注释仍然通过）、要求以 `${current_patch_maintenance_active}` 分支、要求出现 `verify_cluster_idle "" true any`，并反向钉住 `acquire_update_locks` 仍然含 `adopt_current_update_locks`，防止两半漂移。变异验证：删掉该调用时断言变红。

- 何时需要应用：
  - 热修失败后控制台一直显示「升级维护未闭环 / 维护门禁仍生效」，自动故障切换被暂停
  - 重跑同一个热修包被拒绝，提示要求先确认维护门禁状态
  - 受控回退也走不通，只能上文件系统改锁文件

### HF-2026-0929-05.3 生成的 rollback.sh 用 cp 就地覆盖运行中的二进制——ETXTBSY，回退在任何节点都不可能成功，门禁因此必然被保留（`c05f8b2`，P0）

- 现象：上一条的自动回退在三台节点上全部失败：`.154` 08:24:36、`.153` 08:24:39、`.152` 08:24:42，每条都是「restoring the files this hotfix replaced」之后没有下文，08:24:45 汇总为 `rollback_failed` / `automatic rollback incomplete; maintenance gate retained`。「应用成功、回退失败」这个组合本身就是线索：只有回退这一侧失败，说明差异不在文件内容，而在**写文件的方式**。

- 根因：生成器给 `rollback.sh` 的 `restore_backup()` 写的是 `cp -p "${backup}" "${destination}"`，也就是**就地覆盖**目标文件。而 `apply.sh` 那一侧用的是 `install -m <mode> -o <owner> -g <group> "${here}/${artifact}" "${destination}"`——GNU `install` 会先 unlink 目标再创建，所以应用从不 ETXTBSY，回退却**每次都会**：热修替换的文件里就包含正在运行的 `/usr/local/bin/clusterguard` 与 `/usr/local/libexec/clusterguard-update-helper`，对一个正在被执行的文件做截断写入会得到 ETXTBSY。结果是「安装能成功、回退永远不能成功」，而回退失败的设计后果是保留维护门禁——也就是说，这条缺陷把任何一次热修失败都变成需要人工解封的现场。

- 修复：生成的 `restore_backup()` 改为换 inode：`restore_tmp="$(mktemp "${destination}.restore.XXXXXX")"` → `cp -p "${backup}" "${restore_tmp}"` → `mv -f "${restore_tmp}" "${destination}"`。临时文件与目标同目录（因此同文件系统，`mv` 是原子的改名），运行中的进程继续持有旧 inode，不受影响；`cp -p` 仍保留备份文件的 mode 与时间戳。断言分两层：源级 `TestGeneratedRollbackReplacesFilesBySwappingTheInode` 钉住生成器里必须同时出现 mktemp、写入临时文件、`mv -f` 三步，并**禁止**出现 `cp -p "${backup}" "${destination}"`；产物级门禁在真实 `.cgpatch` 解出的 `rollback.sh` 上复查同样两条（必须含 `mv -f "${restore_tmp}"`、不得含就地覆盖）。变异验证：把 `mv -f` 换回 `cp -p` 时源级断言变红，门禁对历史产物也当场变红（这条门禁一加上就抓出了 HF-2026-0928-02 / -03 / -04 三个旧包的回滚同样是坏的）。

- 何时需要应用：
  - 热修失败后自动回退也没有成功，日志停在「restoring the files this hotfix replaced」
  - 回退报 ETXTBSY 或文本文件忙
  - 任何「应用成功、回退失败」的组合，都先怀疑回退是就地覆盖而不是换 inode

### HF-2026-0929-05.4 生成的 rollback.sh 接受「最新的」备份清单——可能恢复别的补丁的文件，甚至把清单里没提到的在役文件直接删掉（`c05f8b2`，P1）

- 现象：回退脚本自己选清单：`backup_list="$(ls -1 "${backup_dir}"/backup-*.txt | tail -1)"`。`/var/lib/clusterguard/hotfix/` 是**所有**热修共用的备份目录（现场在 11:49 那次处理后就留下了整套 11:49 的备份清单），因此回退哪一次补丁、恢复哪些文件，取决于「谁的文件名排序最后」，而不是本补丁声明了什么。上一条的 ETXTBSY 恰好掩盖了这条：`set -euo pipefail` 让脚本在第一个文件就退出，后面的文件根本没被处理；一旦上一条修好、回退真的跑得完，这条就会开始生效。

- 根因：`apply.sh` 写的清单名是 `backup-${stamp}.txt`，只带时间戳、不带补丁编号；`rollback.sh` 于是只能用「取最新」来猜。而 `restore_backup()` 对「清单里找不到这个目标路径」的处理是 `rm -f "${destination}"`，理由是在别的补丁的清单里查不到，并不等于「本补丁之前它不存在」——这正是「补丁前不存在」这一合法状态的判断依据被张冠李戴。取错清单的后果因此有两个方向：恢复成别的补丁备份的旧文件（静默降级，无任何报错），或者把一个本补丁根本没碰过的在役文件删掉。

- 修复：把清单名绑定到补丁编号：`apply.sh` 写 `backup-${hotfix_id}-${stamp}.txt`，`rollback.sh` 只匹配 `backup-<本补丁 id>-*.txt`，取不到就按既有的「没有找到备份清单，无法回滚」退出，而不是退回任意清单。断言：源级 `TestGeneratedBackupManifestIsBoundToItsOwnHotfix` 两处分开钉（生成器里 apply 的清单名必须带 `backup-${manifest.hotfix_id}-${stamp}`、rollback 的匹配必须带 `/backup-${manifest.hotfix_id}-*.txt`），并禁止出现裸 `backup-*.txt`——分开钉是被变异逼出来的：只断言「出现 hotfix id 片段」时，改掉其中一处仍然通过。产物级门禁在真实 `apply.sh` 与 `rollback.sh` 上复查同样两条。

- 何时需要应用：
  - 同一台机器上先后做过多次热修，现在要回退其中一次
  - 回退之后发现某个文件变成了更早的版本，而本次补丁并没有替换它
  - 回退脚本删掉了一个本补丁从未涉及的文件

### 验证

- `sha256sum /usr/local/sbin/clusterguard-upgrade   # 必须等于包内 HOTFIX-MANIFEST.json 中 payload/scripts/clusterguard-upgrade.sh 的 sha256`
- `ls -l /usr/local/sbin/clusterguard-upgrade   # 必须仍为 0750 root:clusterguard`
- `jq -r '.files[].restart_unit // "(none)"' /root/<解包目录>/clusterguard-hotfix/HOTFIX-MANIFEST.json   # 必须全部为 (none)：本包不含二进制，应用不重启任何服务`
- `systemctl is-active clusterguard-ha   # 应用全程必须始终 active：本包不重启控制面，不应出现重启窗口`
- `grep -c 'resolve_leader_host' /usr/local/sbin/clusterguard-upgrade   # 必须 ≥2（定义 + 热修循环里的调用）：逐节点应用后重新解析 Raft Leader`
- `grep -c 'wait_cluster_idle "" true idle' /usr/local/sbin/clusterguard-upgrade   # 必须 ≥1：逐节点等待不得再钉死应用前的 Leader`
- `bash -c '! grep -q "if ! wait_cluster_idle \"\${leader_host}\" true idle" /usr/local/sbin/clusterguard-upgrade'   # 必须成功：被取代的旧形状不得残留`
- `bash -c '! grep -q "^  detect_current_update_lock$" /usr/local/sbin/clusterguard-upgrade || echo present'   # 热修路径必须已能检测本补丁残留的维护门禁`
- `bash /root/<解包目录>/clusterguard-hotfix/rollback.sh   # 生成的 rollback.sh 必须用 mktemp + mv -f 换 inode 恢复，且只认 backup-HF-2026-0929-05-*.txt`
- `/usr/local/sbin/clusterguard-upgrade --patch <本包> --trust-key /etc/clusterguard/trust/patch-signing-public.pem --inspect   # signature=verified、kind=hotfix、database_mutation=false、rollback=available`
- `curl -sk -H 'Authorization: Bearer <token>' https://192.168.102.155:3000/api/v1/control-plane/status | jq -c '{maintenance:.result.update_maintenance_active,active_ops:.result.active_operations}'   # 应用后必须 maintenance:false、active_ops:0`
- `ls /etc/clusterguard/update-maintenance.json   # 应用成功后必须不存在：门禁已释放，控制台横幅不得再出现`
- `下一步验收（本包的目的）：再执行任意一个含二进制的热修包，逐节点应用后不得再出现 leader_changed，且失败时自动回退必须成功、门禁必须被正常释放`

### 回滚

执行 rollback.sh 恢复旧的 /usr/local/sbin/clusterguard-upgrade，然后 systemctl daemon-reload。本包不含任何二进制、不含 systemd 单元、不含安装器，所以回滚不需要重启任何服务，也不会影响正在运行的控制面与 Helper。回滚后会被带回的行为共四处：热修逐节点应用后重新钉死应用前那台 Leader，于是当被重启的最后一个节点就是 Leader 时，等待会再次超时并触发自动回退；热修路径重新无法接管自己残留的门禁，失败后既不能续跑也不能回退，只能像 2026-09-29 16:42 那样上文件系统解封；此后新生成的 rollback.sh 重新用 cp 就地覆盖运行中的二进制（ETXTBSY），回退在任何节点都不会成功；并且重新接受「最新的」备份清单，可能恢复别的补丁的文件或删除在役文件。注意本包不取代 HF-2026-0929-04：HF-04 的载荷（控制面、Helper、update-job 脚本）已经应用在现场并在运行，回滚本包不会也不能把它们带回旧版本——本包只改那份决定「热修怎么执行」的运行时脚本。

## HF-2026-0929-04 — 2.2-105 交付线：控制台无法判断「这个包能不能装在本集群上」——跨基线的升级包占住唯一可执行槽、终态记录锚死按钮、失败原因指向根本不存在的维护门禁

- 严重级别：P1
- 覆盖修复提交：`39ef673`、`4d80125`、`3a61103`、`d0f63e6`、`d2e5d85`
- 构建树：`d2e5d850e20d432b92e0c04f963a8381b0044606`（基线 `bf2feeb070948599d054e66670ada3f29ff8ee25` + 上述修复，不含其它提交）
- 适用版本：2.2-105 → 2.2-105+hf-2026-0929-04（x86_64）
- 产物：`release/2.2-105-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-04-2.2-105.x86_64.cgpatch`
- SHA-256：`78a0b623977a060d157c35a267c67af26cd96c514526d1d2b1f3b4e86999d126`
- 源码差异：`src/HF-2026-0929-04-d2e5d85.patch`
- 交付内容：
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/bin/clusterguard-update-helper` → `/usr/local/libexec/clusterguard-update-helper`（0755）
  - `payload/scripts/clusterguard-update-job.sh` → `/usr/local/libexec/clusterguard-update-job.sh`（0750）
- 需要重启：`clusterguard-ha.service`、`clusterguard-update-helper.service`

> **本身份已冻结。** 它是现场实际执行过的那一份，作为证据保留，**不是该交付线的上传入口**。
> 它仍带着下列缺陷，且不会按今天的规则回炉重造——那会毁掉这份证据。

- 已知缺陷：HF-2026-0929-04:rollback.sh 未用改名换 inode 恢复文件（就地写运行中的二进制会 ETXTBSY）
- 已知缺陷：HF-2026-0929-04:rollback.sh 直接覆盖目标文件（运行中的二进制会 ETXTBSY）
- 已知缺陷：HF-2026-0929-04:apply.sh 未把备份清单绑定到本补丁的 hotfix id
- 已知缺陷：HF-2026-0929-04:rollback.sh 未把备份清单绑定到本补丁的 hotfix id
- 已知缺陷：HF-2026-0929-04:rollback.sh 接受任意备份清单，可能恢复别的补丁的文件或误删文件

### 本包概要

本包替换两个二进制——`/usr/local/bin/clusterguard`（控制面，`internal/api/console.html` 由 `go:embed` 嵌进它）与 `/usr/local/libexec/clusterguard-update-helper`（受限升级 Helper）——外加一个运行时脚本 `/usr/local/libexec/clusterguard-update-job.sh`。两个二进制都必须重建，是因为它们都链接 `internal/platformupdate`（用 `go list -deps ./cmd/clusterguard-update-helper` 可复核），而本包有修复落在那两个包里；Helper 自身行为不变，变的只是它内嵌的那份包代码。运行时脚本由 Helper 在每次起作业时读取，所以它不需要单独重启。本包不改 RPM 版本、不动任何 systemd 单元、不动 agent，也不出现安装器。**这是 2.2-105 交付线的包**：2.2-105 的 `.cgupgrade` 会整包替换这两个二进制，所以必须在滚动升级到 2.2-105 之后应用；先装本包再升级，本包会被升级包覆盖（含这个运行时脚本）。同理，2.2-104 线的 HF-2026-0929-03 不要用在 2.2-105 站点上，反之亦然。本包覆盖 2026-09-29 同一天在同一现场暴露的五个缺陷，它们共同的主线是一句话：**控制台无法判断「这个包能不能装在本集群上」**。它只按一个维度评估记录——时间轴上的作业状态（新不新、失败过没有），而这两个事故的失败都出在另一个维度：包与集群的关系（`source_version` 与集群当前版本是否一致）。前两个缺陷是第一次事故（13:16 上传一个已经修好的 2.2-105 包，按钮却点不动）：`Manager.Snapshot` 按 `uploaded_at` 倒序返回，而控制台把「待升级目标版本」「身份栅格」「按钮是否可用」三件事都锚在列表首行；首行是一条 `uploaded_at` 写在节点时钟快约 5 小时 39 分时的记录（`HF-2026-0928-06`，`2026-09-29T11:01:17.712051173Z`），它挂着 `succeeded` 的 job，于是「待升级包」判定为无、按钮被 `!pending` 永久关掉，而校验面板仍按上传结果显示绿色的「校验完成」——上下两半在同一屏里互相矛盾，操作员拿到一个验签通过却点不动的包，页面上没有一句话说明原因。后三个缺陷是当天 14:02 的第二次事故，也是更根本的那一个：为了给 2.2-105 备好修复，现场把属于 2.2-105 线的热修补丁 `HF-2026-0929-04` 上传到了**仍停在 2.2-104** 的集群。上传顺利（签名通过就收下），它随即占住控制台唯一的可执行槽；点「计划」后升级器按设计拒绝，但这次拒绝不改变任何一条已经做出的判定，于是失败记录**不退位**，而真正该执行的 `cgupgrade-2.2-104-to-2.2-105-x86_64` 排在被它压住的位置、从界面上无法选中。三条修复分别对应：上传口按升级器自己的口径拒绝跨基线包（被拒的包不留任何记录）、快照给已有记录打 `incompatible` 并让前台把它排除出可执行候选、失败作业把升级器写在输出里的真实原因带给操作员而不是继续指向一个不存在的「维护门禁」。

### HF-2026-0929-04.1 控制台把「待升级目标」与「滚动升级」锚在升级记录首行，首行是已完成记录时按钮永久灰死且页面不说原因（`39ef673`，P1）

- 现象：2026-09-29 13:16，现场在「设置 → 版本更新 → 升级」对话框里上传 `cgupgrade-2.2-104-to-2.2-105-x86_64`，上传成功、历史记录里出现「已上传」一行，但下方按钮始终是灰的、点不动；面板上是绿色的「✓ 校验完成 / 升级包已通过签名与兼容性校验。」。截图即为现场所见：绿勾在上、灰按钮在下。页面上没有任何文字说明为什么不能点，操作员只能重传、换浏览器、怀疑包本身有问题。

- 根因：`Manager.Snapshot` 按 `uploaded_at` 倒序返回 packages，控制台此前用 `latestSoftwareUpdate()`（就是 `packages[0]`）同时决定三件事：待升级目标版本、身份栅格（包 ID / 版本 / 是否可滚动 / job 状态），以及按钮是否可用。现场 `HF-2026-0928-06` 的 `uploaded_at` 是节点时钟快约 5 小时 39 分时写下的（`2026-09-29T11:01:17.712051173Z`），所以它排在 13:16 刚上传的 2.2-105 包之上；它的 job 是 `succeeded`，于是 `pendingSoftwareUpdate()` 返回 null，按钮被门禁里的 `!pending` 关掉，`subject` 退化成那条已完成的热修补丁（`rolling:false`、`status:succeeded`），身份栅格与门禁因此都在描述一个根本不该被执行的记录。而校验面板不看 `pending`，只看上传结果，所以继续显示「校验完成」——上下两半在同一屏里互相矛盾。

- 修复：判定改为「扫列表找最新一条仍可执行的记录」：新增 `softwareUpdateActionable`（无 job、或 job 仍待核验、或状态不是终态 `succeeded`/`rolled_back`），`pendingSoftwareUpdate()` 用它扫全列表而不是取首行；身份栅格、job 面板、三个按钮的门禁、`prepareSoftwareUpdateExecution` 与 `openSoftwareUpdateConfirmation` 统一以 `pending || latest` 为唯一的 subject，确认框里要手输的包 ID 也随之指向刚上传的那个包。新增 `verified-blocked` 状态：上传已验签但列表里没有可执行记录时，面板明确写「校验完成，但当前没有可执行的升级包」并点名排在最新、占住列表的那条记录，说明它已执行完成、不能重复发起；摘要行相应改为「最近完成版本 / 最近处理版本」，不再把已完成的记录叫作「待升级目标版本」。历史行在记录 `clock_skew` 为真时追加「· 记录时间戳晚于当前时间，排序与时间不可信」。断言分两层：Go 契约测试断言新实现并反向拒绝旧形状（不允许再从 `packages[0]` 取 subject、不允许把 `clock_skew` 读深一层——`clock_skew` 在记录包装层，不在它内嵌的 `package` 上）；新增真浏览器验收 `tools/console-update-pending-acceptance.cjs`（复用仓库自研的 DevTools-protocol 运行器 `tools/console-cdp-harness.cjs`，无 playwright 依赖，三场景 25 项），在真实页面上走控制台自己的 change 监听器、multipart 上传、刷新与只读计划请求，断言未来时间戳下的上传必须点亮按钮、确认框必须写刚上传的包 ID、无可执行记录时必须显示 blocked 文案并点名、正常上传路径不受影响、且全程没有 Runtime.exceptionThrown。变异验证 4 项（subject 退回 `packages[0]`、身份栅格退回首行、`clock_skew` 读深一层、去掉 blocked 推导）全部被判红，还原后 `console.html` 与 SHA-256 逐字节一致。

- 何时需要应用：
  - 上传并校验成功后「滚动升级」按钮灰着、点不动，历史记录里却明明有「已上传」的包
  - 升级记录里最新一条是「升级成功 / 已回退」之类的终态记录，而刚上传的包排在它下面
  - 想确认「待升级目标版本」显示的是不是真的会被执行的那个包
  - 升级记录的时间戳看起来不对（比当前时间晚），需要判断排序还可不可信

### HF-2026-0929-04.2 服务端不区分写在未来的记录，把不可信的排序当权威下发，控制台无从判断（`4d80125`，P2）

- 现象：`GET /api/v1/platform/updates` 只回记录本身的时间。当某台控制节点的时钟曾经快过，那段时间写下的记录会被永久钉在列表顶部：现场表现为 `HF-2026-0928-06` 的时间看起来像刚刚、实际是 5 小时 39 分之后的未来时间，而控制台只能照单全收，把它当成「最新」。

- 根因：`PackageStatus` 只承载记录本身，缺少「这条记录的 `uploaded_at` 相对本节点时钟处于未来」这一事实。而 `uploaded_at` 同时是排序键和展示值：一旦它来自一个与本节点不一致的时钟，排序和展示就同时失真，且这个失真在接口层完全不可见——控制台没有依据去怀疑首行。

- 修复：在 `PackageStatus` 上新增 `clock_skew`（`json:"clock_skew,omitempty"`），凡是 `uploaded_at` 超出本节点时钟 `clockSkewTolerance = 5m` 的记录即置位，把「这条记录诞生于一个与本节点不一致的时钟」随行下发；控制台据此在历史行上标注。断言：`manager_test.go` 用表驱动覆盖边界（偏移 0、恰好等于 5 分钟、5 分钟 + 1 秒必须置位、-7 分钟不置位），并用现场真实数值复现「未来时间戳的记录排在后来上传的包之上」——首行必须是被标记的那条已完成记录，第二行必须是新上传的包且不被标记。

- 何时需要应用：
  - 升级记录里的时间看起来比当前时间晚，怀疑某个节点的时钟曾经跑快
  - 要判断控制台给出的「最新」记录到底可不可信
  - 排查为什么某条老记录一直排在列表最上面

### HF-2026-0929-04.3 上传口只验「这个包是不是我们的」，不验「这个包能不能装在这台机器上」——跨基线的升级包被收下，并占住控制台唯一的可执行槽（`3a61103`，P1）

- 现象：2026-09-29 14:02，现场把 2.2-105 线的热修补丁 `HF-2026-0929-04` 上传到仍运行 2.2-104 的集群。上传顺利：签名校验通过、历史记录里出现新的一行，控制台随即把它当作「最新、仍可执行」的目标。点「计划」后约 3 秒失败，对话框显示「升级计划生成失败：升级任务失败或被阻断；请查看输出和事件记录，确认维护门禁状态后再续跑或回退」——而该集群此时 `maintenance_active=false`、三台节点上都没有 `/etc/clusterguard/update-maintenance.json`，提示语指向了一个根本不存在的门禁。真实原因只在升级输出 `output.log` 的最后一行：「192.168.102.152 当前版本 2.2-104 与热修补丁基线 2.2-105 不一致；拒绝应用（装错基线会把控制面二进制降级）」。更麻烦的是这次失败没有让那条记录退位：重开对话框，按钮依然指着它；而真正该执行的 `cgupgrade-2.2-104-to-2.2-105-x86_64` 排在它下面、从界面上无法选中——升级记录行是不可交互的表格行，对话框里也没有任何选包控件。产品内既没有退役/删除接口，相同内容重传又保留原时间戳，于是现场只能改文件系统才能解封。

- 根因：守卫本身是对的，错的是它出现的位置和时机。`scripts/clusterguard-upgrade.sh` 会在计划阶段按设计拒绝跨基线包，但在此之前系统已经把「这个包能不能用」判断完了，用的还是另一个维度。四道判据叠成死锁：① **上传准入**（`internal/platformupdate/manager.go`）只校验 `validPatchID` / `SignatureVerified` / `Rolling` / `RollbackAvailable` / `!DatabaseMutation` / `TargetVersion != ""` / 引导器——**签名证明「这个包出自我们」，兼容性回答「这个包能不能装在这台机器上」，这是两件事，而通道只守了前者**。最能说明问题的是 `SourceVersion` 被读出来、被存下、甚至在去重比较里被用到了，却从未与集群当前版本比过一次：字段就在手边，没有人拿它做判断。② **列表**按 `uploaded_at` 降序，前端默认目标取 `packages[0]`，而对话框没有选包控件 ⇒ 唯一目标就是最新记录。③ **可执行性**把 `failed` 判为仍可执行——重试语义本身正确（瞬时失败值得重跑），但系统无法区分「这次失败了、重试可能成」与「这台机器上永远不成」，两者在数据上都是 `status: failed`。④ **唯一正确的拒绝在最末端**，且它的失败不回流去改变①②③已经做出的判定。出路同时被堵死：`internal/api/updates.go` 只有 `GET`、`POST 上传`、`POST {plan|execute|resume|rollback}`、`gate/*`，**没有退役或删除接口**；相同内容重传会命中 `manager.go` 的去重分支、`return existing` 并保留原 `uploaded_at`（上传幂等是对的，副作用是「重传顶下去」这条天然出路也没了）。这个缺陷一直存在，缺的只是一个触发它的包：此前每次「先传补丁再升级」都没事，是因为补丁的 `source_version` 恰好总是**已经装着**的那条线（`HF-2026-0928-06` = 2.2-104）；`HF-2026-0929-04` 是第一个为「尚未安装的目标线」提前建好并上传的包，路径第一次被走到，就锁死了。

- 修复：把那条守卫搬成**一处实现、两处使用**。新增单一谓词 `packageBaselineFault(kind, sourceVersion, targetVersion, current)`，口径逐字对齐升级器自己的两个分支：热修补丁要求已安装版本与补丁基线**完全相等**（它替换已装版本里的文件，且不改 RPM release，所以事后基线仍然有效）；滚动升级包接受节点停在源版本**或**已到目标版本（因为它逐节点应用、可断点续跑）。`current` 或包的 `source_version` 为空时**弃权**，不阻断——否则未链接发布元数据的开发构建会以 `development-0` 拒绝一切。**上传口**在建立目标目录**之前**按它拒绝，返回新哨兵 `ErrPackageBaselineMismatch`（`internal/api/updates.go` 映射为 **409**），所以被拒的包**不留任何记录**，而不是留下一条永远不会执行的死记录。**快照**按同一个谓词给记录打 `incompatible` / `incompatible_reason`（与 `clock_skew` 同层，都在记录包装上），这样在本守卫之前上传的记录也会被解释，而不是被静默列出。运行基线取自二进制自身的构建元数据：`platformupdate.CurrentRelease()` = `buildinfo.Version + "-" + buildinfo.Release`，接线点唯一（`internal/runtime/runtime.go`）。选它而不是起 `rpm` 子进程，是因为已核实热修构建器用 `-X …Release=${rpm_release}` 且 spec 里是 `105`（**不是**清单里算出来的 `105+hf-2026-0929-04`），所以载荷二进制自报 `2.2-105`，与 `rpm -q --qf '%{VERSION}-%{RELEASE}'` 完全相同、热修后也不变。开发构建（`buildinfo.Release == DevRelease`）返回空串，守卫弃权。

- 何时需要应用：
  - 上传了一个为别的发布线准备的升级包，想确认它会不会被收下
  - 控制台里有一条永远失败、又永远删不掉的升级记录
  - 升级包上传成功了，但一点「计划」就立刻失败
  - 要把补丁上传到尚未完成目标版本滚动升级的集群上
  - 升级记录里出现了一个似乎「既执行不了也删不掉」的包，怀疑是被挡住的那一个

### HF-2026-0929-04.4 控制台把「跨基线包」当成一次普通失败来重试——它永远占着唯一的动作槽，按钮只能打在一个本机装不上的包上（`d0f63e6`，P1）

- 现象：承接上一条：`HF-2026-0929-04` 计划失败之后，对话框里的「滚动升级」按钮**依然是亮的**，目标依旧是它。点下去只会再失败一次，而正确的那条记录（`cgupgrade-2.2-104-to-2.2-105-x86_64`，从未运行过）始终排在下面，界面上没有任何入口选择它。历史行只写着通用的失败文案，既不说明这条记录属于别的发布线，也不说明本机装不上——操作员能看到的只有「失败」，于是很自然地重试，或者去翻那条不存在问题的维护门禁。

- 根因：升级记录的行是只读的 `<tr>`，对话框里唯一的输入是文件选择器与确认短语，所以「目标是谁」完全由默认值决定——而默认值此前只按「是否终态」判断可执行性：`!['succeeded','rolled_back'].includes(job.status)`。这个判据对瞬时失败（网络抖动、门禁没清干净）是正确的，失败值得重试；但它对**本机永远不可能成功**的包给不出任何区别，两者在数据上都只是 `status: failed`。基线不一致属于后者，却长得和前者一模一样。这正是「缺陷为什么一直没暴露」的原因：此前每次上传的补丁基线恰好都是已装的那条线，路径从未被走到；而一旦有人为「尚未安装的目标线」提前建包，「最近上传的」天然就是「刚在处理的那件事」，列表顺序、默认目标、可执行槽又全部绑在「最新」上，锁必然出现。同期上一轮修复只修正了**时间轴**（从「取第一个」改为「取第一个未完成」），本次失败在时间轴上完全合格——它是新的、作业确实没完成、时间戳也没问题——所以按设计放行。两轴正交，必须补上第二轴。

- 修复：可执行性判据的第一行改为排除服务端已标记 `incompatible` 的记录：跨基线的包无论如何重试都不是候选。判定**不在前端重算**——重算正是两层答案漂移的方式，而同样的错误已经付过一次代价：`clock_skew` 曾经被从内嵌的 `package` 上读取，而不是它实际所在的记录包装层，页面于是一行标记都不显示；那次是**真浏览器运行**而不是契约测试抓到的。历史行现在用一条 `rowNote` 同时承载两种标记（`incompatible` 优先，其次 `clock_skew`），两者都在记录包装层读取；不可执行的原因直接取自服务端的 `incompatible_reason`，前端只做兜底文案。校验面板的 `verified-blocked` 文案也拆出专门一支来说「最新记录属于别的发布线」，**不再一律说「已经执行完成」**——报告一个错误的原因，正是这次事故把人引向「维护门禁」的那类错误。契约测试同时加了两条负向断言：禁止出现 `snapshot.current_version`（真实 API 没有这个字段，只有测试夹具造了它，拿它比较等于跟 `undefined` 比、恒为 false），禁止把两种标记从 `record`（内嵌的 `package`）上读取。真浏览器验收新增一个场景：跨基线记录存在时它必须不占槽位、历史行必须点名原因、且不得有异常抛出。

- 何时需要应用：
  - 升级按钮一直亮着、却每次点都失败，而且失败的是同一个包
  - 升级记录里有一条本机装不上的包，界面上却没有办法绕开它
  - 想知道升级记录某一行为什么不可执行

### HF-2026-0929-04.5 失败的升级作业把原因说成「确认维护门禁状态」，而这条路径上根本没有门禁（`d2e5d85`，P2）

- 现象：上一条事故里，控制台的对话框只给出「升级计划生成失败：升级任务失败或被阻断；请查看输出和事件记录，确认维护门禁状态后再续跑或回退」。操作员据此去查维护门禁——而 `status.json` 里 `maintenance_active=false`、三台节点上都没有 `/etc/clusterguard/update-maintenance.json`，这个方向查不出任何东西。真实原因（「当前版本 2.2-104 与热修补丁基线 2.2-105 不一致」）其实已经由升级器写进了 `output.log` 的最后一行，但没有任何一处把它带到操作员面前。

- 根因：`scripts/clusterguard-update-job.sh` 的失败分支是一句**固定文案**，把「维护门禁」当作最可能的解释写在最显眼处，而作业脚本本身就掌握 `maintenance_after_failure` 的真实值。控制台再把这句 `message` 原样透出为「升级计划生成失败：<message>」⇒ 一个已知为假（或至少已知为非当前原因）的解释被当成了唯一线索。而升级器其实已经写清了原因：`scripts/clusterguard-upgrade.sh` 的 `die` 把它写在最后一行「升级失败：<原因>」，作业脚本的 helper 又把升级器输出重定向进了 `${job_dir}/output.log`，失败分支执行时升级器已退出、日志是完整的——信息一直都在，只是没被读出来。

- 修复：抽出函数 `update_failure_message()`，从 `${job_dir}/output.log` 里取**最后一条**「升级失败：<原因>」（纯 bash `case` 匹配 + `${line##*升级失败：}` 截取，不用 `sed`），拼成「升级任务失败或被阻断：<真实原因>」；取不到时（例如升级器还没写任何输出就失败，或日志文件不存在）回退到原文案，保证不会因为这次的改动而丢信息。做成函数是为了**可测**：新测试把脚本里的函数体切出来交给 `bash -c` 运行，喂四种输入——现场那条真实日志、多行时取最后一条、有日志但没有「升级失败：」标记、日志文件不存在——所以解析本身写错就会红，而不只是「某一行被删掉」才红。断言用行为而非文本契约，原因同样来自这次的教训：文本契约抓不到实现写错。

- 何时需要应用：
  - 升级计划/执行失败，提示让你去确认维护门禁状态，却看不出到底哪儿不对
  - 想知道升级失败的真正原因而不必登到节点上翻日志文件
  - status.json 的 message 与升级输出对不上，怀疑失败原因被覆盖了

### 验证

- `/usr/local/bin/clusterguard --version-json | jq -r .commit   # 必须等于本次构建树（build_commit）的短哈希，release 仍为 105`
- `rpm -q clusterguard-ha   # 必须仍为 2.2-105：热修补丁不改 RPM 版本，靠 commit 区分是否已应用`
- `sha256sum /usr/local/bin/clusterguard   # 必须等于包内 HOTFIX-MANIFEST.json 中 payload/bin/clusterguard 的 sha256`
- `sha256sum /usr/local/libexec/clusterguard-update-helper   # 必须等于清单中 payload/bin/clusterguard-update-helper 的 sha256`
- `systemctl restart clusterguard-ha clusterguard-update-helper   # apply.sh 绝不自动重启；不重启则两个进程仍是旧代码，页面不会变`
- `curl -sk https://192.168.102.155:3000/ | grep -c '记录时间戳晚于当前时间'   # 必须 ≥1（控制面二进制内的页面已含排序可信度提示，浏览器需强制刷新）`
- `curl -sk https://192.168.102.155:3000/ | grep -c '校验完成，但当前没有可执行的升级包'   # 必须 ≥1（校验面板已含 blocked 文案）`
- `浏览器强制刷新 https://192.168.102.155:3000/ → 设置 → 版本更新 → 升级：若历史记录首行是已完成记录而刚上传的包排在下面，「滚动升级」必须仍可点；点开后确认框里要手输的包 ID 必须是刚上传的那个`
- `同上对话框：若上传已验签但列表里没有可执行记录，面板必须显示「校验完成，但当前没有可执行的升级包」并点名占住列表的记录，而不是显示绿色的「校验完成」配一个灰按钮`
- `curl -sk -H 'Authorization: Bearer <token>' https://192.168.102.155:3000/api/v1/platform/updates | jq -c '.result.packages[] | {id:.package.patch_id, clock_skew}'   # 时间戳落在未来的记录必须带 clock_skew:true（字段缺省即未标记）`
- `curl -sk -X POST -H 'Authorization: Bearer <token>' -F 'package=@clusterguard-ha-hotfix-<为别的发布线准备>.cgpatch' https://192.168.102.155:3000/api/v1/platform/updates   # 必须返回 409，且 message 同时点名包的发布线与本集群当前发布线`
- `ls /var/lib/clusterguard/updates/   # 上传被拒后不得出现该包 ID 的目录：拒绝发生在建立目标目录之前，被拒的包不留任何记录`
- `curl -sk -H 'Authorization: Bearer <token>' https://192.168.102.155:3000/api/v1/platform/updates | jq -c '.result.packages[] | {id:.package.patch_id, src:.package.source_version, incompatible, incompatible_reason}'   # 基线与本集群不一致的已有记录必须 incompatible:true 且 reason 点名两条发布线`
- `sha256sum /usr/local/libexec/clusterguard-update-job.sh   # 必须等于包内 HOTFIX-MANIFEST.json 中 payload/scripts/clusterguard-update-job.sh 的 sha256`
- `cat /var/lib/clusterguard/updates/<最近失败包>/status.json | jq -r .message   # 必须包含升级器给出的真实原因（例如「与热修补丁基线 2.2-105 不一致」），不得再是「确认维护门禁状态」`
- `浏览器强制刷新 https://192.168.102.155:3000/ → 设置 → 版本更新 → 升级：上传一个属于别的发布线的包必须当场被拒并给出点名两条发布线的文案，且历史记录里**不新增**该记录`
- `同上对话框：若列表里已有跨基线记录，该行必须带「与当前集群基线不一致」标记，「滚动升级」按钮不得以它为默认目标；此时若没有别的可执行记录，面板必须说「当前没有可执行的升级包」并点名那条跨基线记录属于别的发布线`

### 回滚

执行 rollback.sh 恢复旧的控制面二进制、旧 Helper 二进制与旧运行时脚本，然后 systemctl daemon-reload 并 systemctl restart clusterguard-ha clusterguard-update-helper；本包不涉及 systemd 单元、不涉及安装器，回滚不需要额外手工对齐。回滚后会被带回的行为共五处：控制台重新按升级记录首行决定「待升级目标版本」与「滚动升级」按钮，因此当列表里存在时间戳落在未来的记录时，按钮会再次被那条已完成记录关掉且页面不再解释原因；服务端不再下发 clock_skew，历史行不再有「排序与时间不可信」标注；上传通道重新只验签名，属于别的发布线的包会被再次收下；快照不再标记 incompatible，跨基线记录会重新被当作可重试候选并占住唯一动作槽；失败的升级作业重新只报固定文案，把操作员指向一个不存在的「维护门禁」。2026-09-29 现场已把三台节点的未来时间戳修正回真实时间，且已把误上传的 HF-2026-0929-04 记录改名隔离，所以正常状态下回滚不会立刻复现；但只要之后再次出现时钟跑快的节点，或再次为尚未安装的目标线提前上传补丁，回滚就等于把这两个缺陷一并带回来。

## HF-2026-0929-03 — 2.2-104 现场修复合集（累积）：在 HF-2026-0929-02 之上合并设置页页签——「账户与偏好」并入「状态设置」，页签回到三个

- 严重级别：P0
- 覆盖修复提交：`de14249`、`7a1ba82`、`ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47`、`9303e9d`、`18d738e`、`81fe3c8`、`ac6f3f7`、`abf5782`、`fce48b7`
- 构建树：`de142494dd73b6d7890df713897e551caa9da2a1`（基线 `e01f5ce376f94e2595590358c72dd2585e7c09b4` + 上述修复，不含其它提交）
- 适用版本：2.2-104 → 2.2-104+hf-2026-0929-03（x86_64）
- 产物：`release/2.2-104-hotfixes/clusterguard-ha-hotfix-HF-2026-0929-03-2.2-104.x86_64.cgpatch`
- SHA-256：`21bdc542d9c24c1d6345788797ba12bfd804c54f2fa33af4ae5022cc75aad7a3`
- 源码差异：`src/HF-2026-0929-03-de14249.patch`
- 交付内容：
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/bin/clusterguard-agent` → `/usr/local/bin/clusterguard-agent`（0755）
  - `payload/bin/clusterguard-update-helper` → `/usr/local/libexec/clusterguard-update-helper`（0755）
  - `payload/systemd/clusterguard-agent-reconcile.service` → `/usr/lib/systemd/system/clusterguard-agent-reconcile.service`（0644）
  - `payload/systemd/clusterguard-update-helper.service` → `/usr/lib/systemd/system/clusterguard-update-helper.service`（0644）
  - `payload/scripts/clusterguard-clock-mesh.sh` → `/usr/local/sbin/clusterguard-clock-mesh.sh`（0755）
  - `payload/scripts/clusterguard-cluster-finalize.sh` → `/usr/local/libexec/clusterguard-cluster-finalize.sh`（0755）
  - `payload/scripts/clusterguard-mysql-install.sh` → `/usr/local/libexec/clusterguard-mysql-install.sh`（0755）
  - `payload/scripts/clusterguard-postgresql-install.sh` → `/usr/local/libexec/clusterguard-postgresql-install.sh`（0755）
  - `payload/scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750）
  - `payload/installer/install_clusterguard.sh` → `仅安装器，现场无对应路径`（0755）
- 需要重启：`clusterguard-ha.service`、`clusterguard-update-helper.service`、`clusterguard-agent-reconcile.service`

> **本身份已冻结。** 它是现场实际执行过的那一份，作为证据保留，**不是该交付线的上传入口**。
> 它仍带着下列缺陷，且不会按今天的规则回炉重造——那会毁掉这份证据。

- 已知缺陷：HF-2026-0929-03:rollback.sh 未用改名换 inode 恢复文件（就地写运行中的二进制会 ETXTBSY）
- 已知缺陷：HF-2026-0929-03:rollback.sh 直接覆盖目标文件（运行中的二进制会 ETXTBSY）
- 已知缺陷：HF-2026-0929-03:apply.sh 未把备份清单绑定到本补丁的 hotfix id
- 已知缺陷：HF-2026-0929-03:rollback.sh 未把备份清单绑定到本补丁的 hotfix id
- 已知缺陷：HF-2026-0929-03:rollback.sh 接受任意备份清单，可能恢复别的补丁的文件或误删文件

### 本包概要

本包完整包含并取代 HF-2026-0929-02（也就是说完整包含 HF-2026-0929-01），另加一项设置页合并（de14249）。交付内容与上一包相同——控制面、agent、update-helper 三个二进制，加上时钟网脚本、单元与环境清单——但三个二进制都按新构建树 de14249 重新编译，因此不要与其它包混装，只装这一个。新增的 de14249 只动 internal/api/console.html：原来「控制台状态 / 运行参数 / 版本更新 / 账户与偏好」四个页签在常规宽度下第四个换行、单独占满一行（现场截图即是），而账户与显示偏好描述的对象本来就是控制台自身，于是把「账户与偏好」并进「状态设置」成为状态面板内部的一块，页签收敛为三个。**console.html 是 go:embed 进控制面二进制的**，所以这项改动只有在装了本包并重启 clusterguard-ha 之后才会在页面上出现，浏览器还需要强制刷新。装完当场核对：控制台设置页只有三个页签、选中「状态设置」能看到「账户与安全」与「显示偏好」、切到「运行参数」时它们离屏、切回后重现、「修改密码」仍能打开对话框。

### HF-2026-0929-03.1 设置页第四个页签单独换行占满一行，账户与偏好被拆成与「控制台状态」无关的一页（`de14249`，P3）

- 现象：控制台设置页的页签是「控制台状态 / 运行参数 / 版本更新 / 账户与偏好」四个。常规窗口宽度下第四个放不进第一行，于是换行落到第二行、单独占满一整行 —— 现场截图就是这个样子。比排版更别扭的是分组本身：账户与安全、界面语言、自动刷新描述的对象都是控制台自己，和「控制台状态」是同一类东西，却被拆成平级的一页，「设置」看上去像两组互不相干的设置；要改界面语言，操作员得先切到「账户与偏好」、改完再切回来。而且这是「再加一个入口就坏」的结构：页签行放三个刚好一行，第五个入口同样会换行，谁都不知道下一次该往哪儿加。

- 根因：页签是按「数据从哪个接口来」切的，不是按「操作员想改什么」切的：控制面状态来自 /api/v1/control-plane/*，账户与偏好来自账户接口，于是后者被实现成一个平级的第四个页签，而没有人体量过 tablist 的宽度。三个入口时一行刚好放得下，第四个一加就换行 —— 这是结构问题，不是一次性的排版失误。分组同样缺少语义依据：账户与显示偏好描述的对象就是控制台自身，正是状态那一页已经描述的对象，两者的区别只是数据来源不同，而数据来源是实现的细节，不是操作员的心理模型。

- 修复：把两个页签合成一个「状态设置」：账户与安全、显示偏好作为**状态面板内部**的一块（搬迁，不是复制），因此切到「运行参数」时它会随面板一起离屏，页签轮转收敛为三个；页签说明改为「Leader、Raft 多数派、运行任务与账户偏好」。这不是纯字符串改动，所以合并从两个方向钉死：契约测试正向断言新页签名；位置断言账户块必须出现在状态面板开始之后、下一个面板之前（只断言字符串存在的话，块浮在面板外也能通过）；反向断言不允许再出现 settings-account-tab / settings-account-panel / data-settings-section="account"，且页签顺序长度必须为 3。新增真浏览器验收 tools/console-settings-merge-acceptance.cjs（沿用仓库既有的自研 CDP 驱动与 console-ui-fixture，无 playwright 依赖），在真实页面上断言账户块有布局盒、切「运行参数」后离屏、切回「状态设置」后回来、「修改密码」仍能打开对话框、界面语言与自动刷新仍渲染、方向键只在三个页签间轮转 —— 账户功能是被搬家，不是被删掉。tools/console-design-acceptance.cjs 里对被删页签的两处引用改指 settings-status-tab。变异验证：契约 5/5、浏览器 3/3 全部被抓（含「把第 4 个页签加回来」「把账户块搬出面板」「删掉账户块」），还原后文件逐字节一致。

- 何时需要应用：
  - 设置页第四个页签单独换行占满一行，「账户与偏好」看起来和「控制台状态」无关
  - 要改界面语言 / 自动刷新却找不到入口，或需要确认合并之后账户功能（当前用户、修改密码、退出）是否还在
  - 打算再往设置页加一个入口，需要先确认页签行现在有几个、还能不能再放

### HF-2026-0929-03.2 时钟拨回之后拓扑观测水位永久拒绝刷新，租约停止续约、VIP 被动释放并把实例全部锁成只读（`7a1ba82`，P0）

- 现象：把三台快 8 小时的时间拨回真实时刻并重启控制面之后，集群再也没能恢复，而且没有任何现场手段能把它拉回来。控制面日志每秒刷一条 `scheduled topology discovery failed: apply discovery refresh batch: … stale topology observation: observed at 2026-09-29T03:44:12.100668659Z is not after 2026-09-29T11:27:36.730892013Z`；`/var/lib/clusterguard/metadata.json` 与 `raft.db` 的 mtime 停在控制面启动后一分钟内不再前进，`clusterguard_state_revision` 卡死；租约侧每 5 分钟报一次 `VIP ownership reconciliation failed: cluster … topology is stale or unavailable`，VIP 归属租约的 expires_at 停在“约 8 小时之后”。数据节点 agent 随后按设计摘掉 VIP 并把每台实例置为只读。真正刺眼的是因果方向：**修时钟这个动作本身把集群推进了停机态** —— 一个为了让系统回到正确状态而做的运维动作，代价是整个集群停机。

- 根因：拓扑观测必须让水位向前走，这条单调性检查是用来挡住“迟到的刷新用更旧的证据覆盖更新的证据”的。但孤立部署没有外部时间基准，水位的好坏完全等于写下它的那口钟：一旦运维修正了一口走错的钟，之后**每一次**观测都会落在水位之后，严格单调性于是永远拒绝刷新。这里被拒的并不是陈旧观测，而是一口已经不再存在的时钟。后果是一条闭锁：拓扑永不刷新 → `topology_snapshots[].observed_at` 永远停在那口错钟写下的 `11:27:36` → ownership_keeper 的新鲜度判据（`snapshot.ObservedAt.After(now.Add(interval))` 即“拓扑来自未来”）把它判成 stale，直接跳过续约 → 租约再没人续 → agent 按设计释放 VIP 并把实例锁成只读。也就是说同一个根因在两侧各有一个面：写入侧表现为“水位拒绝了观测”，授权侧表现为“拓扑来自未来”。

- 修复：改用**差距本身**作判据。探测、重试或调度延迟可以让观测乱序几秒，绝不可能几小时，所以超过任何合理量级的差距不可能是顺序违规——它是一口不再存在的时钟。这种观测直接放行，并把水位落到这条新观测上，严格顺序从那一刻起重新生效；写入侧一放行，`observed_at` 也就跟着落到当前时钟，授权侧的新鲜度判据随之通过，租约恢复续约。容忍窗口（`observationWatermarkRewindTolerance = 5 * time.Minute`）之内行为完全不变，水位存在的意义一点没丢。两个方向都有断言：倒退 1 分钟仍然报 `ErrStaleObservation`，倒退 8 小时被吸收；断言水位、已发布的观测、以及重新打开的持久文件三者一致，并且重置之后新的观测能正常推进。新增门禁 `tools/verify-observation-watermark-tolerance.cjs` 钉住容忍窗口的形状（正数、有界、严格小于它要吸收的那一步），并断言回拨测试里那条顺序违规断言仍在——否则门禁可以靠“删掉它守的那个保护”通过。

- 何时需要应用：
  - 修正过集群时间之后（或任何场合回拨过系统时钟之后）集群停在 degraded、控制台迟迟不刷新
  - 控制面日志每秒刷一条 stale topology observation: observed at … is not after …
  - /var/lib/clusterguard/metadata.json 与 raft.db 的 mtime 不再前进，clusterguard_state_revision 卡死
  - 控制面日志每 5 分钟报 VIP ownership reconciliation failed: … topology is stale or unavailable
  - VIP 归属租约的 expires_at 停在约 8 小时之后
  - 数据节点 agent 摘掉 VIP 并把 MySQL 实例设成只读

### HF-2026-0929-03.3 热修包上传到控制台只得到「请走命令行」，而产品从未提供那条通道（`81fe3c8`，P1）

- 现象：运维按上一版给出的指引，把 clusterguard-ha-hotfix-*.cgpatch 传到「版本更新」对话框，得到「该包是热修补丁包（clusterguard-hotfix/），不经过控制台滚动升级通道；请在控制节点命令行用 tar -xzf 解包后运行 clusterguard-hotfix/apply.sh 安装」。指引本身没错，但控制台既不接受这个包，也不提供 plan/execute/rollback：现场每一次装热修都要手工 SSH 解包、逐台重启，验签结果、执行过程与回滚能力都不留痕。

- 根因：控制台的安装链路本来就是通用的：它只把「执行你已验签并存下的那个包」翻译成 mode + patch_id 请求 update-helper，helper 再调 clusterguard-upgrade 的 --plan/--execute/--rollback，特权边界不区分制品类型。真正把热修挡在门外的是两处各自「多一层保护」：① 执行器把顶层目录写死成 clusterguard-patch/，其它顶层一律判「范围外路径」；② 控制台在文件选择时按文件名含 hotfix 就直接拦截。两处合起来等于热修永远进不了控制台，尽管 --inspect 与 helper 本就有能力承载它。另有一个独立缺陷被顺带挖出：apply.sh / rollback.sh 是在清单「签名之后」才生成的，因此只要 payload 未被改动，替换 apply.sh 就能让补丁装出完全不同的东西而验签一路通过。

- 修复：① clusterguard-upgrade.sh 在解包阶段按顶层目录判定制品类型（clusterguard-patch/=滚动升级、clusterguard-hotfix/=热修；两种布局混装即拒绝），--inspect 新增 kind= 行，旧版升级脚本不报 kind 时按「升级」处理以免回退现场。② 新增 verify_hotfix 与 run_hotfix_update：沿用同一信任锚验签，要求 schema_version 为 3、kind=hotfix、apply.sh/rollback.sh 的摘要与签名清单一致；逐文件比对 sha256 与 SHA256SUMS；落点白名单限 /usr/local/{bin,sbin,libexec}、/usr/lib/systemd/system、/etc/clusterguard；节点侧幂等判据改为「逐文件摘要一致」（RPM 版本判据对热修失效——源与目标 release 相同，滚动循环会把每台都判成「已是最新」却报成功）；拒绝 --resume 并说明理由是应用本身幂等。③ 控制面 Package 新增 Kind 字段，控制台详情面板显示「包类型：热修补丁（只替换清单声明的文件，不改 RPM 版本）」，文件选择器同时放行 .cgupgrade 与 .cgpatch，删除「文件名含 hotfix 就拦截」的整块逻辑。④ 构建脚本在 apply.sh/rollback.sh 生成之后重写清单、把两者摘要写入签名，再重新签名并重发 SHA256SUMS。

- 何时需要应用：
  - 需要在控制台的「版本更新」里直接安装热修补丁包，而不是手工 SSH 解包
  - 上传 .cgpatch 后报「该包是热修补丁包…请在控制节点命令行…」
  - 需要让热修包的验签、计划、执行与回滚都在控制台留有记录
  - 担心热修补丁包里的 apply.sh / rollback.sh 没有被签名锚定

### HF-2026-0929-03.4 热修补丁的落点由来源名猜出，修复被装到产品不读的路径（`18d738e`，P1）

- 现象：HF-04 应用后三台脚本、单元、二进制全部显示「已安装」，但控制台上传热修包仍报「升级包包含范围外路径：clusterguard-hotfix/」：带修复的脚本被装到 /usr/local/libexec/clusterguard-upgrade.sh（产品从不执行该路径），真正被调用的 /usr/local/sbin/clusterguard-upgrade 还是 9 月 24 日 RPM 装的旧版本。

- 根因：构建脚本按「scripts/clusterguard-*.sh → /usr/local/libexec/<同名>」猜落点，而打包清单里至少三个脚本不是这样：clusterguard-upgrade.sh 与 clusterguard-configure.sh 落在 /usr/local/sbin（前者 0750 root:clusterguard），clusterguard-agent-stdio.sh 还要去掉 .sh 后缀。门禁当时只校验 payload 里「有没有」对应文件，从不校验它「落到哪」。

- 修复：落点、模式、属主统一由 scripts/hotfix-payload-map.cjs 读 packaging/rpm/nfpm.yaml 解析（构建、门禁、测试共用同一真源）；payload 目录改为 payload/scripts，apply.sh 按声明的属主属组安装；未在打包清单声明的运行时脚本直接构建失败；门禁新增「每个修复必须抵达其现场路径」与「每个 payload 文件必须落在打包清单声明的路径」两条检查——后者在 HF-04 产物上实测报红，精确指出路径/模式/属主三项偏差。

- 何时需要应用：
  - 热修补丁应用后每台都报「已安装」，但现场行为没有任何变化
  - 控制台上传热修包仍然报「范围外路径：clusterguard-hotfix/」
  - 需要判断某个脚本到底哪一份副本在生效

### HF-2026-0929-03.5 热修补丁包误传控制台时只报「范围外路径」，不说是通道走错了（`9303e9d`，P1）

- 现象：运维把 clusterguard-ha-hotfix-*.cgpatch 传到版本更新对话框（文件选择器按 .cgpatch 后缀放行、实验室链验签也能通过），最后只得到「升级失败：升级包包含范围外路径：clusterguard-hotfix/」——没有任何文字说明热修包根本不该走这个通道，运维会反复重试。

- 根因：热修补丁包沿用了旧滚动升级包的 .cgpatch 后缀，两种不同用途的制品撞名；滚动执行器的白名单只判路径合法性，不识别制品类型；控制台对话框还写着「兼容旧 .cgpatch」，进一步诱导上传。

- 修复：clusterguard-upgrade.sh 在解包检查时识别 clusterguard-hotfix/ 顶层，直接报出正确入口（tar 解包后运行 clusterguard-hotfix/apply.sh）；控制台在文件选择时拦截文件名含 hotfix 的 .cgpatch 并给出同样指引，选择器与就绪文案改为「滚动包专属，热修包走命令行」；update-and-patch 双语文档明确两种 .cgpatch 是两条通道。新增防回归：TestPatchInspectorDivertsHotfixPackagesToTheCLI 真打包真跑 --inspect，控制台契约测试断言拦截逻辑存在，均经变异验证。

- 何时需要应用：
  - 把热修补丁包上传到版本更新对话框，报「升级包包含范围外路径：clusterguard-hotfix/」
  - 运维不确定 .cgpatch 热修包应该走哪条安装通道

### HF-2026-0929-03.6 整机重启后整个集群起不来：共享运行时目录权限与恢复冻结永不释放（`28e3b47`，P0）

- 现象：三台整机关机再开机后，控制台报「部分数据不可用：候选评估」「尚未发现主库」，三个实例全部显示「数据库未启动或不可达」，再次整机关机与故障切换都被阻断。clusterguard-mysql-3306.service 与 clusterguard-cluster-restore.service 双双进入崩溃重启循环，重启计数分别达到 2063/2069/2072 与 1034~1036。

- 根因：① update-helper 单元用 ExecStartPre 把共享目录 /run/clusterguard 建成 0750，而托管 MySQL 单元的 RuntimeDirectory=clusterguard/mysql/3306 嵌套在它下面、以非特权 mysql 用户运行（不属于 clusterguard 组）；/run 是 tmpfs，开机时 update-helper 先创建父目录，mysqld 连这层目录都穿不过去，建 socket 锁文件失败即 Aborting——InnoDB 其实已初始化成功，数据无损。安装当天能用只是因为碰巧 mysqld 先启动，开机顺序一反过来就是必现故障。② cluster-finalize 在 600 秒内等不到主库时以 exit 0 结束，而它是 oneshot + Restart=on-failure，退出码 0 等于宣告完成，恢复冻结此后只能人工解除——脚本自己打印的「修好后会自动恢复」并不成立。

- 修复：共享父目录改为 0755（保持可穿越），并让托管 MySQL/PostgreSQL 单元在自己的 ExecStartPre 里幂等修正该父目录，于是无论谁先创建父目录、无论开机顺序如何，引擎都能到达自己的 socket；cluster-finalize 的 fail-closed 超时路径改为 exit 1，让 systemd 每 30 秒重试，直到主库恢复并走完 power/complete 自动解冻。门禁同时修掉了把 0750 当成期望值的断言，并新增两条防回归检查（共享目录权限、finalize 退出码）。

- 何时需要应用：
  - 整机关机后重新开机，控制台报「数据库未启动或不可达」且没有主库
  - clusterguard-mysql-3306.service 反复重启，error.log 报 Could not create unix socket lock file
  - 计划关机或故障切换被阻断，power 生命周期停在 recovering 且 recovery_freeze 为 true

### HF-2026-0929-03.7 MySQL 写入者协调抖动：授权主库每 5 秒自隔离一次（`dd82ca5`，P0）

- 现象：集群长期 degraded、两条复制链路 unhealthy、候选评估 409、计划关机必被阻断。实测自安装起 9492 次自隔离，read_only 与 VIP 每约 10 秒同步翻转一次。

- 根因：① agent-reconcile 单元的 CapabilityBoundingSet 缺 CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH，root 也读不了 <datadir>/mysqld-auto.cnf（mysql:mysql 0640），IsolationStatus 报错即触发失败关闭；② convergeWritableRestartState 要求 RestartReadOnly 变为 false，但“重启后只读”是永久站点不变量，条件永不满足。

- 修复：收敛判定改为只看“隔离意图已清除 + 运行时可写”（writableRestartStateConverged），不再要求重启栅栏消失；单元补 CAP_DAC_OVERRIDE 与 CAP_DAC_READ_SEARCH（bounding 与 ambient 都补）。

- 何时需要应用：
  - 集群长期 degraded 且复制链路 unhealthy，但复制本身正常
  - journalctl -u clusterguard-agent-reconcile.service 反复出现 self-isolated 或 permission denied

### HF-2026-0929-03.8 升级执行链两个前置缺陷：SSH 私钥属主与 Helper 共用运行时目录（`d5f9491`，P0）

- 现象：版本更新始终 available=false，安装/上传升级包的三个控件全灰；即便手工补齐 update.json，升级执行器仍以“私钥权限过宽”拒绝；Helper 首启报 status=233，且停机时把 /run/clusterguard 整个删掉。

- 根因：① update.json 的 ssh_key 指向 clusterguard 属主的私钥，而 workspace check-file 要求 root 属主且非组/其他可写；② Helper 单元用 RuntimeDirectory=clusterguard 声明了一个共用目录，启动 chown/chmod 存在竞态，停止时 systemd 会删除该目录。

- 修复：安装器额外生成 root:root 0600 的私钥副本 /etc/clusterguard/updates/controller_ed25519 并写入 update.json；Helper 单元去掉 RuntimeDirectory，改为 ExecStartPre 幂等创建共享目录（不再“拥有”该目录，停机不删）。该目录的模式在 28e3b47 中进一步定为 0755。

- 何时需要应用：
  - 控制台版本更新长期 available=false
  - clusterguard-update-helper 首启失败或停机后 /run/clusterguard 丢失

### HF-2026-0929-03.9 控制台不解释“不可用”的原因（`ed9faca`，P1）

- 现象：版本更新面板只显示红徽标“不可用”，不写原因；集群加载横幅只报栏目名（“部分数据不可用：候选评估；操作已锁定”），运维无法判断下一步做什么。

- 根因：前端丢弃了后端给出的原因：`evidenceResult` 只保留布尔状态，409 响应体里的具体原因没有回填到面板与横幅，运维只能看到状态标签。

- 修复：面板内联渲染 `#software-update-panel-reason` 并在徽标上加 tooltip；`evidenceResult` 携带 `reason`，`evidenceReasonText()` 把 409 消息映射为中文，`evidenceUnavailableText()` 按栏目回填原因。

- 何时需要应用：
  - 控制台版本更新面板显示“不可用”但无原因
  - 集群加载横幅只报栏目名、不报原因

### HF-2026-0929-03.10 整机关机提交后控制台卡在无法交互的对话框上（`5ae2039`，P1）

- 现象：提交整机关机后主机断电、控制面随之消失，页面永久停在关机确认对话框上，只能手动关闭标签页；提交后响应丢失时还会误报为错误。

- 根因：poweroff 分支提交成功后只改了结果横幅，既不关闭对话框也不做收尾；主机断电后页面失去所有交互入口，而“已提交却丢了响应”本就是预期结果。

- 修复：新增 settlePoweroffConsole()：等 2.5s 后带 3s 超时探测 /power/status，不可达则显示离线层并 window.close()，仍可达则只关对话框并起 5s 看门狗；service 模式永不关页面。

- 何时需要应用：
  - 提交整机关机后控制台卡死、需要手动关闭标签页
  - 关机提交后偶发“无法连接控制 API”被当作失败

### HF-2026-0929-03.11 时钟回拨后 VIP 归属租约不再过期，自动故障接管被静默关闭（`ac6f3f7`，P0）

- 现象：集群时间整体快 8 小时。修好时间之后——或任何一次向回拨动系统时钟之后——coordination_leases 里 VIP 归属租约的 expires_at 会停在“约 8 小时后到期”。租约仍然授权当前持有者，但**持有者停止续约它也不会过期**：ownership_keeper 每 5 秒调用一次 AcquireStableBatch，判据是“距到期还剩超过半个 TTL 就跳过”，于是整个 8 小时窗口内自动接管失效——持有 VIP 的节点宕机，VIP 不会自动转移。三台若不同步回拨，先拨回的那台会把租约判为已过期并抢注，出现两个 VIP Owner。

- 根因：续约判据只问“还剩多少 TTL”，而时钟回拨之后存储的到期时间被甩到任意远的未来，“还剩超过半个 TTL”于是被读成“无需处理”，持续时长恰好等于回拨的幅度。能暴露回拨的量是到期时间距当前时钟的距离：一次续约总是把到期时间写成“自己的时钟 + 一个 TTL”，所以任何超过一个完整 TTL 的剩余有效期都不可能是时间流逝的产物。（第一次修复改用了“到期时间 - 记录自身的 updated_at”，但续约会同时重写这两个字段，二者之差恒等于授予的 TTL，不携带任何时钟信息。）

- 修复：Acquire 与 AcquireStableBatch 都以 maxLeaseTTL（既是授予上限，也是“存储到期时间距当前时钟”的合法最大值）为准，超过即重锚为 now + TTL；“续约永不缩短既有到期时间”这条既有不变量保持不变。新增三条真跑时间的测试：稳定路径重锚、转移路径重锚，以及边界用例——恰好一个完整 TTL 必须保持不动，否则检测器会在每个 tick 重写所有长期租约。

- 何时需要应用：
  - 修好集群时间之后，或任何场合回拨过系统时钟之后
  - 担心自动故障接管被静默关闭、VIP 不会随主库切换
  - 三台节点时间不一致时出现过 VIP 抢注或双 Owner

### HF-2026-0929-03.12 时钟网脚本把本机时间无校验地冻结成全集群权威，且从不固定显示时区（`abf5782`，P1）

- 现象：装机时三台的 UTC 一致地快 8 小时（RTC 里存本地时间、内核按 UTC 读取）。当时的时钟检查只问“节点之间是否互相一致”——最大偏差 2 秒，通过——随后 clusterguard-clock-mesh.sh --server 把这台的时间升格为全集群权威，再由 hwclock --systohc --utc 与 rtcsync 固化进 RTC，偏差因此挺过每一次重启。现场证书就是证据：CA 的 notBefore 为 Sep 23 14:05:49 2026 GMT，比真实时刻晚整整 8 小时。同时 152/153 显示 America/New_York、154 显示 Asia/Shanghai，集群里同一条记录有两个钟点。

- 根因：两处都被“多一层方便”补成了缺口。① --server 直接把自己当前的时间当作权威，没有任何上游能纠正它，也没有任何确认步骤，而把未校验的时间写进 RTC 正是让一次安装失误变成永久偏差的动作。② 显示时区完全交给各节点 OS 默认，而控制台渲染“配置更新时间”用的是应答节点的本地时区（internal/runtime/configuration_view.go），时区不统一会被产品直接呈现出来。

- 修复：① --server 现在必须给出 --set-utc <RFC3339>（采用指定时刻）或 --accept-current-time（明确为该节点时间背书）；两者都没有时，在改写任何配置或触碰 RTC 之前直接拒绝，并打印两条出路。--set-utc 会先停 chronyd 再拨钟并报告偏差。② 新增 --timezone，在两种角色分支之前统一落地 timedatectl set-timezone；未指定时明确打印“本节点时区不受本工具管理”，--dry-run 同样打印全部决策。③ 安装器改为逐节点与本机 UTC 比对（±30 秒）通过后，才向权威传 --accept-current-time，并把同一个 --timezone 传给权威与全部客户端；成功文案不再把“互相一致”说成已校验。④ 脚本加入 RPM 打包清单（/usr/local/sbin/clusterguard-clock-mesh.sh），现场才能通过正式包路径收到修正版——此前它只随安装器拷贝，已装机的站点永远拿不到修正。

- 何时需要应用：
  - 节点之间时钟一致、但整体偏离真实时间（一个时区偏移的形状）
  - 集群里同一条记录显示两个钟点（节点显示时区不统一）
  - 需要重跑时钟网配置，用 --timezone 把全集群显示时区固定下来

### HF-2026-0929-03.13 热修通道按路径前缀猜“改动落在哪个二进制”，共享包里的修复被静默丢弃（`fce48b7`，P1）

- 现象：本包要携带的租约修复位于 internal/coordination，而构建脚本与台账门禁都用两条硬编码前缀推导组件（internal/agent|cmd/clusterguard-agent 与 internal/api|cmd/clusterguard）。该目录两条都不匹配（实测命中数均为 0），于是构件里只会有运行时脚本、没有二进制：现场会“应用成功”，签名清单会写“修复已交付”，而控制面仍然带着缺陷，且因为门禁与构建脚本共用同一张表，没有任何一方会反对。同一张表还漏了 internal/platformupdate——它被控制面与 clusterguard-update-helper 同时链接，而 HF-2026-0928-06 只交付了控制面，控制台热修通道的 helper 侧那一半从未到达现场。

- 根因：“某个包被哪个二进制链接”是导入图的事实，与路径长什么样无关。共享包不属于任何一条前缀，而这张前缀表同时决定了两件事：构件里放什么，以及哪些修复必须有补丁——共享包里的修复因此在两个方向上都不可见。

- 修复：新增 scripts/hotfix-component-map.cjs 作为唯一真源：候选二进制从 packaging/rpm/nfpm.yaml 的 bin/* 读取（因此涵盖 clusterguard、clusterguard-agent、clusterguard-update-helper、cgctl、clusterguard-k8s-fence-guard，而不是写死的两个），归属由 go list -deps 的真实导入图决定；导入图读不出来时硬失败，而不是返回空集合——空集合与“没有任何东西承载这条修复”无法区分，这正是修复丢失的形态。构建脚本与台账门禁共用该模块，生产路径判据也一并换成依赖图；新增 4 条测试（共享包、内嵌资源、helper、以及不承载任何二进制的文件），并钉死两条前缀表不得复活。

- 何时需要应用：
  - 怀疑某个热修包“应用成功”但现场行为没有任何变化
  - 需要核对补丁里究竟该包含哪些二进制

### 验证

- `ls -ld /run/clusterguard   # 必须是 drwxr-xr-x（0755），可被非特权引擎账户穿越`
- `ls -l /usr/local/sbin/clusterguard-upgrade   # 必须是 0750 root:clusterguard，mtime 为本次应用时间`
- `grep -c '热修补丁包' /usr/local/sbin/clusterguard-upgrade   # 必须为 1（控制台上传校验执行的就是这个文件）`
- `ls -l /usr/local/libexec/clusterguard-upgrade.sh   # 不应存在（HF-04 误装的位置，已改名为 .unused-*）`
- `systemctl cat clusterguard-update-helper.service | grep -c 'RuntimeDirectory=clusterguard'   # 必须为 0`
- `systemctl is-active clusterguard-mysql-3306 clusterguard-ha clusterguard-agent clusterguard-update-helper`
- `tail -20 /var/log/clusterguard/mysql/3306/error.log   # 不应再出现 Could not create unix socket lock file`
- `jq -c '.clusters[] | {recovery_freeze}' /var/lib/clusterguard/metadata.json   # 恢复完成后应为 false`
- `/usr/local/sbin/clusterguard-upgrade --patch /root/clusterguard-ha-hotfix-HF-2026-0928-05-2.2-104.x86_64.cgpatch --trust-key /etc/clusterguard/trust/patch-signing-public.pem --inspect   # 必须报「热修补丁包…apply.sh」而非「范围外路径」`
- `curl -sk https://192.168.102.155:3000/ | grep -c 'clusterguard-hotfix/apply.sh'   # 控制台页面必须已包含热修包拦截指引（浏览器需刷新）`
- `/usr/local/sbin/clusterguard-upgrade --patch /root/clusterguard-ha-hotfix-HF-2026-0928-06-2.2-104.x86_64.cgpatch --trust-key /etc/clusterguard/trust/patch-signing-public.pem --inspect   # 必须输出 kind=hotfix，而不是拒绝该包`
- `curl -sk https://192.168.102.155:3000/ | grep -c '包类型'   # 控制台页面必须已包含包类型字段（浏览器需刷新）`
- `curl -sk https://192.168.102.155:3000/ | grep -c '支持 .cgupgrade 滚动升级包与 .cgpatch 热修补丁包'   # 控制台上传指引必须已同时点名两种包`
- `sha256sum /usr/local/bin/clusterguard /usr/local/bin/clusterguard-agent /usr/local/libexec/clusterguard-update-helper   # 三条必须与本台账「交付内容」中 payload/bin/* 的 sha256 逐条一致`
- `ls -l /usr/local/sbin/clusterguard-clock-mesh.sh   # mtime 必须是本次应用时间，即现场已拿到修正版脚本`
- `grep -c 'Refusing to become the clock authority' /usr/local/sbin/clusterguard-clock-mesh.sh   # 必须为 1`
- `grep -c 'timedatectl set-timezone' /usr/local/sbin/clusterguard-clock-mesh.sh   # 必须为 1`
- `/usr/local/sbin/clusterguard-clock-mesh.sh --server --dry-run   # 必须拒绝并打印 --set-utc 与 --accept-current-time 两条出路，且不改动任何配置`
- `date -u +%Y-%m-%dT%H:%M:%SZ; timedatectl show -p Timezone --value; hwclock --show   # 拨钟之后：三台 UTC 与真实时间一致、Timezone 三台一致、hwclock 与 date -u 一致`
- `jq -r '.coordination_leases | to_entries[] | [.key, .value.lease.ha_endpoint_id, .value.lease.expires_at, .value.updated_at, .value.lease.active] | @tsv' /var/lib/clusterguard/metadata.json   # 关键验收：拨回 8 小时并重启控制面之后，expires_at 必须是当前 UTC + 约 60 秒，而不是当前 UTC + 8 小时`
- `journalctl -u clusterguard-ha --since "-3min" --no-pager | grep -c "stale topology observation"   # 必须为 0（修复前是每秒一条）`
- `jq -r ".observation_watermarks | to_entries[] | [.key, .value] | @tsv" /var/lib/clusterguard/metadata.json   # 水位必须已落到当前时钟，不再是 2026-09-29T11:27:36.730892013Z`
- `jq -r ".topology_snapshots | to_entries[] | [.key, .value.observed_at] | @tsv" /var/lib/clusterguard/metadata.json   # observed_at 必须是当前 UTC——不再「来自未来」，否则 ownership_keeper 会判 stale 并跳过续约`
- `stat -c "%y %s" /var/lib/clusterguard/metadata.json; sleep 30; stat -c "%y %s" /var/lib/clusterguard/metadata.json   # 在 Raft leader 上，mtime 与 clusterguard_state_revision 必须前进`
- `journalctl -u clusterguard-ha --since "-6min" --no-pager | grep -c "topology is stale or unavailable"   # 必须为 0（修复前每 5 分钟一条）`
- `curl -sk https://192.168.102.155:3000/ | grep -c '状态设置'   # 必须 ≥1（控制面二进制里的页面已含合并后的页签名）`
- `curl -sk https://192.168.102.155:3000/ | grep -c 'data-settings-section="account"'   # 必须为 0（旧的第四个页签不得复活）`
- `curl -sk https://192.168.102.155:3000/ | grep -c 'settings-account-block'   # 必须 ≥1（账户块已搬进状态面板内部）`
- `curl -sk https://192.168.102.155:3000/ | grep -c 'id="settings-account-panel"'   # 必须为 0（不再有独立的账户面板）`
- `浏览器强制刷新 https://192.168.102.155:3000/ 后打开设置页：页签必须是三个（状态设置 / 运行参数 / 版本更新），选中「状态设置」时能看到「账户与安全」与「显示偏好」，切到「运行参数」后二者离屏，切回后重现，「修改密码」仍能打开对话框，「界面语言」与「自动刷新」仍可操作`
- `node tools/console-settings-merge-acceptance.cjs   # 仓库内复核本次合并的真浏览器验收（需本机有 Chrome；只读，不连现场）`

### 回滚

执行 rollback.sh 恢复旧二进制、旧单元与旧运行时脚本，然后 systemctl daemon-reload 并重启 clusterguard-ha 与 clusterguard-update-helper。注意两点：① 回滚会把 clusterguard-clock-mesh.sh 恢复成“无校验即成为权威”的旧版本，并撤掉租约重锚——若此后再次回拨系统时钟，VIP 归属租约将再次长期不过期、自动接管再次被静默关闭；② /usr/local/libexec/clusterguard-update-helper 会回到 HF-2026-0928-06 之前的状态（该二进制此前从未被热修更新过）。本包对 /run/clusterguard、clusterguard-update-helper.service 与其它单元的回滚沿用 HF-2026-0928-05 / HF-2026-0928-06 的说明。 ③ 本包另加的水位容忍也会一并回滚：此后若再次回拨系统时钟，拓扑刷新将再次被永久拒绝——拓扑不再更新、租约停止续约、agent 摘掉 VIP 并把实例锁成只读，而现场无法自解。若确需回滚本包又曾回拨过时钟，回滚后必须同时把 observation_watermarks 与 topology_snapshots 手工对齐到当前时钟，否则集群会停在本次同样的停机态。 ④ 本包另加的设置页合并也会一并回滚：控制台回到四个页签、「账户与偏好」重新作为独立一页出现。这个改动只影响页面结构，没有数据或授权影响，回滚不需要额外的手工对齐。

## HF-2026-0928-02 — 2.2-103 现场修复合集（累积）：整机重启后集群不可用、写入者抖动、升级链前置、控制台原因与关机收尾

- 严重级别：P0
- 覆盖修复提交：`914c6c5`、`3c88289`、`4015f97`、`0e8ab48`、`f90f995`
- 构建树：`f90f995fb92c23d66723a5331742a551be7a93b6`（基线 `467e533` + 上述修复，不含其它提交）
- 适用版本：2.2-103 → 2.2-103+hf-2026-0928-02（x86_64）
- 产物：`release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-02-r5-2.2-103.x86_64.cgpatch`
- SHA-256：`347699772b068b4b65251fc63f6116e7ca747bb5b64517988494e5e96ce2963e`
- 产物身份：第 5 修订，替代 `b11c939fcaa581d7b8e6f2c65613f57ca3dc5e72990f4a3fbbf50e4bc60deb50`（`release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-02-r4-2.2-103.x86_64.cgpatch`）——前一个修订的理由断言本仓库的补丁二进制不可逐字节复现，并把原因归于构建路径。两条都不对：字节差异来自构建器每次注入的构建时刻，把该值固定后同一提交可以逐字节复现，实测记录在发布台账里。已签名的产物不得原地改写，所以由本修订纠正；并且从本修订起，签名清单里的理由只回答「为什么替换前一个身份」——不写路径（会迁移），也不写量化事实（会测错），这两类错误今天各作废过一份签名产物。前几修订的完整链条、取证命令与实测数据都在发布台账里。
- 源码差异：`src/HF-2026-0928-02-f90f995.patch`
- 交付内容：
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/bin/clusterguard-agent` → `/usr/local/bin/clusterguard-agent`（0755）
  - `payload/systemd/clusterguard-agent-reconcile.service` → `/usr/lib/systemd/system/clusterguard-agent-reconcile.service`（0644）
  - `payload/systemd/clusterguard-update-helper.service` → `/usr/lib/systemd/system/clusterguard-update-helper.service`（0644）
  - `payload/scripts/clusterguard-cluster-finalize.sh` → `/usr/local/libexec/clusterguard-cluster-finalize.sh`（0755）
  - `payload/scripts/clusterguard-mysql-install.sh` → `/usr/local/libexec/clusterguard-mysql-install.sh`（0755）
  - `payload/scripts/clusterguard-postgresql-install.sh` → `/usr/local/libexec/clusterguard-postgresql-install.sh`（0755）
  - `payload/installer/install_clusterguard.sh` → `仅安装器，现场无对应路径`（0755）
- 需要重启：`clusterguard-ha.service`、`clusterguard-agent-reconcile.service`、`clusterguard-update-helper.service`

### 本包概要

本包累积覆盖 2.2-103 基线缺失的五个修复，替代 HF-2026-0928-01。只装这一个包，不要与旧包混用：多个修复都替换 /usr/local/bin/clusterguard，叠加时结果取决于安装顺序。构建树是 2.2-103 基线加上这五个修复（移植分支 hotfix/2.2-103-fixes，f90f995），不含开发线上未发布的功能提交。新增交付类型：运行时脚本进 payload/libexec/（现场 /usr/local/libexec/），这类脚本在下次被调用时生效，不需要重启服务。

### HF-2026-0928-02.1 整机重启后整个集群起不来：共享运行时目录权限与恢复冻结永不释放（`f90f995`，P0）

- 现象：三台整机关机再开机后，控制台报「部分数据不可用：候选评估」「尚未发现主库」，三个实例全部显示「数据库未启动或不可达」，再次整机关机与故障切换都被阻断。clusterguard-mysql-3306.service 与 clusterguard-cluster-restore.service 双双进入崩溃重启循环，重启计数分别达到 2063/2069/2072 与 1034~1036。

- 根因：① update-helper 单元用 ExecStartPre 把共享目录 /run/clusterguard 建成 0750，而托管 MySQL 单元的 RuntimeDirectory=clusterguard/mysql/3306 嵌套在它下面、以非特权 mysql 用户运行（不属于 clusterguard 组）；/run 是 tmpfs，开机时 update-helper 先创建父目录，mysqld 连这层目录都穿不过去，建 socket 锁文件失败即 Aborting——InnoDB 其实已初始化成功，数据无损。安装当天能用只是因为碰巧 mysqld 先启动，开机顺序一反过来就是必现故障。② cluster-finalize 在 600 秒内等不到主库时以 exit 0 结束，而它是 oneshot + Restart=on-failure，退出码 0 等于宣告完成，恢复冻结此后只能人工解除——脚本自己打印的「修好后会自动恢复」并不成立。

- 修复：共享父目录改为 0755（保持可穿越），并让托管 MySQL/PostgreSQL 单元在自己的 ExecStartPre 里幂等修正该父目录，于是无论谁先创建父目录、无论开机顺序如何，引擎都能到达自己的 socket；cluster-finalize 的 fail-closed 超时路径改为 exit 1，让 systemd 每 30 秒重试，直到主库恢复并走完 power/complete 自动解冻。门禁同时修掉了把 0750 当成期望值的断言，并新增两条防回归检查（共享目录权限、finalize 退出码）。

- 何时需要应用：
  - 整机关机后重新开机，控制台报「数据库未启动或不可达」且没有主库
  - clusterguard-mysql-3306.service 反复重启，error.log 报 Could not create unix socket lock file
  - 计划关机或故障切换被阻断，power 生命周期停在 recovering 且 recovery_freeze 为 true

### HF-2026-0928-02.2 MySQL 写入者协调抖动：授权主库每 5 秒自隔离一次（`4015f97`，P0）

- 现象：集群长期 degraded、两条复制链路 unhealthy、候选评估 409、计划关机必被阻断。实测自安装起 9492 次自隔离，read_only 与 VIP 每约 10 秒同步翻转一次。

- 根因：① agent-reconcile 单元的 CapabilityBoundingSet 缺 CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH，root 也读不了 <datadir>/mysqld-auto.cnf（mysql:mysql 0640），IsolationStatus 报错即触发失败关闭；② convergeWritableRestartState 要求 RestartReadOnly 变为 false，但“重启后只读”是永久站点不变量，条件永不满足。

- 修复：收敛判定改为只看“隔离意图已清除 + 运行时可写”（writableRestartStateConverged），不再要求重启栅栏消失；单元补 CAP_DAC_OVERRIDE 与 CAP_DAC_READ_SEARCH（bounding 与 ambient 都补）。

- 何时需要应用：
  - 集群长期 degraded 且复制链路 unhealthy，但复制本身正常
  - journalctl -u clusterguard-agent-reconcile.service 反复出现 self-isolated 或 permission denied

### HF-2026-0928-02.3 升级执行链两个前置缺陷：SSH 私钥属主与 Helper 共用运行时目录（`3c88289`，P0）

- 现象：版本更新始终 available=false，安装/上传升级包的三个控件全灰；即便手工补齐 update.json，升级执行器仍以“私钥权限过宽”拒绝；Helper 首启报 status=233，且停机时把 /run/clusterguard 整个删掉。

- 根因：① update.json 的 ssh_key 指向 clusterguard 属主的私钥，而 workspace check-file 要求 root 属主且非组/其他可写；② Helper 单元用 RuntimeDirectory=clusterguard 声明了一个共用目录，启动 chown/chmod 存在竞态，停止时 systemd 会删除该目录。

- 修复：安装器额外生成 root:root 0600 的私钥副本 /etc/clusterguard/updates/controller_ed25519 并写入 update.json；Helper 单元去掉 RuntimeDirectory，改为 ExecStartPre 幂等创建共享目录（不再“拥有”该目录，停机不删）。该目录的模式在 28e3b47 中进一步定为 0755。

- 何时需要应用：
  - 控制台版本更新长期 available=false
  - clusterguard-update-helper 首启失败或停机后 /run/clusterguard 丢失

### HF-2026-0928-02.4 控制台不解释“不可用”的原因（`914c6c5`，P1）

- 现象：版本更新面板只显示红徽标“不可用”，不写原因；集群加载横幅只报栏目名（“部分数据不可用：候选评估；操作已锁定”），运维无法判断下一步做什么。

- 根因：前端丢弃了后端给出的原因：`evidenceResult` 只保留布尔状态，409 响应体里的具体原因没有回填到面板与横幅，运维只能看到状态标签。

- 修复：面板内联渲染 `#software-update-panel-reason` 并在徽标上加 tooltip；`evidenceResult` 携带 `reason`，`evidenceReasonText()` 把 409 消息映射为中文，`evidenceUnavailableText()` 按栏目回填原因。

- 何时需要应用：
  - 控制台版本更新面板显示“不可用”但无原因
  - 集群加载横幅只报栏目名、不报原因

### HF-2026-0928-02.5 整机关机提交后控制台卡在无法交互的对话框上（`0e8ab48`，P1）

- 现象：提交整机关机后主机断电、控制面随之消失，页面永久停在关机确认对话框上，只能手动关闭标签页；提交后响应丢失时还会误报为错误。

- 根因：poweroff 分支提交成功后只改了结果横幅，既不关闭对话框也不做收尾；主机断电后页面失去所有交互入口，而“已提交却丢了响应”本就是预期结果。

- 修复：新增 settlePoweroffConsole()：等 2.5s 后带 3s 超时探测 /power/status，不可达则显示离线层并 window.close()，仍可达则只关对话框并起 5s 看门狗；service 模式永不关页面。

- 何时需要应用：
  - 提交整机关机后控制台卡死、需要手动关闭标签页
  - 关机提交后偶发“无法连接控制 API”被当作失败

### 验证

- `ls -ld /run/clusterguard   # 必须是 drwxr-xr-x（0755），可被非特权引擎账户穿越`
- `systemctl cat clusterguard-update-helper.service | grep -c 'RuntimeDirectory=clusterguard'   # 必须为 0`
- `systemctl show clusterguard-mysql-3306 -p NRestarts   # 应用后应停止增长`
- `systemctl is-active clusterguard-mysql-3306 clusterguard-ha clusterguard-agent`
- `tail -20 /var/log/clusterguard/mysql/3306/error.log   # 不应再出现 Could not create unix socket lock file`
- `/usr/local/bin/clusterguard --version`
- `/usr/local/bin/clusterguard-agent --version`
- `systemctl show clusterguard-agent-reconcile.service -p CapabilityBoundingSet`
- `journalctl -u clusterguard-cluster-finalize --since '-10min' | tail   # 主库未恢复时必须非零退出并重试，不得打印 Succeeded`
- `jq -c '.clusters[] | {recovery_freeze}' /var/lib/clusterguard/metadata.json   # 恢复完成后应为 false`

### 回滚

执行 rollback.sh 恢复旧二进制、旧单元与旧运行时脚本，然后 systemctl daemon-reload 并 systemctl restart clusterguard-ha。注意：回滚会把 /run/clusterguard 重新交回 0750 的创建方，下一次整机重启会再次让集群起不来；仅在确认新版本有回归时使用，并在回滚后临时手工执行 chmod 0755 /run/clusterguard。

### 该补丁的身份历史

上面一节描述的是 `clusterguard-ha-hotfix-HF-2026-0928-02-r5-2.2-103.x86_64.cgpatch`。下列身份按不可变规则留在交付目录里，
**不是安装入口**，只作为「现场到底运行过什么」或「本补丁早先构建成了什么」的证据。
完整时间线见 `hotfixes/hotfix-publications.json`。

- **第 4 修订** `b11c939fcaa581d7b8e6f2c65613f57ca3dc5e72990f4a3fbbf50e4bc60deb50`（8,977,452 字节，已被取代）
  - 被替换的原因（原样引自它自己的签名清单）：第 3 修订（r3）的理由里写死了一个二进制的差异字节数，那个数字只对其中一个二进制成立，而且差异长度本身随构建路径变化。根因不是这一次算错，而是签名清单里放进了会随时间漂移的量化断言——清单一旦签名就不可改写，任何笔误都只能靠再出一个修订来纠正，代价是又多一份几乎相同的交付物。第 4 修订把这类细节从签名清单里移出去：清单只保留「为什么替换前一个身份」这一不会随时间变化的事实，实测数据放在发布台账（hotfixes/hotfix-publications.json）里，那是可修订的记录。前三个修订的链条与原始动机见台账。r0 至 r3 均未交付任何现场，其摘要与字节按不可变规则保留，标记为 superseded。已知限制：本仓库的补丁二进制不可逐字节复现，判断一份产物是否被换过只能靠台账登记的摘要。
- **第 3 修订** `13731cae03f04d605cc5d8f339947bd13f882a8149a14b402c719bffebebf764`（8,977,602 字节，已被取代）
  - 被替换的原因（原样引自它自己的签名清单）：起点是第 1 修订（r1）：它的签名清单把发布台账的位置写成 release/hotfix-publications.json，而台账随后迁入源码树（hotfixes/hotfix-publications.json，跟随分支传播），该指针因此指向一个不存在的文件。第 2 修订（r2）本意是纠正这句话，但它在理由里写下了「载荷与 r1 逐字节相同」——这句话不成立：两次构建的二进制大小与代码相同，但每个二进制有 63 字节不同，那是 Go 的 build ID（构建路径派生）。r2 已经签名，按不可变规则不得原地改写，于是由第 3 修订把话改对。自 r1 起构建输入未变：同一 base_commit、build_commit、fix_commits 与组件集合，本修订只动清单里的身份字段与这段散文。已知限制（独立于本次文案修订）：本仓库的补丁二进制**不可逐字节复现**，「同一 commit 重新构建得到相同字节」目前不成立，因此判断一份产物是否被换过只能靠台账登记的摘要，不能靠重构建对拍。r0 的字节已不可恢复；r0、r1、r2 均未交付任何现场，其摘要与字节按不可变规则保留，标记为 superseded。
- **第 2 修订** `4cf15ee118ed73bd28b854d4b331e4f083617b7bd527b52351f9ca26c9a4416d`（8,977,146 字节，已被取代）
  - 被替换的原因（原样引自它自己的签名清单）：第 1 修订（r1）的签名清单把发布台账的位置写成 release/hotfix-publications.json。台账随后迁入源码树（hotfixes/hotfix-publications.json，可随分支传播），r1 清单里的这个指针因此指向一个不存在的文件。已签名的产物不得原地改写——同一个文件名配上不同的字节，会让「现场运行过什么」失去唯一的凭证——所以这段文案由第 2 修订来纠正，而不是回去改 r1。本修订只动清单里的身份字段与散文：载荷（二进制、单元、运行时脚本、源码 diff）与 r1 逐字节相同。r1 已登记但从未交付任何现场，故标记为 superseded，其摘要与字节按不可变规则原样保留。
- **第 1 修订** `62bc8c7cffaced9c5fda113961bc4d50b9e5e55656945aa977fd615fdb388dea`（8,976,887 字节，已被取代）
  - 被替换的原因（原样引自它自己的签名清单）：原身份（r0）的字节在 2026-09-29 17:47 被一次「同名原地重建」覆盖，且三个现场节点都没有留存 2.2-103 线的补丁包，无法像 HF-2026-0929-03 / -04 那样从现场取回，因此 r0 已不可恢复。本修订用修正后的生成器重新构建（生成的 rollback.sh 改为 mktemp + mv -f 换 inode，且只认本补丁自己的备份清单），并以新文件名与新摘要发布；r0 的摘要保留在 release/hotfix-publications.json 作为它曾经存在过的唯一记录。

