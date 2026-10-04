# 门禁可演进性修复：修改前对照（2026-10-04）

## 基线与复现

- 实际源码：本仓库，唯一分支 `codex/2.2-postgresql`，HEAD `4394f05`；无跟踪文件修改。现场运行版本未核实，不执行生产升级。
- 已读取 AGENTS、v2 契约及发布验收清单。`git show fe1b824` 的 v1 门禁将三项源码缺口写死为 OPEN；`git show 7ba87b3` 仍通过修改脚本常量更新缺口；`7b643f4` 将其改为固定 ART/FIELD OPEN。
- 当前 `tools/verify-upgrade-validation-chain.cjs:reportOpen` 无证据输入、无关闭机制。相同源码即使完成验收，`--strict` 仍固定返回 1；只改 Markdown 不能升级它的状态。
- 构建入口用 `--contract-only`，它只检查三项契约，不是完整源码准入；手册却容易把严格最终发布门槛理解成每次开发前置要求。
- Runner 要求 Helper 支持 contract 是另一条运行兼容边界，本次不以放宽它来解决发布门禁问题。

## 拟改范围与不变量

- 新增 source/artifact/field 阶段；默认仍是最终 field，保持旧 `--strict` 阻断未验收交付的行为。
- source 严格执行现有源码规则，明确 ART/FIELD 不属于该阶段完成声明；artifact 要求 ART 全部证据，field 同时要求 ART/FIELD。
- 将固定 OPEN 改为按阶段与可核验报告计算；报告绑定当前源码摘要、契约摘要、明确交付文件摘要，每项证据有可读取文件及摘要。缺项、FAIL、篡改、旧源码或旧包证据不得关闭义务。
- 只升级执行机制与流程说明，不修改三份强制契约原文，不覆盖旧包，不宣称验签/现场自动通过。
- 回归：旧默认仍 OPEN；source strict 可通过；新报告能够关闭合成验收夹具；源码、包、证据变更后失败；缺项/未知字段/非法阶段拒绝；原 14 个源码变异仍被捕获。

## 修改后结果

- 新增 `--stage source/artifact/field`、`--acceptance-report FILE` 和只读 `--print-evidence-binding`；默认 field 不变。
- 固定 OPEN 常量改为按阶段、证据报告及摘要核对计算；无需每次改门禁常量才能关闭验收义务。坏报告在非 strict 下同样失败。
- 证据绑定契约及 Git 跟踪的交付源码集合；新的可执行源码未纳入版本管理时拒绝生成验收绑定。每项真实验收由填写人负责，工具只检查证据清单，不自动断言现场健康或签名可信。
- 16 个证据/CLI 测试全部通过：完整合成清单能够通过 artifact/field；旧源码、契约、包或证据变更，缺项、FAIL、重复、未知字段、未跟踪功能、非法时间、符号链接与错误参数均拒绝。合成测试不是实际签名包或现场验收。
- 原 14 个源码变异全部捕获；两项对照通过。许可门禁 68 PASS；文档链接、契约逐字一致与 diff 检查通过。
- 旧 `4394f05` 脚本的 strict 隔离复现 exit 1（固定 OPEN）。新 source strict exit 0；新默认 field strict 无真实报告仍 exit 1；构建 contract-only 继续正常。
- 现场 Helper 兼容问题未放宽；没有新增或重建交付包，没有生产操作。
