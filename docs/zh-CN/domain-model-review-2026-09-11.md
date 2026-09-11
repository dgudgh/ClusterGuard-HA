# 领域模型评估复核

## 结论与范围

本记录先复核用户提供的 30 项领域模型评估，再按用户后续“修复代码”的要求处理实际复现的三类问题：验证结果一致性缺口、IPv6 地址格式、零值执行时间的 JSON 表达。不把 30 项都当作当前缺陷，不按原报告直接实施模型重构。下文的复现与 30 项判定保留修复前基线；后续实施与最终验收单独记录在文末。

- 当前基线：`33033fa9795fd02520a0ea1962857d013243c320`，工作树 `.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`。
- 已复读 `AGENTS.md`；沿用已读取的发布验收边界。既有图片删除、预览和现场 JSON 不属于本轮。
- 原报告引用的代码规模与初始内核提交 `0ec7a37` 高度吻合：该提交的 `model.go` 206 行、`workflow.go` 110 行、store `repository.go` 348 行、workflow `workflow.go` 298 行；当前分别为 307、191、2467、710 行。原报告并非当前分支的完整审计。
- 当前 `pkg/model` 不止两个源文件，还包含拓扑、恢复、启停、授权和身份认证模型；检查模型时同时读取了 store、workflow、identity、API 及相关适配器的消费者。
- 此处的 P1/P2/P3 表示当前证据支持的优先级。没有从这份报告中确认可直接归为现场 P0 的新故障；这不等于证明系统没有其他 P0。

## 已复现的问题

### P1：矛盾的 Verification 仍能成为成功结果

调用链：

1. [旧工作流执行](../../internal/workflow/workflow.go) 的 `candidate.Verify` 后主要判断 `verification.Passed`。
2. [持久化工作流](../../internal/workflow/durable.go) 的执行收口同样依赖该布尔字段。
3. [人工复核](../../internal/workflow/prepare.go) 的 `Service.Verify` 允许据此将 `indeterminate` 改成 `succeeded`。
4. [store 操作持久化](../../internal/store/operations.go) 的 `verificationReconciliationAllowed` / `applyOperationTransition` 没有统一拒绝 `Passed=true` 与失败 Checks 的矛盾；成功记录也能重新从磁盘读取。

隔离适配器返回：

```go
model.Verification{
    Passed: true,
    Checks: []model.Check{{Name: "writer_unique", Status: model.CheckFail}},
}
```

三个期望“拒绝成功”的测试均失败，实际分别为：

- 旧执行路径：`status=succeeded, err=nil`，报告也记为成功。
- 持久化执行：`execution=succeeded, record=succeeded`；用独立临时文件仓库落盘并重新打开后仍保留 `Passed=true` 和失败检查项。
- 人工复核：先合法进入 `indeterminate`，再返回上述矛盾证据，记录变成 `succeeded`。

这确认了编排与存储边界的防御性缺口，但没有证明现有四种适配器在现场已经产生这种矛盾，也没有证明外部请求可以任意注入适配器验证结果。因此不把隔离夹具的结果写成现场数据库已发生误切换。

旧版对照：`0ec7a37:internal/workflow/workflow.go:196-209` 已只判断 `Passed`。这是从早期实现保留下来的缺口，不是上一轮测试文件归并引入。

建议修复边界：先明确 Verification 的共同成功契约，再覆盖普通执行、持久化执行、人工复核、历史成功快速返回和最终存储边界。至少应让失败 Checks 否决成功；未知状态、空 Checks、WARN 的语义需结合现有适配器和历史记录确认，不能把“无检查”自动当成“全部通过”。已提交操作验证不充分时应保留 `indeterminate`，不自动重提操作，不解除 fencing 或维护门禁。

### P2：IPv6 端点别名格式错误

[EndpointAddress](../../pkg/model/model.go) 使用 `fmt.Sprintf("%s:%d", host, port)`。对 `::1` 生成 `::1:5432`，`net.SplitHostPort` 返回 `too many colons in address`。

该函数用于 [store](../../internal/store/repository.go) 的地址冲突比较与旧地址别名保留，所以不能简单当成纯展示文字。当前复现仅证明地址格式错误；没有运行 IPv6 数据库连接，也没有据此断言当前 IPv4 测试环境受影响。

