package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/discovery"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type candidateAdapterSpy struct {
	adapter.UnsupportedAdapter
	mu                 sync.Mutex
	requests           []adapter.CandidateRequest
	databaseProbeCalls int
	evaluationError    error
}

type realMySQLCandidateRunner struct{}

func (realMySQLCandidateRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]mysql.Row, error) {
	const primaryUUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const replicaUUID = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	if strings.HasPrefix(query, "SELECT @@server_uuid") {
		serverUUID, serverID, readOnly, superReadOnly := primaryUUID, "1", "0", "0"
		if endpoint.Hostname == "mysql-b" {
			serverUUID, serverID, readOnly, superReadOnly = replicaUUID, "2", "1", "1"
		}
		return []mysql.Row{{
			"server_uuid": serverUUID, "hostname": endpoint.Hostname, "port": fmt.Sprint(endpoint.Port), "server_id": serverID,
			"version": "8.0.44", "read_only": readOnly, "super_read_only": superReadOnly, "gtid_mode": "ON",
			"gtid_executed": primaryUUID + ":1-20", "log_bin": "1", "binlog_format": "ROW",
		}}, nil
	}
	if query == "SHOW REPLICA STATUS" {
		if endpoint.Hostname == "mysql-a" {
			return nil, nil
		}
		return []mysql.Row{{
			"Source_UUID": primaryUUID, "Replica_IO_Running": "Yes", "Replica_SQL_Running": "Yes",
			"Seconds_Behind_Source": "0", "Retrieved_Gtid_Set": primaryUUID + ":1-20", "Executed_Gtid_Set": primaryUUID + ":1-20",
		}}, nil
	}
	if query == "SHOW GLOBAL STATUS" {
		return []mysql.Row{
			{"Variable_name": "Questions", "Value": "10"},
			{"Variable_name": "Com_commit", "Value": "3"},
			{"Variable_name": "Com_rollback", "Value": "0"},
			{"Variable_name": "Threads_connected", "Value": "2"},
			{"Variable_name": "Threads_running", "Value": "1"},
			{"Variable_name": "Slow_queries", "Value": "0"},
			{"Variable_name": "Innodb_buffer_pool_reads", "Value": "1"},
			{"Variable_name": "Innodb_buffer_pool_read_requests", "Value": "100"},
		}, nil
	}
	return nil, fmt.Errorf("unexpected query %q", query)
}

func newCandidateAdapterSpy() *candidateAdapterSpy {
	return &candidateAdapterSpy{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL)}
}

func (candidate *candidateAdapterSpy) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityCandidates: {Available: true},
	}}
}

func (candidate *candidateAdapterSpy) Discover(context.Context, adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.databaseProbeCalls++
	return adapter.DiscoveryResult{}, errors.New("database probe must not be called")
}

func (candidate *candidateAdapterSpy) Metrics(context.Context, adapter.DiscoverRequest) ([]model.MetricSample, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.databaseProbeCalls++
	return nil, errors.New("database probe must not be called")
}

func (candidate *candidateAdapterSpy) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.requests = append(candidate.requests, request)
	if candidate.evaluationError != nil {
		return nil, candidate.evaluationError
	}
	result := make([]model.CandidateAssessment, 0)
	for _, instance := range request.Instances {
		if instance.Role == model.RoleReplica {
			result = append(result, model.CandidateAssessment{InstanceID: instance.ResourceID, Eligible: true, Rank: 1})
		}
	}
	return result, nil
}

func (candidate *candidateAdapterSpy) captured() ([]adapter.CandidateRequest, int) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return append([]adapter.CandidateRequest{}, candidate.requests...), candidate.databaseProbeCalls
}

func newAPIServer(t *testing.T, repository *store.Repository, candidate adapter.DatabaseHAAdapter, refresher Refresher, extraOptions ...ServerOption) *Server {
	t.Helper()
	registry := adapter.NewRegistry()
	if candidate != nil {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register candidate adapter: %v", err)
		}
	}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository)
	options := append([]ServerOption{WithControlToken(testControlToken)}, extraOptions...)
	return NewServer(registry, repository, service, refresher, options...)
}

