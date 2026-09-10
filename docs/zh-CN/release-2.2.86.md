# ClusterGuard HA 2.2-86 验收记录

日期：2026-09-08，时间均为 CST。当前由同一代理执行，没有委派其他模型。源码工作树为 `clusterguard-ha/.worktrees/platform-auth-session`；未提交、未推送，保留原有无关文件改动。

## 当前状态

86 不作为最终交付：控制升级、双数据库升级连续性、MySQL 全停恢复和 50 周期通过，但 PG 运行节点恢复于 15:06:00 被阻断，未完成 Recovery Commit。当时三台为 86，MySQL healthy，PG 保持恢复冻结且主库已重新隔离。本报告保留该失败快照；后续已修复并重新验收，最终现场状态见 [2.2-88 报告](release-2.2.88.md)。

### PG 最终验证失败

任务 `e9f7b005-e459-4b9c-aea7-37d8c4e1b6b7` 于 15:02:56 从 Chrome 开始；15:03:24 三台隔离完成，15:04:33 选出 `.154`，15:05:48 两台从库已重建。15:05:49 开始整体验证，15:06:00 返回 `recovery topology is incomplete: members=3/3 links=0/2`，正确阻断且未开放业务入口。

trace 表明主库持续 `database probe failed`，两台从库则为 streaming/lag=0 但缺失主库配对证据。此时 Raft Leader 为 `.154`，与 PG 主库同机；该机路由到本机 IP 的来源地址为 `.154`。恢复 HBA 只包含回环及另外两台 PostgreSQL peer IP，遗漏同机控制器的 `.154` 管理探测。正常配置中的 discovery 用户为 postgres，standby 上相同凭据的探测正常。停止后的主库容器已删除，未伪称保留了其原始 FATAL 日志。

新增真实 LoadConfig→HBA 回归先复现缺少同机控制器规则。87 将只从已验证的 ControllerURLs 提取精确 IP，增加管理用户的 SCRAM 规则；不增加任意业务用户、复制用户或整个网段的访问。旧冻结任务只允许迁移字节和摘要均匹配的旧限制性 HBA，保留原 HBA 与任务身份，不接受被改写的保护文件。

## 本次关键修复

1. PostgreSQL 拓扑按原生身份、sender/receiver 和注册端点证据关联，不把平台 ID 当成原生 ID，不根据“只有一个 Primary”猜测健康链路。原始第一卡断点在上游身份采集/建边之前，没有证据证明 Raft 序列化丢边。详见第一卡历史报告与拓扑回归。
2. 已有 PGDATA 不再由旧 Bootstrap Env 改写运行期 upstream。现场 Docker Swarm 使用独立 Config 发布入口脚本，不能仅靠替换宿主机 RPM；三台已更新 Config 引用并完成多次重启/恢复验证。
3. MySQL 与 PostgreSQL 共用灾难恢复任务：隔离、原生证据、唯一安全主库选择、从库重建、严格验证、Recovery Commit、入口验证、完成。MySQL 比较 GTID 事务集合和 relay；PG 比较 system identifier、timeline、fork、WAL 与独立 COMMIT/PREPARE，不以最大 LSN 草率选主。
4. 修复升级维护门禁误挡已签名 Agent 多数派授权的 HTTP 423。已有 owner 仍校验签名、Leader、quorum、lease；新业务变更继续被维护门禁阻断。81 的实际失败曾触发正确的 Agent 自隔离，不能删除该保护。
5. `self_isolate` 是会停库/设置持久只读的操作，使用 mutation timeout，不再误用 5 秒查询预算。父上下文取消和短期签名许可仍然优先。
6. 86 在 VIP 验证后重新 discovery，并持有同一个 cluster/publication fence 完成严格校验与 Raft 提交，消除单独检查、远程调用、再完成提交之间的发布间隙。失败诊断区分 cluster、成员/links 数量、health 和时间条件；新失败仍阻断并保留冻结。
7. 当前启停状态与历史 unplanned 分开；完成提交原子恢复主从期望角色、复制源、运行状态和 incident 状态，历史记录不删除。
8. 密码/token/passfile/私钥等在 API、CLI、错误、恢复事件、timeline、审计和报告出口脱敏。现场 PG 复制账户已旋转，新连接成功、旧 bootstrap 密码拒绝；凭据回执不纳入发布材料。
9. 操作的三个标签为同一行等宽，说明另行显示；实际可以切换。升级弹窗首次无旧包信息，验签完成后显示本次包并启用滚动按钮；确认补丁 ID 后才执行。圆形步骤条、固定摘要、日志独立滚动和默认折叠保留。
10. 默认三个版本只清理安装/回退材料，不删历史状态、事件或日志。活动/待复核失败任务的回退材料额外保护。

