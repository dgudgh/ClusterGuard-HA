# 前端 UI/UX 升级：代表页面评审

## 范围与阶段

- 本轮：现状审查、统一设计规则、3 个代表页面实现与本地评审；不全站铺开、不发布、不连接现场执行操作。
- 方向：沿用用户提供的 ClusterGuard HTML 中的黑白极简、左右工作区、细线标签、低装饰信息层级；保留品牌 CG 标记、全站导航、状态语义和业务边界。
- 入口：通过 `?ui=review` 启用代表页设计，默认界面保持原样；沿用实际 API 调用与操作处理函数。隔离测试服务不连接现场。
- [x] 确认目录、分支、现有未提交改动、旧版行为。
- [x] 启动隔离预览，完成 8 路由桌面和手机现状截图。
- [x] 代表页：主库切换、运行总览、操作日志。
- [x] 桌面、平板、手机及异常状态回归；相同尺寸前后截图。
- [x] 形成并打开视觉评审材料。全站推广等待用户确认，不在本轮默认执行。

## 修改前基线与不变量

本记录写于代表页面源码修改之前。

- 目录：`/Users/zhaolongjie/codex/clusterguard-ha/.worktrees/platform-auth-session`。
- 分支：`codex/2.2-postgresql`；HEAD：`19d02bcf076b6e294af0e1f30e2c34f0727e6ae4`。
- 历史基线：`bc0546a`（2.2-68）；相关后续提交：`c8795a3`（恢复与 2.2-99）。历史基线仅用于代码行为对照，不代表旧版本缺陷应恢复。
- 当前 `console.html` 已有 215 行工作树差异，含未发布的生命周期与安全修复。此次先冻结为 `.build/ui-ux-review-20260910/before/console.html`，前后截图比较的是本轮修改前后，不冒充旧 RPM 运行截图。
- 当前现场版本本轮没有重新读取，不推断已升级。
- 共享工作树中另有 `loadConsoleAfterAuthentication` 登录后数据读取失败保护，不属于此次视觉实现；保留并纳入 bootstrap 回归，不覆盖或归入此次 UI 改造成果。

| 调用链 | 历史实现 | 当前实现及必须保留内容 |
| --- | --- | --- |
| 顶部集群 → 数据读取 → 操作 | `renderOperationContext` 从拓扑获取主库/候选 | 保留当前集群、角色、候选真实 API 数据；不能硬编码截图里的节点 |
| 操作锁 → 按钮 → 提交 | `updateExecutionButtons` 和 `executeOperation` 检查解锁 | 保留 `ordinaryOperationAllowed`、操作 intent、会话/集群/目标绑定、异步后重验与重复提交保护 |
| 执行 → 进度 → 结果 | 后端阶段和 `verification.passed` 决定成功 | 保留真实阶段，检查状态不得用参考文件里的定时器模拟；预检查仍在现有后端工作流内执行 |
| 日志 → 筛选 → 更多 → 原始详情 | 历史版全量读后前端 slice | 保留现有服务端分页、集群默认联动、搜索全历史、展开才加载详情、旧请求隔离 |
| 刷新/退出/换集群 → 授权 | 旧版保护不完整 | 不恢复旧漏洞；沿用当前入口即撤权和迟到结果拒绝逻辑 |

## 框架与现状

- Go `//go:embed console.html` 内嵌单页；原生 DOM/JavaScript，无 React/Vue、无外部组件库。
- 8 个 hash 路由；三个子标签组；原生 `dialog` 和现有 ARIA tab 键盘逻辑。
- 主题变量与响应式规则集中于 `internal/api/console.html`。只有浅色主题；不虚构深色支持。
- 原图标多数为文字符号，筛选栏有内联搜索图标；本轮不引入大体积图标库。
- 内容来自 `/api/v1/`。本地参考数据与现场执行必须分开，预览不代理写请求。

## 路由清单