func TestRegisterClusterAndRefreshOnlyRegisteredInventory(t *testing.T) {
	repository := store.NewMemory()
	refresher := &fakeRefresher{}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	payload := map[string]interface{}{
		"display_name": "payments-mysql",
		"engine":       "mysql",
		"endpoints": []map[string]interface{}{
			{"hostname": "mysql-a", "ip_address": "192.0.2.10", "port": 3306},
			{"hostname": "mysql-b", "ip_address": "192.0.2.11", "port": 3306},
			{"hostname": "mysql-c", "ip_address": "192.0.2.12", "port": 3306},
		},
	}
	registered := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", payload)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status: %d %s", registered.Code, registered.Body.String())
	}
	var body struct {
		Result struct {
			Cluster   model.DatabaseCluster `json:"cluster"`
			Endpoints []model.Endpoint      `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if !model.ValidResourceID(body.Result.Cluster.ResourceID) || len(body.Result.Endpoints) != 3 {
		t.Fatalf("registration did not create stable inventory: %+v", body.Result)
	}
	refresher.refresh = func(_ context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
		endpoints := repository.Endpoints(clusterID)
		observations := make([]store.DiscoveryObservation, 0, len(endpoints))
		probes := make([]model.ProbeStatus, 0, len(endpoints))
		for _, endpoint := range endpoints {
			role := model.RoleReplica
			source := model.EngineIdentity{"server_uuid": "native-a"}
			nativeID := "native-" + strings.TrimPrefix(endpoint.Hostname, "mysql-")
			if endpoint.Hostname == "mysql-a" {
				role = model.RolePrimary
				source = nil
			}
			instance := model.DatabaseInstance{
				Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": nativeID},
				Hostname: endpoint.Hostname, IPAddress: endpoint.IPAddress, Port: endpoint.Port, Role: role,
				Health:      model.Health{State: model.HealthHealthy},
				Replication: model.ReplicationStatus{SourceIdentity: source, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning},
			}
			observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
			probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}})
		}
		return repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
			ClusterID: clusterID, InventoryGeneration: testInventoryGeneration(t, repository, clusterID), Observations: observations, Probes: probes,
			Health: model.Health{State: model.HealthHealthy}, ObservedAt: time.Now().UTC(),
		})
	}

	refresh := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(body.Result.Cluster.ResourceID)+"/discover", nil)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status: %d %s", refresh.Code, refresh.Body.String())
	}
	if strings.Count(refresh.Body.String(), `"role":"primary"`) != 1 || strings.Count(refresh.Body.String(), `"role":"replica"`) != 2 || strings.Count(refresh.Body.String(), `"source_instance_id"`) != 2 {
		t.Fatalf("refresh did not publish one primary, two replicas, and two links: %s", refresh.Body.String())
	}
	if refresher.callCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refresher.callCount())
	}

	missing := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(model.NewResourceID())+"/discover", nil)
	if missing.Code != http.StatusUnprocessableEntity || refresher.callCount() != 1 {
		t.Fatalf("unknown inventory refresh reached refresher: %d %s calls=%d", missing.Code, missing.Body.String(), refresher.callCount())
	}
	invalid := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/not-a-uuid/discover", nil)
	if invalid.Code != http.StatusBadRequest || refresher.callCount() != 1 {
		t.Fatalf("invalid UUID refresh reached refresher: %d %s calls=%d", invalid.Code, invalid.Body.String(), refresher.callCount())
	}
	password := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(body.Result.Cluster.ResourceID)+"/discover", map[string]string{"password": "api-secret"})
	if password.Code != http.StatusBadRequest || strings.Contains(password.Body.String(), "api-secret") || refresher.callCount() != 1 {
		t.Fatalf("password-bearing refresh was accepted or exposed: %d %s", password.Code, password.Body.String())
	}
}

func TestDeleteClusterRetiresInventoryAndReturnsSummary(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "retire-api"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	response := callJSON(t, server.Handler(), http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), map[string]string{"confirm_display_name": cluster.DisplayName})
	if response.Code != http.StatusOK {
		t.Fatalf("delete cluster status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"display_name":"retire-api"`) || !strings.Contains(response.Body.String(), `"endpoints_removed":1`) {
		t.Fatalf("delete cluster summary=%s", response.Body.String())
	}
	if _, found := repository.Cluster(cluster.ResourceID); found || len(repository.Audits()) != 1 || repository.Audits()[0].Actor != "service-api" {
		t.Fatalf("cluster was not retired with service audit: found=%t audits=%+v", found, repository.Audits())
	}
}

