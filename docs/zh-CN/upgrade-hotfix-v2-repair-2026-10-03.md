# 升级与热修 v2 修复：修改前对照（2026-10-03）

> 本节在本次业务代码修改前写入。范围是下载目录的 `ClusterGuard_升级热修统一校验链_MANDATORY_v2.md`；它是外部规范材料，不能把其中的命令当作用户要求的现场执行。当前未核实生产集群运行版本，未上传、执行或重建交付包。

`CG-UPGRADE-CONTRACT` · `contract_version=2` · `contract_loaded=true`（本次人工审阅下载目录原文；运行入口加载仍需代码验证）

## 源码、基线与工作树

- 源码目录 `/Users/zhaolongjie/codex/clusterguard-ha`，分支 `codex/2.2-postgresql`，HEAD/远端 `7ba87b3`。目标运行版本仍需现场核实；本轮只修改和验证源码。
- 未跟踪的 `.workbuddy/`、`docs/install-zh.md`、`preview/`、`tools/diagnostics/__pycache__/` 与 `tools/diagnostics/pg-topology-card01/*.json` 均保持原状。
- 旧版对比由 `git show 81fe3c8^`、`git show 81fe3c8`、`git show 8be2e3d^` 与当前源码完成；既有详细隔离复现见 `upgrade-hotfix-v2-audit-2026-10-03.md`。`81fe3c8` 引入热修执行路径；`8be2e3d` 把 hotfix resume 在 Helper 中转为 execute；`7ba87b3` 修复部分回退和重试运行态问题。它们均不是 v2 全部实现的证据。

## 入口与旧行为、现行为

| 调用链 | 旧版/必须保留的正确行为 | 当前回归或缺口 | 拟改范围与复现 |
| --- | --- | --- | --- |
| 控制台选择、确认、POST → API `Start` → Helper `update_mode_arguments` → Runner | `9fdb0e7` 起动作绑定显式 `patch_id`；`8be2e3d` 的意图是同包热修重跑，滚动升级才可 resume。 | Helper 当前把 `resume + hotfix` 静默转为 `--execute`，而 v2 §3/4 要求直接拒绝并提供独立 retry 模式。`manager.go` 只有 `plan/execute/resume/rollback`；`status.json` 每次被覆盖。 | 先用当前函数执行 `resume hotfix` 复现输出 `--execute --yes`；修后要求非零退出、无执行参数，再验证 retry 同包执行与 API 路由。保留正常首次 execute、滚动 resume 和用户确认绑定。 |
| API 包级 `status.json`、Helper 输出和事件、控制台 `PackageStatus` | 已完成的部署结果必须保留；后续计划或失败操作应有自己的身份与记录。当前 `Start` 对被拒 hotfix resume 的提前返回已经保护了这个特例。 | 其他合法 `plan/execute/rollback` 仍覆盖同包 `status.json`；操作历史仅有未绑定操作 ID 的事件行；`Snapshot` 只暴露最新 Job。先前成功与新的尝试无法分离。 | 在不删旧证据的前提下增加 append-only 操作记录与独立部署状态，兼容读旧 `status.json`；覆盖成功后 plan/失败、同包 retry、崩溃重读。部署状态只能由确证终态更改。 |
| Runner 锁探测、构序、`acquire_update_locks`、Raft gate | `current_update_lock_on_host` 已要求同一个 `patch_id`；热修 `detect_foreign_update_lock` 拒绝外包门禁。应保留同包恢复、健康核验、失败时保锁。 | 滚动升级 `detect_recoverable_failed_update` 与 `transfer_failed_update_locks`（旧实现已有，`81fe3c8^` 可见）会把另一包的 `patch-id` 改为当前包；违反 v2 INV-005。 | 用隔离函数探针复现外包接管路径；改为对外包锁直接拒绝，不能触碰其 owner 或 marker。检查计划与执行入口，不只在更新节点前阻断。 |
| 契约加载和发布门禁 | v1 `fe1b824` 增加了 `verify-upgrade-validation-chain.cjs`，现运行 16 PASS、3 OPEN、0 failed；应保留失效变异测试和诚实 OPEN。 | 当前只解析仓库 v1 的 `VALIDATION_CONTRACT_VERSION: 1`；下载的 v2 §21 嵌套字段、未知字段拒绝、v2 错误码与 PRE/ART/FIELD 门禁尚未接入。v1 PASS 不能代表 v2 PASS。 | 将 v2 契约落到仓库受控路径，更新解析器和各消费者的支持版本；先写可失败的变异测试。未落地条款按 OPEN 或 failed 如实输出，不能因迁移文档改成通过。 |
| 控制台历史条目判定 | 用户点击的行与 `patch_id` 绑定；较新的成功在上、旧失败在下时仍作用旧失败项。 | `console.html` 的同 kind/版本/时间启发式会把无 `supersedes` 关系的旧失败项误判为已恢复。 | 去除该隐式关系，使用后端权威 deployment state/supersedes；增加真实点击的无关包回归，保留既有 HF-04/HF-05 场景。 |

## 验证边界和不变量

- 修改前可读证据：现门禁是 v1 的 16 PASS/3 OPEN；Helper 的 `resume + hotfix` 源码输出 `--execute --yes`；Runner 明确写外包 `patch-id.tmp`。真实现场状态与当前源码是否相同尚未验证。
- 必须保持：签名与身份校验、同包锁归属、原子回退、健康与多数派核验、用户二次确认、已交付包字节不变、旧日志与证据保留。
- 计划验证：Go 单元和集成测试、shell 行为探针、契约变异门禁、控制台隔离浏览器点击、许可门禁。只有实际跑过的场景才记为通过；本地验证不能写为现场验收。