| 路由 | 类型 | 本轮 |
| --- | --- | --- |
| `#overview` | 总览 / 集群列表 / 筛选 | 代表页面 |
| `#operations` | 主库切换工作台 / 关键操作；旧主恢复、灾难恢复 | 主库切换代表页，另两标签只作兼容验证 |
| `#operation-log` | 审计列表 / 搜索 / 分页 / 展开详情 | 代表页面 |
| `#topology` | 拓扑详情 / 启停 / 身份表 | 审查与回归，待后续确认 |
| `#nodes` | 清单 / 新增及恢复表单 | 审查与回归，待后续确认 |
| `#metrics` | 指标 / 长表格 | 审查与回归，待后续确认 |
| `#about` | 阅读 / 能力说明 | 审查与回归，待后续确认 |
| `#settings` | 状态 / 升级 / 账户表单 | 审查与回归，待后续确认 |

## 审查结论（源码与浏览器截图）

1. `operation-context` 将集群、主库、候选、运行边界、入口和延迟压在同一条摘要栏，关键地址截断，妨碍切换前核对。
2. 操作执行区纵向空白较大，候选与执行按钮分离；缺少邻近按钮的禁用原因。
3. `log-card` 的头部和字段网格重复操作类型、状态，扫描成本高；长名称须换行而不是进一步缩小字。
4. 多层 panel 边框、粗字重与较弱的辅助文字竞争，页面各级标题差别不充分。
5. 小屏需同时保留顶部集群、八项导航及操作锁，不能通过隐藏业务控制换取截图整齐。

值得保留：固定全站上下文、CG 品牌、紧凑搜索栏、语义状态色、原生 dialog、键盘可切换标签、服务端分页、真实后端进度与所有安全锁。

8 个主路由在基线的 1440、1024、768、390px 视口没有整页横向溢出；本轮不把审美调整描述成原页面全部不可用。

| 页面 | 观察到的问题与影响 | 处理 |
| --- | --- | --- |
| 主库切换 | 摘要六项挤在一行，入口与运行边界省略；候选与执行按钮相隔较远 | 左侧集中主库、目标、入口；右侧邻近执行按钮展示实际条件和禁用原因 |
| 总览 | 摘要带、外框和列表头边框重复；手机原规则隐藏第 4 列起的信息 | 平铺摘要与列表，手机保留健康、主库、实例数及观测时间 |
| 操作日志 | 记录头部和字段网格重复显示类型、普通状态；手机记录过长 | 连续记录布局，保留复核信息及重试次数；不改变分页和延迟加载 |
| 拓扑 | 节点内部将多组身份、运行信息挤成短行，主从关系仍有可读性 | 保留角色颜色与拓扑结构；节点内容层级列入下一批 |
| 节点 | 清单、详情、空任务区有多重边框和空白；缺失主机字段显示 `-` | 不编造主机信息；下一批处理清单和任务区 |
| 指标 | 指标摘要、宽表格密度差异明显，专业单位不统一 | 保留各引擎指标；已验证小屏表格在容器内滚动，后续优化列层级 |
| 关于 | 两列顶部因相邻 panel 的间距规则错位，长段落密度高 | 记录待修，不用代表页改造覆盖整个阅读页 |
| 设置 | 状态、升级、账户信息视觉密度接近，重要状态层级不足 | 保留子标签及上传/校验/确认状态机；下一批单独设计 |

## 设计规则