旧版对照：`0ec7a37` 中该函数已是同样的实现。后续修复宜基于 `net.JoinHostPort`，覆盖 DNS、IPv4、IPv6、空地址和端口边界；还需检查已有带方括号别名、历史数据兼容及 IPv6 同址不同写法，不能只替换一行就宣称完整 IPv6 支持。

### P3：结束时间的 omitempty 不生效

[Execution.FinishedAt](../../pkg/model/workflow.go) 为 `time.Time`，使用 `json:"finished_at,omitempty"`。本机 Go 1.26.5 的 `encoding/json` 实际将零值序列化为 `"0001-01-01T00:00:00Z"`，而非省略字段。

原报告将其描述为“应给 StartedAt 也加 omitempty”并不能解决问题；同类型的零值时间仍然会输出。`StartedAt` 并非所有执行都永远为零，未开始操作与真正执行过的操作必须分开讨论。后续可在 API DTO 中明确未开始/未结束语义，或评估可空时间与兼容序列化方案，不直接全仓改指针而改变快照摘要或已发布 JSON 契约。

## 30 项逐条判定

“增强项”表示可能有价值，但未凭这份材料证明当前功能错误；“部分成立”不等于按原严重级别接受全部推论。

| 原编号 | 原议题 | 当前判定与依据 |
| --- | --- | --- |
| 1 | UUID 随机源失败 panic | 未证实为 P0/远程拒绝服务。代码仍在，但本机 Go 1.26.5 的 `go doc crypto/rand.Read` 明确该函数不会返回错误，随机源失败会在标准库内不可恢复终止。`go.mod` 最低 1.22 不代表所有旧工具链都具备同一实现。本轮未注入操作系统随机源故障；不能为可用性回退到时间戳或低熵 ID。 |
| 2 | EngineIdentity 未统一大小写 | 原 P0 推论不成立：`identity.InstanceKey` / `ClusterKey` 在实际身份索引入口对已知键的值 TrimSpace/ToLower，store 查找和 reconcile 均复用它；已有四引擎测试。`Clone` 保留原始证据是合理行为。键名目前严格区分，错误键会缺少必需身份而被拒绝；要支持别名键必须先拒绝规范化后冲突，不能无条件改为小写覆盖。 |
| 3 | cluster.Health 从未写入 | 已过时：`ApplyDiscoveryRefresh` 先调用 `strictTopologyHealth`，再把同一健康结果写入 cluster 和 topology snapshot；元数据变化会显式失效为 unknown。不能根据初始版本认定当前 API 永远返回零值。 |
| 4 | Endpoint 表为空、没有同步 | 已过时且混淆配置与观测：当前有持久化 Endpoint 清单、发现时绑定，以及 `ReconcileMetadataCoordinates` 原子更新实例坐标和对应 Endpoint，并使旧观测失效。低层 `ReconcileInstance` 不是整个 API 写入链路。`EndpointAlias` 类型缺少完整独立管理流程属于待评估遗留模型，不等于必须删除整个 Endpoint 权威清单。 |
| 5 | lag 指针不能区分未测量/零值 | 不成立：隔离 JSON 往返测试区分了 nil 与 0；候选评估会拒绝未知 lag。复制模式、通道、槽的统一结构属于增强项；当前还有 `ReplicationStatus`、`NativeReplicationLink` 和引擎元数据，不能说完全无复制证据。 |
| 6 | Operation 无 TargetID 导致目标丢失；无超时 | 目标丢失已过时：`OperationRecord.TargetID`、`OperationPlan.TargetID` 和解析器绑定实际目标。优先级、截止期、业务原因属于需求扩展。当前有 request/lease context、分引擎 verification deadline、Agent 命令超时和 PG 长任务超时；仍可评估统一端到端期限，但不能由 Operation 没有字段推断所有命令无限等待。 |
| 7 | Execution 时间零值 | 部分成立，见 P3 实测。给 `time.Time` 简单追加 omitempty 不能修复零值省略语义。 |
| 8 | 无结构化错误 | 部分过时：Execution 仍是状态与消息，但 `OperationRecord.FailureClass`、`StepAttempt.FailureClass` 及错误分类接口已存在。统一更多 code/exit-code 是契约增强，不是当前所有错误都只能解析字符串。 |
| 9 | Passed 与 Checks 不一致仍成功 | 成立，P1；三个真实工作流入口的隔离失败测试已复现，持久化结果重新打开后仍存在。 |
| 10 | HAEndpoint 缺优先级/跨区域信息 | 增强项。当前写入口追求唯一归属并用 provider/owner 约束；不能未经业务设计增加多主或按权重迁移写入口。 |
| 11 | 无 SchemaVersion，添加字段必然破坏快照 | 推论不成立：JSON 新字段并非自动破坏向后读取；当前已有 `normalizeSnapshot`、状态 revision/digest/CAS 和旧字段默认迁移。显式 schema 版本可评估，但不能混同 MetadataRevision，也不能顺手修改已签名快照契约。 |
| 12 | Labels/Owner/Environment 缺失 | 部分成立的产品增强：集群/实例缺通用标签；`RuntimeTarget.Labels` 和平台用户/角色已存在。不按未来组织维度需求将其列为安全 P1。 |
| 13 | Health 缺 source、结构化错误与分位值 | 部分过时/增强：`ProbeStatus` 有 EndpointID、InstanceID、Outcome 和发现/指标观测时间，`MetricSample` 单独承载指标。P95/P99 需窗口聚合语义，不宜塞进单次 Health 就当作 SLO 已实现。 |
| 14 | AuditEvent 空壳、无防篡改链 | 夸大：已有 OperationID、Stage、Actor、时间及结构化 `SecurityEvent`，与操作记录关联；store 校验、脱敏并在最终收口写审计。哈希链、外部可信锚和结构化载荷是另立威胁模型的增强项，不宣称现有审计具备防篡改保证。 |
| 15 | Report 只有空标题列表 | 已过时/增强：Report 有 Status、Summary，操作详情还保留计划、尝试、执行和验证。富文本报告或前后快照属于扩展，不是 `/reports` 没有内容。 |
| 16 | anomaly kind/severity 是 string | 成立的维护议题，不是已证实 P0；ResourceMeta 已提供创建/更新时间，显式 detected/resolved 生命周期和枚举需要兼容旧值。 |
| 17 | WorkflowStage 没有转换约束 | 已过时：store 检查 stage 顺序、状态合法性、终态不可变、CAS 和人工复核例外；启停模型另有 `ValidPowerTransition`。这不证明状态机完备，但不能称完全无约束。 |
| 18 | Check 没有 stage/time | 主要是契约选择：Checks 分别挂在 Precheck、Plan、Verification 等父记录，Verification 有 ObservedAt。单条检查时间与结构化 details 可增加，但要先明确消费者需求。 |
| 19 | Controller 无 leader/version | 已过时的范围判断：控制平面运行态由 `internal/consensus.Status` 和 `internal/api.ControlPlaneStatus` 等承载，不能只读持久化 Controller 声明就认定未实现。 |
| 20 | DatabaseNode 只有三项字段 | 已过时：现有 NodeName、Kind、Hostname、IPAddress、Aliases、Active、PlatformID，以及实例 NodeID/WorkloadBinding。归属关系不必重复存双向引用。 |
| 21 | Engine.Valid 每次分配 slice | 未复现；Go 1.26.5 `testing.AllocsPerRun(1000, ...)` 得到零次堆分配。四项固定扫描没有证据需要改可变全局 map，更不是已证实性能故障。 |
| 22 | UUID 大小写和全零值 | 事实部分成立：当前 ValidResourceID 是格式校验，接受大小写十六进制及全零格式值。是否限定版本/variant、拒绝全零、规范化外部 ID 应按入口与历史库存审查；不能全仓替换并破坏已有外键或授权绑定。 |
| 23 | omitempty 不一致代表必填校验失效 | 推论错误：omitempty 控制序列化，不是必填校验；必填字段不省略通常合理。真实零时间问题已独立记录为第 7 项，不能把 tag 风格表直接作为缺陷表。 |
| 24 | EngineIdentity 无强类型 schema | 增强项。当前以 identity 包的分引擎必需键解析为边界；需要类型化时应保留各引擎原始证据及适配器兼容，不能用一个通用大小写 map 规则替代。 |
| 25 | Endpoint.Active 语义含糊 | 可补契约说明，但当前 Active 用于清单启用/参与发现，实际 VIP 归属和健康由 HAEndpoint 的 OwnerID/Healthy 等表达。不能直接把 Active 重命名成 Bound 从而改变消费者理解。 |
| 26 | EndpointAddress 不支持 IPv6 | 成立，P2；生成不带方括号的 IPv6 host:port 已复现。影响的是当前调用者的地址/别名契约，未做真实 IPv6 连接验收。 |
| 27 | pkg/model 总共只有 26 行测试 | 已过时：当前三个测试文件包含 14 个顶层 Test；身份、库存与操作契约还在调用包测试。Clone、地址、UUID 边界的直接单测仍可补充，本轮 overlay 发现了未覆盖的失败，不把现有测试通过当作完备。 |
| 28 | 没有 model.Validate，所以 API 完全不校验 | 不成立：API 元数据路径及 store 已有引擎/身份/地址/端口/归属/计划等验证，且有拒绝与原子失败回归。统一 model.Validate 是分层选择；依赖库存、权限和拓扑的校验也不能只放纯模型。 |
| 29 | Operation.Engine 冗余必然失配 | 未证明当前缺陷。操作保存引擎是执行时快照，解析器核对集群，元数据路径明确从受校验实例绑定引擎；不应移除历史操作的引擎快照。 |
| 30 | OperationKind 只有四种，新增节点没有流程 | 已过时：常规操作已有七种 kind；节点生命周期、灾难恢复、启停和平台升级分别有流程/模型。是否合并这些协议需独立架构设计，不靠往一个枚举加值解决。 |

