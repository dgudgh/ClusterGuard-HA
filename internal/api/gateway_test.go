package api

import (
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

type gatewayInspectorStub struct {
	calls int
	err   error
}

func (i *gatewayInspectorStub) InspectGateway(_ context.Context, id model.ResourceID) (endpoint.GatewayView, error) {
	i.calls++
	return endpoint.GatewayView{ClusterID: id, Ready: true}, i.err
}
func TestGatewayInspectionAPIIsReadOnlyAndPropagatesFailure(t *testing.T) {
	r := store.NewMemory()
	c, _, e := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "hybrid"}, []model.Endpoint{{Kind: model.EndpointDatabase, IPAddress: "192.0.2.1", Port: 3306, Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	inspector := &gatewayInspectorStub{}
	s := newAPIServer(t, r, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithGatewayInspector(inspector))
	path := "/api/v1/clusters/" + string(c.ResourceID) + "/gateway"
	read := callJSON(t, s.Handler(), http.MethodGet, path, nil)
	if read.Code != http.StatusOK || inspector.calls != 1 {
		t.Fatal(read.Code, read.Body.String())
	}
	mutation := callJSON(t, s.Handler(), http.MethodPost, path, map[string]string{"target": "other"})
	if mutation.Code == http.StatusOK || inspector.calls != 1 {
		t.Fatal("inspection accepted a mutation", mutation.Code)
	}
	inspector.err = errors.New("private path and key must not leak")
	read = callJSON(t, s.Handler(), http.MethodGet, path, nil)
	if read.Code != http.StatusServiceUnavailable {
		t.Fatal(read.Code)
	}
	if read.Body.String() == inspector.err.Error() {
		t.Fatal("private error disclosed")
	}
}

func TestGatewayAgentDecisionBindsSignedProviderWithoutHostVIP(t *testing.T) {
	now := time.Now().UTC()
	r := store.NewMemory()
	c, primary, vip, oldLease := seedAgentReconcileState(t, r, now)
	if _, _, e := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Interface: "ens160", Prefix: 24, OwnerID: primary.ResourceID, Active: false}); e != nil {
		t.Fatal(e)
	}
	if e := r.DeleteCoordinationLease(oldLease.ResourceID); e != nil {
		t.Fatal(e)
	}
	resource, _, e := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway, ProviderRef: "site", Hostname: "database.example", Port: 63306, OwnerID: primary.ResourceID, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	oldLease.HAEndpointID = resource.ResourceID
	oldLease.OperationID = resource.ResourceID
	if e = r.PutCoordinationLease(coordination.LeaseRecord{Lease: oldLease, CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "controller:10009"}
	s := newAPIServer(t, r, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{ClusterID: c.ResourceID, InstanceID: primary.ResourceID, RequestedAt: now, Nonce: "gateway-signed-provider"}
	if e = agent.SignReconcileRequest(&request, "agent-secret"); e != nil {
		t.Fatal(e)
	}
	response := callAgentReconcile(t, s, request)
	var decision agent.ReconcileResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if decision.Action != agent.ReconcileKeepVIP || decision.EndpointProvider != model.EndpointProviderGateway || decision.LeaseID != oldLease.ResourceID || agent.VerifyReconcileResponse(decision, request, "agent-secret", now) != nil {
		t.Fatal("gateway majority scope was not signed", decision)
	}
	if _, _, active := s.activeVIP(c.ResourceID); active {
		t.Fatal("gateway still depends on an active local VIP", vip)
	}
}

func TestGatewayDisasterRecoveryRejectsBeforePlanning(t *testing.T) {
	r := store.NewMemory()
	c, primary, _, lease := seedAgentReconcileState(t, r, time.Now().UTC())
	if e := r.DeleteCoordinationLease(lease.ResourceID); e != nil {
		t.Fatal(e)
	}
	if _, _, e := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway, ProviderRef: "site", Hostname: "database.example", Port: 63306, OwnerID: primary.ResourceID, Active: true}); e != nil {
		t.Fatal(e)
	}
	s := newAPIServer(t, r, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithDisasterRecovery(&recoveryManagerStub{}, func(func(context.Context)) {}))
	path := "/api/v1/clusters/" + string(c.ResourceID) + "/recovery/"
	response := callJSON(t, s.Handler(), http.MethodPost, path+"plan", map[string]interface{}{})
	if response.Code != http.StatusNotImplemented || len(r.RecoveryTasks(c.ResourceID)) != 0 {
		t.Fatal("unsupported gateway recovery created a task", response.Code, response.Body.String())
	}
	response = callJSON(t, s.Handler(), http.MethodGet, path+"status", nil)
	var status struct {
		Result struct {
			Available bool `json:"available"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Result.Available {
		t.Fatal("gateway recovery advertised unsupported execution")
	}
}
