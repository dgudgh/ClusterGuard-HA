# MySQL / PostgreSQL 灾难恢复修复记录

更新时间：2026-09-08。下文保留开发期间的失败和修复记录；当前结果以“当前验收状态”及最终发布报告为准，历史待办不代表当前仍未执行。

## 当前验收状态

2026-09-08 15:52 CST：最终版本 2.2-88 已完成本轮现场验收。MySQL 与 PG 各自三节点全停、Chrome 发起恢复均一次成功，原生数据和至少 50 个不同后台周期通过；两种 Recovery Commit 完成，当前启停正常，无残留冻结/维护/自动切换抑制。MySQL 主库为 `.153`，PG 主库为 `.154`。完整结果和未覆盖边界见 [2.2-88 验收报告](release-2.2.88.md)。

### 88 最终验收

- 真实 Chrome 87→88 于 15:36:14 完成，3/3、10 个事件。15:33:44 至 15:36:44，两个 VIP 共 408 次只读 SQL 采样，失败 0 次；六个数据库容器 ID/启动时间不变。
- PG 新任务 `a6cb77f9-fae7-41ea-9b39-2bbf5f02b9f3` 于 15:42:05 一次成功。隔离仍生效时，从 `.154` 自身 IP 的管理连接、非管理 HBA 拒绝和保护摘要均实测通过；随后原生数据、两轮各 50 个不同后台周期通过。
- MySQL 新任务 `cf25a8d5-3186-4b4f-982b-7d71448a963e` 于 15:49:57 一次成功。三台 canary 保留、UUID 未变、GTID 完全一致、主从只读和复制线程正确；15:50:58 至 15:52:06 的 50 个后台周期通过。
- 94 个升级前审计文件内容不变，6 个旧 payload 目录被清理，历史 11 条没有截断。签名包和 RPM 均有已复核的 SHA-256 文件。
- 全仓/竞态/原生正反例/脱敏/UI 回归和 vet 通过；物理断电、真实网络分区和峰值负载未演练。下文为历史失败与修复记录，保留当时的未完成项，不作为最新状态。

### 87 同机控制器恢复 HBA 修复

- 86 的 PG 任务在两台从库已经 streaming 后仍为 `links=0/2`。持续 trace 证明主库探测失败；Raft Leader 与选出的 PG Primary 同为 `.154`，而 Agent peers 仅有另外两台，恢复 HBA 遗漏本机控制器通过 `.154` 发出的管理连接。
- 三台实际配置均包含三个精确 ControllerURLs；本机路由来源为 `.154`。主库停止后旧容器已经删除，没有保存原始 FATAL 日志，不把推断冒充日志证据。
- 新增真实 LoadConfig 到 HBA 的测试先在修复前失败，再验证配置派生的控制器 IP 获得管理用户 SCRAM 规则。只放行 /32 或 /128，不做 DNS/CIDR 扩展，不给控制器增加复制或业务权限。
- 冻结任务仅能迁移字节和摘要完全匹配的旧保护文件；原 HBA 与任务身份保留，任意篡改拒绝。该迁移、幂等及拒绝篡改回归均通过。
- 87 全仓测试、vet、diff 检查、agent/runtime/disaster/api 竞态通过；真实 PG16 八个正反例通过，22.688 秒，包含实际业务连接拒绝及 guarded-start/rebuild。
- Chrome 86→87：15:24:28 开始，15:26:11 三台完成。PG 此时冻结，不能把这一轮作为健康 PG 升级连续性验收。
- 新增只读现场探针 `tools/diagnostics/pg-colocated-guard-field-check.py`：在保护仍生效时，从 `.154` 自身 IP 查询管理连接来源/主库角色，验证非管理用户被 HBA 拒绝，并核对保护文件和回执不变。结果待现场任务完成后记录。

### 84 恢复复验与 86 完成提交修复

