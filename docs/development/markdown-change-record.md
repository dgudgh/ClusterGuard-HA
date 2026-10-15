# Markdown 变更记录

> **规则：本仓任何 Markdown 的新增、删除、修改，都必须在下方登记表留下一条记录，写清文件路径、变更类型（`A` 新增 / `M` 修改 / `D` 删除）和改了什么。** 重命名按「删旧 + 增新」两条记录，不引入 `R`。
> **门禁：** `node tools/verify-markdown-change-record.cjs`（结构 + 覆盖）；`--self-test` 做变异验证。
> **范围：** 只登记 `.md`，并且只登记**入库**的 `.md`。`release/`、`.build/`、`.worktrees/` 不入 git，其中的 md 不在门禁范围。其他 Markdown（`docs/html/`）由 `docs/build-html-docs.mjs` 从这些 md 生成，改了 md 必须重建，但生成页不逐页登记，只写进下方批次说明。

本页登记覆盖的起点提交（门禁按它计算「基线 → 当前工作树」的 md 净变更）：

```text
recorded-through: 73ff4a3
```

门禁的判定规则：从上述基线到当前工作树、每个发生增删改的 `.md` 都必须在下表出现且类型一致；基线以来没有变化的 md 不得出现在表里。**尚未 `git add` 的文件不在门禁范围内**，门禁每次运行都会把它们列出来提醒。

## 怎么记

1. 改完 md 先 `git add`，再跑 `node tools/verify-markdown-change-record.cjs`；门禁会直接列出「变了但没登记」和「登记了但没变」的文件。
2. 一个文件在基线上不存在 → `A`；存在且内容变了 → `M`；被删掉 → `D`。同一个文件在基线以来既新增又修改，只记一条 `A`。
3. 说明写清楚**改了什么**，不写「优化文档」「同步更新」这类空话；至少写清被改动的规则、数字或示例。
4. 台账变长、需要重置基线时，把 `recorded-through` 换成新的提交号，并在批次说明里写明「此前批次已归档到本页历史」，不要悄悄改基线。

## 登记表

