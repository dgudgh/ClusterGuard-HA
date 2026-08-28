# ClusterGuard HA 公众号系列

数据库高可用很容易被压缩成一句话，主库挂了以后把从库提起来。

到了生产现场，事情会多出很多层。谁有权判断主库已经失效，候选节点的数据是否完整，旧主是否已经停止写入，业务入口该落到哪里，恢复节点还能不能安全回到复制关系里，这些问题都要有人回答。

这一组文章从 ClusterGuard HA 的来路讲起，随后分别讨论 MHA 和 Orchestrator，再进入产品机制、离线安装、日常操作、生产验收和 PostgreSQL 2.2。八篇文章可以独立阅读，连起来则是一份从产品认识到落地运行的完整说明。

| 篇目 | 文章 | 主要内容 |
| --- | --- | --- |
| 第一篇 | [我为什么做 ClusterGuard HA](wechat-01-why-clusterguard-ha.md) | 项目的起点、原型阶段遇到的问题，以及建立独立控制内核的原因 |
| 第二篇 | [从 MHA 走向完整的高可用控制平台](wechat-02-mha-comparison.md) | MHA 的贡献、适用场景，以及 ClusterGuard HA 扩展出的资源管理、安全门禁和审计交付 |
| 第三篇 | [Orchestrator 给了我们什么启发](wechat-03-orchestrator-comparison.md) | 拓扑发现、候选评估和恢复编排，以及独立产品重做的身份模型与工作流 |
| 第四篇 | [ClusterGuard HA 是怎样管住数据库切换这件事的](wechat-clusterguard-ha-introduction.md) | 一次切换背后的发现、预检查、执行、验证、审计和报告 |
| 第五篇 | [三台服务器，从零装好 ClusterGuard HA](wechat-05-offline-install.md) | 2.1.45 离线包、三节点 MySQL、首次登录和安装验收 |
| 第六篇 | [装好 ClusterGuard HA 以后，第一天该怎么用](wechat-06-operations-tutorial.md) | 集群选择、计划切换、旧主恢复、节点管理和操作日志复核 |
| 第七篇 | [上线前别只测一次切换](wechat-07-production-readiness.md) | 整机故障、网络分区、VIP 唯一性、旧主恢复和重启收敛 |
| 第八篇 | [PostgreSQL 接进来了，ClusterGuard HA 2.2 做到了什么](wechat-08-postgresql-ha.md) | PostgreSQL 原生身份、流复制切换、自动接管、写 VIP、旧主回挂和三节点实测 |

## 当前版本边界

ClusterGuard HA `2.1.45` 是 2.1 系列封板版本，正式支持范围是 MySQL 高可用。2.2 候选版本线已经完成 PostgreSQL 原生高可用功能，并通过 PostgreSQL 16.4 三节点实验室矩阵。Oracle Data Guard Broker 与 SQL Server Always On 属于后续独立适配和认证范围，不能使用 MySQL 或 PostgreSQL 的测试结论替代。

系列中的产品能力、命令和截图均以仓库文档、2.1.45 交付物和 2.2 PostgreSQL 验收记录为依据。MHA 与 Orchestrator 的描述引用各自官方项目资料。部署到生产前，仍需按现场操作系统、数据库版本、网络、存储和隔离条件重新验收。

想先用一个具体故障引出产品，可以从 [四篇短篇钩子](wechat-hooks.md) 开始。
