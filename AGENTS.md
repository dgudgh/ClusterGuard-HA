# ClusterGuard 修改规则与按需阅读

适用全仓。只保留一条主线 `codex/2.2-postgresql`；保留他人未提交改动。文档按前端、后端和交付模块渐进展开，不要求一次读完全部资料。

## P0：修改前必读门禁（先于模块导航）

1. **开始任何修改前，必须先读[门禁执行与更新流程](docs/zh-CN/validation-gate-workflow.md)**，确定适用门禁阶段及验证计划，然后再进入功能文档。
2. **涉及升级、热修、重试、续跑、回退、控制台更新流程、操作管理、维护锁、构建、签名或发布时，修改/执行前必须完整读取[升级与热修强制契约 v2](docs/zh-CN/upgrade-validation-chain.md)**。任务记录写明 `CG-UPGRADE-CONTRACT`、`contract_version=2`、`contract_loaded=true`。
3. 任务 Markdown 必须记录已读门禁文档、适用阶段和验证计划；未读适用门禁不得开始对应修改或动作，未完成门禁不得宣称对应阶段通过。强制契约是规范，执行流程是指南；功能文档不得降低门禁要求。

## 所有修改必须遵守

- 修改业务代码前先确认实际源码、分支、HEAD、工作树与目标运行版本；用 git log/show/diff 对比可信旧行为，并事先写下任务 Markdown。未完成旧版对比不得修改业务代码。
- 没有完成对应行为回归，不得宣称修复完成；本地源码、隔离浏览器、真实数据库、现场升级分别记录，未执行不得记为通过。
- 不得绕过授权、签名、隔离/fencing、writer lease、维护门禁或多数派检查。保留旧日志与原始证据；局部修复，不附带无关业务变化。
- 许可固定为 AGPL-3.0-only；根 LICENSE 不得修改。任何依赖、打包或文档修改后执行 `node tools/verify-license-consistency.cjs`，必须 passed。
- **任何 Markdown 的新增、删除、修改都必须在 [Markdown 变更记录](docs/development/markdown-change-record.md) 登记**：按文件写清变更类型（`A` 新增 / `M` 修改 / `D` 删除）和改了什么，重命名按「删旧 + 增新」两条记，然后用 `node tools/verify-markdown-change-record.cjs` 校验，必须 passed。改了 md 必须重建入库的生成页（`cd docs && node build-html-docs.mjs`）。没有被登记、或门禁报「变了但没登记」的 md 不得提交。
- GitHub 只放完整离线安装介质；签名 `.cgupgrade` / `.cgpatch` 及摘要仅本地和签约交付，不得公开。已交付身份不得覆盖重建。

## 按任务选择路径

门禁必读完成后，按[阅读优先级与适用规则](docs/development/rules/README.md)确定本任务的其他前置规则，再进入功能模块；历史报告不能覆盖现行规范或替代本次验收。

| 任务 | 必须先读 | 之后按需进入 |
| --- | --- | --- |
| 业务代码修改 | [修改与证据规则](docs/development/rules/change-policy.md) | 当前模块与单功能文档 |
| 前端 | [前端入口](docs/development/frontend/README.md) | 当前页面 → 功能 → 规则/测试 |
| 后端 | [后端入口](docs/development/backend/README.md) | 接口模块 → 功能 → 服务/存储 |
| 高风险操作 | [操作授权规则](docs/development/rules/operation-safety.md) | 对应操作/恢复/节点功能 |
| 升级、热修及其构建/校验 | [强制契约](docs/zh-CN/upgrade-validation-chain.md) | [后端设置：升级](docs/development/backend/settings/updates.md) → 对应子功能 |
| 打包、签名、公开发布、现场交付 | [交付入口](docs/development/delivery/README.md) | 适用渠道规则与完整发布清单 |
| 依赖或许可变更 | [许可与依赖规则](docs/development/rules/licensing.md) | 第三方清单与合规说明 |

先完成以上门禁必读，再按需阅读模块，不递归读取所有链接。父索引只给职责与下一步；具体功能页说明范围、调用链、失败和验证。页中“必须读”按写明的触发条件执行，“仅当”链接留到对应问题出现时再读。旧 AGENTS 正文去向见[适用规则索引](docs/development/rules/README.md)。

[开发总入口](docs/development/README.md) · [文档中心](docs/README.md)