- PG 在 84 上重新执行运行节点隔离，于 14:25:29 成功；`.153` 的真实停库隔离耗时超过 5 秒且不再被查询预算中断。原生身份、测试数据、物理槽、streaming、新复制凭据和 VIP 验证通过；14:27:26 至 14:28:31 完成 50 个后台周期。
- MySQL 84 全停恢复在 14:32:37 完成 Recovery Commit，但 14:32:49 严格完成检查拒绝当时的拓扑，任务重新隔离并保持冻结。没有保存该次失败瞬间的完整快照，因此不能断言是健康、数量还是时间条件触发。
- 同一 84 版本重新取证后于 14:39:16 成功，三台 canary、UUID、GTID、IO/SQL 和只读角色均核对通过。这仅证明重试恢复成功，不证明偶发问题已经修复。
- 代码存在确定的检查/提交间隙：健康刷新之后还要执行三台远程 VIP 检查，然后 Manager 才单独调用 CompleteRecovery，期间后台可发布另一份观察。86 在 VIP 收敛后重新执行一次真实 discovery，并在同一个 cluster/publication fence 内完成严格校验与 Raft 提交；不缓存旧健康结果，也不忽略新失败。
- 新并发回归验证本服务及共享 publication fence 的另一服务都不能在提交回调中插入发布；回调错误保留且释放锁，失败/取消的刷新不能调用提交。两种引擎的新失败仍拒绝完成并保留冻结。错误细分为 cluster、节点/links 数量、health 和 observation 时间，避免只有泛化提示。
- 85 仅构建未上传，已明确撤回；86 使用新的补丁 ID 和签名，不覆盖旧候选内容。

### 81 升级复验发现的新故障

- 13:45:07，PG `.154` Agent 明确记录 `controller reconcile status 423`，因无法取得有效多数派授权释放 VIP 并停止 writer。13:45:16 才开始首个 RPM 安装，证明故障来自维护建立，而不是 PG 升级或数据库数据损坏。
- 根因：签名 Agent reconcile 复用了普通 `authorizeMutation`，被升级维护返回 HTTP 423。维护应该禁止新业务切换，不应该拒绝已有合法 owner 的短期多数派授权。Agent 自隔离行为正确，不能取消。
- 82 将签名验证后的请求送入共用 Leader/quorum 授权逻辑。签名错误仍 401，无多数派仍 503，过期 lease 仍返回签名 SelfIsolate，普通业务变更仍 423。两种引擎正反例、竞态测试、全仓测试和 vet 已通过。
- 81 的 3/3 和 13:46:44“升级成功”只证明控制程序版本与维护门禁检查通过，不证明数据库连续可用。后续验收必须在两种数据库均 healthy 时再执行一次真实 Chrome 升级，对比容器 ID/启动时间、主从角色、复制 links 和 VIP，并重跑原生 SQL 与 50 个实际后台周期。
- 81 安装材料清理后，44 个预存审计文件 SHA-256 与升级前完全一致；历史记录超过三条。仅安装与回退材料按默认三个版本清理，待复核失败任务额外保留。
- 最新页面修复：三个操作标签改为一行等宽，说明文字另起一行；1440/1024/768/390 视口均完成实际点击及布局断言。

### 82 部分节点仍运行时的恢复复验

- 82 三节点于 14:12:48 完成 Chrome 滚动升级。14:14:03 从 Chrome 发起 PG 恢复，`.154` 已停库隔离通过，仍运行的 `.152` 在 14:14:09 进入隔离后于 14:14:15 被 SSH `signal: killed` 中断。
- 根因是 `self_isolate` 会执行 PostgreSQL Stop 或 MySQL 持久只读，却未被 SSH transport 分类为 mutation，误用 5 秒查询预算。先前全停测试没有覆盖正在运行的 PG 容器收敛时间。
- 新增两种引擎回归，修复前都明确得到约 5 秒错误 deadline；修复后使用已配置的 mutation budget，上层取消或更短授权上下文仍优先。没有改变签名有效期、多数派校验或 fencing 判定。
- 83 仅构建未上传，已标记撤回。采用全新 84 包，不覆盖同一版本的签名内容。必须再次从 Chrome 对现场仍运行的节点执行隔离并完成恢复。

