# ClusterGuard HA — 集群下电与自动恢复（Power Lifecycle Management）测试报告

- 生产候选版本：`1.0.0-rc.20260809.13`
- 日期：2026-08-09
- 范围：状态机 + 存储 + API + 真实 agent 关机 + 自动恢复 + 多引擎 + 控制台 + 报告 + 端到端测试
- 结果：**全仓 `go test ./...`、关键包 race、`go build ./...`、`go vet ./...`、`git diff --check` 和生命周期脚本 `bash -n` 全部通过；152–154 真实一键停库、控制面独立重启、三机同时重启、突发主库故障、自动接管和旧主回挂全部通过**

## 生产上线前专项复验（`1.0.0-rc.20260809.13`）

### 计划停库与突发故障识别

- 计划停库由持久化 `PowerOperation`、Recovery Freeze 和实例 Maintenance 共同标识；状态接口返回 `planned_shutdown`、操作者、请求时间、数据库状态以及 `automatic_failover_suppressed=true`。
- 未存在计划停库而主库健康失败时返回 `unexpected_failure`，自动接管保持启用，不会把人为停库误判为故障，也不会把突发故障误判为维护。
- 修复了最新失败 lifecycle 的保护状态读取：失败操作只要仍有 Freeze/Maintenance 就继续显示受保护，直到后续已完成 lifecycle 明确取代它。
- 控制台显示“停机判定 / 数据库状态 / 自动接管 / 管理控制面”四项事实；弹窗默认“停止数据库服务”，明确提示 ClusterGuard 控制面保持运行。

### 真实故障时间线（北京时间）

| 时间 | 动作 | 结果 |
|---|---|---|
| 16:05 | 从控制台执行 `mysql-ha-3306` 一键停库（service 模式） | 三个 3306 实例停止、VIP `.155` 释放；Power 状态为计划停库，自动接管冻结 |
| 停库期间 | 检查 ClusterGuard 与另一套 3384 集群 | 三个控制面均在线；3384 三实例和 VIP `.160` 不受影响 |
| 停库期间 | 向 `.154` 控制进程发送 SIGTERM | systemd 自动拉起，新 PID 生效，`NRestarts=1` |
| 16:08:10 | 三台操作系统同时重启 | 约 10 秒恢复 2/3 控制面，约 17 秒恢复 3/3；3306/3384、角色、复制和 VIP 自动恢复 |
| 16:10:12 | 绕过平台直接停止 3306 原主 `.154`，模拟突发故障 | 控制台判定 `unexpected_failure`，自动接管未冻结 |
| 16:10:51–16:11:14 | 自动故障切换 | `.152` 成为唯一可写主库并接管 VIP `.155`；从故障到验证完成约 62 秒 |
| 16:12:04 | 启动恢复后的旧主 `.154` | 保持 `read_only=1/super_read_only=1`、无 VIP，不会抢写 |
| 16:15:15–16:15:29 | 页面执行旧主回挂 | 操作 `succeeded` 且 Verification passed；`.154` 复制自 `.152`，IO/SQL=Yes/Yes、延迟 0 |

最终事实：`.152` 为唯一可写 3306 主库并唯一持有 VIP `.155`；`.153/.154` 均只读、复制线程运行、延迟 0。Raft Leader 为 `.153`，3 voters、quorum confirmed、无活动操作和 lifecycle task。

升级后又执行 60 秒稳定性观察（每 10 秒采样，共 7 次）：3306 与 3384 两套集群始终保持一主两从、健康副本和延迟 0，Raft commit/applied 一致。三台 `systemctl --failed` 均为空，restore/finalize 单元均为 `Result=success/ExecMainStatus=0`，reconcile timer 均为 enabled+active，控制面、Agent 和 MySQL 最近错误级日志为空。

### 本轮发现并修复

- 旧主回挂下拉曾因“历史上做过主库”或缺省线程字段，把健康从库错误列为恢复候选。现仅接受维护中、健康异常、复制线程明确异常，或历史主库且复制尚未恢复的实例。
- `1.0.0-rc.20260809.13` 部署后，健康拓扑下旧主回挂列表只显示“未发现可恢复节点”；候选主库列表仍正确显示两个健康从库。
- `clusterguard-ha.service` 使用 `Restart=always`，不依赖 MySQL systemd unit；数据库服务停止时管理控制面仍可独立运行和自恢复。

