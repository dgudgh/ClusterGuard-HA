# 设置与关于：版本接口

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

GET /api/v1/platform/version → platformVersionRoute → buildinfo；仅提供版本展示。正式交付身份还须核对 RELEASE-INFO、签名与摘要。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [control_plane.go](../../../../internal/api/control_plane.go)、[buildinfo](../../../../internal/buildinfo)、[server.go](../../../../internal/api/server.go) |
| 回归 | [control_plane_test.go](../../../../internal/api/control_plane_test.go) |

## 需要时再读

- [about](../../frontend/about.md)
