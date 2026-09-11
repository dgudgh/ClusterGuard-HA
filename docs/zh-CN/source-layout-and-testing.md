# 源码、文件与测试导览

维护日期：2026-09-11。面向当前开发分支，不代表已发布安装包已包含后续清理。先读 [AGENTS.md](../../AGENTS.md)，修改前必须完成旧版对照并保留记录。

## 从哪里开始读

| 范围 | 入口与职责 | 不可混淆的边界 |
| --- | --- | --- |
| 程序入口 | [cmd](../../cmd)：服务端、`cgctl`、Agent、升级 Helper、Kubernetes fence guard | 五种程序入口不是重复实现 |
| 装配与配置 | [runtime](../../internal/runtime)、[config](../../internal/config)、[buildinfo](../../internal/buildinfo) | 装配适配器与能力门禁；配置示例不能取代运行态观测 |
| 控制台与接口 | [api](../../internal/api)，实际页面为 [console.html](../../internal/api/console.html) | 静态预览不是现场 API，不能据示例数据判断健康 |
| 数据库适配 | [mysql](../../adapters/mysql)、[postgresql](../../adapters/postgresql)、[oracle](../../adapters/oracle)、[sqlserver](../../adapters/sqlserver) | 原生身份、候选选择和执行验证各不相同；实现不等于完成现场验收 |
| 发现与观测 | [discovery](../../internal/discovery)、[metrics](../../internal/metrics)、[observability](../../internal/observability)、[report](../../internal/report) | 保留观测版本、缺失数据和错误；不能靠清空或忽略证据变快 |
| 持久化 | [store](../../internal/store)、[consensus](../../internal/consensus)、[controlstate](../../internal/controlstate) | 快照、Raft、CAS、审计与大小边界一起核对 |
| 权限与保护 | [auth](../../internal/auth)、[approval](../../internal/approval)、[coordination](../../internal/coordination)、[maintenance](../../internal/maintenance) | 会话、单次批准、锁/租约、升级维护门禁不能互相替代 |
| 操作编排 | [workflow](../../internal/workflow)、[recovery](../../internal/recovery)、[disaster](../../internal/disaster)、[lifecycle](../../internal/lifecycle) | 自动故障处理、灾难恢复和节点生命周期是不同流程 |
| 受限执行与业务入口 | [agent](../../internal/agent)、[endpoint](../../internal/endpoint)、[kubernetes](../../internal/kubernetes) | 先验证授权、身份及 fencing；不同运行环境的接口方法需分别保留 |
| 公共协议 | [pkg](../../pkg)：`adapter`、`model`、`gtid`、`identity`、`redact` | 对外类型、接口实现和兼容字段不能只凭词频删除 |
| 部署与打包 | [scripts](../../scripts)、[deploy](../../deploy)、[configs](../../configs)、[packaging](../../packaging) | 安装、升级、依赖收集与系统服务脚本是交付链路，不是普通测试 |
| 工具与证据 | [tools](../../tools)、[diagnostics](../../tools/diagnostics) | 浏览器模拟、离线验包、只读取证及破坏性恢复分别管理 |

关键阅读顺序：`cmd/clusterguard` -> `internal/runtime` -> `internal/api` -> 具体服务与适配器 -> `internal/store`/Raft；高风险操作还需反查 Agent、权限、fencing、writer lease 和维护门禁。总体设计见 [架构文档](architecture.md)。

## 文件保留规则

- `_test.go` 由 Go 自动发现，调用次数为零是正常现象；Test、Benchmark、测试夹具不是废弃代码。
- 按功能归并同包小测试文件，保留测试名称、原断言、跳过条件和构建约束；不把已经很大的测试文件继续堆成全仓杂项文件。
- 不合并 Linux 与其他平台的受限文件访问实现，也不把不同数据库引擎的方法按相似文本强行通用化。
- `docs/html-src` 是 HTML 文档的 JS/CSS 源；`docs/html` 是含本地资源的生成成品。相同截图、JS 和 CSS 是自包含离线阅读所需，不作重复垃圾删除。
- `packaging/offline-dependencies` 的 RPM、repodata、清单和摘要需保持配套，不能仅删除其中的索引或压缩包。
- `docs/superpowers`、旧发布说明、签名事故与恢复记录是历史证据，不是当前功能状态；保留并从当前入口链接最新记录。
- `.build`、`dist`、`preview` 可能保存已交付包、对比页面和本地证据。清理前确认归属与引用，不能为减少目录数量直接递归删除。
- 不将私有现场 JSON、密码、日志原文、会话文件或签名私钥加入 Git。旧截图删除、未提交预览等他人改动不得混入维护提交。

