# 灾难恢复与旧主恢复

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

恢复候选与证据 → 隔离/HBA/fencing → 有效授权 → 恢复执行 → 复制链路/主库可写 → Recovery Commit。MySQL 与 PostgreSQL 使用各自原生证据，不能通用化提升判断。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [disaster_recovery.go](../../../internal/api/disaster_recovery.go)、[disaster](../../../internal/disaster)、[recovery](../../../internal/recovery)、[postgresql](../../../adapters/postgresql)、[mysql](../../../adapters/mysql) |
| 回归 | [disaster_recovery_test.go](../../../internal/api/disaster_recovery_test.go)、[disaster](../../../internal/disaster) |

## 需要时再读

- [operation-safety](../rules/operation-safety.md)
- [release-recovery-acceptance-checklist](../../zh-CN/release-recovery-acceptance-checklist.md)

## 必须保持与回归

- PostgreSQL 灾难恢复必须用真实实例覆盖遗留 `default_transaction_read_only=on`；不能只证明提升成功或复制连接存在，必须验证最终主库读写状态、两条拓扑链路和 Recovery Commit。处理旧 SQL fence 必须位于有效授权和已验证 HBA 业务隔离之后，不能为修链接而放宽健康条件；覆盖缺 guard、旧指纹和授权撤销。
