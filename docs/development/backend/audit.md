# 日志、分页、存储与 Raft

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

查询范围与游标 → store 分页 → 审计/报告；写入同时核对 Raft/CAS、追加历史和保留策略。只在涉及持久化时再进入 store/consensus 具体实现。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [operation_page.go](../../../internal/api/operation_page.go)、[operations.go](../../../internal/api/operations.go)、[store](../../../internal/store)、[consensus](../../../internal/consensus) |
| 回归 | [operation_page_test.go](../../../internal/store/operation_page_test.go)、[replication_test.go](../../../internal/store/replication_test.go) |

## 需要时再读

- [logs](../frontend/logs.md)

## 必须保持与回归

- 保留已有日志及原始证据，旧运行中/待复核记录仍参与安全判断。
