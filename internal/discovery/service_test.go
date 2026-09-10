package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	workflowcore "clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

var discoveryTestTime = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

type fakeDiscoveryAdapter struct {
	adapter.UnsupportedAdapter

	mu                sync.Mutex
	results           map[string]model.DatabaseInstance
	failures          map[string]error
	metricFailures    map[string]error
	topologies        map[string]adapter.TopologyResult
	topologyCalls     []string
	metricCalls       int
	calls             []string
	current           int
	maximum           int
	started           chan struct{}
	startedHosts      chan string
	release           <-chan struct{}
	releases          map[string]<-chan struct{}
	discoverAvailable bool
	topologyAvailable bool
	metricsAvailable  bool
}

type primaryFailureObservation struct {
	clusterID  model.ResourceID
	failed     bool
	observedAt time.Time
}

type primaryFailureObserverStub struct {
	observations []primaryFailureObservation
}

type discoveryConsensusStub struct {
	repository *store.Repository
	mu         sync.Mutex
	commits    int
}

func (consensus *discoveryConsensusStub) Commit(state []byte) error {
	consensus.mu.Lock()
	consensus.commits++
	consensus.mu.Unlock()
	return consensus.repository.ApplyReplicatedState(state)
}

func (consensus *discoveryConsensusStub) count() int {
	consensus.mu.Lock()
	defer consensus.mu.Unlock()
	return consensus.commits
}

func (observer *primaryFailureObserverStub) Record(clusterID model.ResourceID, failed bool, observedAt time.Time) {
	observer.observations = append(observer.observations, primaryFailureObservation{
		clusterID:  clusterID,
		failed:     failed,
		observedAt: observedAt,
	})
}

func newFakeAdapter() *fakeDiscoveryAdapter {
	return &fakeDiscoveryAdapter{
		UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL),
		results:            map[string]model.DatabaseInstance{},
		failures:           map[string]error{},
		metricFailures:     map[string]error{},
		releases:           map[string]<-chan struct{}{},
		topologies:         map[string]adapter.TopologyResult{},
		discoverAvailable:  true,
		topologyAvailable:  true,
		metricsAvailable:   true,
	}
}

func (candidate *fakeDiscoveryAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: candidate.Engine(), Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover: {Available: candidate.discoverAvailable},
		adapter.CapabilityTopology: {Available: candidate.topologyAvailable},
		adapter.CapabilityMetrics:  {Available: candidate.metricsAvailable},
	}}
}

func newPostgreSQLFakeAdapter() *fakeDiscoveryAdapter {
	candidate := newFakeAdapter()
	candidate.UnsupportedAdapter = adapter.NewUnsupported(model.EnginePostgreSQL)
	candidate.metricsAvailable = false
	return candidate
}

func newOracleFakeAdapter() *fakeDiscoveryAdapter {
	candidate := newFakeAdapter()
	candidate.UnsupportedAdapter = adapter.NewUnsupported(model.EngineOracle)
	candidate.metricsAvailable = false
	return candidate
}

func newSQLServerFakeAdapter() *fakeDiscoveryAdapter {
	candidate := newFakeAdapter()
	candidate.UnsupportedAdapter = adapter.NewUnsupported(model.EngineSQLServer)
	candidate.metricsAvailable = false
	return candidate
}

func (candidate *fakeDiscoveryAdapter) Topology(_ context.Context, request adapter.DiscoverRequest, discovery adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	host := request.Endpoint.Hostname
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.topologyCalls = append(candidate.topologyCalls, host)
	if topology, exists := candidate.topologies[host]; exists {
		return topology, nil
	}
	instance := discovery.Instance
	if len(instance.Replication.SourceIdentity) == 0 {
		return adapter.TopologyResult{}, nil
	}
	return adapter.TopologyResult{Links: []adapter.TopologyLink{{
		SourceIdentity: instance.Replication.SourceIdentity.Clone(),
		TargetIdentity: instance.EngineIdentity.Clone(),
		Healthy: instance.Health.State == model.HealthHealthy &&
			instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning,
		LagSeconds: instance.Replication.LagSeconds,
	}}}, nil
}

func (candidate *fakeDiscoveryAdapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	host := request.Endpoint.Hostname
	candidate.mu.Lock()
	candidate.calls = append(candidate.calls, host)
	candidate.current++
	if candidate.current > candidate.maximum {
		candidate.maximum = candidate.current
	}
	result := candidate.results[host]
	err := candidate.failures[host]
	started := candidate.started
	startedHosts := candidate.startedHosts
	release := candidate.release
	if hostRelease, exists := candidate.releases[host]; exists {
		release = hostRelease
	}
	candidate.mu.Unlock()

	defer func() {
		candidate.mu.Lock()
		candidate.current--
		candidate.mu.Unlock()
	}()
	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if startedHosts != nil {
		select {
		case startedHosts <- host:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	return adapter.DiscoveryResult{Instance: result}, nil
}

func (candidate *fakeDiscoveryAdapter) Metrics(_ context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	candidate.mu.Lock()
	candidate.metricCalls++
	err := candidate.metricFailures[request.Endpoint.Hostname]
	candidate.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []model.MetricSample{{
		ObservedAt: discoveryTestTime,
		Values:     map[string]float64{"host_port": float64(request.Endpoint.Port)},
	}}, nil
}

func (candidate *fakeDiscoveryAdapter) discoveredHosts() []string {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	result := append([]string{}, candidate.calls...)
	sort.Strings(result)
	return result
}

func (candidate *fakeDiscoveryAdapter) setFailure(host string, err error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if err == nil {
		delete(candidate.failures, host)
		return
	}
	candidate.failures[host] = err
}

func newTestService(t *testing.T, repository *store.Repository, candidate *fakeDiscoveryAdapter) *Service {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register fake adapter: %v", err)
	}
	return newTestServiceWithResolver(t, repository, registry, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}))
}

func newTestServiceWithResolver(t *testing.T, repository *store.Repository, registry *adapter.Registry, resolver CredentialResolver) *Service {
	t.Helper()
	var clockMu sync.Mutex
	next := discoveryTestTime.Add(-time.Nanosecond)
	return New(registry, repository, resolver, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		next = next.Add(time.Nanosecond)
		return next
	})
}

func addEndpoint(t *testing.T, repository *store.Repository, clusterID model.ResourceID, host string, port int, kind model.EndpointKind, active bool) model.Endpoint {
	t.Helper()
	endpoint, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: clusterID,
		Kind:      kind,
		Hostname:  host,
		Port:      port,
		Active:    active,
	})
	if err != nil {
		t.Fatalf("add endpoint %s: %v", host, err)
	}
	return endpoint
}

func discoveredInstance(host string, port int, nativeID string, role model.InstanceRole, sourceID string) model.DatabaseInstance {
	instance := model.DatabaseInstance{
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": nativeID},
		DisplayName:    host,
		Hostname:       host,
		Port:           port,
		Role:           role,
		Health:         model.Health{State: model.HealthHealthy, ObservedAt: discoveryTestTime},
		EngineMetadata: map[string]string{"version": "8.0.36"},
	}
	if sourceID != "" {
		lag := int64(1)
		instance.Replication = model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"server_uuid": sourceID},
			IOThread:       model.ThreadRunning,
			SQLThread:      model.ThreadRunning,
			LagSeconds:     &lag,
		}
	}
	return instance
}

func discoveredPostgreSQLInstance(host string, port int, nodeID string, systemID string, role model.InstanceRole, sourceID string) model.DatabaseInstance {
	instance := model.DatabaseInstance{
		Engine: model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       nodeID,
			"system_identifier": systemID,
		},
		DisplayName:       host,
		Hostname:          host,
		Port:              port,
		Role:              role,
		Health:            model.Health{State: model.HealthHealthy, ObservedAt: discoveryTestTime},
		PromotionEligible: role == model.RoleStandby,
		EngineMetadata:    map[string]string{"version": "16.3", "timeline_id": "7"},
	}
	if sourceID != "" {
		lag := int64(0)
		instance.Replication = model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"resource_id": sourceID, "system_identifier": systemID},
			IOThread:       model.ThreadRunning,
			SQLThread:      model.ThreadRunning,
			LagSeconds:     &lag,
		}
	}
	return instance
}