func TestDeleteClusterValidatesConfirmationAndConflicts(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "retire-guard"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	for _, body := range []interface{}{nil, map[string]string{"confirm_display_name": "wrong"}, map[string]string{"confirm_display_name": cluster.DisplayName, "unexpected": "field"}} {
		response := callJSON(t, server.Handler(), http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid delete body=%+v status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	if _, err := repository.PutLifecycleTask(lifecycle.Task{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Status: lifecycle.TaskRunning}); err != nil {
		t.Fatalf("put lifecycle task: %v", err)
	}
	blocked := callJSON(t, server.Handler(), http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), map[string]string{"confirm_display_name": cluster.DisplayName})
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "active work") {
		t.Fatalf("active work delete status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if _, found := repository.Cluster(cluster.ResourceID); !found {
		t.Fatal("blocked delete removed the cluster")
	}
	missing := callJSON(t, server.Handler(), http.MethodDelete, "/api/v1/clusters/"+string(model.NewResourceID()), map[string]string{"confirm_display_name": "missing"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing delete status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestDeleteClusterRequiresMutationLeader(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "leader-only-retirement"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	authority := &apiMutationAuthorityStub{err: errors.New("not leader"), leaderID: model.NewResourceID(), leaderAddress: "192.0.2.10:10009"}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{}, WithMutationAuthority(authority))
	response := callJSON(t, server.Handler(), http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), map[string]string{"confirm_display_name": cluster.DisplayName})
	if response.Code != http.StatusServiceUnavailable || authority.calls != 1 {
		t.Fatalf("follower delete status=%d body=%s calls=%d", response.Code, response.Body.String(), authority.calls)
	}
	if _, found := repository.Cluster(cluster.ResourceID); !found {
		t.Fatal("follower delete changed inventory")
	}
}

func TestRegistrationPostCommitWarningReturnsCommittedUUIDs(t *testing.T) {
	recorder := httptest.NewRecorder()
	cluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "committed"}
	endpoints := []model.Endpoint{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}}
	err := fmt.Errorf("%w: secret filesystem detail", store.ErrPostCommitDurability)
	if !writeClusterRegistrationFailure(recorder, cluster, endpoints, err) {
		t.Fatal("post-commit registration warning was not handled")
	}
	body := recorder.Body.String()
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(body, string(cluster.ResourceID)) || !strings.Contains(body, string(endpoints[0].ResourceID)) || !strings.Contains(body, "committed with durability warning") {
		t.Fatalf("post-commit registration response = %d %s", recorder.Code, body)
	}
	if strings.Contains(body, "secret filesystem detail") {
		t.Fatalf("registration response leaked persistence details: %s", body)
	}
}

func TestDiscoveryPostCommitWarningReturnsPublishedObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "discovery-warning"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot := model.TopologySnapshot{ClusterID: cluster.ResourceID, ObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return snapshot, fmt.Errorf("%w: secret discovery path", store.ErrPostCommitDurability)
	}}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", nil)
	body := response.Body.String()
	if response.Code != http.StatusInternalServerError || !strings.Contains(body, string(cluster.ResourceID)) || !strings.Contains(body, observedAt.Format(time.RFC3339Nano)) || !strings.Contains(body, "published with durability warning") {
		t.Fatalf("post-commit discovery response = %d %s", response.Code, body)
	}
	if strings.Contains(body, "secret discovery path") {
		t.Fatalf("discovery response leaked persistence details: %s", body)
	}
}