## 真实 Chrome 升级

路径：文件选择器上传签名包 → 弹窗校验完成 → 只读计划 → 输入完整补丁 ID → 执行滚动升级。没有使用直接 RPM 安装替代验收。

| 阶段 | 时间 / 结果 |
| --- | --- |
| 上传校验 | 14:50:38，签名与兼容性通过 |
| 只读计划 | 14:51:06，尚未修改节点 |
| 开始升级 | 14:51:48 |
| .152 | 14:52:03 安装，14:52:18 验证通过 |
| .154 | 14:52:25 安装，14:52:41 验证通过 |
| .153 | 14:52:49 安装，14:53:07 验证通过 |
| 集群验证 | 14:53:11 |
| 最终完成 | 14:53:38，10 个事件，3/3，维护门禁释放 |

14:51:21 至 14:54:20，经真实 MySQL VIP `.156:3306` 与 PG VIP `.157:55432` 查询 412 次，连接/原生主库身份/可写角色检查失败 **0 次**。这是有间隔的只读 SQL 采样，不是业务负载压测或对任意亚秒瞬断的证明。

升级前后六个数据库容器 ID、启动时间、主从角色、两条 links 及唯一 VIP 归属完全一致。三台均为 86，MySQL 主库 `.153`，PG 主库 `.154`。

74 个升级前已有审计文件在升级后 SHA-256 和大小完全一致，六个旧 payload 目录被清理；记录未限定为三条。71 的待复核失败任务额外保护，不能据此误判保留策略没有生效。更早版本已经删除的日志无法凭空恢复，没有伪造补录。

## 回归与现场证据

### MySQL 86 全停恢复

停机前写入测试标记 `b15b2c5e-027e-4edd-afc9-9fb1f4e0fe65` 并确认三台均有记录，再通过既有冻结接口及 Swarm scale=0 建立三节点全停场景，保留原数据目录。随后从 Chrome 发起任务 `dab5b76a-4f54-436d-9e58-8f84e9db2357`。

14:56:11 开始，15:00:36 完成全部持久写入隔离，15:01:17 根据完整事务证据选出 `.153`，15:01:38 Recovery Commit，15:01:54 完成唯一业务入口验证并成功。该任务没有 blocked 事件，也没有重新预检或重试。

三台 server UUID 保持原值，GTID 集合一致，测试标记均为 1 条；`.153` RO/SRO=0，`.152`、`.154` RO/SRO=1 且 IO/SQL 均运行，复制错误为 0。最终提交所验证的正常 discovery 发生于 15:01:53.539889219，成功提交于 15:01:54.624708298，现场 trace 保留完整阶段和观察时间。

15:02:10.665873211 至 15:03:22.092431623，共 50 个不同实际后台 observation，全部 healthy、一主两从、两条 links、lag=0。没有通过手工刷新或重复读取同一个时间戳凑次数。

### 自动化与界面回归

