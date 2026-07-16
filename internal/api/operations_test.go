package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestOperationExecutionResponsePrefersIndeterminateOverJournalFailure(t *testing.T) {
	secret := "password=top-secret /var/lib/private"
	execution := model.Execution{Status: model.OperationIndeterminate, Message: secret}
	record := model.OperationRecord{Status: model.OperationIndeterminate, Message: secret, Execution: execution}
	recorder := httptest.NewRecorder()
	writeOperationExecutionResponse(recorder, fmt.Errorf("%w: %s", workflow.ErrJournalPersistence, secret), execution, record)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), `"status":"indeterminate"`) || strings.Contains(recorder.Body.String(), secret) || strings.Contains(recorder.Body.String(), "/var/lib/private") {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestOperationActionErrorRedactsUntrustedDetails(t *testing.T) {
	secret := "token=top-secret /etc/clusterguard/credentials"
	recorder := httptest.NewRecorder()
	server := &Server{}
	server.writeOperationActionError(recorder, errors.New(secret), model.OperationRecord{
		Status: model.OperationFailed, Message: secret,
		Execution: model.Execution{Status: model.OperationFailed, Message: secret},
	})
	if recorder.Code != http.StatusConflict || strings.Contains(recorder.Body.String(), "top-secret") || strings.Contains(recorder.Body.String(), "/etc/clusterguard") {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicOperationRecordPreservesPrecheckOutcomeAndRedactsMessage(t *testing.T) {
	record := model.OperationRecord{Precheck: []model.Check{
		{Name: "replication_threads", Status: model.CheckPass, Message: "username=private"},
		{Name: "gtid_consistency", Status: model.CheckFail, Message: "password=private /var/lib/mysql"},
	}}

	public := publicOperationRecord(record)
	if len(public.Precheck) != 2 || public.Precheck[0].Name != "replication_threads" || public.Precheck[1].Name != "gtid_consistency" || public.Precheck[1].Status != model.CheckFail {
		t.Fatalf("public precheck evidence=%+v", public.Precheck)
	}
	if public.Precheck[0].Message != "check passed" || public.Precheck[1].Message != "check failed" {
		t.Fatalf("public precheck messages were not redacted: %+v", public.Precheck)
	}
	if record.Precheck[1].Message != "password=private /var/lib/mysql" {
		t.Fatalf("public projection mutated the stored operation: %+v", record.Precheck)
	}
}

func newDurableOperationAPIServer(t *testing.T) (*Server, *store.Repository) {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	approvalService := approval.New(repository, rand.Reader, time.Now)
	resolver := workflow.OperationResolverFunc(func(_ context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
		return request, nil
	})
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository,
		workflow.WithOperationStore(repository), workflow.WithOperationResolver(resolver))
	return NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken), WithApprovalService(approvalService)), repository
}

func operationRequestBody(clusterID model.ResourceID, targetID model.ResourceID, key string) map[string]interface{} {
	return map[string]interface{}{
		"operation": map[string]interface{}{
			"cluster_id": clusterID, "engine": "mysql", "kind": "switchover", "requested_by": "dba",
		},
		"target_id": targetID, "idempotency_key": key,
	}
}