func TestClusterDetailExposesRegisteredEndpointsForConditionalVIPDisplay(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, nil, nil)
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "orders",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	activeVIP, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Port: 3306, Active: true,
	})
	if err != nil {
		t.Fatalf("create active VIP endpoint: %v", err)
	}
	retiredVIP, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.101", Port: 3306, Active: false,
	})
	if err != nil {
		t.Fatalf("create retired VIP endpoint: %v", err)
	}

	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("cluster detail status: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Result struct {
			Cluster   model.DatabaseCluster    `json:"cluster"`
			Instances []model.DatabaseInstance `json:"instances"`
			Endpoints []model.Endpoint         `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cluster detail: %v", err)
	}
	if body.Result.Cluster.ResourceID != cluster.ResourceID || len(body.Result.Endpoints) != 3 {
		t.Fatalf("cluster detail missing endpoint inventory: %+v", body.Result)
	}
	endpointByID := make(map[model.ResourceID]model.Endpoint, len(body.Result.Endpoints))
	for _, endpoint := range body.Result.Endpoints {
		endpointByID[endpoint.ResourceID] = endpoint
	}
	if endpointByID[activeVIP.ResourceID].Kind != model.EndpointVIP || !endpointByID[activeVIP.ResourceID].Active {
		t.Fatalf("active VIP endpoint not exposed: %+v", endpointByID[activeVIP.ResourceID])
	}
	if endpointByID[retiredVIP.ResourceID].Kind != model.EndpointVIP || endpointByID[retiredVIP.ResourceID].Active {
		t.Fatalf("retired VIP endpoint contract changed: %+v", endpointByID[retiredVIP.ResourceID])
	}
}

func TestDiscoverBodyIsBoundedAndStrictIndependentOfContentLength(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "strict-discovery-body",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	refresher := &fakeRefresher{}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/discover"
	tests := []struct {
		name             string
		body             string
		forceZeroLength  bool
		transferEncoding []string
		wantStatus       int
	}{
		{name: "empty body", wantStatus: http.StatusOK},
		{name: "one empty object", body: `{}`, wantStatus: http.StatusOK},
		{name: "unknown credential field", body: `{"credentials":{"password":"secret"}}`, wantStatus: http.StatusBadRequest},
		{name: "zero content length with credential bytes", body: `{"password":"secret"}`, forceZeroLength: true, wantStatus: http.StatusBadRequest},
		{name: "chunked credential body", body: `{"password":"secret"}`, transferEncoding: []string{"chunked"}, wantStatus: http.StatusBadRequest},
		{name: "trailing JSON value", body: `{} {}`, wantStatus: http.StatusBadRequest},
		{name: "partial trailing JSON value", body: `{} {`, wantStatus: http.StatusBadRequest},
		{name: "non-object null", body: `null`, wantStatus: http.StatusBadRequest},
		{name: "oversized body", body: strings.Repeat(" ", 2048) + `{}`, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			if test.forceZeroLength {
				request.ContentLength = 0
			}
			if test.transferEncoding != nil {
				request.ContentLength = -1
				request.TransferEncoding = test.transferEncoding
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
	if refresher.callCount() != 2 {
		t.Fatalf("malformed discovery bodies reached refresher: calls=%d", refresher.callCount())
	}
}

func TestGenericJSONDecodeRejectsTrailingValues(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	valid := `{"display_name":"payments","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`
	for _, trailing := range []string{" {}", " {"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(valid+trailing))
		request.Header.Set("Authorization", "Bearer "+testControlToken)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("trailing %q status = %d: %s", trailing, response.Code, response.Body.String())
		}
	}
	if len(repository.Clusters()) != 0 {
		t.Fatalf("trailing JSON registration changed inventory: %+v", repository.Clusters())
	}
}

func TestRegisterClusterRejectsBlankDuplicateNameAndGlobalEndpointWithoutPartialState(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	blank := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "   ", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "blank", "port": 3306}},
	})
	if blank.Code != http.StatusBadRequest || len(repository.Clusters()) != 0 {
		t.Fatalf("blank registration changed inventory: %d %s", blank.Code, blank.Body.String())
	}

	firstPayload := map[string]interface{}{
		"display_name": "Payments", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-a", "ip_address": "192.0.2.10", "port": 3306}},
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", firstPayload); response.Code != http.StatusCreated {
		t.Fatalf("first registration: %d %s", response.Code, response.Body.String())
	}
	duplicateName := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": " payments ", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-b", "port": 3306}},
	})
	if duplicateName.Code != http.StatusConflict {
		t.Fatalf("duplicate name status: %d %s", duplicateName.Code, duplicateName.Body.String())
	}
	duplicateEndpoint := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "reporting", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-c", "ip_address": "192.0.2.10", "port": 3306}},
	})
	if duplicateEndpoint.Code != http.StatusConflict {
		t.Fatalf("duplicate endpoint status: %d %s", duplicateEndpoint.Code, duplicateEndpoint.Body.String())
	}
	if len(repository.Clusters()) != 1 || len(repository.Endpoints(repository.Clusters()[0].ResourceID)) != 1 {
		t.Fatalf("failed registration published partial inventory: clusters=%+v", repository.Clusters())
	}
}

