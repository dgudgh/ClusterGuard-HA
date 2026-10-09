# ClusterGuard HA Full Catalogue and Historical Links / 完整资料与历史链接目录

> 完整资料与历史链接目录，保留拆分前的手册、发布和验收索引。这里用于查找资料，不是每次修改的阅读顺序。 / Full catalogue of manuals and historical release and acceptance links; this is a lookup index, not a reading order for each change.

**修改前先读[根规则](../AGENTS.md)、[门禁执行流程](zh-CN/validation-gate-workflow.md)和[阅读顺序与适用规则](development/rules/README.md)，再进入[开发模块](development/README.md)。** 涉及升级契约范围的修改或动作前，完整读取[强制契约 v2](zh-CN/upgrade-validation-chain.md)。 / Before changes, read the repository rules, gate workflow, and applicable rules, then select a development module; read the complete mandatory contract before changes or actions within its scope.

## 当前完整介质 / Current Installation Kit

[3.1.2.8 双语发布说明 / release notes](zh-CN/release-3.1.2.8.md) · [GitHub v3.1.2.8](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.2.8)。产品版本 3.1.2.8，RPM 兼容基线 2.2-106；GitHub 预发布，FIELD OPEN。 / Product 3.1.2.8, RPM baseline 2.2-106; prerelease with FIELD OPEN. Historical entries below preserve their original scope.

## Document Roles / 资料作用

| Role / 作用 | Entry / 入口 |
| --- | --- |
| Current mandatory rules / 现行强制规范 | [Reading order / 阅读顺序](development/rules/README.md)、[Contract / 强制契约](upgrade-validation-chain.md)、[Release policy / 发版规范](zh-CN/version-release-policy.md)、[Licensing / 许可](zh-CN/licensing.md) |
| Operations instructions / 操作说明 | [Task entry / 场景入口](README.md) → [中文手册](zh-CN/README.md) / [English manuals](en-US/README.md)，满足适用规则后使用对应版本手册 / use the matching version after applicable rules |
| Design and reference / 设计参考 | [Architecture / 架构](architecture.md)、[Product tour / 产品导览](en-US/product-tour.md)；说明设计或接口，不能替代强制规范 / explanations do not replace mandatory rules |
| Historical releases and acceptance / 历史发布与验收 | 下方版本、带日期报告及工程记录用于追溯对应制品和验证范围，不能证明本次现场通过 / records below describe their artifacts and validation scope, not present site acceptance |

门禁开发与验收已按阶段推进，详见[执行和更新流程](zh-CN/validation-gate-workflow.md)；默认最终验收仍需要真实 ART/FIELD 证据。 / Gate execution now separates source, artifact, and field stages; final acceptance still requires actual evidence.

## 源码修复与验收记录 / Recorded Source Repair and Acceptance

记录中的源码修复基线为 `7b643f4`（2026-10-03），唯一主线为 `codex/2.2-postgresql`。该次修复的升级与热修执行 v2 契约：热修失败使用独立 `retry`，滚动升级失败使用 `resume`。该次修复的新包与现场 ART/FIELD 验收记录仍为 **OPEN**，未产生新包或生产部署；不替代本次源码、制品和现场核验。

Recorded source repair baseline: `7b643f4` (2026-10-03), on the sole mainline `codex/2.2-postgresql`. That repair enforces the v2 contract: failed hotfixes use `retry`; interrupted rolling updates use `resume`. Its new-artifact and site ART/FIELD acceptance record remains **OPEN**; that repair produced no new package or production deployment. This does not replace verification of the current checkout, package, or site.

[中文实现状态](zh-CN/upgrade-validation-chain-implementation-status.md) · [English implementation status](en-US/upgrade-validation-chain-implementation-status.md)

热修台账保留旧签名包的实际行为，不表示旧包包含 v2 修复。 / Hotfix catalogues describe existing signed artifacts; they do not imply that those artifacts contain the v2 source fixes.

ClusterGuard HA product documentation is maintained in English and Simplified Chinese. Choose one language and keep the paired page open during installation or operations.

ClusterGuard HA 产品文档同时维护英文和简体中文版本。安装、变更和排障时请选择对应语言，并以同版本文档为准。

## License / 许可

