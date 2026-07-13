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

func seedRebootBootstrapState(t *testing.T, repository *store.Repository, now time.Time) (model.DatabaseCluster, model.DatabaseInstance, endpoint.Lease) {
	t.Helper()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-b", IPAddress: "192.0.2.11", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	lag := int64(0)
	canonicalIdentity := model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: now,
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
				ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: canonicalIdentity.Clone(),
				Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RoleUnknown,
				Health:         model.Health{State: model.HealthDegraded, Summary: "MySQL instance is read-only with no replication source"},
				EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
			}},
			{EndpointID: endpoints[1].ResourceID, Instance: model.DatabaseInstance{
				ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"},
				Hostname: "mysql-b", IPAddress: "192.0.2.11", Port: 3306, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
				Replication:    model.ReplicationStatus{SourceIdentity: canonicalIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag},
				EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
			}},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: now, Health: model.Health{State: model.HealthDegraded}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: now, Health: model.Health{State: model.HealthHealthy}},
		},
		Health: model.Health{State: model.HealthDegraded},
	})
	if err != nil {
		t.Fatal(err)
	}
	var canonical model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.EngineIdentity["server_uuid"] == canonicalIdentity["server_uuid"] {
			canonical = instance
		}
	}
	if !model.ValidResourceID(canonical.ResourceID) {
		t.Fatal("canonical reboot instance was not discovered")
	}
	resource, _, err := repository.PutHAEndpoint(store.HAEndpointSpec{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24,
		OwnerID: canonical.ResourceID, Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitHAEndpointOwner(cluster.ResourceID, resource.ResourceID, canonical.ResourceID, false); err != nil {
		t.Fatal(err)
	}
	lease := endpoint.Lease{
		ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, HAEndpointID: resource.ResourceID,
		OperationID: resource.ResourceID, OwnerID: canonical.ResourceID, ExpiresAt: now.Add(30 * time.Second), Active: true,
	}
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return cluster, canonical, lease
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

func TestAgentReconcileReturnsSignedBootstrapDecisionForVerifiedRebootedPrimary(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, canonical, lease := seedRebootBootstrapState(t, repository, now)
	leaderID := model.NewResourceID()
	authority := &apiMutationAuthorityStub{leaderID: leaderID, leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: canonical.ResourceID, RequestedAt: now, Nonce: "0123456789abcdef"}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response := callAgentReconcile(t, server, request)
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap decision status=%d body=%s", response.Code, response.Body.String())
	}
	var decision agent.ReconcileResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Action != agent.ReconcileBootstrapPrimary || decision.LeaseID != lease.ResourceID || decision.ControllerID != leaderID {
		t.Fatalf("bootstrap decision=%+v", decision)
	}
	if err := agent.VerifyReconcileResponse(decision, request, "agent-secret", now); err != nil {
		t.Fatalf("verify bootstrap decision: %v", err)
	}
}

func TestAgentReconcileBootstrapsVerifiedOwnerWhenLeaseHealthIsMarkedHealthy(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, canonical, _ := seedRebootBootstrapState(t, repository, now)
	resources := repository.HAEndpoints(cluster.ResourceID)
	if len(resources) != 1 {
		t.Fatalf("HA endpoint count=%d", len(resources))
	}
	if err := repository.CommitHAEndpointOwner(cluster.ResourceID, resources[0].ResourceID, canonical.ResourceID, true); err != nil {
		t.Fatal(err)
	}
	authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: canonical.ResourceID, RequestedAt: now, Nonce: "0123456789abcdef"}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response := callAgentReconcile(t, server, request)
	var decision agent.ReconcileResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil {
		t.Fatalf("bootstrap decision status=%d body=%s", response.Code, response.Body.String())
	}
	if decision.Action != agent.ReconcileBootstrapPrimary {
		t.Fatalf("healthy lease marker blocked verified reboot bootstrap: %+v", decision)
	}
}

func TestAgentReconcileRejectsRebootBootstrapWithLeaseOlderThanTopology(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, canonical, lease := seedRebootBootstrapState(t, repository, now)
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{
		Lease: lease, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: canonical.ResourceID, RequestedAt: now, Nonce: "0123456789abcdef"}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response := callAgentReconcile(t, server, request)
	var decision agent.ReconcileResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil {
		t.Fatalf("stale lease decision status=%d body=%s", response.Code, response.Body.String())
	}
	if decision.Action != agent.ReconcileSelfIsolate {
		t.Fatalf("stale pre-reboot lease authorized action=%+v", decision)
	}
}
