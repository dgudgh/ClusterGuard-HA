# 设置：显示偏好

[开发入口](../../README.md) · [前端页面入口](../README.md) · [设置功能](README.md)

## 页面入口与调用链

显示偏好位于 `settings-status-panel`，只有界面语言和自动刷新间隔两个控件。

- 语言：`language-select change` → `state.language` → `applyLanguage()` → 重新渲染总览、拓扑、操作上下文、指标、节点、生命周期任务和操作日志。
- 自动刷新：`refresh-interval change` → `state.refreshIntervalMs` → `scheduleAutoRefresh()` → `setInterval()` → `loadSelectedCluster({ preserveOperationResult:true })`。

自动刷新只在已选择集群，且没有操作执行、节点任务执行、节点生命周期弹窗或集群加载时触发。它刷新所选集群，不写配置文件或集群策略。

## 默认值、范围与存储

- `state.language` 默认 `zh-CN`；可选 `zh-CN`、`en-US`。
- `state.refreshIntervalMs` 默认 `60000`；可选 30 秒、60 秒、2 分钟或关闭 `0`。
- `state.refreshTimer` 保存当前定时器；重新选择间隔前先清除旧定时器。
- `applyLanguage()` 只更新带 `data-i18n` 的文字和显式重新渲染的动态区域，不代表所有硬编码中文均已翻译。

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

现有设置页浏览器脚本只确认两个控件可见；节点安全脚本通过直接修改 `state` 驱动短间隔。用户实际切换语言、切换或关闭刷新间隔、退出后重建定时器以及完整页面刷新恢复默认值目前没有专用真浏览器覆盖，未执行不得记为通过。
