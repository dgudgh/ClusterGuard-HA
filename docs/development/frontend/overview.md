# 总览与拓扑

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

集群上下文 → 观测加载 → 总览/拓扑渲染；区分缺失观测、旧观测和真实不健康，不用示例拓扑覆盖现场状态。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[clusters.go](../../../internal/api/clusters.go) |
| 回归 | [console-overview-acceptance.cjs](../../../tools/console-overview-acceptance.cjs)、[console-context-acceptance.cjs](../../../tools/console-context-acceptance.cjs) |

## 需要时再读

- [product-tour](../../zh-CN/product-tour.md)
