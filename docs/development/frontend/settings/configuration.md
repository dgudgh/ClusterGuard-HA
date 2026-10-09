# 设置：运行参数与节点下发

[开发入口](../../README.md) · [前端页面入口](../README.md) · [设置功能](README.md)

## 页面入口与调用链

`settings-configuration-tab` → `setSettingsSection('configuration')` → `loadConfiguration()` → `GET /api/v1/control-plane/configuration` → `renderConfiguration()` → `configuration-sections`。

进入运行参数面板时还会并行调用 `fetchClusterPolicy()`。本页描述配置投影与节点下发；修改同一面板中的动态策略字段、保存或清除行为时继续读[引擎策略编辑](cluster-policy.md)。

`reload-configuration` 只再次调用 `loadConfiguration()`，刷新操作员看到的投影。它不修改配置文件、不重新配置运行进程，也不能表示配置已经热加载。

## 状态、字段与范围

- `state.configuration`：当前应答控制节点返回的配置投影。
- `state.configurationLoading`：禁用重新读取按钮并显示读取中。
- `state.configurationError`：保留请求失败原因；旧投影存在时不会把错误伪装成新成功结果。
- `renderConfiguration()` 展示 `file_present`、`path`、`process_started_at`、`file_modified_at`、`reload_supported`、`reload_note` 和 `warnings`。
- `sections[].values[]` 展示 `key`、`value`、`source`、`restart_required`、`credential_ref` 和 `note`。
- `source` 显示为配置文件、已下发配置、集群策略或平台默认；配置值只用 `textContent` 渲染。

浏览器没有配置 `localStorage` 或 `sessionStorage` 键。配置路径、值和时间来自当前节点的只读接口；凭据只显示引用信息。若改接口字段、脱敏、来源或重启语义，必须读[运行参数接口](../../backend/settings/configuration.md)。

## 失败与策略联动

- 无投影时显示“读取中”或“不可用”，清空文件元数据并隐藏警告。
- 接口失败写入 `state.configurationError`；旧会话响应由 `fetchResult()` 的会话检查丢弃。
- `configuration-policy` 仅在 `state.configuration` 存在时显示。
- 策略读取失败时 `state.clusterPolicy` 为空，保存和清除按钮必须禁用；配置读取成功不能掩盖策略读取失败。
- `restart_required` 只描述生效方式；“重新读取文件”不得把“需重启”改写成已生效。

## 实现与验证入口

| 职责 | 入口 |
| --- | --- |
| 前端加载与渲染 | [console.html](../../../../internal/api/console.html) |
| 配置接口 | [configuration.go](../../../../internal/api/configuration.go) |
| 配置接口回归 | [configuration_test.go](../../../../internal/api/configuration_test.go) |
| 前端静态契约 | [console_test.go](../../../../internal/api/console_test.go) |
| 策略失败、输入、保存与窄屏真浏览器检查 | [console-cluster-policy-audit.cjs](../../../../tools/console-cluster-policy-audit.cjs) |
| 设置页尺寸烟测 | [console-engine-pages-audit.cjs](../../../../tools/console-engine-pages-audit.cjs) |

## 参数分组默认折叠

每个`sections[]`使用原生`details/summary`，初始全部关闭，标题点击、Enter或Space展开/折叠当前组；标题保留说明并显示该组参数数目。展开后的表格只在组内横向滚动，不拉宽整个页面。

`state.configurationExpandedSections`按类别与section.key共同记录当前会话的逐组状态，重新读取、读取失败和语言重绘保留展开选择；整个页面重新加载默认折叠。会话清理清空状态与旧参数DOM，不把前一个账户的选择带给后一个账户。只改变显示，不调用写参数、热加载或重启接口。

[参数折叠真实浏览器验收](../../../../tools/console-configuration-collapse-acceptance.cjs)覆盖1440/390像素、初始折叠、独立点击/键盘切换、刷新、错误、语言、重载/会话清理、局部表格滚动、原始参数和凭据引用按文本显示、无写入。策略写入仍由`console-cluster-policy-audit.cjs`验证；现场配置下发与重启不属于折叠验收。

## 3.1.2.1：页面编辑与节点下发

原只读表升级为“有效值＋末列可编辑草稿”；默认折叠和会话展开状态保持。25项白名单整数有范围限制，凭据、网络、身份、数据路径及隔离启停显示专用变更流程说明；原动态策略面板保持独立。