## 修复前验证证据

临时测试通过 Go overlay 挂入现有测试包，文件在本地 `.build/model-review-20260911/`，不改动原测试文件、不连接数据库、不把失败测试加入产品构建：

```bash
go test -count=1 -overlay .build/model-review-20260911/overlay.json \
  -run '^TestReview' -v ./pkg/model ./internal/workflow
```

该命令预期暴露问题，实际退出码为 1，不计为验收通过：

| 测试 | 实际结果 |
| --- | --- |
| TestReviewLegacyRejectsContradictoryVerification | FAIL，错误成功及报告已复现 |
| TestReviewDurableRejectsContradictoryVerification | FAIL，落盘后重新打开仍错误成功 |
| TestReviewManualVerifyRejectsContradictoryVerification | FAIL，人工复核错误清除 indeterminate |
| TestReviewIPv6EndpointAddress | FAIL，host:port 解析失败 |
| TestReviewExecutionOmitEmptyFinishedAt | FAIL，实际输出零值时间 |
| TestReviewLagJSONDistinguishesUnknownAndZero | PASS，nil/0 往返保持区分 |
| TestReviewEngineValidDoesNotAllocate | PASS，本机工具链零堆分配 |
| TestReviewClonePreservesRawEvidenceAndIsIndependent | PASS，复制不修改原始值且不共享 map |

