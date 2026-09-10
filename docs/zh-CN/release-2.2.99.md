# ClusterGuard HA 2.2-99 安装介质

本版交付平台一键安装器，不是控制台可上传的 `.cgupgrade` 补丁。保留 98 的界面、日志分页与集群联动、防误触锁、MySQL/PostgreSQL 恢复和升级保护逻辑；本轮仅修复 tar 运行包没有写入版本信息的问题，并把此前已测试的功能源码同步到 GitHub。

## 包含内容

- Linux x86_64 平台 RPM、tar 运行程序、多节点 `install_clusterguard.sh`。
- MySQL 和 PostgreSQL 安装/接入脚本、示例配置、中文手册。
- Rocky Linux 8 x86_64 基础运行依赖：libaio、ncurses-compat-libs、numactl-libs 及仓库元数据；不是任意发行版通用依赖包。
- 静态 Linux jq、升级验签公钥、内外 SHA256 校验清单。没有签名私钥或现场凭据。

**不包含数据库原厂软件、Docker 镜像及 PostgreSQL 源码编译依赖全量包。** 新装数据库时通过 `-r` 指定已批准的 MySQL 二进制包或 PostgreSQL 二进制/官方源码包；PostgreSQL 还需 `--database-version`。完全断网且使用 PG 源码时，必须另备与目标系统匹配的编译依赖，并指定 `--postgresql-dependencies`，不能依赖默认联网安装。

## 使用入口

在目标 Linux 安装机上先核对摘要、解压、查看帮助：

```bash
sha256sum -c clusterguard-ha-2.2-99-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.2-99-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-99-offline-linux-x86_64
sha256sum -c SHA256SUMS
bash install_clusterguard.sh --help
```

安装器默认 `--plan`，显式加 `--execute` 才会修改主机。控制台默认端口仍为 3000。请使用已审核 SSH 主机密钥与交互隐藏输入/受保护凭据文件，不要把密码写入命令历史。

已有集群不要用新装命令覆盖部署状态、Raft 数据、数据库目录或身份文件。该压缩包不能上传到升级弹窗；既有 98 RPM 和签名升级包原样保留，99 安装介质不冒充升级补丁。

## 验证边界

从独立、干净的已提交源码目录构建，`RELEASE-INFO` 和 RPM `BUILD-INFO` 记录版本及源码提交；包内两份控制程序必须嵌入与测试一致的 HTML。构建回归真实运行本机控制程序并核对编译参数、版本信息；Linux 制品另做格式、源码 revision、校验清单、配置及脚本检查。

执行记录见同目录构建审计和交付目录 `verification.json`、`test-results.json`。未执行目标 Linux 安装、现场滚动升级、真实数据库灾难恢复、VIP 验收或 50 个后台发现周期。本版作为待用户现场验收的预发布介质，不能将历史手册中的生产验收报告当成本版现场验收。
