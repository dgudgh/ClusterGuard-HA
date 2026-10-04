# 设置：状态展示

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

settings-status-panel → /api/v1/control-plane/status → Leader、quorum、任务和维护状态。辅助接口失败不能冒充登录失效。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html)、[control_plane.go](../../../../internal/api/control_plane.go) |
| 回归 | [console-bootstrap-audit.cjs](../../../../tools/console-bootstrap-audit.cjs)、[console-engine-pages-audit.cjs](../../../../tools/console-engine-pages-audit.cjs) |

## 需要时再读

- [status](../../backend/settings/status.md)
