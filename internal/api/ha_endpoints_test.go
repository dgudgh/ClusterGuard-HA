package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestClusterHAEndpointAPIStoresAndReadsVIP(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repository.ReconcileInstance(model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "mysql-a-uuid"}, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{})
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/ha-endpoints"
	created := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{
		"kind": "vip", "ip_address": "192.0.2.100", "interface": "ens160", "prefix": 24,
		"owner_id": owner.Instance.ResourceID, "active": true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	read := callJSON(t, server.Handler(), http.MethodGet, path, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	var body struct {
		Result []struct {
			Resource model.HAEndpoint `json:"resource"`
			Endpoint model.Endpoint   `json:"endpoint"`
		} `json:"result"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Result) != 1 || body.Result[0].Resource.OwnerID != owner.Instance.ResourceID || body.Result[0].Endpoint.IPAddress != "192.0.2.100" {
		t.Fatalf("unexpected HA endpoints: %+v", body.Result)
	}
}

func TestClusterHAEndpointAPIRejectsUnknownFields(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{})
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/ha-endpoints"
	response := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{
		"kind": "vip", "ip_address": "192.0.2.100", "interface": "ens160", "prefix": 24,
		"owner_id": model.NewResourceID(), "active": true, "password": "must-not-be-accepted",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}
