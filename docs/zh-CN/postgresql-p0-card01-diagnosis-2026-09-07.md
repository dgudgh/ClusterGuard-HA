# PostgreSQL P0 第一张任务卡：空拓扑诊断

日期：2026-09-07。状态：诊断完成，生产行为未修改，停在第二张任务卡之前。

## 1. 结论与边界

**当前证据将故障断点定位在 PostgreSQL Discovery 的上游身份采集及 Adapter 建边之前，不支持“application_name 只比较平台 resource_id”是本次直接原因。**

当前实现根本没有在这条 Discovery 链路读取 `pg_stat_replication.application_name`、复制槽或 `primary_conninfo` 来建立上游身份。它只读取 standby 上自定义 GUC `clusterguard.primary_node_id`，据此生成 `Replication.SourceIdentity`。缺失该值时，即使 standby 已只读、streaming、lag=0，也会得到当前告警并返回零条原生边。

线上 API 中 pg01、pg03 的 `SourceIdentity` 都缺省，同时 IO/SQL 均 running、read_only=true，且健康摘要正是该分支产生的文本。这与离线复现完全吻合。在已核对的源码分支下，这指向探测输入的 `primary_node_id` 为空或 NULL；如果它是非空非法 UUID，Discovery 会报身份解析错误，而不是成功观测出当前结果。

**尚未直接读取线上数据库 GUC，不能把上述推断写成已执行 SQL 的结果。** 当前 SSH 公钥认证未通过，也没有已验证的数据库只读连接凭据；未借用控制台密码尝试数据库或 SSH。该 GUC 为什么缺失、何时缺失、是否由人工恢复或其他脚本引起，仍需只读配置证据。未捕获生产 Adapter 入参/返回内部 trace，也未读取原始 Raft 日志；后段判断由源码、实际仓库回放和三节点一致性共同支持。

## 2. 实际核验环境

| 项目 | 本轮核验值 |
| --- | --- |
| 源码工作树 | `/Users/zhaolongjie/codex/clusterguard-ha/.worktrees/platform-auth-session` |
| 分支 / HEAD | `codex/2.2-postgresql` / `bc0546a3e91ac86c61017d6d57c7ceeedd3d114f` |
| 运行版本 API | `2.2-68`, Linux amd64, RPM x86_64 |
| 运行版本 API 的 commit / built_at | `dc1a57ad8f8224ffdb267ec64c155405f5409447` / `2026-08-31T09:56:50Z` |
| 集群 | `swarm-pg16` / `8938f553-c7ac-4589-aaf2-5d6823c4cc7e` |
| system_identifier / Timeline | `7678901569924304935` / `11` |

运行版本 API 所报 commit 与 HEAD 之间，`adapters/postgresql`、`internal/discovery`、`internal/store`、`pkg/identity`、`pkg/model`、`internal/api/clusters.go` 无源码差异。本轮未读取部署二进制做 SHA-256 比对，不能将版本字段核对等同于二进制逐字节认证。部署容器中的 entrypoint 文件也未直接读取。

| 节点 | 地址 | 平台 instance.resource_id | 原生 engine_identity.resource_id | 当前 API 状态 |
| --- | --- | --- | --- | --- |
| pg02 | 192.168.102.153 | f936f3da-15b4-4b33-a189-bfc28b7aba76 | 5229477a-212a-4015-b0b7-e1ce53016f87 | primary / healthy |
| pg01 | 192.168.102.152 | a248d206-2d6d-4df7-b1c4-2cf79d5b1e2f | a7dbb99d-7b75-4e3d-851d-6f0a25871306 | standby / degraded / streaming / lag=0 |
| pg03 | 192.168.102.154 | 9cabbf9f-4971-4f01-9ce0-574606dc93bc | d89ed249-b4fb-47d7-8f52-3617ac49c1cb | standby / degraded / streaming / lag=0 |

用户提供的 `application_name` 与 slot active 结果保留为现场输入，本轮没有重新执行主库复制槽查询，不把它们标记为本轮直接 SQL 验证。

