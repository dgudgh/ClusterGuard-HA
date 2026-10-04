# 升级动作与执行保护

[后端](../README.md) → [设置接口](README.md) → [升级与热修](updates.md) → 动作

修改/执行本功能前必须读[强制契约](../../../zh-CN/upgrade-validation-chain.md)；涉及确认与授权时还须读[操作安全规则](../../rules/operation-safety.md)。

## 请求链路

`POST /api/v1/platform/updates/{patch_id}/{mode}` → `softwareUpdateRoute` → `startSoftwareUpdate` → `Manager.StartWithOperationID` → Helper → job wrapper → Runner。

- 通用认证、管理员/CSRF、Leader/quorum 等检查位于 `server.go` 和认证中间件，不在 Manager 中替代实现。
- API 先拒绝外包维护归属并记录审计，再传明确包 ID、confirmation、operation_id。非 plan 动作的 confirmation 必须等于包 ID。
- Manager 为新操作重验存储制品的签名、摘要、kind、来源/目标及当前基线，校验历史与已安装的签名 supersedes，再判断状态机。
- Helper 接收受限输入；Runner 在实际动作边界继续核对包绑定、锁归属、健康和多数派。前端按钮状态不是授权边界。

## 动作选择

| 动作 | 关键状态要求 |
| --- | --- |
| plan | 只读计划，不修改部署结果；不得与已有 queued/running 冲突 |
| execute | 当前任务已 planned；部署状态 installed 时拒绝重新安装 |
| retry | 仅失败热修，不能对已安装部署重试；保留独立 retry 模式 |
| resume | 仅失败滚动升级；热修直接拒绝，不转换成 execute |
| rollback | 包声明可回退，且当前任务为失败或成功；还须通过实际回退证据与健康核验 |

同包恢复可以按 CAS 核验执行身份；外包锁、混合锁和不确定归属不能接管。无关较新成功不能替代旧失败；替代只认已验签 supersedes 与确证安装。

## 失败与受理边界

400/404/409 等明确拒绝不等于已执行。入队后审计失败可能返回 503；断连、5xx 和超时必须按 operation_id 查持久化结果，不能自动重复提交。旧操作 ID 的同包同模式重放返回已知作业；ID 与动作不匹配拒绝。

## 定位与验证

- API 与错误映射：[updates.go](../../../../internal/api/updates.go)、[updates_test.go](../../../../internal/api/updates_test.go)。
- 状态机、签名与替代：[manager.go](../../../../internal/platformupdate/manager.go)、[manager_test.go](../../../../internal/platformupdate/manager_test.go)、[supersedes_test.go](../../../../internal/platformupdate/supersedes_test.go)。
- 受限启动：[helper.go](../../../../internal/platformupdate/helper.go)；实际执行：[Runner](../../../../scripts/clusterguard-upgrade.sh)。
- Raft 门禁：[software_update.go](../../../../internal/store/software_update.go)、[门禁测试](../../../../internal/store/software_update_test.go)。

只有确认框/按钮/请求目标有问题时读[前端升级交互](../../frontend/settings/updates.md)；历史或响应对账问题读[部署与操作历史](update-history.md)。本页列验证入口，不表示本次已执行现场测试。