调用链：读取当前Leader的`configuration/distribution`及本节点的`configuration/node` → 解锁 → 编辑参数、选择控制节点 → `POST configuration/plan` → 展示逐节点原值/目标值、重启说明 → 二次确认 → `POST configuration/dispatch`。请求绑定唯一UUID、预检摘要、参数和目标集合，不能把顶部数据库集群当控制节点范围。

草稿、目标、登录会话、操作锁或页面集群上下文变化后，旧预检不得执行；重复点击不能创建第二个任务。提交后分别显示等待、重启/核验、已核验或失败，不能把Raft提交当全部应用。状态读取失败显示未确认并禁用恢复动作；失败恢复需重新解锁、确认指定任务ID及revision，使用其retry/rollback接口。注销清除草稿与选择，但不能取消已提交的后端任务。

所有控件随界面语言切换。周期读取任务状态，结束后刷新节点有效值；未编辑草稿保留，刷新不下发参数。详细后端边界见[运行参数与节点下发](../../backend/settings/configuration.md)。

回归：[真实浏览器编辑与下发验收](../../../../tools/console-configuration-distribution-acceptance.cjs)，覆盖1440/390、数值边界、目标选择、预检差异、任务提交与实际核验区别、失败与读错误、迟到预检、会话清理、双语和窄屏。它使用隔离API响应，不作为生产节点参数生效证据。

## 3.1.2.4：控制节点卡片

“控制节点参数下发”的目标选择从一行裸 UUID 改为每个投票节点一张卡片：复选框、节点名称、节点不可变 ID、该投票节点的可信 API 地址。三件事实各自独立，互不推导：

- 名称来自节点清单（`GET /api/v1/nodes` 的 `node_name`）；清单没有这个 `resource_id` 时显示“未登记节点”，不回退成 ID。
- ID 是这个投票节点的不可变身份；选择、编辑草稿、切换到别的集群都不改写它。
- 地址优先取 Leader 为该投票节点记录的可信 API 端点（`members[].api_address`），缺失时回退到清单的 `ip_address`／`hostname`，都没有时显示“无可用地址”。

名称旁只在事实成立时加标记：服务本页的控制节点显示“本机”，当前 Leader 显示“Leader”，两者可同时出现（`本机 · Leader`）。标记来自 `state.configurationDistribution.local.node_id` 与 `state.controlPlane.leader_id`，不来自被选中的集合。

卡片**不显示“在线／离线”**：控制面没有控制器存活探测。`/api/v1/control-plane/status` 的 `controller_members` 和 `configuration/distribution` 的 `members` 都只带身份与端点（`resource_id`、`raft_address`／`api_address`），没有任何逐投票节点可达性字段。要显示在线／离线必须先在后端补探测接口，属新功能；在此之前页面不猜测状态。

复选框固定 16×16 像素并沿用主题强调色。控制台全局的 `input { width:100%; min-height:34px }`（窄屏与 review 模式另有 36／44 像素）会把它拉成长方块，这条尺寸规则必须留在卡片作用域内。卡片本身是 `<label>`，点击卡片任意位置切换它自己的复选框；复选框仍带 `data-configuration-node`，提交仍按 `input:checked` 收集目标集合。

回归：[真实浏览器编辑与下发验收](../../../../tools/console-configuration-distribution-acceptance.cjs)新增五组断言（1440/390 各一组）：名称／ID／地址三件事实、“本机”只出现在服务本页的控制节点、复选框 16 像素、卡片内不出现“在线／离线”措辞、点击卡片切换自身复选框。[统一校验链](../../delivery/hotfix-catalog-validation.md)的 `INV-004 / 23` 用页面实际下发的渲染循环（stub DOM）反查同一组事实，并有五条变异。

## 3.1.2.5：卡片角色标记与端点链接

在 3.1.2.4 的卡片基础上按现场截图调整视觉，事实来源不变：

- 每卡恰好一个角色标记，优先级“本机 > Leader > 投票节点”。三者都有据可查：服务本页的控制节点来自 `state.configurationDistribution.local.node_id`，当前 Leader 来自 `state.controlPlane.leader_id`，出现在下发成员列表本身即投票节点。标记配色只沿用既有变量（蓝 `--blue-soft/--accent-strong`、橙 `--warning-soft/--warning`、绿 `--success` 的浅底），不新增色相；本机同时是 Leader 时只显示“本机”。
- 复选框移到卡片右侧，固定 18×18 像素；尺寸规则仍在卡片作用域内，覆盖全局 `input { width:100%; min-height:34px }`。
- 地址行带链接图标。Leader 记录的可信端点自带协议（`internal/runtime/runtime.go` 拼 `apiScheme://host:port`，形如 `https://192.168.102.152:3000`），带 `http(s)://` 时渲染为新页打开的链接；裸 IP／主机名仍是纯文本，不拼协议。链接在 `<label>` 内属交互元素，点击打开新页不会触发卡片复选框。