| 日期 | 文件 | 变更 | 说明 |
| --- | --- | --- | --- |
| 2026-10-15 | `docs/development/update-helper-namespace-2026-10-15.md` | A | 记录3.1.3.4真实Helper226、旧unit对照、systemd239原生复现/修复、隔离不变量与用户自行续装边界。 |
| 2026-10-15 | `docs/zh-CN/release-3.1.3.5.md` | A | 新候选3.1.3.5/RPM113修复易失运行目录与namespace启动顺序，保留已完成MySQL并复用原站点文件续装。 |
| 2026-10-15 | `docs/en-US/release-3.1.3.5.md` | A | 同范围英文修复说明及原生隔离/ART/FIELD分层边界。 |
| 2026-10-15 | `THIRD-PARTY-NOTICES.md` | M | 显式记录系统util-linux/flock依赖、实读2.32.1单文件MIT与整个RPM多许可证的边界，不将单组件许可扩展到发行版包。 |
| 2026-10-15 | `docs/development/mysql-install-resume-2026-10-15.md` | A | 记录旧版部分数据误删/受管残留误报、修改前基线、阶段证明与保留隔离、准备期重试审查、完整原生helper与人工交付边界。 |
| 2026-10-15 | `docs/zh-CN/release-3.1.3.4.md` | A | 新候选3.1.3.4/RPM112：安装失败阶段记录、完整实例保留续装、旧残留早阻断、flock依赖与用户自行人工验收边界。 |
| 2026-10-15 | `docs/en-US/release-3.1.3.4.md` | A | 同范围英文候选说明，保留签名ART、三节点FIELD及自定义SELinux目录验收限制。 |
| 2026-10-15 | `docs/development/mysql-initialize-plugin-options-2026-10-15.md` | A | 记录生产配置初始化插件变量失败的旧代码对照、只读现场证据、临时初始化副本与日志脱敏修复、SOURCE/原生隔离/ART/FIELD验证边界。 |
| 2026-10-15 | `docs/zh-CN/release-3.1.3.3.md` | A | 新完整安装候选3.1.3.3/RPM111说明：保留生产运行配置，初始化副本处理插件变量、错误诊断、失败残留保护与用户自行续装边界。 |
| 2026-10-15 | `docs/en-US/release-3.1.3.3.md` | A | 同范围英文初始化修复及新介质说明，不把原生隔离初始化验证当作现场安装完成。 |
| 2026-10-15 | `docs/zh-CN/release-3.1.3.2.md` | A | 最终完整介质3.1.3.2/RPM110说明，正确区分首次安装开放写入与运行期writer lease；保留ART/FIELD限制。 |
| 2026-10-15 | `docs/en-US/release-3.1.3.2.md` | A | 同范围英文最终候选说明；明确初版内部候选停用、保留制品字节。 |
| 2026-10-15 | `docs/development/offline-3.1.3.1-2026-10-15.md` | A | 记录完整介质3.1.3.1/RPM2.2-109任务、用户仅要安装命令参数的范围、旧版对照、门禁计划及SOURCE/ART/FIELD与公开渠道边界。 |
| 2026-10-15 | `docs/zh-CN/offline-installer-parameters.md` | A | 新增当前离线安装器全部命令参数、别名、默认、必填与冲突、安全输入、目录、引擎及执行模式的完整中文参考。 |
| 2026-10-15 | `docs/en-US/offline-installer-parameters.md` | A | 新增与源码对齐的英文离线安装命令参数参考，明确UID0要求、PG介质选择顺序与只读启动授权边界。 |
| 2026-10-15 | `docs/zh-CN/release-3.1.3.1.md` | A | 新增3.1.3.1完整介质候选说明，涵盖生产模板/独立路径、ACK缺失仅续租、命令手册及未签名ART/FIELD限制。  最终复核后改为内部候选停用记录并指路3.1.3.2，修正首次安装与lease时序。 |
| 2026-10-15 | `docs/en-US/release-3.1.3.1.md` | A | 新增同范围英文完整介质候选说明，区分本地包、GitHub源码与未发布Release。  最终复核后改为内部候选停用记录并指路3.1.3.2，修正首次安装与lease时序。 |
| 2026-10-15 | `docs/development/mysql-replica-failure-vip-2026-10-15.md` | A | 记录从库失去半同步ACK导致现役主库VIP续租失败的旧版对照、仅续租边界、负向保护和SOURCE/ART/FIELD验证结果。 |
| 2026-10-15 | `docs/development/backend/operations.md` | M | 明确从库数据库/复制故障保留现役主库VIP的仅续租条件，保留半同步告警、Raft与Agent观测两套多数派及失效隔离边界。 |
| 2026-10-15 | `docs/zh-CN/offline-rpm-install.md` | M | MySQL8.0改用现场生产主从cnf，取消该分支内存调优；记录数据根默认/data、软件默认/usr/local/mysql及独立自定义选项、IP末段拼端口、用户确认的只读保护、原件和目录记录及解析/现场验收边界。  增加独立安装命令参数手册入口。 |
| 2026-10-15 | `docs/en-US/offline-rpm-install.md` | M | 同步英文生产cnf基线、允许变更的路径/身份/只读字段、软件默认/usr/local/mysql、自定义目录与离线参数解析和现场验收边界。  增加独立安装命令参数手册入口。 |
| 2026-10-15 | `docs/development/mysql-production-cnf-paths-2026-10-15.md` | A | 记录生产cnf基线、旧安装链对照、默认/data及独立软件路径、用户确认的身份与只读保护；追加软件默认/usr/local/mysql的修改前对照和回归，区分SOURCE与ART/FIELD边界。 |
| 2026-10-14 | `docs/development/disaster-binlog-availability-2026-10-14.md` | A | 记录3.1.2.15现场缺日志失败、长期纯GTID可用性判断缺口、修改前完整调用链对照、新日志边界/受控克隆/错误保留范围，以及SOURCE、隔离数据库、ART和用户自行安装的FIELD边界。 |
| 2026-10-14 | `docs/development/disaster-binlog-recovery-2026-10-14.md` | A | 登记物理缺binlog现场复现、过期库存计划ready误报、修改前对照、仅替代未执行计划及保留保护、SOURCE/浏览器/ART/恢复分层验证边界；追加3.1.2.15现场重规划通过但首次重建失败、还原原日志才收尾成功，以及后续实际日志可用性修复指路。 |
| 2026-10-14 | `docs/development/backend/recovery.md` | M | 说明恢复计划库存校验、无保护未执行计划superseded、新确认和已开始任务继续复核；增加实际Previous GTID与可读性、新日志边界、受控克隆及失败保护要求，链接原生复现与回归入口。 |
| 2026-10-14 | `docs/development/frontend/operations.md` | M | 区分普通候选不可用与管理员独立灾难恢复预检，记录新计划确认、已保护库存变化及双语/窄屏回归。 |
| 2026-10-14 | `docs/development/console-configuration-layout-2026-10-14.md` | A | 记录双视图布局任务：源码基线与历史对照、用户确认的两种操作范围、CG-UPGRADE-CONTRACT v2已读、浏览器旧版复现、SOURCE/ART/FIELD验证计划与不可变补丁身份。 |
| 2026-10-14 | `docs/development/frontend/settings/cluster-policy.md` | M | 策略入口改为独立视图；说明唯一表单、真实值与来源、缺字段/读取失败未确认、全部引擎清除范围、草稿/会话清理及双视图Chrome回归。 |
| 2026-10-14 | `docs/development/console-configuration-hints-2026-10-14.md` | A | 记录本轮任务：控制台「运行参数」页顶部两条提示的下线与参数级说明。含首轮门禁判断、基线 `754a824`、提示来源、安装器默认配置触发面、接口/启动日志/审批不变量与验证计划；追加独立复核：工作区Playwright路径、英文新增注释失败复现与修正、190项隔离Chrome检查及不等于FIELD的边界，保留首轮未执行浏览器的历史事实。 |
| 2026-10-14 | `docs/development/frontend/settings/configuration.md` | M | 运行参数页顶部不再渲染重新读取说明和弃用提示，弃用/空引用风险移到approval_token_env注释；追加已知注释及文件错误按语言映射、未知文本与路径保留、当前投影浏览器回归及旧告警夹具不能代替后端过滤验收的边界。 |
| 2026-10-13 | `README.md` | M | 首页按用户给定文案重写（用户要求：首页介绍改成「企业级数据库高可用与故障接管平台」这一版）。标题下定位由自造类别名改为 `Enterprise Database High Availability and Failover Management Platform`，其下 `Make database failover safer. Make business recovery more controllable.`；正文承接用户原文：产品定义段 + 「不是简单提升备库，而是安全转移写入服务、避免多节点同时合法可写」的核心目标段、`Why ClusterGuard HA?`（六问 + 「复制解决同步、本产品解决安全接管」）、`What Can ClusterGuard HA Do?`（统一集群管理/安全切换/写入口同批迁移/降脑裂/故障恢复/统一审计 六项，含「无法证明安全即拒绝」）、`The Core Idea Behind ClusterGuard HA`（「故障可以发生，但错误的故障接管不应该成为第二次事故」+ 不是代理/不是备份）、`Where It Fits`（适用场景 + 后续受控扩展方向 + 收尾标语）。**保留** `Console Preview`/`How It Differs from Other HA Tools`（MHA·Orchestrator·Patroni·repmgr 四维对照表）/`How It Works: Architecture and Safety`（三控制器 Raft 部署图 + 十步工作流 + 八条安全机制，含「高可用不等于零中断零丢失」边界条）/支持矩阵/快速开始/文档/许可，维护者内容仍在文末 `Maintainer Notes`。事实声明（`3.1.2.10`/`2.2-108`/`2.1.45`/`2.2.39`/两条 SHA-256/四条 curl/全部相对链接/`AGPL-3.0-only`/`7b643f4`）逐字保留。 **追加（2026-10-13 同日晚些）：** 该英文首页正文现位于 `README.en.md`——本批为「GitHub 首页默认中文」把中文版移到 `README.md`、英文版改名为 `README.en.md`；同时补回上一行改稿漏掉的控制台鉴权模型段落（`MustChangePassword`、8 小时绝对会话、`logout`），以恢复 `scripts/scripts_test.go` 的认证交付文档契约。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |
| 2026-10-13 | `README.zh-CN.md` | M | 与英文版逐节同构改写（20 个标题一一对应、层级顺序一致，代码块 26/26、表格行 17/17、列表项 28/28）：`企业级数据库高可用与故障接管平台`/`为什么需要 ClusterGuard HA？`/`ClusterGuard HA 能做什么？`/`ClusterGuard HA 的核心理念`/`适用场景`/`控制台预览`/`与其他高可用方案的区别`/`架构与安全机制`/`支持矩阵与版本`/`快速开始`/`文档`/`许可`/`维护者须知`。首屏即用户给定原文（含六问、六项能力、核心理念、适用场景与收尾标语）。命令、路径、API 名、版本号、SHA-256 与英文逐字一致；仅 ASCII 部署图按中文重绘以避免多字节等宽错位。维护者内容同样在文末。 **追加：** 本路径改为指向新首页的短跳转页（正文只剩一行说明加语言链接，分别指向 `README.md` 与 `README.en.md`），使旧链接仍可解析；中文正文整体移到 `README.md`，不再双份维护。 |
| 2026-10-13 | `docs/development/markdown-change-record.md` | M | 登记本批根 README 双语改稿的 3 条变更与批次说明（含生成页 `product-overview` 中英两页重建）。 **追加：** 登记「默认中文首页 + 首页语言切换」批次的 `README.en.md`（A）与双语 `licensing.md`（M）三条变更及批次说明。 |
| 2026-10-13 | `README.en.md` | A | 英文 README 由 `README.md` 改名而来（GitHub 只渲染根 `README.md`，故把中文版放 `README.md` 作默认首页、英文版改名 `README.en.md`）。顶部语言切换改为指向 `README.md` 的简体中文入口；`Quick Start` 补回控制台鉴权模型段落（`MustChangePassword=true`、root 专属 `bootstrap-admin-password` 文件或 `bootstrap_admin_password_env`、Argon2id 哈希、8 小时绝对会话、改密或 `logout` 失效），恢复 `scripts/scripts_test.go` 的认证交付文档契约（上一批改稿漏掉 `MustChangePassword`/`eight-hour`/`logout` 使其变红）。  追加2026-10-15：当前完整介质改为3.1.3.2本地候选/RPM2.2-110，链接安装命令参考并明确尚无GitHubRelease、ART/FIELD OPEN。 |
| 2026-10-13 | `docs/zh-CN/licensing.md` | M | 许可声明载体表的根 README 条目由 `README.md` / `README.zh-CN.md` 改为 `README.md` / `README.en.md`，与语言切换后的实际文件名一致。 |
| 2026-10-13 | `docs/en-US/licensing.md` | M | 同中文版：载体表根 README 条目改为 `README.md` / `README.en.md`。 |
| 2026-10-13 | `docs/development/recovery-repair-gate-2026-10-13.md` | A | 记录复制修复缺失动作参数、灾难恢复已复核操作仍被阻断的历史对照、现场版本只读证据、修复边界与源码/现场分层验证计划。 |
| 2026-10-12 | `docs/development/backend/operations.md` | M | 说明新装 MySQL 合法空 GTID 集合、查询证据缺失仍阻断、目标追平与最终核验不省略；登记空集合执行/坏查询/等待失败回归及不确定任务按原始证据复核的边界。 |
| 2026-10-08 | `AGENTS.md` | M | 在「所有修改必须遵守」里新增一条：任何 Markdown 的新增/删除/修改都必须在 `docs/development/markdown-change-record.md` 登记并过 `verify-markdown-change-record.cjs`，改了 md 必须重建 `docs/html`，未登记不得提交。 |
| 2026-10-08 | `docs/README.md` | M | 文档中心导航表新增一行「改文档 → Markdown 变更记录」，让台账在入口页可发现。  增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |
| 2026-10-08 | `docs/development/README.md` | M | 开发入口在「先确定问题发生在哪一层」之后补一句：改动任何 Markdown 都要登记台账并跑对应门禁。 |
| 2026-10-08 | `docs/development/delivery/README.md` | M | 热修命名条从「四段版本 `MAJOR.CAPABILITY.INTERNAL.BUGFIX`」改为「新封板版本线从 `3.1.1.1` 开始、四段为 `MAJOR.MINOR.PATCH.BUGFIX`」，并写明旧 2.x/HF 文件名全部冻结为历史。 当前交付按产品版本目录与版本ID定位，不用旧RPM目录作当前入口。  增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |
| 2026-10-08 | `docs/development/markdown-change-record.md` | A | 本页：建立 md 变更台账，规定增删改都要登记，并由 `tools/verify-markdown-change-record.cjs` 做结构与覆盖校验。 2026-10-09 追加登记「升级预览版本语义修复」批次与 `updates.md` 的展示口径变更。 2026-10-09 追加登记「控制节点卡片」批次与对应批次说明。 |
| 2026-10-08 | `docs/development/rules/change-policy.md` | M | 新增「文档变更必须登记」一节：登记范围、`A`/`M`/`D` 判据、门禁的三种判红情形、`git add` 后门禁才可见、通过后重建生成页。 |
| 2026-10-08 | `docs/development/rules/licensing.md` | M | 许可声明扫描排除整个根 .workbuddy 本机资料；明确业务/文档及嵌套同名目录仍须扫描并拒绝错误声明。 |
| 2026-10-08 | `docs/en-US/hotfix-package-naming.md` | A | 新增英文版包命名规则页；本轮重建为与中文逐节对齐的 7 节结构，补回初版漏掉的「禁止的名字」一节与 `.cgupgrade` 跨语法禁令；迁移示例改为新运行时 ID HF-2026-1008-02，并说明同旧 ID 不同摘要会导致上传冲突。 补充运行产品版本、RPM兼容基线和历史签名字段来源；目录按四段版本，不以RPM来源命名。 同步版本ID、产品目录、验签替代与旧HF兼容规则。 |
| 2026-10-08 | `docs/en-US/update-and-patch.md` | M | 制品说明与热修规格节改用 `clusterguard-3.1.1.1.x86_64.cgpatch` 示例，并点明新封板版本线起点 `3.1.1.1`；与中文版同口径。 |
| 2026-10-08 | `docs/en-US/upgrade-validation-chain-implementation-status.md` | M | 2026-10-08 增补：登记「载荷已落地是单一判据」「回退只命名最新已应用记录」「supersede 守卫必须有非 deployment 的输入源」三处落点，并记下 2 项未决义务。 |
| 2026-10-08 | `docs/en-US/version-release-policy.md` | M | 交付身份条改为新封板版本线起点 `3.1.1.1` + `MAJOR.MINOR.PATCH.BUGFIX`，并写明历史 2.x/HF 规格名称冻结。 |
| 2026-10-08 | `docs/hotfix-patches.md` | M | 导言改用新命名与封板起点；换行按 `scripts/render-hotfix-catalog.cjs` 的实际输出对齐，避免下次渲染产生无意义重排。 渲染器使用实际台账制品路径的解包示例，兼容产品版本目录。 |
| 2026-10-08 | `docs/zh-CN/hotfix-package-naming.md` | A | 新增中文版包命名规则页（本次随命名线迁移首次入库）：7 节结构，规定四段版本 `MAJOR.MINOR.PATCH.BUGFIX` 从 `3.1.1.1` 起、`HF-...` 只留在签名清单、修订递增 Bug 修订段并记 `supersedes_artifact`。本轮把「本次规格示例」改为「新规格示例」，并修正禁止示例的注释措辞；迁移示例改为 HF-2026-1008-02，要求首次迁移分配新 ID、签名 supersedes 关联旧包，避免上传身份冲突。 产品版本绑定运行二进制与API；历史目标读取自身签名版本；旧已交付包冻结并用3.1.1.3修正；当前制品目录改为release/<四段版本>，旧目录仅留历史，台账迁移不改签名字节。 新产品ID等于patch_version；目录按四段版本；旧HF身份保留历史，签名后继替代投影不改旧失败。 |
| 2026-10-08 | `docs/zh-CN/hotfix-patches.md` | M | 同英文台账：导言改用新命名与封板起点。 渲染器使用实际台账制品路径的解包示例，兼容产品版本目录。 |
| 2026-10-08 | `docs/zh-CN/update-and-patch.md` | M | 制品说明与热修规格节改用 `clusterguard-3.1.1.1.x86_64.cgpatch` 示例并点明封板起点；与英文版同口径。 |
| 2026-10-08 | `docs/zh-CN/upgrade-validation-chain-implementation-status.md` | M | 同英文状态页：登记三处落点与 2 项未决义务。 |
| 2026-10-08 | `docs/zh-CN/version-release-policy.md` | M | 交付身份条改为新封板版本线起点 `3.1.1.1` + `MAJOR.MINOR.PATCH.BUGFIX`。 |
| 2026-10-08 | `docs/zh-CN/validation-gate-workflow.md` | M | 去掉已过期的固定17项计数，以本次源码门禁输出为准；新增功能仍须补充实际回归。 |
| 2026-10-08 | `docs/development/backend/settings/version.md` | M | 说明product_version运行来源和旧历史验签补读，RPM兼容字段不变。 |
| 2026-10-09 | `docs/development/frontend/settings/updates.md` | M | 明确当前/目标/历史版本来源及验签替代；新增staging步骤和新操作重置规则。新增操作前产品版本与RPM基线独立展示、旧来源缺失/混合节点和回退未知规则及浏览器回归。补充历史/当前/事件共用双语消息映射、未知诊断随语言提示并保留原文、原始状态不变及桌面/窄屏语言切换回归入口。 「版本显示来源」再补两处展示口径：上传预览身份栅格把**当前运行版本 / RPM兼容基线 / 目标版本**三件独立事实分开，运行版本与版本摘要同源、不随上传改变；签名校验按包类型分派，热修补丁不带 RPM 升级引导器时显示`已通过 · 热修补丁`而非`已通过 · 历史兼容包`，滚动升级保留引导器/历史兼容两种描述，包类型文案写明热修会更新产品版本并保留 RPM 安装记录。 |
| 2026-10-09 | `docs/development/backend/settings/update-history.md` | M | 记录Snapshot验签后继及部署判据。新增staging与updating共用节点百分比的读取投影、原始状态保留和三节点持久化回归入口。 |