原始输出为 `probes.jsonl` / `probes.stderr`。不是全部现有单测失败，而是五个新加入隔离评审的反例断言失败，分别对应上面的三类问题。

原有九包无缓存回归已完成：`pkg/model`、`pkg/identity`、`internal/store`、`internal/workflow`、`internal/api` 及四种适配器，共 1138 个测试及子测试通过，零失败、零跳过。命令为 `go test -json -count=1 -p 1 ./pkg/model ./pkg/identity ./internal/store ./internal/workflow ./internal/api ./adapters/...`，PATH 含 Node。原始结果为 `existing-tests.jsonl` / `existing-tests.stderr`。该命令不包含原生 Agent 恢复或脚本环境测试，不是全仓或真实数据库验收。

`go vet ./pkg/model ./pkg/identity ./internal/workflow ./internal/store` 通过；本文七个相对文件链接均存在。再次确认跟踪的业务代码与基线无差异，临时 overlay 仅用于评审反例，不属于修复或发布内容。

不会以既有用例通过覆盖上述五个反例失败事实。本轮没有浏览器、SSH、真实数据库、Raft 集群、上传验签、滚动升级或新安装包验收。

## 后续修复次序

1. Verification 成功契约：明确旧行为与期望结果，补入正式回归，修三类执行入口与存储收口；再扩展失败/未知/空 Checks、已完成历史记录、进程重启和失败日志保留场景。
2. EndpointAddress：保持 DNS/IPv4 行为、修 IPv6 格式，并单独验证地址碰撞和已有别名兼容。
3. 时间 DTO：先确定未开始/未结束的对外表达，再评估升级/回滚兼容，不联动修改无关字段。