| 现场项目 | 已取得的证据 |
| --- | --- |
| PG 首轮全停恢复 | Chrome 发起，78 版于 12:55:54 完成；`.154` Primary，`.152`、`.153` Standby；两条 active 物理槽及 streaming |
| PG 首轮稳定性 | 12:56:27 至 12:58:08，50 个不同实际后台 observation，全部 healthy、两条 links |
| MySQL 全停恢复 | Chrome 发起，78 版于 13:03:25 完成；`.153` Primary，`.152`、`.154` Replica |
| MySQL 数据核对 | 停机前 canary `c958227c-96ff-484f-af6b-6b90bc8b4d08` 在三台仍存在；原 server UUID 保留、GTID 相同、RO/SRO 与 IO/SQL 线程符合角色 |
| MySQL 首轮稳定性 | 13:07:19 至 13:09:01，50 个不同实际后台 observation，全部 healthy、两条 links |
| PG 凭据旋转 | 实际账户为 `cg_replication`，控制配置原错误账户已纠正；控制配置、passfile、Swarm bootstrap Secret 已同步，新连接验证成功，旧 bootstrap 密码认证被拒绝 |
| PG 旋转后全停复验 | Chrome 发起，79 版于 13:23:00 完成；canary `08118503-cc9a-46bc-9c1a-a80a7a6b6487` 三台保留，native ID、slots、receiver 与 VIP `.157` 原生 SQL 查询全部通过 |
| Recovery Commit | 两种引擎 `expected_state=running`、`actual_state=running`、`incident_active=false`、`incident_recovered=true`、`last_recovery_status=succeeded`；期望角色等于原生角色 |
| 当前启停状态 | 两种引擎 `outage_classification.kind=normal`、`database_state=running`；没有冻结、维护节点或自动切换抑制。历史 `last_shutdown_classification=unplanned` 保留但不污染当前状态 |
| 签名升级 | Chrome 已完成 73→74→75→76→77→78→79，各次三台均成功；79 完成时间为 13:18:25 |

79 版之后的变更仅涉及安装材料保留：不再删除整个升级目录，保留 package/status JSON、event journal 和输出日志；已清理的历史包不可执行，重新上传同一份通过签名的包才能恢复材料，且不覆盖旧任务。运行中或待复核任务的安装包和回退材料额外保护。

80 候选包在全仓回归中被拦截：Bash 同一个 `local` 声明引用尚未赋值的新变量，使回退目录检查错误使用调用者目录。80 仅上传校验，**没有执行**；修复后相关回归通过，使用新的 81 签名包，避免同一补丁 ID 对应不同内容。旧版本已经删除的日志不能凭空恢复，禁止伪造补录。

脱敏验证覆盖 API 全局 JSON、CLI JSON 和错误、Agent 错误、恢复事件、操作 timeline、审计和报告写入/历史读取。结构化数组、数字值、SQL PASSWORD、SCRAM、私钥材料均有回归；CAS 指纹和授权布尔值不应被破坏。仓库未找到独立 support-bundle 实现，不能宣称测试过不存在的打包出口。

本轮真实 PG 16 正反例再次通过（21.079 秒）：双分支独立 COMMIT、未决 PREPARE、缺 WAL 均拒绝自动选择；祖先分支和唯一提交分支正例、签名 guarded-start/rebuild、两台 streaming 均通过。安全门禁没有为了让页面变绿而放宽。

## 现场授权与当前访问

- 用户已明确授权 `192.168.102.152`、`192.168.102.153`、`192.168.102.154` 上的 MySQL 和 PostgreSQL 用于停机、重建恢复验收，不再重复请求这项授权。
- 2026-09-08 使用用户提供的 SSH 登录信息，已直接连接 `.153`，再通过现场已有控制节点 SSH 信任链核对 `.152` 和 `.154`。密码不写入仓库、文档、测试命令参数或日志；先前 SSH 访问阻塞已经解除。
- 重新使用现场 CA 验证 TLS，通过服务端配置中的控制令牌读取集群和拓扑 API，令牌仅在远端进程内读取。首次采样三台为 73，后续已通过真实 Chrome 上传并完成 73→74、74→75、75→76 的三节点升级；77 正在验收。
- 早期 MySQL 实机验证在 `.153` 上新建内部隔离网络、三个临时容器和独立匿名数据卷，没有公开宿主机端口或使用业务数据目录。11:50 后现场 PG 已进入显式全停恢复测试，不能继续描述为只读测试；当前失败仍保持冻结和数据保留。现场 MySQL 全停恢复尚待执行。
- 本轮由当前主代理执行，没有调用其他模型或子代理。

