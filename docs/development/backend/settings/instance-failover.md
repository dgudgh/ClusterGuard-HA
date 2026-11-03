# 单集群与数据库实例自动切换策略

[返回设置接口](README.md) · [用户说明](../../../zh-CN/instance-failover-policy.md)

修改先读[操作授权规则](../../rules/operation-safety.md)。该策略跟随所选集群，区别于所有同引擎集群共享的[全局覆盖](cluster-policy.md)。

`GET/PUT /api/v1/clusters/<UUID>/failover-policy`，cluster detail同时带failover_policy投影。PUT显式传expected_revision、automatic_enabled和instances[实例UUID]中的automatic_eligible/weight；缺失、null、未知字段及越界拒绝。通用认证/管理员/CSRF、维护门禁、Leader与多数派中间件不变。保存前全体当前Raft投票节点确认protocol_version=1、controller_id与cluster_id；开启还检查各节点执行前置。

[API](../../../../internal/api/instance_failover_policy.go) → [原子CAS存储](../../../../internal/store/instance_failover_policy.go) → model.DatabaseCluster.FailoverPolicy。普通发现/Upsert保留策略；clone深拷贝；恢复快照拒绝坏修订、身份与权重。显式策略中新UUID默认不自动参与，删除实例不把旧UUID规则转给新实例。

[Controller](../../../../internal/recovery/controller.go)按集群读取开关，[selector](../../../../internal/recovery/mysql_selector.go)传AutomaticSelection，MySQL/PG原生候选保留安全优先再按权重。实际[workflow](../../../../internal/workflow/workflow.go)重新检查参与资格和选候选时的修订，手动不走自动排除。MySQL重挂followers单独使用原生安全集合，不能把禁止自动升主误当成禁止复制重挂。

回归：API同名test、store同名test、runtime/instance_failover_peers_test.go、recovery/instance_failover_policy_test.go、两引擎同名单功能测试和workflow同名test。现场与制品阶段见[记录](../../instance-failover-policy-2026-11-03.md)。
