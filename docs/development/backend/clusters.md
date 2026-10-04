# 集群注册、发现与拓扑

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

集群登记 → 引擎适配与原生身份 → discovery → 保存观测 → API 投影；区分配置、实际观测和用户工作上下文。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [clusters.go](../../../internal/api/clusters.go)、[discovery](../../../internal/discovery)、[runtime](../../../internal/runtime)、[adapters](../../../adapters) |
| 回归 | [clusters_test.go](../../../internal/api/clusters_test.go)、[discovery](../../../internal/discovery) |

## 需要时再读

- [context](../frontend/context.md)
- [database-preparation](../../zh-CN/database-preparation.md)