## 已实现的代码

1. 共用恢复任务模型、持久化阶段和恢复编排器。覆盖全成员 fencing、证据采集、选主、重建、验证、Recovery Commit、入口激活、完成的顺序约束。
2. MySQL 使用 GTID 集合包含关系选主，拒绝独立事务分支；PG 使用 system identifier、时间线祖先、fork LSN、共享 WAL 字节以及被舍弃分支 COMMIT/PREPARE 证明，不能仅使用最大 LSN。
3. Agent 增加受签名保护的只读恢复证据命令。MySQL 要求持久只读、GTID/binlog、复制线程停止、relay 已排空、无 XA；PG 要求离线 control/WAL 校验和实际 native ID 匹配。WAL 请求的时间线、范围和证据指纹均参与签名。
4. Recovery Commit 在同一个仓库提交中写入主从角色、期望角色、两条复制链路、主库归属和恢复状态；提交不直接释放维护。恢复失败保持冻结，过期执行租约不能推进状态。新执行器必须重新隔离并采集证据。
5. 灾难恢复冻结期间，普通维护解除接口、writer lease 持久化和后台 VIP 授权受到约束。已完成的历史灾难恢复不会阻塞后续正常启停维护。
6. 修复旧启停恢复流程先解除维护、再写完成状态的问题，改为原子完成，并校验前端已验证的拓扑观察没有变化。
7. PostgreSQL 容器重启入口不再把旧 bootstrap 主库地址、端口和 primary ID 当成已有数据目录的初始化依据。只在旧式临时 passfile 缺失时恢复凭据文件，不改运行期 upstream。此脚本变更尚未发布到容器运行环境。
8. 命令错误、API 错误、恢复事件、审计和报告增加脱敏；审计和报告同时覆盖写入和历史读取出口。
9. 新增 MySQL `recovery_quiesce` Agent 命令：要求有效签名、操作租约、无业务 VIP 和持久只读；先停止 IO 线程、读取完整 received GTID、等待 SQL 线程排空、停止 SQL 线程，再重新取证。拒绝多源通道、复制过滤、applier 错误、字段缺失和未排空日志。不执行 RESET REPLICA，不在选主前丢弃 relay log。取消/超时后仍尝试有界停止 SQL 线程，保持只读；重复请求不能仅凭旧成功回执假定当前仍已排空。尚未接入整集群物理 Driver。
10. 修复 PG 刚提升后 checkpoint 仍在祖先时间线的问题：离线扫描从 REDO 开始按 history 逐段读取祖先 WAL，再读取当前时间线；共享 WAL 比较也按时间线分段。没有放宽缺文件、CRC、独立 COMMIT/PREPARE 的拦截。
11. 恢复证据、WAL 比较和 MySQL 排空命令使用独立 120 秒 SSH 预算，不再被普通 5 秒状态探针预算截断；更短的上层操作上下文仍优先取消。
12. 修复 Docker MySQL 恢复查询未启用 `--raw`：多 UUID GTID 的换行被 batch 客户端再次转义，JSON 解码后变成字面的 `\\n`，导致合法历史解析失败。只修改恢复查询输出模式，原有普通角色查询行为不变。
13. PG 恢复工具分别收集 stdout/stderr，再按固定顺序组合。避免 `pg_waldump` 在正常 WAL 尾部报告的 stderr 先于缓冲 stdout 输出，造成偶发拒绝及取证指纹变化；保留原有 CRC、缺 WAL、事务分支校验。
14. MySQL 恢复证据增加持久化可写覆盖检查：即使当前 `read_only` 和 `super_read_only` 均为 ON，存在会覆盖重启隔离的 `SET PERSIST_ONLY ...=OFF` 时也拒绝恢复证据。此项不是所有 Docker 停机隔离路径的完整审计结论。

## 2026-09-08 现场现状

采样时间约 09:11 CST，来自三台实际 SQL、Swarm service 和控制台 API，不使用历史截图替代当前状态。