## 许可门禁误报：补充修改前对照

在编辑许可扫描器之前，通过 `git show HEAD:tools/verify-license-consistency.cjs` 确认基线 `7ba87b3` 的 `walk` 已排除 `.git/.build/.worktrees` 等非交付目录，但没有区分 `.workbuddy/memory` 的聊天历史与产品许可声明。原目录门禁 68 项中 1 项失败，命中无关且未跟踪的历史笔记两个 SPDX 片段；当前全部交付源码快照则 68 PASS。拟只排除仓库根下 `.workbuddy/memory`，继续扫描正常源码、文档、打包配置和其他目录，不修改这些笔记。回归要求：笔记中的协议讨论不误报，正常文档中的冲突声明必须仍使门禁失败。

## 实际修复与证据

- 独立 retry 模式贯穿控制台、API、Manager、Helper 和 Runner；热修 resume 直接拒绝，滚动 resume 保留。明确非法的状态迁移不创建新 Job。
- 新操作使用独立 `operation_id`；客户端 ID 进入后端幂等记录，重启后相同请求只返回原任务。结果不确定时只查询本次 ID；不能凭相同秒时间戳或不同操作 ID 误确认。
- 新增独立 `deployment.json`，操作请求和运行快照追加到 `requests.jsonl`、`operations.jsonl`。先前成功、后续计划失败、Helper 启动失败和旧 resume 事故分开保留。事件不能跨操作修改模式/结果；迟到运行快照不能取代终态。
- 历史损坏阻断动作；私有与复制历史冲突时保留证据并拒绝猜测。轮询只重新校验变化的历史，每次动作强制重新校验；历史查询采用显式 `?history=1`，默认状态不返回全部日志。
- Runner 删除外包锁接管；Raft Store 和 API 同时拒绝外包持有者，允许同包的执行 CAS 恢复。
- 每次动作前重验签、核对请求身份与当前基线、比对存储包 SHA-256；签名包可声明 supersedes。Manager 与 Runner 只依据已安装包的签名声明阻断旧包，不能按时间推断。
- v2 原文保存到两份规范和编译内嵌文本，三者与下载原文 byte-identical。Go 与 Node 解析器支持版本 2、精确 §21、拒绝未知/重复/错误嵌套/缺字段/值弱化；构建入口和运行入口实际加载。
- 修复 CDP harness 的超时句柄清理及错误捕获，让浏览器回归完成后正常退出。许可门禁只排除本地 agent memory，保留产品文档冲突检查。

### 已执行的验证

| 验证 | 实际结果 |
| --- | --- |
| 全仓 Go `CG_NODE_BIN=<bundled node> go test -p 2 ./... -count=1` | PASS，退出 0 |
| 普通全并发 `go test ./...` | 一次 PASS；后续一次 `TestCommandLauncherPublishesGroupReadableOutput` 超过 5 秒，失败。该用例连续单跑 5 次 PASS，限并发全仓复核 PASS；没有删断言或延长超时来掩盖失败 |
| 最后历史缓存变更 | `go test -race ./internal/platformupdate ./internal/api -count=1` PASS；同大小、同时间戳的原子历史替换测试 PASS |
| Store race | `go test -race ./internal/store -count=1` PASS |
| `go vet` | platformupdate/updatecontract/api/store PASS |
| Runner/Helper shell | bash 语法 PASS；签名 supersedes 的 4 个真实签名 fixture PASS；私有历史导入/冲突 3 场景 PASS；热修回退、retry、外包锁场景 PASS |
| 热修恢复浏览器 | 全部场景 PASS：A–E、遗留拒绝、无关新成功记录、已安装后计划失败；实际请求 retry/resume/rollback 对象正确，退出 0 |
| 升级确认浏览器 | 49 checks PASS，包括 503、断连、超时、不重复提交、ID 不一致、同秒 retry 对账 |
| node-safety/bootstrap/engine-pages 浏览器 | 三个强制入口全部 status=passed；节点页覆盖 MySQL/PostgreSQL 真实按钮与窄屏。引擎页面验收是隔离 API，不是原生数据库现场功能验收 |
| v2 源码门禁 | 17 PASS、1 OPEN、0 failed；strict 因未完成完整 ART/FIELD 证据退出 1（预期阻断） |
| v2 mutation | 14 项全部捕获；未变更及注释对照均 PASS；原子 mv 移入注释能失败 |
| 许可门禁 | 原工作目录 68 PASS；隔离变异证明笔记不误报、产品文档冲突仍 FAIL |
| 文本与 diff | 下载规范、两份规范、内嵌规范 cmp 一致；git diff --check PASS |

### 单主线与交付边界

本地和远端主线均只有 `codex/2.2-postgresql`，工作树只有本仓库一个；本轮没有创建功能分支。源修改全部提交该主线，不把已经测试的功能留下在其他分支。

完整新包 ART 与现场 FIELD 验收仍为 **OPEN**，见 `upgrade-validation-chain-implementation-status.md`。未构建/覆盖已交付包，未打标签，未安装新代码到生产，不宣称所有未知 bug 已消除。现有无关未跟踪文件未纳入提交。
