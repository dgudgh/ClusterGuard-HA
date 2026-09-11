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
	return seedAgentReconcileEngineState(t, repository, now, model.EngineMySQL)
}

func seedAgentReconcileEngineState(t *testing.T, repository *store.Repository, now time.Time, engine model.Engine) (model.DatabaseCluster, model.DatabaseInstance, model.HAEndpoint, endpoint.Lease) {
	t.Helper()
	identity := model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	var clusterIdentity model.EngineIdentity
	metadata := map[string]string{"read_only": "false", "super_read_only": "false"}
	if engine == model.EnginePostgreSQL {
		identity = model.EngineIdentity{"resource_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "system_identifier": "12345678"}
		clusterIdentity = model.EngineIdentity{"system_identifier": "12345678"}
		metadata = map[string]string{"in_recovery": "false", "transaction_read_only": "false"}
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: engine, DisplayName: "payments"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: now,
		ClusterIdentity: clusterIdentity,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: engine, EngineIdentity: identity,
			Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
			EngineMetadata: metadata,
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

func TestUpgradeMaintenancePreservesSignedLeaseDecisionsButNotMutations(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			now := time.Now().UTC()
			repository := store.NewMemory()
			cluster, primary, _, lease := seedAgentReconcileEngineState(t, repository, now, engine)
			authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "controller:10009"}
			server := newAPIServer(t, repository, adapter.NewUnsupported(engine), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"), WithMutationMaintenance(mutationMaintenanceStub{err: errors.New("upgrade maintenance")}))
			request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: primary.ResourceID, RequestedAt: now, Nonce: "upgrade-maintenance-reconcile"}
			if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
				t.Fatal(err)
			}
			response := callAgentReconcile(t, server, request)
			var decision agent.ReconcileResponse
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileKeepVIP || decision.LeaseID != lease.ResourceID || agent.VerifyReconcileResponse(decision, request, "agent-secret", now) != nil {
				t.Fatalf("upgrade blocked existing majority lease: %d %s", response.Code, response.Body.String())
			}
			if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{"display_name": "blocked", "engine": string(engine)}); response.Code != http.StatusLocked {
				t.Fatalf("maintenance allowed a new mutation: %d", response.Code)
			}
			unsigned := request
			unsigned.Signature = "invalid"
			if response := callAgentReconcile(t, server, unsigned); response.Code != http.StatusUnauthorized {
				t.Fatalf("invalid Agent signature accepted during maintenance: %d", response.Code)
			}
			authority.err = errors.New("no majority")
			if response := callAgentReconcile(t, server, request); response.Code != http.StatusServiceUnavailable {
				t.Fatalf("minority authorization accepted during maintenance: %d", response.Code)
			}
			authority.err = nil
			lease.ExpiresAt = now.Add(-time.Second)
			if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-30 * time.Second)}); err != nil {
				t.Fatal(err)
			}
			response = callAgentReconcile(t, server, request)
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileSelfIsolate {
				t.Fatalf("expired lease was extended by maintenance: %d %s", response.Code, response.Body.String())
			}
		})
	}
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

type agentReconcileMutationRPCStub struct {
	calls         int
	leaderAddress string
	payload       agent.ReconcileRequest
	decodeErr     error
}

func (stub *agentReconcileMutationRPCStub) Forward(writer http.ResponseWriter, request *http.Request, leaderAddress string) error {
	stub.calls++
	stub.leaderAddress = leaderAddress
	stub.decodeErr = json.NewDecoder(request.Body).Decode(&stub.payload)
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]bool{"proxied": true}})
	return nil
}

