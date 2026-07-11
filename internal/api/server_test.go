package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const testControlToken = "test-control-token"

type apiRunner struct{}

type metadataAdapterSpy struct {
	adapter.UnsupportedAdapter
	mu    sync.Mutex
	calls int
}

type apiStageFailingJournal struct {
	repository *store.Repository
	stage      model.WorkflowStage
}

func (journal apiStageFailingJournal) RecordAudit(event model.AuditEvent) error {
	if event.Stage == journal.stage {
		return errors.New("secret journal backend failure")
	}
	return journal.repository.RecordAudit(event)
}

func (journal apiStageFailingJournal) RecordReport(report model.Report) error {
	return journal.repository.RecordReport(report)
}

func newMetadataAdapterSpy() *metadataAdapterSpy {
	return &metadataAdapterSpy{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL)}
}

func (candidate *metadataAdapterSpy) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityMetadataReconcile: {Available: true},
	}}
}

func (candidate *metadataAdapterSpy) MetadataPrecheck(context.Context, adapter.MetadataRequest) ([]model.Check, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.calls++
	return nil, nil
}

func (candidate *metadataAdapterSpy) ReconcileMetadata(context.Context, adapter.MetadataRequest) (adapter.MetadataResult, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.calls++
	return adapter.MetadataResult{}, nil
}

func (candidate *metadataAdapterSpy) callCount() int {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return candidate.calls
}

type fakeRefresher struct {
	mu      sync.Mutex
	calls   []model.ResourceID
	refresh func(context.Context, model.ResourceID) (model.TopologySnapshot, error)
}

func (refresher *fakeRefresher) Refresh(ctx context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	refresher.mu.Lock()
	refresher.calls = append(refresher.calls, clusterID)
	callback := refresher.refresh
	refresher.mu.Unlock()
	if callback == nil {
		return model.TopologySnapshot{}, nil
	}
	return callback(ctx, clusterID)
}

func (refresher *fakeRefresher) callCount() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return len(refresher.calls)
}

func (apiRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) ([]mysql.Row, error) {
	if strings.HasPrefix(query, "SELECT") {
		return []mysql.Row{{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "hostname": "mysql-a", "port": "3306", "server_id": "1",
			"version": "8.0.44", "read_only": "0", "super_read_only": "0", "gtid_mode": "ON", "log_bin": "1", "binlog_format": "ROW",
		}}, nil
	}
	return nil, nil
}

func newTestServer(t *testing.T) (*Server, *store.Repository) {
	t.Helper()
	registry := adapter.NewRegistry()
	for _, candidate := range []adapter.DatabaseHAAdapter{mysql.New(apiRunner{}), postgresql.New(), oracle.New(), sqlserver.New()} {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	repository := store.NewMemory()
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, repository)
	return NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken)), repository
}

func callJSON(t *testing.T, handler http.Handler, method string, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestControlAPIPostsRequireConfiguredBearerToken(t *testing.T) {
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	refresher := &fakeRefresher{}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, repository)
	server := NewServer(registry, repository, service, refresher, WithControlToken("control-secret"))
	payload := []byte(`{"display_name":"secured","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`)

	request := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(payload))
		httpRequest.Header.Set("Content-Type", "application/json")
		if token != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httpRequest)
		return response
	}

	for _, token := range []string{"", "wrong"} {
		response := request(token)
		if response.Code != http.StatusUnauthorized || len(repository.Clusters()) != 0 {
			t.Fatalf("token %q did not fail closed: %d %s", token, response.Code, response.Body.String())
		}
	}
	if response := request("control-secret"); response.Code != http.StatusCreated || len(repository.Clusters()) != 1 {
		t.Fatalf("valid control token did not authorize registration: %d %s", response.Code, response.Body.String())
	}

	cluster := repository.Clusters()[0]
	discovery := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", strings.NewReader("{}"))
	discovery.Header.Set("Content-Type", "application/json")
	discoveryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(discoveryResponse, discovery)
	if discoveryResponse.Code != http.StatusUnauthorized || refresher.callCount() != 0 {
		t.Fatalf("anonymous discovery reached refresher: %d %s calls=%d", discoveryResponse.Code, discoveryResponse.Body.String(), refresher.callCount())
	}
}

func TestControlAPIPostsFailClosedWhenTokenIsNotConfigured(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer anything")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured control authentication status = %d, want 503: %s", response.Code, response.Body.String())
	}
}

func testInventoryGeneration(t *testing.T, repository *store.Repository, clusterID model.ResourceID) uint64 {
	t.Helper()
	inventory, found := repository.DiscoveryInventory(clusterID)
	if !found || inventory.Generation == 0 {
		t.Fatalf("missing discovery inventory for %s: %+v found=%t", clusterID, inventory, found)
	}
	return inventory.Generation
}

