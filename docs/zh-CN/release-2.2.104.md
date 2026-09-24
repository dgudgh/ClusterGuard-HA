# ClusterGuard HA 2.2-104 发布说明

## 本次变更

- 新增运行参数查看：展示 `/etc/clusterguard/clusterguard.json` 的实际生效值、来源、脱敏凭据和是否需要重启。
- 新增 MySQL、PostgreSQL 引擎级集群策略：观测次数、证据窗口、切换操作预算和维护抑制均可通过复制存储热更新。
- 策略变更与审计事件使用同一次 Raft 快照提交；共识提交失败时不会发布半个变更。
- 设置页在策略读取失败或输入超出范围时禁止写入，避免未知状态覆盖其他引擎策略。
- 保持完整离线介质交付方式，介质同时嵌入 MySQL 与 PostgreSQL 原厂数据库包。

## 验证范围

- `go build ./...`
- `go vet ./...`
- `go test ./... -count=1`
- MySQL 与 PostgreSQL 节点安全、会话边界、真实点击和窄屏布局浏览器审计。
- 运行参数策略读取失败、数值校验、策略保留其他引擎设置的真实点击审计。
- `verify-license-consistency.cjs` 与离线介质只读校验。

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制。
- PostgreSQL：16.4 官方源码包。**源码编译依赖不在主介质内**：明确选择 PostgreSQL 源码时，
  安装器默认从构建节点已配置、启用签名校验的软件源联网安装到一次性隔离根，不升级生产宿主机；
  软件源不可用时，上传与目标发行版、主版本、架构匹配的
  `clusterguard-ha-*-postgresql-build-deps-*.tar.gz`，解压后用 `--postgresql-dependencies` 重试。
- `dependencies/` 只带控制面与 MySQL 启动所需的小型运行依赖闭包（`libaio`、`ncurses-compat-libs`、
  `numactl-libs`，共 3 个 RPM）及其 `repodata`。**相对 2.2-103 少了 PostgreSQL 源码编译依赖闭包**
  —— 2.2-103 的 `dependencies/` 含 268 个 RPM，主要供 PG 源码编译使用；本版介质体积因此由约
  302 MiB 降至约 113 MiB。这是构建脚本的既有设计（见包内 `dependencies/README.txt` 与
  `RELEASE-INFO` 的 `postgresql_build_dependencies=separate-online-first`），MySQL 场景不受影响。
- ClusterGuard Linux x86_64 RPM、Linux amd64 tar 运行程序、多节点 `install_clusterguard.sh`、
  MySQL/PostgreSQL 接入脚本、示例配置与离线文档中心（`docs/`）。
- 静态 Linux `jq` 与内外两层 SHA256 校验清单。**不包含签名私钥、现场凭据或生产补丁签名公钥**
  （`trust/` 为空目录）。

## 使用入口

```bash
sha256sum -c clusterguard-ha-2.2-104-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.2-104-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-104-offline-linux-x86_64
sha256sum -c SHA256SUMS
bash install_clusterguard.sh --help
```

安装器默认只生成计划，显式 `--execute` 才修改主机。首次登录前先取得初始口令：部署设置了
`bootstrap_admin_password_env` 时用该值；未设置时读取控制节点上 `<metadata_path>` 同目录的
`bootstrap-admin-password`（0600，仅 root 可读）。**本版安装器在交互终端下会先从 Leader 读回
真实口令再显示**，不再打印固定的 `admin123`（2.2-103 介质仍有该旧行为）。

已有集群不要用新装命令覆盖部署状态、Raft 数据、数据库目录或身份文件。本压缩包不能上传到升级弹窗；
需要现场升级请使用对应来源版本的签名 `.cgupgrade`。

## 现场验收状态

现场三节点安装、数据库原生切换和升级执行仍需在目标环境按发布验收清单单独验证；本包不替代现场验签和滚动升级验收。
