# 灾难恢复与旧主恢复

[返回模块](README.md)


修改操作入口、预检、授权或执行链前，必须读[高风险操作授权规则](../rules/operation-safety.md)。

## 调用链与边界

恢复候选与证据 → 隔离/HBA/fencing → 有效授权 → 恢复执行 → 复制链路/主库可写 → Recovery Commit。MySQL 与 PostgreSQL 使用各自原生证据，不能通用化提升判断。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [disaster_recovery.go](../../../internal/api/disaster_recovery.go)、[disaster](../../../internal/disaster)、[recovery](../../../internal/recovery)、[postgresql](../../../adapters/postgresql)、[mysql](../../../adapters/mysql) |
| 回归 | [disaster_recovery_test.go](../../../internal/api/disaster_recovery_test.go)、[disaster](../../../internal/disaster) |

## 现场或发布前必读

准备真实恢复操作、制定恢复现场验收或交付包含恢复改动的版本前，必须读[恢复与升级发布验收清单](../../zh-CN/release-recovery-acceptance-checklist.md)的对应场景。源码调查不要求执行现场操作；未执行不能报为通过。

## 必须保持与回归

- 恢复计划必须绑定当前不可变库存；过期但从未执行、无证据/保护/活跃租约的计划保留为`superseded`并重新规划，旧确认失效。已经执行的任务不自动换库存，继续保留保护并要求复核。预检必须先校验计划库存，再查Driver能力。回归见`internal/store/disaster_replan_test.go`及[物理缺日志复现记录](../disaster-binlog-recovery-2026-10-14.md)。
- PostgreSQL 灾难恢复必须用真实实例覆盖遗留 `default_transaction_read_only=on`；不能只证明提升成功或复制连接存在，必须验证最终主库读写状态、两条拓扑链路和 Recovery Commit。处理旧 SQL fence 必须位于有效授权和已验证 HBA 业务隔离之后，不能为修链接而放宽健康条件；覆盖缺 guard、旧指纹和授权撤销。
