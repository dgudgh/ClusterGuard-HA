# 设置：集群策略编辑

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

运行参数面板 → 策略读取 → 编辑/确认 → PUT /api/v1/cluster-policy → 查询持久化结果；读取失败或输入越界不得继续写。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html)、[cluster_policy.go](../../../../internal/api/cluster_policy.go) |
| 回归 | [cluster_policy_test.go](../../../../internal/api/cluster_policy_test.go) |

## 需要时再读

- [cluster-policy](../../backend/settings/cluster-policy.md)
- [operation-safety](../../rules/operation-safety.md)
