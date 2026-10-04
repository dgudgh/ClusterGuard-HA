# 集群注册、发现与拓扑

[返回模块](README.md)

## 调用链与边界

集群登记 → 引擎适配与原生身份 → discovery → 保存观测 → API 投影；区分配置、实际观测和用户工作上下文。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [clusters.go](../../../internal/api/clusters.go)、[discovery](../../../internal/discovery)、[runtime](../../../internal/runtime)、[adapters](../../../adapters) |
| 回归 | [clusters_test.go](../../../internal/api/clusters_test.go)、[discovery](../../../internal/discovery) |

## 需要时再读

- [集群上下文与导航](../frontend/context.md)
- [ClusterGuard HA 数据库接入手册](../../zh-CN/database-preparation.md)