func TestAgentReconcileForwardsVerifiedBodyToRaftLeader(t *testing.T) {
	repository := store.NewMemory()
	leaderID := model.NewResourceID()
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: leaderID,
		leaderAddress: "controller-a:10009", leaderAPI: "https://controller-a:3000",
	}
	rpc := &agentReconcileMutationRPCStub{}
	server := newAPIServer(
		t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{},
		WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"), WithMutationRPC(rpc),
		WithMutationMaintenance(mutationMaintenanceStub{err: errors.New("upgrade maintenance")}),
	)
	payload := agent.ReconcileRequest{
		ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(),
		RequestedAt: time.Now().UTC(), Nonce: "forward-agent-0001",
	}
	if err := agent.SignReconcileRequest(&payload, "agent-secret"); err != nil {
		t.Fatal(err)
	}

	response := callAgentReconcile(t, server, payload)
	if response.Code != http.StatusOK {
		t.Fatalf("forwarded reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	if rpc.calls != 1 || rpc.leaderAddress != "https://controller-a:3000" {
		t.Fatalf("forward calls=%d leader=%q", rpc.calls, rpc.leaderAddress)
	}
	if rpc.decodeErr != nil {
		t.Fatalf("forwarded reconcile body is not decodable: %v", rpc.decodeErr)
	}
	if rpc.payload.ClusterID != payload.ClusterID || rpc.payload.InstanceID != payload.InstanceID ||
		rpc.payload.Nonce != payload.Nonce || rpc.payload.Signature != payload.Signature {
		t.Fatalf("forwarded payload=%+v want=%+v", rpc.payload, payload)
	}
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
	if lifetime := keep.ValidUntil.Sub(now); lifetime <= 0 || lifetime > 11*time.Second {
		t.Fatalf("leader authorization lifetime=%s, want at most 10 seconds plus test scheduling tolerance", lifetime)
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

func TestAgentReconcileRecordsPositiveAuthorizationForFailoverFencing(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, primary, _, _ := seedAgentReconcileState(t, repository, now)
	tracker := coordination.NewAgentAuthorizationTracker()
	authority := &apiMutationAuthorityStub{
		term: 17, leaderID: model.NewResourceID(), leaderAddress: "controller-a:10009",
	}
	server := newAPIServer(
		t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{},
		WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"),
		WithAgentAuthorizationTracker(tracker),
	)
	request := agent.ReconcileRequest{
		ClusterID: cluster.ResourceID, InstanceID: primary.ResourceID,
		RequestedAt: now, Nonce: "record-authorization-0001",
	}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}

	response := callAgentReconcile(t, server, request)
	if response.Code != http.StatusOK {
		t.Fatalf("agent reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	validUntil, found := tracker.AuthorizationUntil(cluster.ResourceID, primary.ResourceID, 17)
	if !found || !validUntil.After(now) || validUntil.After(now.Add(11*time.Second)) {
		t.Fatalf("recorded authorization=(%s,%t)", validUntil, found)
	}
}

func TestAgentReconcileKeepsPreparedTransitionTargetOnlyAtExecuteStage(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, formerPrimary, lease := seedRebootBootstrapState(t, repository, now)
	snapshot, found := repository.TopologySnapshot(cluster.ResourceID)
	if !found {
		t.Fatal("topology snapshot is missing")
	}
	var target model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.ResourceID != formerPrimary.ResourceID {
			target = instance
		}
	}
	if !model.ValidResourceID(target.ResourceID) {
		t.Fatal("transition target is missing")
	}
	record, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"},
		TargetID:  target.ResourceID, IdempotencyKey: "agent-transition-stage",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{Stage: model.StageApprove, Status: model.OperationRunning, Message: "approval validated"})
	if err != nil {
		t.Fatal(err)
	}
	lease.OperationID = record.ResourceID
	lease.OwnerID = target.ResourceID
	lease.PreviousOwnerID = formerPrimary.ResourceID
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	leaderID := model.NewResourceID()
	authority := &apiMutationAuthorityStub{leaderID: leaderID, leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))

	request := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: target.ResourceID, RequestedAt: now, Nonce: "approve-stage-0001"}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response := callAgentReconcile(t, server, request)
	var decision agent.ReconcileResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileSelfIsolate {
		t.Fatalf("approve-stage decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
	}

	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{Stage: model.StageExecute, Status: model.OperationRunning, Message: "adapter execution started"})
	if err != nil {
		t.Fatal(err)
	}
	request.RequestedAt = time.Now().UTC()
	request.Nonce = "execute-stage-0001"
	request.Signature = ""
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response = callAgentReconcile(t, server, request)
	decision = agent.ReconcileResponse{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileTransitionTarget || decision.LeaseID != lease.ResourceID {
		t.Fatalf("execute-stage decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
	}

	sourceRequest := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: formerPrimary.ResourceID, RequestedAt: time.Now().UTC(), Nonce: "execute-source-0001"}
	if err := agent.SignReconcileRequest(&sourceRequest, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response = callAgentReconcile(t, server, sourceRequest)
	decision = agent.ReconcileResponse{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileTransitionSource || decision.LeaseID != lease.ResourceID {
		t.Fatalf("execute-stage source decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
	}

	if err := repository.CommitHAEndpointOwner(cluster.ResourceID, lease.HAEndpointID, target.ResourceID, true); err != nil {
		t.Fatal(err)
	}
	lease.OperationID = lease.HAEndpointID
	lease.PreviousOwnerID = ""
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request.RequestedAt = time.Now().UTC()
	request.Nonce = "finalized-stage-0001"
	request.Signature = ""
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response = callAgentReconcile(t, server, request)
	decision = agent.ReconcileResponse{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileTransitionTarget || decision.LeaseID != lease.ResourceID {
		t.Fatalf("finalized execute-stage decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
	}

	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: model.StageReport, Status: model.OperationSucceeded, Message: "operation completed before topology convergence",
		Verification: &model.Verification{Passed: true, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckPass}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request.RequestedAt = time.Now().UTC()
	request.Nonce = "stable-before-topology-0001"
	request.Signature = ""
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response = callAgentReconcile(t, server, request)
	decision = agent.ReconcileResponse{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil || decision.Action != agent.ReconcileKeepVIP || decision.LeaseID != lease.ResourceID {
		t.Fatalf("stable handoff decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
	}
}

func TestAgentReconcileKeepsPromotedUnverifiedAutomaticFailoverTarget(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, formerPrimary, lease := seedRebootBootstrapState(t, repository, now)
	snapshot, found := repository.TopologySnapshot(cluster.ResourceID)
	if !found {
		t.Fatal("topology snapshot is missing")
	}
	var target model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.ResourceID != formerPrimary.ResourceID {
			target = instance
		}
	}
	record, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
			Kind: model.OperationFailover, RequestedBy: "clusterguard-automatic-recovery",
		},
		TargetID: target.ResourceID, IdempotencyKey: "automatic-promoted-unverified",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := model.OperationPlan{
		OperationID: record.ResourceID, ClusterID: cluster.ResourceID,
		SourceID: formerPrimary.ResourceID, TargetID: target.ResourceID,
		Stage: model.StagePlan, ObservationToken: "automatic-resume-observation",
		ResourceRevisions: map[model.ResourceID]uint64{
			cluster.ResourceID:       cluster.MetadataRevision,
			formerPrimary.ResourceID: formerPrimary.MetadataRevision,
			target.ResourceID:        target.MetadataRevision,
		},
		Steps:  []model.PlanStep{{Index: 1, Name: "promote target", Owner: "mysql", TargetID: target.ResourceID, Mutating: true}},
		Digest: "sha256:automatic-resume", Mutating: true,
	}
	record, err = repository.PutOperationPlan(record.ResourceID, record.MetadataRevision, plan)
	if err != nil {
		t.Fatal(err)
	}
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate,
		FailureClass: "promoted_unverified", Message: "promotion committed; verification incomplete",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.OperationID = record.ResourceID
	lease.OwnerID = target.ResourceID
	lease.PreviousOwnerID = formerPrimary.ResourceID
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	authority := &apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "controller-a:10009"}
	server := newAPIServer(t, repository, adapter.NewUnsupported(model.EngineMySQL), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
	request := agent.ReconcileRequest{
		ClusterID: cluster.ResourceID, InstanceID: target.ResourceID,
		RequestedAt: now, Nonce: "resume-target-0001",
	}
	if err := agent.SignReconcileRequest(&request, "agent-secret"); err != nil {
		t.Fatal(err)
	}
	response := callAgentReconcile(t, server, request)
	decision := agent.ReconcileResponse{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decision) != nil ||
		decision.Action != agent.ReconcileTransitionTarget || decision.LeaseID != lease.ResourceID {
		t.Fatalf("resumed target decision status=%d response=%+v body=%s", response.Code, decision, response.Body.String())
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

func TestAgentReconcileAcceptsValidLeaseOlderThanFreshEquivalentTopology(t *testing.T) {
	now := time.Now().UTC()
	repository := store.NewMemory()
	cluster, canonical, lease := seedRebootBootstrapState(t, repository, now)
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{
		Lease: lease, CreatedAt: now.Add(-20 * time.Second), UpdatedAt: now.Add(-5 * time.Second),
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
		t.Fatalf("valid lease decision status=%d body=%s", response.Code, response.Body.String())
	}
	if decision.Action != agent.ReconcileBootstrapPrimary {
		t.Fatalf("valid lease was invalidated by a newer equivalent topology sample: %+v", decision)
	}
}

func TestValidBootstrapLeaseRecordRejectsInvalidTemporalEvidence(t *testing.T) {
	now := time.Now().UTC()
	base := coordination.LeaseRecord{
		Lease:     endpoint.Lease{ExpiresAt: now.Add(30 * time.Second)},
		UpdatedAt: now,
	}
	tests := []struct {
		name   string
		mutate func(*coordination.LeaseRecord)
	}{
		{name: "zero update time", mutate: func(record *coordination.LeaseRecord) { record.UpdatedAt = time.Time{} }},
		{name: "future update time", mutate: func(record *coordination.LeaseRecord) { record.UpdatedAt = now.Add(time.Nanosecond) }},
		{name: "non-positive lifetime", mutate: func(record *coordination.LeaseRecord) { record.UpdatedAt = record.Lease.ExpiresAt }},
		{name: "lifetime exceeds maximum", mutate: func(record *coordination.LeaseRecord) { record.UpdatedAt = now.Add(-31 * time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			test.mutate(&record)
			if validBootstrapLeaseRecord(record, now) {
				t.Fatalf("invalid bootstrap lease record was accepted: %+v", record)
			}
		})
	}
}

func TestAgentReconcileRejectsBootstrapLeaseWithImpossibleLifetime(t *testing.T) {
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
