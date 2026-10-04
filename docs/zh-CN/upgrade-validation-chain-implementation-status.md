# 升级热修统一校验链 v2：实现与验收状态

> 门禁执行更新（2026-10-04）：开发使用 `--stage source --strict`；新包与现场使用 `--stage artifact/field --strict --acceptance-report FILE`，需真实证据。默认仍为 field，当前 ART/FIELD 未完成；详情见[分阶段门禁与更新流程](validation-gate-workflow.md)。

更新：2026-10-03。强制契约为 `docs/upgrade-validation-chain.md`，与下载目录 v2 原文逐字一致。此文件记录实现状态，不修改契约规则。

## 已实现并在本地验证

| 范围 | 落点 | 证据 |
| --- | --- | --- |
| §21 严格契约加载 | `internal/updatecontract`、Manager、Helper、Runner、四个构建入口 | 缺失、未知、重复、嵌套错误、值弱化及不支持版本均拒绝；嵌入文本与两份规范一致 |
| §3/4 热修 retry 与滚动 resume 分离 | API、控制台、Helper、Runner | 热修 resume 拒绝；retry 全链路保留模式；真实点击请求绑定明确 patch_id |
| INV-004 部署状态与操作分离 | `deployment.json`、`requests.jsonl`、`operations.jsonl` | 成功后计划失败、Helper 启动失败、进程重启、遗留事故迁移、迟到运行快照与历史损坏测试 |
| 操作身份与响应不确定 | 客户端 operation_id、API、Manager 幂等处理 | 同一 ID 重启后只启动一次；真实按钮覆盖错误 ID、相同秒时间戳、503 后查询确认；不自动重提 |
| INV-005 同包门禁 | Runner 与 Raft Store | 外包、部分外包及混合持有者拒绝；同包执行可 CAS 接管 |
| 后端重校验与替代关系 | Inspector、Manager、Runner、签名清单 | 操作前重验签并比对存储摘要；真实签名 fixture 覆盖已安装替代包、无关包、已回退包和篡改包 |
| RB-001/002 回退保护 | 原有回退生成器与运行核验 | 生成后的实际脚本核验原子替换；注释不能充当实际 mv；保留包绑定及缺备份阻断 |
| Leader 切换私有历史 | Helper 私有历史导入 | 同操作终态可导入、日志保留；不同操作或冲突终态拒绝，不能用时间戳猜测替代关系 |
| 控制台安全回归 | node-safety、bootstrap、engine-pages、update confirmation、hotfix recovery | 本地真实 Chrome，隔离 API fixture；不属于原生数据库或生产升级验收 |

状态读取默认不携带全量操作历史；需要历史时显式访问包状态接口 `?history=1`。根目录历史文件持续保留，操作日志按追加写入；非法记录阻断新的动作。

## OPEN：不能作为升级包发布通过证据

**card: UPDATE-V2-FIELD-ACCEPTANCE**

- ART-001..013：本轮没有构建新的交付包，未取得新包全部签名、载荷、源脚本一致性、模式/属主/重启单元等完整证据。已有源门禁检查并不能替代新包验收。
- FIELD-001..012：没有核实本轮生产基线，也没有执行新包的现场上传、验签、滚动升级/热修、回退或跨 Leader 验收。三节点最终健康、独立部署历史及真实 UI 一致性尚未验收。
- 私有与复制历史属于不同操作时，当前实现拒绝执行并保留两份证据；不提供凭时间排序自动选择其中一份的路径。

`verify-upgrade-validation-chain.cjs --strict` 必须因这个 OPEN 退出非零。本地源码可提交，不能据此宣称新包可发布、已安装或已现场验证。

## 阶段

本轮产出为源码修复和本地验证；没有新构建、签名、标签、交付介质或生产安装。源码已提交并推送到唯一主线 `codex/2.2-postgresql`，实现提交为 `7b643f42ed11901c5d73f7703f3e3f176d053bb4`。禁止将本地测试通过写成“所有 bug 已消除”。

## 门禁结果与文档边界

2026-10-03 实现回归：源码门禁 17 PASS / 1 OPEN / 0 failed；strict 因 OPEN 退出 1；14 个变异被捕获；许可 68 PASS。详细执行记录见[修复记录](upgrade-hotfix-v2-repair-2026-10-03.md)。本次文档同步的再次执行结果见[同步记录](documentation-sync-2026-10-03.md)。

[热修台账](hotfix-patches.md)由已签名的既有制品生成，HF-2026-0930-01 等旧包的 resume 转 execute、plan→execute 描述是历史包行为，不能当作当前 v2 的恢复流程；没有为本轮源码修复重建这些包。运行环境须同时具备支持 `contract` 的 Helper 和匹配的 Manager/Runner。