| 2026-10-09 | `docs/development/frontend/settings/preferences.md` | M | 语言设置接入静态原文绑定、动态双语目录和完整重绘；明确业务数据/原始证据、内存偏好及无请求/表单保持边界，登记四引擎八页面真实浏览器与目录回归。 |
| 2026-10-09 | `docs/development/frontend/settings/configuration.md` | M | 记录参数分组默认折叠、点击/键盘展开、刷新与双语保留、注销清空、局部表格滚动和专用真实浏览器验收；增加3.1.2.1页面编辑、选定控制节点、二次确认、逐节点结果与失效拦截，记录隔离浏览器范围。 2026-10-09 追加登记「控制节点卡片」批次：目标选择由裸 UUID 改为名称/不可变 ID/可信 API 地址三件独立事实的卡片，只在事实成立时标「本机」「Leader」，不显示控制面没有数据源的在线/离线，复选框固定在卡片作用域内 16 像素，卡片是 label 且点击任意处切换自身复选框。 2026-10-09 追加登记「卡片角色标记与端点链接」批次：每卡改为恰好一个角色标记（本机>Leader>投票节点，配色只沿用既有变量），复选框移到卡片右侧固定 18 像素，地址行带链接图标且 `http(s)://` 可信端点渲染为新页链接，裸地址仍是纯文本；浏览器验收与 INV-004/23 反查及变异同步更新。 增补参考图布局：实际所选数、蓝色选中卡片、固定底栏/通知避让、文件元信息与分组总数、已知提示双语及隔离浏览器回归。 追加白名单分类（可编辑在上、专用流程在下）、末列修改值、异步能力确认与独立折叠/策略挂载，记录Chrome回归。 |

