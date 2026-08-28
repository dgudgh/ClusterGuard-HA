package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/approval"
	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type approvalAPIAdapter struct {
	adapter.UnsupportedAdapter
	executeCalls   int
	buildPlanCalls int
	precheck       []model.Check
	executeStarted chan struct{}
	executeRelease chan struct{}
	executeOnce    sync.Once
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
	if candidate.precheck != nil {
		return append([]model.Check{}, candidate.precheck...), nil
	}
	return []model.Check{{Name: "candidate_ready", Status: model.CheckPass}}, nil
}

func (candidate *approvalAPIAdapter) BuildPlan(_ context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	candidate.buildPlanCalls++
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
	if candidate.executeStarted != nil {
		candidate.executeOnce.Do(func() { close(candidate.executeStarted) })
	}
	if candidate.executeRelease != nil {
		select {
		case <-ctx.Done():
			return model.Execution{Status: model.OperationFailed, Message: ctx.Err().Error()}, ctx.Err()
		case <-candidate.executeRelease:
		}
	}
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

func newAuthenticatedApprovalAPIClient(t *testing.T, role model.PlatformRole) (*authTestClient, *store.Repository, *approvalAPIAdapter, model.ResourceID, model.ResourceID, string) {
	t.Helper()
	server, repository, candidate, clusterID, targetID := newApprovalAPIServer(t)
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	username := string(role) + "-user"
	password := "Secure-platform-password-123"
	hasher := platformauth.Argon2Hasher{
		Params: platformauth.Argon2Params{
			Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x61}, 256)),
	}
	passwordHash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("hash platform password: %v", err)
	}
	if _, err := repository.CreatePlatformUser(model.PlatformUser{
		ResourceMeta: model.ResourceMeta{
			ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now,
		},
		Username: username, DisplayName: username, Role: role,
		PasswordHash: passwordHash, AuthRevision: 1,
	}); err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	server.authentication = platformauth.New(
		repository,
		hasher,
		bytes.NewReader(bytes.Repeat([]byte{0x62}, 8192)),
		func() time.Time { return now },
		8*time.Hour,
	)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, username, password); response.Code != http.StatusOK {
		t.Fatalf("platform login status=%d body=%s", response.Code, response.Body.String())
	}
	return client, repository, candidate, clusterID, targetID, username
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

