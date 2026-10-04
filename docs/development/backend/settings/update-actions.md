# 升级：动作、验签与门禁

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

显式包 ID → 重验签/摘要/基线 → 检查动作合法性 → 同包锁归属 → Helper 执行。热修 retry 与滚动 resume 分开；禁止外包锁接管和未知包降级。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [updates.go](../../../../internal/api/updates.go)、[manager.go](../../../../internal/platformupdate/manager.go)、[inspector.go](../../../../internal/platformupdate/inspector.go)、[helper.go](../../../../internal/platformupdate/helper.go)、[clusterguard-upgrade.sh](../../../../scripts/clusterguard-upgrade.sh)、[software_update.go](../../../../internal/store/software_update.go) |
| 回归 | [manager_test.go](../../../../internal/platformupdate/manager_test.go)、[supersedes_test.go](../../../../internal/platformupdate/supersedes_test.go)、[software_update_test.go](../../../../internal/store/software_update_test.go) |

## 需要时再读

- [upgrade-validation-chain](../../../zh-CN/upgrade-validation-chain.md)
- [updates](../../frontend/settings/updates.md)
