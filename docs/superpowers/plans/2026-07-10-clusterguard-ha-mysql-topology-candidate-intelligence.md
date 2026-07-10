# MySQL Topology and Candidate Intelligence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver inventory-scoped MySQL topology discovery, replication and performance health, candidate ranking, APIs, CLI output, and a compact topology console without enabling any database mutation.

**Architecture:** Extend common resources and adapter contracts first, then persist inventory endpoints, replication links, and bounded metrics. A discovery service probes only registered endpoints, reconciles immutable MySQL identities, constructs links, and delegates candidate evaluation to the MySQL adapter. API, CLI, and console consume the same platform snapshot.

**Tech Stack:** Go 1.19+, standard library HTTP/JSON/process execution, atomic JSON snapshot persistence, embedded HTML/CSS/JavaScript, fake SQL runners, and browser verification.

## Global Constraints

- Implement behavior independently; do not copy source, APIs, configuration, schemas, package structure, or runtime dependencies from the frozen prototype.
- Keep platform UUIDs as resource keys; hostname, IP address, port, server ID, and display name are mutable metadata.
- Probe and control only endpoints registered in the selected cluster inventory.
- Keep switchover, failover, former-primary rejoin, VIP mutation, and node lifecycle execution unsupported in this project.
- Never persist discovery passwords, include them in command arguments, or return them through APIs, audit records, or reports.
- Keep PostgreSQL, Oracle, and SQL Server registered and fail closed for unimplemented methods.
- Use TDD for every task and commit each independently green deliverable.

---

### Task 1: Common Topology, Metrics, and Candidate Contracts

**Files:**
- Create: `pkg/model/topology.go`
- Modify: `pkg/model/model.go`
- Modify: `pkg/adapter/adapter.go`
- Modify: `adapters/mysql/mysql.go`
- Modify: `adapters/postgresql/postgresql.go`
- Modify: `adapters/oracle/oracle.go`
- Modify: `adapters/sqlserver/sqlserver.go`
- Test: `pkg/model/topology_test.go`
- Test: `pkg/adapter/registry_test.go`

**Interfaces:**
- Produces: `model.ReplicationStatus`, `model.MetricSample`, `model.TopologySnapshot`, `model.CandidatePolicy`, `model.CandidateAssessment`, `adapter.CapabilityMetrics`, `adapter.CapabilityCandidates`, `DatabaseHAAdapter.Metrics`, and `DatabaseHAAdapter.EvaluateCandidates`.
- Preserves: existing `DatabaseHAAdapter` methods and explicit unsupported behavior for skeleton adapters.

- [ ] **Step 1: Write failing model and adapter contract tests**

Add tests that construct a replica with portable replication state and require every skeleton adapter to reject the new methods:

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

- [ ] **Step 2: Run the focused tests and observe the missing types and methods**

Run: `go test ./pkg/model ./pkg/adapter -count=1`

Expected: compile failure naming `ReplicationStatus`, `Metrics`, and `EvaluateCandidates`.

- [ ] **Step 3: Implement the common contracts**

Create the following types and add `Replication`, `Maintenance`, `PromotionEligible`, and `EngineMetadata` to `DatabaseInstance`:

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

Add `CapabilityMetrics`, `CapabilityCandidates`, `Metrics(context.Context, DiscoverRequest) ([]model.MetricSample, error)`, and `EvaluateCandidates(context.Context, CandidateRequest) ([]model.CandidateAssessment, error)` to the adapter contract. `CandidateRequest` contains `Cluster`, `Primary`, `Instances`, `Links`, and `Policy`. All four adapters initially return `ErrUnsupported` from both methods and advertise both capabilities as unavailable. Tasks 3 and 5 replace the MySQL placeholders and advertise each capability only after its implementation is tested.

Use this exact request contract:

```go
type CandidateRequest struct {
	Cluster   model.DatabaseCluster  `json:"cluster"`
	Primary   model.DatabaseInstance `json:"primary"`
	Instances []model.DatabaseInstance `json:"instances"`
	Links     []model.ReplicationLink `json:"links"`
	Policy    model.CandidatePolicy  `json:"policy"`
}
```

- [ ] **Step 4: Run contract tests**