### 发布物

三台当前软链接均指向 `/opt/clusterguard/releases/1.0.0-rc.20260809.13`。最终离线包路径和 SHA-256 由构建完成后的外部交付清单记录，避免在归档自身包含的报告中形成自引用摘要。

> 本轮真实测试覆盖“仅停数据库服务”和“三台操作系统同时重启”。整机断电后的自动上电仍依赖 VMware、IPMI、BMC 或 BIOS，不属于已关机节点上的 ClusterGuard 进程能力，本轮没有伪装为已验证。

## 上一轮生产候选复验（2026-08-09）

### 修复内容

1. MySQL 停库在停止服务前，对所有节点执行 VIP 释放和本地隔离；任一节点隔离失败时不进入服务停止阶段。
2. 最新电源操作按更新时间、创建时间和资源 ID 稳定排序，避免 `/power/status` 随机返回旧操作。
3. 三控制节点同时开机期间，`power/boot-detected`、`power/recovering`、拓扑发现、`power/verify`、`power/complete` 和状态读取对 `000/500/502/503/504` 做有界重试。
4. follower HTTP 就绪但 Raft 日志尚未追平时，恢复脚本对 `prechecking/shutdown_planned/shutting_down` 只读等待，未确认 `power_off/boot_detected/recovering/verifying` 前禁止角色修改和恢复动作；无活动操作、非法状态和超时仍 fail-closed。
5. 恢复完成增加关机快照身份门禁：快照中的同一 `resource_id` 必须恢复为健康主库，全部快照从库也必须以健康 replica/standby 角色出现；即使另一节点已成为健康主库，也不得提前释放恢复冻结。

### 真实服务器结果

测试节点：`192.168.102.152`、`192.168.102.153`、`192.168.102.154`。

| 集群 | 主库 | 从库 | VIP | 结果 |
|---|---|---|---|---|
| `mysql-ha-3306`（MySQL 8.0.44） | 154:3306 | 152/153:3306 | `192.168.102.155` | 通过 |
| `mysql-test-8.4`（MySQL 8.4.10） | 153:3384 | 152/154:3384 | `192.168.102.160` | 通过 |

- 两套集群均通过非 Leader 的 152 控制台会话执行 `precheck -> plan -> execute`；execute 返回 HTTP 200、`power_off`、verification passed、recovery freeze active、3 个实例 maintenance active。
- 停库后 6 个 MySQL 服务全部 `inactive`，两个 VIP 在三台节点上均不存在，每台节点持久化两份独立集群快照。
- 三台同时重启后 boot ID 全部变化；控制面、Agent、reconcile timer、MySQL 3306 和 MySQL 3384 均自动启动且 enabled。
- restore/finalize 首次执行均为 `Result=success`、`ExecMainStatus=0`、`NRestarts=0`；153 在 Leader 选举窗口内遇到一次 HTTP 503 并在同次 oneshot 内成功重试，三台均无 unit 重启和 failure 状态。
- Raft 为 3 voters，Leader quorum confirmed，三节点 `state_revision=1068910`、`commit_index=applied_index=1283781`，无活动 operation/lifecycle task。
- 3306 主库 `read_only=0/super_read_only=0`，两个从库均为 `1/1`，receiver/applier 均 ON、错误码 0；3384 同样通过。
- VIP 直连返回 `orch-mysql03:3306:0:0` 和 `orch-mysql02:3384:0:0`，证明 VIP 只指向对应可写主库。
- 60 秒内每 10 秒采样一次，共 7 次：Raft、两套三节点拓扑、健康状态、单主约束、复制延迟 0、power lifecycle completed 和 VIP 可写入口全部 PASS。
- 三台 `systemctl --failed` 为空，MySQL 当前启动日志错误扫描干净。

### 发布物

```text
/tmp/clusterguard-release-v11-final-20260809/clusterguard-ha-1.0.0-rc.20260809.11-linux-amd64.tar.gz
SHA-256: f614cfb5d9b36230d9dbb701e2151c8e2e4c669fa2b2b83b8b7941b2250871e3
```

该轮测试时三台软链接均指向 `/opt/clusterguard/releases/1.0.0-rc.20260809.11`；当前部署版本以文档顶部的上线前专项复验为准。