三件事实互不推导、不显示在线／离线的规则与 3.1.2.4 相同。

回归：真实浏览器验收在 1440/390 各更新断言——单角色标记序列（本机／Leader／投票节点，fixture 让本机与 Leader 落在不同节点）、`https://` 端点渲染为新页链接、复选框 18 像素。统一校验链 `INV-004 / 23` 的 stub DOM 反查同步更新，变异增至八条（新增：复选框移回事实左侧、投票节点标记丢失、`https` 端点退化为纯文本）。

## 审查修正：范围、时限与可复跑证据

字段`scope=cluster`要求全部投票节点；子集选择时前端拒绝预检并显示双语原因，后端独立检查当前成员。仅`scope=node`的Raft应用时限和Agent并发数可选子集。确认框显示后端计划的逐节点`step_timeout_seconds`，该时限参与摘要并持久化，Leader变化不以新Leader本地配置替换。

浏览器验收使用Playwright库及本机已安装Chrome的`channel: chrome`；无需Playwright专属Chromium缓存，但必须能加载库并启动Chrome。桌面/窄屏的范围限制、实际时限、中文/英文确认均有真实浏览器事件测试，API仍是隔离fixture。依赖诊断及复跑命令见[验收工具入口](../../delivery/hotfix-catalog-validation.md)；不能写成现场生效验证。

## 参考图布局：选择、计数与底部操作

节点卡片沿用真实名称/ID/可信地址与角色来源；勾选卡片用蓝色边框和背景标记，复选框仍受解锁条件控制。选中数量按当前投票成员与实际选择交集计算，未选择时为0；没有读取成功时不得以3台常量冒充。卡片点击与Space立即同步数量，确认失效/草稿时序仍按原接口规则处理。

文件路径、进程启动和文件修改时间独立展示；分组标题统计接口实际`sections/values`，初始仍全部折叠。运行参数页底部固定显示所选数、解锁、预检下发和清稿；窄屏换行，离开此页隐藏，通知上移避免遮挡操作，底部留空间避免遮住最后一项。不会因按钮移位绕过原授权、预检或实际生效核验。

“重新读取”只刷新投影，白名单通过确认下发和逐节点重启后核验，动态策略另用策略面板。已知旧版reload_note与CG_APPROVAL_TOKEN弃用告警按语言映射，接口原文不修改，未知诊断仍按文本保留。文件路径和参数值不作为翻译键。真实Chrome验收覆盖1440/390、所选数量、按钮定位/切页隐藏、通知无覆盖、键盘、双语及原下发链；这是隔离API验证，不是现场重启验收。

## 参数分类与末列修改值

上方“可在页面变更”列出本节点`configuration/node.fields`实际白名单，原生模块组继续默认折叠；最后一列“修改值”放整数草稿，第二列仅保留有效值。下方“需专用变更流程”仅展示凭据、路径、身份和其他不在白名单的字段。同一模块有两类字段时拆成两个独立折叠组，标题数按该组实际字段计算，总数仍按原始接口统计，不重复参数。白名单不随解锁、角色或任务锁改变，输入框仍受原授权条件控制。

原动态策略摘要和独立策略编辑器保留在上方，继续使用已有策略保存/清除流程；不替换成整数下发、不误标为不能在线修改。分类使用白名单与策略来源，不用`restart_required`猜可编辑性。凭据引用始终无编辑器。

尚未取得白名单时普通参数显示“变更能力尚未确认”，读取成功后重新分类；读取失败保留已知白名单并禁用下发。能力集合变化才重绘参数表，避免周期查询打断正在编辑的输入；重读和语言切换保留草稿以及按类别独立的展开状态。会话清理移除旧参数并保留隐藏的策略表单挂载点，不遗失按钮事件或带入上一账号数据。

[真实Chrome分类验收](../../../../tools/console-configuration-groups-acceptance.cjs)覆盖1440/390、末列输入、生命周期混组、字段守恒、凭据和非重启字段不误编辑、未读与迟到白名单、错误锁定、独立展开、重读草稿、双语、策略挂载及会话清理；只使用隔离API响应，未证明生产节点重启或参数生效。
