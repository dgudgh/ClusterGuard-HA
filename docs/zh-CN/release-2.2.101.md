# ClusterGuard HA 2.2-101 完整安装介质

## 交付计划与修改前基线

用户要求 MySQL / PostgreSQL 最新完整安装包和 GitHub 源码提交。本轮由当前代理执行，不调用其他模型，不重装 152–154，不覆盖历史交付。

- 当前工作树 `.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD `19d02bc`；GitHub 同名分支也是该提交，仓库 `dgudgh/ClusterGuard-HA` 为私有仓库。
- 旧版对照：`git show c8795a3:scripts/build-clusterguard-offline-kit.sh`，以及 99 包 `RELEASE-INFO` / 归档清单。99 包 `database_package_count=0`，PG 编译依赖标为 `separate-online-first`；不能称为内含数据库的完整介质。
- 当前安装器已经支持显式 `--postgresql-dependencies` 与源码只编译一次；本轮沿用此路径，不改变数据库初始化、身份、权限、VIP、Raft 或旧安装参数。
- 当前校验工具写死 release=99 / database_package_count=0；本轮构建前必须改为显式期望版本、对应归档名及数据库内容验证。
- 构建器仅凭干净 Git 工作树设置 stable 不等于现场验收。增加显式 candidate 渠道，发布为 GitHub prerelease；本轮已关闭代码层 root shell 共享目录风险，但真实安装后的 socket/升级回退现场验收仍不能冒充完成。
- 拟改范围：完整介质 PG 依赖嵌入与安装器自动发现、可显式指定的发布渠道、校验工具和回归测试。既有功能修复在提交前逐项检查，设计预览目录、原始现场 JSON、缓存、凭据、无关图片删除不混入提交。

## 执行清单

1. [完成] 验证数据库介质来源、摘要/签名、运行与编译依赖，记录旧代码差异。
2. [完成] 构建器/校验器与离线计划回归；默认仍为 plan，只有显式 execute 修改主机。
3. [完成] 全仓、race、vet、shell、UI 静态检查；真实数据库 Linux 测试的边界单独记录。
4. [进行中] 从干净提交构建两套不同文件名的 2.2-101 介质，验证内外清单、版本、嵌入 HTML 和凭据隔离。
5. [待做] 推送当前分支，上传 GitHub 预发布安装包与摘要，下载比对并记录入口。

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制，minimal 不包含调试符号，保留数据库运行程序；ClusterGuard 程序、安装脚本及 Rocky Linux 8 x86_64 运行依赖一并打包。
- PostgreSQL：16.4 官方源码，加 Rocky Linux 8 x86_64 离线编译依赖闭包，安装时在首个数据节点编译一次并分发。不是 PostgreSQL 的预编译二进制包。
- 两套均面向全新安装，不能上传到升级弹窗，也不能覆盖已有数据库目录、部署状态或 Raft 数据。端口、拓扑、VIP、凭据由安装参数指定，不写死现场秘密。
- 数据库版本保持已有测试基线；“最新”指 ClusterGuard 最新源码，不宣称 MySQL 8.0.44 / PostgreSQL 16.4 是上游最新版本。

## 已知限制

本次生成完整安装介质，不等于实际清空三台主机重装、业务灾难恢复或滚动升级验收完成。当前 Go Helper 的 Linux workspace root/服务 UID 边界已通过本地与 152–154 原生 workspace 检查，详见 `updater-private-workspace-2026-09-11.md`；但 152–154 没有安装后的 `clusterguard` 服务账号和 Helper unit，真实 socket/升级回退未采集。历史秘密轮换、PXC/Oracle/SQL Server 原生恢复仍不在本轮交付范围内。

## 官方来源

- [MySQL 官方通用二进制安装说明](https://dev.mysql.com/doc/refman/8.0/en/binary-installation.html)
- [MySQL 8.0.44 归档介质](https://cdn.mysql.com/archives/mysql-8.0/mysql-8.0.44-linux-glibc2.17-x86_64-minimal.tar.xz)
- [PostgreSQL 16.4 官方源码与摘要](https://ftp.postgresql.org/pub/source/v16.4/)
