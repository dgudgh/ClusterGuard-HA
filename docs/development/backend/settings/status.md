# 控制面状态接口

[后端功能](../README.md) → [设置接口](README.md) → 状态

本接口描述控制面节点、成员和任务状态，不等于某个数据库集群的健康报告。

## 调用与来源

`GET /api/v1/control-plane/status` → `controlPlaneStatusRoute` → 两秒 context → `currentControlPlaneStatus` → 状态 provider → no-store 响应。

- 配置了 provider 时读其当前控制面事实；provider 返回错误时响应 503，不伪造 Leader/quorum。
- 未配置 provider 时使用 `localControlPlaneStatus` 的 standalone 投影，统计本地存储中的运行操作、待复核记录和活动节点任务。
- maintenance checker 报阻断时，投影将 `update_maintenance_active` 设为 true。
- GET 身份校验在 `server.go`；状态接口不会替代执行接口的权限、Leader 或多数派校验。

## 定位与验证

实现和 DTO 位于[control_plane.go](../../../../internal/api/control_plane.go)。[control_plane_test.go](../../../../internal/api/control_plane_test.go)分别检查安全状态投影、已认证读取、standalone 计划计数与未复核记录。

API 状态正确而显示错误时才读[前端控制面状态](../../frontend/settings/status.md)；维护归属或升级动作有问题时读[升级动作保护](update-actions.md)。