func TestRegisterClusterMapsTypedStoreErrorsWithoutLeakingInternals(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	invalid := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "invalid-port", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-a", "port": 0}},
	})
	if invalid.Code != http.StatusBadRequest || len(repository.Clusters()) != 0 {
		t.Fatalf("validation mapping = %d %s clusters=%+v", invalid.Code, invalid.Body.String(), repository.Clusters())
	}
	valid := map[string]interface{}{
		"display_name": "payments", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}},
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", valid); response.Code != http.StatusCreated {
		t.Fatalf("seed registration: %d %s", response.Code, response.Body.String())
	}
	conflict := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": " PAYMENTS ", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-b", "port": 3306}},
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict mapping = %d %s", conflict.Code, conflict.Body.String())
	}

	internalPath := filepath.Join(t.TempDir(), "metadata.json")
	failing, err := store.Open(internalPath)
	if err != nil {
		t.Fatalf("open failing repository: %v", err)
	}
	if err := os.Mkdir(internalPath, 0700); err != nil {
		t.Fatalf("create blocking snapshot directory: %v", err)
	}
	failingServer := newAPIServer(t, failing, newCandidateAdapterSpy(), &fakeRefresher{})
	internal := callJSON(t, failingServer.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "internal-failure", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-c", "port": 3306}},
	})
	if internal.Code != http.StatusInternalServerError || strings.Contains(internal.Body.String(), internalPath) || len(failing.Clusters()) != 0 {
		t.Fatalf("internal mapping leaked or published state: %d %s clusters=%+v", internal.Code, internal.Body.String(), failing.Clusters())
	}
}

func TestClusterAPIErrorsDoNotExposeDatabaseOrCredentialDetails(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "sanitized-errors",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return model.TopologySnapshot{}, errors.New("dial failed with password db-secret")
	}}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", nil)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "db-secret") || strings.Contains(response.Body.String(), "dial failed") {
		t.Fatalf("refresh error exposed database details: %d %s", response.Code, response.Body.String())
	}

	candidateRepository := store.NewMemory()
	candidateCluster, _ := seedCandidateTopology(t, candidateRepository, 1, false)
	candidate := newCandidateAdapterSpy()
	candidate.evaluationError = errors.New("candidate SQL failed with credential candidate-secret")
	candidateServer := newAPIServer(t, candidateRepository, candidate, &fakeRefresher{})
	response = callJSON(t, candidateServer.Handler(), http.MethodGet, "/api/v1/clusters/"+string(candidateCluster.ResourceID)+"/candidates", nil)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "candidate-secret") || strings.Contains(response.Body.String(), "SQL failed") {
		t.Fatalf("candidate error exposed adapter details: %d %s", response.Code, response.Body.String())
	}
}

func TestDiscoverMapsStaleObservationToSanitizedConflict(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "stale-api",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return model.TopologySnapshot{}, fmt.Errorf("internal path detail: %w", store.ErrStaleObservation)
	}}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", map[string]interface{}{})
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "internal path detail") || !strings.Contains(response.Body.String(), "stale") {
		t.Fatalf("stale mapping = %d %s", response.Code, response.Body.String())
	}
}

func TestDiscoverMapsInventoryGenerationChangeToSanitizedConflict(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "inventory-conflict-api"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return model.TopologySnapshot{}, fmt.Errorf("old address detail: %w", store.ErrInventoryChanged)
	}}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), refresher)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", map[string]interface{}{})
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "old address detail") || !strings.Contains(response.Body.String(), "inventory") {
		t.Fatalf("inventory conflict mapping = %d %s", response.Code, response.Body.String())
	}
}

