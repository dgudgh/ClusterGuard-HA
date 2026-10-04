# 平台版本信息

[后端功能](../README.md) → [设置接口](README.md) → 版本

`GET /api/v1/platform/version` → `platformVersionRoute` → `buildinfo.Current("clusterguard")` → no-store JSON。

这是当前程序构建信息投影，不探测所有控制器 RPM，不验签，也不证明签名热修包已经应用。页面版本、源码 HEAD、已交付构建提交和现场包身份须分别核对。

## 定位与验证

修改响应看[control_plane.go](../../../../internal/api/control_plane.go)，修改构建字段看[buildinfo](../../../../internal/buildinfo)；接口契约由[control_plane_test.go](../../../../internal/api/control_plane_test.go)的版本测试覆盖。

响应正确但关于页显示不对时读[关于页面](../../frontend/about.md)；修改来源/目标兼容性时读[升级动作保护](update-actions.md)；准备交付身份才读[版本发布规范](../../../zh-CN/version-release-policy.md)。
