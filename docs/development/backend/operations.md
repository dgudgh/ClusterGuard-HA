# 切换、授权与执行编排

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

precheck → plan → approval → execute → workflow → adapter → verify → 持久化结果；每个执行边界重新验证授权及安全事实。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [operations.go](../../../internal/api/operations.go)、[approvals.go](../../../internal/api/approvals.go)、[workflow](../../../internal/workflow)、[approval](../../../internal/approval)、[coordination](../../../internal/coordination) |
| 回归 | [operations_test.go](../../../internal/api/operations_test.go)、[workflow](../../../internal/workflow) |

## 需要时再读

- [operation-safety](../rules/operation-safety.md)
- [operations](../frontend/operations.md)
