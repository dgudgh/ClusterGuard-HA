# ClusterGuard HA 中文完整资料与历史链接目录

> 完整资料与历史链接目录，保留拆分前的手册、发布和验收索引。这里用于查找资料，不是每次修改的阅读顺序。

**开发或文档修改前先读[根规则](../../AGENTS.md)、[门禁执行流程](validation-gate-workflow.md)和[阅读顺序与适用规则](../development/rules/README.md)，再进入[开发模块](../development/README.md)。** 涉及升级契约范围的修改或动作前，完整读取[强制契约 v2](upgrade-validation-chain.md)。

## 按资料作用查找

| 资料作用 | 入口与效力 |
| --- | --- |
| 现行强制规范 | [阅读顺序与适用规则](../development/rules/README.md)、[强制契约](upgrade-validation-chain.md)、[发版规范](version-release-policy.md)、[许可与合规](licensing.md)；在触发动作前遵守 |
| 操作说明 | [中文场景入口](README.md)；满足适用规则后使用对应版本的安装、接入、运维和升级手册 |
| 设计参考 | 产品导览、架构与接口资料解释设计或使用边界，不替代强制规范 |
| 历史验收与发布 | 下方版本、带日期报告及工程记录用于追溯对应制品和验证范围，不能证明本次现场通过 |

> 门禁执行更新（2026-10-04）：开发使用 `--stage source --strict`；新包与现场使用 `--stage artifact/field --strict --acceptance-report FILE`，需真实证据。默认仍为 field，该记录的 ART/FIELD 未完成；详情见[分阶段门禁与更新流程](validation-gate-workflow.md)。

## 源码修复与验收记录

记录中的源码修复基线为 `7b643f4`（2026-10-03），唯一主线为 `codex/2.2-postgresql`。该次修复的升级与热修执行 v2 契约：热修失败使用独立 `retry`，滚动升级失败使用 `resume`。该次修复的新包与现场 ART/FIELD 验收记录仍为 **OPEN**，未产生新包或生产部署。见[实现与验收状态](upgrade-validation-chain-implementation-status.md)；不替代本次源码、制品和现场核验。

离线 HTML 文档中心入口为 [`../html/index.html`](../html/index.html)，包含本手册、迁移流程、验收证据、本地搜索和打印页面，不依赖外部网络。

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/README.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


## 已记录的版本与安装介质

### 2.2-105 至 2.2-101 介质记录

[2.2-105 发布说明](release-2.2.105.md)：该版本的安装介质，代码基线 `b383092`。把 2.2-104 之后在现场暴露并修复的一批缺陷固化进正式介质——**修正集群时钟不再让集群进入停机态**（拨钟后拓扑观测水位永久拒绝刷新 → VIP 归属租约停止续期 → 数据节点摘 VIP 并把实例设为只读）、**停止 MySQL 写入者每约 10 秒自我隔离的抖动**、「账户与偏好」并入「状态设置」、控制台直接显示"不可用"的原因、可从控制台安装签名热修补丁。这 27 个提交此前只以签名 `.cgpatch` 热修包的形式在现场存在，本版是它们第一次随正式版本交付。介质同时内嵌 MySQL 8.0.44 与 PostgreSQL 16.4 原厂介质。本地已按发版门禁构建并核验；**介质未上传 GitHub、未被现场验收**；三节点滚动升级、真实故障切换与 VIP 自动接管均未执行。

[2.2-104 发布说明](release-2.2.104.md)：上一份安装介质，**已由 2.2-105 取代**；代码基线 `259b796`（介质内 `RELEASE-INFO` 记录的是打包时的 HEAD `e01f5ce`，该提交只补完发布说明与索引、不含代码改动，两者代码等价）。相对 2.2-103 新增**运行参数只读视图**（控制台「设置 → 运行参数」展示配置文件的实际生效值、来源与「需重启」标记，凭据只显示环境变量名）与**引擎级集群策略**（自动切换观测次数、证据窗口、切换操作预算、维护抑制写入 Raft 复制存储，控制台可改、可审计、运行时每轮读取即刻生效）。策略变更与审计事件共用同一次 Raft 提交；策略读取失败或输入越界时设置页禁止写入。介质同时内嵌 MySQL 8.0.44 与 PostgreSQL 16.4 原厂介质。本地已按发版门禁构建并核验；**介质未上传 GitHub、未被现场验收**。本版另配了签名 `.cgupgrade`（`2.2-103 → 2.2-104`，实验室链，现场信任该链），本地保存、未上传 GitHub、未执行现场滚动升级。

