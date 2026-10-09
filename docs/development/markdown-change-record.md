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
| 2026-10-08 | `AGENTS.md` | M | 在「所有修改必须遵守」里新增一条：任何 Markdown 的新增/删除/修改都必须在 `docs/development/markdown-change-record.md` 登记并过 `verify-markdown-change-record.cjs`，改了 md 必须重建 `docs/html`，未登记不得提交。 |
| 2026-10-08 | `docs/README.md` | M | 文档中心导航表新增一行「改文档 → Markdown 变更记录」，让台账在入口页可发现。  增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |
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

| 2026-10-09 | `docs/zh-CN/README.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/en-US/README.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/zh-CN/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/en-US/catalogue.md` | M | 增加3.1.2.8完整安装介质入口，区分产品版本与RPM基线、预发布和FIELD未执行边界。 |

| 2026-10-09 | `docs/development/delivery/offline-installation-media.md` | A | 完整介质子功能路径、产品身份传递、实际版本回归、干净构建与唯一目录、显式公开附件及分阶段验收。 |

| 2026-10-09 | `docs/zh-CN/release-3.1.2.8.md` | A | 3.1.2.8双语发布说明：完整包产品版本注入、累计修复/下发功能、下载摘要入口、介质内容及现场未验收边界。 |

| 2026-10-09 | `docs/en-US/release-3.1.2.8.md` | A | 3.1.2.8双语发布说明：完整包产品版本注入、累计修复/下发功能、下载摘要入口、介质内容及现场未验收边界。 |

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

3.1.2.8完整介质发布批次：统一运行产品身份和RPM兼容基线，补当前公开下载路径及双语发布说明；完整重建HTML，保留历史记录。