| 项目 | 当前证据 |
| --- | --- |
| MySQL | `.152` Primary，`.153`、`.154` Replica；只读与复制线程符合角色；API healthy、两条 links |
| MySQL GTID | 三台 executed/purged 相同，包含两个 server UUID；不能据此推断所有全量重建路径已可用 |
| PG `.153` / pg02 | `cgpg16_postgresql02` 期望副本数 0，实际无容器；数据目录为正常关闭状态 |
| PG `.152`、`.154` | 实际为 Standby，时间线 11，回放至 `0/90001C0`，没有 WAL receiver 连接 |
| PG API | degraded、`links=[]`；这次没有可用 Primary/streaming 的事实足以解释当前降级，不能当成纯页面误报 |
| PG 离线取证 | pg02 的 system identifier 为 `7678901569924304935`，timeline 11，REDO `0/9000148`，验证 WAL 终点 `0/90001C0` |

pg02 的只读检查通过 Agent 实际 Docker PostgreSQL 控制器执行：验证服务身份、期望副本数为零、任务停止、绑定的数据目录，再调用只读、无网络临时工具容器读取 control/WAL。没有启动 PostgreSQL 或修改数据。

## 2026-09-08 实机复现与复验

1. 隔离 MySQL 三节点真实建立 GTID 复制，暂停一个 SQL 线程，新增业务事务，确认该事务已收到但未应用，然后执行真正的 `RecoveryQuiesce`。修复后事务完整落库，三个节点拒绝业务写入；重启保持只读；两支独立业务 COMMIT 被拒绝选主；未决 XA 被拒绝取证。
2. MySQL 多 UUID 历史用例原先明确失败：`invalid GTID group "\\n...:1"`。修复后同一实机用例通过。这是恢复取证路径缺陷，不等于已经解释所有历史拓扑或升级问题。
3. 现场停着的 pg02 原先重复取证偶发失败：`unexpected error after WAL scan`。拆分 stdout/stderr 后，同一目录完成稳定取证、实际 WAL 字节验证和旧指纹拒绝；连续五次完整只读用例通过，每次约 7.4 至 7.9 秒。
4. 本机六个真实 PG 16 用例复跑通过，合计 15.141 秒，包括祖先 WAL、独立分支 COMMIT、缺 WAL、PREPARE 和单节点离线取证。
5. 最终隔离 MySQL 实测 70.25 秒通过，新增真实 `SET PERSIST_ONLY ...=OFF` 反例：运行态仍只读时，恢复取证明确拒绝不安全的重启覆盖，随后还原临时节点隔离设置。
6. 全仓 `go test ./... -count=1` 通过，脚本包 214.815 秒。最后增加持久化覆盖检查后，重新运行三个关联包普通测试、race 测试和 `go vet`，均通过。Linux amd64 测试二进制交叉编译并在 `.153` 执行；这不是升级包发布。
7. 临时容器和临时内部网络已清理，按测试标签复查均为空。09:45 CST 再读现场 API，原 MySQL 仍 healthy、两条 links；原 PG 仍为两台 Standby、一台停止、`links=[]`。没有把只读取证通过描述为 PG 已恢复。

新测试入口：`internal/agent/recovery_mysql_cluster_test.go`、`internal/agent/recovery_postgresql_field_test.go`。MySQL 测试显式要求 `CG_MYSQL_RECOVERY_TEST_IMAGE`；PG 现场测试显式要求只读 Agent 配置路径和集群 ID，缺少环境变量时跳过，跳过不算实机通过。

用户提供的 PXC 脚本恢复流程已整理成独立[恢复场景参考](recovery-scenarios-pxc-reference-2026-09-08.md)。目前只收到流程截图，未收到原始 `.sh` 文件；没有执行截图中的 bootstrap 指令。

## 本轮已经执行的验证

