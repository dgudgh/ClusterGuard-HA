# 升级与热修 v2 代码审查（2026-10-03）

> **后续状态（2026-10-03）：** 本文保留当时的基线、方案与验收事实。当前主线 `7b643f4` 已实现 v2，热修 resume 直接拒绝，失败热修走 retry；不同包的成功不再按版本/时间推断替代关系。独立部署与操作历史保护既有成功。仅保留 `codex/2.2-postgresql` 一条本地/远端主线；文内旧分支与旧门禁数字属于历史。当前证据与 OPEN 见[实现状态](upgrade-validation-chain-implementation-status.md)，生产是否已包含该源码仍未重新验收。

## 范围与基线

- 规范：`/Users/zhaolongjie/Downloads/ClusterGuard_升级热修统一校验链_MANDATORY_v2.md`。
- `CG-UPGRADE-CONTRACT`，`contract_version=2`，`contract_loaded=true`。
- 源码：主分支 `codex/2.2-postgresql`，HEAD `174d6a8`；与已测试的 `d614c0d` 文件树一致。已有未跟踪文件不属于本次审查。
- 任务为代码审查与隔离复现。本次不执行现场上传、升级、回退，也不修改业务代码或已交付包。
- 对照证据：现校验器支持版本为 1；`81fe3c8` 引入热修执行路径，`c05f8b2` 修改回退生成器的原子替换；`9fdb0e7` 修改界面状态和目标绑定，`8be2e3d` 修改热修 resume 路由，`a256c37` 修改旧失败记录筛选，`d614c0d` 调整相应浏览器 fixture。
- 检查调用链：控制台 → API → `platformupdate.Manager.Start` → Helper → `clusterguard-update-job.sh` → `clusterguard-upgrade.sh` → 包内 apply/rollback；同时检查构建器、发布账本与门禁。

## 复现方式

在 `.build/v2-contract-audit/` 保存审查探针和输出。探针提取当前源码函数或生成器，网络、SSH、systemd 等动作以显式 stub 替代；文件破坏测试只操作新建临时目录。Go 回归测试在 `scripts/hotfix_v2_test.go`，通过 shell 子进程执行当前生产函数与生成的回退函数。探针证明控制流和持久化问题，不宣称现场已经重现。

## 初步检查点

- 受控热修回退的成功分支是否在写终态前执行健康核验和维护释放。
- 生成的回退脚本如何处理部分备份清单、缺失备份和重试后的备份选择。
- 文件摘要已匹配但服务尚未重启时，retry 是否仍补齐运行态核验。
- 新计划/失败操作是否会覆盖已有成功部署结果。
- 热修 resume 是否在所有入口一致拒绝；是否存在外包维护锁转移。
- 前端是否用同发布线的任意后续成功替代明确 supersedes/deployment 状态。
- v2 的加载、未知字段拒绝、错误码和强制阻断是否真正进入运行与发布入口。

## 修改前旧代码对照（2026-10-03，业务代码尚未修改）

