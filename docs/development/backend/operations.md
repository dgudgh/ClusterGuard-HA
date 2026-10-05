# 切换、授权与执行编排

[返回模块](README.md)

修改本页所述操作管理、操作入口、预检、授权或执行链前，必须完整读[升级与热修强制契约 v2](../../zh-CN/upgrade-validation-chain.md)和[高风险操作授权规则](../rules/operation-safety.md)。本页不能缩小根规则中“操作管理”的契约触发范围。

## 调用链与边界

precheck → plan → approval → execute → workflow → adapter → verify → 持久化结果；每个执行边界重新验证授权及安全事实。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [operations.go](../../../internal/api/operations.go)、[approvals.go](../../../internal/api/approvals.go)、[workflow](../../../internal/workflow)、[approval](../../../internal/approval)、[coordination](../../../internal/coordination) |
| 回归 | [operations_test.go](../../../internal/api/operations_test.go)、[workflow](../../../internal/workflow) |

## 需要时再读

- [操作与恢复交互](../frontend/operations.md)
