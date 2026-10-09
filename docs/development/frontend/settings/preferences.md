# 设置：显示偏好

[开发入口](../../README.md) · [前端页面入口](../README.md) · [设置功能](README.md)

## 页面入口与调用链

显示偏好位于 `settings-status-panel`，只有界面语言和自动刷新间隔两个控件。

- 语言：`language-select change` → `changeConsoleLanguage()` → 校验语言 → `state.language` → `applyLanguage()` → 重绘账户、控制面、运行参数、八个页面及打开的对话框。
- 自动刷新：`refresh-interval change` → `state.refreshIntervalMs` → `scheduleAutoRefresh()` → `setInterval()` → `loadSelectedCluster({ preserveOperationResult:true })`。

自动刷新只在已选择集群，且没有操作执行、节点任务执行、节点生命周期弹窗或集群加载时触发。它刷新所选集群，不写配置文件或集群策略。

## 默认值、范围与存储

- `state.language` 默认 `zh-CN`；可选 `zh-CN`、`en-US`。
- `state.refreshIntervalMs` 默认 `60000`；可选 30 秒、60 秒、2 分钟或关闭 `0`。
- `state.refreshTimer` 保存当前定时器；重新选择间隔前先清除旧定时器。
- 静态文案在首次 API 加载前通过 `registerStaticUILanguage()` 绑定原始 HTML 文本节点和 title/placeholder/aria-label；只绑定双语目录已有的界面文字。切换时更新仍在页面内的绑定，不扫描或替换后来加载的业务数据。
- 动态界面文案用 `ui()` 和同一 `uiCatalog`；插值值保留原文。缓存状态标签在访问时按当前语言生成；已知界面提示可通过 `localizeUIMessage()` 在重绘时切换语言。
- `changeConsoleLanguage()` 更新账户/Leader/quorum/任务/时长、各页、升级消息和打开的确认/进度/改密等窗口；不加载配置或策略，不提交后端动作，不重置表单输入。
- 集群/节点/用户名称、ID、参数值和原始诊断不是界面文案，保持原值。未知升级诊断用当前语言的核对提示及可展开原文，不能凭状态推断成功。

没有偏好 `localStorage`、`sessionStorage` 或后端存储键：

- 完整页面刷新后恢复 `zh-CN` 和 60 秒默认值。
- `showLogin()` 会清除自动刷新定时器，但不会重置 `state.language` 或 `state.refreshIntervalMs`。
- 同一页面内重新登录后，`loadConsoleAfterAuthentication()` 按仍在内存中的刷新间隔重建定时器。
- 关闭浏览器或重新加载页面后，内存偏好不会保留。

## 失败与安全边界

- 选择“关闭”时 `scheduleAutoRefresh()` 只清除现有定时器，不创建新定时器。
- 自动刷新跳过正在执行的操作和节点任务，不能打断、重复提交或解除操作锁。
- 切换语言只影响显示；不得改变选中集群、筛选范围、按钮可用条件或后端请求。
- 显示偏好没有独立后端 API；若需求要求跨刷新持久化，应作为新的存储与安全设计处理，不能声称现有行为已经支持。

涉及自动刷新与操作授权、节点任务或请求晚到的联动时，必须读[集群上下文](../context.md)和[高风险操作授权规则](../../rules/operation-safety.md)；只改语言文案时无需展开高风险规则。

## 实现与验证入口

| 职责 | 入口 |
| --- | --- |
| 状态、事件与计时器 | [console.html](../../../../internal/api/console.html) |
| 控件位置与状态面板切换真浏览器检查 | [console-settings-merge-acceptance.cjs](../../../../tools/console-settings-merge-acceptance.cjs) |
| 禁止持久存储与静态契约 | [console_test.go](../../../../internal/api/console_test.go) |
| 自动刷新期间节点安全边界 | [console-node-safety-audit.cjs](../../../../tools/console-node-safety-audit.cjs) |
| 设置页桌面与窄屏尺寸烟测 | [console-engine-pages-audit.cjs](../../../../tools/console-engine-pages-audit.cjs) |

[完整语言浏览器回归](../../../../tools/console-language-acceptance.cjs)通过真实语言选择事件检查四引擎八页面、设置、打开的改密窗口、数据刷新、往返切换、无请求及输入/身份值保持；[双语目录回归](../../../../tools/console-language.test.cjs)核对静态文字/辅助属性覆盖、英文与插值对应、已知提示往返。升级结果另见[升级设置](updates.md)。自动刷新间隔、退出后定时器重建和完整页面刷新恢复默认值仍未由语言回归覆盖，需分别验证。