这是诊断阶段确定的顺序，实际实施见下文。本轮没有证据支持以“统一归一化全部身份、随机数失败降级、放宽健康、删除 Endpoint 表”作为安全修复。

## 后续修复：修改前记录

用户已明确要求修复代码。本节在第一次业务代码编辑前建立；以上内容保留为修复前证据，最终状态另见本节验收结果。

- 基线仍为 `33033fa`，业务代码相对该提交无差异。已复读 AGENTS 和恢复发布清单；前轮文档尚未提交，图片删除及现场文件仍不纳入本次修改。
- 旧版对照沿用已完整读取的 `0ec7a37` 定义与执行链，以及本轮再次检查的 store `applyOperationTransition`、workflow `detachedVerification` / `Service.Verify` / `terminalRecordResult` / abandoned / automatic continuation。三个问题均不是测试文件归并引入。
- 修复前同路径证据为上述五个失败反例；九个关联包原有 1138 项回归通过。临时测试将转成对应功能文件中的正式回归，不删除原有测试。
- 成功契约：显式 Passed=true、Checks 非空且所有状态仅为 pass/warn；fail、空/未知状态、缺失检查或 Passed=false 均不通过。WARN 不自动升级为失败，保留原有各引擎警告政策。
- 所有 workflow 验证出口、历史成功快速返回、失联收口、自动续跑和 store 成功写入都使用同一契约。已产生变更但未证实成功的结果保留 indeterminate；旧历史不回写为成功、不自动重提执行，旧快照仍须可读取。
- IPv6 使用标准库 host:port 构造，规范化合法 IP（含方括号输入）；DNS/IPv4、空地址、无效端口的已有边界保留。增加压缩/完整 IPv6 同址冲突及历史别名保留测试。
- Execution 的时间字段不改变模型/快照 JSON 编码；仅在 API 输出边界使用可空时间 DTO，零值不输出，非零时间和其他字段保留。普通、诊断、列表/详情及嵌套响应均需覆盖；源记录不被改写，脱敏不减弱。
- 计划：先加入失败回归；实施上述三个范围；运行受影响包、全仓、race、vet、Linux 构建及适用浏览器回归；记录环境跳过和未部署项。安装包和 `v2.2.101` 不覆盖，不宣称新代码已在现场生效。

## 修复实施

### 验证成功契约

- `model.Verification.Successful()` 统一约束显式结论及检查明细。工作流入口还核对检查结果所属操作，适配器报错时不能返回成功结论。
- 普通执行、持久化执行、人工复核、自动续跑、失联操作收口，以及 store 的 Transition/Finalize 成功写入使用同一契约。新写入的成功记录不得缺失检查证据；不确定的变更仍保留 `indeterminate`。
- 老快照可以读取，原始历史不迁移或改写。重复执行/复核遇到矛盾的历史成功证据时返回需复核，不重新执行适配器，不把历史状态直接当作安全依据。历史展示仍保留当时记录，不声称完成全量历史修复。
- 保留原有 WARN 策略。未改各引擎的选主、复制、权限、fencing、writer lease、VIP、维护门禁或多数派条件。
- 正式反例先在修改前运行：四种引擎的普通/持久化/人工复核共 84 个子用例，其中失败明细、未知明细、空状态、缺失明细的 48 个反例子用例失败；IPv6/zone 两个及 API 零时间两个子用例也失败。之后才修改业务代码。
- 四个已有测试的成功样本缺少检查明细，现补充明确 pass 证据，保留原断言。磁盘持久化失败测试额外排除校验失败，避免提前被新校验拦住却误报该故障路径已覆盖；跨操作审计拒绝测试也提供合法验证样本后再验证原子拒绝。

