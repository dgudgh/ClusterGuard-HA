# 适用规则索引

任何修改前先读[门禁执行与更新流程](../../zh-CN/validation-gate-workflow.md)；涉及契约范围时先完整读[强制契约 v2](../../zh-CN/upgrade-validation-chain.md)。随后用本索引找到其他适用规则，不要求读完全部正文。

| 原 AGENTS 主题 | 当前正文 | 触发条件 |
| --- | --- | --- |
| 修改前旧代码对照、证据和验收真实性 | [修改与证据](change-policy.md) | 任何业务代码修改前 |
| 页面风格、实际点击、桌面/窄屏 | [前端通用交互](frontend-common.md) | 前端交互或布局变化 |
| 风险确认、授权撤回、预检迟到、重复提交 | [操作授权](operation-safety.md) | 新增/修改变更入口或执行链 |
| 集群上下文、日志分页、节点弹窗、恢复等专项回归 | [前端页面入口](../frontend/README.md)、[后端功能入口](../backend/README.md) | 进入实际涉及功能的必守/回归条目 |
| 公开 Release 资产与删除义务 | [公开渠道](public-release.md) | 公开上传/删除/核验 Release |
| 许可、依赖、许可入包 | [许可与依赖](licensing.md) | 对应许可/依赖/打包事项 |
| 完整打包前阻断清单 | [交付清单](release-checklist.md) | 准备交付或发布 |

旧记录说“见 AGENTS”时，从[根规则](../../../AGENTS.md)确定任务范围，再到此表对应正文；不要把旧历史结论当作当前实现。
