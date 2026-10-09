# 设置：运行参数只读视图

[开发入口](../../README.md) · [前端页面入口](../README.md) · [设置功能](README.md)

## 页面入口与调用链

`settings-configuration-tab` → `setSettingsSection('configuration')` → `loadConfiguration()` → `GET /api/v1/control-plane/configuration` → `renderConfiguration()` → `configuration-sections`。

进入运行参数面板时还会并行调用 `fetchClusterPolicy()`。本页只描述配置投影；修改同一面板中的策略字段、保存或清除行为时继续读[引擎策略编辑](cluster-policy.md)。

`reload-configuration` 只再次调用 `loadConfiguration()`，刷新操作员看到的投影。它不修改配置文件、不重新配置运行进程，也不能表示配置已经热加载。

## 状态、字段与范围

- `state.configuration`：当前应答控制节点返回的配置投影。
- `state.configurationLoading`：禁用重新读取按钮并显示读取中。
- `state.configurationError`：保留请求失败原因；旧投影存在时不会把错误伪装成新成功结果。
- `renderConfiguration()` 展示 `file_present`、`path`、`process_started_at`、`file_modified_at`、`reload_supported`、`reload_note` 和 `warnings`。
- `sections[].values[]` 展示 `key`、`value`、`source`、`restart_required`、`credential_ref` 和 `note`。
- `source` 显示为配置文件、集群策略或平台默认；配置值只用 `textContent` 渲染。

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

`state.configurationExpandedSections`按section.key记录当前会话的逐组状态，重新读取、读取失败和语言重绘保留展开选择；整个页面重新加载默认折叠。会话清理清空状态与旧参数DOM，不把前一个账户的选择带给后一个账户。只改变显示，不调用写参数、热加载或重启接口。

[参数折叠真实浏览器验收](../../../../tools/console-configuration-collapse-acceptance.cjs)覆盖1440/390像素、初始折叠、独立点击/键盘切换、刷新、错误、语言、重载/会话清理、局部表格滚动、原始参数和凭据引用按文本显示、无写入。策略写入仍由`console-cluster-policy-audit.cjs`验证；现场配置下发与重启不属于折叠验收。
