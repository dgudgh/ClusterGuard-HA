package api

import (
	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/discovery"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExistingDatabaseEndpointRegistration(t *testing.T) {
	server, repo := newTestServer(t)
	cluster := registerTestCluster(t, server, "existing")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/database-endpoints"
	input := map[string]interface{}{"endpoints": []map[string]interface{}{{"hostname": "cloud-mysql", "ip_address": "192.0.2.40", "port": 3306}}}
	// Inventory is not writable without authentication.
	req := httptest.NewRequest(http.MethodPost, path, nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", w.Code)
	}
	for i := 0; i < 2; i++ {
		response := callJSON(t, server.Handler(), http.MethodPost, path, input)
		if response.Code != http.StatusOK {
			t.Fatalf("registration: %d %s", response.Code, response.Body.String())
		}
	}
	if len(repo.Endpoints(cluster.ResourceID)) != 3 || len(repo.Instances(cluster.ResourceID)) != 0 {
		t.Fatal("registration did not preserve inventory/observation distinction")
	}
	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID), nil)
	var body struct {
		Result struct {
			Endpoints []model.Endpoint `json:"endpoints"`
		} `json:"result"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil || len(body.Result.Endpoints) != 3 {
		t.Fatalf("endpoint not visible: %s", detail.Body.String())
	}
	for _, bad := range []interface{}{map[string]interface{}{"endpoints": []map[string]interface{}{{"ip_address": "192.0.2.40", "port": 0}}}, map[string]interface{}{"endpoints": []map[string]interface{}{{"hostname": "http://user:password@cloud", "port": 3306}}}, map[string]interface{}{"endpoints": []map[string]interface{}{{"ip_address": "192.0.2.41", "port": 3306, "password": "forbidden"}}}} {
		response := callJSON(t, server.Handler(), http.MethodPost, path, bad)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad request: %d %s", response.Code, response.Body.String())
		}
	}
	if len(repo.Endpoints(cluster.ResourceID)) != 3 {
		t.Fatal("invalid requests mutated inventory")
	}
}

func TestExistingEndpointDiscoveryPublishesReplica(t *testing.T) {
	repository := store.NewMemory()
	registry := adapter.NewRegistry()
	candidate := mysql.New(realMySQLCandidateRunner{})
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	refresher := discovery.New(registry, repository, discovery.CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "test"}, nil
	}), nil)
	server := newAPIServer(t, repository, candidate, refresher)
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "hybrid"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/clusters/" + string(cluster.ResourceID)
	for _, stage := range []string{"before", "after"} {
		if stage == "after" {
			response := callJSON(t, server.Handler(), http.MethodPost, base+"/database-endpoints", map[string]interface{}{"endpoints": []map[string]interface{}{{"hostname": "mysql-b", "port": 3306}}})
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			if _, exists := repository.TopologySnapshot(cluster.ResourceID); exists {
				t.Fatal("stale topology survived inventory change")
			}
		}
		response := callJSON(t, server.Handler(), http.MethodPost, base+"/discover", map[string]interface{}{})
		if response.Code != http.StatusOK {
			t.Fatalf("discovery %s: %s", stage, response.Body.String())
		}
	}
	topology, ok := repository.TopologySnapshot(cluster.ResourceID)
	if !ok || len(topology.Instances) != 2 || len(topology.Links) != 1 {
		t.Fatalf("missing discovered replica: %+v", topology)
	}
	if len(repository.Nodes()) != 0 {
		t.Fatal("discovery changed controller membership")
	}
}

func TestExistingEndpointRegistrationAuthorization(t *testing.T) {
	server, repo, _ := newAuthenticationTestServer(t)
	cluster, _, _ := repo.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "secure"}, nil)
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/database-endpoints"
	input := map[string]interface{}{"endpoints": []map[string]interface{}{{"hostname": "cloud", "port": 3306}}}
	admin, _ := repo.PlatformUserByUsername("admin")
	for _, role := range []model.PlatformRole{model.PlatformRoleViewer, model.PlatformRoleOperator, model.PlatformRoleAdmin} {
		user := admin
		user.ResourceID = model.NewResourceID()
		user.Username = string(role) + "test"
		user.Role = role
		user.MustChangePassword = false
		if _, err := repo.CreatePlatformUser(user); err != nil {
			t.Fatal(err)
		}
		client := &authTestClient{handler: server.Handler()}
		if response := client.login(t, user.Username, apiBootstrapPassword); response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		if response := client.request(t, http.MethodPost, path, input, false); response.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF accepted for %s: %d", role, response.Code)
		}
		response := client.request(t, http.MethodPost, path, input, true)
		expected := http.StatusForbidden
		if role == model.PlatformRoleAdmin {
			expected = http.StatusOK
		}
		if response.Code != expected {
			t.Fatalf("role %s: %d %s", role, response.Code, response.Body.String())
		}
	}
	if len(repo.Endpoints(cluster.ResourceID)) != 1 {
		t.Fatal("unauthorized registration mutated inventory")
	}
}