> 验收范围是“控制台一键安全停止数据库集群 + 操作系统重启后自动启动和恢复”。机器彻底断电后自动上电属于 VMware/IPMI/服务器 BIOS 能力，不能由已关机的本机进程实现，本次未将其伪装为软件能力。

---

## 一、交付总览（按 Phase）

| Phase | 交付物 | 验证 |
|-------|--------|------|
| 1 | PowerState 状态机 + PowerOperation 模型 + 保护标记（RecoveryFreeze/Maintenance）+ power API 框架 + cgctl power 子命令 | 状态机测试 + API 测试 |
| 2 | agent 真实关机：`mysql_service_stop`/`mysql_service_start`/`mysql_power_status`/`node_poweroff` 命令 + LinuxPowerController + HMAC 签名 agent 请求 + 四阶段真实执行（persist → 停副本 → 停主库 → 可选整机下电） | agent + workflow + 集成测试 |
| 3 | （并入 Phase 2/4）agent 电源状态命令 + boot-detected 上报 | 同上 |
| 4 | 自动恢复：`clusterguard-cluster-restore.sh`（B4 调 power/boot-detected + power/recovering）+ `clusterguard-cluster-finalize.sh`（power/verify + power/complete 轮询 + stamp_recovered_at），保护释放原子化委托 Go 侧 | 脚本 `bash -n` + Go 集成测试覆盖等价路径 |
| 5 | 多引擎分派：PostgreSQL 跳过 persist_read_only、`postgresql_stop`/`postgresql_status`、engine-aware plan steps | workflow + API 测试 |
| 6 | Web 控制台：只读「电源生命周期」面板（状态徽章、保护标记、最近操作、历史表），中文标签，不处理审批 token（设计契约） | console 测试 |
| 7 | 报告：durable workflow 原子落盘 terminal report + audit；`/api/v1/reports/{id}` JSON + HTML | 集成测试断言（新增） |
| 8 | 端到端集成测试：3 个 Go 全栈场景（service 模式 / poweroff 模式 / agent 失败 fail-closed）+ 实验室 bash 脚本 `scripts/integration/power-lifecycle-lab.sh` | `go test` + `bash -n` |

---

## 二、全仓测试结果（go test ./... -count=1）

```
ok  	clusterguard.io/ha/cmd/cgctl	10.772s
ok  	clusterguard.io/ha/cmd/clusterguard	4.576s
ok  	clusterguard.io/ha/cmd/clusterguard-agent	1.160s
ok  	clusterguard.io/ha/internal/agent	3.486s
ok  	clusterguard.io/ha/internal/api	3.275s
ok  	clusterguard.io/ha/internal/approval	2.293s
ok  	clusterguard.io/ha/internal/auth	4.020s
ok  	clusterguard.io/ha/internal/config	4.776s
ok  	clusterguard.io/ha/internal/consensus	5.064s
?   	clusterguard.io/ha/internal/controlstate	[no test files]
ok  	clusterguard.io/ha/internal/coordination	3.917s
ok  	clusterguard.io/ha/internal/discovery	5.750s
ok  	clusterguard.io/ha/internal/endpoint	5.152s
ok  	clusterguard.io/ha/internal/lifecycle	5.120s
ok  	clusterguard.io/ha/internal/metrics	5.080s
ok  	clusterguard.io/ha/internal/observability	4.813s
ok  	clusterguard.io/ha/internal/recovery	4.995s
ok  	clusterguard.io/ha/internal/report	4.939s
ok  	clusterguard.io/ha/internal/runtime	5.632s
ok  	clusterguard.io/ha/internal/store	6.768s
ok  	clusterguard.io/ha/internal/workflow	5.319s
ok  	clusterguard.io/ha/pkg/adapter	5.368s
ok  	clusterguard.io/ha/pkg/identity	5.288s
ok  	clusterguard.io/ha/pkg/model	5.531s
ok  	clusterguard.io/ha/scripts	17.895s
```

关键包（agent / workflow / api / endpoint / cgctl）共 **495 个用例全部 PASS，0 FAIL**。

---

## 三、Power 生命周期专项测试矩阵