- `go test ./... -count=1`：通过。脚本包约 230 秒。此后补充修改又单独运行关联包测试。
- `go vet ./...`：通过。
- `go test -race ./internal/disaster ./internal/store ./internal/agent ./internal/coordination ./internal/api -count=1`：通过。
- `TestRecoveryManagerFencesBeforeEvidenceAndCommitsBeforeActivation`：MySQL 和 PG 均覆盖正常顺序以及 preflight、fence、inspect、compare、start、rebuild、verify、commit、activate、verify-activation、complete 故障注入。这里使用测试执行器，不是数据库实机灾备演练。
- `TestDisasterCommitBothEnginesPersistsAndKeepsFreeze`：两种引擎的持久化、重开仓库、拒绝空 links、维护保护、最终完成状态通过。
- `TestDisasterCommitFailureKeepsEveryProtection`、`TestPowerRecoveryCompletionIsAtomic`：磁盘同步失败不能先释放保护。
- `TestRecoveryFreezeRejectsStaleWriterGrant`、`TestRecoveryCompletionRejectsNewFailureAfterCommit`：旧 writer lease 批次不能恢复授权；提交之后新出现的拓扑故障不能被完成动作忽略。
- `TestRecoveryOfflineNativePostgreSQL`：使用隔离的 PostgreSQL 16 原生实例，实际 initdb、启动、写入、checkpoint、停库、pg_controldata、pg_waldump、WAL 哈希与过期指纹拒绝验证通过。
- `TestPostgreSQLNativeStreamingDiscoveryFiftyBackgroundCycles`：隔离的 PG 三节点，50 个不同后台 discovery 周期均 healthy、2 条链路；暂停回放后正确降级并移除不安全链路。不是现场集群验收。
- `TestInitializedEntrypointIgnoresObsoleteBootstrapCoordinates`：执行真实入口脚本的重启分支，模拟旧地址无效、旧端口无效、旧 primary ID 无效，保持已保存 upstream。仅 stub 文件 chown 与最终入口转交；不把它称为容器实测。
- 2026-09-07 开发机没有可用 docker，彼时 Docker 集成测试未执行。2026-09-08 已在授权 `.153` 上执行隔离 MySQL 容器测试和 PG 只读取证，详见上节；仍不是整套灾难恢复现场验收。
- MySQL GTID 选主、隔离证据解析及失败测试通过；未完成 MySQL 实机整集群停机/重建验收。
- 本轮 `go test ./internal/agent ./internal/endpoint ./internal/disaster -count=1` 通过；MySQL 排空顺序、超时、取消、安全收尾、签名/租约/VIP 门禁、旧回执重验及取证字段缺失的回归通过。这些 MySQL 用例使用测试执行器，不冒充 MySQL 实机演练。
- 本轮使用 `CG_PG16_BIN` 指向隔离 PG 16 二进制，运行五个真实三节点用例，14.535 秒全部通过：`TestRecoveryPostgreSQLActualThreeNodeWALSelection`、`TestRecoveryPostgreSQLActualDivergentCommits`、`TestRecoveryPostgreSQLActualPromotedBranchWithoutOldCommits`、`TestRecoveryPostgreSQLActualMissingWALBlocksSelection`、`TestRecoveryPostgreSQLActualPreparedTransactionBlocksSelection`。使用实际 initdb/basebackup、复制、业务 INSERT、promotion、停库、control/WAL 读取；仅监听临时 Unix socket，结束后关闭全部临时实例。
- 本轮关联 `go vet ./internal/agent ./internal/endpoint ./internal/disaster` 通过。
- 最后一次变更后，全仓 `go test ./... -count=1` 通过，脚本包 209.220 秒；`go test -race ./internal/agent ./internal/endpoint ./internal/disaster ./internal/store ./internal/api ./internal/coordination -count=1` 通过，store 86.892 秒；`GOOS=linux GOARCH=amd64 go build ./cmd/clusterguard ./cmd/clusterguard-agent ./cmd/cgctl` 通过。交叉编译不等于已生成、签名或现场验证升级包。

## 本轮真实复现的 PG 取证错误

双分支业务提交用例最初失败，证据来自隔离实例实际 `pg_controldata`：

```text
Latest checkpoint location:        0/3000060
Latest checkpoint's REDO location: 0/3000028
Latest checkpoint's TimeLineID:    1
Minimum recovery ending location:  0/40001B8
Min recovery ending loc's timeline:2
```

旧实现用 timeline 2 从祖先 REDO `0/3000028` 读取，错误寻找 `000000020000000000000003`，于是将正常祖先 WAL 当成缺失文件。修复后同一真实用例成功完成祖先字节比较，分别验证旧、新分支的独立 COMMIT，最终正确拒绝自动选主。新增“只有新分支提交”的正向用例也通过，排除一律阻止新时间线的假修复。

该问题是开发中的恢复取证路径缺陷，不能据此声称已经查明或修复所有现场 `links=[]`、升级超时或节点回退问题。

