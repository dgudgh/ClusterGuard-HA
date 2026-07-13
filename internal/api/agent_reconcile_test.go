package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func seedAgentReconcileState(t *testing.T, repository *store.Repository, now time.Time) (model.DatabaseCluster, model.DatabaseInstance, model.HAEndpoint, endpoint.Lease) {
	t.Helper()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: now,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
			EngineMetadata: map[string]string{"read_only": "false", "super_read_only": "false"},
		}}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: now, Health: model.Health{State: model.HealthHealthy}}},
		Health: model.Health{State: model.HealthHealthy},
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := snapshot.Instances[0]
	resource, _, err := repository.PutHAEndpoint(store.HAEndpointSpec{ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: primary.ResourceID, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitHAEndpointOwner(cluster.ResourceID, resource.ResourceID, primary.ResourceID, true); err != nil {
		t.Fatal(err)
	}
	lease := endpoint.Lease{ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, HAEndpointID: resource.ResourceID, OperationID: resource.ResourceID, OwnerID: primary.ResourceID, ExpiresAt: now.Add(30 * time.Second), Active: true}
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return cluster, primary, resource, lease
}

func callAgentReconcile(t *testing.T, server *Server, request agent.ReconcileRequest) *httptest.ResponseRecorder {
	t.Helper()
	contents, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/reconcile", bytes.NewReader(contents))
	httpRequest.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httpRequest)
	return response
}

func TestAgentReconcileRequiresLeaderQuorumAndReturnsSignedOwnershipDecision(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, primary, _, lease := seedAgentReconcileState(t, repository, now)
	leaderID := model.NewResourceID()
	authority := &apiMutationAuthorityStub{leaderID: leaderID, leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: primary.ResourceID, RequestedAt: now, Nonce: "0123456789abcdef"}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}

	response := callAgentReconcile(t, server, request)
	if response.Code != http.StatusOK {
		t.Fatalf("keep decision status=%d body=%s", response.Code, response.Body.String())
	}
	var keep agent.ReconcileResponse
	if err := json.Unmarshal(response.Body.Bytes(), &keep); err != nil {
		t.Fatal(err)
	}
	if keep.Action != agent.ReconcileKeepVIP || keep.LeaseID != lease.ResourceID || keep.ControllerID != leaderID || agent.VerifyReconcileResponse(keep, request, "agent-secret", now) != nil {
		t.Fatalf("keep decision=%+v", keep)
	}

	lease.OwnerID = model.NewResourceID()
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response = callAgentReconcile(t, server, request)
	var isolate agent.ReconcileResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &isolate) != nil || isolate.Action != agent.ReconcileSelfIsolate || agent.VerifyReconcileResponse(isolate, request, "agent-secret", now) != nil {
		t.Fatalf("isolation decision status=%d response=%+v body=%s", response.Code, isolate, response.Body.String())
	}

	authority.err = errors.New("no quorum")
	response = callAgentReconcile(t, server, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("minority decision status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentReconcileRejectsUnsignedRequestWithoutControlTokenFallback(t *testing.T) {
	repository := store.NewMemory()
	authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID()}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	response := callAgentReconcile(t, server, agent.ReconcileRequest{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), RequestedAt: time.Now().UTC(), Nonce: "0123456789abcdef"})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned reconcile status=%d body=%s", response.Code, response.Body.String())
	}
}
