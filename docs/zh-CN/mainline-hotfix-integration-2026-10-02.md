# 2026-10-02 主线热修回归修复前对照

> **后续状态（2026-10-03）：** 本文保留当时的基线、方案与验收事实。当前主线 `7b643f4` 已实现 v2，热修 resume 直接拒绝，失败热修走 retry；不同包的成功不再按版本/时间推断替代关系。独立部署与操作历史保护既有成功。仅保留 `codex/2.2-postgresql` 一条本地/远端主线；文内旧分支与旧门禁数字属于历史。当前证据与 OPEN 见[实现状态](upgrade-validation-chain-implementation-status.md)，生产是否已包含该源码仍未重新验收。

## 基线与问题

- 主线为远端默认分支 `codex/2.2-postgresql`；本地合并提交 `73c7f05`，远端目前为 `8fa4817`。当前工作树只有与本次无关的未跟踪预览和诊断文件。
- 用户要求完成修复后合入主分支。`codex/hotfix-history-kind` 已成为 `73c7f05` 的第二父提交；旧 `codex/hotfix-console-105` 和 `hotfix/2.2-103-fixes` 的独有提交需按行为核对，不把旧发布线或 WIP 原型直接覆盖到新主线。
- `a256c37` 在 `internal/api/console.html` 的 `softwareUpdateActionable` 中加入收敛规则：控制面明确报告维护门禁已解除，且同类型、同来源版本已有后续执行成功时，较早的失败热修仍留在历史，但不再占据当前操作对象。它修复 HF-05 成功后 HF-04 仍显示为待处理的回归。
- `e875a6a` 的 `tools/console-update-hotfix-recovery-acceptance.cjs` 在该规则之前写成：同时放入 HF-05 成功和 HF-04 失败记录，使用 fixture 默认 `update_maintenance_active=false`，却仍要求点击并提交 HF-04。合并后 HF-04 正确退入历史，按钮不能点击，驱动等不到确认框并触发 120 秒 watchdog。上一轮实际运行中，前五个浏览器场景通过，第六个在 `acting below a newer successful record` 超时。

## 保持的不变量与拟改范围

- 门禁明确解除且较新热修成功：HF-04 留在历史，当前摘要和操作对象指向 HF-05；不得为了让旧测试通过而恢复 HF-04 的可执行状态。
- 门禁仍活动或状态未知：旧失败记录不得被静默隐藏；确认框和请求的 `patch_id` 必须与页面描述的记录一致。滚动升级失败时仍提供续跑，热修失败时提供重新执行。
- 只调整浏览器验收 fixture 与场景断言，让“门禁释放后的历史收敛”和“仍需处置时操作对象绑定”分别有实际页面点击覆盖；不修改后端验签、维护门禁、现场作业记录或已交付包。
- 验证 `tools/console-update-hotfix-recovery-acceptance.cjs` 全场景、相关 Go 测试、`tools/verify-upgrade-validation-chain.cjs` 与 `git diff --check`。浏览器 fixture 不能替代真实现场验收。

## 关联测试暴露的客户端优先级问题（修改前补充）

- `go test ./scripts` 在本机失败：`TestAdapterRuntimeUsesManagedMySQLClientAndWritesReadinessMarker` 创建了受管 MySQL 客户端，但脚本链接到 `/opt/homebrew/Cellar/mysql@8.0/.../bin/mysql`。旧实现可追溯到 `b33b259` 的 `scripts/clusterguard-adapter-runtime-install.sh:prepare_mysql`，现主线仍按“显式客户端、既有链接、PATH 上的客户端、受管客户端、包内客户端”依次选取；测试预期受管客户端优先于系统 PATH。
- 正确行为：保留 `CG_MYSQL_CLIENT` 显式指定和现有有效链接的优先级；首次准备时优先使用本机受管安装树，再退回系统 PATH 和包内客户端。否则有 Homebrew 或系统 MySQL 的主机可能在受管客户端存在时仍链接到错误的版本，且跨机器行为不同。
- 拟改范围只限 `prepare_mysql` 候选顺序；用当前失败的原测试及 `TestAdapterRuntimeFailsClosedWithoutRequiredClient` 验证，不改变 readiness marker 格式或 PostgreSQL 分支。

## 分支核对与修改后验证

- `codex/hotfix-history-kind` 已由 `73c7f05` 正常合并。`codex/hotfix-console-fix`、`codex/mysql-feature-parity`、`codex/mysql-topology-intelligence`、`codex/phase1-control-kernel` 与 `codex/platform-auth-session` 的提交已是主线祖先。
- `hotfix/2.2-103-fixes` 属于旧发布线；主线的 `5ae2039` 与 `28e3b47` 已移植其断电页面收敛和共享运行目录修复。浏览器验收工具、update-helper unit 与 finalize 脚本在两条线间逐文件相同；不把旧发布线的整段历史合并到 2.2-105 主线。
- `codex/hotfix-console-105` 的热修控制台能力已经由主线 `81fe3c8` 和后续修复实现。旧分支的热修 `--resume` 方案与当前强制校验链的“热修重新执行，滚动升级续跑”规则冲突，因此保留为历史调查分支；`codex/hotfix-console-experiment` 明确是 WIP 原型。
- 修正浏览器 fixture 后，`tools/console-update-hotfix-recovery-acceptance.cjs` 的全部场景在本机 Chrome 通过：门禁已解除时旧失败仅留在历史；门禁仍活动时执行和回退均提交页面所示 HF-04 ID；热修历史动作显示“热修应用”，普通升级仍显示“滚动升级”。此验证只访问本地 fixture。
- `go test ./scripts -run '^TestAdapterRuntime' -count=1`、`go test ./scripts -count=1` 与 `go test ./...` 均通过；`tools/verify-license-consistency.cjs` 为 68 checks passed；`tools/verify-upgrade-validation-chain.cjs` 为 16 checks passed、0 failed，仍有 3 项历史账本义务标为 OPEN。