Run: `go test ./pkg/model ./pkg/adapter ./adapters/... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the contracts**

```bash
git add pkg/model pkg/adapter adapters/mysql adapters/postgresql adapters/oracle adapters/sqlserver
git commit -m "feat: define topology and candidate contracts"
```

### Task 2: Durable Cluster Inventory, Links, and Bounded Metrics

**Files:**
- Modify: `internal/store/repository.go`
- Test: `internal/store/repository_test.go`

**Interfaces:**
- Consumes: Task 1 model types.
- Produces: `CreateClusterWithEndpoints`, `UpsertEndpoint`, `Endpoints`, `ReplaceReplicationLinks`, `ReplicationLinks`, `StoreMetricSamples`, `MetricSamples`, and `FindInstanceByIdentity`.

- [ ] **Step 1: Write failing persistence and inventory tests**

Add tests that register two endpoints, persist one link and one metric sample,
reload the file repository, and assert all resources survive. Add a second test
that asks `FindInstanceByIdentity` for a known MySQL `server_uuid`.

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

- [ ] **Step 2: Run the repository tests and observe missing methods**

Run: `go test ./internal/store -count=1`

Expected: compile failure for the six new repository methods.

- [ ] **Step 3: Extend the snapshot and repository**

Add snapshot maps for endpoints, replication links, and metric slices. Clone all
nested maps and identities on read. Implement these exact signatures:

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

`UpsertEndpoint` rejects invalid ports, empty or unknown cluster UUIDs, and duplicate active
addresses within one cluster. `StoreMetricSamples` keeps the newest `limit`
samples per instance, ordered by `ObservedAt`.

`CreateClusterWithEndpoints` validates the complete request before taking the
repository lock, assigns all UUIDs, and persists the cluster and endpoint set in
one snapshot write. Any invalid or colliding endpoint leaves no cluster or
endpoint behind.

- [ ] **Step 4: Run repository tests including persistence reload**

Run: `go test ./internal/store -count=1`

Expected: PASS.

- [ ] **Step 5: Commit durable inventory**

```bash
git add internal/store
git commit -m "feat: persist cluster inventory and topology state"
```

### Task 3: Multi-version MySQL Probe and Metrics Parser

**Files:**
- Create: `adapters/mysql/runner.go`
- Create: `adapters/mysql/probe.go`
- Create: `adapters/mysql/metrics.go`
- Modify: `adapters/mysql/mysql.go`
- Test: `adapters/mysql/probe_test.go`
- Test: `adapters/mysql/metrics_test.go`
- Modify: `adapters/mysql/mysql_test.go`
- Modify: `internal/api/server_test.go`
- Modify: `pkg/adapter/registry_test.go`

**Interfaces:**
- Consumes: Task 1 adapter and model contracts.
- Produces: header-aware `SQLRunner.Query`, MySQL identity/replication parsing, read-only metrics, and complete `Discover` output for supported MySQL generations.

- [ ] **Step 1: Write failing header parser and version-compatibility tests**

Define a fake runner that returns `[]Row` and cover both terminology families:

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

Add table tests for versions `5.7.44`, `8.0.44`, `8.4.10`, and `9.7.0` using
the same engine-neutral result assertions. Add a runner test proving the password
is in `MYSQL_PWD` and absent from `command.Args`.

- [ ] **Step 2: Run adapter tests and observe missing row-based probe code**

Run: `go test ./adapters/mysql -count=1`

Expected: compile failure for `Row`, `parseReplication`, and new runner method.

- [ ] **Step 3: Implement header-aware query execution**

Use this contract:

```go
type Row map[string]string

