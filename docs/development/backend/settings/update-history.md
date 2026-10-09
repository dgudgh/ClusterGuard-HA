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

## 传输与节点更新进度

`Manager.Job` → `deriveJobProgress` → `progressPercent`将同一operation_id的status/events投影为页面进度。Runner在每个热修节点前发出`staging`，current是已完成节点数；它与`updating`共用节点步骤的百分比计算，避免传输下一节点时走未知阶段的0%分支。只修改读取投影，原始状态和事件保持不变，完成仍须以终态及健康核验为准。

[三节点持久化回归](../../../../internal/platformupdate/progress_staging_test.go)实际读取每一步status/events，覆盖准备、三次传输、节点更新、集群验证、最终成功；可将实际投影供[浏览器回归](../../../../tools/console-update-staging-progress-acceptance.cjs)使用。跨操作隔离仍由operation_id约束。

## 操作版本与RPM准入基线

Package.source_version仍是签名RPM兼容基线。Runner在维护锁建立后、载荷替换前通过各节点`clusterguard --version-json`观测已安装二进制的产品身份，写入`Job.from_node_versions`；全节点可读且一致才写`from_version`。`to_version`来自签名应用目标；热修回退的备份产品版本未取证时不猜成RPM来源；核验回退完成后重新观测恢复版本，保留原from字段，记录实际to字段（含自动回退）。观测失败不代替或绕过原有签名、准入与健康门禁。

Runner将字段写入同operation_id的status/events/operations；wrapper终态仅保留同操作字段，并复制至其他控制节点。Manager按原样读取，重启不丢失。首次安装时已运行的旧wrapper可能丢弃新字段；Manager仅从同operation_id/patch_id/mode的Runner追加快照恢复读投影，不重写旧文件、不串用其他操作。旧记录没有观测则保持缺失，不用当前二进制身份或邻近历史补写。旧签名包和原日志不改。

验证入口：[真实Runner与wrapper回归](../../../../scripts/operation_version_test.go)、[Manager重启/JSON往返](../../../../internal/platformupdate/operation_version_test.go)、[浏览器版本变化](../../../../tools/console-update-version-transition-acceptance.cjs)。
