package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
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

func TestClusterHAEndpointAPIStoresKubernetesService(t *testing.T) {
	repository := store.NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders-k8s"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a.database.svc", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repository.ReconcileInstance(model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "mysql-a-k8s-uuid"}, Hostname: "mysql-a.database.svc", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{})
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/ha-endpoints"
	created := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{
		"kind": "service", "provider": "kubernetes_service", "provider_ref": "database/mysql-writer/mysql-writer-clusterguard",
		"hostname": "mysql-writer.database.svc", "port": 3306, "owner_id": owner.Instance.ResourceID, "active": true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create Kubernetes Service endpoint status=%d body=%s", created.Code, created.Body.String())
	}
	var body struct {
		Result struct {
			Resource model.HAEndpoint `json:"resource"`
			Endpoint model.Endpoint   `json:"endpoint"`
		} `json:"result"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Result.Resource.Provider != model.EndpointProviderKubernetesService || body.Result.Endpoint.Hostname != "mysql-writer.database.svc" || body.Result.Endpoint.Port != 3306 {
		t.Fatalf("Kubernetes Service endpoint=%+v", body.Result)
	}
}

func TestClusterHAOwnershipAPIExposesOnlyActiveVIPLease(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, primary, resource, lease := seedAgentReconcileState(t, repository, now)
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{})
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/ha-ownership"

	response := callJSON(t, server.Handler(), http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("ownership status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Result struct {
			Resource    model.HAEndpoint `json:"resource"`
			Endpoint    model.Endpoint   `json:"endpoint"`
			ActiveLease endpoint.Lease   `json:"active_lease"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Result.Resource.ResourceID != resource.ResourceID || body.Result.Endpoint.InstanceID != primary.ResourceID || body.Result.ActiveLease.ResourceID != lease.ResourceID {
		t.Fatalf("unexpected ownership result: %+v", body.Result)
	}
	if !strings.Contains(response.Body.String(), `"operation_id"`) || strings.Contains(response.Body.String(), `"OperationID"`) {
		t.Fatalf("ownership lease API must expose stable snake_case fields: %s", response.Body.String())
	}

	lease.Active = false
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response = callJSON(t, server.Handler(), http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("inactive ownership status=%d body=%s", response.Code, response.Body.String())
	}
	var inactive struct {
		Result struct {
			ActiveLease *endpoint.Lease `json:"active_lease"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &inactive); err != nil {
		t.Fatal(err)
	}
	if inactive.Result.ActiveLease != nil {
		t.Fatalf("inactive lease was exposed: %+v", inactive.Result.ActiveLease)
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
