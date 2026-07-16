package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type approvalAPIAdapter struct {
	adapter.UnsupportedAdapter
	executeCalls int
}

func newApprovalAPIAdapter() *approvalAPIAdapter {
	return &approvalAPIAdapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL)}
}

func (candidate *approvalAPIAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityPrecheck: {Available: true},
		adapter.CapabilityPlan:     {Available: true},
		adapter.CapabilityExecute:  {Available: true, Mutating: true},
		adapter.CapabilityVerify:   {Available: true},
	}}
}

func (candidate *approvalAPIAdapter) Precheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return []model.Check{{Name: "candidate_ready", Status: model.CheckPass}}, nil
}

func (candidate *approvalAPIAdapter) BuildPlan(_ context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	resolved := request.Resolved
	return model.OperationPlan{
		OperationID:      request.Operation.ResourceID,
		ClusterID:        request.Operation.ClusterID,
		SourceID:         resolved.Primary.ResourceID,
		TargetID:         request.TargetID,
		ObservationToken: resolved.ObservationToken,
		ResourceRevisions: map[model.ResourceID]uint64{
			resolved.Cluster.ResourceID: 1,
			resolved.Primary.ResourceID: 1,
			resolved.Target.ResourceID:  1,
		},
		Steps:    []model.PlanStep{{Index: 1, Name: "switch_primary", Owner: "mysql", TargetID: request.TargetID, Mutating: true}},
		Digest:   "sha256:approval-api-plan",
		Summary:  "approval API plan",
		Mutating: true,
	}, nil
}

func (candidate *approvalAPIAdapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	candidate.executeCalls++
	if err := request.Progress.CompleteStep(ctx, "switch_primary", "switched"); err != nil {
		return model.Execution{Status: model.OperationIndeterminate, Message: err.Error()}, err
	}
	return model.Execution{Status: model.OperationRunning, Message: "executed"}, nil
}

func (candidate *approvalAPIAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	return model.Verification{Passed: true, Checks: []model.Check{{Name: "primary_and_vip", Status: model.CheckPass}}}, nil
}

type approvalAPIDiscovery struct {
	snapshot model.TopologySnapshot
}

func (discovery approvalAPIDiscovery) CaptureObservation(_ context.Context, operation model.Operation) (workflow.ObservationToken, error) {
	return workflow.ObservationToken{
		ClusterID:  operation.ClusterID,
		ObservedAt: discovery.snapshot.ObservedAt,
		Digest:     "sha256:approval-api-observation",
		Snapshot:   discovery.snapshot,
	}, nil
}

func (discovery approvalAPIDiscovery) RevalidateObservation(context.Context, model.Operation, workflow.ObservationToken) error {
	return nil
}

func newApprovalAPIServer(t *testing.T) (*Server, *store.Repository, *approvalAPIAdapter, model.ResourceID, model.ResourceID) {
	t.Helper()
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
		Engine:       model.EngineMySQL, DisplayName: "approval-test",
	}
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
		ClusterID:    cluster.ResourceID, Engine: model.EngineMySQL, Role: model.RolePrimary,
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
		ClusterID:    cluster.ResourceID, Engine: model.EngineMySQL, Role: model.RoleReplica,
	}
	resolved := adapter.ResolvedOperation{
		Cluster: cluster, Primary: primary, Target: target,
		Snapshot: model.TopologySnapshot{
			ClusterID: cluster.ResourceID, ObservedAt: now,
			Instances: []model.DatabaseInstance{primary, target},
		},
	}
	registry := adapter.NewRegistry()
	candidate := newApprovalAPIAdapter()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	repository := store.NewMemory()
	approvalService := approval.New(repository, bytes.NewReader(bytes.Repeat([]byte{0x51}, 32)), func() time.Time { return now })
	resolver := workflow.OperationResolverFunc(func(_ context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
		request.Resolved = &resolved
		return request, nil
	})
	service := workflow.New(
		registry,
		approvalAPIDiscovery{snapshot: resolved.Snapshot},
		workflow.AllowAllSafety{},
		workflow.NewMemoryLocks(),
		approvalService,
		repository,
		workflow.WithOperationStore(repository),
		workflow.WithOperationResolver(resolver),
	)
	server := NewServer(
		registry, repository, service, &fakeRefresher{},
		WithControlToken(testControlToken),
		WithApprovalService(approvalService),
	)
	return server, repository, candidate, cluster.ResourceID, target.ResourceID
}

func approvalIssueBody(clusterID, targetID model.ResourceID) map[string]interface{} {
	return map[string]interface{}{
		"cluster_id":      clusterID,
		"engine":          "mysql",
		"operation_kind":  "switchover",
		"target_id":       targetID,
		"issued_by":       "dba-admin",
		"ttl_seconds":     300,
		"idempotency_key": "approval-api-switch",
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body interface{}, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestApprovalIssuanceRequiresAdministrativeCredentialAndReturnsSecretOnce(t *testing.T) {
	server, _, _, clusterID, targetID := newApprovalAPIServer(t)
	request := approvalIssueBody(clusterID, targetID)
	unauthorized := requestJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", request, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestApprovalGrantExecutesWithoutControlTokenAndRejectsReuse(t *testing.T) {
	server, repository, candidate, clusterID, targetID := newApprovalAPIServer(t)
	issued := requestJSON(
		t, server.Handler(), http.MethodPost, "/api/v1/approvals",
		approvalIssueBody(clusterID, targetID),
		"Bearer "+testControlToken,
	)
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issueEnvelope struct {
		Result struct {
			Operation     model.OperationRecord `json:"operation"`
			Grant         model.ApprovalGrant   `json:"grant"`
			ApprovalToken string                `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issueEnvelope); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if issueEnvelope.Result.ApprovalToken == "" || issueEnvelope.Result.Grant.TokenHash != "" || issueEnvelope.Result.Operation.Plan.Digest == "" {
		t.Fatalf("issue result=%+v", issueEnvelope.Result)
	}

	executeBody := map[string]interface{}{
		"operation":       issueEnvelope.Result.Operation.Operation,
		"target_id":       targetID,
		"idempotency_key": issueEnvelope.Result.Operation.IdempotencyKey,
		"approval_token":  issueEnvelope.Result.ApprovalToken,
	}
	executed := requestJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/execute", executeBody, "")
	if executed.Code != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", executed.Code, executed.Body.String())
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("execute calls=%d", candidate.executeCalls)
	}
	storedGrant, found := repository.ApprovalGrant(issueEnvelope.Result.Grant.ResourceID)
	if !found || storedGrant.Status != model.ApprovalGrantConsumed {
		t.Fatalf("stored grant=%+v found=%v", storedGrant, found)
	}

	reused := requestJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/execute", executeBody, "")
	if reused.Code != http.StatusConflict && reused.Code != http.StatusUnauthorized {
		t.Fatalf("reuse status=%d body=%s", reused.Code, reused.Body.String())
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("reused grant executed again: calls=%d", candidate.executeCalls)
	}
}

func TestHTTPInputCannotSelectAutomaticAuthorization(t *testing.T) {
	server, _, _, _, _ := newApprovalAPIServer(t)
	response := requestJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/execute", map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id": model.NewResourceID(), "engine": "mysql", "kind": "failover",
			"requested_by": workflow.AutomaticRecoveryActor,
		},
		"target_id":          model.NewResourceID(),
		"idempotency_key":    "forged-automatic",
		"authorization_mode": "automatic",
	}, "")
	if response.Code != http.StatusBadRequest && response.Code != http.StatusUnauthorized {
		t.Fatalf("forged automatic status=%d body=%s", response.Code, response.Body.String())
	}
}
