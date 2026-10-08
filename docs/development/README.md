# 开发入口

## 修改前必读门禁

先读[根规则](../../AGENTS.md)和[门禁执行与更新流程](../zh-CN/validation-gate-workflow.md)，在任务记录中写明适用阶段与验证计划。涉及升级、热修、回退、控制台更新、操作管理、维护锁、构建、签名或发布时，修改/执行前完整读[强制契约 v2](../zh-CN/upgrade-validation-chain.md)。

## 再选择功能模块

先按[阅读优先级与适用规则](rules/README.md)确定当前任务的前置规则；历史设计和验收结果只用于对应版本追溯。

先确定问题发生在哪一层，只进入对应入口。业务代码修改前遵守[修改与证据规则](rules/change-policy.md)；改动任何 Markdown 都要登记到 [Markdown 变更记录](markdown-change-record.md) 并跑 `node tools/verify-markdown-change-record.cjs`。

| 你要改什么 | 进入哪里 |
| --- | --- |
| 页面显示、点击流程、前端状态 | [前端页面](frontend/README.md) |
| API、执行流程、数据库适配、持久化 | [后端功能](backend/README.md) |
| 构建、签名、发布、现场安装升级 | [交付流程](delivery/README.md) |

门禁必读完成后，每层只选择下一步，不预读所有链接。功能页说明入口、作用范围和验证；跨层排查时再沿其条件链接进入另一侧。历史资料由[完整目录](../catalogue.md)查询。