## 3. 线上只读证据

在已授权的控制台登录之后，仅执行 GET；没有调用 discovery refresh、升级、恢复、启停、fencing 或 failover 接口。Chrome 页面也确认相同异常状态，但以 API 快照为主要证据。

同一集群观察到四个不同的后台 `observed_at`，均为 `links=[]`，两台 standby 的 `source_identity` 缺省：

| 服务端 observed_at（UTC） | 采样范围 |
| --- | --- |
| 2026-09-07T02:57:03.740915336Z | .152 |
| 2026-09-07T02:58:34.645278645Z | .152 |
| 2026-09-07T02:59:31.893732172Z | .152 |
| 2026-09-07T03:00:13.02316774Z | .152 / .153 / .154 |

最后一次三节点采样具有相同的 topology observed_at、term `55`、commit/applied index `1798779`、state revision `1798671`。Leader 为 .153，`quorum_confirmed=true`、`mutation_authority=true`；三节点 ready，升级维护标志均为 false。Follower 的本地 mutation authority 为 false 不代表集群多数派丢失。

这说明问题跨越多轮实际后台观测存在，且三处 API 对同一已复制状态的返回一致；不支持“浏览器缓存”“单个 follower 落后”解释。本次是四个不同观测的抽样，**不是连续 50 周期验收**。

证据中的 read_at 为客户端时间，observed_at 为服务端时间，两者存在时钟偏差。本报告以服务端观测标识区分周期，没有用客户端时间判断 observation 是否过期，也没有调整服务器时钟。

证据文件：`tools/diagnostics/pg-topology-card01/live-api-evidence.json`。只保存白名单版本、节点身份、复制状态及控制面索引，不包含登录 cookie、密码或完整连接信息。

## 4. 逐层定位

| 层 | 核验结果与源码锚点 | 本次判定 |
| --- | --- | --- |
| PostgreSQL Discovery | `adapters/postgresql/probe.go:14` 只查 receiver、自定义 GUC、控制文件与角色；`:160` 仅在 primary_node_id 合法时写 SourceIdentity；`:175` 同时以此判断 standby 健康 | 上游身份没有形成，告警已在这里产生 |
| Adapter Link Builder | `adapters/postgresql/postgresql.go:77`，SourceIdentity 为空就直接返回空 Links | 第一处可定位的空边产生点 |
| Discovery 汇总 | `internal/discovery/service.go:240` 复制 Adapter 原生边；`:739` 的 role-based fallback 仅适用于 Oracle / SQL Server | PG 没有基于“恰好一个 primary”猜测上游；不应简单增加不安全猜测 |
| Identity Resolver | `pkg/identity/identity.go:15` 使用 EngineIdentity.resource_id；`internal/store/repository.go:1974` 用原生身份建索引，`:1999` 绑定 source/target | 并非只用平台 resource_id 匹配；平台 ID 与原生 ID 不同本身不是 bug |
| Observation Merge | `internal/store/repository.go:1984` 丢弃本轮成功观测目标的旧边，再用本轮原生边重建 | 新观测原生边为空，旧边被有意替换为空；不是有效新边被无条件丢掉 |
| 持久化 / Raft FSM | `internal/store/repository.go:2076` 发布完整快照；`internal/store/replication.go:446` 编解码；`internal/consensus/raft.go:417` 调用 ApplyReplicatedState | 离线完整状态往返保留 0/1/2 条边；线上三节点同 revision 同内容。无证据支持 Raft 丢字段 |
| 过期观测防护 | `internal/store/repository.go:1813` 拒绝不晚于 watermark 的观测；`:1817` 验证 inventory generation | 旧时间戳不能覆盖新状态；时间更新但身份缺失的观测仍会发布，这两者必须区分 |
| Health Evaluator | `internal/store/repository.go:1706` 要求新鲜探测、唯一 primary、standby streaming 与健康入边 | degraded 是上游缺证据的结果；不能放宽健康规则遮盖问题 |
| Topology API | `internal/api/clusters.go:309` 直接输出仓库 TopologySnapshot，`pkg/model/topology.go:58` 明确保留 links | 未发现按状态过滤 links 的序列化路径 |

