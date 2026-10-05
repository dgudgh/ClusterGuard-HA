# 升级与热修门禁的阶段和更新流程

维护日期：2026-10-04。规则仍来自[强制契约 v2](upgrade-validation-chain.md)，本文说明如何执行和维护门禁，不修改契约内容。

## 修改前必读，先于模块导航

本文是任何修改开始前的必读门禁入口。先确定本次适用门禁及验证计划，在任务 Markdown 记录已读文档和适用阶段，再按模块读取具体功能。

涉及升级、热修、重试、续跑、回退、控制台更新流程、操作管理、维护锁、构建、签名或发布时，修改/执行前必须完整读[强制契约 v2](upgrade-validation-chain.md)，并按 §20 记录 `CG-UPGRADE-CONTRACT`、`contract_version=2`、`contract_loaded=true`。强制契约是规范，本文是执行指南；功能文档不得降低门禁要求。

未读适用门禁不得开始对应修改或动作；未完成门禁不得宣称对应阶段通过。普通文档改动执行适用的文档/许可校验，源码、新包和现场任务分别执行下表阶段；提高阅读优先级不等于每次都执行全部阶段。

## 为什么旧门禁一直无法完成

此前脚本把 ART/FIELD 未完成写成常量 OPEN，没有读取验收证据的入口。修改 Markdown、完成现场测试或增加功能都无法关闭它。源码检查与最终验收又共用一个 strict 入口，导致开发完成和现场完成容易混淆。

现在按阶段执行；默认阶段仍为 `field`，兼容旧最终验收的阻断行为。

| 阶段 | 命令 | 通过表示什么 |
| --- | --- | --- |
| 源码 | `node tools/verify-upgrade-validation-chain.cjs --stage source --strict` | 现有 17 项源码规则成立；不声明新包或现场通过 |
| 新包 | `node tools/verify-upgrade-validation-chain.cjs --stage artifact --strict --acceptance-report /private/acceptance.json` | 源码规则及 ART-001..013 证据清单均满足；不声明现场通过 |
| 最终验收 | `node tools/verify-upgrade-validation-chain.cjs --stage field --strict --acceptance-report /private/acceptance.json` | 源码规则与 ART-001..013、FIELD-001..012 的验收记录及证据引用均满足 |
| 契约预检查 | `node tools/verify-upgrade-validation-chain.cjs --contract-only` | 仅契约加载与兼容性，不能代替上述阶段 |

源码阶段明确将 ART/FIELD 标为 DEFERRED，禁止用其 exit 0 宣称已发布、已部署或已现场验收。未带证据的 artifact/field 阶段仍为 OPEN；strict 返回非零。提供坏证据时即使不加 strict 也失败。

## 每次功能更新怎么推进

先按本次目标推进：纯源码修复完成源码回归、source 严格门禁及源码归档；新包交付再进入构建和 ART，现场升级/验收再进入 FIELD。各阶段的完成声明仍以本次实际证据为准。

1. 按 AGENTS 留下旧代码对照；实现功能并完成相应真实回归。
2. 更新该功能的门禁断言和失败变异用例，执行源码阶段及 `--self-test`。这 17 项是已有源码检查，不是全部功能测试；新增功能还须增加相应测试。
3. 提交源码时，将本次源码和新文件归入唯一主线；涉及交付身份时，再核对版本、来源与目标及不可变身份。
4. 需要新包交付时，从这份源码构建新的包，不重建已交付身份；执行 ART 项并保存实际验签、载荷、权限、脚本一致性等原始输出。
5. 需要现场升级/验收时，在有现场授权后执行 FIELD 项，记录实际集群、控制节点和结果。没有执行的项不能填 passed。
6. 执行新包或现场阶段时，根据真实结果整理证据报告，运行对应严格阶段；源码、契约或包变了，旧报告不能复用。源码阶段通过不关闭 ART/FIELD，也不触发自动构建或部署。

增加功能不等于需要提升契约版本。只有强制规则或机器接口发生变化时，才按契约 §23 升版：同步三份原文、schema、Go/Node 消费者支持版本、测试和新交付兼容性。未知版本仍必须拒绝，不能为了升级而接受未知字段。

## 证据报告格式

这是**人工验收记录及证据清单**，工具核对完整性、作用范围及文件摘要；它不执行现场测试，也不从日志文本自动推导签名可信或集群健康。填写人必须为每个 passed 保留真实验证结果；不得用空文件、合成夹具或一份源码测试日志充当现场证明。

先输出当前绑定值：

```bash
node tools/verify-upgrade-validation-chain.cjs --print-evidence-binding
```

绑定涵盖 Git 跟踪的生产、构建、测试、配置、依赖及契约文件。相关未跟踪的可执行源码须先纳入版本管理；新增/修改功能后重新生成绑定。普通 README 和历史文档的编辑不使源码验收失效，强制契约修改会失效。该命令只打印身份，不能同时声明阶段通过。

报告结构示意如下，省略项必须补全；示例不能直接通过验收：

```json
{
  "schema_version": 1,
  "contract_sha256": "当前契约的64位SHA-256",
  "source_sha256": "当前跟踪源码集合的64位SHA-256",
  "package_id": "实际签名清单中的包ID",
  "artifacts": [{"path": "交付包.cgpatch", "sha256": "实际包摘要"}],
  "checks": [{
    "id": "ART-001",
    "status": "passed",
    "evidence": [{"path": "验签原始输出.txt", "sha256": "该证据文件摘要"}]
  }],
  "recorded_by": "实际验收负责人",
  "recorded_at": "2026-10-04T10:00:00+08:00",
  "field": {"cluster_id": "实际集群UUID", "controller_ids": ["实际控制节点ID"]}
}
```

- artifact 阶段必须包含 ART-001..013；field 阶段另包含 FIELD-001..012 和现场作用范围。
- 每项必须为 passed 并引用至少一份非空、非符号链接的实际证据文件。所有引用文件和签名包重新计算 SHA-256；相对路径以报告目录为根。
- 未知/重复检查 ID、缺项、FAIL、空证据、非法时间、未知字段、源码或契约摘要过期均拒绝。报告 schema 与契约版本是不同版本号。
- 报告与现场日志放私有验收目录，不上传公共 Release。该报告不替代许可、公开资产、交付血缘、真实浏览器和数据库验收门禁。
- Runner 对旧 Helper 的 contract 能力要求不变。这是现场组件兼容问题，须用匹配的新身份交付解决，不能靠修改 Markdown 或选择 source 阶段绕过。

## 当前结果

现有 v2 源码通过 source strict；仓库没有新的真实包/现场验收报告，默认 field strict 仍被 OPEN 阻断。实现和回归见[修复记录](validation-gate-evolution-2026-10-04.md)。