func discoveredOracleInstance(host string, port int, dbid string, database string, instanceName string, role model.InstanceRole) model.DatabaseInstance {
	instance := model.DatabaseInstance{
		Engine: model.EngineOracle,
		EngineIdentity: model.EngineIdentity{
			"dbid":           dbid,
			"db_unique_name": database,
			"instance_name":  instanceName,
		},
		DisplayName:       host,
		Hostname:          host,
		Port:              port,
		Role:              role,
		Health:            model.Health{State: model.HealthHealthy, ObservedAt: discoveryTestTime},
		PromotionEligible: role == model.RoleStandby,
		EngineMetadata:    map[string]string{"version": "19c"},
	}
	if role == model.RoleStandby {
		instance.Replication.IOThread = model.ThreadRunning
		instance.Replication.SQLThread = model.ThreadRunning
	}
	return instance
}

func discoveredSQLServerInstance(host string, port int, groupID string, replicaID string, role model.InstanceRole) model.DatabaseInstance {
	instance := model.DatabaseInstance{
		Engine: model.EngineSQLServer,
		EngineIdentity: model.EngineIdentity{
			"group_id":   groupID,
			"replica_id": replicaID,
		},
		DisplayName:       host,
		Hostname:          host,
		Port:              port,
		Role:              role,
		Health:            model.Health{State: model.HealthHealthy, ObservedAt: discoveryTestTime},
		PromotionEligible: role == model.RoleReplica,
		EngineMetadata:    map[string]string{"ag_name": "orders-ag"},
	}
	if role == model.RoleReplica {
		instance.Replication.IOThread = model.ThreadRunning
		instance.Replication.SQLThread = model.ThreadRunning
	}
	return instance
}

func TestRefreshBindsPostgreSQLSystemIdentifierOnFirstCompleteObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-production"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "pg-a", 5432, model.EndpointDatabase, true)
	standbyEndpoint := addEndpoint(t, repository, cluster.ResourceID, "pg-b", 5432, model.EndpointDatabase, true)
	primaryID := "11111111-1111-4111-8111-111111111111"
	standbyID := "22222222-2222-4222-8222-222222222222"
	systemID := "7428625847249870011"
	candidate := newPostgreSQLFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredPostgreSQLInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, primaryID, systemID, model.RolePrimary, "")
	candidate.results[standbyEndpoint.Hostname] = discoveredPostgreSQLInstance(standbyEndpoint.Hostname, standbyEndpoint.Port, standbyID, systemID, model.RoleStandby, primaryID)

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh PostgreSQL: %v", err)
	}
	stored, found := repository.Cluster(cluster.ResourceID)
	if !found || stored.EngineIdentity["system_identifier"] != systemID {
		t.Fatalf("cluster identity was not bound: %+v", stored)
	}
	if len(snapshot.Instances) != 2 || len(snapshot.Links) != 1 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected PostgreSQL topology: %+v", snapshot)
	}
}

func TestRefreshBindsOracleDataGuardClusterIdentity(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineOracle, DisplayName: "oracle-dg"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "ora-a", 1521, model.EndpointDatabase, true)
	standbyEndpoint := addEndpoint(t, repository, cluster.ResourceID, "ora-b", 1521, model.EndpointDatabase, true)
	candidate := newOracleFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredOracleInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "1234567890", "MESDB", "mesdb", model.RolePrimary)
	candidate.results[standbyEndpoint.Hostname] = discoveredOracleInstance(standbyEndpoint.Hostname, standbyEndpoint.Port, "1234567890", "REPORTDB", "mesdb", model.RoleStandby)

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh Oracle: %v", err)
	}
	stored, found := repository.Cluster(cluster.ResourceID)
	if !found || stored.EngineIdentity["dbid"] != "1234567890" || stored.EngineIdentity["db_unique_name"] != "" {
		t.Fatalf("Oracle cluster identity was not bound: %+v", stored)
	}
	if len(snapshot.Instances) != 2 || len(snapshot.Links) != 1 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected Oracle topology: %+v", snapshot)
	}
}

func TestRefreshRejectsMixedOracleDataGuardClusterIdentities(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineOracle, DisplayName: "oracle-mixed"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "ora-a", 1521, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "ora-b", 1521, model.EndpointDatabase, true)
	candidate := newOracleFakeAdapter()
	candidate.results[first.Hostname] = discoveredOracleInstance(first.Hostname, first.Port, "1234567890", "ORCL_A", "orcl1", model.RolePrimary)
	candidate.results[second.Hostname] = discoveredOracleInstance(second.Hostname, second.Port, "9999999999", "ORCL_B", "orcl2", model.RoleStandby)

	if _, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID); err == nil || !strings.Contains(err.Error(), "mixed cluster identities") {
		t.Fatalf("mixed Oracle identities were not rejected: %v", err)
	}
	stored, _ := repository.Cluster(cluster.ResourceID)
	if len(stored.EngineIdentity) != 0 || len(repository.Instances(cluster.ResourceID)) != 0 {
		t.Fatalf("failed mixed Oracle refresh published partial state: cluster=%+v instances=%+v", stored, repository.Instances(cluster.ResourceID))
	}
}

func TestRefreshBindsSQLServerAvailabilityGroupIdentity(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineSQLServer, DisplayName: "sqlserver-ag"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "sql-a", 1433, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "sql-b", 1433, model.EndpointDatabase, true)
	candidate := newSQLServerFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredSQLServerInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "11111111-1111-4111-8111-111111111111", model.RolePrimary)
	candidate.results[replicaEndpoint.Hostname] = discoveredSQLServerInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "22222222-2222-4222-8222-222222222222", model.RoleReplica)

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh SQL Server: %v", err)
	}
	stored, found := repository.Cluster(cluster.ResourceID)
	if !found || stored.EngineIdentity["group_id"] != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("SQL Server AG identity was not bound: %+v", stored)
	}
	if len(snapshot.Instances) != 2 || len(snapshot.Links) != 1 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected SQL Server topology: %+v", snapshot)
	}
}

func TestRefreshRejectsMixedSQLServerAvailabilityGroups(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineSQLServer, DisplayName: "sql-mixed"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "sql-a", 1433, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "sql-b", 1433, model.EndpointDatabase, true)
	candidate := newSQLServerFakeAdapter()
	candidate.results[first.Hostname] = discoveredSQLServerInstance(first.Hostname, first.Port, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "11111111-1111-4111-8111-111111111111", model.RolePrimary)
	candidate.results[second.Hostname] = discoveredSQLServerInstance(second.Hostname, second.Port, "ffffffff-bbbb-4ccc-8ddd-eeeeeeeeeeee", "22222222-2222-4222-8222-222222222222", model.RoleReplica)

	if _, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID); err == nil || !strings.Contains(err.Error(), "mixed cluster identities") {
		t.Fatalf("mixed SQL Server AG identities were not rejected: %v", err)
	}
	stored, _ := repository.Cluster(cluster.ResourceID)
	if len(stored.EngineIdentity) != 0 || len(repository.Instances(cluster.ResourceID)) != 0 {
		t.Fatalf("failed mixed SQL Server refresh published partial state: cluster=%+v instances=%+v", stored, repository.Instances(cluster.ResourceID))
	}
}

func TestRefreshDoesNotPublishPartialPostgreSQLIdentityBeforeFirstCompleteObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-first-bind"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "pg-a", 5432, model.EndpointDatabase, true)
	standbyEndpoint := addEndpoint(t, repository, cluster.ResourceID, "pg-b", 5432, model.EndpointDatabase, true)
	primaryID := "11111111-1111-4111-8111-111111111111"
	standbyID := "22222222-2222-4222-8222-222222222222"
	systemID := "7428625847249870011"
	candidate := newPostgreSQLFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredPostgreSQLInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, primaryID, systemID, model.RolePrimary, "")
	candidate.results[standbyEndpoint.Hostname] = discoveredPostgreSQLInstance(standbyEndpoint.Hostname, standbyEndpoint.Port, standbyID, systemID, model.RoleStandby, primaryID)
	candidate.setFailure(standbyEndpoint.Hostname, errors.New("standby unavailable"))
	service := newTestService(t, repository, candidate)

	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err == nil ||
		!strings.Contains(err.Error(), "observed 1 of 2 endpoints") {
		t.Fatalf("partial first PostgreSQL observation error=%v", err)
	}
	stored, _ := repository.Cluster(cluster.ResourceID)
	if len(stored.EngineIdentity) != 0 || len(repository.Instances(cluster.ResourceID)) != 0 {
		t.Fatalf("partial first observation changed durable identity: cluster=%+v instances=%+v", stored, repository.Instances(cluster.ResourceID))
	}
	for _, endpoint := range repository.Endpoints(cluster.ResourceID) {
		if endpoint.InstanceID != "" {
			t.Fatalf("partial first observation bound endpoint: %+v", endpoint)
		}
	}

	candidate.setFailure(standbyEndpoint.Hostname, nil)
	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("complete PostgreSQL observation: %v", err)
	}
	stored, _ = repository.Cluster(cluster.ResourceID)
	if stored.EngineIdentity["system_identifier"] != systemID || len(snapshot.Instances) != 2 {
		t.Fatalf("complete observation did not bind atomically: cluster=%+v topology=%+v", stored, snapshot)
	}
}

