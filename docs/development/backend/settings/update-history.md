# 升级部署状态、操作历史与幂等

[后端](../README.md) → [设置接口](README.md) → [升级与热修](updates.md) → 历史

修改本功能前必须读[强制契约](../../../zh-CN/upgrade-validation-chain.md)，尤其部署结果与操作尝试分离、历史追加和身份绑定条款。

## 三种记录各回答什么

| 记录 | 职责 |
| --- | --- |
| `status.json` / Job | 最近一次任务的模式、进度、状态和 operation_id，可随该任务更新 |
| `deployment.json` | 当前部署状态及 last_verified_state / last_verified_operation_id，独立于最近一次尝试 |
| `requests.jsonl` / `operations.jsonl` | 受理与操作快照的追加历史，不用后续失败覆盖旧成功证据 |

API `GET /api/v1/platform/updates/{patch_id}` 返回包、当前 job、独立 deployment；只有 `?history=1` 才附操作历史。日常轮询不需要拉全量历史。

## 写入与读取链

`StartWithOperationID` → 先持久化受理身份 → 启动任务 → wrapper/Runner 更新任务与独立部署 → `Manager.Job/Deployment/Operations` → API 投影。

- queued/running、成功、失败、回退是操作状态；installed、applying、rollbacking、rolled_back、recovery_required 等是部署状态，不能只按同名字段互相覆盖。
- 新计划或 Helper 启动失败不能抹掉已核实安装；需要恢复判断时同时保留最后一次确证部署。
- 同 ID、同包、同模式在重启后仍返回已知操作；不同包或模式不能共享 ID。相同秒时间戳不是身份。
- 损坏历史阻断新的动作。追加文件采用受限目录和非符号链接访问；不能先清空损坏历史再继续执行。
- Leader 切换导入私有历史时，同操作终态可核对；不同操作身份或冲突终态拒绝并保留两份证据，不按时间戳猜一份权威。

## 排查顺序

1. 确认用户操作的 patch_id 和 operation_id，不从列表首行推断目标。
2. 对比 deployment、最新 job 与追加历史，区分已安装但本次尝试失败、真正未完成部署、回退失败。
3. 有 5xx/断连时查询同一操作；有历史冲突时保留原文件并调查，禁止自动重提。
4. API 已一致而页面显示不一致，再读[前端升级交互](../../frontend/settings/updates.md)。

## 定位与验证

持久化/兼容迁移看[history.go](../../../../internal/platformupdate/history.go)和[history_test.go](../../../../internal/platformupdate/history_test.go)；幂等受理与重启看[manager.go](../../../../internal/platformupdate/manager.go)和[manager_test.go](../../../../internal/platformupdate/manager_test.go)；跨 Leader 私有历史看[job wrapper](../../../../scripts/clusterguard-update-job.sh)。

[实现与验收状态](../../../zh-CN/upgrade-validation-chain-implementation-status.md)只记录其标注日期与源码基线下的历史快照，不能作为当前 HEAD 或当前现场的权威状态。当前判定必须重新输出当前证据绑定，执行[门禁流程](../../../zh-CN/validation-gate-workflow.md)规定的相应 strict 阶段，并核对本次制品或现场的实际验收报告；源码测试不代替现场历史一致性验收。

## 版本身份与旧记录替代

Snapshot从原包SHA与验签结果补读supersedes，仅已安装且兼容的签名后继输出superseded_by；旧失败status与原记录不改，活动任务不隐藏。