### 地址与时间

- 合法 IP 用 `net/netip` 解析，host:port 用 `net.JoinHostPort`；IPv6 完整/压缩/带括号写法统一，zone 保留。DNS、IPv4 和非正端口的旧行为保留。没有把此修复写成全产品 IPv6 网络支持已验收。
- 端点冲突通过真实 store 注册入口验证；地址变更后的 UUID、旧裸 IPv6 别名和新增规范端点别名经落盘重开检查。
- 新增私有 HTTP DTO，只省略零值 StartedAt/FinishedAt。未修改 `model.Execution` 类型、tag 或模型 MarshalJSON，避免改变持久化快照内容摘要。
- 覆盖普通/诊断输出、嵌套列表、指针、nil/空列表、只开始未结束、带偏移的非零时间及真实 HTTP 列表/详情路由；普通模型 JSON 编码不变，历史矛盾记录可经复制状态恢复并重新打开，检查证据不被改写。

## 修复后验收

以下命令均已结束。执行环境为 macOS arm64、Go 1.26.5，Go 测试 PATH 包含 Node；浏览器使用本机 Chrome，载入当前真实 `console.html` 与隔离接口夹具。

| 检查 | 结果 | 本地证据 |
| --- | --- | --- |
| `go test -json -count=1 -p 1 ./...` | 38 包、2382 个测试及子测试通过，零失败，14 个环境依赖测试跳过 | `full-tests.jsonl` / `full-tests.stderr` |
| `go test -race -json -count=1 -p 1 ./pkg/model ./internal/workflow ./internal/store ./internal/api` | 4 包、823 个测试及子测试通过，零失败、零跳过，无 race 报告 | `race.jsonl` / `race.stderr` |
| `go vet ./...` | 通过 | `vet/stdout.log` / `vet/stderr.log` |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` | 通过；仅交叉构建检查，不是安装包验收 | `linux-build/stdout.log` / `linux-build/stderr.log` |
| `tools/console-bootstrap-audit.cjs` | 5 项通过 | `bootstrap-audit/result.json` |
| `tools/console-node-safety-audit.cjs` | 31 项通过 | `node-safety-audit/result.json` |
| `tools/console-engine-pages-audit.cjs` | 73 项通过，包含四引擎、八页面及宽/窄屏检查 | `engine-pages-audit/result.json` |
| `tools/console-log-pagination-acceptance.cjs` | 7 项通过，第一页/详情按需读取、加载更多、筛选、旧请求和窄屏边界保留 | `log-pagination-acceptance/result.json` |
| 文档链接与补丁空白检查 | 7 个相对文件链接均存在；`git diff --check` 通过 | 本文及 Git diff |

完整输出与浏览器截图放在本地忽略目录 `.build/model-fixes-20260911/`，不把生成证据或临时诊断脚本加入产品代码。

跳过的 14 项逐类说明：9 项需要 `CG_PG16_BIN` 的原生 PostgreSQL WAL/恢复/50 周期发现测试；3 项需要 `CG_MYSQL_RECOVERY_TEST_IMAGE` 的真实 MySQL 恢复测试；1 项需要 `CG_DOCKER_INTEGRATION_TESTS=1` 的 PostgreSQL entrypoint 测试；1 项需要显式只读 Agent 配置的停库 Docker 证据读取测试。跳过不计为通过，四引擎工作流夹具也不等于四引擎真实数据库已验收。

修复前的零时间 overlay 测的是模型 JSON；该编码为兼容快照刻意不改。本轮用正式 HTTP DTO 与路由回归验收零时间修复，不伪称旧 overlay 的模型省略断言已通过。

本轮没有改页面布局、升级器或部署脚本，没有打新安装包或补丁包，没有 SSH、真实 IPv6 连接、现场上传验签或滚动升级验收，没有部署到 `192.168.102.152–154`。已发布 `v2.2.101` 仍指向 `78dbdbffc9645dd22c9867888cd11b4f2dc2bd89`，不覆盖旧制品。仅提交这三类已复现问题的代码、正式回归及本文，不声称整个项目或原报告的 30 项均已闭环。