| 2026-10-09 | `docs/development/backend/settings/configuration.md` | M | 3.1.2.1新增25项整数编辑、指定控制节点、预检绑定、Raft任务、固定Helper滚动重启、实际值回执与失败重试回退；区分生产验收。 |

| 2026-10-09 | `docs/development/frontend/settings/README.md` | M | 参数入口指向编辑、选定控制节点下发与实际重启核验，移除只读入口误导。 |
| 2026-10-09 | `docs/development/backend/settings/README.md` | M | 参数导航加入plan/dispatch/permit、Raft配置任务和逐节点重启核验路径。 |

| 2026-10-09 | `docs/development/delivery/hotfix-catalog-validation.md` | A | 显式私有台账/声明/目录输入，同判据与失败回归；Chrome依赖复跑和fixture/现场证据边界。 |

| 2026-10-09 | `README.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `README.zh-CN.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/zh-CN/README.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |

| 2026-10-09 | `docs/en-US/README.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |

| 2026-10-09 | `docs/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |

| 2026-10-09 | `docs/zh-CN/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |

| 2026-10-09 | `docs/en-US/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。  追加2026-10-15：当前完整介质指向3.1.3.2本地候选与安装命令参考，删除不存在的GitHubRelease链接，明确ART/FIELD OPEN。 |

