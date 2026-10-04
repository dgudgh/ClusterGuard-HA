# 总览与拓扑

[返回模块](README.md)

## 调用链与边界

集群上下文 → 观测加载 → 总览/拓扑渲染；区分缺失观测、旧观测和真实不健康，不用示例拓扑覆盖现场状态。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[clusters.go](../../../internal/api/clusters.go) |
| 回归 | [console-overview-acceptance.cjs](../../../tools/console-overview-acceptance.cjs)、[console-context-acceptance.cjs](../../../tools/console-context-acceptance.cjs) |

## 需要时再读

- [ClusterGuard HA 产品导览](../../zh-CN/product-tour.md)