type SQLRunner interface {
	Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)
}
```

The CLI runner invokes the local client with `--batch --raw` and without
`--skip-column-names`, parses the first TSV line as headers, and maps each later
line into a `Row`. It sets the password only through `MYSQL_PWD`.

- [ ] **Step 4: Implement identity and replication probes**

Run an aliased identity query for server UUID, hostname, port, server ID,
version, read-only flags, GTID mode, binary logging, and binlog format. Probe
replication with `SHOW REPLICA STATUS`, falling back to `SHOW SLAVE STATUS` only
when the first statement is rejected. Normalize field names with:

```go
func first(row Row, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(row[name]); value != "" { return value }
	}
	return ""
}
```

An empty replication result identifies a writable primary only when both
read-only flags are false. A replica with missing or stopped threads is degraded,
not healthy. Store version, GTID mode, binary-log state, and binlog format in
`EngineMetadata`.

- [ ] **Step 5: Implement read-only performance metrics**

Parse `SHOW GLOBAL STATUS` for `Questions`, `Com_commit`, `Com_rollback`,
`Threads_connected`, `Threads_running`, `Slow_queries`, `Innodb_buffer_pool_reads`,
and `Innodb_buffer_pool_read_requests`. Return counters using these exact keys:
`questions_total`, `transactions_total`, `connections`, `running_threads`,
`slow_queries_total`, and `buffer_pool_hit_ratio`. Clamp the ratio to `[0,1]`.

- [ ] **Step 6: Run all MySQL adapter tests**

Update the shared adapter contract test so MySQL advertises metrics and returns
samples, while PostgreSQL, Oracle, and SQL Server continue to advertise metrics
and candidates as unavailable. MySQL candidate evaluation remains unavailable
until Task 5.

Run: `go test ./adapters/mysql -count=1`

Expected: PASS across all four version fixtures.

- [ ] **Step 7: Commit the MySQL probe**

```bash
git add adapters/mysql
git commit -m "feat: discover MySQL replication and performance state"
```

### Task 4: Inventory-scoped Cluster Discovery Service

**Files:**
- Create: `internal/discovery/service.go`
- Test: `internal/discovery/service_test.go`
- Modify: `pkg/model/topology.go`
- Modify: `pkg/model/topology_test.go`
- Modify: `internal/store/repository.go`
- Modify: `internal/store/repository_test.go`

**Interfaces:**
- Consumes: adapter registry, repository inventory, MySQL credentials, Task 3 discovery results.
- Produces: `Service.Refresh(context.Context, model.ResourceID) (model.TopologySnapshot, error)`, `model.ProbeStatus`, `Repository.ReplaceClusterAnomalies`, `Repository.ApplyDiscoveryRefresh`, and `ErrInventoryRequired`.

- [ ] **Step 1: Write failing service tests for inventory scope and link resolution**

Use a fake adapter that returns one primary and two replicas whose
`Replication.SourceIdentity` points to the primary UUID. Assert that `Refresh`
probes exactly the three registered endpoints, preserves the three platform
UUIDs on a second refresh, and writes two links. Add a test that supplies an
unregistered endpoint and confirms there is no public service method capable of
probing it.

Add a failed-probe test. A never-discovered endpoint appears in `Probes` with
its endpoint UUID and unknown health but does not create a fake
`DatabaseInstance`. A previously discovered endpoint retains its instance UUID
and appears unknown until a later successful refresh.

Add tests proving unsupported adapters fail before any repository publication,
resolver/adapter error text is not copied into public health summaries, a
metrics failure degrades the probe and cluster without storing a sample, two
endpoints resolving to one `server_uuid` produce one instance and no false
split-brain alert, and simultaneous refreshes of one cluster cannot interleave.

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

- [ ] **Step 2: Run discovery tests and observe the missing service**

Run: `go test ./internal/discovery -count=1`

Expected: compile failure because `Service` and `Refresh` do not exist.

- [ ] **Step 3: Implement deterministic refresh orchestration**

Extend the snapshot contract before implementing the service:

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

Create `Service` with registry, repository, credential resolver, clock, and a
bounded parallelism of four. `Refresh` performs these steps in order:

1. Load the cluster and its active database endpoints.
2. Reject an empty inventory with `ErrInventoryRequired`.
3. Require the selected adapter to advertise discovery before starting probes;
   an unsupported engine returns `adapter.ErrUnsupported` without publication.
4. Serialize refresh publication per cluster and probe each endpoint through
   the selected engine adapter.
5. Reconcile every successful native identity and bind the inventory endpoint's
   `InstanceID` to the returned platform UUID.
6. Resolve each source native identity to a platform instance UUID.
7. Publish instances, endpoint bindings, replication links, metric samples, and
   cluster anomalies through one repository snapshot transaction.
8. Return endpoint-level unknown probe status for failures without inventing an
   engine identity, instance, or replication link.

If two writable primaries are discovered, return the snapshot with a critical
anomaly and degraded cluster health; do not choose one implicitly.

Add `ReplaceClusterAnomalies(clusterID model.ResourceID, anomalies []model.MetadataAnomaly) error` to the repository. It atomically replaces discovery anomalies for that cluster while retaining anomalies for other clusters.

Add these transaction inputs and method to the repository:

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

`ApplyDiscoveryRefresh` locks once, clones the current snapshot, reconciles all
native identities in the candidate, binds every successful endpoint, resolves
replication links, appends bounded metric samples, replaces cluster anomalies,
persists the candidate once, and only then publishes it. Any error returns with
the live snapshot unchanged. Duplicate observations with the same engine
identity bind every endpoint to one platform UUID and return one instance.

Public `ProbeStatus.Health.Summary` values use fixed classifications such as
`database probe failed`, `discovery credentials unavailable`, and
`performance metrics unavailable`; they never contain raw resolver, client, or
adapter error strings. A metrics failure leaves database discovery successful
but changes the probe and overall snapshot health to degraded.

Update `ReconcileInstance` so an existing identity receives the latest
`Replication`, `EngineMetadata`, `Maintenance`, and `PromotionEligible` values.
Use candidate-snapshot persistence so a persistence error leaves the live
instance unchanged. Add store tests that first reconcile a replica, refresh it
with changed lag/thread state and metadata, and then force persistence failure
to prove both successful refresh and rollback behavior.

- [ ] **Step 4: Run discovery and store tests**

Run: `go test ./internal/discovery ./internal/store ./pkg/model -count=1`

Expected: PASS.

- [ ] **Step 5: Commit cluster discovery**

```bash
git add internal/discovery internal/store pkg/model
git commit -m "feat: refresh inventory-scoped MySQL topology"
```

### Task 5: GTID-aware Candidate Evaluation

**Files:**
- Create: `adapters/mysql/gtid.go`
- Create: `adapters/mysql/candidates.go`
- Test: `adapters/mysql/gtid_test.go`
- Test: `adapters/mysql/candidates_test.go`

**Interfaces:**
- Consumes: `adapter.CandidateRequest` and discovered replication state.
- Produces: deterministic MySQL `EvaluateCandidates` results and `CapabilityCandidates` availability.

- [ ] **Step 1: Write failing GTID set tests**

Cover normalized UUID intervals, subset comparison, missing transactions, and
errant transactions:

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

- [ ] **Step 2: Write failing candidate ranking tests**

Create one zero-lag healthy replica, one lagged replica, one stopped-thread
replica, and one errant replica. Require the healthy replica to rank first,
the lagged replica to remain eligible with warning risk when inside policy, and
the latter two to be blocked. Verify stable ordering by resource UUID when two
candidates have equal scores.

- [ ] **Step 3: Run MySQL tests and observe missing evaluator code**

Run: `go test ./adapters/mysql -run 'TestCompareGTID|TestEvaluateCandidates' -count=1`

Expected: compile failure for `ParseGTIDSet`, `CompareGTIDSets`, and evaluator behavior.

- [ ] **Step 4: Implement GTID interval normalization and comparison**

Parse comma-separated UUID groups and colon-separated inclusive intervals.
Merge overlapping intervals per UUID. Return a parse error for descending,
zero, malformed, or UUID-less intervals. `CompareGTIDSets` counts candidate
transactions missing from the primary set and transactions present only on the
candidate.

- [ ] **Step 5: Implement candidate checks and deterministic ranking**

Evaluate these blocking checks: inventory membership, non-primary role,
reachability, promotion eligibility, maintenance state, running replication
threads, source identity equals current primary, policy lag, GTID enabled when
required, no errant transactions, and compatible major version. Emit warnings
for nonzero lag and incomplete probe coverage. Rank by:

1. no warnings before warnings;
2. fewer missing transactions;
3. lower lag;
4. exact primary version before compatible version;
5. lexical platform UUID.

Set `Rank` only on eligible candidates and advertise `CapabilityCandidates` as
available. Keep `CapabilityExecute` unavailable.

- [ ] **Step 6: Run all adapter and contract tests**

Run: `go test ./adapters/mysql ./pkg/adapter -count=1`

Expected: PASS.

- [ ] **Step 7: Commit candidate intelligence**

```bash
git add adapters/mysql pkg/adapter
git commit -m "feat: rank MySQL promotion candidates"
```

### Task 6: Cluster Registration, Topology, Candidate, and Metrics APIs

**Files:**
- Create: `internal/api/clusters.go`
- Create: `internal/api/metrics.go`
- Create: `internal/metrics/service.go`
- Create: `internal/metrics/service_test.go`
- Modify: `internal/runtime/runtime.go`
- Modify: `internal/api/server.go`
- Modify: `internal/api/server_test.go`
- Create: `internal/api/clusters_test.go`
- Create: `internal/api/metrics_test.go`

**Interfaces:**
- Consumes: repository, discovery service, adapter registry, candidate evaluator.
- Produces: cluster registration, refresh, topology, candidates, derived rates, JSON metrics, and Prometheus text routes.

- [ ] **Step 1: Write failing cluster registration and refresh API tests**

Test `POST /api/v1/clusters` with a stable name and three database endpoints,
then `POST /api/v1/clusters/{id}/discover`. Assert the response includes one
primary, two replicas, and two links. Submit a duplicate active endpoint and
expect `409`. Attempt refresh for a UUID without inventory and expect `422`.

Use this registration payload:

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

- [ ] **Step 2: Write failing candidate and metrics API tests**

Test:

- `GET /api/v1/clusters/{id}/topology`
- `GET /api/v1/clusters/{id}/candidates`
- `GET /api/v1/clusters/{id}/metrics`
- `GET /api/v1/clusters/{id}/metrics/prometheus`

The candidate result must use platform UUIDs. The Prometheus body must include
`clusterguard_mysql_replication_lag_seconds` and
`clusterguard_mysql_connections` with `cluster_id` and `instance_id` labels,
and must not include hostname as identity.

Add a metrics-service test with two samples ten seconds apart. A questions delta
of 50 must produce QPS 5, transaction delta 20 must produce TPS 2, and slow
queries delta 10 must produce slow-query rate 1. A counter decrease represents a
restart and omits that rate rather than emitting a negative value.

- [ ] **Step 3: Run API tests and observe missing routes**

Run: `go test ./internal/api -count=1`

Expected: `404` or compile failures for the new handlers.

- [ ] **Step 4: Implement strict registration and read APIs**

Split cluster handlers out of `server.go`. Registration creates the cluster UUID
and endpoints through `CreateClusterWithEndpoints`, so endpoint validation and
the snapshot write are atomic. Discovery uses only repository inventory and configured credentials; the
API accepts no password field. Candidate policy defaults to maximum lag 10
seconds and required GTID, with optional query overrides bounded to safe numeric
values.

Implement `metrics.Service.Derive(samples []model.MetricSample) map[model.ResourceID]map[string]float64` using the newest two samples per instance. It returns `qps`, `tps`, and `slow_queries_per_second` only when timestamps increase and counters do not decrease; gauges and buffer-pool ratio come from the newest sample.

Prometheus output escapes label values and emits only finite numeric samples.
Set `Content-Type: text/plain; version=0.0.4; charset=utf-8`.

Extend `runtime.New` in this task to construct the discovery service from the
configured MySQL credentials and pass it to `api.NewServer`. Update API test
construction to inject a fake discovery service, so no unit test opens a real
database connection.

- [ ] **Step 5: Remove unrestricted direct discovery**

Remove `POST /api/v1/discovery`. Add an API test asserting it returns `404`, so a
caller cannot probe or reconcile a node outside registered inventory.

- [ ] **Step 6: Run API and full unit tests**

Run: `go test ./internal/api ./internal/discovery ./... -count=1`

Expected: PASS.

- [ ] **Step 7: Commit API delivery**

```bash
git add internal/api
git commit -m "feat: expose MySQL topology and candidate APIs"
```

### Task 7: CLI and Compact Topology Console

**Files:**
- Modify: `cmd/cgctl/main.go`
- Modify: `cmd/cgctl/main_test.go`
- Modify: `internal/api/console.html`
- Modify: `internal/api/console_test.go`
- Test: `internal/api/console_test.go`

**Interfaces:**
- Consumes: Task 6 API routes.
- Produces: `cgctl topology`, `cgctl candidates`, `cgctl metrics`, cluster refresh command, and a selected-cluster topology view.

- [ ] **Step 1: Write failing CLI route and output tests**

Extend the command table tests:

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

- [ ] **Step 2: Write failing console contract tests**

Require the embedded page to fetch topology and candidates for the selected
cluster and render the following labels: `主库`, `候选节点`, `从库`, `延迟`,
`版本`, `IP`, `端口`, and `VIP` only when an HA endpoint exists. Require a
single refresh button and no switch/execute button in this project.

- [ ] **Step 3: Run CLI and console tests and observe failures**

Run: `go test ./cmd/cgctl ./internal/api -count=1`

Expected: compile failure for `requestFor` or missing console route strings.

- [ ] **Step 4: Implement CLI commands**

Replace `endpointFor` with `requestFor(arguments) (method, path string, err error)`.
Use `http.NewRequest` so refresh sends `POST` with an empty JSON object. Preserve
`--json`; human output lists resource UUID, display endpoint, role, health, lag,
and candidate rank without exposing credentials.

- [ ] **Step 5: Implement the topology console**

Render a compact rounded-rectangle topology with one primary column and a
replica column. Each node displays hostname, IP, port on one centered line and
version, role, lag on a second centered line. Lines connect to fixed card anchor
points. Candidate selection only highlights a card; it does not execute a role
change. Use existing panel widths, 8px-or-smaller radii, restrained colors, and
responsive stacking below 900px.

- [ ] **Step 6: Run tests and browser verification**

Run: `go test ./cmd/cgctl ./internal/api -count=1`

Then start `clusterguardd`, open `http://127.0.0.1:8088/`, and verify at desktop
and mobile widths that cards fit, connectors meet card edges, cluster switching
updates all sections, and browser logs contain no errors.

