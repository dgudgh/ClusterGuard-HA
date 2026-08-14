# MySQL 拓扑和候选智能实现计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-10-clusterguard-ha-mysql-topology-candidate-intelligence.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于代理工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项实施此计划任务。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标：** 在不启用任何数据库变更的情况下，交付资源清单范围的 MySQL 拓扑发现、复制和性能健康、候选排名、API、CLI 输出和一个紧凑的拓扑控制台。

**架构：** 首先扩展通用资源和适配器合同，然后持久化资源清单端点、复制链接和有界指标。发现服务仅探测已注册的端点，协调不可变的 MySQL 身份，构建链接，并将候选评估委托给 MySQL 适配器。API、CLI 和控制台使用相同的平台快照。

**技术栈：** Go 1.19+，标准库 HTTP/JSON/进程执行，原子 JSON 快照持久化，嵌入式 HTML/CSS/JavaScript，假 SQL 执行器和浏览器验证。

## 全局约束

- 独立实现行为；不要从冻结的原型中复制源代码、API、配置、模式、包结构或运行时依赖。
- 保持平台 UUID 作为资源键；主机名、IP 地址、端口、服务器 ID 和显示名称是可变的元数据。
- 仅探测和控制在所选集群资源清单中注册的端点。
- 在本项目中保持切换、故障转移、前主节点重新加入、VIP 变更和节点生命周期执行不支持。
- 永远不要持久化发现密码，不要将它们包含在命令参数中，也不要通过 API、审计记录或报告返回它们。
- 保持 PostgreSQL、Oracle 和 SQL Server 注册，并对未实现的方法关闭故障。
- 每个任务都使用 TDD，并独立提交每个绿色交付物。

---

### 任务 1：通用拓扑、指标和候选合同

**文件：**
- 创建：`pkg/model/topology.go`
- 修改：`pkg/model/model.go`
- 修改：`pkg/adapter/adapter.go`
- 修改：`adapters/mysql/mysql.go`
- 修改：`adapters/postgresql/postgresql.go`
- 修改：`adapters/oracle/oracle.go`
- 修改：`adapters/sqlserver/sqlserver.go`
- 测试：`pkg/model/topology_test.go`
- 测试：`pkg/adapter/registry_test.go`

**接口：**
- 生成：`model.ReplicationStatus`、`model.MetricSample`、`model.TopologySnapshot`、`model.CandidatePolicy`、`model.CandidateAssessment`、`adapter.CapabilityMetrics`、`adapter.CapabilityCandidates`、`DatabaseHAAdapter.Metrics` 和 `DatabaseHAAdapter.EvaluateCandidates`。
- 保留：现有的 `DatabaseHAAdapter` 方法和骨架适配器的显式不支持行为。

- [ ] **步骤 1：编写失败的模型和适配器合同测试**

添加测试，构建一个具有可移植复制状态的副本，并要求每个骨架适配器拒绝新方法：

```go
func TestTopologyContractsCarryPortableReplicationState(t *testing.T) {
	lag := int64(3)
	instance := DatabaseInstance{
		ResourceMeta: ResourceMeta{ResourceID: NewResourceID()},
		Engine: EngineMySQL,
		Replication: ReplicationStatus{
			SourceIdentity: EngineIdentity{"server_uuid": "source-uuid"},
			IOThread: ThreadRunning,
			SQLThread: ThreadRunning,
			LagSeconds: &lag,
		},
	}
	if instance.Replication.SourceIdentity["server_uuid"] != "source-uuid" || *instance.Replication.LagSeconds != 3 {
		t.Fatalf("portable replication state was lost: %+v", instance)
	}
}
```

```go
func TestSkeletonAdaptersFailClosedForReadExtensions(t *testing.T) {
	for _, candidate := range []adapter.DatabaseHAAdapter{postgresql.New(), oracle.New(), sqlserver.New()} {
		if _, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s metrics must be unsupported: %v", candidate.Engine(), err)
		}
		if _, err := candidate.EvaluateCandidates(context.Background(), adapter.CandidateRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s candidates must be unsupported: %v", candidate.Engine(), err)
		}
	}
}
```

- [ ] **步骤 2：运行聚焦测试并观察缺失的类型和方法**

运行：`go test ./pkg/model ./pkg/adapter -count=1`

预期：编译失败，命名 `ReplicationStatus`、`Metrics` 和 `EvaluateCandidates`。

- [ ] **步骤 3：实现通用合同**

创建以下类型，并将 `Replication`、`Maintenance`、`PromotionEligible` 和 `EngineMetadata` 添加到 `DatabaseInstance`：