func decodeOperationResult(t *testing.T, responseBody []byte) model.OperationRecord {
	t.Helper()
	var envelope struct {
		Result model.OperationRecord `json:"result"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		t.Fatalf("decode operation response: %v", err)
	}
	return envelope.Result
}

func TestOperationAPIProvidesIdempotentCreateAndRead(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()
	body := operationRequestBody(clusterID, targetID, "api-switch-1")

	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", body)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	if !model.ValidResourceID(created.ResourceID) || created.IdempotencyKey != "api-switch-1" {
		t.Fatalf("unexpected created operation: %+v", created)
	}

	reusedResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", body)
	if reusedResponse.Code != http.StatusOK {
		t.Fatalf("reuse status=%d body=%s", reusedResponse.Code, reusedResponse.Body.String())
	}
	reused := decodeOperationResult(t, reusedResponse.Body.Bytes())
	if reused.ResourceID != created.ResourceID {
		t.Fatalf("idempotent create returned another operation: created=%s reused=%s", created.ResourceID, reused.ResourceID)
	}

	readResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(created.ResourceID), nil)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	read := decodeOperationResult(t, readResponse.Body.Bytes())
	if read.ResourceID != created.ResourceID || read.TargetID != targetID {
		t.Fatalf("read returned wrong operation: %+v", read)
	}
}

func TestOperationAPIReadsSingleOperationByIdempotencyKey(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-switch-lookup"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())

	lookup := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?idempotency_key=api-switch-lookup", nil)
	if lookup.Code != http.StatusOK {
		t.Fatalf("lookup status=%d body=%s", lookup.Code, lookup.Body.String())
	}
	found := decodeOperationResult(t, lookup.Body.Bytes())
	if found.ResourceID != created.ResourceID || found.IdempotencyKey != "api-switch-lookup" {
		t.Fatalf("lookup returned wrong operation: %+v", found)
	}

	missing := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?idempotency_key=unknown-operation", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing lookup status=%d body=%s", missing.Code, missing.Body.String())
	}
	ambiguous := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?cluster_id="+string(created.Operation.ClusterID)+"&idempotency_key=api-switch-lookup", nil)
	if ambiguous.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous lookup status=%d body=%s", ambiguous.Code, ambiguous.Body.String())
	}
}

func TestOperationAPIReadIncludesPersistedAuditAndReportTimeline(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-operation-timeline"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	secret := "password=timeline-secret /var/lib/private"
	execution := model.Execution{OperationID: created.ResourceID, Status: model.OperationFailed, Message: secret}
	if _, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{Stage: model.StageVerify, Status: model.OperationFailed, Execution: &execution, Message: secret}); err != nil {
		t.Fatalf("transition operation: %v", err)
	}
	if err := repository.RecordAudit(model.AuditEvent{OperationID: created.ResourceID, Stage: model.StageVerify, Message: secret}); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if err := repository.RecordReport(model.Report{OperationID: created.ResourceID, Title: "switchover report", Status: model.OperationFailed, Summary: secret}); err != nil {
		t.Fatalf("record report: %v", err)
	}

	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(created.ResourceID), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Timeline struct {
			Audits  []model.AuditEvent `json:"audits"`
			Reports []model.Report     `json:"reports"`
		} `json:"timeline"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if len(envelope.Timeline.Audits) != 1 || len(envelope.Timeline.Reports) != 1 || envelope.Timeline.Audits[0].OperationID != created.ResourceID || strings.Contains(response.Body.String(), "timeline-secret") || strings.Contains(response.Body.String(), "/var/lib/private") {
		t.Fatalf("operation timeline=%+v", envelope.Timeline)
	}
}

func TestOperationAPIRejectsMissingKeyAndConflictingReuse(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()

	missing := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, targetID, ""))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", missing.Code, missing.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, targetID, "api-switch-conflict")); response.Code != http.StatusCreated {
		t.Fatalf("initial create status=%d body=%s", response.Code, response.Body.String())
	}
	conflict := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(clusterID, model.NewResourceID(), "api-switch-conflict"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting reuse status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestOperationAPIDefaultExecutionIsUnsupportedBeforeSideEffects(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-default-unsupported"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())

	executeResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/execute", map[string]string{"approval_token": "approved"})
	if executeResponse.Code != http.StatusUnauthorized {
		t.Fatalf("execute status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}
	persisted, found := repository.Operation(created.ResourceID)
	if !found || persisted.Status != model.OperationPlanned {
		t.Fatalf("unapproved operation changed state: found=%t record=%+v", found, persisted)
	}
	if len(persisted.Attempts) != 0 {
		t.Fatalf("unsupported operation recorded mutating steps: %+v", persisted.Attempts)
	}
}

func TestOperationAPIMapsIndeterminateExecutionToServerError(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-indeterminate"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	execution := model.Execution{OperationID: created.ResourceID, Status: model.OperationIndeterminate, Message: "promotion outcome requires verification"}
	if _, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate, Execution: &execution, FailureClass: "promoted_unverified", Message: execution.Message,
	}); err != nil {
		t.Fatalf("mark indeterminate: %v", err)
	}

	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/execute", map[string]string{"approval_token": "approved"})
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "approval grant") {
		t.Fatalf("indeterminate execute status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOperationAPIExposesResourceScopedPrecheckAndPlanActions(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-prepare-actions"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	for _, action := range []string{"precheck", "plan"} {
		response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/"+action, map[string]interface{}{})
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "topology observation") {
			t.Fatalf("%s route status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
	verify := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/verify", map[string]interface{}{})
	if verify.Code != http.StatusNotImplemented {
		t.Fatalf("verify route status=%d body=%s", verify.Code, verify.Body.String())
	}
}

func TestLegacyOperationStageRoutesUseDurableUUIDWorkflow(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	clusterID := model.NewResourceID()
	targetID := model.NewResourceID()
	for _, action := range []string{"precheck", "plan"} {
		body := operationRequestBody(clusterID, targetID, "legacy-"+action)
		response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+action, body)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "topology observation") {
			t.Fatalf("legacy %s status=%d body=%s", action, response.Code, response.Body.String())
		}
		if _, found := repository.OperationByIdempotencyKey("legacy-" + action); !found {
			t.Fatalf("legacy %s did not create a durable UUID operation", action)
		}
	}
	verifyBody := operationRequestBody(clusterID, targetID, "legacy-verify")
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", verifyBody); response.Code != http.StatusCreated {
		t.Fatalf("create legacy verify operation status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/verify", verifyBody); response.Code != http.StatusNotImplemented {
		t.Fatalf("legacy verify status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOperationStageRoutesRequireIdempotencyKey(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	body := operationRequestBody(model.NewResourceID(), model.NewResourceID(), "")
	for _, action := range []string{"precheck", "plan", "execute", "verify"} {
		response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+action, body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "idempotency") {
			t.Fatalf("%s status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
}
