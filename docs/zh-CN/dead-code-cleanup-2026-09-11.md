# 无引用代码清理

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