- 全仓 `go test -p 2 ./... -count=1` 通过，scripts 包 253.710 秒。
- discovery/disaster/store/runtime 竞态测试通过；完成回调期间同一服务与共享 fence 的另一服务均不能发布，新失败仍阻断。初次新增测试的公开方法清单与固定测试时钟错误已更正，最终整组重新通过。
- `go vet ./...`、`git diff --check` 通过。
- 真实 PG16 七个测试重新通过，20.096 秒：三节点 WAL 选择、双分支独立 COMMIT、提升分支祖先 WAL、缺 WAL、PREPARE、离线取证、guarded start 与两台 streaming 重建。
- 原始 PG 拓扑缺陷的独立真实三节点集成测试通过，8.302 秒：pg01 上游 GUC 为空、pg03 上游 GUC 为错误旧值，50 个不同后台周期仍为 pg02 Primary、两台健康 Standby、两条正确 links；只读 discovery 没有修改这些 GUC。随后暂停 pg01 回放，确认集群降级且相应边退出健康拓扑。此项是本机隔离数据库，不冒充现场 50 周期。
- 原生 MySQL 三个隔离容器测试此前已通过：三节点 relay/GTID 选择、guarded clone、实际 executor 重建；独立事务、XA 和不安全持久可写覆盖有阻断用例。它们不替代本节待完成的 86 现场测试。
- 实际 console HTML 在 1440/1024/768/390 视口完成标签同排、点击切换、确认、预检失败、禁止重复提交、持久任务重开、日志独立滚动和 blocked 状态测试；此项使用模拟 API，明确与现场数据库测试分开。
- Chrome 86 页面已实际点击三个标签并截图，主库切换/旧主恢复/灾难恢复同排。

证据目录：工作树 `.build/recovery-field-20260908/`。关键文件为 `continuity-monitor-86.jsonl`、`continuity-after-86.json`、`retention-before-86.json`、`retention-after-86.json`、`mysql-full-stop-86.json`、`mysql-completion-trace-86.jsonl`。原始证据只含允许公开的状态和身份，不含登录凭据。

## 安装材料

发布目录：`/Users/zhaolongjie/codex/clusterguard-ha/release/2.2-86-recovery-candidate`。

| 文件 | 来源 / 平台 | 大小 | 构建时间 |
| --- | --- | --- | --- |
| `clusterguard-ha-2.2-84_to_2.2-86.x86_64.cgupgrade` | 2.2-84 → 2.2-86，Linux x86_64 | 36,694,705 bytes | 14:50:02 |
| `clusterguard-ha-2.2-86.x86_64.rpm` | 目标完整 RPM，Linux x86_64 | 18,385,828 bytes | 14:48:40 |

签名升级包 SHA-256：
```text
c2c774f198189123d8596ef9fda11aff7a273f3a1994c84af6ac6f0a9ee4c3de
```
RPM SHA-256：
```text
8bc0d3ffe0c13db5eab0d0d4c999855bf8040bded9115d528a96d4982fe986f4
```
包内含回退 RPM 和引导器，构建时约束现场信任公钥，实际浏览器上传验签通过。不覆盖既有版本重打。83、85 未上传且已撤回；80 仅上传未执行；81 的控制升级成功不作为数据库连续性通过。

## 未覆盖的边界

未对物理宿主机断电、真实网络分区或业务峰值负载执行破坏性演练；支持的系统形态以本次三台 Rocky Linux 8 / Docker Swarm、MySQL 8.0.44 与 PostgreSQL 16.4 为准。没有 PXC/Galera 现场集群，不能把其 seqno/bootstrap 规则和测试结果混同普通 MySQL。仓库没有独立 support-bundle 实现，未宣称验收不存在的导出出口。

今后每次发布按 [验收清单](release-recovery-acceptance-checklist.md) 执行。失败记录保留在 [恢复修复记录](mysql-postgresql-disaster-recovery-progress-2026-09-07.md)，不以重试成功覆盖失败证据。