func seedCandidateTopology(t *testing.T, repository *store.Repository, primaryCount int, failedReplica bool) (model.DatabaseCluster, model.TopologySnapshot) {
	t.Helper()
	endpointCount := primaryCount + 1
	if endpointCount < 2 {
		endpointCount = 2
	}
	requestedEndpoints := make([]model.Endpoint, endpointCount)
	for index := range requestedEndpoints {
		requestedEndpoints[index] = model.Endpoint{Kind: model.EndpointDatabase, Hostname: "mysql-" + string(rune('a'+index)), Port: 3306 + index, Active: true}
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "candidate-cluster"}, requestedEndpoints)
	if err != nil {
		t.Fatalf("create candidate inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	observations := make([]store.DiscoveryObservation, 0, endpointCount)
	probes := make([]model.ProbeStatus, 0, endpointCount)
	for index, endpoint := range endpoints {
		role := model.RoleReplica
		if index < primaryCount {
			role = model.RolePrimary
		}
		instance := model.DatabaseInstance{
			Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-" + string(rune('a'+index))},
			Hostname: endpoint.Hostname, Port: endpoint.Port, Role: role, PromotionEligible: role == model.RoleReplica,
			Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
		}
		observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
		health := model.Health{State: model.HealthHealthy, ObservedAt: observedAt}
		if failedReplica && role == model.RoleReplica {
			health = model.Health{State: model.HealthUnknown, ObservedAt: observedAt}
		}
		probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, DiscoveryObservedAt: observedAt, Health: health})
	}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), Observations: observations, Probes: probes,
		Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt}, ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatalf("seed candidate topology: %v", err)
	}
	return cluster, snapshot
}

func TestCandidateReadUsesPersistedProbesAndBoundedPolicyWithoutDatabaseProbes(t *testing.T) {
	repository := store.NewMemory()
	cluster, snapshot := seedCandidateTopology(t, repository, 1, true)
	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})

	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("candidate status: %d %s", response.Code, response.Body.String())
	}
	requests, databaseCalls := candidate.captured()
	if len(requests) != 1 || databaseCalls != 0 {
		t.Fatalf("candidate read invoked wrong adapter paths: requests=%d database_calls=%d", len(requests), databaseCalls)
	}
	if requests[0].Primary.ResourceID == "" || requests[0].Primary.Role != model.RolePrimary || !reflect.DeepEqual(requests[0].Probes, snapshot.Probes) || !requests[0].ObservedAt.Equal(snapshot.ObservedAt) {
		t.Fatalf("candidate request omitted authoritative primary or probes: %+v", requests[0])
	}
	if requests[0].Policy.MaximumLagSeconds != 10 || !requests[0].Policy.RequireGTID {
		t.Fatalf("unexpected default candidate policy: %+v", requests[0].Policy)
	}

	override := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates?maximum_lag_seconds=30&require_gtid=false", nil)
	if override.Code != http.StatusOK {
		t.Fatalf("candidate policy override: %d %s", override.Code, override.Body.String())
	}
	requests, _ = candidate.captured()
	if len(requests) != 2 || requests[1].Policy.MaximumLagSeconds != 30 || requests[1].Policy.RequireGTID {
		t.Fatalf("bounded policy override was not passed: %+v", requests)
	}
}

func TestSnapshotDerivedRoutesRequireTheRequestedObservation(t *testing.T) {
	repository := store.NewMemory()
	cluster, snapshot := seedCandidateTopology(t, repository, 1, false)
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	base := "/api/v1/clusters/" + string(cluster.ResourceID)

	for _, route := range []string{"/health", "/candidates", "/metrics"} {
		current := callJSON(t, server.Handler(), http.MethodGet, base+route+"?observation_id="+url.QueryEscape(snapshot.ObservedAt.Format(time.RFC3339Nano)), nil)
		if current.Code != http.StatusOK {
			t.Fatalf("current observation %s: %d %s", route, current.Code, current.Body.String())
		}
		stale := callJSON(t, server.Handler(), http.MethodGet, base+route+"?observation_id="+url.QueryEscape(snapshot.ObservedAt.Add(-time.Minute).Format(time.RFC3339Nano)), nil)
		if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "observation changed") {
			t.Fatalf("stale observation %s: %d %s", route, stale.Code, stale.Body.String())
		}
	}
}

