# 后端升级与热修

修改/执行前先读[门禁执行流程](../../../zh-CN/validation-gate-workflow.md)和完整[强制契约](../../../zh-CN/upgrade-validation-chain.md)，并按 §20 记录契约加载；子功能页不能替代契约。

本功能的粗略链路是 `updates API → Manager → Helper → job wrapper → Runner → 签名载荷`。完成前置阅读后按问题进入子功能，具体代码和测试在子功能页。

| 当前问题 | 进入的子功能 |
| --- | --- |
| execute/retry/resume/rollback、验签、基线、外包锁 | [动作与执行保护](update-actions.md) |
| 成功被失败覆盖、重复提交、操作 ID、Leader 历史冲突 | [部署状态与操作历史](update-history.md) |
| 契约加载、门禁阶段、构建前检查、验收证据更新 | [契约与验收门禁](update-validation.md) |

具体现场命令在执行升级时读[现场手册](../../../zh-CN/update-and-patch.md)；构建/签名/发布进入[交付流程](../../delivery/README.md)。

[返回设置接口](README.md)
