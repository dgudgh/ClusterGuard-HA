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

func TestPublicOperationRecordExplainsIndeterminateOutcomeWithoutLeakingDetails(t *testing.T) {
	secret := "password=private /var/lib/postgresql"
	tests := []struct {
		name         string
		failureClass string
		want         string
	}{
		{name: "former primary rebuild", failureClass: "rebuild_failed", want: "former-primary synchronization failed"},
		{name: "promotion verification", failureClass: "promoted_unverified", want: "primary transition completed"},
		{name: "rewind verification", failureClass: "rewind_unknown", want: "former-primary synchronization outcome requires verification"},
		{name: "unknown", failureClass: "unexpected", want: "operation outcome requires verification"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := model.OperationRecord{
				Status:       model.OperationIndeterminate,
				FailureClass: test.failureClass,
				Message:      secret,
				Execution: model.Execution{
					Status:  model.OperationIndeterminate,
					Message: secret,
				},
			}

			public := publicOperationRecord(record)
			if !strings.Contains(public.Message, test.want) || !strings.Contains(public.Execution.Message, test.want) {
				t.Fatalf("public outcome=%q execution=%q, want %q", public.Message, public.Execution.Message, test.want)
			}
			if strings.Contains(public.Message, "private") || strings.Contains(public.Execution.Message, "/var/lib/postgresql") {
				t.Fatalf("public operation leaked private details: %+v", public)
			}
		})
	}
}

func TestPublicOperationRecordExposesReviewEvidenceWithoutFreeformNote(t *testing.T) {
	reviewedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	record := model.OperationRecord{
		Status: model.OperationIndeterminate,
		Review: &model.OperationReview{
			ReviewedAt: reviewedAt, ReviewedBy: "dba", Disposition: model.OperationReviewAcknowledgedIndeterminate,
			Note: "password=private /var/lib/mysql",
		},
	}
	public := publicOperationRecord(record)
	if public.Review == nil || public.Review.ReviewedAt != reviewedAt || public.Review.ReviewedBy != "dba" || public.Review.Note != "operator review note recorded" {
		t.Fatalf("public review evidence=%+v", public.Review)
	}
	if record.Review.Note != "password=private /var/lib/mysql" {
		t.Fatalf("public projection mutated stored review: %+v", record.Review)
	}
}

func TestClassifyOperationExecutionUsesSafeFailureClassGuidance(t *testing.T) {
	record := model.OperationRecord{Status: model.OperationIndeterminate, FailureClass: "rebuild_failed"}
	execution := model.Execution{Status: model.OperationIndeterminate}
	response := classifyOperationExecution(errors.New("password=private"), execution, record)
	if response.code != http.StatusInternalServerError || response.status != "indeterminate" ||
		!strings.Contains(response.message, "current primary is reachable") || strings.Contains(response.message, "private") {
		t.Fatalf("unexpected classified response: %+v", response)
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

func TestOperationAPIListsMoreThanThreeAuditRecordsAcrossClusters(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	clusterIDs := []model.ResourceID{model.NewResourceID(), model.NewResourceID()}
	for index := 0; index < 8; index++ {
		body := operationRequestBody(clusterIDs[index%len(clusterIDs)], model.NewResourceID(), fmt.Sprintf("audit-history-%d", index))
		response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create operation %d: status=%d body=%s", index, response.Code, response.Body.String())
		}
	}

	decodeList := func(response *httptest.ResponseRecorder) []model.OperationRecord {
		t.Helper()
		var envelope struct {
			Result []model.OperationRecord `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode operation list: %v", err)
		}
		return envelope.Result
	}

	allResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations", nil)
	if allResponse.Code != http.StatusOK {
		t.Fatalf("list all status=%d body=%s", allResponse.Code, allResponse.Body.String())
	}
	if operations := decodeList(allResponse); len(operations) != 8 {
		t.Fatalf("all-cluster operation history was truncated: got %d want 8", len(operations))
	}

	clusterResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?cluster_id="+string(clusterIDs[0]), nil)
	if clusterResponse.Code != http.StatusOK {
		t.Fatalf("list cluster status=%d body=%s", clusterResponse.Code, clusterResponse.Body.String())
	}
	if operations := decodeList(clusterResponse); len(operations) != 4 {
		t.Fatalf("cluster operation history was truncated: got %d want 4", len(operations))
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

func TestOperationAPIReviewsIndeterminateOutcomeWithoutChangingItsStatus(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-review-indeterminate"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	indeterminate, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate, FailureClass: "verification_unknown", Message: "manual review required",
	})
	if err != nil {
		t.Fatalf("mark indeterminate: %v", err)
	}

	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/review", map[string]string{
		"note": "database role and writer endpoint verified",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("review status=%d body=%s", response.Code, response.Body.String())
	}
	result := decodeOperationResult(t, response.Body.Bytes())
	if result.Status != model.OperationIndeterminate || result.Review == nil || result.Review.ReviewedBy != "service-api" || result.Review.Note != "operator review note recorded" || result.RequiresReview() {
		t.Fatalf("review API rewrote or omitted evidence: %+v", result)
	}
	persisted, found := repository.Operation(created.ResourceID)
	if !found || persisted.Review == nil || persisted.Review.Note != "database role and writer endpoint verified" || len(repository.Audits()) != 1 || len(repository.Reports()) != 1 {
		t.Fatalf("review was not durably recorded: %+v audits=%+v reports=%+v", persisted, repository.Audits(), repository.Reports())
	}

	retry := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/review", map[string]string{
		"note": "database role and writer endpoint verified",
	})
	if retry.Code != http.StatusOK || len(repository.Audits()) != 1 || len(repository.Reports()) != 1 {
		t.Fatalf("review retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	conflict := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/review", map[string]string{"note": "replace note"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("review overwrite status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if persisted.MetadataRevision != indeterminate.MetadataRevision+1 {
		t.Fatalf("review metadata revision=%d want=%d", persisted.MetadataRevision, indeterminate.MetadataRevision+1)
	}
}

func TestOperationAPIRejectsInvalidReviewRequests(t *testing.T) {
	server, _ := newDurableOperationAPIServer(t)
	createdResponse := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations", operationRequestBody(model.NewResourceID(), model.NewResourceID(), "api-review-invalid"))
	created := decodeOperationResult(t, createdResponse.Body.Bytes())
	planned := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/review", map[string]string{"note": "reviewed"})
	if planned.Code != http.StatusConflict {
		t.Fatalf("planned review status=%d body=%s", planned.Code, planned.Body.String())
	}
	missing := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/"+string(created.ResourceID)+"/review", map[string]string{})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing note status=%d body=%s", missing.Code, missing.Body.String())
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
