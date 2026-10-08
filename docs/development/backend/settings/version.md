# 平台版本信息

[后端功能](../README.md) → [设置接口](README.md) → 版本

`GET /api/v1/platform/version` → `platformVersionRoute` → `buildinfo.Current("clusterguard")` → no-store JSON。

这是当前程序构建信息投影，不探测所有控制器 RPM，不验签，也不证明签名热修包已经应用。页面版本、源码 HEAD、已交付构建提交和现场包身份须分别核对。

## 产品版本与兼容基线

- `product_version`：构建器从签名规格的 `patch_version` 注入当前二进制，四段产品版本；非法值或旧构建不返回该字段。
- `version/release`：既有 RPM 基线，例如2.2/105；升级来源准入仍使用它，不能用产品显示版本替换。
- `clusterguard version` 和页面优先显示产品版本，旧构建兼容回退到 `version-release`。
- 历史/待升级目标取同一个包经验签的 `Package.patch_version`；不从上传排序、文件名或历史包推导当前运行版本。补读旧包签名原件由 `internal/platformupdate/version.go` 完成，逐次核对摘要、验签缓存绑定可信公钥，不覆盖持久化操作历史。

## 定位与验证

修改响应看[control_plane.go](../../../../internal/api/control_plane.go)，修改构建字段看[buildinfo](../../../../internal/buildinfo)；接口契约由[control_plane_test.go](../../../../internal/api/control_plane_test.go)的版本测试覆盖。

响应正确但关于页显示不对时读[关于页面](../../frontend/about.md)；修改来源/目标兼容性时读[升级动作保护](update-actions.md)；准备交付身份才读[版本发布规范](../../../zh-CN/version-release-policy.md)。
