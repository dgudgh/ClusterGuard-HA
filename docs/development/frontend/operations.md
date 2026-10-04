# 操作与恢复交互

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

操作入口 → 显式风险确认 → 预检 → 再验授权 → 提交 → 按操作身份查询持久化结果；后台任务与页面授权是不同生命周期。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[operations.go](../../../internal/api/operations.go)、[disaster_recovery.go](../../../internal/api/disaster_recovery.go) |
| 回归 | [console-operation-lock-acceptance.cjs](../../../tools/console-operation-lock-acceptance.cjs)、[console-recovery-acceptance.cjs](../../../tools/console-recovery-acceptance.cjs)、[console-operation-intent-acceptance.cjs](../../../tools/console-operation-intent-acceptance.cjs) |

## 需要时再读

- [operation-safety](../rules/operation-safety.md)
- [recovery](../backend/recovery.md)

## 必须保持与回归

- 恢复提交必须区分明确未受理与结果不确定：4xx 拒绝后退出等待但不得恢复确认；网络断连、5xx 或 202 状态未推进时只查询持久化结果，不自动重提。测试服务端需按幂等键计数，不能把浏览器传输重试等同多次业务执行。