func TestRefreshRejectsMixedPostgreSQLSystemIdentifiersWithoutPublishing(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-mixed"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "pg-a", 5432, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "pg-b", 5432, model.EndpointDatabase, true)
	candidate := newPostgreSQLFakeAdapter()
	candidate.results[first.Hostname] = discoveredPostgreSQLInstance(first.Hostname, first.Port, "11111111-1111-4111-8111-111111111111", "7428625847249870011", model.RolePrimary, "")
	candidate.results[second.Hostname] = discoveredPostgreSQLInstance(second.Hostname, second.Port, "22222222-2222-4222-8222-222222222222", "9999999999999999999", model.RoleStandby, "11111111-1111-4111-8111-111111111111")

	if _, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID); err == nil || !strings.Contains(err.Error(), "mixed cluster identities") {
		t.Fatalf("mixed PostgreSQL cluster was not rejected: %v", err)
	}
	stored, _ := repository.Cluster(cluster.ResourceID)
	if len(stored.EngineIdentity) != 0 || len(repository.Instances(cluster.ResourceID)) != 0 {
		t.Fatalf("failed mixed refresh published partial state: cluster=%+v instances=%+v", stored, repository.Instances(cluster.ResourceID))
	}
	for _, endpoint := range repository.Endpoints(cluster.ResourceID) {
		if endpoint.InstanceID != "" {
			t.Fatalf("failed mixed refresh bound endpoint: %+v", endpoint)
		}
	}
}

func TestRefreshRejectsInventoryWithoutActiveDatabaseEndpoints(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "empty"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	addEndpoint(t, repository, cluster.ResourceID, "inactive", 3306, model.EndpointDatabase, false)
	addEndpoint(t, repository, cluster.ResourceID, "vip", 3307, model.EndpointVIP, true)
	service := newTestService(t, repository, newFakeAdapter())

	_, err = service.Refresh(context.Background(), cluster.ResourceID)
	if !errors.Is(err, ErrInventoryRequired) {
		t.Fatalf("empty database inventory error = %v, want %v", err, ErrInventoryRequired)
	}
}

func TestRefreshBuildsLinksAndMetricsOnlyFromRegisteredInventory(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	registered := []model.Endpoint{
		addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true),
		addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true),
		addEndpoint(t, repository, cluster.ResourceID, "mysql-c", 3308, model.EndpointDatabase, true),
	}
	addEndpoint(t, repository, cluster.ResourceID, "mysql-inactive", 3309, model.EndpointDatabase, false)
	addEndpoint(t, repository, cluster.ResourceID, "mysql-vip", 3310, model.EndpointVIP, true)

	candidate := newFakeAdapter()
	candidate.results["mysql-a"] = discoveredInstance("mysql-a", 3306, "native-a", model.RolePrimary, "")
	candidate.results["mysql-b"] = discoveredInstance("mysql-b", 3307, "native-b", model.RoleReplica, "native-a")
	candidate.results["mysql-c"] = discoveredInstance("mysql-c", 3308, "native-c", model.RoleReplica, "native-a")
	candidate.results["not-registered"] = discoveredInstance("not-registered", 3399, "native-extra", model.RoleReplica, "native-a")
	service := newTestService(t, repository, candidate)

	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.Instances) != 3 || len(first.Links) != 2 || len(first.Probes) != 3 {
		t.Fatalf("unexpected first topology: %+v", first)
	}
	if got, want := candidate.discoveredHosts(), []string{"mysql-a", "mysql-b", "mysql-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probed hosts = %v, want registered inventory %v", got, want)
	}
	assertSnapshotResourceOrder(t, first)

	firstIDs := instanceIDsByNativeIdentity(first.Instances)
	firstLinkIDs := linkResourceIDs(first.Links)
	wantTargets := map[model.ResourceID]bool{firstIDs["native-b"]: true, firstIDs["native-c"]: true}
	for _, link := range first.Links {
		if link.SourceInstanceID != firstIDs["native-a"] || !wantTargets[link.TargetInstanceID] {
			t.Fatalf("native replication identity resolved to the wrong platform edge: %+v", link)
		}
		delete(wantTargets, link.TargetInstanceID)
	}
	if len(wantTargets) != 0 {
		t.Fatalf("replication targets were not resolved: %v", wantTargets)
	}
	second, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !reflect.DeepEqual(instanceIDsByNativeIdentity(second.Instances), firstIDs) {
		t.Fatalf("platform instance UUIDs changed: first=%v second=%v", firstIDs, instanceIDsByNativeIdentity(second.Instances))
	}
	if !reflect.DeepEqual(linkResourceIDs(second.Links), firstLinkIDs) {
		t.Fatalf("replication link UUIDs changed: first=%v second=%v", firstLinkIDs, linkResourceIDs(second.Links))
	}
	assertSnapshotResourceOrder(t, second)

	endpoints := repository.Endpoints(cluster.ResourceID)
	bound := map[model.ResourceID]bool{}
	for _, endpoint := range endpoints {
		if endpoint.Kind == model.EndpointDatabase && endpoint.Active {
			if endpoint.InstanceID == "" {
				t.Fatalf("registered endpoint was not bound: %+v", endpoint)
			}
			bound[endpoint.InstanceID] = true
		}
	}
	if len(bound) != len(registered) {
		t.Fatalf("endpoint bindings = %v, want %d distinct instances", bound, len(registered))
	}
	samples := repository.MetricSamples(cluster.ResourceID)
	if len(samples) != 6 {
		t.Fatalf("stored metric samples = %d, want one per successful endpoint per refresh", len(samples))
	}
	for _, sample := range samples {
		if sample.InstanceID == "" || !bound[sample.InstanceID] {
			t.Fatalf("metric sample was not bound to a reconciled instance: %+v", sample)
		}
	}

	serviceType := reflect.TypeOf(service)
	if got, want := exportedMethodNames(serviceType), []string{"Refresh", "RefreshAndCommit", "RefreshBatch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("service exposes a probe path outside inventory refresh: methods=%v", exportedMethodNames(serviceType))
	}
}

func TestRefreshBatchPublishesAllClustersWithOneConsensusCommit(t *testing.T) {
	repository := store.NewMemory()
	candidate := newFakeAdapter()
	clusterIDs := make([]model.ResourceID, 0, 3)
	for index := 0; index < 3; index++ {
		cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: fmt.Sprintf("batch-%d", index)})
		if err != nil {
			t.Fatalf("create cluster %d: %v", index, err)
		}
		host := fmt.Sprintf("batch-mysql-%d", index)
		addEndpoint(t, repository, cluster.ResourceID, host, 3306+index, model.EndpointDatabase, true)
		candidate.results[host] = discoveredInstance(host, 3306+index, fmt.Sprintf("batch-native-%d", index), model.RolePrimary, "")
		clusterIDs = append(clusterIDs, cluster.ResourceID)
	}
	consensus := &discoveryConsensusStub{repository: repository}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}
	service := newTestService(t, repository, candidate)
	published, err := service.RefreshBatch(context.Background(), clusterIDs)
	if err != nil {
		t.Fatalf("refresh cluster batch: %v", err)
	}
	if len(published) != len(clusterIDs) {
		t.Fatalf("published clusters=%d want=%d", len(published), len(clusterIDs))
	}
	if commits := consensus.count(); commits != 1 {
		t.Fatalf("service batch used %d consensus commits, want one", commits)
	}
	for _, clusterID := range clusterIDs {
		snapshot, found := repository.TopologySnapshot(clusterID)
		if !found || len(snapshot.Instances) != 1 {
			t.Fatalf("cluster %s topology=%+v found=%t", clusterID, snapshot, found)
		}
	}
}