| 路径 | 基线与正确行为 | 当前行为、引入位置 | 复现和拟改范围 |
| --- | --- | --- | --- |
| 热修受控回退 | 同一函数自动回退路径 `scripts/clusterguard-upgrade.sh:2127-2141` 在写 `rolled_back` 前执行 `verify_cluster_idle` 和 `finish_update_maintenance`，释放本地及 Raft 门禁；`finish_update_maintenance` 是统一释放函数。 | `81fe3c8` 首次引入热修路径；当前受控回退 `:2083-2088` 只写成功并返回，`retain_update_locks=true` 使退出清理不释放。 | 提取当前 `run_hotfix_update` 及 stub 执行：`journal=running,rolled_back`，`gate_active=true`，`release_calls=0`。拟只复用自动回退的健康核验、释放、失败终态模式。 |
| 包内回退脚本 | `c05f8b2^` 版本使用 `cp -p` 覆盖运行文件（错误）；`c05f8b2` 改为同目录临时文件加 `mv`（正确行为须保留）。已有备份清单区分「有目标且空备份」与「无目标行」。 | 旧版至当前均仅取 awk 第二列，两个状态同为空；无行即 `rm -f`，备份路径丢失只打印并成功退出。生成器 `scripts/build-hotfix-patch.sh:498-520`。 | 提取当前生成器构建的 `rollback.sh` 函数，在临时目录里：缺行删除已有文件；缺备份仍报告成功；有效备份恢复正常。拟只使缺行和缺备份失败关闭，保留明确空备份行时删除及原子替换。 |
| 热修重试已匹配文件 | `81fe3c8` 的节点更新路径会执行 apply、服务重启、ready 检查；`scripts/clusterguard-upgrade.sh:1933-1941` 定义了运行态核验。 | 当前 `:2103-2106` 只凭文件摘要跳过完整节点路径，最终仍写 `succeeded`，未证明进程加载了补丁。 | 提取当前函数 stub 重试，三节点摘要匹配时 `restart_calls=0`、`ready_calls=0`、`journal=succeeded`。拟在跳过 apply 时仍检验声明服务活动、节点 ready、集群状态；不得通过重启扰动已经健康的服务。 |

必须保持：同包门禁接管与外包阻断；失败的回退保留门禁；执行中的已应用节点倒序回退；二进制原子替换；签名和摘要校验；成功的真实热修不提供 resume；任何已交付包字节不变。修改只限源码和必要回归，不重建历史包。隔离探针不等于现场验收。

## 已修复的代码缺口（仅源码，尚无新交付包）

1. **受控回退虚报维护释放，P0。** `scripts/clusterguard-upgrade.sh:2083-2088` 原先在 `rollback_hotfix_nodes` 成功后立即写 `rolled_back` 并退出；退出清理保留门禁。隔离复现原结果为 `journal=running,rolled_back; gate_active=true; release_calls=0`。现复用 `finish_update_maintenance`，释放失败写 `rollback_lock_release_failed` 且保留门禁；成功后再核验控制面。回归：成功 `gate=false; releases=1`，释放失败 `rollback_lock_release_failed; gate=true`。
2. **生成的回退脚本可能删掉未处理的原有文件，P0。** `scripts/build-hotfix-patch.sh` 原先把「备份清单无该目标行」和「明确记录补丁前不存在」都解释为空备份，直接 `rm -f`；备份文件丢失也只 warning 并返回成功。现清单首行绑定 package ID，遍历同包清单取目标第一次备份；无行、身份不符、备份丢失均阻断，明确空备份才删除。临时文件加原子 `mv` 保留，恢复后对比原备份内容；清单名加入纳秒并禁止已有清单覆盖。隔离复现原来缺行删除已有文件、缺备份保留补丁内容仍返回 0；修后两种均拒绝且保留原文件，同包重试取最早的原始备份。**已交付旧包仍含旧脚本，不可原地重建；需新补丁 ID 才能下发修复。**
3. **摘要全匹配时可能跳过运行态，P0。** `scripts/clusterguard-upgrade.sh:2103-2106` 原先直接 `continue`，最终仍写成功；隔离复现三节点 `restart_calls=0; ready_calls=0`。现同包重试即使文件已匹配也重新核对锁归属、重启声明服务、验证节点 ready、集群 idle 并重解析 Leader；失败保留维护门禁。隔离回归三节点 `restart_calls=3; ready_calls=3`，就绪失败 `failed; gate=true`。

自动化：`scripts/hotfix_v2_test.go` 从当前生产函数和当前构建器提取实际代码执行，覆盖以上成功与故障路径。它不依赖网络、真实服务或生产目录。

## v2 未完成的硬阻断（未修改，不能算通过）

