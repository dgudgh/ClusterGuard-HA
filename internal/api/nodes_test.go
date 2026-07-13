package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestNodeRegistryAPIKeepsFixedNameWhileCoordinatesChange(t *testing.T) {
	server, _ := newTestServer(t)
	created := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", map[string]interface{}{
		"node_name": "cg-data-0001", "display_name": "Database host 1", "hostname": "mysql-old",
		"ip_address": "192.0.2.10", "kind": "data", "active": true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Result model.DatabaseNode `json:"result"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode created node: %v", err)
	}
	if !model.ValidResourceID(envelope.Result.ResourceID) || envelope.Result.NodeName != "cg-data-0001" {
		t.Fatalf("created node=%+v", envelope.Result)
	}

	updated := callJSON(t, server.Handler(), http.MethodPut, "/api/v1/nodes/"+string(envelope.Result.ResourceID), map[string]interface{}{
		"node_name": "cg-data-0001", "display_name": "Database host 1", "hostname": "mysql-new",
		"ip_address": "192.0.2.20", "kind": "data", "active": true,
	})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"mysql-old"`) || !strings.Contains(updated.Body.String(), `"192.0.2.10"`) {
		t.Fatalf("update node: %d %s", updated.Code, updated.Body.String())
	}

	renamed := callJSON(t, server.Handler(), http.MethodPut, "/api/v1/nodes/"+string(envelope.Result.ResourceID), map[string]interface{}{
		"node_name": "cg-data-renamed", "hostname": "mysql-new", "ip_address": "192.0.2.20", "kind": "data", "active": true,
	})
	if renamed.Code != http.StatusConflict {
		t.Fatalf("rename fixed node: %d %s", renamed.Code, renamed.Body.String())
	}

	listed := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/nodes", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"node_name":"cg-data-0001"`) || strings.Contains(listed.Body.String(), "cg-data-renamed") {
		t.Fatalf("list nodes: %d %s", listed.Code, listed.Body.String())
	}
}

func TestNodeRegistryAPIRejectsDuplicateNameAndUnknownFields(t *testing.T) {
	server, _ := newTestServer(t)
	payload := map[string]interface{}{"node_name": "cg-data-0001", "hostname": "mysql-a", "kind": "data", "active": true}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", payload); response.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", response.Code, response.Body.String())
	}
	payload["hostname"] = "mysql-b"
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", payload); response.Code != http.StatusConflict {
		t.Fatalf("duplicate node name: %d %s", response.Code, response.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", map[string]interface{}{
		"node_name": "cg-data-0002", "hostname": "mysql-c", "kind": "data", "active": true, "password": "must-not-be-accepted",
	}); response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "must-not-be-accepted") {
		t.Fatalf("unknown node field: %d %s", response.Code, response.Body.String())
	}
}

func TestNodeRegistryAPIAcceptsPreallocatedIdentityAndRejectsIdentityChange(t *testing.T) {
	server, _ := newTestServer(t)
	resourceID := model.NewResourceID()
	created := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", map[string]interface{}{
		"resource_id": resourceID, "node_name": "cg-node-0001", "hostname": "mysql-a",
		"ip_address": "192.0.2.10", "kind": "mixed", "active": true,
	})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), string(resourceID)) {
		t.Fatalf("create preallocated node: %d %s", created.Code, created.Body.String())
	}
	changed := callJSON(t, server.Handler(), http.MethodPut, "/api/v1/nodes/"+string(resourceID), map[string]interface{}{
		"resource_id": model.NewResourceID(), "node_name": "cg-node-0001", "hostname": "mysql-b",
		"ip_address": "192.0.2.20", "kind": "mixed", "active": true,
	})
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed node identity: %d %s", changed.Code, changed.Body.String())
	}
	invalid := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes", map[string]interface{}{
		"resource_id": "not-a-uuid", "node_name": "cg-node-0002", "hostname": "mysql-c",
		"kind": "data", "active": true,
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid preallocated identity: %d %s", invalid.Code, invalid.Body.String())
	}
}
