# ClusterGuard HA 中文文档

离线 HTML 文档中心入口为 [`../html/index.html`](../html/index.html)，包含本手册、迁移流程、验收证据、本地搜索和打印页面，不依赖外部网络。

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/README.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


## 当前版本

### 最新安装介质预发布

[2.2-101 发布说明](release-2.2.101.md) 与 [GitHub v2.2.101](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101)：一份 MySQL 8.0.44 / PostgreSQL 16.4 安装介质及单独 ClusterGuard RPM，固定构建提交 `78dbdbf`。安装包已核验并上传；生产 `.cgupgrade` 和最终版现场安装、升级、回退验收尚未完成。包内 `release_channel=stable` 不改变其 GitHub 预发布状态。

升级器修复与剩余门槛见 [私有执行区安全记录](updater-private-workspace-2026-09-11.md)。下载摘要及最新文档补正以 2.2-101 发布说明为准。

### 历史正式版本入口

| 版本 | 状态 | 数据库支持边界 |
| --- | --- | --- |
| `2.1-45` | 正式封板 | MySQL 高可用控制平台 |
| `2.2-39` | 正式发布 | PostgreSQL 16.4、Docker Swarm，并保留 2.1 MySQL 能力 |
| 后续版本 | 规划 | Oracle Data Guard Broker、SQL Server Always On 独立验收 |

以下为本手册原有的 2.2-39 正式版下载入口，保留用于历史追溯；不代表 2.2-101 已正式验收：

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39>

候选包和预发布不能仅凭较高版本号替代正式 Release。交付物必须核对 GitHub Release 的发布状态及随包 SHA256。

## 推荐阅读顺序

最新安装介质先看 [2.2-101 发布说明](release-2.2.101.md)。2.2.48 及以后的中文发布说明
（`release-2.2.69.md` 至 `release-2.2.101.md`）按版本号倒序存放在本目录，**没有英文对应版本**，
英文侧发布说明只维护到 `release-2.2.47.md`。

下面按正式基线 `2.2-39` 的顺序编排：

1. [2.2.47 发布说明](release-2.2.47.md)
   确认正式包、摘要、支持范围和生产准入边界。
2. [2.2.46 发布说明](release-2.2.46.md)
3. [2.2.45 发布说明](release-2.2.45.md)
4. [2.2.44 发布说明](release-2.2.44.md)
5. [2.2.43 发布说明](release-2.2.43.md)
6. [2.2.42 发布说明](release-2.2.42.md)
7. [2.2.41 发布说明](release-2.2.41.md)
8. [2.2.40 发布说明](release-2.2.40.md)
9. [2.2.39 发布说明](release-2.2.39.md)
10. [产品导览](product-tour.md)
   通过实际控制台截图了解拓扑、操作、节点生命周期和操作日志。
11. [离线安装与部署手册](offline-rpm-install.md)
   完成控制节点、数据节点、数据库、Agent、Raft、VIP 和证书部署。
12. [数据库接入手册](database-preparation.md)
   准备数据库原生身份、最小权限、复制和健康检查条件。
13. [从 Orchestrator 迁移](orchestrator-migration.md)
   在不重装已有数据库的前提下接入 MySQL，并安全转移唯一恢复与 VIP 控制权。
14. [运维操作手册](operations-manual.md)
   执行切换、旧主恢复、节点扩容、计划关机、审计和应急处理。
15. [版本升级与回退手册](update-and-patch.md)
   验证签名升级包、生成变更计划、滚动升级、断点续跑和受控回退。
16. [版本与发版规范](version-release-policy.md)
   构建新版本、维护标签和发布 PostgreSQL 2.2 时使用。

## 验收证据

开发与维护先看 [源码、文件与测试导览](source-layout-and-testing.md) 和 [旧版对比及清理记录](dead-code-cleanup-2026-09-11.md)。测试归并不删除原有回归；本地、浏览器夹具和真实现场结果分别记录。

- [MySQL 旧主恢复专项验收](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [生产故障与并发测试报告](production-chaos-test-report-2026-08-09.md)
- [计划关机和自动恢复报告](power-lifecycle-test-report.md)
- [PostgreSQL 16.4 生产验收报告](postgresql-production-qualification-2026-08-23.md)
- [Docker Swarm MySQL 实机验证](docker-swarm-mysql-validation-plan.md)
- [Kubernetes MySQL 接管手册](kubernetes-mysql.md)

这些报告记录特定实验室、数据库包和日期下的结果。更换数据库小版本、Linux
发行版、存储、网络、VIP 网卡或隔离方式后，必须重新执行现场验收。

## PostgreSQL 2.2

PostgreSQL 从 2.2 开始，不回填到 `v2.1.45`。2.2 主离线包保持精简：明确指定
`--engine postgresql` 时默认在隔离构建根中联网解析源码编译依赖；现场无法联网时，
上传单独的 PostgreSQL 依赖包并使用 `--postgresql-dependencies`。

PostgreSQL 数据库权限、原生身份、流复制和恢复要求见
[数据库接入手册](database-preparation.md)；完整安装参数见
[离线安装与部署手册](offline-rpm-install.md)。

三节点 PostgreSQL 16.4 的计划轮换、虚拟机强制断电、网络分区、失去多数派、并发操作、写 VIP 唯一性和旧主 `pg_rewind` 结果，见
[PostgreSQL 生产验收报告](postgresql-production-qualification-2026-08-23.md)。

## Docker Swarm MySQL

Docker Swarm 第一阶段采用宿主机 Agent、固定 Service slot、MySQL GTID 复制和宿主机 VIP。设计边界、部署顺序与安全约束见
[Docker Swarm MySQL 接管手册](docker-swarm-mysql.md)，`192.168.102.152-154` 三节点的计划切换、自动故障切换、旧主回挂、Manager 中断、Raft 无多数派和重复 VIP 结果见
[Docker Swarm MySQL 实机验证](docker-swarm-mysql-validation-plan.md)。

## Kubernetes MySQL

Kubernetes 模式不迁移宿主机 VIP，也不修改 CoreDNS。ClusterGuard 使用 Raft 授权的
selectorless Service/EndpointSlice、独立单副本 StatefulSet、持久角色注解和启动守卫完成 MySQL 切换。
部署约束、RBAC、资源登记和当前验收边界见
[Kubernetes MySQL 接管手册](kubernetes-mysql.md)。该功能目前只有代码级自动化测试，尚无真实 Kubernetes 生产验收报告。

## 公众号系列

[ClusterGuard HA 公众号系列](wechat-series.md) 已更新 PostgreSQL 2.2 专题，适合用于产品介绍、技术选型和上线前沟通。正式部署参数仍以本手册和对应版本发布说明为准。

## 文档效力

以下文件是生产交付入口：

- `docs/zh-CN/release-*.md`
- `docs/zh-CN/offline-rpm-install.md`
- `docs/zh-CN/database-preparation.md`
- `docs/zh-CN/operations-manual.md`
- `docs/zh-CN/update-and-patch.md`
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
