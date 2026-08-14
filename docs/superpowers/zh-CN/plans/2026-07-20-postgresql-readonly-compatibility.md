# PostgreSQL 只读兼容性实现计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-20-postgresql-readonly-compatibility.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于智能代理工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项执行此计划任务。步骤使用复选框 (`- [ ]`) 语法进行跟踪。

**目标：** 在不启用写入操作的情况下，安全地实现 PostgreSQL 注册、发现、拓扑、健康、延迟和候选评估。

**架构：** 添加一个由 `psql` 支持的原生只读 PostgreSQL 适配器，原子地持久化 `system_identifier` 与发现过程，运行独立的引擎过滤发现调度器。重用通用注册表、资源存储、API、控制台、监控和工作流门控。

**技术栈：** Go 1.22，PostgreSQL `psql`，原生流复制视图，现有 JSON 快照存储和 Raft 复制。

## 全局约束

- 不依赖 Patroni 或任何外部高可用框架。
- 在此阶段不宣传或执行任何 PostgreSQL 写入操作。
- 主机名、IP 和端口从不作为资源标识。
- 缺失或矛盾的标识和拓扑证据将导致拒绝。
- 在生产更改之前编写并观察测试失败。

---

### 任务 1：PostgreSQL 查询执行器和探针

**文件：**
- 创建：`adapters/postgresql/runner.go`
- 创建：`adapters/postgresql/runner_test.go`
- 创建：`adapters/postgresql/probe.go`
- 创建：`adapters/postgresql/probe_test.go`

**接口：**
- 生成 `SQLRunner.Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)`.
- 生成 `discover(context.Context, SQLRunner, adapter.DiscoverRequest) (adapter.DiscoveryResult, error)`.

- [x] 编写失败的 JSON 行、安全命令构造、标识验证、主健康、备健康、暂停重放、未知延迟和格式错误 LSN 证据测试。
- [x] 运行 `go test ./adapters/postgresql -count=1` 并确认失败是由于缺少实现。
- [x] 实现测试所需的最小执行器和探针。
- [x] 运行 `go test ./adapters/postgresql -count=1` 并确认通过。

### 任务 2：适配器能力、拓扑、候选和元数据

**文件：**
- 修改：`adapters/postgresql/postgresql.go`
- 创建：`adapters/postgresql/postgresql_test.go`
- 创建：`adapters/postgresql/candidates.go`
- 创建：`adapters/postgresql/candidates_test.go`
- 修改：`pkg/identity/identity.go`
- 修改：`pkg/identity/identity_test.go`

**接口：**
- 生成 `postgresql.New(SQLRunner) *Adapter`.
- 实现发现、拓扑、健康、候选和元数据方法。
- 所有写入方法继续返回 `adapter.ErrUnsupported`.

- [x] 编写失败的能力、拓扑、排名、标识和不支持操作测试。
- [x] 运行目标测试并确认失败。
- [x] 实现拓扑和时间线感知的候选评估。
- [x] 运行目标测试并确认通过。

### 任务 3：原子集群标识发布

**文件：**
- 修改：`internal/discovery/service.go`
- 修改：`internal/discovery/service_test.go`
- 修改：`internal/store/repository.go`
- 修改：`internal/store/replication_test.go`

**接口：**
- 添加 `DiscoveryRefresh.ClusterIdentity model.EngineIdentity`.
- 首次发布绑定一个空的 PostgreSQL 集群标识。
- 不匹配的标识拒绝完整的刷新。

- [x] 编写首次绑定、混合系统刷新、持久不匹配和端点重命名无重复实例的失败测试。
- [x] 运行目标测试并确认失败。
- [x] 实现验证和原子发布。
- [x] 运行目标测试并确认通过。

### 任务 4：配置和独立调度

**文件：**
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/runtime/runtime.go`
- 修改：`internal/runtime/runtime_test.go`
- 修改：`configs/clusterguard.example.json`

**接口：**
- 添加 `File.PostgreSQL` 带有只读发现配置。
- 凭据解析器根据集群引擎切换。
- 每个启用的引擎启动一个过滤调度器。

- [x] 编写失败的配置、凭据路由和调度器过滤测试。
- [x] 运行目标测试并确认失败。
- [x] 实现配置加载和运行时连接。
- [x] 运行目标测试并确认通过。

### 任务 5：引擎安全的监控和控制台

**文件：**
- 修改：`internal/api/metrics.go`
- 修改：`internal/api/metrics_test.go`
- 修改：`internal/api/monitoring.go`
- 修改：`internal/api/monitoring_test.go`
- 修改：`internal/api/console.html`
- 修改：`internal/api/console_test.go`

**接口：**
- 指标名称接收集群引擎。
- PostgreSQL 生命周期和写入控制保持禁用。
- 原生标识显示是引擎感知的。

- [x] 编写失败的 Prometheus、Zabbix 和静态控制台契约测试。
- [x] 运行目标测试并确认失败。
- [x] 实现引擎感知的监控和控制台保护。
- [x] 运行目标测试并确认通过。

### 任务 6：文档和完整验证

**文件：**
- 修改：`README.md`
- 修改：`docs/architecture.md`
- 修改：`docs/operations.md`

- [x] 文档化 PostgreSQL 授权、标识设置、配置和不支持的写入边界。
- [x] 对更改的 Go 文件运行 `gofmt`。
- [x] 运行 `go test ./... -count=1`。
- [x] 运行 `go test -race ./... -count=1`。
- [x] 运行 `go vet ./...`。
- [x] 使用 `jq empty` 验证每个 `configs/*.json` 文件。
- [x] 运行 `go mod verify` 和 `git diff --check`。
