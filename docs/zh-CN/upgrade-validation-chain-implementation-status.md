# 升级热修统一校验链 v2：实现与验收状态

> **文档性质：历史实现/验收快照。** 下方结果对应所标日期和 `7b643f4` 修复基线，不是当前现场状态或现行门禁定义。当前判定使用[门禁流程](validation-gate-workflow.md)、本次绑定摘要、相应严格阶段及真实验收报告；旧结果不能覆盖本次未执行项。

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

## 2026-10-08 增补：载荷已落地是单一判据

> 本节记录 2026-10-08 的源码修复与落点，**不修改契约规则**。下列判定都是既有条款的执行，不是新条款：§6「创建 job 前 `validate not superseded`」、§INV-009「服务端重校验 `superseded status`」、§21 `history.deployment_success_overwrite_forbidden` 与 `console.subject_equals_action_target`、§8 回退契约与 §16 UI-005。

| 范围 | 落点 | 证据 |
| --- | --- | --- |
| §6 / INV-009 supersede 守卫必须有 deployment 之外的输入源 | `scripts/clusterguard-upgrade.sh`（`payload_applied`、`assert_not_superseded`）、`internal/platformupdate/supersedes.go`（`successorInstalled`）、`internal/platformupdate/history.go`（`payloadInstalled`） | 三台现场的真实归档、`package.json`、`status.json`、`events.jsonl` 搭出守卫输入后跑真实脚本：改动前的守卫**静默通过**（该根下 `deployment.json` 数量为 0，守卫不是失败而是从不运行），改动后具名拒绝 `HF-2026-0929-05` 与 `HF-2026-0929-04`；反向对照（无后继的 `HF-2026-1008-01` 自身）走到后续检查、未被误拦。Go 单测 `TestAppliedSuccessorBlocksPredecessorWithoutAnyDeploymentRecord`、`TestRejectedAttemptOverVerifiedSuccessCountsAsApplied`、`TestSucceededRollingPackageIsNotABlockingSuccessor` 与脚本级 `TestRunnerBlocksSupersededPredecessorWithoutAnyDeploymentRecord`（真实签名包跑真实脚本） |
| INV-004 / §21 载荷在盘上由**两个来源之一**判定 | `internal/api/console.html`（`softwareUpdatePayloadApplied`）、`scripts/clusterguard-upgrade.sh`（`payload_applied`） | `deployment.json == installed`，或该包自己的 operation 记录证明载荷已落地（`status == succeeded`，或 `status == failed` 而最后一条已完成事件是 `succeeded`）。被随后被拒的 `resume` 覆盖的 `status=failed` 记录据此仍判为已生效；真实部署记录（如 `rolled_back`）优先于事件链 |
| §5 / UI-005 回退只能命名最新已应用记录 | `internal/api/console.html`（`softwareUpdateRollbackTarget`） | 真浏览器 8 场景 / 80 断言全通过；已被新补丁替换的记录不提供回退按钮，若点击则具名拒绝并说明「回退只替换它自己携带的文件」。**面板不用时间戳推断取代关系**——取代由服务端按签名声明裁决，面板只在载荷已在盘上时不提供动作 |
| §12 门禁 | `tools/verify-upgrade-validation-chain.cjs` | 21 项检查通过；`--self-test` 21 个变异全部被捕获，no-bite 与 comment-only 两个控制行为正确 |

### 2026-10-08 新增未决

- `history.supersede_declaration_not_persisted_in_package_json`：现场在役包由早于该字段的构建器产出，`package.json` 里没有 `supersedes`，取而代之的是**重新 `--inspect` 归档**读取签名声明。于是「谁被谁替代」不落在包内的持久记录里，只落在签名清单与重新验签的结果里。
- 上述判定目前是既有条款的执行，**未**升契约版本。若要把它们写成规范条款，须按 §23 走一次完整的版本递增：升 `contract.version`、更新第 21 节机器可读块与 `internal/updatecontract/schema.json`、嵌入副本、`SupportedVersion`、消费方声明与回归测试。本节据此只记录落点与证据，不代替该决定。
