# 设置：账户与会话

[开发入口](../../README.md) · [前端页面入口](../README.md) · [设置功能](README.md)

## 页面入口与调用链

账户入口位于 `settings-status-panel`，与侧栏账户菜单共用同一套函数：

- 身份展示：`showAuthenticatedConsole(user)` → `state.currentUser` → `renderAuthenticatedUser()` → 设置页与侧栏。
- 修改密码：`settings-change-password` → `openPasswordChange(false)` → `savePasswordChange()` → `POST /api/v1/auth/password` → `showLogin()`。
- 退出：`settings-logout` → `logout()` → `POST /api/v1/auth/logout` → `showLogin()` → `clearSessionData()`。
- 会话校验：`fetchResult()` 捕获请求开始时的 `authEpoch` 与 `currentUser`；会话变化后到达的旧响应按 `staleSession` 丢弃，401 才进入登录页。

## 状态、凭据与默认行为

- `state.currentUser`：当前身份和角色；仅 `admin`、`operator` 可操作集群，平台管理只允许 `admin`。
- `state.authEpoch`：登录、退出或会话失效时递增，用于隔离迟到响应。
- `state.loggingOut`：退出请求开始时立即置为 `true`；权限判断随即失效并调用 `relockSwitch()`，不等待服务端响应。
- `state.passwordChangeRequired`：首次登录强制改密时禁止关闭密码对话框。
- `state.authenticating`：阻止登录按钮重复提交。
- 密码输入只在对话框内短暂存在；发出请求后在 finally 清空。两次新密码不一致时先显示提示并返回，不发请求。

浏览器没有账户 `localStorage` 或 `sessionStorage` 键。`clusterguard_session` 是 HttpOnly 会话 Cookie；`clusterguard_csrf` 由 `csrfCookieValue()` 读取并作为 `X-CSRF-Token` 发送。修改 Cookie、CSRF、会话撤销或密码策略时必须读[认证与账户接口](../../backend/settings/account.md)。

## 失败与会话边界

- 修改密码成功后撤销当前页面身份并要求重新登录；失败时保留对话框、显示错误并清空密码字段。
- 退出失败时保持操作锁定并显示错误；不能因请求失败恢复此前已解锁的操作意图。
- `showLogin()` 会停止自动刷新和升级轮询、终止或作废旧请求、关闭对话框并清除旧用户缓存。
- 普通数据读取失败不是会话失效；只有 401 或已确认的会话变化可以隐藏控制台。
- 设置页与侧栏的改密、退出入口必须保持相同语义。

涉及刷新、退出或新登录期间的授权撤回、操作锁和迟到响应时，必须读[高风险操作授权规则](../../rules/operation-safety.md)；只改账户文字或布局时无需展开该规则。

## 实现与验证入口

| 职责 | 入口 |
| --- | --- |
| 前端状态与交互 | [console.html](../../../../internal/api/console.html) |
| Cookie、CSRF 与认证路由 | [auth.go](../../../../internal/api/auth.go) |
| 设置页合并与改密入口真浏览器检查 | [console-settings-merge-acceptance.cjs](../../../../tools/console-settings-merge-acceptance.cjs) |
| 退出、旧缓存、迟到 401 与新会话 | [console-session-boundary-audit.cjs](../../../../tools/console-session-boundary-audit.cjs) |
| 认证接口与 Cookie | [auth_test.go](../../../../internal/api/auth_test.go) |

`console-settings-merge-acceptance.cjs` 只验证设置页按钮可达并能打开改密对话框；`console-session-boundary-audit.cjs` 从侧栏执行退出。设置页改密提交、设置页退出按钮、退出失败窗口和强制改密完整流程目前没有对应的专用真浏览器覆盖，未执行不得记为通过。

## 必须保持与回归

- 刷新和退出必须测试真实按钮从点击到响应返回前的窗口，以及请求失败、迟到和新登录；只直接调用 `loadSelectedCluster` 或 `showLogin` 不算覆盖。开始刷新/退出即收回旧授权，不能等服务器返回才锁定。