| 2026-10-09 | `docs/development/delivery/offline-installation-media.md` | A | 完整介质子功能路径、产品身份传递、实际版本回归、干净构建与唯一目录、显式公开附件及分阶段验收。 |

| 2026-10-09 | `docs/zh-CN/release-3.1.2.8.md` | A | 3.1.2.8双语发布说明：完整包产品版本注入、累计修复/下发功能、下载摘要入口、介质内容及现场未验收边界。 |

| 2026-10-09 | `docs/en-US/release-3.1.2.8.md` | A | 3.1.2.8双语发布说明：完整包产品版本注入、累计修复/下发功能、下载摘要入口、介质内容及现场未验收边界。 |
| 2026-10-12 | `docs/zh-CN/release-3.1.2.9.md` | A | 3.1.2.9双语发布说明：安装器两处修复（双引号远端载荷内的反引号在运维机本地执行、`Type=simple` 单元 `enable --now` 后就绪断言竞态）；产品版本 3.1.2.9 对 RPM 兼容基线 2.2-107；下载入口与 FIELD OPEN 边界。 标注历史附件内含 MySQL minimal 包且保留；3.1.2.10 起默认不嵌入数据库介质。 |
| 2026-10-12 | `docs/en-US/release-3.1.2.9.md` | A | 与中文逐节对齐的 3.1.2.9 英文发布说明：同两处安装器修复、同一产品版本与 RPM 基线、同一下载入口与 FIELD OPEN 边界。 同步历史 minimal 数据库附件保留及3.1.2.10默认不嵌入的英文说明。 |
| 2026-10-12 | `docs/zh-CN/release-3.1.2.10.md` | A | 3.1.2.10 双语发布说明：完整介质默认不再嵌入数据库包（`packages/database/` 为空，数据库介质由现场自备并经 `-r` 或 `/opt` 兜底）；产品版本 3.1.2.10 对 RPM 兼容基线 2.2-108；下载入口与 FIELD OPEN 边界。 |
| 2026-10-12 | `docs/en-US/release-3.1.2.10.md` | A | 与中文逐节对齐的 3.1.2.10 英文发布说明：同「默认不嵌入数据库介质」口径、同一产品版本与 RPM 基线、同一下载入口与 FIELD OPEN 边界。 |
| 2026-10-12 | `README.md` | M | 当前完整安装介质入口由 3.1.2.8 起经 3.1.2.9 更新到 3.1.2.10；RPM 兼容基线 2.2-106→2.2-107→2.2-108；本版声明介质不再嵌入数据库包；发布说明链接同步。 |
| 2026-10-12 | `README.zh-CN.md` | M | 同上中文入口：介质版本经 3.1.2.9 更新到 3.1.2.10、RPM 基线到 2.2-108、声明不再嵌入数据库包、发布说明链接同步。 |
| 2026-10-12 | `docs/README.md` | M | 文档中心导航表「当前完整安装介质」由 3.1.2.9 更新到 3.1.2.10 发布说明。 |
| 2026-10-12 | `docs/zh-CN/README.md` | M | 中文入口表「当前完整安装介质」由 3.1.2.9 更新到 3.1.2.10 发布说明。 |
| 2026-10-12 | `docs/en-US/README.md` | M | 英文入口表「当前完整安装介质」由 3.1.2.9 更新到 3.1.2.10 发布说明。 |
| 2026-10-12 | `docs/catalogue.md` | M | 「当前完整介质」段由 3.1.2.9 更新为 3.1.2.10、RPM 基线 2.2-108，历史条目保持原范围。 |
| 2026-10-12 | `docs/zh-CN/catalogue.md` | M | 同上中文完整资料目录的当前介质段更新到 3.1.2.10。 |
| 2026-10-12 | `docs/en-US/catalogue.md` | M | 同上英文完整资料目录的当前介质段更新到 3.1.2.10。 |
| 2026-10-12 | `docs/development/delivery/offline-installation-media.md` | M | 身份示例由产品 3.1.2.9 对 RPM 2.2-107 更新为 3.1.2.10 对 2.2-108；新增「数据库介质：默认不嵌入」一节（介质不携带数据库包、安装器三级查找优先级、缺 `.sha256` 侧车静默跳过校验）；构建示例去掉 `--database-package`、校验示例期望数据库包数 2→0；保留随介质安装器的契约条目。  追加2026-10-15：示例版本3.1.3.2/RPM110，记录中英文命令参考映射和制品字节校验。 |
| 2026-10-12 | `docs/development/markdown-change-record.md` | M | 登记 3.1.2.9 与 3.1.2.10 两个完整介质发布批次（本页）。 |

| 2026-10-12 | `docs/zh-CN/offline-rpm-install.md` | M | 新增中断安装重试 FAQ：精确识别本端口受管单元、停用并确认无主进程后再替换；明确完整数据保护与失败停用状态。 |
| 2026-10-12 | `docs/en-US/offline-rpm-install.md` | M | 同步英文中断安装重试 FAQ、受管单元识别与停止失败阻断、完整数据保护和生产恢复边界。 |

## 批次说明

本轮配置下发审查：前后端configuration页补全23项集群级/2项节点级范围、schema 2、显式且任务绑定的超时、拒绝覆盖恢复及首次现场证据；门禁流程/交付入口链接私有工具页；命名页中英明确功能版本规则的设立日期。完整HTML重建。

升级预览版本语义修复：上传预览不再只有「RPM兼容基线」，新增独立的「当前运行版本」，并停止用 RPM 升级引导器给热修补丁判「历史兼容包」。源码改动在 `internal/api/console.html`（非 md，不在本页登记范围）；本批 md 只改了 `docs/development/frontend/settings/updates.md` 与本页。这两份 md 都不在 `docs/build-html-docs.mjs` 的生成清单里，因此本批**不重建** `docs/html`。