### 3.1 agent 端（internal/agent）
| 测试 | 断言 |
|------|------|
| TestPowerControllerStopServiceRunsSystemctl / StartServiceRunsSystemctl / PowerOffRunsSystemctl | 精确命令（systemctl stop/start/poweroff）+ stub runner |
| TestPowerControllerServiceStatusParsesIsActive / ReportsStoppedService | is-active 输出解析，stopped → ServiceRunning=false |
| TestPowerControllerRejectsMissingServiceName | 未配置服务名 fail-closed |
| TestAgentPowerCommandsDispatchToController | Handle 分发 4 个新命令 |
| TestAgentPowerCommandsBlockWithoutController | 未装配 controller → 拒绝（fail-closed） |
| TestAgentPowerMutationRequiresPlanDigest | mutation（stop/start/poweroff）强制 sha256 plan digest |

### 3.2 workflow 端（internal/workflow，PowerShutdownAdapter）
| 测试 | 断言 |
|------|------|
| TestPowerShutdownAdapterBuildPlan / BuildPlanPostgreSQLSkipsPersist | MySQL 5 步 / PostgreSQL 4 步（无 persist_read_only，重编号） |
| TestPowerShutdownAdapterExecuteAppliesProtectionsAndAdvancesState | 保护 + 状态转移 + nil transport 降级 |
| TestPowerShutdownAdapterExecuteRunsAgentShutdown | 四阶段真实执行：persist×3 → 副本×2 → 主库最后 |
| TestPowerShutdownAdapterExecuteAgentFailureKeepsProtections | agent 失败 → 停留在 shutting_down，保护保持 |
| TestPowerShutdownAdapterExecutePoweroffModePowersOffNodes | node_poweroff 在全部 stop 之后 |
| TestPowerShutdownAdapterExecuteRejectsUnplannedState | 非 shutdown_planned 拒绝 |
| TestPowerShutdownAdapterExecutePostgreSQLEngine | 无 persist_role；postgresql_stop×2 先副本后主库；engine=postgresql |
| TestPowerShutdownAdapterVerify / VerifyUsesAgentStatus / VerifyPoweroffModeAcceptsUnreachable / VerifyUsesEngineStatusCommand | agent 状态校验；poweroff 模式下不可达视为通过；PG 用 postgresql_status |
| TestPowerShutdownAdapterPrecheckRequiresShutdownPlanned / EngineAndCapabilities | 前置条件 + 引擎能力声明 |

### 3.3 API 端（internal/api）
| 测试 | 断言 |
|------|------|
| TestPowerLifecycleEndToEnd | 完整状态机流（cancel 路径在内） |
| TestPowerExecuteRequiresApprovalAndPlannedState | 审批 + 状态门 |
| TestPowerFailKeepsProtectionsActive | fail 后保护保持 |
| TestPowerPrecheckUnknownCluster | 未知集群 404 |
| TestConsoleShowsPowerLifecyclePanel | 只读面板契约：电源生命周期 / power-state-badge / power-protection / power-history / powerStateLabels / loadPowerStatus(clusterId) / power/status fetch / state.powerStatus；**不含 approval-token（设计契约 TestConsoleOperationDoesNotHandleApprovalToken 同时通过）** |

### 3.4 端到端集成测试（internal/api/power_lifecycle_integration_test.go，新增）
真实 HTTP 表面 + 记录型 fake agent transport + durable workflow：

**TestPowerLifecycleIntegrationFullShutdownAndRecovery**（service 模式全流程）
- persist_role×3 → mysql_service_stop×3（副本先于主库，主库最后）→ 无 node_poweroff
- 每个 agent 请求：签名、OperationID、`sha256:` plan digest、engine=mysql
- execute 后：RecoveryFreeze + 3 实例 Maintenance 全部激活；状态 power_off
- boot-detected → recovering → verify → complete：保护全部释放，状态 completed
- **Phase 7 断言（新增）**：durable 流程落盘 terminal report（status=succeeded），`GET /api/v1/reports/{id}` 返回 audits 非空，`/html` 返回 200

**TestPowerLifecycleIntegrationPoweroffMode**
- node_poweroff×3，且每个都在最后一次 mysql_service_stop 之后
- 停机后 mysql_power_status transport 失败（"ssh: host is down"）→ verify 视为通过
- 状态 power_off

