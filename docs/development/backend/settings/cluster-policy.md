# 设置：引擎级集群策略

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

GET/PUT /api/v1/cluster-policy → clusterPolicyRoute → 管理员/Leader/quorum 与边界验证 → Raft 提交策略及审计 → 运行时读取。API 输入、存储和每轮读取一并核对。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [cluster_policy.go](../../../../internal/api/cluster_policy.go)、[cluster_policy.go](../../../../internal/store/cluster_policy.go)、[runtime](../../../../internal/runtime) |
| 回归 | [cluster_policy_test.go](../../../../internal/api/cluster_policy_test.go)、[cluster_policy_test.go](../../../../internal/store/cluster_policy_test.go) |

## 需要时再读

- [cluster-policy](../../frontend/settings/cluster-policy.md)
