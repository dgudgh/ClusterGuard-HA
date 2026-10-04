# 节点同步、生命周期与受限执行

[返回模块](README.md)


修改操作入口、预检、授权或执行链前，必须读[高风险操作授权规则](../rules/operation-safety.md)。

## 调用链与边界

节点目标 → planner → gates → executor/Agent → lifecycle 终态；关机/启动/修复与普通切换是不同流程，执行成功和后续读取失败分开记录。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [nodes.go](../../../internal/api/nodes.go)、[node_sync.go](../../../internal/api/node_sync.go)、[power.go](../../../internal/api/power.go)、[lifecycle](../../../internal/lifecycle)、[agent](../../../internal/agent)、[endpoint](../../../internal/endpoint) |
| 回归 | [nodes_test.go](../../../internal/api/nodes_test.go)、[node_sync_test.go](../../../internal/api/node_sync_test.go)、[lifecycle](../../../internal/lifecycle) |

## 需要时再读

- [节点与生命周期弹窗](../frontend/nodes.md)
