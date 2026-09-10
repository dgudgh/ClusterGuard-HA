# ClusterGuard HA 2.2-88 修复与现场验收

日期：2026-09-08，时间为 CST。由当前代理完成，没有委派其他模型。源码工作树：`clusterguard-ha/.worktrees/platform-auth-session`，未提交或推送。

## 当前状态

2026-09-08 15:52 CST：本报告列明的修复和现场验收完成，三台均为 2.2-88。MySQL `.153` Primary、`.152/.154` Replica；PG `.154` Primary、`.152/.153` Standby；两种集群均 healthy、两条复制链路、lag=0。

真实 Chrome 签名上传/滚动升级、双 VIP 升级连续性、两种数据库分别全停恢复、原生数据与至少 50 个不同后台周期均通过。88 与 87 程序修复代码相同，仅更新发布版本和构建信息。验收边界见文末，不代表任意部署形态或故障均已演练。

## 关键修复

- PG Discovery 使用原生身份、注册端点和 sender/receiver 证据建边，不因平台 ID 与 engine ID 不同而猜测、丢弃或伪造复制关系。上游身份 GUC 为空或残留旧值时，也必须按真实证据解析；证据冲突仍降级。
- PG 已有数据目录不再被旧 Bootstrap Env 改写运行期主库。现场 Swarm 的入口脚本 Config 引用已单独更新，保留旧 Env 做回归。
- MySQL 和 PG 一键灾难恢复实现全节点隔离、原生事务比较、安全选主、从库重建、严格验证、Recovery Commit 和唯一入口验证。PG 检查 timeline/WAL/独立 COMMIT/PREPARE，MySQL 检查 GTID/relay/XA；不能仅以最大 LSN 或“当前只有一个主库”排除历史分叉。
- 升级维护不再拒绝已验签 Agent 的合法多数派续租。错误签名、失去 quorum、过期许可仍拒绝，普通业务变更仍受维护门禁约束。停库 fencing 使用 mutation 预算，父上下文取消和短期授权仍优先。
- 完成恢复时，在同一 discovery publication fence 内完成新观察与严格提交，消除远程入口检查后的并发发布间隙。历史 unplanned 与当前运行/事故恢复状态分开，失败不能伪装成成功。
- 恢复 HBA 从已验证 ControllerURLs 派生精确 IP，仅补管理用户 SCRAM 规则，解决主库与 Raft Leader 同机时漏放本机管理探测。不放行整段网段或任意业务用户。旧冻结任务仅能迁移字节与摘要匹配的旧保护文件，保留原 HBA 和任务身份。
- 密码、token、passfile、SCRAM 与私钥材料在已有 API/CLI/日志/事件/审计/报告出口脱敏。已暴露的 PG 复制密码完成旋转，私有回执不随交付包发布。
- 操作页面三个标签同排等宽且可切换，说明另行显示。升级弹窗初始无旧包信息，校验完成后启用按钮；告警在弹窗顶部，圆形步骤、固定摘要、日志独立滚动和默认折叠保留。
- 默认三个版本只清理安装和回退材料，不删审计/升级事件；活动和待复核失败任务额外保护。早期已删除日志不能凭空恢复，不伪造补录。

## 验收记录

| 项目 | 当前结果 |
| --- | --- |
| 全仓测试 | `go test -p 2 ./... -count=1` 通过，scripts 241.199 秒；与 88 程序代码相同 |
| 竞态与静态检查 | agent/runtime/disaster/api 竞态、vet、diff 检查通过；86 的 discovery/store publication fence 并发回归通过 |
| 原生 PG 16 | 八项正反例通过，22.688 秒，包括独立 COMMIT、缺 WAL、PREPARE、真实隔离与两台从库重建 |
| PG 原始拓扑 P0 | 原生三节点 50 个不同后台周期通过，7.926 秒；空/旧 upstream GUC 未被改写；暂停回放后正确降级并撤销对应健康边 |
| 原生 MySQL 隔离场景 | 三节点 GTID/relay、guarded clone、executor 重建此前通过；不替代 88 现场恢复 |
| UI 回归 | 实际 HTML 加模拟 API，1440/1024/768/390 视口切换、确认、防重复提交、重开持久任务、独立滚动、失败状态通过 |
| Chrome 87→88 | 15:32:54 上传校验，15:34:09 开始，15:36:14 三台成功、10 个事件、维护门禁释放；未使用直接安装 RPM 替代 |
| 双 VIP 升级连续性 | 15:33:44 至 15:36:44，408 次采样、0 失败；六个容器 ID/启动时间、主从和唯一 VIP 均不变。采样不等价于高负载压测或任意亚秒瞬断证明 |
| 88 PG 全停恢复 | 任务 `a6cb77f9-fae7-41ea-9b39-2bbf5f02b9f3`：15:39:10 开始，15:41:55 Recovery Commit，15:42:05 成功，无重试；保护生效时本机管理连接与非管理 HBA 拒绝均通过 |
| 88 MySQL 全停恢复 | 任务 `cf25a8d5-3186-4b4f-982b-7d71448a963e`：15:44:15 开始，15:49:43 Recovery Commit，15:49:57 成功，无重试；停机前 canary `3e01c115-3eb2-404d-aada-2b0d7f450672` 在恢复后三台仍各一条 |
| 88 原生数据与 50 周期 | PG 原生 canary/身份/槽/streaming/新凭据/VIP 通过；15:42:38.826617232 至 15:43:48.890331734 的 50 周期全健康；15:47:43.243769574 至 15:48:50.134435725 再测 50 周期亦通过。MySQL 原生 UUID/GTID/线程/只读/canary 通过；15:50:58.523606829 至 15:52:06.921048461 的 50 周期全健康。各轮均为不同实际后台观察、一主两从、两条 links、lag=0 |
| 恢复最终状态 | 两种 Recovery Commit 均为 running、incident 已恢复；无 freeze、maintenance、automatic-failover suppression；当前启停 normal。历史 unplanned 保留。15:51:59 至 15:52:04 再经两个 VIP 查询 14 次，无失败 |
| 安装材料与审计保留 | 三台均为 88；94 个预存审计文件 SHA-256/大小不变，6 个旧 payload 目录新清理；升级历史 11 条，不截断为三条 |