- 宽度：内容最大 1440px；桌面导航 208px，正文边距 32px；平板 24px、手机 16px。
- 栅格：切换工作区左侧约 2/3 展示迁移关系，右侧 1/3 展示真实上下文/安全状态与动作；手机上下堆叠。
- 密度：总览数字为平铺摘要带；日志为连续记录列表；不将所有页面改成相同卡片布局。
- 字体：系统字体优先，中文 PingFang SC / Microsoft YaHei；正文 14px/1.6、辅助 12px/1.5、页标题 24px、区标题 16px、关键节点 22px。字距为 0，禁止按 viewport 缩放字体。
- 颜色：白色背景、#181a1d 主文字、#62656b 辅助文字、#e4e6e8 分隔、#f6f7f8 计划工具底色；绿色成功、琥珀警告、红色危险。颜色须同时配合文字，不把未知画成健康。
- 控件：桌面高度 36px，触控最小 44px；6px 圆角；禁用有明确文字与足够对比度；focus-visible 清晰。
- 表格：长名称允许换行，身份/端口保留；密集表格允许容器内横滚，页面不得整体溢出。
- 弹窗：原生 dialog、标题/内容/底部分区，保留取消、ESC、焦点返回与真实校验；本轮不替换执行流程。
- 动效：仅 120ms 的颜色反馈，不做延迟显现与循环装饰；尊重 reduced-motion。
- 状态：加载、失败、未知、权限不足与禁用分开；不把快照当实时，不用前端定时器捏造后端成功。

## 实现与评审入口

