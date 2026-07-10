package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type apiRunner struct{}

func (apiRunner) Query(context.Context, adapter.Endpoint, adapter.Credentials, string) (string, error) {
	return "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\tmysql-a\t192.0.2.10\t3306\t1\t8.0.44\t0\t0\n", nil
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
	service := workflow.New(registry, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.TokenApproval{}, repository)
	return NewServer(registry, repository, service), repository
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
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

func TestDiscoveryEndpointReconcilesMySQLInstance(t *testing.T) {
	server, repository := newTestServer(t)
	clusterID := model.NewResourceID()
	payload := map[string]interface{}{
		"engine":      "mysql",
		"cluster_id":  clusterID,
		"endpoint":    map[string]interface{}{"hostname": "mysql-old", "ip_address": "192.0.2.10", "port": 3306},
		"credentials": map[string]string{"username": "monitor", "password": "hidden"},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/discovery", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("discover status: %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(clusterID)
	if len(instances) != 1 || instances[0].EngineIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("discovery was not reconciled: %+v", instances)
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
	clusterID := model.NewResourceID()
	if _, err := repository.ReconcileInstance(model.DatabaseInstance{
		ClusterID: clusterID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName: "mysql-a", Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	for _, path := range []string{"/api/v1/clusters/" + string(clusterID) + "/topology", "/api/v1/clusters/" + string(clusterID) + "/health"} {
		response := callJSON(t, server.Handler(), http.MethodGet, path, nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), string(clusterID)) {
			t.Fatalf("endpoint %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestMetadataExecuteReusesResourceIDForRenamedMySQLEndpoint(t *testing.T) {
	server, repository := newTestServer(t)
	clusterID := model.NewResourceID()
	first, err := repository.ReconcileInstance(model.DatabaseInstance{
		ClusterID: clusterID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName: "mysql-old", Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	payload := map[string]interface{}{
		"operation":      map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"},
		"approval_token": "approved",
		"instance": map[string]interface{}{
			"cluster_id": clusterID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			"display_name": "mysql-new", "hostname": "mysql-new", "ip_address": "192.0.2.20", "port": 3310, "role": "replica", "health": map[string]string{"state": "healthy"},
		},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("metadata execute: %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(clusterID)
	if len(instances) != 1 || instances[0].ResourceID != first.Instance.ResourceID || instances[0].Hostname != "mysql-new" {
		t.Fatalf("metadata execute did not reconcile existing resource: %+v", instances)
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
