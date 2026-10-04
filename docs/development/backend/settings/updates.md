# 设置：升级与热修编排

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

/api/v1/platform/updates → Manager → 受限 Helper → job wrapper → Runner → 签名载荷。只选择下面与当前问题对应的子功能。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [updates.go](../../../../internal/api/updates.go)、[platformupdate](../../../../internal/platformupdate)、[updatecontract](../../../../internal/updatecontract)、[clusterguard-update-job.sh](../../../../scripts/clusterguard-update-job.sh)、[clusterguard-upgrade.sh](../../../../scripts/clusterguard-upgrade.sh)、[software_update.go](../../../../internal/store/software_update.go) |
| 回归 | [updates_test.go](../../../../internal/api/updates_test.go)、[manager_test.go](../../../../internal/platformupdate/manager_test.go)、[history_test.go](../../../../internal/platformupdate/history_test.go)、[software_update_test.go](../../../../internal/store/software_update_test.go) |

## 需要时再读

- [update-actions](update-actions.md)
- [update-history](update-history.md)
- [update-validation](update-validation.md)
