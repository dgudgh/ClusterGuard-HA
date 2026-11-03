# 单集群与数据库实例自动切换策略

[返回设置接口](README.md) · [用户说明](../../../zh-CN/instance-failover-policy.md)

修改先读[操作授权规则](../../rules/operation-safety.md)。该策略跟随所选集群，区别于所有同引擎集群共享的[全局覆盖](cluster-policy.md)。

`GET/PUT /api/v1/clusters/<UUID>/failover-policy`，cluster detail同时带failover_policy投影。PUT显式传expected_revision、automatic_enabled和instances[实例UUID]中的automatic_eligible/weight；缺失、null、未知字段及越界拒绝。通用认证/管理员/CSRF、维护门禁、Leader与多数派中间件不变。保存前全体当前Raft投票节点确认protocol_version=1、runtime_compatibility_protocol=1、controller_id与cluster_id；开启还检查各节点执行前置。

[API](../../../../internal/api/instance_failover_policy.go) → [原子CAS存储](../../../../internal/store/instance_failover_policy.go) → model.DatabaseCluster.FailoverPolicy。普通发现/Upsert保留策略；clone深拷贝；恢复快照拒绝坏修订、身份与权重。显式策略中新UUID默认不自动参与，删除实例不把旧UUID规则转给新实例。

[Controller](../../../../internal/recovery/controller.go)按集群读取开关，[selector](../../../../internal/recovery/mysql_selector.go)传AutomaticSelection，MySQL/PG原生候选保留安全优先再按权重。实际[workflow](../../../../internal/workflow/workflow.go)重新检查参与资格和选候选时的修订，手动不走自动排除。MySQL重挂followers单独使用原生安全集合，不能把禁止自动升主误当成禁止复制重挂。

回归：API同名test、store同名test、runtime/instance_failover_peers_test.go、recovery/instance_failover_policy_test.go、两引擎同名单功能测试和workflow同名test。现场与制品阶段见[记录](../../instance-failover-policy-2026-11-03.md)。

首次保存与策略同一次Raft提交持久化最低运行协议；关闭自动、退休和旧快照恢复不得降低。已存在的旧策略也视为需要协议1。保存后升级/回退先检查实际目标程序，详见[持久兼容门槛](runtime-compatibility.md)。本轮仅SOURCE与隔离root测试，现场3.1.7.7尚未包含该保护。


PG上游失联后可使用先前双端原生核验的绑定，但必须重新核验当前注册端点、配置指纹、system identifier/timeline及已排空WAL；不把GUC或缓存healthy当故障接管证据。新鲜来源查询、Agent隔离、多数派、租约及提升后校验不变；新进展见[三项缺陷修复](../../ha-remaining-fixes-2026-11-03.md)。本机PG16停主后候选通过不等于现场自动切换/重挂通过。


2026-11-03，3.1.7.11三节点升级后真实停止PG主152，自动操作选择权重900的153并排除权重1000的云库，隔离/提升/网关及复制重挂通过；旧主正式pg_rewind回挂、受控切回152及四库事务保留已测。停主旧连接失败和回挂期间二次入口中断仍分开记录，不能宣称连续写入/全部FIELD通过。原自动关闭/权重100以新revision恢复，单网关未改，详见[现场记录](../../ha-remaining-fixes-2026-11-03.md)。
