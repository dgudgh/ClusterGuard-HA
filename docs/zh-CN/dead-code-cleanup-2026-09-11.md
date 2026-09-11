# 代码与文件清理记录

## 修改前基线

- 用户要求：删除废弃代码；沿用当前 ClusterGuard 项目，不将旧版本兼容路径、测试或历史证据仅凭名称标成废弃。
- 实际工作树：`.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，修改前 HEAD `0a04ee44e8c4031a8304550d28baa79247a7fc0b`。已发布 2.2-101 固定构建提交为 `78dbdbf`，本次清理不重打该包或移动 tag。
- 已读取 `AGENTS.md` 和恢复与升级发布验收清单。既有 11 个图片删除、未跟踪 preview、缓存及现场 JSON 不属于本次清理。
- 以 `git ls-files` 枚举 532 个已跟踪 Go/HTML/JS/CJS/Shell/YAML 文件，标识符计数仅作候选筛选，再逐项全文检索、读取函数和现有调用链。下面 8 个非导出函数均只有定义，无源码、测试、脚本或前端引用。
- 对照 `bc0546a`（2.2-68）完整定义：8 个函数与当前完全一致；该版本全仓 Go 检索也未发现其调用。对照 `git diff bc0546a c8795a3` 的仓库变化，未把新恢复状态、升级门禁或脱敏逻辑作为清理对象。
- 额外追溯 `d35a60f`：原 `persistSnapshotLocked` 的调用已转为 `commitSnapshotLocked`，后续实际持久化由 `persistSnapshotRevisionLocked` 完成。本次删除遗留包装，不更改 revision、CAS 或 Raft 提交。
- 本记录在首次业务代码编辑前建立。此次是无引用定义清理，无需制造业务失败；验收目标为现有行为不变，不宣称修复了可观察的功能故障或提升运行速度。

## 删除清单与保留路径

| 文件 | 删除定义 | 证据及保持的行为 |
| --- | --- | --- |
| `adapters/oracle/oracle.go` | `oracleBrokerEndpoint`、`oracleBrokerOutputHealthy` | 未被引用；发现入口调用 `parseOracleBrokerDatabase`，现有连接构造和角色转换验证保留 |
| `adapters/sqlserver/sqlserver.go` | `sqlServerAlwaysOnOutputHealthy` | 未被引用；`Verify` 使用 `parseSQLServerVerificationEvidence` 校验单主、指定目标和副本健康，发现行解析保留 |
| `internal/consensus/raft.go` | `raftTailKeys` | 未被引用；尾日志修复调用 `raftTailKeySuffix(path, 256)`，合法快照、备份、连续性和截断拒绝条件保留 |
| `internal/endpoint/kubernetes_service.go` | `boolValue` | 未被引用的布尔指针包装；Pod/StatefulSet 归属、Ready 与入口安全验证保留 |
| `internal/store/operations.go` | `operationSummary` | 未被引用的格式化辅助函数；操作记录、审计、分页与终态逻辑保留；移除仅供它使用的 `fmt` 导入 |
| `internal/store/repository.go` | `persistSnapshotLocked` | 未被引用的无 revision 参数包装；保留 `commitSnapshotLocked` 和 `persistSnapshotRevisionLocked` 全部调用 |
| `internal/store/runtime_bindings.go` | `runtimeBindingSummary` | 未被引用的摘要辅助函数；运行环境绑定、身份校验及查询保留；移除仅供它使用的 `fmt` 导入 |

## 范围与验收计划

1. [完成] 修改前旧版定义对照、零引用确认与当前替代调用链检查。
2. [完成] 五个受影响包无缓存基线测试通过；仅删除上表 8 个定义和两个失效导入，共删除 66 行 Go 代码。
3. [完成] 删除后全仓串行复跑、五个关联包 race、vet、Linux amd64 构建通过；复查删除清单和格式差异，保留函数实现无变化。
4. [完成] 写回实际结果并限定提交范围为七个源码文件和本记录；不混入现有无关改动。

扫描边界：零引用筛选不是全仓可达性证明；导出 API、接口实现、测试夹具、兼容入口和故障恢复分支不凭词频删除。此次不修改前端、数据库配置、生产数据或升级签名，未执行现场部署或灾难恢复。

## 验证记录

- 修改前无缓存基线：`go test -count=1 ./adapters/oracle ./adapters/sqlserver ./internal/consensus ./internal/endpoint ./internal/store`，五个包全部通过。
- 删除后 `go vet ./...`、`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`、`git diff --check` 均通过。再次检索已跟踪源码、脚本及前端，8 个标识符均无残留引用。
- 首轮 `go test -json -count=1 ./...` 未通过：37 个包通过，2209 个测试及子测试通过，15 个用例按环境条件跳过，1 个测试失败。失败为 `internal/platformupdate/TestCommandLauncherPublishesGroupReadableOutput` 在等待子进程退出时触发既有 5 秒超时；不将该轮记为通过。
- 上述 Helper 测试连续单独复跑 10 次通过。`git diff 78dbdbf -- internal/platformupdate cmd/clusterguard-update-helper` 无差异；`go list -deps -test ./internal/platformupdate` 的依赖不包含本次修改的五个包。首轮同时运行构建与 vet，但尚无足够证据确定超时原因；本次未修改 Helper、测试断言或超时阈值。
- 将应用内附带的 Node 目录 `/Applications/ChatGPT.app/Contents/Resources/cua_node/bin` 加入 PATH 后，`go test -json -count=1 -p 1 ./...` 串行重跑通过：38 个包、2211 个测试及子测试通过，14 个测试跳过，零失败。`TestConsoleUpdateStateUsesLatestExecutionAndLiveMaintenance` 本轮实际执行并通过。首轮失败与最终复跑的原始输出分别保存在本地 `.build/dead-code-cleanup-20260911/full-tests.jsonl` 与 `final-tests.jsonl`，未加入发布包。
- `go test -race -count=1 ./adapters/oracle ./adapters/sqlserver ./internal/consensus ./internal/endpoint ./internal/store` 五个包全部通过；输出保存在本地 `.build/dead-code-cleanup-20260911/race-tests.log`。

### 未执行的环境测试

最终全仓回归的 14 个跳过用例均未算作通过：

| 条件 | 用例数 | 未验证范围 |
| --- | ---: | --- |
| 未设置 `CG_PG16_BIN` | 9 | 真实 PG 恢复隔离、WAL 分支与事务证据、启动重建、50 轮原生复制发现 |
| 未设置 `CG_MYSQL_RECOVERY_TEST_IMAGE` | 3 | 真实 MySQL 三节点选择、受控 clone 和重建 |
| 未设置 `CG_DOCKER_INTEGRATION_TESTS=1` | 1 | Docker PG 入口脚本集成 |
| 未提供只读 Agent 配置和指定集群 | 1 | 停止态 PG Docker 现场只读取证 |

本轮未执行现场升级或破坏性恢复，也未做新一轮浏览器交互验收；不得将本地回归解释为这些场景已通过。已发布 `v2.2.101`、安装包及其 SHA-256 保持不变，本次源码清理不能追记为该包内已有内容。

## 第二轮：文件组织与测试归并

### 修改前基线

- 新请求：全面梳理源码、测试和废弃文件，精简文件组织并更新 Markdown。基线为第一轮完成后的 `7e9717d89fa5c9305b263f8bfec678f83add6f7a`；本节在第二轮首次源码或测试编辑前建立。
- 清单覆盖 776 个 Git 跟踪路径，其中 11 个图片已由其他工作删除，保留其现状；未跟踪的 preview、现场 JSON、构建产物和诊断缓存不纳入提交。
- 使用 Go 标准库 `go/parser`/AST 读取全部 383 个 Go 文件，覆盖 40 个目录包、4425 个函数/方法、196 个测试文件、1638 个顶层 Test 函数。该轮未发现仅定义一次的非导出顶层声明。词频与语法扫描不是完整运行时可达性证明，也不能据此声称无功能缺陷。
- 已梳理模块入口、导入关系、脚本与 RPM/离线包引用、文档构建来源、同包重复函数体，并全文复核下列归并文件和终态判断调用点。不是对全部源码逐行完成了安全审计。
- 比对 `bc0546a`（2.2-68）：`terminalReportStatus` / `terminalOperationStatus`、`terminalProgressStatus` / `durableTerminalStatus` 的完整签名和函数体均与当前一致。各对使用相同的 `model.OperationStatus` 类型及五种终态；计划态、运行态、空值、未知值均不是终态。
- 读取报告写入、原子操作结束、Raft 快照标准化、进度重试与持久化操作接管的调用位置。拟只复用包内已有判断，不改变 CAS、旧报告空状态迁移、幂等、重试、维护或保护条件。
- 下表测试由 `c8795a3` 引入，至本轮基线无改动；`pkg/redact/bounded_test.go` 由 `f76b2f4` 引入，至本轮基线无改动。已经比较引入版本和现有完整测试，不把最近新增的安全回归标为废弃。
- 修改前 `go test -count=1 ./internal/api ./internal/store ./internal/workflow ./pkg/redact`（PATH 含应用附带 Node）全部通过。第一轮全仓串行回归与竞态结果仅作为已有基线，第二轮改动后另行验证。

### 归并清单

| 原测试文件 | 归并位置 | 保留的覆盖 |
| --- | --- | --- |
| `internal/store/disaster_abandoned_test.go`、`recovery_authorization_test.go` | `internal/store/disaster_test.go` | 执行器失联后保护、租约过期、库存变化、MySQL/PG 授权 |
| `internal/store/power_complete_test.go` | `internal/store/power_test.go` | 恢复结束原子提交与磁盘失败保护 |
| `internal/store/operation_context_test.go` | `internal/store/operation_page_test.go` | 全历史上下文、旧主身份、同时间排序；保留现有分页基准测试 |
| `internal/api/operation_page_test.go` | `internal/api/operation_list_test.go` | 服务器分页、游标校验、延迟详情读取、不截断审计 |
| `internal/api/console_cluster_loading_test.go`、`console_update_state_test.go` | `internal/api/console_state_test.go` | 切集群加载保护、经典样式约束、工具栏与真实 Node 升级状态回归 |
| `pkg/redact/sql_test.go`、`bounded_test.go` | `pkg/redact/redact_test.go` | SQL 密码/密钥脱敏、UTF-8 截断与长度边界 |

只删除归并后的空壳文件，不删除上述 Test、Benchmark 或辅助函数。修改前已生成规范化函数 AST 摘要，归并后逐项比较，不能以测试总数相同替代原断言保留检查。

### 保留决定

- `docs/html` 是由 Markdown 和 `docs/html-src` 构建的离线成品；重复 CSS、JS、截图是自包含交付依赖，不作死代码删除。
- `packaging/offline-dependencies` 的 RPM、repodata 和校验文件服务于无网安装，不凭文件类型清除。
- Go 的平台特定文件、Agent 不同引擎的接口方法、不同结构类型的地址碰撞判断不强行合并；源码相同不代表所属协议或类型可互换。
- 浏览器与现场验收脚本、旧兼容测试、性能对照基准、发布说明和历史故障证据保留；未配置真实环境的测试不是废弃测试。
- `tools/console-design-acceptance.cjs` 含旧设计专用选择器，本轮不以删除整套测试来消除过时断言。工具状态和运行边界在文件导览中单独标明。

### 执行状态

1. [完成] 文件清单、AST 扫描、重复内容分析、旧版对照和四模块基线测试。
2. [完成] 归并测试文件，净减少八个文件；复用两处终态判断，增加完整状态集合回归。
3. [完成] 比较原测试/基准函数摘要；全仓、关联竞态、静态检查与 Linux 构建通过。
4. [完成] 运行四组浏览器回归，更新文件与测试导览、实际结果和未验证项；提交范围只包含本次源码、测试与 Markdown。

### 实际精简结果

- Go 文件从 383 个减至 375 个，其中测试文件从 196 个减至 188 个。归并保留原有 1638 个 Test 函数及两个 Benchmark，逐项比较规范化 AST 摘要，无缺失、无原函数内容变化；涉及的测试辅助函数也保留。
- 新增 `TestTerminalOperationStatusContract` 和 `TestDurableTerminalStatusContract`，分别覆盖九种状态，包括空值与未知值。检查报告写入、原子结束报告和终态进度拒绝行为，不只是比较函数返回值。现在共有 1640 个 Test 函数及两个 Benchmark。
- 删除 `terminalReportStatus`、`terminalProgressStatus` 两个重复定义，调用点分别复用本包已有的 `terminalOperationStatus`、`durableTerminalStatus`。保留原有五种终态、空状态历史迁移、重试读取和 CAS 条件；没有跨模块引入新的公共抽象。
- 新增 [源码、文件与测试导览](source-layout-and-testing.md)，集中记录模块入口、测试命令、工具用途和保留规则；两个文档索引补充入口，并将已有离线 HTML 页数由错误的 41 更正为实际 65。没有重新生成已发布文档快照。
- 实际控制台 `internal/api/console.html` 与基线逐字节一致，SHA-256 为 `27af5dfac3a19d718b274d31bb77213b29ce37365accca00e37ddba82f861258`。本轮不是 UI 改版或现场性能修复。

### 第二轮验证结果

| 检查 | 本轮实际结果 |
| --- | --- |
| 修改前与修改后四模块无缓存测试 | `internal/api`、`internal/store`、`internal/workflow`、`pkg/redact` 均通过 |
| 全仓 `go test -json -count=1 -p 1 ./...` | 38 个包、2231 个测试及子测试通过，14 项跳过，零失败；本轮第一次全仓执行即通过 |
| 四模块 `go test -race -count=1` | 四个包全部通过，无竞态报告 |
| 静态检查与目标构建 | `go vet ./...`、`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`、`git diff --check` 通过 |
| 工具语法与诊断单测 | 49 个 Shell 文件 `bash -n`、28 个 JS/CJS/MJS 文件 `node --check` 通过；三个 Python 诊断单测通过 |
| 文档检查 | 四个本次维护的 Markdown 文件中，本地相对链接均能定位目标 |

全仓回归 PATH 包含 Node，因此 Node 驱动的控制台升级状态用例实际执行。原始结果位于本地 `.build/file-audit-20260911/full-tests.jsonl`、`race-tests.log`；规范化函数对照为同目录 `before.json`、`after.json`。这些生成证据未混入发布包或源码提交。

浏览器使用当前真实控制台 HTML、隔离 API 夹具和本机 Chrome，结果分别保存在 `.build/file-audit-20260911/browser/` 的对应子目录：

| 工具 | 检查数 | 实际覆盖 |
| --- | ---: | --- |
| `console-bootstrap-audit.cjs` | 5 | 慢集群、辅助接口失败、能力或集群列表失败、迟到总览响应；操作锁保持有效 |
| `console-node-safety-audit.cjs` | 31 | MySQL/PG 未解锁不执行、重复点击、预检迟到、切集群、退出和重新锁定；1440/390 宽度弹窗 |
| `console-engine-pages-audit.cjs` | 73 | MySQL/PG/Oracle/SQL Server，八个页面、1440/390 宽度共 64 张截图；不支持的操作禁用、引擎指标和未捕获异常检查 |
| `console-log-pagination-acceptance.cjs` | 7 | 首屏 20 条、详情按需读取、失败重试原游标、83 条历史完整访问、搜索和筛选、迟到结果丢弃、桌面与手机无横向溢出 |

四组共 116 项检查通过。日志测试完成 13 个成功分页响应，单页最大 9901 字节，83 条记录全部保留。另实际查看了 PG 桌面拓扑和手机节点弹窗截图；不将截图自动生成等同于逐张人工审查。

### 尚未覆盖与后续边界

- 14 项环境跳过条件与第一轮表格相同：九项原生 PG、三项 MySQL Docker、一项 Docker 入口、一项现场停止态只读取证，均不计作通过。本轮没有 SSH、现场升级、数据库故障注入、凭据旋转或三节点恢复。
- `console-design-acceptance.cjs` 旧设计选择器和交互路径仍需单独迁移；本轮未运行该历史脚本，不能宣称所有工具均已完成行为验收。仅做语法检查的其他脚本同样不等于已执行。
- 本轮完成全仓文件与语法清单、重点调用链复核和受影响路径验证，不代表全部源码已逐行审计，也不代表所有功能缺陷已消除。
- 不移动 `v2.2.101`，不覆盖安装包、不改变发布版本；清理提交中的内容须在后续正式构建时另行验包。既有 11 个图片删除和未跟踪预览、缓存、现场 JSON 不属于本次提交。
