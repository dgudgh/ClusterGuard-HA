# ClusterGuard HA 2.2-101 完整安装介质

## 最终发布状态

截至 2026-09-11，源码已推送至 `codex/2.2-postgresql`，安装产物已发布到 [GitHub v2.2.101 预发布](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101)。交付物为**一份同时包含 MySQL 与 PostgreSQL 介质的安装包，以及单独的 ClusterGuard RPM**。

- 固定构建提交和 tag 指向：`78dbdbffc9645dd22c9867888cd11b4f2dc2bd89`。
- 构建时间：`2026-09-11T03:30:00Z`，即北京时间 11:30；干净源码，`source_tree_dirty=false`、`source_untracked_count=0`。
- GitHub 状态：`isDraft=false`、`isPrerelease=true`。包内 `release_channel=stable` 是构建器根据干净工作树生成的标记，**不代表现场验收或正式生产准入**；本轮没有实现新的显式渠道参数。
- 生产签名升级包 `.cgupgrade`：未生成、未发布。本次安装介质不能上传到控制台升级弹窗。

本页是发布后的文档补正。已发布包内的说明仍是构建时快照，其中旧提交/旧摘要以本页及 Release 的 `RELEASE-INFO`、`.sha256`、`verification.json` 为准。文档提交可以晚于构建提交；仅补正文档不重建同版本二进制、不移动 `v2.2.101`，避免再次改变已发布摘要。

## 下载与摘要

| 附件 | 大小（字节） | SHA256 |
| --- | ---: | --- |
| [MySQL + PostgreSQL 安装包](https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.101/clusterguard-ha-2.2-101-offline-linux-x86_64.tar.gz) | 316905938 | `c4c0d6659a7b4d1ff39ada3c38542eddbe2d1d91cede5a6a1a97d4954404a5f5` |
| [ClusterGuard RPM](https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.101/clusterguard-ha-2.2-101.x86_64.rpm) | 18430614 | `3dfa81bf09895b1be0e27c2e44a8d33b9ac51d9526b02ea5f1acc4b56770238c` |

Release 共六个附件：上述两个二进制、各自的 `.sha256`、[RELEASE-INFO](https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.101/RELEASE-INFO) 和 [verification.json](https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.101/verification.json)。本地重新计算的两个 SHA256 均与 GitHub 资产 API 的 `digest` 一致；小附件已下载逐字比对。没有把“大附件大小一致”写成重新下载完整二进制后的验收。

在下载目录校验：

```bash
sha256sum -c clusterguard-ha-2.2-101-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-2.2-101.x86_64.rpm.sha256
```

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制，minimal 不包含调试符号，保留数据库运行程序；ClusterGuard 程序、安装脚本及 Rocky Linux 8 x86_64 运行依赖一并打包。
- PostgreSQL：16.4 官方源码，加 Rocky Linux 8.10 x86_64 编译依赖 RPM 和仓库元数据，安装器支持在首个数据节点编译一次并分发。不是 PostgreSQL 的预编译二进制包；本次未执行目标主机安装和编译验收。
- 包内 `dependencies/COLLECTION-INFO` 标记 `postgresql_source_build=true`，安装器会识别并复用该目录；也可显式指定 `--postgresql-dependencies /path/to/extracted-kit/dependencies`。`RELEASE-INFO` 中的 `postgresql_build_dependencies=separate-online-first` 是构建器的通用固定标记，不能据此判断本包缺少编译依赖。
- 安装器按 `--engine mysql` 或 `--engine postgresql` 选择介质。默认生成计划，显式 `--execute` 才执行；完整参数见 [离线安装与部署手册](offline-rpm-install.md)。安装包用于新部署，不能覆盖已有数据库目录、部署状态或 Raft 数据。单独 RPM 仅包含 ClusterGuard 程序，不包含 MySQL/PG 数据库介质。
- 数据库版本保持已有测试基线；“最新”指 ClusterGuard 最新源码，不宣称 MySQL 8.0.44 / PostgreSQL 16.4 是上游最新版本。

