# 切换、授权与执行编排

[返回模块](README.md)

修改本页所述操作管理、操作入口、预检、授权或执行链前，必须完整读[升级与热修强制契约 v2](../../zh-CN/upgrade-validation-chain.md)和[高风险操作授权规则](../rules/operation-safety.md)。本页不能缩小根规则中“操作管理”的契约触发范围。

## 调用链与边界

precheck → plan → approval → execute → workflow → adapter → verify → 持久化结果；每个执行边界重新验证授权及安全事实。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [operations.go](../../../internal/api/operations.go)、[approvals.go](../../../internal/api/approvals.go)、[workflow](../../../internal/workflow)、[approval](../../../internal/approval)、[coordination](../../../internal/coordination) |
| 回归 | [operations_test.go](../../../internal/api/operations_test.go)、[workflow](../../../internal/workflow) |

## MySQL 新装集群的空 GTID 集合

GTID 已启用但尚无已记录事务时，`@@GLOBAL.gtid_executed` 可以为空字符串。预检与隔离旧主后的 GTID 捕获都按合法空集合处理；仍须取得恰好一行且包含 `gtid_executed` 字段的查询结果，并完成 GTID 解析、目标追平等待、复制重挂、唯一可写主库和业务入口核验。查询失败、缺行、多行、缺字段或非法 GTID 仍阻断，不能用空集合代替缺失的证据。

回归见 [switchover_empty_gtid_test.go](../../../adapters/mysql/switchover_empty_gtid_test.go)：空集合执行与最终验证、五类坏查询结果保护源/目标，以及空集合仍执行目标等待。该回归使用隔离 SQL 夹具，不等于现场切换验收。

现场提示“结果不确定”时，先按操作 ID 核对原始失败步骤，再读回三台数据库角色、复制状态和 VIP。确认当前实际状态后才能记录人工复核；保留原任务与执行证据，不凭这条提示重复切换或改写为成功。

## 需要时再读

- [操作与恢复交互](../frontend/operations.md)

## 从库故障与现役主库 VIP

两个从库的数据库停止或复制报错时，主库仍可写、控制面有多数派、既有稳定租约有效且实际VIP归属一致，应保留现役主库VIP。半同步无ACK仍显示降级和告警，不据此授予新主库或放宽切换条件。

仅允许明确的半同步ACK缺失原因续租同一Owner的既有稳定租约：必须有当前批次成功SQL探针、有效半同步配置、主库两项运行时只读均关闭、唯一主库和直接VIP观测。该分支只发`RenewOnly`，不新建/恢复已过期租约，不改Owner，不做零Owner引导。

Raft多数派和数据节点Agent的VIP观测覆盖分别校验。两台整机或Agent失联导致观测不足、失去控制面多数派、主库失联/只读、归属冲突或灾难恢复冻结时，继续按原规则停止授权并隔离。实现与验证记录见[从库故障下的VIP续租修复](../mysql-replica-failure-vip-2026-10-15.md)。