```go
type ThreadState string

const (
	ThreadUnknown ThreadState = "unknown"
	ThreadRunning ThreadState = "running"
	ThreadStopped ThreadState = "stopped"
)

type ReplicationStatus struct {
	SourceIdentity    EngineIdentity `json:"source_identity,omitempty"`
	IOThread          ThreadState    `json:"io_thread"`
	SQLThread         ThreadState    `json:"sql_thread"`
	LagSeconds        *int64         `json:"lag_seconds,omitempty"`
	RetrievedPosition string         `json:"retrieved_position,omitempty"`
	ExecutedPosition  string         `json:"executed_position,omitempty"`
	LastError         string         `json:"last_error,omitempty"`
}

type MetricSample struct {
	InstanceID ResourceID        `json:"instance_id"`
	ObservedAt time.Time         `json:"observed_at"`
	Values     map[string]float64 `json:"values"`
}

type TopologySnapshot struct {
	ClusterID  ResourceID         `json:"cluster_id"`
	Instances  []DatabaseInstance `json:"instances"`
	Links      []ReplicationLink  `json:"links"`
	ObservedAt time.Time          `json:"observed_at"`
}

type CandidatePolicy struct {
	MaximumLagSeconds int64 `json:"maximum_lag_seconds"`
	RequireGTID       bool  `json:"require_gtid"`
}

type CandidateAssessment struct {
	InstanceID   ResourceID `json:"instance_id"`
	Eligible     bool       `json:"eligible"`
	Rank         int        `json:"rank"`
	RiskLevel    string     `json:"risk_level"`
	DataLossRisk string     `json:"data_loss_risk"`
	Checks       []Check    `json:"checks"`
}
```

将 `CapabilityMetrics`、`CapabilityCandidates`、`Metrics(context.Context, DiscoverRequest) ([]model.MetricSample, error)` 和 `EvaluateCandidates(context.Context, CandidateRequest) ([]model.CandidateAssessment, error)` 添加到适配器合同。`CandidateRequest` 包含 `Cluster`、`Primary`、`Instances`、`Links` 和 `Policy`。所有四个适配器最初从两个方法返回 `ErrUnsupported`，并宣传两个功能不可用。任务 3 和 5 替换 MySQL 占位符，并在实现测试后仅宣传每个功能。

使用此确切的请求合同：

```go
type CandidateRequest struct {
	Cluster   model.DatabaseCluster  `json:"cluster"`
	Primary   model.DatabaseInstance `json:"primary"`
	Instances []model.DatabaseInstance `json:"instances"`
	Links     []model.ReplicationLink `json:"links"`
	Policy    model.CandidatePolicy  `json:"policy"`
}
```

- [ ] **步骤 4：运行合同测试**

运行：`go test ./pkg/model ./pkg/adapter ./adapters/... -count=1`

预期：通过。

- [ ] **步骤 5：提交合同**

```bash
git add pkg/model pkg/adapter adapters/mysql adapters/postgresql adapters/oracle adapters/sqlserver
git commit -m "feat: define topology and candidate contracts"
```

### 任务 2：持久集群资源清单、链接和有界指标

**文件：**
- 修改：`internal/store/repository.go`
- 测试：`internal/store/repository_test.go`

**接口：**
- 消耗：任务 1 模型类型。
- 生成：`CreateClusterWithEndpoints`、`UpsertEndpoint`、`Endpoints`、`ReplaceReplicationLinks`、`ReplicationLinks`、`StoreMetricSamples`、`MetricSamples` 和 `FindInstanceByIdentity`。

- [ ] **步骤 1：编写失败的持久化和资源清单测试**

添加测试，注册两个端点，持久化一个链接和一个指标样本，
重新加载文件仓库，并断言所有资源存活。添加第二个测试
询问 `FindInstanceByIdentity` 一个已知的 MySQL `server_uuid`。

```go
func TestRepositoryPersistsInventoryLinksAndBoundedMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil { t.Fatal(err) }
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine:model.EngineMySQL, DisplayName:"payments"})
	if err != nil { t.Fatal(err) }
	clusterID := cluster.ResourceID
	endpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: clusterID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true})
	if err != nil { t.Fatal(err) }
	if endpoint.ResourceID == "" { t.Fatal("endpoint UUID is required") }
	link := model.ReplicationLink{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID, SourceInstanceID: model.NewResourceID(), TargetInstanceID: model.NewResourceID(), Healthy: true}
	if err := repository.ReplaceReplicationLinks(clusterID, []model.ReplicationLink{link}); err != nil { t.Fatal(err) }
	if err := repository.StoreMetricSamples(clusterID, []model.MetricSample{{InstanceID: link.TargetInstanceID, ObservedAt: time.Now(), Values: map[string]float64{"qps": 4}}}, 60); err != nil { t.Fatal(err) }
	reloaded, err := Open(path)
	if err != nil { t.Fatal(err) }
	if len(reloaded.Endpoints(clusterID)) != 1 || len(reloaded.ReplicationLinks(clusterID)) != 1 || len(reloaded.MetricSamples(clusterID)) != 1 {
		t.Fatal("inventory snapshot did not round trip")
	}
}
```

- [ ] **步骤 2：运行仓库测试并观察缺失的方法**

运行：`go test ./internal/store -count=1`

预期：六个新仓库方法的编译失败。

- [ ] **步骤 3：扩展快照和仓库**

添加端点、复制链接和指标切片的快照映射。在读取时克隆所有
嵌套映射和身份。实现这些确切的签名：

```go
func (repository *Repository) CreateClusterWithEndpoints(model.DatabaseCluster, []model.Endpoint) (model.DatabaseCluster, []model.Endpoint, error)
func (repository *Repository) UpsertEndpoint(model.Endpoint) (model.Endpoint, error)
func (repository *Repository) Endpoints(model.ResourceID) []model.Endpoint
func (repository *Repository) ReplaceReplicationLinks(model.ResourceID, []model.ReplicationLink) error
func (repository *Repository) ReplicationLinks(model.ResourceID) []model.ReplicationLink
func (repository *Repository) StoreMetricSamples(model.ResourceID, []model.MetricSample, int) error
func (repository *Repository) MetricSamples(model.ResourceID) []model.MetricSample
func (repository *Repository) FindInstanceByIdentity(model.ResourceID, model.Engine, model.EngineIdentity) (model.DatabaseInstance, bool)
```

