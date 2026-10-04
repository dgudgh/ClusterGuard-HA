# 设置：升级与热修交互

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

版本更新 → 上传校验 → 选定 patch_id → 确认 → execute/retry/resume/rollback → operation_id 对账。完整强制契约仅在涉及本功能时读取。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html)、[updates.go](../../../../internal/api/updates.go) |
| 回归 | [console-update-confirmation-acceptance.cjs](../../../../tools/console-update-confirmation-acceptance.cjs)、[console-update-hotfix-recovery-acceptance.cjs](../../../../tools/console-update-hotfix-recovery-acceptance.cjs) |

## 需要时再读

- [updates](../../backend/settings/updates.md)
- [operation-safety](../../rules/operation-safety.md)
- [upgrade-validation-chain](../../../zh-CN/upgrade-validation-chain.md)

## 必须保持与回归

- 升级与热修的动作对象必须用真实点击覆盖：成功的热修不提供「续跑」；失败的热修提供「重新执行」而非「续跑」；被拒绝的续跑在成功事件链上留下的失败状态按「本次尝试失败」解释；失败的滚动升级保留「续跑」；以及列表里「更新的成功记录在上、失败记录在下」时，执行与回退都只作用于失败的那一条，且请求里的 `patch_id` 与确认框要求输入的 id 一致。回归入口 `tools/console-update-hotfix-recovery-acceptance.cjs`，逐场景如实报告是否执行、是否通过。