func TestRefreshUsesInventoryCoordinatesWhenReportedHostnamesCollide(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "shared-short-hostname"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "mysql-a.example.test", 3306, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "mysql-b.example.test", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[first.Hostname] = discoveredInstance("mysql-short", 3306, "native-a", model.RolePrimary, "")
	candidate.results[second.Hostname] = discoveredInstance("mysql-short", 3306, "native-b", model.RoleReplica, "native-a")

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh shared reported hostname: %v", err)
	}
	if len(snapshot.Instances) != 2 {
		t.Fatalf("shared reported hostname collapsed inventory: %+v", snapshot.Instances)
	}
	hosts := map[string]bool{}
	for _, instance := range snapshot.Instances {
		hosts[instance.Hostname] = true
		if instance.EngineMetadata["reported_hostname"] != "mysql-short" {
			t.Fatalf("reported hostname evidence missing: %+v", instance)
		}
	}
	if !hosts[first.Hostname] || !hosts[second.Hostname] {
		t.Fatalf("instances did not retain authoritative inventory coordinates: %+v", snapshot.Instances)
	}
	if _, mutated := candidate.results[first.Hostname].EngineMetadata["reported_hostname"]; mutated {
		t.Fatal("refresh mutated adapter-owned engine metadata")
	}
}

func TestRefreshRepresentsFirstAndKnownProbeFailuresWithoutInventingInstancesOrLinks(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "failures"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	never := addEndpoint(t, repository, cluster.ResourceID, "never", 3306, model.EndpointDatabase, true)
	known := addEndpoint(t, repository, cluster.ResourceID, "known", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results["known"] = discoveredInstance("known", 3307, "known-native", model.RolePrimary, "")
	candidate.setFailure("never", errors.New("offline"))
	service := newTestService(t, repository, candidate)

	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	firstProbes := probesByEndpoint(first.Probes)
	if len(first.Instances) != 1 || len(first.Links) != 0 || firstProbes[never.ResourceID].InstanceID != "" || firstProbes[never.ResourceID].Health.State != model.HealthUnhealthy || firstProbes[never.ResourceID].Outcome != model.ProbeOutcomeDatabaseUnavailable {
		t.Fatalf("never-successful endpoint invented topology: %+v", first)
	}
	knownID := firstProbes[known.ResourceID].InstanceID
	if knownID == "" || firstProbes[known.ResourceID].Health.State != model.HealthHealthy || firstProbes[known.ResourceID].Outcome != model.ProbeOutcomeReachable {
		t.Fatalf("successful endpoint was not bound: %+v", firstProbes[known.ResourceID])
	}

	candidate.setFailure("known", errors.New("temporarily unavailable"))
	second, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("failed-probe refresh: %v", err)
	}
	secondProbes := probesByEndpoint(second.Probes)
	if len(second.Instances) != 1 || len(second.Links) != 0 {
		t.Fatalf("failed bound member disappeared or invented a relation: %+v", second)
	}
	if secondProbes[known.ResourceID].InstanceID != knownID || secondProbes[known.ResourceID].Health.State != model.HealthUnhealthy || secondProbes[known.ResourceID].Outcome != model.ProbeOutcomeDatabaseUnavailable {
		t.Fatalf("known failed probe lost its UUID or health state: %+v", secondProbes[known.ResourceID])
	}
	if second.Instances[0].ResourceID != knownID || second.Instances[0].Role != model.RoleUnknown || second.Instances[0].Health.State != model.HealthUnhealthy {
		t.Fatalf("failed bound member exposed stale topology health: %+v", second.Instances)
	}
	if secondProbes[never.ResourceID].InstanceID != "" || len(repository.Instances(cluster.ResourceID)) != 1 {
		t.Fatalf("failed probes changed durable instance identity: probes=%+v instances=%+v", second.Probes, repository.Instances(cluster.ResourceID))
	}

	candidate.setFailure("known", nil)
	third, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	thirdProbe := probesByEndpoint(third.Probes)[known.ResourceID]
	if thirdProbe.InstanceID != knownID || thirdProbe.Health.State != model.HealthHealthy || thirdProbe.Outcome != model.ProbeOutcomeReachable {
		t.Fatalf("recovered endpoint did not resume its stable identity: %+v", thirdProbe)
	}
}

func TestRefreshPublishesPrimaryFailureEvidenceOnlyAfterDurableTopologyRefresh(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "failure-evidence"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register fake adapter: %v", err)
	}
	observer := &primaryFailureObserverStub{}
	now := discoveryTestTime.Add(-5 * time.Second)
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}), func() time.Time {
		now = now.Add(5 * time.Second)
		return now
	}, WithPrimaryFailureObserver(observer))

	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("healthy refresh: %v", err)
	}
	candidate.setFailure(primaryEndpoint.Hostname, errors.New("primary unreachable"))
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("failed-primary refresh: %v", err)
	}
	candidate.setFailure(primaryEndpoint.Hostname, nil)
	candidate.metricFailures[primaryEndpoint.Hostname] = errors.New("metrics unavailable")
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("metrics-only failure refresh: %v", err)
	}

	want := []primaryFailureObservation{
		{clusterID: cluster.ResourceID, failed: false, observedAt: discoveryTestTime},
		{clusterID: cluster.ResourceID, failed: true, observedAt: discoveryTestTime.Add(5 * time.Second)},
		{clusterID: cluster.ResourceID, failed: false, observedAt: discoveryTestTime.Add(10 * time.Second)},
	}
	if !reflect.DeepEqual(observer.observations, want) {
		t.Fatalf("primary failure evidence = %+v, want %+v", observer.observations, want)
	}
}

func TestDatabasePortFailureFeedsStableAutomaticFailoverWindow(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "database-port-failure"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register fake adapter: %v", err)
	}
	window := coordination.NewFailureWindow(6, 30*time.Second)
	now := discoveryTestTime.Add(-5 * time.Second)
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}), func() time.Time {
		now = now.Add(5 * time.Second)
		return now
	}, WithPrimaryFailureObserver(window))

	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("healthy refresh: %v", err)
	}
	candidate.setFailure(primaryEndpoint.Hostname, errors.New("dial tcp 192.0.2.10:3306: connect: connection refused"))
	for check := 0; check < 7; check++ {
		if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
			t.Fatalf("database-port failure refresh %d: %v", check+1, err)
		}
		if check < 6 && window.Stable(cluster.ResourceID, now) {
			t.Fatalf("database-port failure became stable after only %d checks", check+1)
		}
	}
	if !window.Stable(cluster.ResourceID, now) {
		t.Fatal("six follow-up database-port failures across 30 seconds did not open the automatic failover window")
	}

	candidate.setFailure(primaryEndpoint.Hostname, nil)
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("database recovery refresh: %v", err)
	}
	if window.Stable(cluster.ResourceID, now) {
		t.Fatal("one successful database probe did not close the automatic failover window")
	}
}

func TestSlowUnavailablePrimaryDoesNotConsumeTheWholeRefreshDeadline(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "bounded-primary-probe"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")
	service := newTestService(t, repository, candidate)
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}

	candidate.releases[primaryEndpoint.Hostname] = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	started := time.Now()
	snapshot, err := service.Refresh(ctx, cluster.ResourceID)
	if err != nil {
		t.Fatalf("bounded failed-primary refresh: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 1200*time.Millisecond {
		t.Fatalf("refresh consumed the caller deadline: %s", elapsed)
	}
	probes := probesByEndpoint(snapshot.Probes)
	if probes[primaryEndpoint.ResourceID].Outcome != model.ProbeOutcomeDatabaseUnavailable {
		t.Fatalf("slow primary probe outcome=%s, want database unavailable", probes[primaryEndpoint.ResourceID].Outcome)
	}
	if probes[replicaEndpoint.ResourceID].Outcome != model.ProbeOutcomeReachable || probes[replicaEndpoint.ResourceID].DiscoveryObservedAt.IsZero() {
		t.Fatalf("reachable replica observation was discarded: %+v", probes[replicaEndpoint.ResourceID])
	}
}

