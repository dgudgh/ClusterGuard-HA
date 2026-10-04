# 交付流程入口

**修改/执行前必读：[门禁执行与更新流程](../../zh-CN/validation-gate-workflow.md)和[强制契约 v2](../../zh-CN/upgrade-validation-chain.md)。** 记录适用阶段，再选择当前动作。

只在产出制品、发布或现场部署时进入。先区分要做的动作；源码检查通过不代表后续阶段已完成。

| 动作 | 本步骤必须读 |
| --- | --- |
| 构建新身份的 RPM、介质或签名包 | [版本与制品规范](../../zh-CN/version-release-policy.md)、[打包阻断清单](../rules/release-checklist.md) |
| 维护许可声明、引入依赖、封装文档许可 | [许可与依赖规则](../rules/licensing.md) |
| 上传/删除 GitHub Release 或其附件 | [公开渠道规则](../rules/public-release.md) |
| 推进源码、新包与现场的门禁证据 | [分阶段门禁流程](../../zh-CN/validation-gate-workflow.md) |
| 现场上传、升级、重试、回退 | [现场升级手册](../../zh-CN/update-and-patch.md)、[强制契约](../../zh-CN/upgrade-validation-chain.md) |

做完整发布时执行打包清单中的全部适用门槛；只修改业务功能时返回对应前端/后端模块。