func TestApprovalBlockingPlanReturnsSafeOperationEvidence(t *testing.T) {
	server, repository, candidate, clusterID, targetID := newApprovalAPIServer(t)
	candidate.precheck = []model.Check{{
		Name: "replication_lag", Status: model.CheckFail,
		Message: "candidate password=secret is behind",
	}}
	response := requestJSON(
		t, server.Handler(), http.MethodPost, "/api/v1/approvals",
		approvalIssueBody(clusterID, targetID),
		"Bearer "+testControlToken,
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("blocking approval status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Status  string                `json:"status"`
		Message string                `json:"message"`
		Result  model.OperationRecord `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode blocking approval: %v", err)
	}
	if envelope.Status != "blocked" || envelope.Message != "operation precheck contains blocking checks" {
		t.Fatalf("blocking approval envelope=%+v", envelope)
	}
	if len(envelope.Result.Precheck) != 1 || envelope.Result.Precheck[0].Name != "replication_lag" ||
		envelope.Result.Precheck[0].Status != model.CheckFail || envelope.Result.Precheck[0].Message != "check failed" {
		t.Fatalf("blocking approval evidence=%+v", envelope.Result.Precheck)
	}
	if strings.Contains(response.Body.String(), "password=secret") || strings.Contains(response.Body.String(), "approval_token") {
		t.Fatalf("blocking approval leaked sensitive data: %s", response.Body.String())
	}
	if grants := repository.ApprovalGrants(); len(grants) != 0 {
		t.Fatalf("blocking approval persisted grants: %+v", grants)
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

func TestApprovalIssuanceReusesPersistedImmutablePlan(t *testing.T) {
	server, _, candidate, clusterID, targetID := newApprovalAPIServer(t)
	planBody := map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id": clusterID, "engine": "mysql", "kind": "switchover", "requested_by": "dba-admin",
		},
		"target_id": targetID, "idempotency_key": "approval-api-switch",
	}
	planned := requestJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/plan", planBody, "Bearer "+testControlToken)
	if planned.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planned.Code, planned.Body.String())
	}
	issued := requestJSON(
		t, server.Handler(), http.MethodPost, "/api/v1/approvals",
		approvalIssueBody(clusterID, targetID),
		"Bearer "+testControlToken,
	)
	if issued.Code != http.StatusCreated {
		t.Fatalf("approval status=%d body=%s", issued.Code, issued.Body.String())
	}
	if candidate.buildPlanCalls != 1 {
		t.Fatalf("persisted immutable plan was rebuilt: calls=%d", candidate.buildPlanCalls)
	}
}

func TestPlatformSessionAutomaticallyIssuesAndConsumesOneTimeApproval(t *testing.T) {
	for _, role := range []model.PlatformRole{model.PlatformRoleAdmin, model.PlatformRoleOperator} {
		t.Run(string(role), func(t *testing.T) {
			client, repository, candidate, clusterID, targetID, username := newAuthenticatedApprovalAPIClient(t, role)
			idempotencyKey := "session-switch-" + string(role)
			response := client.request(t, http.MethodPost, "/api/v1/operations/execute", map[string]interface{}{
				"operation": map[string]interface{}{
					"cluster_id":   clusterID,
					"engine":       "mysql",
					"kind":         "switchover",
					"requested_by": "caller-supplied-identity",
				},
				"target_id":       targetID,
				"idempotency_key": idempotencyKey,
			}, true)
			if response.Code != http.StatusOK {
				t.Fatalf("session execute status=%d body=%s", response.Code, response.Body.String())
			}
			if candidate.executeCalls != 1 {
				t.Fatalf("session execute calls=%d", candidate.executeCalls)
			}
			for _, forbidden := range []string{"cgag_", "token_hash", "approval_token"} {
				if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
					t.Fatalf("session execute response exposes %q: %s", forbidden, response.Body.String())
				}
			}
			record, found := repository.OperationByIdempotencyKey(idempotencyKey)
			if !found || record.Operation.RequestedBy != username {
				t.Fatalf("stored operation=%+v found=%v", record, found)
			}
			grants := repository.ApprovalGrants()
			if len(grants) != 1 {
				t.Fatalf("approval grants=%+v", grants)
			}
			if grants[0].Status != model.ApprovalGrantConsumed || grants[0].IssuedBy != username {
				t.Fatalf("approval grant=%+v", grants[0])
			}
		})
	}
}

func TestPlatformSessionDuplicateExecuteJoinsOneIdempotentOperation(t *testing.T) {
	client, repository, candidate, clusterID, targetID, _ := newAuthenticatedApprovalAPIClient(t, model.PlatformRoleAdmin)
	candidate.executeStarted = make(chan struct{})
	candidate.executeRelease = make(chan struct{})
	body := map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id": clusterID, "engine": "mysql", "kind": "switchover", "requested_by": "ignored",
		},
		"target_id": targetID, "idempotency_key": "session-duplicate-switch",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal duplicate execute request: %v", err)
	}
	request := func() *httptest.ResponseRecorder {
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/operations/execute", bytes.NewReader(payload))
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("X-CSRF-Token", client.csrf)
		for _, cookie := range client.cookies {
			httpRequest.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		client.handler.ServeHTTP(response, httpRequest)
		return response
	}

	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstResult <- request() }()
	select {
	case <-candidate.executeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first session execution did not reach the adapter")
	}
	secondResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondResult <- request() }()
	time.Sleep(10 * time.Millisecond)
	close(candidate.executeRelease)

	first := <-firstResult
	second := <-secondResult
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("duplicate session execution responses: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	firstOperation := decodeOperationResult(t, first.Body.Bytes())
	secondOperation := decodeOperationResult(t, second.Body.Bytes())
	if firstOperation.ResourceID != secondOperation.ResourceID || firstOperation.Status != model.OperationSucceeded || secondOperation.Status != model.OperationSucceeded {
		t.Fatalf("duplicate session operations: first=%+v second=%+v", firstOperation, secondOperation)
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("duplicate session request executed adapter %d times", candidate.executeCalls)
	}
	if grants := repository.ApprovalGrants(); len(grants) != 1 || grants[0].Status != model.ApprovalGrantConsumed {
		t.Fatalf("duplicate session approval grants=%+v", grants)
	}
}

func TestPlatformSessionAutomaticallyApprovesDurableOperationResource(t *testing.T) {
	client, repository, candidate, clusterID, targetID, username := newAuthenticatedApprovalAPIClient(t, model.PlatformRoleAdmin)
	idempotencyKey := "session-resource-switch"
	planned := client.request(t, http.MethodPost, "/api/v1/operations/plan", map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id":   clusterID,
			"engine":       "mysql",
			"kind":         "switchover",
			"requested_by": "forged-plan-actor",
		},
		"target_id":       targetID,
		"idempotency_key": idempotencyKey,
	}, true)
	if planned.Code != http.StatusOK {
		t.Fatalf("session plan status=%d body=%s", planned.Code, planned.Body.String())
	}
	record, found := repository.OperationByIdempotencyKey(idempotencyKey)
	if !found || record.Operation.RequestedBy != username {
		t.Fatalf("planned operation=%+v found=%v", record, found)
	}
	executed := client.request(
		t,
		http.MethodPost,
		"/api/v1/operations/"+string(record.ResourceID)+"/execute",
		map[string]interface{}{},
		true,
	)
	if executed.Code != http.StatusOK {
		t.Fatalf("session resource execute status=%d body=%s", executed.Code, executed.Body.String())
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("session resource execute calls=%d", candidate.executeCalls)
	}
	grants := repository.ApprovalGrants()
	if len(grants) != 1 || grants[0].Status != model.ApprovalGrantConsumed || grants[0].IssuedBy != username {
		t.Fatalf("session resource grants=%+v", grants)
	}
}

func TestServiceBearerCannotAutoIssuePlatformApproval(t *testing.T) {
	client, repository, candidate, clusterID, targetID, _ := newAuthenticatedApprovalAPIClient(t, model.PlatformRoleAdmin)
	response := requestJSON(t, client.handler, http.MethodPost, "/api/v1/operations/execute", map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id":   clusterID,
			"engine":       "mysql",
			"kind":         "switchover",
			"requested_by": "service-api",
		},
		"target_id":       targetID,
		"idempotency_key": "service-bearer-without-grant",
	}, "Bearer "+testControlToken)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("service bearer status=%d body=%s", response.Code, response.Body.String())
	}
	if candidate.executeCalls != 0 || len(repository.ApprovalGrants()) != 0 {
		t.Fatalf("service bearer bypassed approval: calls=%d grants=%+v", candidate.executeCalls, repository.ApprovalGrants())
	}
}

func TestAuthenticatedServiceAPIStillSupportsExplicitOneTimeApproval(t *testing.T) {
	client, repository, candidate, clusterID, targetID, _ := newAuthenticatedApprovalAPIClient(t, model.PlatformRoleAdmin)
	issued := requestJSON(
		t,
		client.handler,
		http.MethodPost,
		"/api/v1/approvals",
		approvalIssueBody(clusterID, targetID),
		"Bearer "+testControlToken,
	)
	if issued.Code != http.StatusCreated {
		t.Fatalf("service approval issue status=%d body=%s", issued.Code, issued.Body.String())
	}
	var envelope struct {
		Result struct {
			Operation     model.OperationRecord `json:"operation"`
			ApprovalToken string                `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode service approval: %v", err)
	}
	executed := requestJSON(t, client.handler, http.MethodPost, "/api/v1/operations/execute", map[string]interface{}{
		"operation":       envelope.Result.Operation.Operation,
		"target_id":       targetID,
		"idempotency_key": envelope.Result.Operation.IdempotencyKey,
		"approval_token":  envelope.Result.ApprovalToken,
	}, "Bearer "+testControlToken)
	if executed.Code != http.StatusOK {
		t.Fatalf("service approved execute status=%d body=%s", executed.Code, executed.Body.String())
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("service approved execute calls=%d", candidate.executeCalls)
	}
	grants := repository.ApprovalGrants()
	if len(grants) != 1 || grants[0].Status != model.ApprovalGrantConsumed {
		t.Fatalf("service approval grants=%+v", grants)
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