当前故障链：

```text
standby probe 缺少可用 primary_node_id（由线上投影与源码推断）
  -> SourceIdentity 缺省 + standby degraded
  -> Adapter.Topology 原生 links=0
  -> 新 authoritative observation 替换旧边
  -> 空边快照持久化并复制到三个控制节点
  -> 严格健康判定 degraded
  -> API 如实返回 links=[]
```

## 5. 离线对照复现与已执行检查

复现调用实际 `Adapter.Discover`、`Adapter.Topology`、`identity.InstanceKey`、`Repository.ApplyDiscoveryRefresh`、`ReplicatedState`、`ApplyReplicatedState` 以及 Topology JSON 编解码。只用内存 fixture，没有数据库或生产 Raft 写入。

| 输入条件 | Adapter 边数 | 实际仓库边数 / 健康 | 与现场的关系 |
| --- | --- | --- | --- |
| 上游 GUC 为空，standby 已 streaming、只读、lag=0 | 0 | 0 / degraded | 与现场一致 |
| 上游 GUC 是 pg02 原生 UUID | 2 | 2 / healthy，pg02 -> pg01 与 pg02 -> pg03 | 原生身份绑定正向对照 |
| 上游 GUC 错填 pg02 平台 UUID | 2 | 0 / degraded；两个 standby 的 Adapter 健康均 healthy | 与现场 SourceIdentity 缺省、standby degraded 的组合不同 |
| 上游 GUC 仍是旧主 pg01 原生 UUID | 2 | 1 / degraded；自环被拒，剩 pg01 -> pg03 | 说明过期合法身份另有风险，不能只验证 UUID 格式 |
| 上游 GUC 是非空非法 UUID | 0 | 本 fixture 未回放仓库，两个 standby discovery 报错 | 与现场成功观测不同 |
| 正确快照后到达旧时间戳空身份观测 | 0 | 拒绝 ErrStaleObservation，仍为 2 / healthy | 旧 observation 没有覆盖新 observation |
| 正确快照后到达更新的空身份观测 | 0 | 0 / degraded | 复现重新降级的后段机制，但不是现场变更时间线证明 |

fixture 中的 application_name/slot 字段只是额外输入；真实 SQL 从未选择这些字段。这不是对真实数据库复制槽做集成测试。

非法 GUC 场景只验证 Adapter 错误，不声称整轮调度一定不提交：`internal/discovery/service.go:189` 会将单节点 database probe failure 转为失败探测记录，整轮仍可能发布降级快照。

共 5 个 Adapter 对照场景、6 个 Repository 顺序场景通过断言。每个 Repository 场景的完整状态在另一内存仓库恢复后相同，API JSON 往返也相同。现有 store 定向测试覆盖实际批量 discovery 单次 consensus commit、过期观测、身份绑定和快照恢复；不等同于生产故障注入。

已执行：

```sh
go run ./tools/diagnostics/pg-topology-card01
go test ./adapters/postgresql ./pkg/identity -count=1
go test ./internal/store -run 'Test(ApplyDiscoveryRefresh|DiscoveryRefreshBatch|DiscoverySnapshot|DiscoveryWatermark|TopologySnapshot|Repository.*(Snapshot|Replicated|Backlog)|SnapshotRevision)' -count=1
go vet ./tools/diagnostics/pg-topology-card01
go test ./adapters/postgresql ./pkg/identity ./internal/discovery ./internal/store ./tools/diagnostics/pg-topology-card01 -count=1
```

最终复核中，PostgreSQL Adapter、identity、discovery、store 四个包全部通过；诊断包编译通过（该包没有 Go test 文件，其 11 个场景由 go run 中的断言执行）。以上是故障复现与现有行为验证，不是修复完成声明。

本轮输出保存在 `tools/diagnostics/pg-topology-card01/offline-reproduction.json`。诊断程序刻意断言当前缺陷行为，仅用于第一卡根因复现；将来修复查询后，应同步更新或归档，不能把它当作要求缺陷永远存在的产品回归测试。