**TestPowerLifecycleIntegrationAgentFailureIsFailClosed**
- 第一个副本 mysql_service_stop 失败 → execute 409（error）
- 状态停留在 shutting_down；RecoveryFreeze 保持；只有 1 个 stop 被发送（fail-fast）
- power/fail → failed（终态）；complete 409 拒绝
- 新的 precheck → 200 prechecking，**blocking_reasons 非空**（存活保护如实报告）

---

## 四、集成测试发现并修复的真实缺陷

| # | 缺陷 | 根因 | 修复 |
|---|------|------|------|
| 1 | durable post-execute verify 总是失败 → execute 返回 500 indeterminate | `powerFrozenInstance` 要求 `frozen.MetadataRevision == instance.MetadataRevision`，而 `ApplyPowerProtections → SetMaintenance` 会 bump 每个实例的 revision | 移除 revision 相等性检查；身份坐标（hostname/IP/engine/port）仍捕获漂移 |
| 2 | 集成测试 execute 500 | 测试 fixture resolver 未设置 `OperationID`（生产代码 `internal/workflow/resolver.go:251` 会设置 `request.Resolved.OperationID = request.Operation.ResourceID`） | 在 `newPowerIntegrationServer` 的 resolver 中补上 |

两个缺陷都在 Go 集成测试（非 mock 单测）层面暴露 —— 证明全栈测试的价值。

---

## 五、自动恢复脚本（Phase 4）

| 脚本 | 关键改动 | 校验 |
|------|----------|------|
| `scripts/clusterguard-cluster-restore.sh` | B4 改调 power/boot-detected → power/recovering（容忍 409，幂等）→ discover | `bash -n` 干净 |
| `scripts/clusterguard-cluster-finalize.sh` | F4 走 power/verify → power/complete 轮询（0=完成 / 2=lifecycle 失败→保护保持 / 1=继续轮询）；stamp_recovered_at；F5 CRITICAL 路径保留 fail-closed 说明 | `bash -n` 干净 |
| `scripts/integration/power-lifecycle-lab.sh` | **新增**：L1–L6 端到端实验室验收脚本（见下） | `bash -n` 干净 |

设计要点：
- B3 的本地 read_only 持久化清除保留（节点本地、控制面不可用时仍健壮）
- 保护释放统一委托 Go 侧 `powerComplete` 原子释放（不再逐节点裸 curl）
- verify 幂等（200/409 容忍）；complete 阻塞时轮询直至超时，fail-closed

---

## 六、实验室运行清单（152–154）

### 6.1 真实运行结果（2026-08-07 UTC，152–154 ✅ 已执行）

在真实三节点实验室完成完整验收：cluster `8785b695-40f3-4892-825d-3f784a6ca95d`（mysql-ha-3306，engine=mysql），节点 orch-mysql01/02/03（192.168.102.152/153/154），VIP 192.168.102.155，控制面 leader 152（`https://192.168.102.152:3000`，TLS + Bearer token）。模式：service。时间均为节点 UTC。

**运行时间线（UTC）**

| 时间 | 步骤 | 结果 |
|------|------|------|
| 08:10 | precheck → plan → approval → execute（首次） | plan 快照 primary=154（8b52abb7）、replicas=152/153、VIP=192.168.102.155、5 步 plan |
| 09:33 | **fail-closed 实证**：153/154 agent 为旧版（无 `mysql_service_stop`）→ execute 停在 shutting_down | frozen=true protected=true 如实保持；`power/fail` → failed（op 07680241）；report `7e0671e2` = **failed** "agent command is unsupported" |
| 11:29 | 部署新 agent（md5 19954414…）后重试（op aaedb032） | plan 快照无 primary（观察期集群无主）→ execute 409 "power snapshot has no primary"（快照完整性门）；`power/fail` 终结 |
| 11:42–11:50 | 干净 lifecycle（op c5d1515d）：fail → precheck → plan → approval → execute | precheck 200（**blocking_reasons=1**，残留保护如实报告）；plan 200 快照 primary=154；approval **201**（token 85 字符，TTL 5 分钟）；**execute 200 → power_off**，frozen=true protected=true |
| 11:5x | L4 节点验证 | **三节点 `systemctl is-active mysqld` 全部 inactive**（真实关机完成） |
| 12:0x | L5 恢复：三节点 start mysqld → 154（primary）清 PERSIST_ONLY read_only（B3 SQL）→ boot-detected → recovering → verify → complete | boot-detected 200 → recovering 200 → verify 200 → complete 200 → **completed**（"recovery verified; protections released"）；重复调用 409 幂等容忍 |
| 12:05 | L6 最终断言 + 报告 | **frozen=false protected=false**（保护完全释放）；cluster health=healthy；report `05e07164` = **succeeded** "operation completed and verified" |

