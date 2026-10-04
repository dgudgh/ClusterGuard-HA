# 节点与生命周期弹窗

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

节点添加/修复 → 目标绑定 → 风险确认 → 异步预检 → 执行 → 单独查询终态；不要复用操作页测试替代节点弹窗验收。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[nodes.go](../../../internal/api/nodes.go)、[node_sync.go](../../../internal/api/node_sync.go) |
| 回归 | [console-node-safety-audit.cjs](../../../tools/console-node-safety-audit.cjs)、[console-operation-lifecycle-audit.cjs](../../../tools/console-operation-lifecycle-audit.cjs) |

## 需要时再读

- [operation-safety](../rules/operation-safety.md)
- [nodes](../backend/nodes.md)

## 必须保持与回归

- 节点添加/修复弹窗必须单独验收：默认锁定、显式风险确认、重复调用、目标变化、预检迟到、手动刷新、退出、新会话、自动刷新期间的真实点击。普通操作页的锁测试不能替代节点页测试。
- 已确认成功的节点执行与后续状态读取必须分开记录；任务查询失败不得把成功操作改成失败或留在执行中，不得自动重新提交。必须覆盖完成后读取 503、重新锁定和重复调用。