本次新增功能按用户纠正使用3.1.2.1；同步中英命名页的PATCH递增/BUGFIX重置及revision=0边界，更新版本规范的产品身份和唯一主线口径。

### 2026-10-09 · 热修传输阶段进度

修复staging缺少进度映射，补充上述前后端功能说明；登记仍按73ff4a3以来的路径净变更，重建docs/html生成页。原始任务和制品证据仅存本地。

同日补充升级结果中文显示：统一已知Runner消息的历史/当前/事件显示，未知诊断保留可展开原文，语言切换重绘；只更新上述前端功能页并再次重建生成页。

### 2026-10-08 · 热修版本身份线迁移与统一校验链收口（基线 `73ff4a3`）

两条线合并成一批登记，因为它们落在同一组文件上，只按路径记一次净变更：

1. **热修版本身份线迁移。** `8474f4e` 首次定义热修包命名，用四段 `MAJOR.CAPABILITY.INTERNAL.BUGFIX`（示例 `2.2.105.1`）并把前两段约束到 `rpm_version`、第三段约束到 `rpm_release`；`4bd5eb1` 把整套口径改成新的封板版本线 `MAJOR.MINOR.PATCH.BUGFIX`（起点 `3.1.1.1`），四段就此成为**交付版本身份**、不再要求与源 RPM 的 `rpm_version`、`rpm_release` 逐段相等，构建器与台账门禁里那条逐段一致性强校验同时被移除。受影响的 9 份 md 见上表。
2. **统一校验链收口。** `4d93052` 修掉三处现场缺陷（supersede 守卫只读 `deployment.json` 因而从不运行、载荷判据只认原始 `status`、控制台操作位钉在已生效且已被取代的记录上），改动落在两份实现状态页的 2026-10-08 增补里。
3. **本轮文档整理（2026-10-08）。** 把 `hotfix-package-naming.md` 中英两版拉成逐节对齐的 7 节结构（行号与节号完全一致，各 116 行），补回英文版缺失的「禁止的名字」一节与 `.cgupgrade` 跨语法禁令；把 `update-and-patch.md` 中英两版的制品说明统一到「必须填 `patch_version`、封板版本线从 `3.1.1.1` 开始」；把 `docs/hotfix-patches.md` 的换行对齐到渲染器的实际输出。
4. **生成页重建。** `docs/html/` 是入库产物，`docs/build-html-docs.mjs` 重新生成 98 页，其中 19 页内容变化：6 页来自本批命名文案（`hotfix-patches` / `update-and-patch` / `version-release-policy` 的中英版），其余来自更早的 md 改动此前一直没重建（`index`、`api-operations`、`operations-manual`、`product-overview`、`upgrade-validation-chain`、`update-operation-identity-incident-2026-09-30`、`release-recovery-acceptance-checklist`）。生成产物不逐页登记。
5. **规则落点。** 「改 md 必须登记」写进四处：`AGENTS.md`（全仓强制条款）、`docs/development/rules/change-policy.md`（新增「文档变更必须登记」一节，含判红情形与门禁可见性）、`docs/development/README.md` 与 `docs/README.md`（入口可发现）。这些文件本身也在上表登记。
6. **命名规则已被真实实例化。** 同一批改动还产出了第一份按新规则命名的真实规格与产物（在本地 `release/2.2-105-hotfixes/`，该目录不入 git）：规格里 `patch_version` 为 `3.1.1.1`、`rpm_version` 为 `2.2`、`rpm_release` 为 `105`，与两份 `hotfix-package-naming.md`（`docs/zh-CN/` 与 `docs/en-US/`）的示例逐字段一致；原来按旧 `MAJOR.CAPABILITY.INTERNAL.BUGFIX` 命名的产物被改名移入 `obsolete-20261008-3.1-renaming/` 并留下 `REASON.md`。本轮没有改动这些私有文件，也没有把它们写进本页。
7. **未纳入本页的既知未跟踪 md（已于 2026-10-08 21:30 归档）。** `docs/install-zh.md`（引用了已不存在的 `codex/phase1-control-kernel`、`codex/platform-auth-session` 分支的旧草稿）与 `.workbuddy/memory/*.md`（本机工作记忆）都不加入索引、不由本页登记。这两类文件现已移出原位置：前者到 `.workbuddy/archive/docs-drafts/`，后者中超过当日的日志到 `.workbuddy/archive/memory/`（索引见该目录 `README.md`）；`.workbuddy/` 同时进入 `.gitignore`，所以门禁的「未跟踪 md」提醒列表现在是空的。归档是移动不是删除，字节未改。

8. **迁移身份验证（2026-10-08）。** 对真实上传路径验证后，纠正中英命名示例为新的 `HF-2026-1008-02`；首次迁移分配新内部包 ID，保留版本文件名 `clusterguard-3.1.1.1.x86_64.cgpatch`。同 ID 不同摘要的中间包归档，签名字节不改写。私有制品验收和交付说明留在 `release/`。命名页当前不在 HTML 生成器页清单中，仍执行完整重建检查。

9. **本机参考与门禁修复（2026-10-08）。** 本机记忆逐章节原文拆分到忽略的 `.workbuddy/reference/` 并保留完整快照和摘要对拍；入库规则说明根本机目录的扫描边界。许可 CLI 回归覆盖本机归档不误报、真实源码/文档/嵌套同名目录错误声明仍拒绝。热修修订链支持版本文件名，继续验证递增修订及双向替代。流程不再固定旧检查数量；执行完整 HTML 重建。私有新补丁用于交付尚未入包的主线修复，制品及现场验收分开记录。

本次目录规则调整：当前3.1.1.3入口按产品版本定位，renderer同步新旧目录识别与台账解包路径示例；HTML重建。

控制节点卡片：目标选择不再是一行裸 UUID 加被全局 `input` 规则拉大的复选框，而是每个投票节点一张卡片——复选框、节点名称、不可变 ID、该投票节点的可信 API 地址。名称来自节点清单，地址优先取 Leader 记录的端点，两者都不猜测。卡片不写「在线/离线」：控制面没有逐投票节点存活探测，`controller_members` 与 `members` 都只带身份与端点，要显示就得先补后端接口。源码改动在 `internal/api/console.html`（非 md，不在本页登记范围）；本批 md 只改了 `docs/development/frontend/settings/configuration.md` 与本页。这份 md 不在 `docs/build-html-docs.mjs` 的生成清单里，因此本批**不重建** `docs/html`。