**关键实证**
- **fail-closed 完整闭环**：agent 步骤失败 → 保护保持（frozen+protected）→ `power/fail` → 新 precheck 200 + blocking_reasons 非空（存活保护如实呈现）→ 新 lifecycle 重试成功
- **审批 gating**：approval 签发 201 + token（5 分钟 TTL）；execute 校验 token 与 cluster/engine/kind/target 匹配
- **快照完整性门**：plan 快照必须含 primary，否则 execute 409（防止对无主快照执行关机）
- **恢复幂等**：boot-detected / recovering 重复调用 409 容忍（多节点并发上报安全）
- **报告联动**：complete 自动落盘 terminal report（succeeded），`GET /api/v1/reports/{id}` 可查（audits 非空）

### 6.2 端到端验收（脚本驱动）

```bash
# 前置：控制面 3000 端口、集群已注册且健康、节点已部署 agent（agent.json 含 MySQLService）
export CG_CONTROL_TOKEN='...'                # 控制 token（环境变量，绝不写入仓库）
export CG_LAB_SSH_TARGETS="root@192.168.102.152 root@192.168.102.153 root@192.168.102.154"
bash scripts/integration/power-lifecycle-lab.sh service     # service 模式全流程
bash scripts/integration/power-lifecycle-lab.sh poweroff    # 整机下电模式（节点需能远端重启）
```

脚本断言（全部通过才 exit 0）：
- L1 集群 healthy → L2 precheck/plan → shutdown_planned
- L3 审批 + execute → power_off 且 recovery_freeze=true
- L4 SSH 探测：service 模式所有节点 MySQL 停止 / poweroff 模式节点不可达
- L5 重启节点 → boot-detected → recovering → verify → complete → completed
- L6 保护全部释放（frozen=false、protected=false）

> 注：6.1 的完整生命周期（L1–L6 等价路径）已在真实 152–154 上手动执行通过；6.2 脚本为可重复的自动化验收入口。

### 6.3 真实整机关机与 VMware 带外启动（2026-08-09，152–154 ✅ 已执行）

本轮执行了 `poweroff`，不是数据库服务停机模拟。3306 发起整机关机前，平台先将同宿主的 MySQL 8.4、UPSQL 2.3 和 PostgreSQL 16 依次纳入计划停机；随后三台 Linux 虚拟机全部关闭。VMware 主机为 `192.168.102.68`，只操作以下三个已确认的 VMX：

- `G:\ibfluxdb+es\influxdb02\influxdb02.vmx`（192.168.102.152）
- `G:\ibfluxdb+es\influxdb03\influxdb03.vmx`（192.168.102.153）
- `G:\ibfluxdb+es\influxdb04\orch-db03.vmx`（192.168.102.154）

**现场结果（Asia/Shanghai）**

| 时间 | 验证 | 结果 |
|------|------|------|
| 17:46–17:47 | Safety Guard、Lock、Approval、Execute、Verify、Audit、Report | workflow operation `138a0235-0c08-49ef-a023-556e09452f9d` succeeded；12 个阶段审计齐全；report `6479e2b7-4eda-43cb-992b-f3378ff7f382` succeeded |
| 17:47 | 延迟整机关机 | 三台 SSH、3000 和数据库端口全部不可达；三个目标 VMX 从 `vmrun list` 消失 |
| 17:50 | VMware 带外启动 | 确认目标 VM 无运行进程后，清除三个遗留 `.vmx.lck`；仅启动上述三个 VMX，未操作其他虚拟机 |
| 17:51:10 / 17:51:21 / 17:51:29 | 系统启动 | 152、153、154 分别启动；`clusterguard-ha`、Agent 和 reconcile timer 均自动恢复 |
| 18:10 | 恢复收口 | power operation `81f78b12-a6ee-4b84-8d85-8e8cd0d809e9` → completed；`protected=false`、`recovery_freeze=false`、maintenance 为空 |

**本轮发现并修复**