func TestPrimaryFailureEvidenceUsesHAOwnerAfterDiscoveryServiceRestart(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "restart-failure-evidence"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register fake adapter: %v", err)
	}
	credentials := CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	})
	now := discoveryTestTime
	firstService := New(registry, repository, credentials, func() time.Time {
		observedAt := now
		now = now.Add(5 * time.Second)
		return observedAt
	})
	healthy, err := firstService.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("healthy refresh: %v", err)
	}
	primaryID, ok := currentObservedPrimaryID(healthy)
	if !ok {
		t.Fatalf("healthy primary not observed: %+v", healthy)
	}
	if _, _, err := repository.PutHAEndpoint(store.HAEndpointSpec{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "eth0", Prefix: 24, OwnerID: primaryID, Active: true,
	}); err != nil {
		t.Fatalf("register HA endpoint: %v", err)
	}

	candidate.setFailure(primaryEndpoint.Hostname, errors.New("primary unavailable"))
	if _, err := firstService.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("first failed refresh: %v", err)
	}
	stale, _ := repository.TopologySnapshot(cluster.ResourceID)
	for _, instance := range stale.Instances {
		if instance.ResourceID == primaryID && instance.Role != model.RoleUnknown {
			t.Fatalf("failed primary retained a current role before restart: %+v", instance)
		}
	}

	observer := &primaryFailureObserverStub{}
	restartedService := New(registry, repository, credentials, func() time.Time {
		observedAt := now
		now = now.Add(5 * time.Second)
		return observedAt
	}, WithPrimaryFailureObserver(observer))
	if _, err := restartedService.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("failed refresh after service restart: %v", err)
	}
	want := []primaryFailureObservation{{clusterID: cluster.ResourceID, failed: true, observedAt: discoveryTestTime.Add(10 * time.Second)}}
	if !reflect.DeepEqual(observer.observations, want) {
		t.Fatalf("failure evidence after restart = %+v, want %+v", observer.observations, want)
	}
}

func TestRefreshRetainsFailedMemberRelationAsUnhealthyUntilSuccessfulObservationUpdatesIt(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "relation-failure"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")
	service := newTestService(t, repository, candidate)

	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.Links) != 1 || !first.Links[0].Healthy {
		t.Fatalf("initial relation was not healthy: %+v", first.Links)
	}
	linkID := first.Links[0].ResourceID
	replicaID := probesByEndpoint(first.Probes)[replicaEndpoint.ResourceID].InstanceID

	candidate.setFailure(replicaEndpoint.Hostname, errors.New("temporarily unavailable"))
	second, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("failed-member refresh: %v", err)
	}
	if len(second.Instances) != 2 || len(second.Links) != 1 || second.Links[0].ResourceID != linkID || second.Links[0].Healthy {
		t.Fatalf("failed member relation was not retained as unhealthy: %+v", second)
	}
	instances := make(map[model.ResourceID]model.DatabaseInstance, len(second.Instances))
	for _, instance := range second.Instances {
		instances[instance.ResourceID] = instance
	}
	if instances[replicaID].Health.State != model.HealthUnhealthy {
		t.Fatalf("failed replica retained stale health: %+v", instances[replicaID])
	}

	candidate.setFailure(replicaEndpoint.Hostname, nil)
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "")
	third, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("relation update refresh: %v", err)
	}
	if len(third.Links) != 0 {
		t.Fatalf("successful observation did not replace the old relation: %+v", third.Links)
	}
}

func TestRefreshMarksMultipleWritablePrimariesCriticalAndDegraded(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "split-brain"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results["mysql-a"] = discoveredInstance("mysql-a", 3306, "native-a", model.RolePrimary, "")
	candidate.results["mysql-b"] = discoveredInstance("mysql-b", 3307, "native-b", model.RolePrimary, "")
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if snapshot.Health.State != model.HealthDegraded || len(snapshot.Anomalies) != 1 {
		t.Fatalf("multiple primaries did not degrade the cluster: %+v", snapshot)
	}
	anomaly := snapshot.Anomalies[0]
	if anomaly.ClusterID != cluster.ResourceID || anomaly.Kind != "multiple_writable_primaries" || anomaly.Severity != "critical" {
		t.Fatalf("unexpected multiple-primary anomaly: %+v", anomaly)
	}
	if len(snapshot.Instances) != 2 || snapshot.Instances[0].Role != model.RolePrimary || snapshot.Instances[1].Role != model.RolePrimary {
		t.Fatalf("discovery implicitly selected a primary: %+v", snapshot.Instances)
	}
	persisted := anomaliesForClusterID(repository.Anomalies(), cluster.ResourceID)
	if len(persisted) != 1 || persisted[0].Kind != anomaly.Kind {
		t.Fatalf("cluster anomaly was not persisted: %+v", persisted)
	}
}

func TestRefreshClusterHealthRequiresCompleteHealthySinglePrimaryTopology(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeDiscoveryAdapter, []model.Endpoint)
		want  model.HealthState
	}{
		{
			name: "healthy single primary and replica",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				candidate.results[endpoints[1].Hostname] = discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
			},
			want: model.HealthHealthy,
		},
		{
			name: "zero primary",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RoleReplica, "")
				candidate.results[endpoints[1].Hostname] = discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
			},
			want: model.HealthDegraded,
		},
		{
			name: "multiple primary",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				candidate.results[endpoints[1].Hostname] = discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RolePrimary, "")
			},
			want: model.HealthDegraded,
		},
		{
			name: "unknown probe",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				candidate.setFailure(endpoints[1].Hostname, errors.New("offline"))
			},
			want: model.HealthDegraded,
		},
		{
			name: "degraded instance",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				instance := discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				instance.Health.State = model.HealthDegraded
				candidate.results[endpoints[0].Hostname] = instance
				candidate.results[endpoints[1].Hostname] = discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
			},
			want: model.HealthDegraded,
		},
		{
			name: "unhealthy instance",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				instance := discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				instance.Health.State = model.HealthUnhealthy
				candidate.results[endpoints[0].Hostname] = instance
				candidate.results[endpoints[1].Hostname] = discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
			},
			want: model.HealthDegraded,
		},
		{
			name: "stopped replica IO thread",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				replica := discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
				replica.Replication.IOThread = model.ThreadStopped
				candidate.results[endpoints[1].Hostname] = replica
			},
			want: model.HealthDegraded,
		},
		{
			name: "stopped replica SQL thread",
			setup: func(candidate *fakeDiscoveryAdapter, endpoints []model.Endpoint) {
				candidate.results[endpoints[0].Hostname] = discoveredInstance(endpoints[0].Hostname, endpoints[0].Port, "native-a", model.RolePrimary, "")
				replica := discoveredInstance(endpoints[1].Hostname, endpoints[1].Port, "native-b", model.RoleReplica, "native-a")
				replica.Replication.SQLThread = model.ThreadStopped
				candidate.results[endpoints[1].Hostname] = replica
			},
			want: model.HealthDegraded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "health-" + strings.ReplaceAll(test.name, " ", "-")})
			if err != nil {
				t.Fatalf("create cluster: %v", err)
			}
			endpoints := []model.Endpoint{
				addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true),
				addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true),
			}
			candidate := newFakeAdapter()
			test.setup(candidate, endpoints)
			snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if snapshot.Health.State != test.want {
				t.Fatalf("health = %s, want %s; snapshot=%+v", snapshot.Health.State, test.want, snapshot)
			}
		})
	}
}

func TestRefreshPersistsSeparateCurrentDiscoveryAndMetricsEvidence(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "evidence"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	good := addEndpoint(t, repository, cluster.ResourceID, "good", 3306, model.EndpointDatabase, true)
	metricsFailed := addEndpoint(t, repository, cluster.ResourceID, "metrics-failed", 3307, model.EndpointDatabase, true)
	databaseFailed := addEndpoint(t, repository, cluster.ResourceID, "database-failed", 3308, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[good.Hostname] = discoveredInstance(good.Hostname, good.Port, "native-good", model.RolePrimary, "")
	candidate.results[metricsFailed.Hostname] = discoveredInstance(metricsFailed.Hostname, metricsFailed.Port, "native-metrics", model.RoleReplica, "native-good")
	candidate.metricFailures[metricsFailed.Hostname] = errors.New("metrics unavailable")
	candidate.setFailure(databaseFailed.Hostname, errors.New("database unavailable"))

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	probes := probesByEndpoint(snapshot.Probes)
	if probes[good.ResourceID].DiscoveryObservedAt != discoveryTestTime || probes[good.ResourceID].MetricsObservedAt != discoveryTestTime {
		t.Fatalf("successful probe evidence is incomplete: %+v", probes[good.ResourceID])
	}
	if probes[metricsFailed.ResourceID].DiscoveryObservedAt != discoveryTestTime || !probes[metricsFailed.ResourceID].MetricsObservedAt.IsZero() {
		t.Fatalf("metrics failure evidence is incorrect: %+v", probes[metricsFailed.ResourceID])
	}
	if !probes[databaseFailed.ResourceID].DiscoveryObservedAt.IsZero() || !probes[databaseFailed.ResourceID].MetricsObservedAt.IsZero() {
		t.Fatalf("database failure invented current evidence: %+v", probes[databaseFailed.ResourceID])
	}
}

func TestRefreshBoundsConcurrentEndpointProbesAtFour(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "parallel"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	candidate := newFakeAdapter()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	candidate.started = started
	candidate.release = release
	for index := 0; index < 8; index++ {
		host := "mysql-" + string(rune('a'+index))
		addEndpoint(t, repository, cluster.ResourceID, host, 3306+index, model.EndpointDatabase, true)
		candidate.results[host] = discoveredInstance(host, 3306+index, "native-"+host, model.RoleUnknown, "")
	}
	service := newTestService(t, repository, candidate)
	done := make(chan error, 1)
	go func() {
		_, err := service.Refresh(context.Background(), cluster.ResourceID)
		done <- err
	}()

	for index := 0; index < 4; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d probes started before timeout", index)
		}
	}
	select {
	case <-started:
		t.Fatal("a fifth probe started while four probes were blocked")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	candidate.mu.Lock()
	maximum := candidate.maximum
	candidate.mu.Unlock()
	if maximum != 4 {
		t.Fatalf("maximum concurrent probes = %d, want 4", maximum)
	}
}

