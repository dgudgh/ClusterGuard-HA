package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func registerTestCluster(t *testing.T, server *Server, name string) model.DatabaseCluster {
	t.Helper()
	payload := map[string]interface{}{
		"display_name": name,
		"engine":       "mysql",
		"endpoints": []map[string]interface{}{
			{"hostname": "mysql-a", "ip_address": "192.0.2.10", "port": 3306},
			{"hostname": "mysql-b", "ip_address": "192.0.2.11", "port": 3306},
		},
	}
	registered := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", payload)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status: %d %s", registered.Code, registered.Body.String())
	}
	var body struct {
		Result struct {
			Cluster model.DatabaseCluster `json:"cluster"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	return body.Result.Cluster
}

func TestSetClusterRecoveryFreezeFreezesAndUnfreezes(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	cluster := registerTestCluster(t, server, "freeze-api")

	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/recovery-freeze"

	freeze := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{"freeze": true})
	if freeze.Code != http.StatusOK {
		t.Fatalf("freeze status: %d %s", freeze.Code, freeze.Body.String())
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("frozen=%t err=%v", frozen, err)
	}

	unfreeze := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{"freeze": false})
	if unfreeze.Code != http.StatusOK {
		t.Fatalf("unfreeze status: %d %s", unfreeze.Code, unfreeze.Body.String())
	}
	frozen, err = repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("frozen=%t err=%v", frozen, err)
	}
}

func TestSetClusterRecoveryFreezeRejectsUnknownCluster(t *testing.T) {
	server := newAPIServer(t, store.NewMemory(), newCandidateAdapterSpy(), &fakeRefresher{})
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(model.NewResourceID())+"/recovery-freeze", map[string]interface{}{"freeze": true})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
}

func TestSetClusterRecoveryFreezeRequiresBearerToken(t *testing.T) {
	repository := store.NewMemory()
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	cluster := registerTestCluster(t, server, "freeze-secured")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/recovery-freeze", strings.NewReader(`{"freeze":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous freeze did not fail closed: %d %s", response.Code, response.Body.String())
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("anonymous freeze mutated state: frozen=%t err=%v", frozen, err)
	}
}