- [评审总页](http://127.0.0.1:18810/review)：三页的同尺寸前后截图、桌面/平板/手机切换、异常状态截图、规则和路由清单。
- [交互测试预览](http://127.0.0.1:18810/sample/?ui=review#operations)：真实 console 源码，隔离示例数据；搜索、筛选、分页、解锁交互可检查。任何写请求都被测试服务拒绝，不能产生真实数据库操作。
- [现场快照回放](http://127.0.0.1:18810/?ui=review#operations)：真实节点与地址来自已保存快照，回放使用只读评审身份；不是实时现场。
- [默认界面](http://127.0.0.1:18810/sample/#operations)：未启用评审主题。
- `18810` 只监听本机回环，是新增评审服务；原 `18789` 预览进程未停止，现场 `3000` 端口未改动。

改动集中于 `internal/api/console.html` 的可选主题变量、3 个代表页样式与展示挂钩。新增状态区只读取现有 state，不增加 API 请求，不伪造预检通过。原执行按钮、权限判断、服务端工作流及后端验证结果仍是唯一执行依据。4 个 Lucide 图标以内联节点形式使用，许可证见 `docs/licenses/lucide-ui-review.txt`，不增加 CDN 或运行期图标依赖。

这不是全站改造完成：预览中公共字体、控件主题随 `?ui=review` 生效，只有三个代表页完成内容布局设计。其他页面只做兼容检查，视觉推广必须在本轮确认之后。

## 验证结果

预览采用实际 console 源码。现场内容只使用已保存的脱敏只读快照并标明采集时间；缺少的历史/指标明确标注未采集，不拼接示例值冒充现场。独立测试夹具用于长内容、分页、错误和权限分支，不是现场验收。

快照来源：`.build/field-repair-20260910/final/152-api.json`，采集时间 `2026-09-10T04:00:13.130416Z`。回放过滤密码、token、passfile、conninfo、环境变量与原始输出字段；不存在的接口数据返回明确的未采集错误。未通过 SSH 或浏览器重新执行现场操作。

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| 8 路由 × 4 视口，修改前与评审样式 | 整页横向溢出 0，截断控件 0；保存视口与完整页面截图 | `before/capture.json`、`after/capture.json` |
| 默认界面与冻结基线的关键元素布局 | 32 组几何对照通过；新增评审元素默认不显示 | `integrity.json` |
| 3 代表页适用交互与边界 | 16 组通过，浏览器脚本异常 0 | `acceptance.json`、`states/` |
| 主库切换主要文字对比度 | 抽查 6 组均 ≥ 4.5:1，禁用按钮 4.83:1 | `integrity.json` |
| 长表格 | 390px 下表格容器可以横滚，页面不溢出 | `integrity.json` |
| 既有锁定、授权意图、生命周期回归 | 分别 16 / 19 / 48 组通过 | `regression/lock`、`operation-intent-acceptance`、`operation-lifecycle-audit` |
| 日志范围与分页 | 9 / 7 组通过；首批 20 条，全部 83 条历史可达，展开才查详情 | 范围测试输出、`regression/log-pagination-acceptance/result.json` |
| 节点安全与登录启动流程 | 31 / 5 组通过 | `regression/node-safety-audit`、`bootstrap-audit` |
| MySQL / PostgreSQL / Oracle / SQL Server 页面合同 | 73 组通过，含各引擎专属指标与不支持操作的禁用 | `regression/engine-pages-audit/result.json` |
| 评审入口 | 三页 × 三尺寸截图原始尺寸相同，9 组对照和完整图片链接通过；现场回放 MySQL/PG 均为只读，无非 GET 请求 | `portal.json`、`review-1440.png`、`review-768.png`、`review-390.png` |
| Go Console 测试 | `go test ./internal/api -run Console -count=1` 通过 | 本轮命令结果：`ok clusterguard.io/ha/internal/api` |
| 构建与格式检查 | Go 二进制构建、console 脚本语法、`git diff --check` 通过 | `.build/ui-ux-review-20260910/clusterguard` 仅为本机构建检查产物，不是安装包 |

证据根目录：`.build/ui-ux-review-20260910/`。上述“组”由各测试的场景粒度定义，不表示生产数据库演练次数。

日志范围脚本沿用自己的固定输出路径 `.build/log-scope/after/result.json`，其余套件通过 `CONSOLE_TEST_OUTPUT` 写入本轮证据目录。

适用状态包括：1440/768/390/320px 长中英文集群和主机名、无候选、未知/加载、拓扑失败、日志失败与重试、空搜索结果、viewer 权限、禁用按钮、表单必填校验、ESC 关闭与焦点返回、标签方向键/Home/End、reduced-motion。复核信息和多次重试标记没有随普通重复状态一起隐藏。

截图采集曾发现路由切换事件晚于点击返回，已将脚本改为等待目标视图可见再保存，并在 JSON 中记录 `visibleView`。正式对照使用重采结果，不使用误捕获上一页的截图。手机切换标题曾因继承 flex-wrap 导致溢出，已用明确 grid 布局修正并重测。

执行命令（在项目目录，Node 需要可解析本机 Playwright）：

```sh
node tools/console-ux-review.cjs before
node tools/console-ux-review.cjs after
node tools/console-ux-acceptance.cjs
node tools/console-ux-integrity.cjs
CONSOLE_UI_REVIEW=1 node tools/console-operation-lock-acceptance.cjs
CONSOLE_UI_REVIEW=1 node tools/console-operation-intent-acceptance.cjs
CONSOLE_UI_REVIEW=1 node tools/console-operation-lifecycle-audit.cjs
CONSOLE_UI_REVIEW=1 node tools/console-log-scope-acceptance.cjs
CONSOLE_UI_REVIEW=1 node tools/console-log-pagination-acceptance.cjs
CONSOLE_UI_REVIEW=1 node tools/console-node-safety-audit.cjs
CONSOLE_UI_REVIEW=1 node tools/console-bootstrap-audit.cjs
CONSOLE_UI_REVIEW=1 node tools/console-engine-pages-audit.cjs
go test ./internal/api -run Console -count=1
go build -o .build/ui-ux-review-20260910/clusterguard ./cmd/clusterguard
git diff --check
```

## 剩余问题与下一批

1. 待用户确认三个代表页的视觉；不默认推广或发布。本轮不生成升级包。
2. 拓扑、节点、指标、关于、设置，以及旧主恢复/灾难恢复完整页面布局仍待分批设计；既有流程不能因设计简化而删除。
3. 新增评审文案目前以中文为主，完整英文对等稿尚未完成；不得当作双语发布版本。
4. 没有既有深色主题，本轮深色检查不适用。尚未进行完整屏幕阅读器、Safari/Firefox、Windows 字体或实体触屏设备验收，不能声称完整 WCAG 达标。
5. 本轮验证为浏览器隔离接口与保存快照回放，不证明现场性能、数据库切换、VIP、恢复、升级或所有引擎原生能力已通过生产验收。
6. 后续按“拓扑与节点”“指标与阅读”“设置与升级/恢复弹窗”分批执行，每批保留本轮的默认锁、分页、集群联动与错误回归，不以静态截图替代业务验收。