卡片角色标记与端点链接（2026-10-09 第二批）：按现场截图把每卡的角色标记收敛为恰好一个（本机>Leader>投票节点），配色只沿用既有变量不新增色相；复选框移到右侧固定 18 像素；地址行加链接图标，Leader 记录的端点自带 `apiScheme://`（`internal/runtime/runtime.go`），带协议就渲染为新页链接、裸地址仍纯文本。本批 md 同样只改 `configuration.md` 与本页，均不在 `docs/build-html-docs.mjs` 清单内（`grep -c development` 实测 0），**不重建** `docs/html`。

| 2026-10-09 | `docs/development/backend/settings/update-history.md` | M | 操作执行前产品身份按节点观测、同operation_id持久化；RPM基线独立，缺失历史不猜测，补充实跑回归入口。 |

2026-10-09参考图参数页改稿：仅更新配置功能页和本台账，重建HTML文档；SOURCE与隔离浏览器独立记录，FIELD不冒充。

本批参数分类修改只涉及上述设置功能页与本台账，生成页照流程重建；SOURCE、隔离Chrome与ART/FIELD分别记录。

3.1.2.8完整介质发布批次：统一运行产品身份和RPM兼容基线，补当前公开下载路径及双语发布说明；完整重建HTML，保留历史记录。后续修正：把「注入全部五个 Go 目标」做成真实可观察——五个负载命令（clusterguard/cgctl/clusterguard-agent/clusterguard-k8s-fence-guard/clusterguard-update-helper）都报告共享四段身份，契约 ART-005 逐个命令判红（+2 变异），发布说明与介质指南同步登记；本页与两份发布说明、介质指南仍在同一批次内，不新增台账行。

3.1.2.9完整介质发布批次（2026-10-12）：修复随介质出货安装器 `scripts/install_clusterguard.sh` 的两处现场缺陷——配置滚动升级 Helper 的远端载荷是双引号字符串，其中注释里的反引号被运维机本地在执行时当命令替换（打印 `workspace: 未找到命令` 并清空该注释行）；`Type=simple` 的 `clusterguard-update-helper.service` 在 `enable --now` 返回后立即断言 `is-active` 与 socket，未等进程绑定造成就绪竞态、整步 `exit=1`。源码修复与契约同批：`verify-upgrade-validation-chain.cjs` 新增 1 条检查（安装器不得含反引号；`enable --now` 后的就绪断言前必须有有界重试）+ 2 条变异，检查数 32→33、变异 58→60。补双语发布说明与当前下载/介质入口；目录按产品版本 `3.1.2.9` 定位，RPM 兼容基线 `2.2-106`→`2.2-107`（RPM 内装入当前版本发布说明，内容变化故递增）。完整重建 HTML。SOURCE 与 ART 以本次记录为准；FIELD（客户生产现场验收）仍 OPEN，实验室三节点全新安装的验证结果另记于交付台账。

3.1.2.10完整介质发布批次（2026-10-12）：确立并落地「数据库介质默认不嵌入」口径——完整介质不再携带 MySQL/PostgreSQL 二进制。构建脚本 `build-clusterguard-offline-kit.sh` 的 `database_packages=()` 空起步、全脚本唯一自动发现是 `--dependencies`，只有显式 `--database-package FILE` 才嵌入，`THIRD-PARTY-NOTICES.md` 也把数据库包写成 "(when embedded by `--database-package`)"；此前 3.1.2.9 公开介质显式嵌入了 mysql-8.0.44-minimal 与 postgresql-16.4 两个包，其再分发义务由发布方承担，属本版显式嵌入的例外，已记录。`docs/development/delivery/offline-installation-media.md` 新增「数据库介质：默认不嵌入」一节（安装器三级查找优先级、`/opt` 兜底位、缺 `.sha256` 侧车会静默跳过校验），构建示例去掉 `--database-package`、`verify-offline-kit.cjs` 期望数据库包数 2→0；每次发布独立定稿的随介质发布说明由 3.1.2.9 递增为 3.1.2.10，RPM 兼容基线 `2.2-107`→`2.2-108`。同步构建脚本 `scripts/build-clusterguard-offline-kit.sh` 生成的 `packages/database/README.txt` 措辞（脚本非 md，不在本页登记范围；改写使 DB-less 时自述不再声称已嵌入包，故本版介质可安全重建，不撞「同一产品版本只构建一次」）。目录按产品版本 `3.1.2.10` 定位。完整重建 HTML。SOURCE 与 ART 以本次记录为准；FIELD（客户生产现场验收）仍 OPEN，实验室三节点用本版介质配现场自备完整包的重新安装验证结果另记于交付台账。

### 2026-10-12 · 中断安装的旧 MySQL 单元处理

双语安装手册补充本端口受管服务停用/验证与数据保护边界；按用户决定给3.1.2.9历史附件加minimal数据库说明，保留原制品字节。重建全部生成HTML。源码回归在scripts/mysql_reinstall_test.go，现场未在本任务重新执行。

### 2026-10-13 · 根 README 双语「产品作用」改稿

问题：根 README 的首屏是仓库门禁与源码修复基线（提交号、ART/FIELD 状态），读者落页后读不到这个软件做什么；能力清单是 20+ 条平铺实现细节，下载与校验和章节压在产品描述之前，标题下还遗留两行重复的中英标语与错位的 `Go 1.22` 要求。

改动：把「它解决什么问题 / 它做什么 / 引擎与运行形态支持 / 部署形态 / 设计取舍与边界」提到首屏；下游章节（安装介质、正式发布、从源码快速开始、注册与发现、CLI、文档、许可）顺序调整但**不改**任何数字、摘要、命令与链接；开发门禁段压缩为一行指引，与 `7b643f4` 源码修复基线一起集中到文末 `Maintainer Notes`。

未改变的事实声明：当前完整介质 `3.1.2.10` / RPM 兼容基线 `2.2-108` / 预发布且 FIELD OPEN；许可仍是 `AGPL-3.0-only`；`v2.1.45` 与 `v2.2.39` 的支持边界、SHA-256 值、`2.1-45` 与 `2.2-39` 下载命令逐字保留；MySQL 9.x 只有单元测试、无现场验收记录这一限定也保留。

