# 功能缺陷报告复核与修复

## 范围与基线（业务代码修改前记录）

用户提供约 954 KB 的审查过程和 14 条结论。本文件把该报告作为线索，不把其中“已确证”“干净”“测试全绿”当作本轮证据。由当前模型独立复核，不调用其他模型，不操作现场数据库或安装包。

当前发布工作树：`.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD `19d02bcf076b6e294af0e1f30e2c34f0727e6ae4`。现有 PG 恢复、页面安全及测试改动保留；图片删除等无关改动不触碰。

实际计数（Git 跟踪的非测试 Go 文件）：

| 工作树 | HEAD | 文件 | 行数 | 本轮范围 |
| --- | --- | ---: | ---: | --- |
| 根 phase1 | 415afa6 | 18 | 2024 | 历史原型，不混入发布补丁 |
| mysql-topology-intelligence | 6e158fa | 92 | 20003 | 历史功能分支，不跨工作树修改 |
| platform-auth-session | 19d02bc | 183 | 53945 | 当前发布主线，优先复核与修复 |

已读取 AGENTS.md、恢复与升级发布验收清单；对比 `bc0546a` 与当前 HEAD 的目标文件，以及 Linux VIP、SQL Server 现有正确实现。现场版本/状态未在本轮重查；上一轮 2.2-100 包不包含本轮尚未实施的改动，不能覆盖重打。

## 修改前证据与计划

| 顺序 | 报告项 | 源码证据、旧行为与拟改范围 | 复现/不变量 |
| --- | --- | --- | --- |
| 1 | #1 K8s finalize | `endpoint/lease_authorization.go` 在 bc0546a 和 HEAD 相同；FinalizeTransition 改成 stable 后仍用旧 transition request 续租。对照同版 `linux_vip.go` 已更新续租请求。补齐相同收口行为。 | 完成后至少数次真实续租保持同一租约/owner，失去授权必须取消上下文，Finalize/Abort 幂等。 |
| 2 | 新发现 RenewOnly | `endpoint/lease.go`、`coordination/lease.go` 单次 Acquire 忽略 RenewOnly；批量 AcquireStableBatch 已拒绝缺失租约。两版均存在。 | 不得在缺失、过期或其他 owner 下新建/接管；内存与持久存储行为一致。 |
| 3 | #2 TTL/grace | `transitionLeaseTTL` 给 61-90 秒，存储 >60 秒降到30秒；等待后 Acquire 会重新获取，所以原报告“必然 Verify 失败”并不严谨。bc0546a 与当前相关实现相同。改成支持范围内的 TTL，并在长 grace 中续租，逐次校验同一租约身份。 | 15/30/31/60 秒 grace，身份不得空窗重建；多数派丢失/超时/换 owner 必须阻断，不增加 Agent 签名许可寿命。 |
| 4 | #4 Oracle plan | Oracle BuildPlan 写摘要但 Execute/Verify 不读 Plan；bc0546a 同样缺失。对照 SQL Server/PostgreSQL 执行计划验证模式补齐。 | 无计划、篡改、资源/观测变更不得发 DGMGRL/Agent mutation；验证允许已执行后的观测更新，不允许摘要变化。 |
| 5 | #3 SQL Server Verify | Verify 无限循环；正常 durable 执行已有 30 秒 detachedVerification，手动 Verify 直传 ctx。不能说所有升级都被此阻塞。加 adapter 有界轮询，保留调用方更短期限。 | 一直未收敛、查询失败、取消、随后收敛；不得误报成功。 |
| 6 | #5 MySQL UUID | c8795a3 新增 DisasterExecutor，旧 bc0546a 无此文件；旧 `switchover.go` 的 native UUID 比较使用 ToLower/TrimSpace，新文件漏用。 | 大小写/空白归一，同一UUID通过；空值/不同UUID、缺GTID/读写隔离仍拒绝。 |
| 7 | #6 UTF-8 | c8795a3 新增 redact.Bounded，按字节直接截断。 | 中文及多字节字符字节边界，先脱敏再截断，字节上限不增加。 |

本轮不拆 `operation_lock.go` 的互斥锁：#7 只有网络写入位置，没有延迟测量/原子替代方案，不能据此宣称功能 bug 或移走安全锁。

报告 #8-9 的引用指向旧 MySQL 工作树，#10-14 指向根 phase1 原型；不能直接当成当前发布版漏洞。若需维护历史分支，应另做独立基线、复现与发布验收。不得把整段流程或未审查模块声明为“干净”。

复核当前发布实现：

- #10：`api/server.go` 的 metadata verify 调用实时 `verifyMetadata`，失败返回 409，不是原型的固定成功。
- #11/#14：当前路由存在 `authorizeControl/authorizeMutation`，durable workflow 消费一次性 approval grant，不是根原型的空 ExpectedToken 分支。
- #12/#13：当前 `ReconcileInstance` 克隆候选状态再 `commitSnapshotLocked`；`RecordAudit/RecordReport` 返回提交错误，不是原型的就地修改和吞错。
- #8：当前 Fence 在隔离失败后也保留未到期 transition。不能直接套用“错误就 Release”：Agent/外部 fencing 超时可能已经生效，释放或回滚到旧 owner 需要明确的状态证据。此项未证明所有错误分支都应释放，本轮不做通用 defer Release，不将其列为已修复。

## 复现记录

先新增测试，再运行原业务代码，退出码 1：

- K8s 共用授权 helper：Finalize 后下一次续租产生 `endpoint lease conflict`，取消调用上下文。
- 内存/持久 LeaseStore：缺失租约时 RenewOnly 返回 nil 错误，实际创建了租约。
- 31/60 秒 grace：实际租约只有30秒，等待后原租约失效，出现 `original lease lost`。
- Oracle：缺计划、篡改、旧观测、错目标、缺 revision 时仍到达 stub controller 的 `CGPROD2` 切换调用。错集群原有 Precheck 已阻断，不能将其计作新修复。
- MySQL：同 UUID 的大小写/空白形式在 Preflight 和 qualified 被误拒。
- SQL Server：background context 进入查询时没有 deadline；正常 durable 自动验证原本已有30秒期限，问题主要在手动/直接 Verify 路径。
- 脱敏截断：1/2/4/5 等字节上限生成非法 UTF-8。

修复后同一组测试全部通过。另覆盖 Finalize 幂等、Finalize 后禁止 Abort、失去租约后不得重建、RenewOnly 禁止接管、15/30/31/60秒等待完整性、等待期间丢多数派/过期/替换/取消、Oracle 执行后新观测可验证但篡改计划仍拒绝、SQL Server 查询取消/失败/随后收敛、MySQL 身份不同或缺失与 write fence/GTID 缺失仍拒绝。

Oracle 旧测试存在绕过 BuildPlan 直接执行/验证的夹具，已改为绑定真实生成的计划，并给等待 probe 启动的测试增加超时，避免新校验拒绝后测试本身无限等待。不是删除校验使旧测试通过。

## 执行状态

- [x] 核对工作树、规模、旧版对比与调用链。
- [x] 新增针对性测试，在修改前记录失败。
- [x] 修复当前分支确认的7类缺陷（含新增发现的 RenewOnly）。
- [x] 关联回归、竞态、全仓测试、vet 与 diff 检查。
- [x] 记录最终结果和未覆盖范围。

本轮不改页面风格，不删除日志，不恢复旧根目录 API，不更改 fencing/quorum/恢复分支判断。现场验收、安装、打包与 GitHub 发布不以本地源码测试替代。

## 最终验证

证据目录：`.build/functional-review-20260910.SNeMfl/`。

| 检查 | 本轮结果 |
| --- | --- |
| 新增回归 | 11 个测试函数，含子测试共38个 pass 事件 |
| 全仓 `go test -p 1 -json ./... -count=1 -timeout=10m` | 36 个包通过，2143 个测试/子测试通过，0失败；364.740秒 |
| 关联 `go test -race -p 1` | endpoint、coordination、workflow、runtime、mysql、oracle、sqlserver、redact 共8包，689个测试/子测试通过，0失败、0跳过；60.764秒 |
| `go vet ./...` | 退出码0 |
| `git diff --check` | 通过 |
| PostgreSQL 实例测试 | 设置 CG_PG16_BIN，真实 `TestRecoveryPostgreSQLActualGuardedStartAndRebuild` 通过（3.30秒），包含上一轮遗留只读设置回归 |

全仓测试显式使用 bundled Node PATH，页面状态的 Node 测试未因缺 Node 跳过。没有进行本轮浏览器外观验收，因为没有修改页面。

以下5个需要 Docker 的隔离数据库/入口测试在本机未运行，不能算通过：

- TestEntrypointPreservesDynamicPostgreSQLRole
- TestRecoveryMySQLActualGuardedClone
- TestRecoveryMySQLActualThreeNodeRelayDrainAndSelection
- TestRecoveryMySQLActualExecutorRebuild
- TestRecoveryPostgreSQLReadOnlyStoppedDockerEvidence

Oracle/SQL Server/Kubernetes 本轮为适配器及模拟接口回归，不是原生现场切换验收。没有声称其他模块逐行无缺陷。旧分支问题、隔离失败后何时安全释放 transition、现场网络故障演练仍需独立证据；不能用全仓测试通过替代这些边界。

**交付状态：源码与测试已修改；本轮没有 SSH 修改现场、没有提交/推送 GitHub、没有生成或安装新升级包。已交付的 2.2-100 包没有被覆盖，也不包含本轮7类修复。** 后续发布必须用新版本，执行发布门禁与来源版本/验签检查。