| 优先级 | v2 条款 | 当前源码证据与影响 |
| --- | --- | --- |
| P0 | §0、§21、INV-010 | `tools/verify-upgrade-validation-chain.cjs:30` 只支持版本 1，读取仓库 v1 文档；Downloads v2 尚未成为运行入口加载的契约，未知字段也未严格拒绝。当前默认门禁 `16 passed / 3 OPEN / 0 failed`，针对的是 v1，不能证明 v2。`--strict` 因历史 3 项失败。必须先实施 v2 schema、消费者与变异门禁，缺约定返回 `CG_CONTRACT_UNAVAILABLE`。 |
| P0 | INV-006、§2.2/2.3、§3、§17 | `internal/platformupdate/manager.go:483-532` 用单个 `status.json` 保存最新 Job；`ModePlan` 在已有成功 Job 后仍可写入 queued，随后可覆盖成功结果。无独立 Deployment State、operation_id、append-only operation log 或 `ModeRetry`。拒绝 hotfix resume 的局部修复仍在，但不足以满足历史不变。当前门禁自己将三项历史义务标 `OPEN`。 |
| P0 | INV-004、§3、§4 | `scripts/clusterguard-update-job.sh:184-197` 的 `resume + hotfix` 仍静默改为 `--execute --yes`；API Manager 已拒绝此组合，但 privileged helper 的直接入口未按 v2 直接拒绝且没有专用 retry mode。 |
| P0 | INV-005、§6 | `scripts/clusterguard-upgrade.sh:1392-1449` 的滚动升级接管路径会把**另一个**失败包的 lock `patch-id` 改写为当前包。即使有条件和补偿，v2 明文要求外包锁阻断。 |
| P1 | INV-001/002、§16 | `internal/api/console.html:3558-3588` 用同 kind、source_version、稍晚 succeeded 推断失败项已被恢复，没有 `supersedes` 或权威 deployment_state 证据。直接执行当前函数：HF-A 失败，独立 HF-B 成功，未声明 supersedes，门禁 false，HF-A 仍被标为 `actionable=false`。当前真实浏览器 fixture 对既定 HF-04/HF-05 情形通过，但未覆盖无关系的两个包。 |
| P1 | §8、§10-15、§19 | 完整回退仍缺签名来源包的原始逐文件摘要与版本核验、操作维度的备份 identity、现场断言及 11 个 `CG_*` 错误码映射。当前改动只阻断已复现的破坏路径，不能宣称满足 v2 整体。 |

## 验证与限制

- `bash -n scripts/build-hotfix-patch.sh scripts/clusterguard-upgrade.sh`：通过。
- `go test ./scripts ./internal/platformupdate -count=1`：通过（scripts 100.543s，manager 3.802s；生成器测试在此轮因未找到 Node 而跳过，随后已用 `CG_NODE_BIN` 单独实跑）。
- `CG_NODE_BIN=/Users/zhaolongjie/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node go test ./scripts -run 'TestHotfixRollbackAndRetryCompleteTheirRuntimeChecks|TestGeneratedHotfixRollbackFailsClosedOnIncompleteBackup' -count=1 -v`：两组实际执行并通过；未设置 `CG_NODE_BIN` 时生成器测试会跳过。
- `tools/console-update-hotfix-recovery-acceptance.cjs`：隔离 Chrome 场景逐项 PASS，包括 HF-04 位于新成功 HF-05 下方时的执行与回退目标；脚本打印总通过后未自行退出，手动中断了残留进程。属于隔离浏览器测试，不是现场验收。
- `tools/verify-upgrade-validation-chain.cjs`：v1 的 16 PASS、3 OPEN；`--strict` 退出 1。**v2 门禁未通过。**
- `tools/verify-license-consistency.cjs` 在当前工作区退出 1，原因是预先存在的未跟踪 `.workbuddy/memory/2026-09-22.md` 文本被扫描为相反许可声明。对 HEAD 加本次变更的隔离源码快照运行，68 项通过；原工作区门禁仍是失败，不得写成通过。
- 未做生产现场上传、验签、三节点滚动或回退，未生成或修改任何已签名交付包。需要新 ID 的候选包、产物与变异门禁、现场验收之后才可宣称交付。
