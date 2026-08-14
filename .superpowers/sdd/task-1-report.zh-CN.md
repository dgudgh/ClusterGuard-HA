# 任务 1 报告：常见拓扑结构、指标和候选合约

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](task-1-report.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 实现

- 在 `pkg/model/topology.go` 中添加了可移植的拓扑结构合约：
  `ThreadState`, `ReplicationStatus`, `MetricSample`, `TopologySnapshot`,
  `CandidatePolicy`, 和 `CandidateAssessment`, 使用简报中确切的字段
  名称、JSON 标签和枚举值。
- 使用 `model.DatabaseInstance` 扩展了 `Replication`, `Maintenance`,
  `PromotionEligible`, 和 `EngineMetadata`。
- 向适配器合约添加了 `CapabilityMetrics`, `CapabilityCandidates`, `CandidateRequest`, 以及
  `Metrics` 和 `EvaluateCandidates` 方法。
- 扩展 `adapter.UnsupportedAdapter` 以同时宣传新的能力为不可用，并从两个方法中返回 `adapter.ErrUnsupported`。PostgreSQL、Oracle 和 SQL Server 的骨架继承了此行为，未作更改。
- 在 MySQL 适配器中添加了显式的临时不支持的方法和不可用能力条目。后续任务可以在其实现经过测试后替换这些占位符。

## 测试

### RED 证据

在添加合约测试并进行生产更改之前，运行了：

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./pkg/model ./pkg/adapter -count=1
```

结果：退出代码 `1`，并出现预期的合约缺失失败：

```text
pkg/model/topology_test.go:10:3: unknown field Replication in struct literal of type DatabaseInstance
pkg/model/topology_test.go:10:16: undefined: ReplicationStatus
pkg/model/topology_test.go:12:20: undefined: ThreadRunning
pkg/model/topology_test.go:13:20: undefined: ThreadRunning
pkg/model/topology_test.go:17:14: instance.Replication undefined
pkg/adapter/registry_test.go:55:36: undefined: adapter.CapabilityMetrics
pkg/adapter/registry_test.go:58:36: undefined: adapter.CapabilityCandidates
pkg/adapter/registry_test.go:61:26: candidate.Metrics undefined
pkg/adapter/registry_test.go:64:26: candidate.EvaluateCandidates undefined
pkg/adapter/registry_test.go:64:75: undefined: adapter.CandidateRequest
FAIL
```

### 聚焦 GREEN 证据

运行了：

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./pkg/model ./pkg/adapter ./adapters/... -count=1
```

结果：退出代码 `0`。

```text
ok   clusterguard.io/ha/pkg/model
ok   clusterguard.io/ha/pkg/adapter
ok   clusterguard.io/ha/adapters/mysql
?    clusterguard.io/ha/adapters/oracle [no test files]
?    clusterguard.io/ha/adapters/postgresql [no test files]
?    clusterguard.io/ha/adapters/sqlserver [no test files]
```

还成功运行了 `git diff --check`。

### 全面 GREEN 证据

在提交之前运行了一次所需的完整测试套件：

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./...
```

结果：退出代码 `0`；所有包均通过测试，包括 MySQL、API、配置、存储、工作流、适配器、身份和模型包。没有测试文件的适配器包成功编译。

## 修改的文件

- 创建了 `pkg/model/topology.go`。
- 创建了 `pkg/model/topology_test.go`。
- 修改了 `pkg/model/model.go`。
- 修改了 `pkg/adapter/adapter.go`。
- 修改了 `pkg/adapter/registry_test.go`。
- 修改了 `adapters/mysql/mysql.go`。

允许的 PostgreSQL、Oracle 和 SQL Server 文件未被编辑，因为它们现有的 `adapter.UnsupportedAdapter` 嵌入自动提供了新的方法和不可用能力状态，无需冗余包装器。

## 自我审查

- 确认新的接口签名使用 `DiscoverRequest` 来进行指标，并使用简报中确切的 `CandidateRequest` 形状。
- 确认不支持的方法返回共享的哨兵 `ErrUnsupported`，因此 `errors.Is` 保持可靠。
- 确认所有四个适配器均被新的 fail-closed 测试覆盖，包括显式的能力检查和方法调用。
- 确认 MySQL 现有的发现和健康行为未被更改。
- 确认没有修改允许的实现/测试集和明确请求的报告路径之外的文件。

## 关注点

- 默认的 shell `PATH` 在此环境中不包含 Go。验证使用了可用的 Go 1.26.2 工具链位于
  `/Users/zhaolongjie/sdk/go1.26.2/bin/go`；仓库声明使用 Go 1.19，且测试代码与现有模块兼容。
- MySQL 的能力条目是故意设置为不可用的占位符，用于后续的指标和候选实现任务。