87 的失败任务 `e9f7b005-e459-4b9c-aea7-37d8c4e1b6b7` 于 15:27:44 从 Chrome 重新执行，15:29:18 选出 `.154`，15:30:38 Recovery Commit，15:30:51 成功。旧失败事件保留。原生 SQL 证实三台 canary 各一条、两台 streaming、两个 active 物理槽、新复制凭据和唯一 VIP `.157` 正常。

87 的专用现场探针先遇 Python 3.6 参数兼容、再遇 PostgreSQL inet 文本带掩码的断言问题；修正后隔离窗口已结束，因此未把该探针记为通过。88 新恢复任务重新执行该探针，实际 `source=192.168.102.154`、Primary、管理连接成功、非管理 HBA 拒绝、保护摘要一致均通过，证据为 `pg-colocated-guard-88.jsonl`。

MySQL 三台 GTID 完全相同：`14158b19-a162-11f1-9ab2-02420a210103:1-14,142250a0-a162-11f1-9a47-02420a210102:1-24`。三台 server UUID 保留；主库 RO/SRO=0，两台从库 RO/SRO=1、IO/SQL=ON，复制错误为 0，offline_mode 均解除。PG 原生 node ID 不变，两台物理槽 active、application_name 与原生身份匹配，复制源均为 `.154`。当前主库由原生证据选择，不写死早期环境中的 pg02 或 Bootstrap Primary。

脱敏专项回归再次通过，覆盖结构化/数字 secret、嵌套 primary_conninfo、SQL PASSWORD、SCRAM/私钥、CLI 历史响应、API 错误、恢复事件及审计/报告历史读取。没有打印或发布现场私有凭据回执。隔离原生 MySQL 测试容器已确认无残留。

## 安装材料

正式目录：`/Users/zhaolongjie/codex/clusterguard-ha/release/2.2-88`。由已验收候选文件逐字节复制，不覆盖或重打同一个补丁 ID。原候选目录保留用于追溯。

| 文件 | 用途 | SHA-256 |
| --- | --- | --- |
| `clusterguard-ha-2.2-87_to_2.2-88.x86_64.cgupgrade` | 2.2-87 到 2.2-88 的签名升级包，内含回退与引导器 | `6230131fdfaa3ea35a4dcd5fe27b39c69d54ebc7677911f4107095d972764d54` |
| `clusterguard-ha-2.2-88.x86_64.rpm` | 完整 Linux x86_64 RPM；不是浏览器签名升级包 | `0cfb2dac0326b501e887bdffaa1eead6b2854e070cf6ecf7552589752ee2f7cb` |

升级包为 36,704,864 bytes，构建于 15:32:18；RPM 为 18,388,477 bytes，构建于 15:31:50。两者均有独立 SHA-256 文件，升级包内不含私钥或现场配置凭据。

## 证据与边界

原始无凭据证据位于工作树 `.build/recovery-field-20260908/`，交付目录的 `evidence/` 包含本版原生核对、周期、连续性、保留和恢复 trace 的明确白名单副本。历史失败与修复过程见 [修复记录](mysql-postgresql-disaster-recovery-progress-2026-09-07.md)，今后逐项执行 [发布验收清单](release-recovery-acceptance-checklist.md)。

现场范围为授权的 `.152/.153/.154`，Rocky Linux 8、Docker Swarm、MySQL 8.0.44、PostgreSQL 16.4。未执行物理断电、真实网络分区或业务峰值压力演练；没有 PXC/Galera 现场集群。仓库没有独立 support-bundle 实现，不能声称验收了不存在的导出功能。
