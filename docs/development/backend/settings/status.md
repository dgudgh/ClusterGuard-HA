# 设置：控制面状态接口

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

GET /api/v1/control-plane/status → currentControlPlaneStatus → 状态 provider/本地回退 → 响应。核对 Leader、成员、quorum、维护及活动任务来源，不把旧成功观测当作当前状态。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [control_plane.go](../../../../internal/api/control_plane.go)、[server.go](../../../../internal/api/server.go)、[runtime](../../../../internal/runtime) |
| 回归 | [control_plane_test.go](../../../../internal/api/control_plane_test.go) |
