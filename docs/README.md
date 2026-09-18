# ClusterGuard HA Documentation / 文档中心

ClusterGuard HA product documentation is maintained in English and Simplified Chinese. Choose one language and keep the paired page open during installation or operations.

ClusterGuard HA 产品文档同时维护英文和简体中文版本。安装、变更和排障时请选择对应语言，并以同版本文档为准。

## Offline HTML / 离线 HTML

Open [`html/index.html`](html/index.html) directly in a browser. The generated documentation center contains 65 HTML pages across both languages, local search, print styles, responsive navigation, and bundled screenshots; it does not require a web server or external CDN.

直接使用浏览器打开 [`html/index.html`](html/index.html)。当前跟踪的文档中心包含 65 个 HTML 页面（含中英文与语言入口）、本地搜索、打印样式、响应式目录和随包截图，不依赖 Web 服务或外部 CDN。

Rebuild after changing Markdown:

修改 Markdown 后重新生成：

```bash
cd docs
npm install
npm run build:html
```

## Product Documentation / 产品文档

Latest installer / 最新安装介质：[2.2-102 发布说明（中文）](zh-CN/release-2.2.102.md)。One kit contains MySQL and PostgreSQL media; it is built and verified locally but not uploaded and not accepted on site. 同一安装包包含 MySQL/PG 介质，已在本地构建并核验，尚未上传 GitHub、尚未现场验收。Previous / 上一版：[2.2-101 发布说明（中文）](zh-CN/release-2.2.101.md) · [GitHub v2.2.101](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101)。Updater follow-up / 升级器后续状态见 [私有执行区安全记录](zh-CN/updater-private-workspace-2026-09-11.md)。这些链接指向当前 Markdown；已发布包内文档仍保留构建时快照。

| Topic / 主题 | English | 简体中文 |
| --- | --- | --- |
| Documentation index / 文档索引 | [English](en-US/README.md) | [中文](zh-CN/README.md) |
| Product tour / 产品导览 | [English](en-US/product-tour.md) | [中文](zh-CN/product-tour.md) |
| Architecture / 架构 | [English](architecture.md) | [中文](zh-CN/architecture.md) |
| 2.2.47 release notes / 2.2.47 发布说明 | [English](en-US/release-2.2.47.md) | [中文](zh-CN/release-2.2.47.md) |
| 2.2.46 release notes / 2.2.46 发布说明 | [English](en-US/release-2.2.46.md) | [中文](zh-CN/release-2.2.46.md) |
| 2.2.45 release notes / 2.2.45 发布说明 | [English](en-US/release-2.2.45.md) | [中文](zh-CN/release-2.2.45.md) |
| 2.2.44 release notes / 2.2.44 发布说明 | [English](en-US/release-2.2.44.md) | [中文](zh-CN/release-2.2.44.md) |
| 2.2.43 release notes / 2.2.43 发布说明 | [English](en-US/release-2.2.43.md) | [中文](zh-CN/release-2.2.43.md) |
| 2.2.42 release notes / 2.2.42 发布说明 | [English](en-US/release-2.2.42.md) | [中文](zh-CN/release-2.2.42.md) |
| 2.2.41 release notes / 2.2.41 发布说明 | [English](en-US/release-2.2.41.md) | [中文](zh-CN/release-2.2.41.md) |
| 2.2.40 release notes / 2.2.40 发布说明 | [English](en-US/release-2.2.40.md) | [中文](zh-CN/release-2.2.40.md) |
| 2.2.39 release notes / 2.2.39 发布说明 | [English](en-US/release-2.2.39.md) | [中文](zh-CN/release-2.2.39.md) |
| 2.1.45 release notes / 2.1.45 发布说明 | [English](en-US/release-2.1.45.md) | [中文](zh-CN/release-2.1.45.md) |
| Offline RPM installation / 离线 RPM 安装 | [English](en-US/offline-rpm-install.md) | [中文](zh-CN/offline-rpm-install.md) |
| Database preparation / 数据库接入 | [English](en-US/database-preparation.md) | [中文](zh-CN/database-preparation.md) |
| Migration from Orchestrator / 从 Orchestrator 迁移 | [English](en-US/orchestrator-migration.md) | [中文](zh-CN/orchestrator-migration.md) |
| Operations manual / 运维手册 | [English](en-US/operations-manual.md) | [中文](zh-CN/operations-manual.md) |
| PostgreSQL HA / PostgreSQL 高可用 | [English](postgresql-ha.md) | [中文](zh-CN/postgresql-ha.md) |
| Version update and rollback / 版本升级与回退手册 | [English](en-US/update-and-patch.md) | [中文](zh-CN/update-and-patch.md) |
| MySQL proven methods / MySQL 已验证方法 | [English](proven-mysql-ha-methods.md) | [中文](zh-CN/proven-mysql-ha-methods.md) |
| MySQL feature acceptance / MySQL 功能验收 | [English](mysql-feature-parity-acceptance.md) | [中文](zh-CN/mysql-feature-parity-acceptance.md) |
| Version and release policy / 版本发布规范 | [English](en-US/version-release-policy.md) | [中文](zh-CN/version-release-policy.md) |

