# 设置：升级与热修交互

[前端页面](../README.md) → [设置功能](README.md) → 版本更新

修改本功能前必须读[强制契约](../../../zh-CN/upgrade-validation-chain.md)和[操作授权规则](../../rules/operation-safety.md)。

## 用户入口与请求链

`software-update-panel` → `loadSoftwareUpdates()` → 包列表/当前维护事实 → 选择动作对象 → 上传校验或确认框 → POST → 按 operation_id 查询结果。

实现集中在 [console.html](../../../../internal/api/console.html) 的 `loadSoftwareUpdates`、`softwareUpdateActionable`、动作确认和提交处理。接口及真实合法状态在[后端动作保护](../../backend/settings/update-actions.md)，不能以按钮 disabled 代替后端校验。

## 对象、状态与错误

- 先绑定用户点击项的 patch_id，再生成按钮与确认框；POST 的 ID 必须与二次确认输入一致。
- 成功热修不提供续跑；失败热修重新执行走 retry，不先 plan 覆盖失败；失败滚动升级走 resume。
- 部署状态与最新尝试分别展示；已安装后的失败计划不是卸载。无关较新成功不能按版本或时间自动覆盖旧失败。
- 受理响应不确定时仅查同一 operation_id；相同秒时间戳不能代替操作身份。不因断连、5xx 或超时自动再次提交。
- 用户换上下文、退出或失去授权时，旧确认不得继续执行；后台进度查询和提交授权是不同生命周期。

## 回归与条件跳转

[升级确认验收](../../../../tools/console-update-confirmation-acceptance.cjs)覆盖确认/请求/不确定结果；[热修恢复验收](../../../../tools/console-update-hotfix-recovery-acceptance.cjs)覆盖热修 retry、滚动 resume、跨记录执行/回退对象。两者是隔离 API 上的浏览器回归，不能代表现场包已验收。

后台状态也错误时读[部署与操作历史](../../backend/settings/update-history.md)；门禁无法推进时读[契约与验收门禁](../../backend/settings/update-validation.md)；实际构建或现场执行才进入[交付流程](../../delivery/README.md)。

## 必须保持与回归


- 升级与热修的动作对象必须用真实点击覆盖：成功的热修不提供「续跑」；失败的热修提供「重新执行」而非「续跑」；被拒绝的续跑在成功事件链上留下的失败状态按「本次尝试失败」解释；失败的滚动升级保留「续跑」；以及列表里「更新的成功记录在上、失败记录在下」时，执行与回退都只作用于失败的那一条，且请求里的 `patch_id` 与确认框要求输入的 id 一致。回归入口 `tools/console-update-hotfix-recovery-acceptance.cjs`，逐场景如实报告是否执行、是否通过。

## 版本显示来源

当前运行版本取平台版本 API 的 `product_version`，没有该字段时显示 RPM `version-release`；上传更高版本不改变这个值。目标、确认框和历史行取各包已验证的 `patch_version`，旧包回退到 `target_version`。历史与进度的版本变化用当前操作的 `job.from_version → job.to_version`（正常应用目标缺字段时仍可读取同包签名目标）。Runner在维护锁建立后、载荷替换前读取各节点二进制版本；`from_node_versions`保留逐节点观测，只有全部一致才填from_version。混合节点明确显示“节点版本不一致”；旧记录或节点不可读取显示“执行前版本未记录”。不从上一条成功记录、上传顺序、当前运行版本或签名source推测历史起点；回退目标未记录时明确显示未知。RPM基线单独列出，用于兼容性核对，不放在实际版本变化箭头左侧。真实桌面及390像素浏览器回归覆盖当前3.1.1.1与待升级3.1.1.3同时存在，防止上传记录冒充运行版本。

上传预览的身份栅格把三件互相独立的事实分开排列：**当前运行版本 / RPM兼容基线 / 目标版本**。运行版本与版本摘要同源（`platformVersionText`，即平台版本 API），选择和上传任何包都不改变它；`RPM兼容基线`是包的准入条件，`目标版本`取该包已验证的`patch_version`。三者不得互相推导，也不得用上传记录填充运行版本。

签名校验按包类型分派。热修补丁不带 RPM 升级引导器——它按签名清单替换文件并保留已装 RPM 记录——所以它显示`已通过 · 热修补丁`，而不是拿`bootstrap_available`判出来的`已通过 · 历史兼容包`。滚动升级包才用引导器口径：有引导器显示`已通过 · 引导器 v{0}`，没有引导器的历史包保留`已通过 · 历史兼容包`。未验签一律`未通过`。包类型文案写明热修**会更新产品版本**并保留 RPM 安装记录，不再只说「不改 RPM 版本」。

## 版本身份与旧记录替代

新包ID等于产品版本，确认输入与请求绑定一致；API给出的superseded_by排除旧失败候选，历史保留替代标记，摘要与当前成功对应；无验签替代声明不隐藏旧失败。

## 节点传输阶段的进度

`staging` 表示向下一控制节点传输已签名补丁，属于“节点滚动”步骤；页面显示传输提示，百分比取同一操作的后端进度，不退回准备步骤。进入新 retry/rollback 操作时显示新操作的进度，不能用上次成功的100%或跨操作累计最大值代替。

[传输进度浏览器回归](../../../../tools/console-update-staging-progress-acceptance.cjs)使用真实Manager读取持久化status/events的三节点序列，再通过隔离API轮询真实页面，覆盖1440/390像素、每个节点的staging边界、最终成功及新操作重置；不执行现场升级。

## 结果和事件的语言

中文页面的历史结果、当前任务说明和两处事件列表共用`softwareUpdateMessageText`/`renderSoftwareUpdateMessage`。已知Runner消息及旧中文结果按当前语言双向映射；切换语言时立即重绘升级结果、状态、动作和事件。状态、subject和动作判断仍读取原始字段，不从翻译结果推断成功或门禁状态。

未知诊断显示当前语言的核对提示，并提供可展开的“原始信息”，按字面文本呈现，不执行其中的HTML；原始Job/events与输出日志不改。原始输出区域继续保留原文，供排查使用。

[消息映射回归](../../../../tools/console-update-message-language.test.cjs)检查Runner全部固定事件消息；[真实浏览器回归](../../../../tools/console-update-message-language-acceptance.cjs)覆盖桌面/窄屏、新旧成功记录、失败/门禁保留、传输、未知诊断和语言往返切换。完整页面语言绑定见[显示偏好](preferences.md)。

[操作版本浏览器回归](../../../../tools/console-update-version-transition-acceptance.cjs)覆盖桌面/窄屏、实际7到8、旧记录、混合节点、观测缺失、回退、双语及原始API字段不改。
