# 设置：账户与会话

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

状态设置 → 修改密码/退出 → auth API；开始刷新或退出就撤回旧操作授权，不能等响应回来才撤回。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html)、[auth.go](../../../../internal/api/auth.go) |
| 回归 | [console-session-boundary-audit.cjs](../../../../tools/console-session-boundary-audit.cjs)、[console-bootstrap-audit.cjs](../../../../tools/console-bootstrap-audit.cjs) |

## 需要时再读

- [account](../../backend/settings/account.md)
- [operation-safety](../../rules/operation-safety.md)

## 必须保持与回归

- 刷新和退出必须测试真实按钮从点击到响应返回前的窗口，以及请求失败、迟到和新登录；只直接调用 `loadSelectedCluster` 或 `showLogin` 不算覆盖。开始刷新/退出即收回旧授权，不能等服务器返回才锁定。
