# ClusterGuard HA 中文文档

## 当前版本

| 版本 | 状态 | 数据库支持边界 |
| --- | --- | --- |
| `2.1-45` | 正式封板 | MySQL 高可用控制平台 |
| `2.2.x` | 开发与验收 | PostgreSQL，保留 2.1 MySQL 能力 |
| 后续版本 | 规划 | Oracle Data Guard Broker、SQL Server Always On 独立验收 |

2.1 最终版本下载：

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.1.45>

不要使用本地目录中编号高于 `2.1-45` 的历史候选包替代正式 Release。正式交付物
必须来自 GitHub Release，并通过随包 SHA256 校验。

## 推荐阅读顺序

1. [2.1-45 发布说明](release-2.1.45.md)
   确认正式包、摘要、支持范围和生产准入边界。
2. [离线安装与部署手册](offline-rpm-install.md)
   完成控制节点、数据节点、数据库、Agent、Raft、VIP 和证书部署。
3. [数据库接入手册](database-preparation.md)
   准备数据库原生身份、最小权限、复制和健康检查条件。
4. [运维操作手册](operations-manual.md)
   执行切换、旧主恢复、节点扩容、计划关机、审计和应急处理。
5. [版本与发版规范](version-release-policy.md)
   构建新版本、维护标签和发布 PostgreSQL 2.2 时使用。

## 验收证据

- [MySQL 旧主恢复专项验收](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [生产故障与并发测试报告](production-chaos-test-report-2026-08-09.md)
- [计划关机和自动恢复报告](power-lifecycle-test-report.md)

这些报告记录特定实验室、数据库包和日期下的结果。更换数据库小版本、Linux
发行版、存储、网络、VIP 网卡或隔离方式后，必须重新执行现场验收。

## PostgreSQL 2.2

PostgreSQL 从 2.2 开始，不回填到 `v2.1.45`。2.2 主离线包保持精简：明确指定
`--engine postgresql` 时默认在隔离构建根中联网解析源码编译依赖；现场无法联网时，
上传单独的 PostgreSQL 依赖包并使用 `--postgresql-dependencies`。

PostgreSQL 数据库权限、原生身份、流复制和恢复要求见
[数据库接入手册](database-preparation.md)；完整安装参数见
[离线安装与部署手册](offline-rpm-install.md)。

## 文档效力

以下文件是生产交付入口：

- `docs/zh-CN/release-*.md`
- `docs/zh-CN/offline-rpm-install.md`
- `docs/zh-CN/database-preparation.md`
- `docs/zh-CN/operations-manual.md`
- `docs/zh-CN/version-release-policy.md`

`docs/superpowers/` 保存历史设计和实施计划，只用于追溯，不是当前安装或生产操作
手册。文档与正式 Release 不一致时，以对应 Release 内的 `RELEASE-INFO`、摘要文件
和该版本发布说明为准。

## 问题反馈所需信息

提交问题时至少提供：

- ClusterGuard 完整版本和 Git 提交；
- 数据库引擎、完整版本、端口和集群 UUID；
- 控制节点 Leader、Raft 成员和 quorum 状态；
- 操作 UUID、请求 ID、发生时间和操作类型；
- 脱敏后的操作报告、审计记录和 Agent/systemd 日志；
- 问题发生前后的主库、复制源和 VIP Owner。

不要在工单、截图或聊天记录中提交数据库密码、控制令牌、审批令牌、私钥或完整
`deployment-secrets.env`。