## 修复与验证

| 项目 | 已有结果与边界 |
| --- | --- |
| 升级器安全修复 | `f76b2f4` 纳入 root 私有执行区、输入快照、降权结果发布和可信恢复记录；详见 [安全修复记录](updater-private-workspace-2026-09-11.md) |
| 包校验器 | `e26f3af` 去除 release=99、数据库数量=0 和固定测试公钥硬编码；显式校验版本、包数量、摘要、RPM、HTML、配置和脚本；存在 Go VCS 元数据时才校验该字段，缺失时不宣称二进制提供了 VCS 证明 |
| 源码回归 | 发布前 `go test ./...`、Helper 两包 race、`go vet ./...`、shell 语法和差异检查通过；此处引用发布轮次结果，本次文档更新未重跑业务测试 |
| 最终包核验 | `verification.json` 为 `passed`，构建提交 `78dbdbf`，内外摘要、版本、架构、嵌入 HTML、MySQL/PG 文件、JSON 和高置信凭据扫描通过；安装器仅运行 `--help` |
| 9 月 10 日隔离测试 | [三节点测试记录](helper-linux-field-test-2026-09-10.md) 记载当时 Helper UID/socket 与隔离 MySQL/PG 恢复结果；属于此前源码和夹具，不能替代最终 101 安装验收 |
| 9 月 11 日 workspace 检查 | 152–154 原生检查通过：可信目录/文件与快照可用，组或其他用户可写目录、目录和文件符号链接被拒绝 |
| 最终版现场安装与升级 | `deployed=false`、`field_acceptance=false`；未执行 101 安装、真实上传验签、滚动升级、回退、Leader 接管或业务集群灾难恢复 |

## 未完成项

1. 最终版安装后的 Helper socket/服务 UID、滚动升级和回退现场验收尚未采集。9 月 11 日检查记录称服务账号/unit/目标路径不可用，与 9 月 10 日已有安装的记录不一致；本次文档核对未重新 SSH，不能据此断言三台当前均未安装，需在后续现场验收重新确认。
2. 未确认与现场信任公钥匹配的生产补丁签名私钥，故未发布生产 `.cgupgrade`。校验器允许安装包不带补丁信任公钥；本包 `trust/` 没有该公钥，未把测试密钥作为生产信任配置。
3. 目标操作系统上的依赖签名信任、最终安装/PG 编译、现场 Raft Recovery Commit 和 VIP/lease 恢复仍需验收。历史 replication secret 轮换、PXC/Oracle/SQL Server 原生恢复不因安装包发布而完成。

## 基线与本次补正

- 原修复基线：`bc0546a`（2.2-68）与 `c8795a3`（2.2-99），99 包 `database_package_count=0`；本包为 2。
- 文档补正前对比：`git show 298dce2:docs/zh-CN/release-2.2.101.md` 与 `78dbdbf` 中的记录，发现提交/摘要停留在中间构建，且混用“两套介质”和统一包、计划中的渠道参数与已实现行为。
- 补正证据：最终包 `RELEASE-INFO`、`verification.json`、归档清单及 `dependencies/COLLECTION-INFO`，本地重新计算的 SHA256，GitHub Release 资产 `digest/state/size` 和远端 tag。
- 本次只修改发布说明、安全状态交叉引用和 Markdown 索引；原始历史测试结果保留，不倒填现场验收，不修改业务代码或已发布产物。

## 官方来源

- [MySQL 官方通用二进制安装说明](https://dev.mysql.com/doc/refman/8.0/en/binary-installation.html)
- [MySQL 8.0.44 归档介质](https://cdn.mysql.com/archives/mysql-8.0/mysql-8.0.44-linux-glibc2.17-x86_64-minimal.tar.xz)
- [PostgreSQL 16.4 官方源码与摘要](https://ftp.postgresql.org/pub/source/v16.4/)