1. MySQL 3306 的一台从库已经追平，但因不是当前半同步 ACK 节点而显示 degraded，导致恢复验证卡住。现在仅在主库半同步确认数满足、复制线程正常、延迟为 0、来源一致且 GTID 完全相同时允许恢复收口；GTID 少一笔仍会阻断。
2. UPSQL 2.3（MySQL 5.7.23 分支）不支持 `SET PERSIST_ONLY`。恢复脚本现在先独立解除运行时只读，再按服务端主版本决定是否清除持久化只读；5.7 跳过持久化语法，8.x 继续执行。
3. 停机状态分类复用同一套严格半同步证据，不再把安全的备用 ACK 空闲状态误报成 `unexpected_failure`。

**恢复后的数据库与接入验证**

| 集群 | 主库 | 从库 | VIP | 结果 |
|------|------|------|-----|------|
| MySQL 8.0 / 3306 | 152，可写 | 153/154，只读，IO/SQL running，lag 0 | 192.168.102.155，仅 152 持有 | 通过；备用 ACK 节点保持可见 degraded 提示，但停机分类 normal/running |
| MySQL 8.4 / 3384 | 153，可写 | 152/154，只读，IO/SQL running，lag 0 | 192.168.102.160，仅 153 持有 | healthy |
| UPSQL 2.3 / 3360 | 152，可写 | 153/154，只读，IO/SQL running，lag 0 | 192.168.102.165，仅 152 持有 | healthy |
| PostgreSQL 16 / 5432 | 152，primary | 153/154，streaming standby，WAL receive=replay | 192.168.102.166，仅 152 持有 | healthy |

四个 VIP 的服务端口均从客户端实际建立 TCP 连接。三台机器的 3000/3306/3360/3384/5432 端口均监听；systemd failed unit 数均为 0。最终 Raft 为 3 voters、quorum confirmed、mutation authority=true、ready=true。

### 6.4 手工抽查项
| 项 | 命令 |
|----|------|
| 节点 read_only 已持久化 | `ssh 152 cat /var/lib/mysql/mysqld-auto.cnf \| grep read_only` |
| agent 命令直测 | `ssh 152 "/usr/local/libexec/clusterguard-agent-stdio --config /etc/clusterguard/agent.json" <<< '{"command":"mysql_power_status",...}'` |
| 报告查看 | `curl -sk -H "Authorization: Bearer $CG_CONTROL_TOKEN" https://127.0.0.1:3000/api/v1/reports | jq` → 取 report id → `.../reports/{id}` JSON / `/html` |

### 6.5 故障演练
| 场景 | 预期 |
|------|------|
| 关机中 SSH 断开 | execute 409；状态 shutting_down；保护保持；`power/fail` → failed；新 precheck 报告 blocking reasons |
| 恢复中主库未就绪 | verify 不通过；complete 轮询不结束；保护不释放（fail-closed） |

---

## 七、fail-closed 保证（回归断言）

| 场景 | 行为 | 测试 |
|------|------|------|
| agent 步骤失败 | Execute 失败；停在 shutting_down；保护保持 | Adapter + 集成测试 |
| agent 未装配 controller | 命令被拒 | TestAgentPowerCommandsBlockWithoutController |
| transport 未配置 | 降级为仅保护标记（Phase 1 行为） | ExecuteAppliesProtectionsAndAdvancesState |
| power/fail 后 | 终态 failed；complete 409 | 集成测试 |
| 失败后新 lifecycle | precheck 200 + blocking_reasons 非空 | 集成测试 |
| 控制台 | 只读面板，不承载审批 | TestConsoleShowsPowerLifecyclePanel |

---

## 八、已知边界与后续

- ✅ **真实实验室验收已完成（任务 #19）**：service 模式完整生命周期（precheck → plan → approval → execute → power_off → 恢复 → verify → complete → completed → 保护释放）在 152–154 上全部通过，含 fail-closed 实证与报告生成，详见 6.1。
- Phase 3（周期性健康报备）按计划并入 Phase 2/4，无独立交付。
- ✅ **真实 poweroff 验收已完成**：三台虚拟机整机关机、VMware 带外启动、systemd 自启动、Raft 恢复、四套同宿主数据库恢复、VIP 唯一归属和保护释放均已实测，详见 6.3。
- 多引擎适配目前覆盖 MySQL + PostgreSQL；Oracle 停机不在本版本范围（broker 关机流程保留为后续）。