实现依据：[PostgreSQL 16 Timeline 文档](https://www.postgresql.org/docs/16/continuous-archiving.html#BACKUP-TIMELINES)、[MySQL 8.0 WAIT_FOR_EXECUTED_GTID_SET](https://dev.mysql.com/doc/refman/8.0/en/gtid-functions.html#function_wait-for-executed-gtid-set)。

## 12:55 验收检查点（历史）

2026-09-08 12:55 CST 更新，尚未通过全部验收：

- 已接通实际恢复 Driver、签名 Agent 命令、Runtime、HTTP plan/status/execute 以及可切换的灾难恢复标签页。请求包含冻结清单、任务版本、确认集群名称及 fencing 确认，执行在后台持久化。
- PG 主库使用任务绑定的临时 HBA 隔离业务，依据离线证据启动；其他节点 rewind，失败才走保留旧目录的 basebackup；提交后才恢复业务访问和 VIP。
- 普通 MySQL 使用真实 GTID 比较和物理 Clone 执行器；准备插件期间启用 offline_mode，停止业务访问且不生成额外 binlog；重建后核对 native UUID、GTID、复制线程与持久只读。
- MySQL 隔离实机完整执行器测试通过（75.76 秒），测试强制制造缺失 binlog，实际走 Clone、容器重启、复制重建并验证业务行。没有对业务库执行 PURGE。
- PG 真实 guarded-start/rewind/basebackup/streaming 测试通过；全套真实 PG 测试 21.056 秒；隔离 PG 的 50 个不同后台 discovery 周期 7.39 秒通过。此项仍不是现场 50 周期。
- 浏览器隔离页面验收通过：真实 HTML、模拟 HTTP API，覆盖预检失败、确认门禁、防重复、重开任务、日志独立滚动与移动端。这不是现场数据库恢复验收。
- 新的全仓回归通过，scripts 包 207.509 秒；关联 race 测试通过，store 包 93.842 秒。后续脱敏修改重新运行 redact/cgctl/api/store 测试通过。
- 结构化诊断 JSON 保留 uint64 精度、授权布尔值及非秘密的 observation CAS 指纹；密码、连接串、token、passfile 脱敏。`approval issue` 的有意签发凭据不被错误掩盖。
- 真实 Chrome 完成 73→74（11:35:48）、74→75（11:49:39）、75→76（12:11:47）上传、签名验证、确认执行和三节点滚动升级。公钥与现场信任匹配，包含回退 RPM。未上传时无旧包信息、校验完成才启用滚动升级已实测。
- 修复恢复编排持有 publication 锁导致自身 discovery 无法提交的问题：使用仍受多数派保护的 lifecycle 锁；新增互斥及观察提交回归，75 已部署此修复。
- PG 三个 Swarm 服务在全部停止、VIP 不存在、恢复冻结保留时迁移到修复后的不可变入口 Config。旧 Config 和配置引用审计保留，原数据目录未删除。新脚本 SHA-256：`a2e985905423a68771944bd8bacdb823e9169871f481d36c1401cf46656397e3`。
- PG 首次真实页面恢复在主库启动前阻断：生成的指纹已带 `sha256:`，Agent 再拼一次造成合法请求拒绝。真实 PostgreSQL 测试扩展为签名 Request → Service 校验 → guarded start/rebuild；修复前同样失败，修复后通过。76 部署此修复。
- PG 重试暴露第二个协议错误：HTTP 授权客户端使用发请求前的时间校验收到响应后的 10 秒授权，正常延迟触发“short validity”拒绝。77 改为接收时重新取时间，缓存回退也重新检查时间；没有增加授权有效期。新增 TLS HTTP 延迟响应与到达时已过期的回归。
- 重试界面首次查询读到旧 blocked 版本便停止轮询。77 记录提交版本，等待更高版本再判断终态，并拒绝低版本观察倒退；浏览器复现先失败，修复后通过。
- 76→77（12:21:32）、77→78（12:51:58）均通过真实 Chrome 文件上传、校验和三节点滚动升级。78 的 RPM 安装钩子在现场生成了实际受限服务 drop-in，未通过直接安装 RPM 绕过上传流程。
- 77 的 PG 现场恢复已能选出并启动 `.154`，但重建时失败。只读网络探测在 12:32:55 成功连接新主库；随后 `.154` 的 systemd journal 明确记录协调进程无法读取 postgres 所有、0700 数据目录内的 `pg_hba.conf`，继而安全 fencing 停库。因此不能把该错误归结为复制密码或关闭 fencing。
- 78 为 PG 协调进程增加必需的 DAC 访问能力，并按 agent.json 中已注册 PGDATA 生成精确 ReadWritePaths；保留 ProtectSystem=strict，不授权整个 /data。恢复原 HBA 的回执移到私有 sibling 目录，读取旧回执迁移时保留原始 HBA 和旧备份。
- `tools/diagnostics/recovery-systemd-sandbox-check.py` 在 `.153` 的真实 systemd 临时单元中复现旧权限拒绝；修复配置通过 0700 数据目录读取、授权目录原子写入和未授权旁边目录 EROFS 拒绝。该测试只创建独立 fixture，不修改数据库服务。
- 78 增加 native UUID 绑定的物理复制槽：先核验源库身份与主库角色，再预留 WAL；拒绝活跃、逻辑或无法确认的槽，重建后核对 primary_slot_name 与 WAL receiver，并在真实 PG 集成用例确认两条 active 物理槽对应两个 application_name。依据 PostgreSQL 16 pg_basebackup 与管理函数文档，不用临时槽假装长期 WAL 保留。
- 最新真实 PG 正反例集合通过（21.692 秒），包括签名请求、guarded start/rebuild、两台 streaming、独立 COMMIT/PREPARE 与缺 WAL 拒绝；全仓 `go test -p 2 ./... -count=1` 通过，scripts 237.722 秒。默认测试中缺少 MySQL 容器环境变量而跳过的用例不算本次实机通过。
- 78 为验收候选，不是已通过全部恢复门槛的最终版本。PG 当前正在真实页面重试，尚未达到 Recovery Commit；MySQL 现场全停恢复、两种现场 50 周期、secret 旋转仍未完成。

## 12:55 待办清单（历史）

以下为当时未完成项；已完成结果见本文开头，不以本节作为当前验收结论：

- 三节点现场恢复期间专用授权、业务隔离、Recovery Commit 后 VIP/writer 激活的完整验收。
- MySQL 现场 Swarm 全停后的受隔离重启和完整恢复，不能用隔离容器测试代替。
- 现场 PG 两条复制链路、原生身份、业务访问、旧 bootstrap Env 不覆盖运行态，以及容器入口配置迁移核查。
- 分支双提交、缺 WAL、全节点失联、Leader 切换的实际控制链路负向验收；隔离数据库部分反例已通过。
- 全部 CLI、operation plan/timeline、support bundle 的秘密出口审计尚未全部完成；已暴露 replication secret 未旋转。
- 现场 PG 当前主库/两条 links 的连续 50 轮验收尚未完成。
- 本轮升级包已生成、签名并通过 Chrome 上传校验，尚未确认滚动升级完成；没有用直接安装 RPM 代替浏览器上传验收。

## 后续发布门槛

1. 在隔离环境分别完成 MySQL 与 PG 的全节点停机恢复；包含主库自动选择、其他成员重建、唯一业务写入口、完整复制和 Recovery Commit。
2. MySQL GTID 分叉、PG 分支独立 COMMIT/PREPARE、成员不可达、缺失必要 WAL、控制面无多数派必须保持禁止写入并明确报告原因。
3. 两种引擎恢复完成后连续至少 50 个后台发现周期稳定；旧 observation、旧 bootstrap Env、历史事故不能覆盖已提交的当前状态。
4. 控制台按真实用户路径点击恢复，验证确认、进度、失败原因、重试及页面刷新后持久化状态；发布升级包按上传、签名验证、滚动升级、三节点验收路径测试。
5. 发布文档分别列出代码测试、隔离数据库实测、现场验收、签名包路径与 SHA-256。任何未完成项目不得写成已通过。

SSH 访问及现场停机重建授权均已具备。代码集成与隔离实测不代替现场闭环；现场恢复、稳定性及发布门槛全部通过之前，候选包不得标成“已验收的一键恢复版”。