`UpsertEndpoint` 拒绝无效端口、空或未知的集群 UUID，以及同一集群内的重复活动
地址。`StoreMetricSamples` 保留每个实例最新的 `limit`
样本，按 `ObservedAt` 排序。

`CreateClusterWithEndpoints` 在获取仓库锁之前验证完整的请求，
分配所有 UUID，并在一个快照写入中持久化集群和端点集。
任何无效或冲突的端点不会留下集群或
端点。

- [ ] **步骤 4：运行包括持久化重新加载的仓库测试**

运行：`go test ./internal/store -count=1`

预期：通过。

- [ ] **步骤 5：提交持久资源清单**

```bash
git add internal/store
git commit -m "feat: persist cluster inventory and topology state"
```

### 任务 3：多版本 MySQL 探测和指标解析器

**文件：**
- 创建：`adapters/mysql/runner.go`
- 创建：`adapters/mysql/probe.go`
- 创建：`adapters/mysql/metrics.go`
- 修改：`adapters/mysql/mysql.go`
- 测试：`adapters/mysql/probe_test.go`
- 测试：`adapters/mysql/metrics_test.go`
- 修改：`adapters/mysql/mysql_test.go`
- 修改：`internal/api/server_test.go`
- 修改：`pkg/adapter/registry_test.go`

**接口：**
- 消耗：任务 1 适配器和模型合同。
- 生成：支持 MySQL 生成的完整 `Discover` 输出、头信息感知的 `SQLRunner.Query`、MySQL 身份/复制解析、只读指标。

- [ ] **步骤 1：编写失败的头解析器和版本兼容性测试**

定义一个返回 `[]Row` 的假执行器，并覆盖两个术语家族：

```go
func TestParseReplicationAcceptsBothTerminologyFamilies(t *testing.T) {
	legacy := Row{"Master_UUID":"source-uuid", "Slave_IO_Running":"Yes", "Slave_SQL_Running":"Yes", "Seconds_Behind_Master":"2", "Retrieved_Gtid_Set":"source-uuid:1-10", "Executed_Gtid_Set":"source-uuid:1-10"}
	modern := Row{"Source_UUID":"source-uuid", "Replica_IO_Running":"Yes", "Replica_SQL_Running":"Yes", "Seconds_Behind_Source":"2", "Retrieved_Gtid_Set":"source-uuid:1-10", "Executed_Gtid_Set":"source-uuid:1-10"}
	for _, row := range []Row{legacy, modern} {
		status, err := parseReplication(row)
		if err != nil { t.Fatal(err) }
		if status.SourceIdentity["server_uuid"] != "source-uuid" || status.IOThread != model.ThreadRunning || status.SQLThread != model.ThreadRunning || *status.LagSeconds != 2 {
			t.Fatalf("unexpected status: %+v", status)
		}
	}
}
```

使用相同的引擎中立结果断言为版本 `5.7.44`、`8.0.44`、`8.4.10` 和 `9.7.0` 添加表测试。添加一个执行器测试证明密码在 `MYSQL_PWD` 中存在，而在 `command.Args` 中不存在。

- [ ] **步骤 2：运行适配器测试并观察缺失的基于行的探测代码**

运行：`go test ./adapters/mysql -count=1`

预期：`Row`、`parseReplication` 和新的执行器方法编译失败。

- [ ] **步骤 3：实现支持表头的查询执行**

使用以下契约：

```go
type Row map[string]string

type SQLRunner interface {
	Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)
}
```

CLI 执行器使用 `--batch --raw` 调用本地客户端，不使用 `--skip-column-names`，将第一行 TSV 解析为表头，并将后续每一行映射为 `Row`。它仅通过 `MYSQL_PWD` 设置密码。

- [ ] **步骤 4：实现身份和复制探测**

运行一个别名身份查询，用于获取服务器 UUID、主机名、端口、服务器 ID、版本、只读标志、GTID 模式、二进制日志记录和 binlog 格式。使用 `SHOW REPLICA STATUS` 探测复制，仅在第一个语句被拒绝时回退到 `SHOW SLAVE STATUS`。使用以下方式规范化字段名称：

```go
func first(row Row, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(row[name]); value != "" { return value }
	}
	return ""
}
```

当两个只读标志都为 false 时，空的复制结果仅标识一个可写主节点。缺少或停止线程的副本是降级的，而不是健康的。将版本、GTID 模式、二进制日志状态和 binlog 格式存储在 `EngineMetadata` 中。

- [ ] **步骤 5：实现只读性能指标**

解析 `SHOW GLOBAL STATUS` 以获取 `Questions`、`Com_commit`、`Com_rollback`、`Threads_connected`、`Threads_running`、`Slow_queries`、`Innodb_buffer_pool_reads` 和 `Innodb_buffer_pool_read_requests`。使用这些确切的键返回计数器：`questions_total`、`transactions_total`、`connections`、`running_threads`、`slow_queries_total` 和 `buffer_pool_hit_ratio`。将比率限制为 `[0,1]`。

