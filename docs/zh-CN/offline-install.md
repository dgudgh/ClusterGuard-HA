# ClusterGuard HA 离线安装

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../offline-install.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

已封板的 MySQL 生产版本是 ClusterGuard HA `2.1-45`。PostgreSQL
交付从 `2.2` 系列开始。支持的生产引导路径是
完整的离线套件及其多节点安装程序：

```text
clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
  install_clusterguard.sh
  packages/clusterguard-ha-2.1-45.x86_64.rpm
  packages/database/
  dependencies/
  docs/ClusterGuard-HA-离线安装与部署手册.md
```

上面列出的是已封板的 `2.1-45` MySQL 专用套件。本页所有 PostgreSQL 步骤都需要
`2.2` 或更高版本的套件，不要用 `2.1-45` 套件执行。

权威的当前部署指南是
[`docs/zh-CN/offline-rpm-install.md`](./offline-rpm-install.md)。它涵盖以下内容：

- 审核过的 SSH 主机密钥和受保护的站点状态；
- 三节点奇数控制平面安装；
- 混合和分离的控制器/数据节点布局；
- MySQL 8.0/8.4，UPSQL 兼容的软件包暂存、依赖项、VIP 和
  验证；
- 后续节点生命周期和恢复边界。

在构建或发布新软件包之前，请参阅已封板的 [2.1-45 版本说明](./release-2.1.45.md) 和
[版本/发布策略](./version-release-policy.md)。

在每次生产部署之前使用 `install_clusterguard.sh --plan`。除非明确提供 `--execute`，
否则它不会更改远程主机。基于 VIP 的 MySQL HA 默认使用稳定 MySQL 故障证据、Raft 多数过渡租约和本地 Agent 的
失败时阻断 VIP/只读协调，实现数据库级别的自动故障转移。`--fencer` 是一个可选的第二层，适用于具有 BMC、PDU、云或虚拟化隔离的站点。仅在有意禁用自动恢复时使用
`--manual-failover-only`；它不能与 `--fencer` 或 `--fencer-assets` 同时配置。

不要使用旧的每节点配置示例来替代多节点安装程序。每节点的 `clusterguard-install.sh` 辅助工具在控制平面存在后由平台生命周期工作流使用；它不是初始的生产引导过程。

可以使用可重复的 `--database-package` 构建器选项将审批的数据清单档嵌入到正式套件中。否则，将审批的 MySQL 或 UPSQL 兼容的二进制存档，或 PostgreSQL 二进制文件或官方发布源存档，放在 `packages/database/` 下。安装程序会自动发现一个引擎/版本匹配，因此 `-r` 仅用于解决故意的歧义。PostgreSQL 源代码模式在选定的数据节点上编译一次，验证生成的二进制文件，创建构建清单和 SHA256 摘要，并将该确切的工件分发到所有节点。

主套件中仅包含小型、签名的 ClusterGuard 和 MySQL 运行时仓库。PostgreSQL 源代码构建依赖项优先在线：选定的构建节点从其配置的仓库中解析签名的软件包到一个临时的 DNF 安装根目录，而不会修改主机的软件包数据库。如果在线解析失败，请使用 `tools/收集RHEL离线依赖.sh --postgresql-source-build` 和
`tools/构建PostgreSQL依赖包.sh` 创建并上传单独的 PostgreSQL 依赖项附加组件，然后使用
`--postgresql-dependencies <extracted-directory>/dependencies` 重试。
