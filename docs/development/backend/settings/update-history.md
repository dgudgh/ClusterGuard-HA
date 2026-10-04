# 升级：部署状态、历史与幂等

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

operation_id → 受理记录 → job/status → deployment.json + requests.jsonl + operations.jsonl → 显式 ?history=1 查询。已安装状态不能被失败计划覆盖；私有历史冲突保留证据并阻断。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [history.go](../../../../internal/platformupdate/history.go)、[manager.go](../../../../internal/platformupdate/manager.go)、[clusterguard-update-job.sh](../../../../scripts/clusterguard-update-job.sh)、[clusterguard-upgrade.sh](../../../../scripts/clusterguard-upgrade.sh) |
| 回归 | [history_test.go](../../../../internal/platformupdate/history_test.go)、[manager_test.go](../../../../internal/platformupdate/manager_test.go) |

## 需要时再读

- [upgrade-validation-chain](../../../zh-CN/upgrade-validation-chain.md)
- [upgrade-validation-chain-implementation-status](../../../zh-CN/upgrade-validation-chain-implementation-status.md)