- [ ] **步骤 6：运行所有 MySQL 适配器测试**

更新共享适配器契约测试，使 MySQL 宣布指标并返回样本，而 PostgreSQL、Oracle 和 SQL Server 继续宣布指标并将候选者标记为不可用。MySQL 候选者评估在任务 5 之前仍不可用。

运行：`go test ./adapters/mysql -count=1`

预期：所有四个版本固定装置均通过。

- [ ] **步骤 7：提交 MySQL 探测**

```bash
git add adapters/mysql
git commit -m "feat: discover MySQL replication and performance state"
```

### 任务 4：基于资源清单范围的集群发现服务

**文件：**
- 创建：`internal/discovery/service.go`
- 测试：`internal/discovery/service_test.go`
- 修改：`pkg/model/topology.go`
- 修改：`pkg/model/topology_test.go`
- 修改：`internal/store/repository.go`
- 修改：`internal/store/repository_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`pkg/model/topology.go`
- 修改：`pkg/model/topology_test.go`

**接口：**
- 使用：适配器注册表、仓库资源清单、MySQL 凭据、任务 3 发现结果。
- 生成：`Service.Refresh(context.Context, model.ResourceID) (model.TopologySnapshot, error)`、`model.ProbeStatus`、`Repository.ReplaceClusterAnomalies`、`Repository.ApplyDiscoveryRefresh` 和 `ErrInventoryRequired`。

- [ ] **步骤 1：为资源清单范围和链接解析编写失败的服务测试**

使用一个返回一个主节点和两个副本的假适配器，其 `Replication.SourceIdentity` 指向主节点的 UUID。断言 `Refresh` 探测恰好三个注册端点，在第二次刷新时保留三个平台 UUID，并写入两个链接。添加一个测试，提供一个未注册的端点并确认没有公共服务方法可以探测它。

添加一个失败探测测试。一个从未被发现的端点会出现在 `Probes` 中，带有其端点 UUID 和未知健康状态，但不会创建假的 `DatabaseInstance`。一个之前被发现的端点保留其实例 UUID，并在后续成功刷新之前显示为未知。

添加测试以证明不支持的适配器在任何仓库发布之前失败，解析器/适配器错误文本不会被复制到公共健康摘要中，指标失败会降级探测和集群但不存储样本，两个端点解析到一个 `server_uuid` 会产生一个实例且不产生虚假的脑裂警报，以及一个集群的并发刷新不能交错。

```go
func TestRefreshBuildsLinksOnlyFromRegisteredInventory(t *testing.T) {
	repository := store.NewMemory()
	cluster, _ := repository.UpsertCluster(model.DatabaseCluster{Engine:model.EngineMySQL, DisplayName:"payments"})
	for index, host := range []string{"mysql-a", "mysql-b", "mysql-c"} {
		_, err := repository.UpsertEndpoint(model.Endpoint{ClusterID:cluster.ResourceID, Kind:model.EndpointDatabase, Hostname:host, Port:3306+index, Active:true})
		if err != nil { t.Fatal(err) }
	}
	service := newFakeDiscoveryService(t, repository)
	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil { t.Fatal(err) }
	if len(snapshot.Instances) != 3 || len(snapshot.Links) != 2 { t.Fatalf("unexpected topology: %+v", snapshot) }
}
```

- [ ] **步骤 2：运行发现测试并观察缺失的服务**

运行：`go test ./internal/discovery -count=1`

预期：由于 `Service` 和 `Refresh` 不存在，编译失败。

- [ ] **步骤 3：实现确定性的刷新编排**

在实现服务之前扩展快照契约：

```go
type ProbeStatus struct {
	EndpointID ResourceID `json:"endpoint_id"`
	InstanceID ResourceID `json:"instance_id,omitempty"`
	Health     Health     `json:"health"`
}

type TopologySnapshot struct {
	ClusterID  ResourceID         `json:"cluster_id"`
	Instances  []DatabaseInstance `json:"instances"`
	Links      []ReplicationLink  `json:"links"`
	Probes     []ProbeStatus      `json:"probes"`
	Health     Health             `json:"health"`
	Anomalies  []MetadataAnomaly  `json:"anomalies,omitempty"`
	ObservedAt time.Time          `json:"observed_at"`
}
```

使用注册表、仓库、凭证解析器、时钟和最多四个并行度创建 `Service`。`Refresh` 按以下顺序执行这些步骤：

1. 加载集群及其活动数据库端点。
2. 使用 `ErrInventoryRequired` 拒绝空资源清单。
3. 要求选择的适配器在开始探测之前宣传发现；不支持的引擎返回 `adapter.ErrUnsupported` 而不发布。
4. 按集群和通过所选引擎适配器逐个探测每个端点，按顺序发布刷新。
5. 对每个成功的本地身份进行协调，并将资源清单端点的 `InstanceID` 绑定到返回的平台 UUID。
6. 将每个源本地身份解析为平台实例 UUID。
7. 通过一个仓库快照事务发布实例、端点绑定、复制链接、指标样本和集群异常。
8. 对于失败，返回端点级别的未知探测状态，但不发明引擎身份、实例或复制链接。

如果发现两个可写主节点，返回带有严重异常和降级集群健康的快照；不要隐式选择一个。

向仓库添加 `ReplaceClusterAnomalies(clusterID model.ResourceID, anomalies []model.MetadataAnomaly) error`。它原子地替换该集群的发现异常，同时保留其他集群的异常。

向仓库添加这些事务输入和方法：

```go
type DiscoveryObservation struct {
	EndpointID model.ResourceID
	Instance   model.DatabaseInstance
	Metrics    []model.MetricSample
}