func TestEnginesEndpointListsAllRegisteredEngines(t *testing.T) {
	server, _ := newTestServer(t)
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/engines", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("engines status: %d %s", response.Code, response.Body.String())
	}
	for _, engine := range []string{"mysql", "postgresql", "oracle", "sqlserver"} {
		if !strings.Contains(response.Body.String(), `"engine":"`+engine+`"`) {
			t.Fatalf("engine response missing %s: %s", engine, response.Body.String())
		}
	}
}

func TestDirectDiscoveryRouteIsRemoved(t *testing.T) {
	server, _ := newTestServer(t)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/discovery", map[string]interface{}{
		"credentials": map[string]string{"username": "monitor", "password": "hidden-secret"},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("direct discovery status: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "hidden-secret") {
		t.Fatalf("direct discovery error exposed request secret: %s", response.Body.String())
	}
}

func TestGenericJSONRoutesRejectOversizedBodiesIndependentOfContentLength(t *testing.T) {
	server, repository := newTestServer(t)
	for _, path := range []string{"/api/v1/clusters", "/api/v1/operations/execute", "/api/v1/metadata/reconcile/execute"} {
		t.Run(path, func(t *testing.T) {
			body := strings.Repeat(" ", maximumJSONBodyBytes+1) + `{}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			request.ContentLength = 0
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("oversized body status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
	if len(repository.Clusters()) != 0 {
		t.Fatalf("oversized registration changed inventory: %+v", repository.Clusters())
	}
}

func TestExecuteEndpointFailsClosedWhenMutationIsUnsupported(t *testing.T) {
	server, _ := newTestServer(t)
	payload := map[string]interface{}{
		"operation":      map[string]interface{}{"engine": "mysql", "kind": "failover", "requested_by": "dba"},
		"approval_token": "approved",
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/execute", payload)
	if response.Code != http.StatusNotImplemented || !strings.Contains(response.Body.String(), "unsupported") {
		t.Fatalf("unsupported execute must fail closed: %d %s", response.Code, response.Body.String())
	}
}

func TestClusterTopologyAndHealthEndpointsUsePlatformResourceIDs(t *testing.T) {
	server, repository := newTestServer(t)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "platform-ids"}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			DisplayName: "mysql-a", Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
		}}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		Health: model.Health{State: model.HealthHealthy},
	})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	for _, path := range []string{"/api/v1/clusters/" + string(cluster.ResourceID) + "/topology", "/api/v1/clusters/" + string(cluster.ResourceID) + "/health"} {
		response := callJSON(t, server.Handler(), http.MethodGet, path, nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), string(cluster.ResourceID)) || !strings.Contains(response.Body.String(), string(snapshot.Instances[0].ResourceID)) {
			t.Fatalf("endpoint %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestMetadataExecuteReusesResourceIDForRenamedMySQLEndpoint(t *testing.T) {
	server, repository := newTestServer(t)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	seed, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName: "mysql-old", Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
	}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	payload := map[string]interface{}{
		"operation":      map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"},
		"approval_token": "approved",
		"instance": map[string]interface{}{
			"resource_id": seed.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			"display_name": "mysql-new", "hostname": "mysql-new", "ip_address": "192.0.2.20", "port": 3310, "role": "replica", "health": map[string]string{"state": "unhealthy"},
		},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("metadata execute: %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].ResourceID != seed.Instances[0].ResourceID || instances[0].Hostname != "mysql-new" || instances[0].Role != model.RolePrimary || instances[0].Health.State != model.HealthHealthy {
		t.Fatalf("metadata execute did not reconcile existing resource: %+v", instances)
	}
	updatedEndpoints := repository.Endpoints(cluster.ResourceID)
	if updatedEndpoints[0].Hostname != "mysql-new" || updatedEndpoints[0].IPAddress != "192.0.2.20" || updatedEndpoints[0].Port != 3310 {
		t.Fatalf("metadata execute did not update discovery endpoint: %+v", updatedEndpoints)
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("metadata execute must invalidate topology")
	}
}

func TestMetadataExecutePersistenceFailureIsSanitizedAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register mysql: %v", err)
	}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, repository)
	server := NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken))
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-failure"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "failure-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	beforeInstances := repository.Instances(cluster.ResourceID)
	beforeEndpoints := repository.Endpoints(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("block snapshot path: %v", err)
	}
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "failure-native"}, "hostname": "mysql-new", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), path) || strings.Contains(response.Body.String(), "rename metadata snapshot") {
		t.Fatalf("metadata persistence mapping = %d %s", response.Code, response.Body.String())
	}
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) {
		t.Fatalf("failed metadata persistence published partial state")
	}
}

func TestMetadataExecuteReturnsIndeterminateResultAfterPostCommitJournalFailure(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-indeterminate"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "indeterminate-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register mysql: %v", err)
	}
	journal := apiStageFailingJournal{repository: repository, stage: model.StageExecute}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, journal)
	server := NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken))
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "indeterminate-native"}, "hostname": "mysql-new", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"status":"indeterminate"`) || strings.Contains(response.Body.String(), "secret journal") {
		t.Fatalf("post-commit journal response = %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].Hostname != "mysql-new" {
		t.Fatalf("metadata commit was not preserved: %+v", instances)
	}
	reports := repository.Reports()
	if len(reports) != 1 || !strings.Contains(reports[0].Summary, "journal persistence failed") {
		t.Fatalf("durable report did not preserve indeterminate outcome: %+v", reports)
	}
}

func TestMetadataRouteRejectsEngineAndIdentityTrustMismatchBeforeAdapterOrWorkflow(t *testing.T) {
	tests := []struct {
		name               string
		payloadEngine      model.Engine
		operationEngine    model.Engine
		operationClusterID model.ResourceID
		serverUUID         string
	}{
		{name: "payload engine", payloadEngine: model.EnginePostgreSQL, operationEngine: model.EnginePostgreSQL, serverUUID: "trust-native"},
		{name: "operation engine", payloadEngine: model.EngineMySQL, operationEngine: model.EnginePostgreSQL, serverUUID: "trust-native"},
		{name: "operation cluster", payloadEngine: model.EngineMySQL, operationEngine: model.EngineMySQL, operationClusterID: model.NewResourceID(), serverUUID: "trust-native"},
		{name: "native identity", payloadEngine: model.EngineMySQL, operationEngine: model.EngineMySQL, serverUUID: "different-native"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-trust-" + test.name}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
			if err != nil {
				t.Fatalf("create inventory: %v", err)
			}
			observedAt := time.Date(2026, time.July, 12, 15, 0, 0, 0, time.UTC)
			snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "trust-native"}, Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
			if err != nil {
				t.Fatalf("seed topology: %v", err)
			}
			candidate := newMetadataAdapterSpy()
			server := newAPIServer(t, repository, candidate, &fakeRefresher{})
			beforeInstances := repository.Instances(cluster.ResourceID)
			beforeEndpoints := repository.Endpoints(cluster.ResourceID)
			beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
			beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
			beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
			payload := map[string]interface{}{
				"operation": map[string]interface{}{"engine": test.operationEngine, "cluster_id": test.operationClusterID, "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
				"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": test.payloadEngine, "engine_identity": map[string]string{"server_uuid": test.serverUUID}, "hostname": "must-not-publish", "port": 4406},
			}
			response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("trust mismatch status = %d: %s", response.Code, response.Body.String())
			}
			if candidate.callCount() != 0 || len(repository.Audits()) != 0 || len(repository.Reports()) != 0 {
				t.Fatalf("trust mismatch reached adapter/workflow: calls=%d audits=%d reports=%d", candidate.callCount(), len(repository.Audits()), len(repository.Reports()))
			}
			afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
			afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
			afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
			if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) || afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
				t.Fatal("trust mismatch changed repository state")
			}
		})
	}
}

func TestMetadataRouteRejectsEmptyActiveEndpointAddressAtomically(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "empty-metadata-address"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "empty-address-native"}, Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	candidate := newMetadataAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	beforeInstances := repository.Instances(cluster.ResourceID)
	beforeEndpoints := repository.Endpoints(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "empty-address-native"}, "hostname": "   ", "ip_address": "\t", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty metadata address status = %d: %s", response.Code, response.Body.String())
	}
	if candidate.callCount() != 0 || len(repository.Audits()) != 0 || len(repository.Reports()) != 0 {
		t.Fatalf("empty address reached adapter/workflow: calls=%d audits=%d reports=%d", candidate.callCount(), len(repository.Audits()), len(repository.Reports()))
	}
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) || afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
		t.Fatal("empty metadata address changed repository state")
	}
}

func TestConsoleIsServedAtRoot(t *testing.T) {
	server, _ := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected console response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "ClusterGuard HA") {
		t.Fatalf("expected standalone product console, got %s", response.Body.String())
	}
	head := httptest.NewRecorder()
	server.Handler().ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/", nil))
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("expected console HEAD response, got %d %q", head.Code, head.Header().Get("Content-Type"))
	}
}