func TestRefreshFailsClosedWhenDiscoveryIsUnsupported(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		configure          func(*fakeDiscoveryAdapter, string)
		wantDiscoveryCalls int
	}{
		{
			name: "capability unavailable",
			configure: func(candidate *fakeDiscoveryAdapter, _ string) {
				candidate.discoverAvailable = false
			},
		},
		{
			name: "adapter returns unsupported",
			configure: func(candidate *fakeDiscoveryAdapter, host string) {
				candidate.failures[host] = adapter.ErrUnsupported
			},
			wantDiscoveryCalls: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, endpoint := seedDiscoveryRepositoryState(t, repository)
			before := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
			candidate := newFakeAdapter()
			candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "replacement-native", model.RolePrimary, "")
			testCase.configure(candidate, endpoint.Hostname)
			service := newTestService(t, repository, candidate)

			_, err := service.Refresh(context.Background(), cluster.ResourceID)
			if !errors.Is(err, adapter.ErrUnsupported) {
				t.Fatalf("unsupported discovery error = %v, want %v", err, adapter.ErrUnsupported)
			}
			if calls := len(candidate.discoveredHosts()); calls != testCase.wantDiscoveryCalls {
				t.Fatalf("discovery calls = %d, want %d", calls, testCase.wantDiscoveryCalls)
			}
			after := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("unsupported discovery changed repository state:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestRefreshSanitizesCredentialAndDatabaseProbeErrors(t *testing.T) {
	const secret = "super-secret-password"
	for _, testCase := range []struct {
		name        string
		wantSummary string
		wantState   model.HealthState
		wantOutcome model.ProbeOutcome
		configure   func(*testing.T, *store.Repository, model.DatabaseCluster, model.Endpoint, *fakeDiscoveryAdapter) *Service
	}{
		{
			name:        "credential resolver",
			wantSummary: "discovery credentials unavailable",
			wantState:   model.HealthUnknown,
			wantOutcome: model.ProbeOutcomeCredentialsUnavailable,
			configure: func(t *testing.T, repository *store.Repository, _ model.DatabaseCluster, _ model.Endpoint, candidate *fakeDiscoveryAdapter) *Service {
				registry := adapter.NewRegistry()
				if err := registry.Register(candidate); err != nil {
					t.Fatalf("register fake adapter: %v", err)
				}
				return newTestServiceWithResolver(t, repository, registry, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
					return adapter.Credentials{}, errors.New("resolver leaked " + secret)
				}))
			},
		},
		{
			name:        "database adapter",
			wantSummary: "database probe failed",
			wantState:   model.HealthUnhealthy,
			wantOutcome: model.ProbeOutcomeDatabaseUnavailable,
			configure: func(t *testing.T, repository *store.Repository, _ model.DatabaseCluster, endpoint model.Endpoint, candidate *fakeDiscoveryAdapter) *Service {
				candidate.failures[endpoint.Hostname] = errors.New("client leaked " + secret)
				return newTestService(t, repository, candidate)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "sanitized"})
			if err != nil {
				t.Fatalf("create cluster: %v", err)
			}
			endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
			candidate := newFakeAdapter()
			service := testCase.configure(t, repository, cluster, endpoint, candidate)

			snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if len(snapshot.Probes) != 1 || snapshot.Probes[0].Health.State != testCase.wantState || snapshot.Probes[0].Health.Summary != testCase.wantSummary || snapshot.Probes[0].Outcome != testCase.wantOutcome {
				t.Fatalf("unexpected sanitized probe status: %+v", snapshot.Probes)
			}
			if strings.Contains(strings.ToLower(fmtSnapshot(snapshot)), strings.ToLower(secret)) {
				t.Fatalf("snapshot exposed secret-bearing error: %+v", snapshot)
			}
		})
	}
}

func TestRefreshMetricsFailureDegradesProbeAndClusterWithoutStoringSample(t *testing.T) {
	const secret = "metrics-client-secret"
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metrics"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	candidate.metricFailures[endpoint.Hostname] = errors.New("metrics leaked " + secret)
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Instances) != 1 || len(snapshot.Probes) != 1 {
		t.Fatalf("successful discovery was lost after metrics failure: %+v", snapshot)
	}
	if snapshot.Probes[0].Health.State != model.HealthDegraded || snapshot.Probes[0].Health.Summary != "performance metrics unavailable" {
		t.Fatalf("metrics failure did not degrade probe health safely: %+v", snapshot.Probes[0])
	}
	if snapshot.Health.State != model.HealthDegraded || snapshot.Health.Summary != "performance metrics unavailable" {
		t.Fatalf("metrics failure did not degrade cluster health safely: %+v", snapshot.Health)
	}
	if len(repository.MetricSamples(cluster.ResourceID)) != 0 {
		t.Fatalf("metrics failure stored samples: %+v", repository.MetricSamples(cluster.ResourceID))
	}
	if strings.Contains(strings.ToLower(fmtSnapshot(snapshot)), strings.ToLower(secret)) {
		t.Fatalf("snapshot exposed metrics error: %+v", snapshot)
	}
}

func TestRefreshDeduplicatesTwoEndpointsForOneNativeIdentity(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "aliases"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "mysql-a.internal", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[first.Hostname] = discoveredInstance(first.Hostname, first.Port, "shared-native", model.RolePrimary, "")
	candidate.results[second.Hostname] = discoveredInstance(second.Hostname, second.Port, "shared-native", model.RolePrimary, "")
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Instances) != 1 || len(snapshot.Anomalies) != 0 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("duplicate native observation created false topology: %+v", snapshot)
	}
	probes := probesByEndpoint(snapshot.Probes)
	if probes[first.ResourceID].InstanceID == "" || probes[first.ResourceID].InstanceID != probes[second.ResourceID].InstanceID {
		t.Fatalf("duplicate native endpoints did not bind one platform UUID: %+v", snapshot.Probes)
	}
	bound := repository.Endpoints(cluster.ResourceID)
	if len(bound) != 2 || bound[0].InstanceID == "" || bound[0].InstanceID != bound[1].InstanceID {
		t.Fatalf("repository endpoint bindings were not deduplicated: %+v", bound)
	}
}

func TestRefreshPublishesAdapterNativeTopologyFromTheSameObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "adapter-topology"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	primary := discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	replica := discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "")
	candidate.results[primaryEndpoint.Hostname] = primary
	candidate.results[replicaEndpoint.Hostname] = replica
	lag := int64(3)
	candidate.topologies[replicaEndpoint.Hostname] = adapter.TopologyResult{Links: []adapter.TopologyLink{{
		SourceIdentity: primary.EngineIdentity.Clone(),
		TargetIdentity: replica.EngineIdentity.Clone(),
		Healthy:        true,
		LagSeconds:     &lag,
	}}}

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Links) != 1 || snapshot.Links[0].LagSeconds == nil || *snapshot.Links[0].LagSeconds != lag {
		t.Fatalf("adapter-native topology was not published: %+v", snapshot.Links)
	}
	candidate.mu.Lock()
	topologyCalls := append([]string{}, candidate.topologyCalls...)
	candidate.mu.Unlock()
	sort.Strings(topologyCalls)
	if !reflect.DeepEqual(topologyCalls, []string{primaryEndpoint.Hostname, replicaEndpoint.Hostname}) {
		t.Fatalf("topology calls = %v", topologyCalls)
	}
}

