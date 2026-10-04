# 节点同步、生命周期与受限执行

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

节点目标 → planner → gates → executor/Agent → lifecycle 终态；关机/启动/修复与普通切换是不同流程，执行成功和后续读取失败分开记录。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [nodes.go](../../../internal/api/nodes.go)、[node_sync.go](../../../internal/api/node_sync.go)、[power.go](../../../internal/api/power.go)、[lifecycle](../../../internal/lifecycle)、[agent](../../../internal/agent)、[endpoint](../../../internal/endpoint) |
| 回归 | [nodes_test.go](../../../internal/api/nodes_test.go)、[node_sync_test.go](../../../internal/api/node_sync_test.go)、[lifecycle](../../../internal/lifecycle) |

## 需要时再读

- [operation-safety](../rules/operation-safety.md)
- [nodes](../frontend/nodes.md)