- [ ] **Step 7: Commit the operator experience**

```bash
git add cmd/cgctl internal/api/console.html internal/api/console_test.go
git commit -m "feat: add MySQL topology operator experience"
```

### Task 8: Documentation and Release Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/operations.md`
- Modify: `configs/clusterguard.example.json`

**Interfaces:**
- Consumes: all project-one behavior.
- Produces: documented registration/discovery workflow and an evidence-backed release candidate.

- [ ] **Step 1: Document supported behavior and safety boundary**

Document cluster registration, inventory-only discovery, candidate checks,
metrics endpoints, CLI commands, credential environment variables, and metadata
identity behavior. State explicitly that all MySQL role mutation, writer
endpoint mutation, and node lifecycle operations remain unsupported.

- [ ] **Step 2: Run formatting, unit, race, build, JSON, and clean-room checks**

Run:

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

Expected: every command exits zero and the clean-room scan prints no matches.

- [ ] **Step 3: Run local HTTP and CLI smoke tests**

Start the daemon with test-only environment credentials, register a three-node
fake or lab cluster, refresh topology, then run:

```bash
cgctl clusters
cgctl topology <cluster-uuid>
cgctl candidates <cluster-uuid>
cgctl metrics <cluster-uuid>
```

Expected: all commands return the same cluster UUID and instance resource UUIDs;
no command reports a mutating capability.

- [ ] **Step 4: Verify unsupported execution remains fail closed**

Run the API test that posts `switchover` and `failover` execution requests for
MySQL and all skeleton adapters.

Expected: `501 unsupported`; fake runners record no mutating SQL call.

- [ ] **Step 5: Commit the project-one baseline**

```bash
git add README.md docs configs
git commit -m "docs: deliver MySQL topology intelligence"
```

## Completion Evidence

The project is complete only when the final report records:

- branch and latest commit;
- full test, race, build, JSON, format, browser, and clean-room results;
- supported MySQL version fixtures;
- registered cluster, instance, link, and metric counts from the smoke test;
- candidate ordering and every blocking/warning check;
- confirmation that role changes, writer endpoint mutation, and node lifecycle
  remain unsupported;
- remaining work for guarded role operations, endpoint safety, and node
  lifecycle projects.
