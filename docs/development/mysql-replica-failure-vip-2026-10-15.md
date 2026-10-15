# MySQL 从库故障时保留现役主库 VIP

## 修改前基线与门禁

- 用户要求修复：主库正常、两个从库数据库或复制故障时，不能因从库失去半同步ACK而撤销主库VIP；两台整机/Agent失联及控制面失去多数派继续执行现有隔离保护。
- 分支为唯一主线 `codex/2.2-postgresql`，HEAD `d0ce098d26468410acb7e26e5761f019d4e5ddce`。进入时已有生产cnf/离线路径相关暂存改动，全部保留。本次不修改用户生产参数，不创建包、不部署现场。
- 已读门禁执行与更新流程、完整升级与热修强制契约v2、规则入口、修改与证据规则、操作授权规则、后端操作编排及观测模块说明。`CG-UPGRADE-CONTRACT`；`contract_version=2`；`contract_loaded=true`。适用阶段为SOURCE，ART/FIELD未执行。
- 旧版对照：`git show c8795a3:internal/coordination/ownership_keeper.go` 已要求主库和对应探针均为healthy；当前HEAD保留该判定。`f76b2f4`仅增加非MySQL引擎禁止重启引导的保护，不是本次缺陷的起点。当前工作树未修改该调用链。
- 缺陷调用链：`adapters/mysql/probe.go`在半同步必需且无ACK时保留primary角色及可写事实、将Health标记为degraded；`writablePrimary/currentSuccessfulProbe`拒绝该实例；Keeper无法续租；现役稳定租约过期后Agent执行撤VIP和只读隔离。已有续租/多数派测试未覆盖从库ACK全部丢失的场景。
- 历史补充：`b33b259`引入半同步必需时的Health降级；旧版Keeper的healthy限制未同步区分数据库写入事实与副本耐久性降级，形成该组合缺陷。

## 修复边界与不变量

- 半同步降级和告警继续保留，不关闭半同步、不改超时、复制、只读启动或任何生产cnf参数。
- 仅允许明确的“半同步配置有效但缺少ACK”原因进入现役主库续租分支；连接/权限/插件缺失/配置错误/未知原因、主库只读或不新鲜的探针全部拒绝。
- 必须有唯一当前主库、新鲜SQL探针、同一实例的实际VIP归属、规范VIP元数据及现有观测覆盖。只发出`RenewOnly`，持久租约存储核对同集群、同VIP、同Owner、同操作且未过期；不得新建租约、改变Owner、零Owner引导或自动提升从库。
- 保留Raft多数派、Agent签名、租约有效期、双主/双VIP/主库失联、数据Agent观测多数派及灾难恢复隔离。PostgreSQL和正常健康主库流程保持原有规则。

## 验证计划

1. 先增加ACK故障回归，在旧代码确认失败；覆盖两个从库停止或复制报错，主库仍可写且Agent在线。
2. 验证结构化降级原因来自实际Discover结果；插件/权限/配置错误不能取得该原因，健康状态仍为degraded。
3. 用真实LeaseStore连续跨多个TTL续租同一现役Owner；缺失/过期/外来Owner或操作的租约拒绝，主库只读、证据过期、双主/双VIP、数据探针不足、多数派失效及恢复冻结均拒绝。
4. 运行受影响Go单元/集成及race、必要的API/Agent回归；新增SOURCE门禁和失败变异，并执行source strict、self-test、许可、Markdown台账和HTML重建。
5. 独立审查实际差异；现场故障注入、升级、真实数据库/VIP回读为FIELD OPEN，不把隔离夹具当成现场结果。

## 结果

- 已复现旧行为失败：分类矩阵中现代/旧术语的无ACK主库共4个子例不产生明确原因；现役VIP连续续租回归的reachable/metrics_unavailable两个子例在首轮被旧Keeper拒绝，报告`ambiguous additional primary`及`reboot bootstrap requires every instance to be fully read-only`。
- 实现：Discover仅在半同步配置有效但无ACK时记录`semi_sync_ack_missing=true`，仍保留degraded；Keeper新增`ownership_ack.go`窄分支，重新核对配置、当前SQL探针、唯一可写主库及实际VIP归属，强制`RenewOnly`，不提交Owner元数据。稳定租约存储原有的锁内同Owner/同操作/未过期检查保持不变。
- 行为回归通过：真实Discover解析SQL夹具结果→当前批次snapshot→Keeper→实际LeaseStore；连续5轮模拟155秒及跨模块3轮均保持原LeaseID。正向包含主库普通SQL可达/仅指标失败、两从库数据库不可达或复制停止；负向覆盖零Owner、缺失/过期/外来/transition租约、主库只读/维护、探针失败/过期、双主/双VIP、元数据错误、部分观测缺主库或不足多数、控制面无多数和DR冻结。ACK恢复后正常主库原路径也保留。
- 本机完整回归：`go test ./internal/coordination ./adapters/mysql ./internal/discovery ./internal/agent ./internal/api -count=1`全部通过；coordination全包race通过，最终跨模块及负向定向回归再次通过。
- SOURCE严格门禁38/38；新增8个失败变异，合计91个变异全部命中、两个对照通过；许可68项通过。独立安全审查最终结论为SOURCE通过、未发现阻断项，已补零Owner和部分观测缺主库两项直接回归。Markdown台账11项通过，104页HTML生成器已运行；本批开发模块说明不在生成页精选集合内，生成页无新增变化。
- 边界：以上SQL和VIP/控制面观测使用隔离夹具与真实生产函数，不是现场故障注入。没有连接或修改服务器、没有安装、没有生成或重建交付包，ART/FIELD保持OPEN。
