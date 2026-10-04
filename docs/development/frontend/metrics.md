# 指标与引擎能力

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

选中集群/引擎 → 获取能力和指标 → 渲染可用项；Oracle/SQL Server 的能力须与真实后端相符。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[metrics.go](../../../internal/api/metrics.go) |
| 回归 | [console-engine-pages-audit.cjs](../../../tools/console-engine-pages-audit.cjs) |

## 需要时再读

- [engines](engines.md)
- [observability](../backend/observability.md)