结构化自检（本地只读脚本，不入库）：双语章节层级与顺序一致（各 17 节）、代码块数 26/26、表格行 23/23、列表项 28/28，相对链接全部可解析，`AGPL-3.0-only` 两侧均出现。

截图：控制台预览由 1 张改为 2 张（新增 `docs/assets/screenshots/cluster-topology.png`，复用产品导览已入库的同一文件，不新增图片资产），两处语言切换页面共用同一张图，避免双语展示冲突。

生成页：`docs/html/en-US/product-overview.html` 与 `docs/html/zh-CN/product-overview.html` 随本批重建，不逐页登记。

### 2026-10-13 · 默认中文首页与首页语言切换

GitHub 只渲染仓库根 `README.md`，所以「首页默认中文」只能由 `README.md` 承载中文正文来实现。做法：中文版移到 `README.md`（默认），英文版改名 `README.en.md`，两页顶部各留一条 `<!-- LANGUAGE-SWITCH -->`（生成 HTML 时会整段剥离，不影响离线文档），正文互为语言入口。旧路径 `README.zh-CN.md` **保留为短跳转页**（指向 `README.md`），既不丢旧链接也不删除路径，因此台账中该路径的既有条目仍然成立，无需回改历史行。

同时修一处回归：上一批 README 改稿把控制台鉴权模型段落（`MustChangePassword`、八小时绝对会话、改密或 `logout` 失效、root 专属 `bootstrap-admin-password`）删掉，`scripts/scripts_test.go` 的 `TestAuthenticationDeliveryDocumentationCoversBootstrapAndRecovery`（原先读 `../README.md`）因此变红；本批按原文事实把该段落补回中英两版，并把该契约测试的读取路径改为 `../README.en.md`——交付物里落盘为 `README.md` 的那一份仍然是英文版。

耦合点同步（非 md，不逐条登记）：`docs/build-html-docs.mjs` 的 en-US `product-overview` 源改指 `README.en.md`、zh-CN 源改指 `README.md`；`tools/verify-license-consistency.cjs` 的声明载体清单把 `README.zh-CN.md` 换成 `README.en.md`；`scripts/build-clusterguard-bundle.sh` 与 `scripts/build-clusterguard-rpm.sh` 复制/安装的 README 源改指 `README.en.md`，使交付物中的 `README.md` 仍为英文版，与既有交付行为一致。生成页 `docs/html/{en-US,zh-CN}/product-overview.html` 与两页 `licensing.html` 随本批重建，不逐页登记。

## 2026-10-14：运行参数整页双视图

修改 configuration.md 的节点底栏与策略分类说明，追加双视图、末列输入、真实值/来源、缺值未确认及隔离Chrome验收入口；已有 M 记录继续有效。新增任务页及修改策略页在本批登记。生成页使用 docs/build-html-docs.mjs 重建；本地 release 与 .build 中的证据不入公共仓库。

生产cnf与离线路径批次（2026-10-15）：原件逐字节入库、只改路径和已确认的身份/只读保护；数据根默认/data，软件默认/usr/local/mysql且独立可配，默认ID为IP末段拼端口。源码门禁新增保真判据和4个失败变异。登记任务记录及双语安装手册并重建104个HTML页面，生成页实际变更为中英文离线安装手册。未构建新介质、未安装现场。

从库故障VIP续租批次（2026-10-15）：登记专项任务及后端操作模块说明，明确ACK缺失只允许现役稳定Owner续租，保留降级、授权及观测保护。对应SOURCE门禁新增8个失败变异；开发模块文档由源文件链接访问，按要求运行104页生成器，不新增公开交付或现场验收声明。

完整介质3.1.3.1批次（2026-10-15）：安装命令详情独立中英文手册，入口页只指路；介质显式携带并校验原文字节，生成器增加对应两页及版本说明导航。当前介质入口统一为3.1.3.1本地候选/RPM2.2-109，删除尚不存在的GitHub Release入口；SOURCE、结构校验、未签名ART及FIELD状态分开记录。

3.1.3.1内部候选停用：最终复核发现新写随包说明混淆安装引导与运行期writer lease，原件保留并停用，不覆盖重建。对应双语发布页改为停用记录，最终新增3.1.3.2/RPM2.2-110说明；上层当前入口、命令参考与构建示例随之更新，HTML重新生成。任务记录继续使用初始任务文件名追踪两次候选。

MySQL初始化插件选项批次（2026-10-15）：修复生产模板正常运行选项误用于原生初始化，独立临时副本保留持久化布局与生产参数、增加失败原生日志及精确秘密脱敏；双语安装FAQ增加故障定位与残留保护，参数参考及当前介质入口更新到3.1.3.3/RPM2.2-111。两份原生产cnf字节不改，既有制品不重建，ART/FIELD保持实际边界。HTML重新生成。

### 2026-10-15 · MySQL失败续装与人工交付验收

安装手册中英§12.8改为证据充分后保留隔离，§12.10新增原子阶段记录、明确失败/空准备的续装准入、旧格式人工确认、实例/子挂载保护、稳定锁与三节点人工交付清单。当前完整介质入口及构建示例统一指向3.1.3.4/RPM112本地候选，旧版本字节与历史证据保留。THIRD-PARTY-NOTICES补系统util-linux/flock实际源码许可来源，未新增vendored程序。新增任务与中英候选说明登记于上表，并追加真实原生helper、c088653干净构建、33,845,130字节及0600b1d0摘要/15项结构/3处载荷比对回执；HTML全部重建。签名ART和真实三节点FIELD不因本次源码、结构或原生隔离测试关闭。

### 2026-10-15 · 更新Helper缺失运行目录的226

新增本轮任务和中英3.1.3.5/RPM113说明；当前完整介质导航与构建示例升至3.1.3.5。中英安装手册§12.11说明systemd239启动顺序、仅易失目录缺失容忍与持久路径强制，强调已完成MySQL/registered站点保留数据和原state/secrets/work-dir。3.1.3.4说明补已知故障和新版本指路，保留旧制品与历史回执；HTML全量重建，ART/FIELD未完成不冒充通过。