ClusterGuard HA is licensed under the **GNU Affero General Public License, version 3 only** (`AGPL-3.0-only`); the authoritative text is [`../LICENSE`](../LICENSE). You may use, modify and sell it, but a modified version made available to others — including as a network service only — must have its Corresponding Source published under the same license. Third-party components are listed in [`../THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md). Run `node tools/verify-license-consistency.cjs` after any dependency or packaging change.

ClusterGuard HA 采用 **GNU Affero 通用公共许可证第 3 版（仅此版本，`AGPL-3.0-only`）** 授权，权威全文见 [`../LICENSE`](../LICENSE)。你可以自由使用、修改、收费分发；但修改后的版本一旦对外提供——包括只作为网络服务提供——就必须按同一许可开放对应源码。第三方组件见 [`../THIRD-PARTY-NOTICES.md`](../THIRD-PARTY-NOTICES.md)。依赖或打包改动后请运行 `node tools/verify-license-consistency.cjs`。

详细义务见 [许可与合规](zh-CN/licensing.md) / [Licensing and Compliance](en-US/licensing.md)。

## Offline HTML / 离线 HTML

Open [`html/index.html`](html/index.html) directly in a browser. The documentation center covers both languages with local search, print styles, responsive navigation, and bundled screenshots; it does not require a web server or external CDN. Its page list is generated from the markdown sources, so every release note under `docs/zh-CN/` and `docs/en-US/` is reachable, including the Chinese-only notes after 2.2.47.

直接使用浏览器打开 [`html/index.html`](html/index.html)。文档中心含中英文、本地搜索、打印样式、响应式目录和随包截图，不依赖 Web 服务或外部 CDN；页面清单由 markdown 源生成，因此两侧目录下的每份发布说明都可直达，包括 2.2.47 之后仅中文的版本。

Rebuild after changing Markdown:

修改 Markdown 后重新生成：

```bash
cd docs
npm install
npm run build:html
```

## Manuals, Rules and Historical Release Links / 手册、规范与历史发布链接

Recorded installer / 已记录的 2.2-105 安装介质：[2.2-105 发布说明（中文）](zh-CN/release-2.2.105.md)。Baseline `b383092`. It freezes the defects that 2.2-104 exposed in the field into a release medium. Correcting a cluster clock no longer takes the cluster down: a rewind used to leave the topology observation watermark in the future, every later refresh was then rejected as out of order, the VIP ownership lease stopped being renewed, and the data nodes released the VIP and set every instance read-only — fixing the clock was itself what stopped the cluster. The MySQL writer that isolated itself roughly every ten seconds is aligned again, 账户与偏好 folds into 状态设置, the console names why a section is unavailable, and a signed hotfix patch can be installed from the console. These 27 commits had only ever reached a site as signed `.cgpatch` bundles; this is the first medium that carries them. 基线 `b383092`：把 2.2-104 之后在现场暴露并修复的一批缺陷固化进正式介质。**修正集群时钟不再让集群进入停机态**（拨钟后拓扑观测水位留在未来 → 每次刷新按顺序违规被拒 → VIP 归属租约停止续期 → 数据节点摘 VIP 并把实例设为只读），**停止 MySQL 写入者每约 10 秒自我隔离的抖动**，「账户与偏好」并入「状态设置」，控制台直接显示“不可用”的原因，可从控制台安装签名热修补丁。这 27 个提交此前只以签名 `.cgpatch` 热修包的形式在现场存在，本版是它们第一次随正式版本交付。Built and verified locally, not uploaded to GitHub and not accepted on site; the three-node rolling upgrade, real failover and VIP takeover have not been run. 已在本地构建核验，未上传 GitHub、未现场验收；三节点滚动升级、真实故障切换与 VIP 自动接管均未执行。Previous / 上一版：[2.2-104 发布说明（中文）](zh-CN/release-2.2.104.md)（已由 2.2-105 取代；代码基线 `259b796`，介质内 `RELEASE-INFO` 记录的是打包提交 `e01f5ce`，该提交只补完发布说明与索引、不含代码改动，两者代码等价；本版另配了签名 `.cgupgrade`（`2.2-103 → 2.2-104`，实验室链，现场信任该链），本地保存、未上传 GitHub）。Earlier / 再上一版：[2.2-103 发布说明（中文）](zh-CN/release-2.2.103.md)。One kit contains MySQL and PostgreSQL media; this is the **second build** of 2.2-103, reissued so that the artifacts themselves carry the `AGPL-3.0-only` license (the first build predates the license and contained no license file). It is built and verified locally but not uploaded and not accepted on site. 同一安装包包含 MySQL/PG 介质；这是 2.2-103 的**第二次构建**，为把 `AGPL-3.0-only` 许可装进产物而重出（首版构建于许可落地之前，包内无许可文件）。已在本地构建并核验，尚未上传 GitHub、尚未现场验收。This version also changes the fresh-install administrator credential: without `bootstrap_admin_password_env` the controller now generates a random password into a root-only 0600 file instead of falling back to `admin123`. 本版另含一处新装行为变更：未设置 `bootstrap_admin_password_env` 时，首次管理员口令改由控制面随机生成并写入 root-only 0600 文件，不再回退到 `admin123`。Previous / 上一版：[2.2-102 发布说明（中文）](zh-CN/release-2.2.102.md)（已由 2.2-103 取代，其包内文档为修正前快照） · [2.2-101 发布说明（中文）](zh-CN/release-2.2.101.md) · [GitHub v2.2.101](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.101)。Updater follow-up / 升级器后续状态见 [私有执行区安全记录](zh-CN/updater-private-workspace-2026-09-11.md)。这些链接指向当前 Markdown；2.2-103 之后的包内文档仍会保留构建时快照。英文侧发布说明止于 `en-US/release-2.2.47.md`；此后的发布说明只维护简体中文版，现存 2.2.69–2.2.73、2.2.86、2.2.88–2.2.105（其间号段未生成发布说明，不是断链）。下表只列中英成对的文档，因此发布说明一栏停在 2.2.47。中文场景入口见 [中文文档索引](zh-CN/README.md)，发布记录列表见 [中文完整资料目录](zh-CN/catalogue.md)。

| Role / 作用 | Topic / 主题 | English | 简体中文 |
| --- | --- | --- | --- |
| Task entry / 场景入口 | Documentation index / 文档索引 | [English](en-US/README.md) | [中文](zh-CN/README.md) |
| Design or reference / 设计参考 | Product tour / 产品导览 | [English](en-US/product-tour.md) | [中文](zh-CN/product-tour.md) |
| Design or reference / 设计参考 | Architecture / 架构 | [English](architecture.md) | [中文](zh-CN/architecture.md) |
| Historical release / 历史发布 | 2.2.47 release notes / 2.2.47 发布说明 | [English](en-US/release-2.2.47.md) | [中文](zh-CN/release-2.2.47.md) |
| Historical release / 历史发布 | 2.2.46 release notes / 2.2.46 发布说明 | [English](en-US/release-2.2.46.md) | [中文](zh-CN/release-2.2.46.md) |
| Historical release / 历史发布 | 2.2.45 release notes / 2.2.45 发布说明 | [English](en-US/release-2.2.45.md) | [中文](zh-CN/release-2.2.45.md) |
| Historical release / 历史发布 | 2.2.44 release notes / 2.2.44 发布说明 | [English](en-US/release-2.2.44.md) | [中文](zh-CN/release-2.2.44.md) |
| Historical release / 历史发布 | 2.2.43 release notes / 2.2.43 发布说明 | [English](en-US/release-2.2.43.md) | [中文](zh-CN/release-2.2.43.md) |
| Historical release / 历史发布 | 2.2.42 release notes / 2.2.42 发布说明 | [English](en-US/release-2.2.42.md) | [中文](zh-CN/release-2.2.42.md) |
| Historical release / 历史发布 | 2.2.41 release notes / 2.2.41 发布说明 | [English](en-US/release-2.2.41.md) | [中文](zh-CN/release-2.2.41.md) |
| Historical release / 历史发布 | 2.2.40 release notes / 2.2.40 发布说明 | [English](en-US/release-2.2.40.md) | [中文](zh-CN/release-2.2.40.md) |
| Historical release / 历史发布 | 2.2.39 release notes / 2.2.39 发布说明 | [English](en-US/release-2.2.39.md) | [中文](zh-CN/release-2.2.39.md) |
| Historical release / 历史发布 | 2.1.45 release notes / 2.1.45 发布说明 | [English](en-US/release-2.1.45.md) | [中文](zh-CN/release-2.1.45.md) |
| Operations instructions / 操作说明 | Offline installation entry / 离线安装入口 | [English](offline-install.md) | [中文](zh-CN/offline-install.md) |
| Operations instructions / 操作说明 | Offline RPM installation / 离线 RPM 安装 | [English](en-US/offline-rpm-install.md) | [中文](zh-CN/offline-rpm-install.md) |
| Operations instructions / 操作说明 | Database preparation / 数据库接入 | [English](en-US/database-preparation.md) | [中文](zh-CN/database-preparation.md) |
| Operations instructions / 操作说明 | Migration from Orchestrator / 从 Orchestrator 迁移 | [English](en-US/orchestrator-migration.md) | [中文](zh-CN/orchestrator-migration.md) |
| Operations instructions / 操作说明 | Operations manual / 运维手册 | [English](en-US/operations-manual.md) | [中文](zh-CN/operations-manual.md) |
| Operations instructions / 操作说明 | PostgreSQL HA / PostgreSQL 高可用 | [English](postgresql-ha.md) | [中文](zh-CN/postgresql-ha.md) |
| Operations instructions / 操作说明 | Version update and rollback / 版本升级与回退手册 | [English](en-US/update-and-patch.md) | [中文](zh-CN/update-and-patch.md) |
| Design or reference / 设计参考 | MySQL proven methods / MySQL 已验证方法 | [English](proven-mysql-ha-methods.md) | [中文](zh-CN/proven-mysql-ha-methods.md) |
| Acceptance reference / 验收记录 | MySQL feature acceptance / MySQL 功能验收 | [English](mysql-feature-parity-acceptance.md) | [中文](zh-CN/mysql-feature-parity-acceptance.md) |
| Mandatory rules / 强制规范 | Version and release policy / 版本发布规范 | [English](en-US/version-release-policy.md) | [中文](zh-CN/version-release-policy.md) |
| Mandatory rules / 强制规范 | Licensing and compliance / 许可与合规 | [English](en-US/licensing.md) | [中文](zh-CN/licensing.md) |
| Operations instructions / 操作说明 | Kubernetes MySQL / Kubernetes MySQL 接管 | [English](en-US/kubernetes-mysql.md) | [中文](zh-CN/kubernetes-mysql.md) |
| Operations instructions / 操作说明 | Docker Swarm MySQL / Docker Swarm MySQL 接管 | [English](en-US/docker-swarm-mysql.md) | [中文](zh-CN/docker-swarm-mysql.md) |
| Design or reference / 设计参考 | Control-plane and API reference / 控制面与 API 参考 | [English](operations.md) | [中文](zh-CN/operations.md) |


## Historical Qualification Evidence / 历史验收证据

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

Source and test reference / 源码与测试追溯资料：[文件与测试导览](zh-CN/source-layout-and-testing.md) · [旧版对比与清理记录](zh-CN/dead-code-cleanup-2026-09-11.md)。These development records distinguish local checks, browser fixtures, historical tools, and field validation; they are not a production acceptance claim.

`docs/superpowers/` contains design specifications and implementation plans for traceability. Each English record has a Simplified Chinese counterpart under `docs/superpowers/zh-CN/`, except the bilingual index `docs/superpowers/README.md` itself. These records are historical engineering evidence. Current changes and actions follow the repository rules and applicable mandatory contracts. A release’s RELEASE-INFO, checksums and release notes establish its delivered identity and historical behavior; they cannot override current mandatory rules.

`docs/superpowers/` 保存设计规格和实施计划，用于工程追溯。除中英合并的索引 `docs/superpowers/README.md` 本身外，每份英文记录在 `docs/superpowers/zh-CN/` 下都有中文对应版本。这些文件属于历史工程证据。当前修改和动作遵守根规则及适用强制契约；对应 Release 的 RELEASE-INFO、摘要和发布说明用于确认已交付身份与历史行为，不能覆盖当前强制规则。

## Documentation Rules / 文档规则

- Never publish secrets, credentials, private keys, session cookies, or complete secret files.
- Commands, paths, API names, versions, and configuration keys must remain identical across languages.
- Product screenshots must come from the real console and must not imply unsupported capability.
- A feature is considered documented only when both language versions and their links pass validation.

- 禁止发布密码、令牌、私钥、会话 Cookie 或完整秘密文件。
- 命令、路径、API 名称、版本号和配置键在两个语言版本中必须一致。
- 产品截图必须来自真实控制台，不得暗示尚未交付的能力。
- 只有中英文版本及其链接都通过校验，功能文档才算完成。