type DiscoveryRefresh struct {
	ClusterID    model.ResourceID
	Observations []DiscoveryObservation
	Anomalies    []model.MetadataAnomaly
}

func (repository *Repository) ApplyDiscoveryRefresh(DiscoveryRefresh) (model.TopologySnapshot, error)
```

`ApplyDiscoveryRefresh` 一次锁定，克隆当前快照，协调候选中的所有本地身份，绑定每个成功端点，解析复制链接，追加有界指标样本，替换集群异常，持久化候选一次，然后仅发布它。任何错误都返回，且实时快照保持不变。具有相同引擎身份的重复观察将每个端点绑定到一个平台 UUID，并返回一个实例。

公开的 `ProbeStatus.Health.Summary` 值使用固定的分类，如 `database probe failed`、`discovery credentials unavailable` 和 `performance metrics unavailable`；它们从不包含原始解析器、客户端或适配器错误字符串。指标失败会使数据库发现成功，但将探测和整体快照健康状态更改为降级。

更新 `ReconcileInstance` 以使现有身份接收最新的
`Replication`, `EngineMetadata`, `Maintenance`, 和 `PromotionEligible` 值。
使用候选快照持久化，使持久化错误不会影响活动实例。添加存储测试，首先使副本达成一致，用更改后的延迟/线程状态和元数据刷新它，然后强制持久化失败，以证明成功刷新和回滚行为。

- [ ] **步骤 4：运行发现和存储测试**

运行：`go test ./internal/discovery ./internal/store ./pkg/model -count=1`

预期结果：通过（PASS）。

- [ ] **步骤 5：提交集群发现**

```bash
git add internal/discovery internal/store pkg/model
git commit -m "feat: refresh inventory-scoped MySQL topology"
```

### 任务 5：基于 GTID 的候选评估

**文件：**
- 创建：`adapters/mysql/gtid.go`
- 创建：`adapters/mysql/candidates.go`
- 修改：`adapters/mysql/probe.go`
- 修改：`adapters/mysql/mysql.go`
- 修改：`adapters/mysql/mysql_test.go`
- 修改：`pkg/adapter/adapter.go`
- 修改：`pkg/adapter/registry_test.go`
- 测试：`adapters/mysql/gtid_test.go`
- 测试：`adapters/mysql/candidates_test.go`

**接口：**
- 使用：`adapter.CandidateRequest` 和发现的复制状态。
- 生成：确定性的 MySQL `EvaluateCandidates` 结果和 `CapabilityCandidates` 可用性。

- [ ] **步骤 1：编写失败的 GTID 集测试**

覆盖标准化的 UUID 区间、子集比较、缺失事务和异常事务：

```go
func TestCompareGTIDSetsFindsMissingAndErrantIntervals(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-20")
	if err != nil { t.Fatal(err) }
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-18,ffffffff-1111-2222-3333-444444444444:1")
	if err != nil { t.Fatal(err) }
	comparison := CompareGTIDSets(primary, candidate)
	if comparison.MissingTransactions != 2 || comparison.ErrantTransactions != 1 {
		t.Fatalf("unexpected comparison: %+v", comparison)
	}
}
```

- [ ] **步骤 2：编写失败的候选排名测试**

创建一个零延迟的健康副本、一个延迟副本、一个线程停止的副本和一个异常副本。要求健康副本排名第一，延迟副本在策略范围内时仍具有资格但带有警告风险，后两者被阻止。当两个候选者的分数相等时，通过资源 UUID 确保排序稳定。

- [ ] **步骤 3：运行 MySQL 测试并观察缺失的评估器代码**

运行：`go test ./adapters/mysql -run 'TestCompareGTID|TestEvaluateCandidates' -count=1`

预期结果：`ParseGTIDSet`, `CompareGTIDSets` 和评估器行为的编译失败。

- [ ] **步骤 4：实现 GTID 区间标准化和比较**

解析逗号分隔的 UUID 组和冒号分隔的包含区间。按 UUID 合并重叠区间。对于降序、零、格式错误或无 UUID 的区间返回解析错误。`CompareGTIDSets` 计算主集缺少的候选事务和仅在候选中出现的事务。差值聚合必须使用受检算术；不可表示的单集或多 UUID 总和是错误，候选评估失败即阻断，而不是环绕或饱和。

将只读身份查询扩展为收集 `@@GLOBAL.gtid_executed` 并存储在 `EngineMetadata["gtid_executed"]` 中。这是可写主的权威位置；副本比较继续使用复制状态执行集。探测必须在 GTID 模式禁用时容忍空集，但绝不能从仅副本状态行推断主 GTID 位置。

- [ ] **步骤 5：实现候选检查和确定性排名**

将 `Probes []model.ProbeStatus` 添加到 `adapter.CandidateRequest`；探测覆盖率必须显式提供，绝不能从实例或链接数量推断。

评估这些阻止检查：资源清单成员资格、非主角色、可达性、提升资格、维护状态、运行中的复制线程、源身份等于当前主、策略延迟、需要时启用 GTID、无异常事务和兼容的主要版本。对非零延迟、非零缺失 GTID 事务和不完整的探测覆盖率发出警告。候选者需要显式的健康边界探测来证明资源清单成员资格和当前可达性。缺少证据或非健康边界候选探测会阻止它，即使同一实例的另一个别名端点是健康的。空探测证据是不完整的。对不同资源清单端点的失败或未绑定探测仅发出警告，不会阻止其自身绑定探测健康的候选者。按以下顺序排名：

1. 无警告优先于有警告；
2. 缺失事务更少；
3. 延迟更低；
4. 精确主版本优先于兼容版本；
5. 字典顺序平台 UUID。

当 GTID 解析或计数失败，或历史记录因异常事务而发散时，`DataLossRisk` 绝不能报告 `none`。这些情况报告一个显式的不确定值；成功的比较即使异常事务也阻止候选者，仍保留缺失事务计数。

仅在有资格的候选者上设置 `Rank`，并宣传 `CapabilityCandidates` 作为 MySQL 适配器和注册表契约的可用项。保持 `CapabilityExecute` 不可用。将 MySQL 发布系列 `5.7`, `8.0`, `8.4`, 和 `9.7` 视为不同的兼容性系列；精确版本仅在兼容系列内作为排名偏好，绝不能作为绕过不兼容系列阻止的方法。

- [ ] **步骤 6：运行所有适配器和契约测试**

运行：`go test ./adapters/mysql ./pkg/adapter -count=1`

预期结果：通过（PASS）。

- [ ] **步骤 7：提交候选智能**

```bash
git add adapters/mysql pkg/adapter
git commit -m "feat: rank MySQL promotion candidates"
```

### 任务 6：集群注册、拓扑、候选和指标 API

**文件：**
- 创建：`internal/api/clusters.go`
- 创建：`internal/api/metrics.go`
- 创建：`internal/metrics/service.go`
- 创建：`internal/metrics/service_test.go`
- 修改：`internal/runtime/runtime.go`
- 修改：`internal/api/server.go`
- 修改：`internal/api/server_test.go`
- 修改：`internal/discovery/service.go`
- 修改：`internal/discovery/service_test.go`
- 修改：`internal/store/repository.go`
- 修改：`internal/store/repository_test.go`
- 创建：`internal/api/clusters_test.go`
- 创建：`internal/api/metrics_test.go`

**接口：**
- 使用：仓库、发现服务、适配器注册表、候选评估器。
- 生成：集群注册、刷新、拓扑、候选、派生速率、JSON 指标和 Prometheus 文本路由。

- [ ] **步骤 1：编写失败的集群注册和刷新 API 测试**

使用稳定名称和三个数据库端点测试 `POST /api/v1/clusters`，然后测试 `POST /api/v1/clusters/{id}/discover`。断言响应包含一个主节点、两个副本和两个链接。提交一个重复的活动端点并期望 `409`，包括当相同活动数据库地址已被另一个集群拥有时。拒绝空白或大小写不敏感重复的集群显示名称。尝试刷新一个没有资源清单的 UUID 并期望 `422`。

使用此注册负载：

```json
{
  "display_name": "payments-mysql",
  "engine": "mysql",
  "endpoints": [
    {"hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},
    {"hostname":"mysql-b","ip_address":"192.0.2.11","port":3306},
    {"hostname":"mysql-c","ip_address":"192.0.2.12","port":3306}
  ]
}
```

- [ ] **步骤 2：编写失败的候选和指标 API 测试**

测试：

- `GET /api/v1/clusters/{id}/topology`
- `GET /api/v1/clusters/{id}/candidates`
- `GET /api/v1/clusters/{id}/metrics`
- `GET /api/v1/clusters/{id}/metrics/prometheus`

候选结果必须使用平台UUID。Prometheus正文必须包含
`clusterguard_mysql_replication_lag_seconds`和
`clusterguard_mysql_connections`，并带有`cluster_id`和`instance_id`标签，
且不得将主机名作为身份。

候选读取必须在`409`的情况下关闭，除非持久化快照具有
恰好一个当前主节点和明确的探测证据。它们必须将
持久化探测传递到`CandidateRequest`；它们绝不能选择零个或多个可写主节点中的第一个。无效的非UUID集群路径参数
在任何仓库或适配器调用之前返回`400`。

添加一个metrics-service测试，包含两个样本，间隔十秒。问题数量的差值为50必须产生QPS 5，事务数量的差值为20必须产生TPS 2，慢查询数量的差值为10必须产生慢查询率1。计数器减少表示重启，并省略该速率，而不是发出负值。

- [ ] **步骤3：运行API测试并观察缺失的路由**

运行：`go test ./internal/api -count=1`

预期：`404`或新处理程序的编译失败。

- [ ] **步骤4：实现严格的注册和读取API**

将集群处理程序从`server.go`中分离出来。注册通过`CreateClusterWithEndpoints`创建集群UUID
和端点，因此端点验证和
快照写入是原子操作。发现仅使用仓库清单和配置的凭证；API不接受密码字段。候选策略默认最大延迟10
秒和必需的GTID，可选查询覆盖值限制为安全的数字
值。

使用每个实例的最新两个样本实现`metrics.Service.Derive(samples []model.MetricSample) map[model.ResourceID]map[string]float64`。它仅在时间戳增加且计数器不减少时返回`qps`、`tps`和`slow_queries_per_second`；仪表和缓冲池比率来自最新样本。

将最新的完整`TopologySnapshot`观察状态作为同一发现事务的一部分进行持久化：集群健康状况、每个端点的探测健康状况、
观察时间、实例、链接、异常和度量样本一起发布或不发布。添加一个由拓扑、候选和健康处理程序使用的仓库读取方法。进程重启必须保留最后一次探测
覆盖率和集群健康状况；GET路由不得触发数据库探测。

为最新周期的每个探测持久化显式的发现和度量新鲜时间戳。候选评估仅在该周期成功观察到主节点角色时接受主节点。JSON和Prometheus仅在当前度量观察时发出性能仪表/速率，仅在当前发现观察时发出复制延迟；历史样本仍存储但不得伪装成当前值。JSON报告每个实例的实际度量观察时间。

在任何状态变更之前，拒绝时间戳等于或早于当前持久快照的观察。度量推导返回实际使用的样本的时间戳，并仅在与当前周期度量证据匹配时发出值，包括控制器时钟回滚后。当多个端点别名在周期内合并为一个实例时，持久化该实例的一个确定的最新完整样本，以防止别名探测成为连续的速率样本。

拓扑读取表示每个已知的活动清单成员。当前探测失败但之前绑定的端点保留其稳定的实例UUID，但使用当前的探测失败健康状态，而不是过时的健康状态。从未成功的端点保持为仅探测状态，且不会发明实例。在标记链接不健康时，暂时未观察到的节点的最后已知复制关系仍需保留，只要任一端点缺少当前健康的探测。候选评估仍使用当前探测证据，因此阻止不可用节点。

拓扑读取覆盖规范的UUID地址实例资源，而不是将重复的快照副本视为元数据权威。主机名、IP或端口的协调立即可见，无需更改`resource_id`。添加、删除、激活、停用或重新定位清单端点会失效持久化观察，直到下一次刷新。

元数据协调是一个仅坐标的原子事务。它保留原生身份和所有观察的运行时字段，更新下一个探测使用的选定绑定数据库端点，验证全局地址所有权，并失效拓扑。可以推断出一个绑定端点；多个别名需要显式的端点UUID。

集群健康仅在有完整的活动清单探测证据、恰好一个成功观察的可写主节点、健康的当前实例状态、运行的副本IO/SQL线程和健康的已知链接时才为健康。零个或多个主节点、不完整的探测、任何不健康的实例或停止的线程必须降级或失败健康。

Prometheus输出转义标签值，仅发出有限的数值样本。
设置`Content-Type: text/plain; version=0.0.4; charset=utf-8`。

HTTP JSON解码是有限的，且仅消耗一个完整的值。发现刷新路由仅接受空体或一个空JSON对象，
与`Content-Length`无关；分块凭证字段、尾随值和部分尾随数据在刷新器之前失败。有效的未知集群UUID在读取路由上返回`404`，而已注册但没有观察的集群返回`409`。

仓库集群名称验证由创建和更新路径共享：
修剪后的名称非空且不区分大小写唯一。注册将类型验证、冲突和持久化错误映射到`400`、`409`和`500`，
而不会泄露内部信息。当启用MySQL发现时，配置需要非空的用户名、密码环境变量名称和解析后的密码。

无法更改集群更新的数据库引擎。非空的原生集群标识符通过引擎标识符契约进行验证，该契约具有全局唯一性，并且为一次性写入：它只能建立一次，之后不能更改或清除。通用的JSON解码器对注册、操作和元数据请求以及单值框架施加显式的字节限制。

在此任务中扩展`runtime.New`，以使用配置的MySQL凭据构建发现服务，并将其传递给`api.NewServer`。更新API测试构建，以注入一个假的发现服务，因此没有任何单元测试会打开真实的数据库连接。

在将刷新功能暴露给API调用者之前，将无限制的每集群锁映射替换为有界隔离机制（固定锁条带化或经过测试的引用计数驱逐设计）。任意未知的集群UUID不得增加进程内存，而同一集群的刷新应保持序列化，不同的资源清单集群应保留有用的并发性。

- [ ] **步骤5：移除无限制的直接发现**

移除`POST /api/v1/discovery`。添加一个API测试，断言其返回`404`，因此调用者无法探测或同步注册资源清单之外的节点。

- [ ] **步骤6：运行API和完整单元测试**

运行：`go test ./internal/api ./internal/discovery ./... -count=1`

预期结果：通过。

- [ ] **步骤7：提交API交付**

```bash
git add internal/api internal/discovery internal/metrics internal/runtime internal/store
git commit -m "feat: expose MySQL topology and candidate APIs"
```

### 任务7：CLI和紧凑拓扑控制台

**文件：**
- 修改：`cmd/cgctl/main.go`
- 修改：`cmd/cgctl/main_test.go`
- 修改：`internal/api/console.html`
- 修改：`internal/api/console_test.go`
- 测试：`internal/api/console_test.go`

**接口：**
- 使用：任务6的API路由。
- 生成：`cgctl topology`、`cgctl candidates`、`cgctl metrics`、集群刷新命令和选定集群的拓扑视图。

- [ ] **步骤1：编写失败的CLI路由和输出测试**

扩展命令表测试：

```go
func TestEndpointForMySQLIntelligenceCommands(t *testing.T) {
	tests := []struct{ args []string; method, path string }{
		{[]string{"topology", "cluster-id"}, "GET", "/api/v1/clusters/cluster-id/topology"},
		{[]string{"candidates", "cluster-id"}, "GET", "/api/v1/clusters/cluster-id/candidates"},
		{[]string{"metrics", "cluster-id"}, "GET", "/api/v1/clusters/cluster-id/metrics"},
		{[]string{"refresh", "cluster-id"}, "POST", "/api/v1/clusters/cluster-id/discover"},
	}
	for _, test := range tests {
		method, path, err := requestFor(test.args)
		if err != nil || method != test.method || path != test.path { t.Fatalf("unexpected request: %s %s %v", method, path, err) }
	}
}
```

- [ ] **步骤2：编写失败的控制台契约测试**

要求嵌入页面为选定的集群获取拓扑和候选者，并渲染以下标签：`主库`、`候选节点`、`从库`、`延迟`、`版本`、`IP`、`端口`和`VIP`，仅当存在HA端点时。要求一个刷新按钮，且在此项目中不提供切换/执行按钮。

- [ ] **步骤3：运行CLI和控制台测试并观察失败**

运行：`go test ./cmd/cgctl ./internal/api -count=1`

预期结果：`requestFor`的编译失败或缺少控制台路由字符串。

- [ ] **步骤4：实现CLI命令**

将`endpointFor`替换为`requestFor(arguments) (method, path string, err error)`。
使用`http.NewRequest`，使刷新发送`POST`并附带一个空的JSON对象。保留`--json`；人工输出列出资源UUID、显示端点、角色、健康状态、延迟和候选排名，但不暴露凭据。

- [ ] **步骤5：实现拓扑控制台**

渲染一个紧凑的圆角矩形拓扑，包含一个主列和一个副本列。每个节点在一行居中位置显示主机名、IP和端口，并在第二行居中位置显示版本、角色和延迟。线条连接到固定的卡片锚点。候选选择仅突出显示卡片，不执行角色更改。使用现有的面板宽度、8像素或更小的圆角、受控的颜色以及在900像素以下的响应式堆叠。

- [ ] **步骤6：运行测试和浏览器验证**

运行：`go test ./cmd/cgctl ./internal/api -count=1`

然后启动`clusterguard`，打开`http://127.0.0.1:8088/`，并在桌面和移动宽度下验证卡片是否适合、连接器是否接触卡片边缘、集群切换是否更新所有部分，以及浏览器日志中是否没有错误。

- [ ] **步骤7：提交操作员体验**

```bash
git add cmd/cgctl internal/api/console.html internal/api/console_test.go
git commit -m "feat: add MySQL topology operator experience"
```

### 任务8：文档和发布验证

**文件：**
- 修改：`README.md`
- 修改：`docs/architecture.md`
- 修改：`docs/operations.md`
- 修改：`configs/clusterguard.example.json`

**接口：**
- 使用：所有项目一的行为。
- 生成：记录的注册/发现工作流程和有证据支持的发布候选。

- [ ] **步骤1：记录支持的行为和安全边界**

记录集群注册、仅资源清单发现、候选检查、指标端点、CLI命令、凭据环境变量和元数据标识行为。明确说明所有MySQL角色变更、写入端点变更和节点生命周期操作仍不支持。

- [ ] **步骤2：运行格式、单元、竞态、构建、JSON和无依赖检查**

运行：

```bash
gofmt -w adapters cmd internal pkg
go test ./... -count=1
go test -race ./internal/... ./adapters/mysql ./pkg/... -count=1
go build ./cmd/...
for file in configs/*.json; do jq empty "$file"; done
git diff --check
patterns=('orches''trator' 'orch''ctl' 'proxy''sql' 'db''proxy' 'route''repair' 'trace''mind')
! rg -ni "$(IFS='|'; echo "${patterns[*]}")" .
```

预期结果：每个命令都以零退出，并且无依赖扫描不打印任何匹配项。

- [ ] **步骤3：运行本地HTTP和CLI烟雾测试**

使用仅测试环境凭据启动守护进程，注册一个三节点的假或实验室集群，刷新拓扑，然后运行：

```bash
cgctl clusters
cgctl topology <cluster-uuid>
cgctl candidates <cluster-uuid>
cgctl metrics <cluster-uuid>
```

预期结果：所有命令返回相同的集群UUID和实例资源UUID；没有任何命令报告变更能力。

- [ ] **步骤4：验证不支持的执行仍保持失败即阻断**

运行API测试，该测试对MySQL和所有骨架适配器提交`switchover`和`failover`执行请求。

预期结果：`501 unsupported`；假执行器记录没有变更的SQL调用。

- [ ] **步骤5：提交项目一的基线**

```bash
git add README.md docs configs
git commit -m "docs: deliver MySQL topology intelligence"
```

## 完成证据

只有当最终报告记录以下内容时，项目才算完成：

- 分支和最新提交；
- 完整的测试、竞态、构建、JSON、格式、浏览器和无依赖结果；
- 支持的MySQL版本固定值；
- 烟雾测试中注册的集群、实例、链接和指标数量；
- 候选排序和每个阻塞/警告检查；
- 确认角色变更、写入端点变更和节点生命周期仍不支持；
- 仍需完成的受保护角色操作、端点安全性和节点生命周期项目的剩余工作。