[2.2-103 发布说明](release-2.2.103.md)：上一份安装介质，**已由 2.2-104 取代**；一份 MySQL 8.0.44 / PostgreSQL 16.4 完整离线安装介质及单独 ClusterGuard RPM，代码基线为 `68c3448`（**重建第二版**；首版基线 `eb741ee`）。本版为把 `AGPL-3.0-only` 许可文本装进产物而重出——首版构建于许可落地之前，包内无任何许可文件，RPM 头也写着 `Proprietary`，从未发布、从未交付。相对 2.2-102 含**一处新装行为变更**：未设置 `bootstrap_admin_password_env` 的全新安装，首次管理员口令改为控制面生成的随机口令，写入 `metadata.json` 同目录的 root-only 0600 文件，不再固定为 `admin123`；装维流程需相应调整。本地已按发版门禁构建并核验；生产 `.cgupgrade` 和最终版现场安装、升级、回退验收尚未完成。本版**介质**未上传 GitHub（仓库的 `AGPL-3.0-only` 许可已随文档分支发布），也未被现场验收，不能仅凭较高版本号当作已发布版本。

[2.2-102 发布说明](release-2.2.102.md)：上一份安装介质，**已由 2.2-103 取代**——它的包内文档取的是修正前快照，仍含本版改正的证据窗口次数、`cgctl` 全局标志顺序和首次口令说明。代码基线为 `7b36461`（介质内 `RELEASE-INFO` 记录的是打包时的 HEAD `2b9a449`；该提交只新增本发布说明与索引、不含代码改动，两者代码等价）。

[2.2-101 发布说明](release-2.2.101.md) 与 [GitHub v2.2.101](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101)：再上一份安装介质，固定构建提交 `78dbdbf`，已核验并上传为 GitHub 预发布。包内 `release_channel=stable` 不改变其预发布状态。

升级器修复与剩余门槛见 [私有执行区安全记录](updater-private-workspace-2026-09-11.md)。对应制品的下载摘要及文档补正见该版本的发布说明；不覆盖当前强制规则。

### 历史正式版本入口

| 版本 | 状态 | 数据库支持边界 |
| --- | --- | --- |
| `2.1-45` | 正式封板 | MySQL 高可用控制平台 |
| `2.2-39` | 正式发布 | PostgreSQL 16.4、Docker Swarm，并保留 2.1 MySQL 能力 |
| 后续版本 | 规划 | Oracle Data Guard Broker、SQL Server Always On 独立验收 |

以下为本手册原有的 2.2-39 正式版下载入口，保留用于历史追溯；不代表 2.2-101 已正式验收：

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39>

候选包和预发布不能仅凭较高版本号替代正式 Release。交付物必须核对 GitHub Release 的发布状态及随包 SHA256。

## 历史资料列表（原 2.2-39 基线）

2.2-105 介质记录见 [2.2-105 发布说明](release-2.2.105.md)。英文侧发布说明只维护到
`release-2.2.47.md`；此后的发布说明**没有英文对应版本**，只维护简体中文，现存
`release-2.2.69.md` 至 `release-2.2.105.md`，按版本号倒序存放在本目录。

下方保留原正式基线 `2.2-39` 的资料链接，列表次序不代表本次修改或动作的阅读优先级；现行任务先进入上方规则与场景入口。

- [2.2.47 发布说明](release-2.2.47.md)
   确认正式包、摘要、支持范围和生产准入边界。
- [2.2.46 发布说明](release-2.2.46.md)
- [2.2.45 发布说明](release-2.2.45.md)
- [2.2.44 发布说明](release-2.2.44.md)
- [2.2.43 发布说明](release-2.2.43.md)
- [2.2.42 发布说明](release-2.2.42.md)
- [2.2.41 发布说明](release-2.2.41.md)
- [2.2.40 发布说明](release-2.2.40.md)
- [2.2.39 发布说明](release-2.2.39.md)
- [产品导览](product-tour.md)
   通过实际控制台截图了解拓扑、操作、节点生命周期和操作日志。
