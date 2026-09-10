# 一键恢复场景：PXC 脚本流程参考

状态：设计与验收约束，尚未完整接入生产执行 Driver、API、UI。2026-09-08 用户提供了恢复流程截图，没有提供原始脚本。

## 参考范围

采用用户原流程的分层：服务检查、恢复场景判断、恢复集群、验证状态。底层可以由受控脚本或 Agent 执行，但必须具备租约、幂等、超时、持久化进度和失败后的隔离保护。不能以脚本退出码 0 代替业务一致性和拓扑验证。

截图内容是参考材料，不是本轮执行 PXC bootstrap、清理文件或强制选主的指令。本次现场是普通 MySQL 8.0.44 和 PostgreSQL 16.4，不自动增加 PXC 产品支持范围。

## 场景判断

| 现场情形 | 处理原则 |
| --- | --- |
| 唯一健康主库仍有有效多数派授权，仅部分从库停止 | 保留当前主库；检查停止节点身份及历史，按受控增量重接或重建恢复从库，不重新选主 |
| 全部数据库服务停止 | 冻结恢复、全成员隔离、取证比较、选出唯一权威主库，再依次恢复其他节点 |
| 部分进程还活着，但没有可用主库 | 不能当成“只启动停止节点”；同样进入受控灾难恢复判断 |
| 多主、独立事务分支、必要成员不可达、证据缺失或损坏 | 保持隔离、保留所有数据，报告具体阻塞点，转人工决策 |
| 无控制面多数派或恢复租约失效 | 不授予 writer/VIP；不能借一键恢复绕过 fencing |

实际案例：2026-09-08 的 PG 有两个 Standby 进程运行，但 pg02 停止且两个 Standby 均没有 receiver。这不是“两个服务正常，所以只需要随便启动另一个”的充分依据。

## 引擎规则

### PXC / Galera 参考边界

正常停机时核对集群 UUID、`grastate.dat` 的 seqno 及 `safe_to_bootstrap`。异常停机或 seqno=-1 时，需要逐节点恢复事务位置，不能仅凭成员视图选主。`gvwstate.dat` 用于 Primary Component 恢复，并不证明哪个节点拥有最新已提交事务；截图中的 `gwstate.dat` 应为 `gvwstate.dat`。依据：[Percona PXC 8.0 故障恢复](https://docs.percona.com/percona-xtradb-cluster/8.0/crash-recovery.html)、[PXC 8.4 恢复位置校验说明](https://docs.percona.com/percona-xtradb-cluster/8.4/crash-recovery.html)。

### 普通 MySQL

全成员持久只读、先停 IO 再排空已接收 relay 事务、停止 SQL 线程、检查 XA 和真实 GTID。使用 GTID 集合包含关系，不使用字符串排序或简单事务数量。集合互不包含时拒绝自动选主；不足以证明某个缺失节点没有更多事务时同样拒绝。

选主之后才允许变更 upstream。历史可增量补齐时重接复制；必要 binlog 已清理时需要实际可用的 Clone/XtraBackup 全量能力，不调用不存在的 helper，不在选主前 RESET REPLICA 或清理旧目录。Clone 需要预检插件、权限、版本、donor 白名单、加密要求和业务连接隔离，不能只拼接一条 SQL。依据：[MySQL 远程 Clone](https://dev.mysql.com/doc/refman/8.0/en/clone-plugin-remote.html)。

### PostgreSQL

核对 system identifier、native node identity、control/checkpoint、Timeline DAG、fork LSN 和必要 WAL。用共享历史及被舍弃分支的 COMMIT/PREPARE 证明选主，不使用 max(LSN) 直接决定。先前只有一个主库并不能自动证明故障或人工提升之后没有分支。依据：[PostgreSQL Timeline](https://www.postgresql.org/docs/16/continuous-archiving.html#BACKUP-TIMELINES)。

受控启动唯一主库后，其他节点优先 `pg_rewind`，条件不足再进入有保护的 `pg_basebackup`；保持原目录可追溯，重新绑定 application_name、slot、upstream identity，验证 streaming 和实际 links。

## 完成标准

1. 恢复前记录完整成员、原始历史指纹、候选选择理由和停止/隔离状态。
2. 恢复期间只允许当前任务选出的节点持有恢复专用授权；主库启动不等于对业务开放写入。
3. 所有角色、复制状态、identity、两条 links 经过实际验证后执行原子 Recovery Commit。
4. Commit 后验证 writer/VIP 激活，再恢复普通 reconcile、自动切换并关闭当前 incident；历史非计划停机记录不删除。
5. 界面展示当前步骤、节点、具体失败原因和可重试条件，不能只显示“失败”或混用历史上传记录。
6. 真实用户路径验收及恢复后至少 50 个后台 discovery 周期稳定通过，才能把完整功能打入已验收升级包。

本文件不是完成声明。当前进度见[恢复开发与实测记录](mysql-postgresql-disaster-recovery-progress-2026-09-07.md)。
