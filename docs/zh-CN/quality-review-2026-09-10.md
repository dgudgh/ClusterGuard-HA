# 系统性质量提升（第三轮）执行记录

2026-09-10。本文件按 AGENTS.md 要求，在第一次业务代码修改**之前**记录基线、旧版对比与拟改范围。
本记录不把子代理结论当作证据；每条进入修改范围的项均由主代理回读源码确认。

## 一、范围与基线

实际源码目录 `.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD `19d02bc`
（与已发布 2.2-99/100 对应的产品主线）。历史分支不纳入本轮：

| 工作树 | HEAD | 定位 | 本轮 |
| --- | --- | --- | --- |
| 根目录 | 415afa6 | phase1 原型 | 不修改 |
| mysql-topology-intelligence | 6e158fa | 历史功能分支 | 不修改 |
| platform-auth-session | 19d02bc | 当前发布主线 | 修复范围 |

保护既有并行工作：该工作树已有上一轮未提交改动（7 类适配器/租约/前端修复、新增测试、图片删除、
预览与审计工具）。本轮只在其之上追加，不回退、不重排、不提交、不打包。

基线检查（修改前）：`go build ./...`、`go vet ./...` 通过；
`go test -p 1 ./... -count=1`（Node 与 `CG_PG16_BIN` 已注入）36 包全部 ok，含 `scripts` 222s。

未覆盖/无法验证：现场 152–154 未访问；Docker 容器类测试本机无环境；
Oracle/SQL Server 原生灾难恢复、PXC、密钥轮换不在本轮范围；未做发布与安装包验证。

## 二、本轮覆盖

新增审计面（前两轮未逐行覆盖）：控制台内联 JS 逻辑（console.html ~5.4k 行）、
可维护性与测试缺口、以及上一轮未提交改动集的**独立回归复核**。
既有已审计模块（租约/共识/适配器/权限/存储等）不重复逐行重审。

## 三、修改前旧版对比与问题卡

基线旧提交 `bc0546a`（2.2-68，2.2-99 之前的正式标签基线）。旧文件：`git show bc0546a:internal/api/console.html`（4637 行）。

### QR-01 登录/引导阶段：数据加载失败被当作会话失效（旧有缺陷）

- 旧版对照：`bc0546a` 的 `submitLogin` 同样在 `try` 内 `await loadClusters()`，`catch` 无条件
  `showLogin(...)`；`bootstrapConsole` 同。属旧版已有缺陷，非本轮引入，但违反 AGENTS.md
  「后台辅助接口失败不得冒充会话失效」。
- 现版位置：`submitLogin`（console.html 5212-5215）、`bootstrapConsole`（5276-5279）、
  `loadClusters` 的 `Promise.all(['/api/v1/clusters','/api/v1/capabilities'])`（5171-5173）。
- 现状行为：认证成功后 `/clusters` 或 `/capabilities` 返回 5xx/断连，异常冒泡到统一 catch，
  对已认证会话调用 `showLogin` → 清 `currentUser`、隐藏外壳、清自动刷新，且无重试入口。
- 复现：隔离 fixture 让 `/api/v1/clusters` 返回 503，用正确口令登录，观察是否被弹回登录页。
- 拟改范围：认证失败（401/staleSession）与登录后数据加载失败分离；加载失败保留已认证外壳、
  显示错误并提供重试。仅改控制台，不动权限后端。
- 必须保持：401 仍弹回登录；登录口令错误提示不变；危险操作在数据未就绪时保持锁定。

### QR-02 运行中“刷新/重试”与操作意图（已分析，本轮不修改）

- 现象：操作执行中点击“刷新所选集群”，`beginClusterRequest`→`relockSwitch(null)` 会置
  `activeOperationIntent.cancelled=true`，跟踪路径 `requireOperationIntent`（2723-2728）随即抛 409，
  控制台显示“操作已重新锁定…”，而服务端请求无 signal、可能仍在执行。
- 旧版对照：`bc0546a` 的 `beginClusterRequest(clusterId, preserveOperationResult)` 只有两个参数，
  尚无意图概念；现版 `refreshDiscovery`（5283）未传意图，属意图特性引入后的行为变化。
- **不改的理由**：既有发布回归 `tools/console-operation-intent-acceptance.cjs` 明确断言“预检进行中
  刷新后不得提交（提交数 0）”，AGENTS.md 也要求“刷新必须使旧确认失效”。因此刷新取消**待执行**的
  确认是既定且被测的正确行为，缺少区分“待执行确认”与“已提交跟踪中”的状态。
  按 AGENTS.md“不能越修越退步”，本轮不做通用“运行中禁止刷新/重试”（会破坏上述既定语义）。
  已撤回该尝试性改动，保留为待作者决策项（见第四节）。
- 后续建议（需作者确认语义后单独实现）：为“已提交跟踪中”引入显式状态，或在 `requireOperationIntent`
  失败于跟踪阶段时把结果分类为 `indeterminate`（结果待确认），而不是直接报失败。

### QR-03 PostgreSQL 计划摘要防篡改缺测试（测试缺口）

- 证据：`adapters/postgresql/execution.go` 有 `plan.Digest != digest` 拒绝分支；现有用例
  `TestPostgreSQLPlanRejectsMissingPinnedResourceRevision` 篡改后**重算**了 digest，只覆盖 revision。
  MySQL/Oracle/SQL Server 均有“保留旧 Digest”的篡改用例。此门被删/写错不会有测试失败。
- 拟改范围：仅新增测试，不改业务代码。

### QR-04 `pkg/gtid` 无直接测试（测试缺口）

- 证据：`pkg/gtid/gtid.go`（324 行）无 `_test.go`，仅经 `adapters/mysql` 别名层间接覆盖；
  `checkedAddTransactions` 溢出、非法 tag/interval 无直接断言。
- 拟改范围：仅新增表驱动测试。

## 四、已分析但本轮不修改（含理由）

- 控制台“运行中刷新取消操作意图”（QR-02）：与既有“刷新使旧确认失效”语义冲突且无状态可区分，
  见第三节 QR-02。待作者确认语义后单独实现。
- 后端 PG 恢复围栏清除（`internal/agent/recovery_start.go:88` 追加
  `default_transaction_read_only = 'off'`）：现场修复引入，位置在授权与 HBA guard 验证之后。
  经核对 `internal/agent/reconciler.go:178-214`，恢复保护仅在 **activation（Recovery Commit 后）**
  释放；放弃恢复时保护保持，业务访问仍被阻断，注释断言的“guard 仍拦截业务访问”成立。
  残留风险（放弃后外部监督者重启实例为可写但被 HBA 拦截）记录为现场复核项，不擅自改动安全路径。
- Oracle 执行阶段 revision 相等性分支（`adapters/oracle/oracle.go:671`）：该分支在
  `resolved.ObservationToken != ""` 时不生效；但 MySQL 的 `validateExecutionPlan`
  （`adapters/mysql/switchover.go:514-525`）是同一契约。属既有设计（有语义 observation token 时
  由 token 绑定），非本轮引入的退化，不改。
- `operation_lock.go` 持有本地互斥锁执行 Raft 写入（旧 #7）：仍无延迟测量与原子替代方案，不移走安全锁。
- 重复逻辑与死代码（摘要见第五节）：属重构，按用户要求“架构调整单独说明、不混入常规缺陷修复”，
  本轮不改。

## 五、后续清单（不在本轮修复范围）

- 重复实现：四引擎 plan digest/revisions（`adapters/*`）；两套 lease store（`internal/endpoint/lease.go`
  与 `internal/coordination/lease.go`，且内存实现被 agent-quorum 测试使用却与生产实现漂移）；
  严格 JSON 解码 13 处（其中 `internal/agent/config.go`、`internal/platformupdate/helper.go`、
  `cmd/clusterguard-agent/main.go` 缺尾随值检查）；身份归一化内联 25+ 处；`hasFailedCheck` 5 份。
- 死代码/过期：`internal/workflow/workflow.go:340` `executeLegacy`（生产不可达，仅测试）；
  `endpoint.MemoryLeaseStore` 生产零引用；旧静态审批令牌 `ApprovalTokenEnv`/`TokenApproval`。
- 其他测试缺口：`cmd/clusterguard-update-helper` peer 凭证授权（root 边界）无测试；
  `internal/platformupdate` 回滚模式无测试。
- 前端低危项（本轮未改）：电源链路无超时且弹窗在 `powerRunning` 时无法关闭；
  `renderPowerStatus` 在 `powerStatus=null` 时保留旧“已启用”徽标；`fleetContextError` 只写不读；
  `instanceAddress` 缺 `port` 渲染 `:undefined`；未校验 `new Date()` 渲染 Invalid Date；
  `fetchResult` 对 JSON `null` 抛 TypeError；登出未清理跨会话状态。

## 六、验证方法与证据

证据目录：`.build/quality-review-20260910/`。

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| QR-01 修改前反例 | 旧控制台 `bc0546a` 在 `/api/v1/clusters` 503 时 `loggedIn=false, shellVisible=false, loginHidden=false`（被弹回登录页） | `cluster-load-before/result.json`（failures=4，含 `cluster-list-failure`） |
| QR-01 修改后 | 5 个场景全通过：保持已认证外壳 + `集群列表读取失败…` 可重试提示，操作仍锁定 | `cluster-load-after2/result.json`（failures=0） |
| 既有前端回归 | `console-ui-acceptance`、`console-operation-intent-acceptance`、`console-context-acceptance` 全部 exit=0 | `.build/quality-review-20260910/regression/` |
| QR-03 | `TestPostgreSQLPlanRejectsTamperedDigest` 通过（保留旧摘要 → 被 digest 守卫拒绝） | `go test ./adapters/postgresql/...` |
| QR-04 | `pkg/gtid` 表驱动测试通过（归并、标签、13 类非法输入、missing/errant、恢复评估、uint64 溢出） | `go test ./pkg/gtid/...` |
| 构建与静态检查 | `go build ./...`、`go vet ./...`、`git diff --check`、`gofmt -l`（改动文件）全部通过 | 终端记录 |
| 关联竞态 | `go test -race -p 1 ./internal/api ./internal/coordination ./internal/endpoint ./internal/workflow ./adapters/postgresql ./pkg/gtid` 通过；`internal/api` 最终版复跑 race 通过 | 终端记录 |
| 全仓测试 | `go test -p 1 ./... -count=1`（Node 与 `CG_PG16_BIN` 注入）38 包全部 ok，含 `scripts` 215.9s | 终端记录 |

未验证（不得记为通过）：现场 152–154 未访问；`cmd/clusterguard-update-helper` 与
`internal/platformupdate` 回滚模式仍无测试；Docker 容器类测试本机无环境；
Oracle/SQL Server 原生灾难恢复、PXC、密钥轮换、安装包与发布未执行。

## 七、执行状态

- [x] 确认工作树/分支/HEAD/未提交改动与保护范围。
- [x] 旧版对比（bc0546a）并落盘本记录。
- [x] 修改前反例复现（QR-01 浏览器基线）。
- [x] 分批修复与回归（QR-01 控制台；QR-03/QR-04 测试缺口）；QR-02 经复核后撤回。
- [x] 独立复核最终差异（子代理对抗式复核 + 主代理逐条落实）；未发现阻断性问题，
      已按复核意见为失败路径增加 `clearClusterView()` 与 `error?.message` 兜底。
- [x] 未提交、未打包、未访问现场。

交付状态：**源码与测试已修改，全部本地检查通过；本轮没有 Git 提交/推送，没有生成或安装新包，
没有访问现场。已交付的 2.2-100 包不包含本轮改动。** 后续发布必须使用新版本号并执行发布门禁。