本次逐文件去向和旧版证据见 [清理记录](dead-code-cleanup-2026-09-11.md#第二轮文件组织与测试归并)。

## 本地基础回归

以下命令从项目根目录运行，需要 Go；控制台状态回归还需要 `node` 在 PATH 中，脚本回归按具体用例需要 `bash`、`jq`、`openssl` 等。缺依赖导致的 skip 必须单独记录。

```bash
go test -count=1 -p 1 ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
git diff --check
```

先按包串行完成全仓回归，再执行较重的 race 或构建，避免同时争用资源干扰短时限测试。`-p 1` 只限制包并行，不跳过测试，也不修改测试超时或安全门槛。

本次文件归并和终态判断涉及的局部命令：

```bash
go test -count=1 ./internal/api ./internal/store ./internal/workflow ./pkg/redact
go test -race -count=1 ./internal/api ./internal/store ./internal/workflow ./pkg/redact
python3 -B -m unittest discover -s tools/diagnostics -p 'test_*.py' -v
```

`python3 -B` 不生成新的 `__pycache__`。上述 Python 单测替换了外部执行函数，不读取现场密码或连接数据库。

日志分页的性能对照仍在 [operation_page_test.go](../../internal/store/operation_page_test.go)，不要因旧接口名称而删掉基准：

```bash
go test ./internal/store -run '^$' -bench 'BenchmarkOperation(LogPage|LegacyFullClone)$' -benchmem
```

性能结果只能说明该机器、该数据规模和该构建；不能推断 PostgreSQL 现场刷新时延已改善。

## 浏览器回归

这些工具不随 `go test ./...` 自动执行。它们使用 Playwright 和本机 Chrome；`playwright` 须由现有 Node 环境提供，必要时设置 NODE_PATH 指向已安装的包目录。某些评审脚本另依赖 `sharp`。使用 `CONSOLE_TEST_OUTPUT` 指向新的 `.build` 子目录，避免覆盖历史截图和结果。

| 目的 | 工具（位于 `tools/`） |
| --- | --- |
| 启动读取与辅助接口失败隔离 | [console-bootstrap-audit.cjs](../../tools/console-bootstrap-audit.cjs) |
| MySQL/PG 节点弹窗锁、预检迟到、会话失效 | [console-node-safety-audit.cjs](../../tools/console-node-safety-audit.cjs) |
| 四种引擎、八个页面、桌面/窄屏与能力边界 | [console-engine-pages-audit.cjs](../../tools/console-engine-pages-audit.cjs) |
| 普通操作防误触与幂等 | [operation-lock](../../tools/console-operation-lock-acceptance.cjs)、[operation-intent](../../tools/console-operation-intent-acceptance.cjs)、[operation-lifecycle](../../tools/console-operation-lifecycle-audit.cjs) |
| 刷新、退出和迟到请求的会话边界 | [console-session-boundary-audit.cjs](../../tools/console-session-boundary-audit.cjs) |
| 恢复确认与升级确认 | [console-recovery-acceptance.cjs](../../tools/console-recovery-acceptance.cjs)、[console-update-confirmation-acceptance.cjs](../../tools/console-update-confirmation-acceptance.cjs) |
| 日志分页、范围、工具栏 | [log-pagination](../../tools/console-log-pagination-acceptance.cjs)、[log-scope](../../tools/console-log-scope-acceptance.cjs)、[log-toolbar](../../tools/console-log-toolbar-acceptance.cjs) |
| 集群上下文、总览与延迟 | [context](../../tools/console-context-acceptance.cjs)、[overview](../../tools/console-overview-acceptance.cjs)、[latency](../../tools/console-latency-acceptance.cjs) |
| 经典样式及页面流程 | [console-ui-acceptance.cjs](../../tools/console-ui-acceptance.cjs) |
| 带 `?ui=review` 的代表页评审 | [console-ux-acceptance.cjs](../../tools/console-ux-acceptance.cjs)、[console-ux-integrity.cjs](../../tools/console-ux-integrity.cjs)、[console-ux-review.cjs](../../tools/console-ux-review.cjs) |
| 现场证据回放审计 | [console-field-evidence-audit.cjs](../../tools/console-field-evidence-audit.cjs) |
| 离线预览生成 | [build-console-preview.cjs](../../tools/build-console-preview.cjs)，使用 [console-ui-fixture.cjs](../../tools/console-ui-fixture.cjs) |

基础浏览器检查分别运行：

```bash
CONSOLE_TEST_OUTPUT=.build/validation/bootstrap node tools/console-bootstrap-audit.cjs
CONSOLE_TEST_OUTPUT=.build/validation/node-safety node tools/console-node-safety-audit.cjs
CONSOLE_TEST_OUTPUT=.build/validation/engine-pages node tools/console-engine-pages-audit.cjs
```

这些是隔离 API 夹具上的真实浏览器交互，不是数据库端到端执行。`--baseline` 模式用于保存旧版失败证据，不能拿其正常退出当作当前验收通过。

历史设计检查 [console-design-acceptance.cjs](../../tools/console-design-acceptance.cjs) 仍引用当前页面没有的 `overview-recent-operations`、`metric-comparisons` 选择器，且部分弹窗路径早于当前防误触锁。本轮仅确认这些静态不匹配，未运行整套历史脚本。它不能作为当前发布通过门槛，也不能通过删除安全断言获得绿色结果。需要复用时，应先将独有的离线网络、布局和弹窗覆盖迁入当前流程，再退役旧脚本。

`--baseline`、UX 完整性对照和现场回放还依赖已有 `.build` 快照；缺少快照时应报告未执行，不能自动回放示例数据并标为现场已通过。

## 真数据库与现场测试

| 所需环境 | 用途与边界 |
| --- | --- |
| `CG_PG16_BIN` | 隔离 PostgreSQL 16 实例、WAL 分支、事务证据、受控恢复和 50 个后台发现周期 |
| `CG_MYSQL_RECOVERY_TEST_IMAGE` | 隔离 MySQL 容器的 GTID、relay、clone 和重建 |
| `CG_DOCKER_INTEGRATION_TESTS=1` | Docker PG 入口脚本与动态主从角色验证 |
| 只读 Agent 配置及指定集群 | 停止态 PG Docker 现场只读取证；不能用虚构节点结果代替 |

`tools/diagnostics/recovery-field-acceptance.py`、`pg-replication-secret-rotation.py` 的部分模式会准备恢复现场、修改服务或旋转凭据；`helper-linux-field-test.py` 需要 Linux 特权和隔离 Helper 环境。这些不是普通本地单测，不能批量遍历执行整个 diagnostics 目录。

只读取证、恢复观测、连续性、保留策略、同机控制器隔离等工具入口和现场门槛见 [恢复与升级发布验收清单](release-recovery-acceptance-checklist.md)。没有执行的 native、Docker 或三节点场景必须明确标为未执行。

## 打包与文档

安装材料从 `scripts/build-clusterguard-bundle.sh`、`build-clusterguard-rpm.sh`、`build-clusterguard-offline-kit.sh` 生成；签名补丁使用 `build-clusterguard-patch.sh`。版本与产物检查分别见 [bundle-version-acceptance.cjs](../../tools/bundle-version-acceptance.cjs) 和 [verify-offline-kit.cjs](../../tools/verify-offline-kit.cjs)。必须先核对各脚本参数、来源介质、可信公钥及发布清单，不将语法检查等同验包成功。

Markdown 是可维护来源，HTML 文档使用 [build-html-docs.mjs](../build-html-docs.mjs) 生成；仅维护当前源文档不会改变已发布包内的文档快照。没有实际重新构建和核验，不得标注为新安装包。

## 持续维护

1. 新回归优先放进对应功能测试文件；仅在独立运行环境、构建约束或清晰的新功能边界出现时新建文件。
2. 文件改名或归并前记录旧提交和去向，确认无脚本按旧文件名编译或提取代码。
3. 同时比较 Test/Benchmark 名称和规范化函数内容；不能通过减少测试让回归看起来更快。
4. 将精简结果、失败、跳过和现场限制写入 [清理记录](dead-code-cleanup-2026-09-11.md)，不要重复创建只有一句“已通过”的零散报告。
