# 指标与引擎能力

[返回模块](README.md)

修改跨引擎能力判断、Oracle/SQL Server 指标或通用执行能力映射前，必须读[全页面与引擎能力边界](engines.md)；仅调整已确认指标的排版、不改变指标名称、语义或能力判断时无需展开跨页面规则。

## 调用链与边界

选中集群/引擎 → 获取能力和指标 → 渲染可用项；Oracle/SQL Server 的能力须与真实后端相符。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[metrics.go](../../../internal/api/metrics.go) |
| 回归 | [console-engine-pages-audit.cjs](../../../tools/console-engine-pages-audit.cjs) |

## 需要时再读

- [健康、指标与报告](../backend/observability.md)