func TestRefreshFallsBackToDiscoveryRelationsWhenTopologyCapabilityIsUnavailable(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "topology-fallback"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	replicaEndpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.topologyAvailable = false
	candidate.results[primaryEndpoint.Hostname] = discoveredInstance(primaryEndpoint.Hostname, primaryEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[replicaEndpoint.Hostname] = discoveredInstance(replicaEndpoint.Hostname, replicaEndpoint.Port, "native-b", model.RoleReplica, "native-a")

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Links) != 1 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("topology-incapable adapter lost discovered relation: %+v", snapshot)
	}
	candidate.mu.Lock()
	topologyCalls := len(candidate.topologyCalls)
	candidate.mu.Unlock()
	if topologyCalls != 0 {
		t.Fatalf("unsupported topology method was called %d times", topologyCalls)
	}
}

func TestRefreshSkipsMetricsWhenCapabilityIsUnavailable(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metrics-optional"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.metricsAvailable = false
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")

	snapshot, err := newTestService(t, repository, candidate).Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if snapshot.Health.State != model.HealthHealthy || len(snapshot.Probes) != 1 || !snapshot.Probes[0].MetricsObservedAt.IsZero() {
		t.Fatalf("missing metrics capability degraded discovery: %+v", snapshot)
	}
	if len(repository.MetricSamples(cluster.ResourceID)) != 0 {
		t.Fatalf("metrics were stored without capability: %+v", repository.MetricSamples(cluster.ResourceID))
	}
	candidate.mu.Lock()
	metricCalls := candidate.metricCalls
	candidate.mu.Unlock()
	if metricCalls != 0 {
		t.Fatalf("unsupported metrics method was called %d times", metricCalls)
	}
}

func TestRefreshSerializesSameClusterWhileDifferentClustersProceed(t *testing.T) {
	repository := store.NewMemory()
	firstCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "first"})
	if err != nil {
		t.Fatalf("create first cluster: %v", err)
	}
	secondCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "second"})
	if err != nil {
		t.Fatalf("create second cluster: %v", err)
	}
	firstEndpoint := addEndpoint(t, repository, firstCluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	secondEndpoint := addEndpoint(t, repository, secondCluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[firstEndpoint.Hostname] = discoveredInstance(firstEndpoint.Hostname, firstEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[secondEndpoint.Hostname] = discoveredInstance(secondEndpoint.Hostname, secondEndpoint.Port, "native-b", model.RolePrimary, "")
	started := make(chan string, 4)
	releaseFirst := make(chan struct{})
	candidate.startedHosts = started
	candidate.releases[firstEndpoint.Hostname] = releaseFirst
	service := newTestService(t, repository, candidate)

	refresh := func(clusterID model.ResourceID) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, err := service.Refresh(context.Background(), clusterID)
			result <- err
		}()
		return result
	}
	firstRefresh := refresh(firstCluster.ResourceID)
	if host := waitForStartedHost(t, started); host != firstEndpoint.Hostname {
		t.Fatalf("first probe host = %s, want %s", host, firstEndpoint.Hostname)
	}
	queuedSameCluster := refresh(firstCluster.ResourceID)
	differentCluster := refresh(secondCluster.ResourceID)
	if host := waitForStartedHost(t, started); host != secondEndpoint.Hostname {
		t.Fatalf("same-cluster refresh interleaved before independent cluster: started %s", host)
	}
	if err := <-differentCluster; err != nil {
		t.Fatalf("different-cluster refresh: %v", err)
	}
	select {
	case host := <-started:
		t.Fatalf("same-cluster refresh entered adapter while prior refresh was blocked: %s", host)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstRefresh; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if host := waitForStartedHost(t, started); host != firstEndpoint.Hostname {
		t.Fatalf("queued same-cluster probe host = %s, want %s", host, firstEndpoint.Hostname)
	}
	if err := <-queuedSameCluster; err != nil {
		t.Fatalf("queued same-cluster refresh: %v", err)
	}
}

func TestCanceledProbeDoesNotPublishADegradedObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "cancel-probe"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	service := newTestService(t, repository, candidate)
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}
	before := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
	beforeTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !found {
		t.Fatal("seed refresh did not publish topology")
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	candidate.started = started
	candidate.release = release
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, refreshErr := service.Refresh(ctx, cluster.ResourceID)
		result <- refreshErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("canceled probe did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled refresh error = %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("canceled probe did not return promptly")
	}
	after := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !reflect.DeepEqual(after, before) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) {
		t.Fatalf("canceled probe changed repository state:\nbefore=%+v\nafter=%+v\nbefore topology=%+v\nafter topology=%+v", before, after, beforeTopology, afterTopology)
	}
}

func TestCanceledRefreshStopsWhileWaitingForClusterLock(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "cancel-lock"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	candidate.started = started
	candidate.release = release
	service := newTestService(t, repository, candidate)
	first := make(chan error, 1)
	go func() {
		_, refreshErr := service.Refresh(context.Background(), cluster.ResourceID)
		first <- refreshErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not acquire the cluster lock")
	}

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, refreshErr := service.Refresh(ctx, cluster.ResourceID)
		second <- refreshErr
	}()
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lock wait cancellation error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-first
		<-second
		t.Fatal("refresh ignored cancellation while waiting for the cluster lock")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
}

func TestRefreshWaitsWithoutPublishingWhileWorkflowHoldsClusterFence(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "shared-fence"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	probeStarted := make(chan struct{}, 1)
	candidate.started = probeStarted
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	locks := workflowcore.NewMemoryLocks()
	_, release, err := locks.Acquire(context.Background(), model.Operation{ClusterID: cluster.ResourceID})
	if err != nil {
		t.Fatalf("acquire workflow fence: %v", err)
	}
	defer release()
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}), func() time.Time { return discoveryTestTime }, WithPublicationFence(locks))

	type refreshResult struct {
		snapshot model.TopologySnapshot
		err      error
	}
	result := make(chan refreshResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		snapshot, refreshErr := service.Refresh(ctx, cluster.ResourceID)
		result <- refreshResult{snapshot: snapshot, err: refreshErr}
	}()

	select {
	case <-probeStarted:
		t.Fatal("discovery probed the database while workflow held the cluster fence")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case value := <-result:
		t.Fatalf("discovery did not wait for the active workflow fence: %v", value.err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("discovery published topology while workflow held the cluster fence")
	}
	release()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("discovery did not begin probing after the workflow fence was released")
	}
	select {
	case value := <-result:
		if value.err != nil || value.snapshot.ClusterID != cluster.ResourceID {
			t.Fatalf("discovery did not resume after the workflow fence was released: snapshot=%+v err=%v", value.snapshot, value.err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery remained blocked after the workflow fence was released")
	}
}

func TestRefreshBatchDoesNotProbeWhileWorkflowHoldsClusterFence(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "batch-fence"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	probeStarted := make(chan struct{}, 1)
	candidate.started = probeStarted
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	locks := workflowcore.NewMemoryLocks()
	_, release, err := locks.Acquire(context.Background(), model.Operation{ClusterID: cluster.ResourceID})
	if err != nil {
		t.Fatalf("acquire workflow fence: %v", err)
	}
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}), func() time.Time { return discoveryTestTime }, WithPublicationFence(locks))

	result := make(chan error, 1)
	go func() {
		_, refreshErr := service.RefreshBatch(context.Background(), []model.ResourceID{cluster.ResourceID})
		result <- refreshErr
	}()
	select {
	case <-probeStarted:
		t.Fatal("batch discovery probed the database while workflow held the cluster fence")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("batch discovery did not begin probing after the workflow fence was released")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("batch discovery after fence release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("batch discovery remained blocked after the workflow fence was released")
	}
}

func TestRefreshAndCommitPreventsInterveningPublication(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "recovery-completion"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	locks := workflowcore.NewMemoryLocks()
	service := newTestService(t, repository, candidate)
	service.publicationFence = locks
	other := newTestService(t, repository, candidate)
	other.publicationFence = locks
	other.now = func() time.Time { return discoveryTestTime.Add(time.Second) }
	commitFailure := errors.New("completion commit rejected")
	_, err = service.RefreshAndCommit(context.Background(), cluster.ResourceID, func(snapshot model.TopologySnapshot) error {
		stored, found := repository.TopologySnapshot(cluster.ResourceID)
		if !found || !stored.ObservedAt.Equal(snapshot.ObservedAt) {
			t.Fatal("callback ran before the observation was persisted")
		}
		for _, publisher := range []*Service{service, other} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, publishErr := publisher.Refresh(ctx, cluster.ResourceID)
			cancel()
			if publishErr == nil {
				t.Fatal("another observation published before completion committed")
			}
		}
		return commitFailure
	})
	if !errors.Is(err, commitFailure) {
		t.Fatalf("callback failure was hidden: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := other.Refresh(ctx, cluster.ResourceID); err != nil {
		t.Fatalf("failed completion leaked the publication fence: %v", err)
	}
}

