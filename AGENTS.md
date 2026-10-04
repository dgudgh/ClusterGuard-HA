# ClusterGuard 修改规则与按需阅读

适用全仓。只保留一条主线 `codex/2.2-postgresql`；保留他人未提交改动。文档按前端、后端和交付模块渐进展开，不要求一次读完全部资料。

## 所有修改必须遵守

- 修改业务代码前先确认实际源码、分支、HEAD、工作树与目标运行版本；用 git log/show/diff 对比可信旧行为，并事先写下任务 Markdown。未完成旧版对比不得修改业务代码。
- 没有完成对应行为回归，不得宣称修复完成；本地源码、隔离浏览器、真实数据库、现场升级分别记录，未执行不得记为通过。
- 不得绕过授权、签名、隔离/fencing、writer lease、维护门禁或多数派检查。保留旧日志与原始证据；局部修复，不附带无关业务变化。
- 许可固定为 AGPL-3.0-only；根 LICENSE 不得修改。任何依赖、打包或文档修改后执行 `node tools/verify-license-consistency.cjs`，必须 passed。
- GitHub 只放完整离线安装介质；签名 `.cgupgrade` / `.cgpatch` 及摘要仅本地和签约交付，不得公开。已交付身份不得覆盖重建。

## 按任务选择路径

| 任务 | 必须先读 | 之后按需进入 |
| --- | --- | --- |
| 业务代码修改 | [修改与证据规则](docs/development/rules/change-policy.md) | 当前模块与单功能文档 |
| 前端 | [前端入口](docs/development/frontend/README.md) | 当前页面 → 功能 → 规则/测试 |
| 后端 | [后端入口](docs/development/backend/README.md) | 接口模块 → 功能 → 服务/存储 |
| 高风险操作 | [操作授权规则](docs/development/rules/operation-safety.md) | 对应操作/恢复/节点功能 |
| 升级、热修及其构建/校验 | [后端设置：升级](docs/development/backend/settings/updates.md) | 对应子功能；执行前必须读[强制契约](docs/zh-CN/upgrade-validation-chain.md) |
| 打包、签名、公开发布、现场交付 | [交付入口](docs/development/delivery/README.md) | 适用渠道规则与完整发布清单 |
| 依赖或许可变更 | [许可与依赖规则](docs/development/rules/licensing.md) | 第三方清单与合规说明 |

不要递归读取所有链接。模块索引只给职责和粗略路径；涉及的单功能文档及其适用硬规则必须读取。跨模块问题才沿调用链读取关联模块。

[开发总入口](docs/development/README.md) · [文档中心](docs/README.md)