## 6. Bootstrap Env 的独立结论

`deploy/docker-swarm/postgresql/clusterguard-postgres-entrypoint.sh:36` 的 `replace_primary=false` 分支保留现有 `clusterguard.primary_node_id`；只有本次新建 PGDATA 的 `bootstrapped_now=true` 分支才从 Env 写上游 GUC 与初始连接信息（`:80`、`:108`）。`internal/discovery/scheduler.go:75` 也没有每轮从 Docker Service Env 覆盖主节点的逻辑。

因此，**目前不能归因为“每轮 discovery 把旧 Env 强行写回去”**。还有两项不能漏掉的风险：

1. 空 PGDATA 重建仍使用静态旧 source_host / primary_node_id；恢复后的权威来源必须另行设计。
2. slot 02/03 的启动路径在判断 PGDATA 是否已存在之前，仍会根据 Env 的 source_host 重写 `/var/lib/postgresql/.pgpass`（`:70`）。若当前复制恰好还使用此文件，旧 Env 可能影响重启后的认证；本轮未确认线上当前 passfile 路径，不能宣称已经排除所有 Env 运行期影响。

这两点保留给后续卡的启动/恢复审查；本次未重启容器，也未将 Env 改成新的主库地址。

## 7. Recovery Commit 与安全待办

本卡没有实现 Recovery Commit，也没有将“历史 unplanned shutdown”归零或删除。后续应原子提交当前主库、desired roles、复制源/边、expected/actual running、incident recovered 与 recovery succeeded；历史 unplanned 分类必须保留但不能继续代表当前异常。VIP、writer lease、reconcile、自动 failover 的释放条件不能因本次诊断而放宽。

一键灾难恢复仍留在第六卡。Timeline DAG、fork LSN、旧分支独立业务 COMMIT、system_identifier 和 checkpoint 校验未执行；没有以 max(LSN) 自动选主。用户关于故障时 majority fencing 正确的约束原样保留。

安全审查发现 `internal/agent/command.go:16` 会把命令 CombinedOutput 放进错误，`internal/agent/protocol.go:558` 的 publicAgentError 只截断而不脱敏。存在错误输出把连接秘密送入上层返回的风险；本轮未重放历史泄密脚本，也未证明所有日志/API/CLI/UI/timeline/support bundle 出口均安全。第七卡必须统一脱敏并测试嵌套错误、URL/conninfo/password/token/passfile 等形式，完成后再按授权窗口旋转已暴露的 replication secret。截断不能替代脱敏。

## 8. 第二卡入口与停止点

第一卡完成后停止，不自动进入修复。第二卡应限定为 PostgreSQL 身份证据采集/解析，不改健康阈值、fencing、自动选主和生产数据库设置：

1. 用 `tools/diagnostics/pg-topology-card01/read-only.sql` 在实际注册的三个 PG 端点补证，只读获取 native node/primary ID、receiver sender endpoint、Timeline 与复制对端；不要输出完整 primary_conninfo 或任何秘密。该 SQL 已提供，但本轮未在现场执行。
2. 将原生身份与平台 ID 显式区分。运行期关联需要相互印证的主库 sender 证据、standby receiver 证据、注册实例原生身份、system_identifier 与 Timeline；不能只靠任意合法 UUID、IP、应用名或“唯一 primary”猜边。
3. 为缺失身份、错误平台 UUID、重复/歧义应用名、旧主身份、跨 system_identifier、断流、冲突 Timeline 和动态上游变更建立反例，再实施最小修复。
4. 后续修复验收必须连续至少 50 个真实后台 discovery 周期保持 pg02 primary、pg01/pg03 standby、全 healthy 且恰好两条预期边。不能把 50 次同一 observed_at 的 GET、单测循环或手工刷新计作验收。

本轮仅新增此报告、白名单证据、离线诊断程序及只读 SQL；没有修改产品源码、发布版本、打包、部署、执行恢复或旋转凭据。原工作树中已有的图片删除保持不动。