## Qualification Evidence / 验收证据

| Evidence / 证据 | English | 简体中文 |
| --- | --- | --- |
| Former-primary recovery / 旧主恢复 | [English](en-US/mysql-former-primary-recovery-qualification-2026-08-09.md) | [中文](zh-CN/mysql-former-primary-recovery-qualification-2026-08-09.md) |
| Production chaos tests / 生产故障测试 | [English](en-US/production-chaos-test-report-2026-08-09.md) | [中文](zh-CN/production-chaos-test-report-2026-08-09.md) |
| Planned shutdown lifecycle / 计划关机生命周期 | [English](en-US/power-lifecycle-test-report.md) | [中文](zh-CN/power-lifecycle-test-report.md) |
| MySQL production qualification / MySQL 生产验收 | [English](mysql-production-qualification-2026-07-28.md) | [中文](zh-CN/mysql-production-qualification-2026-07-28.md) |
| PostgreSQL 16.4 production qualification / PostgreSQL 16.4 生产验收 | [English](en-US/postgresql-production-qualification-2026-08-23.md) | [中文](zh-CN/postgresql-production-qualification-2026-08-23.md) |

Qualification reports record a specific laboratory build and date. They do not replace site acceptance after changing the database version, operating system, storage, network, VIP interface, or fencing policy.

验收报告只代表特定实验室版本和日期下的结果。数据库版本、操作系统、存储、网络、VIP 网卡或隔离策略变化后，必须重新执行现场验收。

## Internal Engineering Records / 内部工程记录

Source and test organization / 源码与测试维护入口：[文件与测试导览](zh-CN/source-layout-and-testing.md) · [旧版对比与清理记录](zh-CN/dead-code-cleanup-2026-09-11.md)。These development records distinguish local checks, browser fixtures, historical tools, and field validation; they are not a production acceptance claim.

`docs/superpowers/` contains design specifications and implementation plans for traceability. Each English record has a Simplified Chinese counterpart under `docs/superpowers/zh-CN/`. These records are historical engineering evidence, not current production runbooks.

`docs/superpowers/` 保存设计规格和实施计划，用于工程追溯。每份英文记录在 `docs/superpowers/zh-CN/` 下都有中文对应版本。这些文件属于历史工程证据，不是当前生产操作手册。

## Documentation Rules / 文档规则

- Never publish secrets, credentials, private keys, session cookies, or complete secret files.
- Commands, paths, API names, versions, and configuration keys must remain identical across languages.
- Product screenshots must come from the real console and must not imply unsupported capability.
- A feature is considered documented only when both language versions and their links pass validation.

- 禁止发布密码、令牌、私钥、会话 Cookie 或完整秘密文件。
- 命令、路径、API 名称、版本号和配置键在两个语言版本中必须一致。
- 产品截图必须来自真实控制台，不得暗示尚未交付的能力。
- 只有中英文版本及其链接都通过校验，功能文档才算完成。
