# 当前集群自动切换与实例编辑

[设置入口](README.md) · [后端语义](../../backend/settings/instance-failover.md) · [用户说明](../../../zh-CN/instance-failover-policy.md)

修改必须先读[操作授权](../../rules/operation-safety.md)与[前端规则](../../rules/frontend-common.md)。页面位于运行参数的自动切换策略页，当前集群编辑器在引擎全局覆盖之前；前者跟随顶部集群，后者不跟随。

cluster detail的failover_policy → hydrateInstanceFailoverPolicy → renderInstanceFailoverPolicy → 解锁 → saveInstanceFailoverPolicy → 当前集群PUT。禁止用主机/controller ID代替实例UUID。开关与权重草稿绑定当前用户、集群、requestGeneration及策略revision；保存函数再次检查解锁、角色和范围。beginClusterRequest、clearClusterView和会话清理清除草稿和授权；迟到保存结果不得填入其他集群。

读失败/能力未知不能保存；整数范围0..1000；保存期间防重复并禁用输入，结束重锁。写失败不显示成功，CAS冲突要求重读。原始诊断与已确认事实分开，不用开关状态假称当前运行已具备多数派或自动切主。

[浏览器验收](../../../../tools/console-instance-failover-acceptance.cjs)覆盖双集群/实例、开关/权重、禁止越界、迟到响应、失败重锁、双语及窄屏，使用隔离API不写现场。全局策略和参数下发仍保留原回归，见[全局编辑器](cluster-policy.md)。