func TestRealMySQLDiscoveryProducesEligibleReadOnlyCandidate(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "real-adapter-candidate",
	}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	registry := adapter.NewRegistry()
	mysqlAdapter := mysql.New(realMySQLCandidateRunner{})
	if err := registry.Register(mysqlAdapter); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	refresher := discovery.New(registry, repository, discovery.CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "monitor", Password: "secret"}, nil
	}), func() time.Time { return time.Date(2026, time.July, 11, 18, 0, 0, 0, time.UTC) })
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository)
	server := NewServer(registry, repository, service, refresher, WithControlToken(testControlToken))
	refresh := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", map[string]interface{}{})
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status: %d %s", refresh.Code, refresh.Body.String())
	}
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("candidate status: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Result []model.CandidateAssessment `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(body.Result) != 2 {
		t.Fatalf("candidate assessments: %+v", body.Result)
	}
	var eligible model.CandidateAssessment
	for _, assessment := range body.Result {
		if assessment.Eligible {
			eligible = assessment
		}
	}
	if eligible.Rank != 1 || assessmentCheck(eligible.Checks, "promotion_eligibility") != model.CheckPass || assessmentCheck(eligible.Checks, "replica_read_only") != model.CheckPass {
		t.Fatalf("real discovered replica was not ranked safely: %+v", body.Result)
	}
}

func assessmentCheck(checks []model.Check, name string) model.CheckStatus {
	for _, check := range checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

func TestCandidateReadFailsClosedBeforeAdapterInvocation(t *testing.T) {
	for _, primaryCount := range []int{0, 2} {
		repository := store.NewMemory()
		cluster, _ := seedCandidateTopology(t, repository, primaryCount, false)
		candidate := newCandidateAdapterSpy()
		server := newAPIServer(t, repository, candidate, &fakeRefresher{})
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
		requests, _ := candidate.captured()
		if response.Code != http.StatusConflict || len(requests) != 0 {
			t.Fatalf("primary count %d did not fail closed: %d %s calls=%d", primaryCount, response.Code, response.Body.String(), len(requests))
		}
	}

	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "no-observation"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create unobserved cluster: %v", err)
	}
	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
	requests, _ := candidate.captured()
	if response.Code != http.StatusConflict || len(requests) != 0 {
		t.Fatalf("candidate read without persisted probe evidence did not fail closed: %d %s", response.Code, response.Body.String())
	}
}

func TestCandidateReadRejectsRetainedPrimaryWithoutCurrentRoleEvidence(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "stale-primary",
	}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3307, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)
	primary := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-a"},
		Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}
	replica := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-b"},
		Hostname: "mysql-b", Port: 3307, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{SourceIdentity: model.EngineIdentity{"server_uuid": "native-a"}, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning},
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
		},
		ObservedAt: observedAt,
	}); err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	secondObservedAt := observedAt.Add(time.Minute)
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations:        []store.DiscoveryObservation{{EndpointID: endpoints[1].ResourceID, Instance: replica}},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthUnknown}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: secondObservedAt, Health: model.Health{State: model.HealthHealthy}},
		},
		ObservedAt: secondObservedAt,
	})
	if err != nil {
		t.Fatalf("publish failed-primary observation: %v", err)
	}
	if len(snapshot.Instances) != 2 || snapshot.Instances[0].Role != model.RolePrimary && snapshot.Instances[1].Role != model.RolePrimary {
		t.Fatalf("last-known primary was not retained in topology: %+v", snapshot.Instances)
	}

	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
	requests, _ := candidate.captured()
	if response.Code != http.StatusConflict || len(requests) != 0 {
		t.Fatalf("stale primary role reached candidate adapter: %d %s calls=%d", response.Code, response.Body.String(), len(requests))
	}
}