- [离线安装与部署手册](offline-rpm-install.md)
   完成控制节点、数据节点、数据库、Agent、Raft、VIP 和证书部署。
- [数据库接入手册](database-preparation.md)
   准备数据库原生身份、最小权限、复制和健康检查条件。
- [从 Orchestrator 迁移](orchestrator-migration.md)
   在不重装已有数据库的前提下接入 MySQL，并安全转移唯一恢复与 VIP 控制权。
- [运维操作手册](operations-manual.md)
   执行切换、旧主恢复、节点扩容、计划关机、审计和应急处理。
- [版本升级与回退手册](update-and-patch.md)
   验证签名升级包、生成变更计划、滚动升级、断点续跑和受控回退。
- [版本与发版规范](version-release-policy.md)
   构建新版本、维护标签和发布 PostgreSQL 2.2 时使用。

## 历史验收证据

源码与测试追溯资料见 [源码、文件与测试导览](source-layout-and-testing.md) 和 [旧版对比及清理记录](dead-code-cleanup-2026-09-11.md)。测试归并不删除原有回归；本地、浏览器夹具和真实现场结果分别记录。

- [MySQL 旧主恢复专项验收](mysql-former-primary-recovery-qualification-2026-08-09.md)
- [生产故障与并发测试报告](production-chaos-test-report-2026-08-09.md)
- [计划关机和自动恢复报告](power-lifecycle-test-report.md)
- [PostgreSQL 16.4 生产验收报告](postgresql-production-qualification-2026-08-23.md)
- [Docker Swarm MySQL 实机验证](docker-swarm-mysql-validation-plan.md)

相关操作说明：[Kubernetes MySQL 接管手册](kubernetes-mysql.md)，不作为历史验收报告。

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
部署约束、RBAC、资源登记和记录中的验收边界见
[Kubernetes MySQL 接管手册](kubernetes-mysql.md)。该手册记录的验证范围只有代码级自动化测试，未提供真实 Kubernetes 生产验收报告。

## 公众号系列

[ClusterGuard HA 公众号系列](wechat-series.md) 已更新 PostgreSQL 2.2 专题，适合用于产品介绍、技术选型和上线前沟通。正式部署参数仍以本手册和对应版本发布说明为准。

## 文档效力

现行修改和动作先遵守根规则及适用强制契约；下列资料按作用使用，不是同级的生产通过证明：

- 强制规范：`docs/zh-CN/version-release-policy.md`、`docs/zh-CN/licensing.md`，以及上方链接的根规则、适用规则和升级契约。
- 操作说明：`docs/zh-CN/offline-rpm-install.md`、`docs/zh-CN/database-preparation.md`、`docs/zh-CN/operations-manual.md`、`docs/zh-CN/update-and-patch.md`；动作前满足适用规则，并核对对应运行版本。
- 设计参考：产品导览、架构、接口说明及工程设计材料；用于理解与追溯，不替代强制规范或实际验收。
- 历史记录：`docs/zh-CN/release-*.md` 和上述验收报告，只记录对应制品、源码与验证范围。

**发布渠道是硬性规则**：GitHub Release 只承载完整离线安装介质；签名 `.cgupgrade`
升级包（含旧 `.cgpatch`）只对签约企业客户交付、只留本地，**不得上传任何公开渠道**。
反过来，**不带完整离线介质的 Release 也不得留在公开渠道**。上传前后各跑一次
`node tools/verify-public-release-assets.cjs`，报 `status=passed` 方可上传；公开渠道出现
升级包即视为越权分发。详见[版本与发版规范 §3.1](version-release-policy.md)。

**许可是硬性条款**：交付线采用 `AGPL-3.0-only`，权威全文是仓库根目录 `LICENSE`。
`packaging/rpm/nfpm.yaml` 的 `license` 字段、RPM 内的
`/usr/share/doc/clusterguard-ha/LICENSE` 与各 README 必须与之一致；改动依赖或打包后
跑一次 `node tools/verify-license-consistency.cjs`。各类使用场景下的开源义务见
[许可与合规](licensing.md)。

`docs/superpowers/` 保存历史设计和实施计划，只用于追溯，不是当前安装或生产操作
手册。对应 Release 内的 `RELEASE-INFO`、摘要文件和该版本发布说明用于确认已交付
制品身份与历史行为，不能覆盖当前强制规则，也不替代本次验收。

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
