# 健康、指标与报告

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

引擎观测 → metrics/monitoring → API/报告投影；缺失数据和超时须保留，不能删健康检查制造性能改善。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [metrics.go](../../../internal/api/metrics.go)、[monitoring.go](../../../internal/api/monitoring.go)、[metrics](../../../internal/metrics)、[observability](../../../internal/observability)、[report](../../../internal/report) |
| 回归 | [metrics_test.go](../../../internal/api/metrics_test.go)、[monitoring_test.go](../../../internal/api/monitoring_test.go)、[metrics](../../../internal/metrics) |

## 需要时再读

- [metrics](../frontend/metrics.md)