func TestRefreshAndCommitDoesNotCommitFailedOrCancelledObservation(t *testing.T) {
	service := newTestService(t, store.NewMemory(), newFakeAdapter())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{context.Background(), ctx} {
		_, err := service.RefreshAndCommit(ctx, model.NewResourceID(), func(model.TopologySnapshot) error {
			t.Fatal("failed refresh invoked completion")
			return nil
		})
		if err == nil {
			t.Fatal("invalid refresh succeeded")
		}
	}
}

func TestRefreshEvictsClusterLocksForArbitraryUnknownIDs(t *testing.T) {
	service := newTestService(t, store.NewMemory(), newFakeAdapter())
	for index := 0; index < 256; index++ {
		if _, err := service.Refresh(context.Background(), model.NewResourceID()); err == nil {
			t.Fatal("unknown inventory cluster refresh must fail")
		}
	}
	service.clusterLocksMu.Lock()
	defer service.clusterLocksMu.Unlock()
	if len(service.clusterLocks) != 0 {
		t.Fatalf("unknown cluster IDs leaked %d lock entries", len(service.clusterLocks))
	}
}

func TestRefreshPreservesTypedStaleObservationThroughWrapping(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "stale-service"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}), func() time.Time { return discoveryTestTime })
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); !errors.Is(err, store.ErrStaleObservation) {
		t.Fatalf("wrapped stale error = %v", err)
	}
}

func TestMetadataCoordinateUpdateChangesNextDiscoveryEndpoint(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-endpoint"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	candidate := newFakeAdapter()
	oldInstance := model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "metadata-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}
	candidate.results["mysql-old"] = oldInstance
	service := newTestService(t, repository, candidate)
	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	metadata := first.Instances[0]
	metadata.Hostname = "mysql-new"
	metadata.Port = 4406
	if _, endpoint, err := repository.ReconcileMetadataCoordinates(store.MetadataCoordinates{Instance: metadata}); err != nil {
		t.Fatalf("reconcile metadata: %v", err)
	} else if endpoint.ResourceID != endpoints[0].ResourceID {
		t.Fatalf("single bound endpoint was not inferred: %+v", endpoint)
	}
	newInstance := oldInstance
	newInstance.Hostname = "mysql-new"
	newInstance.Port = 4406
	candidate.results["mysql-new"] = newInstance
	if _, err := service.Refresh(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("refresh updated endpoint: %v", err)
	}
	if hosts := candidate.discoveredHosts(); !reflect.DeepEqual(hosts, []string{"mysql-new", "mysql-old"}) {
		t.Fatalf("discovery did not use metadata-updated endpoint: %v", hosts)
	}
}

func TestRefreshRejectsInFlightObservationAfterMetadataInventoryChange(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "in-flight-metadata"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	candidate := newFakeAdapter()
	candidate.results["mysql-old"] = model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "in-flight-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}
	service := newTestService(t, repository, candidate)
	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("seed refresh: %v", err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	candidate.started = started
	candidate.release = release
	result := make(chan error, 1)
	go func() {
		_, refreshErr := service.Refresh(context.Background(), cluster.ResourceID)
		result <- refreshErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("in-flight refresh did not start")
	}
	metadata := first.Instances[0]
	metadata.Hostname = "mysql-new"
	metadata.Port = 4406
	if _, _, err := repository.ReconcileMetadataCoordinates(store.MetadataCoordinates{Instance: metadata}); err != nil {
		t.Fatalf("reconcile metadata during probe: %v", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, store.ErrInventoryChanged) {
		t.Fatalf("in-flight refresh error = %v", err)
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("old-address in-flight refresh republished invalid topology")
	}
}

type discoveryRepositoryState struct {
	instances []model.DatabaseInstance
	endpoints []model.Endpoint
	links     []model.ReplicationLink
	metrics   []model.MetricSample
	anomalies []model.MetadataAnomaly
}

func seedDiscoveryRepositoryState(t *testing.T, repository *store.Repository) (model.DatabaseCluster, model.Endpoint) {
	t.Helper()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "seeded"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	instance := discoveredInstance(endpoint.Hostname, endpoint.Port, "seed-native", model.RolePrimary, "")
	instance.ClusterID = cluster.ResourceID
	result, err := repository.ReconcileInstance(instance)
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	endpoint.InstanceID = result.Instance.ResourceID
	if _, err := repository.UpsertEndpoint(endpoint); err != nil {
		t.Fatalf("seed endpoint binding: %v", err)
	}
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{SourceInstanceID: result.Instance.ResourceID, TargetInstanceID: model.NewResourceID()}}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{InstanceID: result.Instance.ResourceID, ObservedAt: discoveryTestTime, Values: map[string]float64{"qps": 1}}}, 60); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(cluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "seed", Severity: "warning"}}); err != nil {
		t.Fatalf("seed anomalies: %v", err)
	}
	return cluster, endpoint
}

func captureDiscoveryRepositoryState(repository *store.Repository, clusterID model.ResourceID) discoveryRepositoryState {
	return discoveryRepositoryState{
		instances: repository.Instances(clusterID),
		endpoints: repository.Endpoints(clusterID),
		links:     repository.ReplicationLinks(clusterID),
		metrics:   repository.MetricSamples(clusterID),
		anomalies: anomaliesForClusterID(repository.Anomalies(), clusterID),
	}
}

func fmtSnapshot(snapshot model.TopologySnapshot) string {
	return fmt.Sprintf("%+v", snapshot)
}

func waitForStartedHost(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case host := <-started:
		return host
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe to start")
		return ""
	}
}

func probesByEndpoint(probes []model.ProbeStatus) map[model.ResourceID]model.ProbeStatus {
	result := make(map[model.ResourceID]model.ProbeStatus, len(probes))
	for _, probe := range probes {
		result[probe.EndpointID] = probe
	}
	return result
}

func instanceIDsByNativeIdentity(instances []model.DatabaseInstance) map[string]model.ResourceID {
	result := make(map[string]model.ResourceID, len(instances))
	for _, instance := range instances {
		result[instance.EngineIdentity["server_uuid"]] = instance.ResourceID
	}
	return result
}

func linkResourceIDs(links []model.ReplicationLink) map[string]model.ResourceID {
	result := make(map[string]model.ResourceID, len(links))
	for _, link := range links {
		result[string(link.SourceInstanceID)+"->"+string(link.TargetInstanceID)] = link.ResourceID
	}
	return result
}

func assertSnapshotResourceOrder(t *testing.T, snapshot model.TopologySnapshot) {
	t.Helper()
	if !sort.SliceIsSorted(snapshot.Instances, func(i, j int) bool { return snapshot.Instances[i].ResourceID < snapshot.Instances[j].ResourceID }) {
		t.Fatalf("instances are not ordered by resource UUID: %+v", snapshot.Instances)
	}
	if !sort.SliceIsSorted(snapshot.Links, func(i, j int) bool { return snapshot.Links[i].ResourceID < snapshot.Links[j].ResourceID }) {
		t.Fatalf("links are not ordered by resource UUID: %+v", snapshot.Links)
	}
	if !sort.SliceIsSorted(snapshot.Probes, func(i, j int) bool { return snapshot.Probes[i].EndpointID < snapshot.Probes[j].EndpointID }) {
		t.Fatalf("probes are not ordered by endpoint UUID: %+v", snapshot.Probes)
	}
	if !sort.SliceIsSorted(snapshot.Anomalies, func(i, j int) bool { return snapshot.Anomalies[i].ResourceID < snapshot.Anomalies[j].ResourceID }) {
		t.Fatalf("anomalies are not ordered by resource UUID: %+v", snapshot.Anomalies)
	}
}

func exportedMethodNames(candidate reflect.Type) []string {
	result := make([]string, 0, candidate.NumMethod())
	for index := 0; index < candidate.NumMethod(); index++ {
		result = append(result, candidate.Method(index).Name)
	}
	return result
}

func anomaliesForClusterID(anomalies []model.MetadataAnomaly, clusterID model.ResourceID) []model.MetadataAnomaly {
	result := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range anomalies {
		if anomaly.ClusterID == clusterID {
			result = append(result, anomaly)
		}
	}
	return result
}