func TestTopologyAndCandidateReadsOverlayOnlyCanonicalMetadataCoordinatesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "metadata-overlay-api",
	}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-replica", IPAddress: "192.0.2.11", Port: 3307, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 19, 0, 0, 0, time.UTC)
	primary := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-primary"},
		DisplayName: "mysql-old", Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306,
		Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: false,
	}
	replica := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-replica"},
		DisplayName: "mysql-replica", Hostname: "mysql-replica", IPAddress: "192.0.2.11", Port: 3307,
		Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
		Replication: model.ReplicationStatus{SourceIdentity: model.EngineIdentity{"server_uuid": "native-primary"}, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning},
	}
	initial, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
		},
		ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatalf("seed observed topology: %v", err)
	}
	primaryID := model.ResourceID("")
	for _, instance := range initial.Instances {
		if instance.Role == model.RolePrimary {
			primaryID = instance.ResourceID
		}
	}
	metadata := primary
	metadata.ResourceID = primaryID
	metadata.ClusterID = cluster.ResourceID
	metadata.DisplayName = "mysql-renamed"
	metadata.Hostname = "mysql-renamed"
	metadata.IPAddress = "192.0.2.99"
	metadata.Port = 4406
	metadata.Role = model.RoleReplica
	metadata.Health = model.Health{State: model.HealthUnhealthy}
	metadata.Replication = model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadStopped}
	metadata.PromotionEligible = true
	if _, err := repository.ReconcileInstance(metadata); err != nil {
		t.Fatalf("reconcile metadata payload: %v", err)
	}

	assertReads := func(t *testing.T, repository *store.Repository) {
		t.Helper()
		candidate := newCandidateAdapterSpy()
		server := newAPIServer(t, repository, candidate, &fakeRefresher{})
		topologyResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/topology", nil)
		if topologyResponse.Code != http.StatusOK {
			t.Fatalf("topology status: %d %s", topologyResponse.Code, topologyResponse.Body.String())
		}
		var body struct {
			Result model.TopologySnapshot `json:"result"`
		}
		if err := json.Unmarshal(topologyResponse.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode topology: %v", err)
		}
		var readPrimary model.DatabaseInstance
		for _, instance := range body.Result.Instances {
			if instance.ResourceID == primaryID {
				readPrimary = instance
			}
		}
		if readPrimary.Hostname != metadata.Hostname || readPrimary.IPAddress != metadata.IPAddress || readPrimary.Port != metadata.Port || readPrimary.DisplayName != metadata.DisplayName {
			t.Fatalf("topology did not overlay coordinates: %+v", readPrimary)
		}
		if readPrimary.Role != model.RolePrimary || readPrimary.Health.State != model.HealthHealthy || readPrimary.Replication.IOThread != primary.Replication.IOThread || readPrimary.PromotionEligible != primary.PromotionEligible || readPrimary.EngineIdentity["server_uuid"] != "native-primary" {
			t.Fatalf("metadata payload replaced observed runtime facts: %+v", readPrimary)
		}
		candidateResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates", nil)
		requests, _ := candidate.captured()
		if candidateResponse.Code != http.StatusOK || len(requests) != 1 || requests[0].Primary.ResourceID != primaryID || requests[0].Primary.Role != model.RolePrimary || requests[0].Primary.Health.State != model.HealthHealthy {
			t.Fatalf("candidate read used metadata runtime payload: %d %s requests=%+v", candidateResponse.Code, candidateResponse.Body.String(), requests)
		}
	}
	assertReads(t, repository)
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	assertReads(t, reopened)
}

func TestInvalidCandidatePolicyAndClusterUUIDFailBeforeDependencies(t *testing.T) {
	repository := store.NewMemory()
	cluster, _ := seedCandidateTopology(t, repository, 1, false)
	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	for _, query := range []string{
		"maximum_lag_seconds=-1", "maximum_lag_seconds=86401", "maximum_lag_seconds=nan", "maximum_lag_seconds=",
		"require_gtid=maybe", "require_gtid=1", "require_gtid=",
	} {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/candidates?"+query, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid policy %q status: %d %s", query, response.Code, response.Body.String())
		}
	}
	requests, _ := candidate.captured()
	if len(requests) != 0 {
		t.Fatalf("invalid policy invoked candidate adapter %d times", len(requests))
	}

	invalidServer := NewServer(adapter.NewRegistry(), nil, nil, &fakeRefresher{})
	for _, suffix := range []string{"topology", "health", "candidates", "metrics", "metrics/prometheus"} {
		response := callJSON(t, invalidServer.Handler(), http.MethodGet, "/api/v1/clusters/not-a-uuid/"+suffix, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid UUID %s status: %d %s", suffix, response.Code, response.Body.String())
		}
	}
}

func TestClusterReadRoutesDistinguishUnknownFromUnobservedInventory(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	unknownID := model.NewResourceID()
	paths := []string{"topology", "health", "candidates", "metrics", "metrics/prometheus"}
	for _, suffix := range paths {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(unknownID)+"/"+suffix, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unknown cluster %s status = %d: %s", suffix, response.Code, response.Body.String())
		}
	}

	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "unobserved-routes",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create unobserved inventory: %v", err)
	}
	for _, suffix := range paths {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/"+suffix, nil)
		if response.Code != http.StatusConflict {
			t.Fatalf("unobserved cluster %s status = %d: %s", suffix, response.Code, response.Body.String())
		}
	}
}
