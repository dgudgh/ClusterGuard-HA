# 后端持久化基础设施

[返回后端功能](README.md)

仅在业务功能已定位到写入、复制、CAS、快照或保留问题时读本页；先回到该业务功能确认记录类型和成功判定。

## 粗略路径

`业务服务 → Repository → Raft/本地存储 → 记录与审计 → 读取投影`。升级部署文件、数据库原生恢复证据和 Raft 元数据不是同一种存储，不可按相似字段混为一个真相。

| 问题 | 从哪里继续 |
| --- | --- |
| 通用存储装配与快照 | [repository.go](../../../internal/store/repository.go)、[consensus](../../../internal/consensus) |
| 软件升级的 Raft 门禁 | [software_update.go](../../../internal/store/software_update.go)、[升级动作](settings/update-actions.md) |
| 升级私有文件和幂等历史 | [部署与操作历史](settings/update-history.md) |
| 策略与审计的共同提交 | [引擎策略](settings/cluster-policy.md) |
| 日志查询或筛选 | [日志与审计](audit.md) |

涉及这些层时核对 CAS 归属、失败是否留下不完整记录、旧任务证据是否仍参与安全判断。复制问题看[复制测试](../../../internal/store/replication_test.go)，具体记录类型再执行其单独测试；不因触碰一个查询就要求阅读全仓存储实现。
