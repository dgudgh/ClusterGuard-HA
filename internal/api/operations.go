package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func (server *Server) approvedOperation(ctx context.Context, token string, operation model.Operation, targetID model.ResourceID) (model.OperationRecord, error) {
	if server.approvals == nil || server.store == nil {
		return model.OperationRecord{}, errors.New("approval service is not configured")
	}
	grant, err := server.approvals.AuthorizeIntent(ctx, token, operation, targetID)
	if err != nil {
		return model.OperationRecord{}, err
	}
	record, found := server.store.Operation(grant.OperationID)
	if !found {
		return model.OperationRecord{}, approval.ErrMismatch
	}
	if record.Operation.ClusterID != operation.ClusterID ||
		record.Operation.Engine != operation.Engine ||
		record.Operation.Kind != operation.Kind ||
		record.TargetID != targetID {
		return model.OperationRecord{}, approval.ErrMismatch
	}
	return record, nil
}

func (server *Server) issuePlatformSessionOperationApproval(
	ctx context.Context,
	authentication requestAuthenticationState,
	request adapter.OperationRequest,
) (model.OperationRecord, string, error) {
	if server.approvals == nil || server.workflow == nil || server.store == nil {
		return model.OperationRecord{}, "", errors.New("approval service is not configured")
	}
	request.Operation.RequestedBy = authentication.principal.Username
	if key := strings.TrimSpace(request.IdempotencyKey); key != "" {
		if existing, found := server.store.OperationByIdempotencyKey(key); found {
			if !operationRecordMatchesRequest(existing, request) || existing.Operation.RequestedBy != authentication.principal.Username {
				return existing, "", fmt.Errorf("%w: operation idempotency key belongs to another intent", store.ErrConflict)
			}
			// A repeated browser POST can arrive when the writer VIP moves while the
			// original response is in flight. Durable workflow execution already
			// joins running operations and returns terminal operations idempotently,
			// so only a still-planned operation needs another approval grant.
			if existing.Status != model.OperationPlanned {
				return existing, "", nil
			}
		}
	}
	record, err := server.plannedOperationForApproval(ctx, request)
	if err != nil {
		return record, "", err
	}
	issued, err := server.approvals.Issue(ctx, approval.IssueRequest{
		Operation: record,
		IssuedBy:  authentication.principal.Username,
	})
	if err != nil {
		return record, "", err
	}
	server.recordSecurityEvent(
		authentication.principal,
		authentication.principal.Username,
		"operation_approval_issued",
		"success",
		"one-time platform operation approval issued",
	)
	return record, issued.Token, nil
}

func operationRecordMatchesRequest(record model.OperationRecord, request adapter.OperationRequest) bool {
	return record.Operation.ClusterID == request.Operation.ClusterID &&
		record.Operation.Engine == request.Operation.Engine &&
		record.Operation.Kind == request.Operation.Kind &&
		record.TargetID == request.TargetID
}

type sessionOperationExecutionGate struct {
	mutex      sync.Mutex
	references int
}

// lockSessionOperationExecution serializes automatic session approval and
// execution for one idempotency key. All mutating requests reach the Raft
// leader, so this closes the small gap before durable workflow execution can
// apply its own idempotent in-progress/terminal handling.
func (server *Server) lockSessionOperationExecution(idempotencyKey string) func() {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return func() {}
	}
	server.sessionOperationMu.Lock()
	if server.sessionOperationGates == nil {
		server.sessionOperationGates = make(map[string]*sessionOperationExecutionGate)
	}
	gate := server.sessionOperationGates[key]
	if gate == nil {
		gate = &sessionOperationExecutionGate{}
		server.sessionOperationGates[key] = gate
	}
	gate.references++
	server.sessionOperationMu.Unlock()

	gate.mutex.Lock()
	return func() {
		gate.mutex.Unlock()
		server.sessionOperationMu.Lock()
		gate.references--
		if gate.references == 0 && server.sessionOperationGates[key] == gate {
			delete(server.sessionOperationGates, key)
		}
		server.sessionOperationMu.Unlock()
	}
}

// plannedOperationForApproval binds approval to an already persisted immutable
// plan when one exists. Routine discovery may advance resource revisions
// between the plan response and approval request even when the stable topology
// digest is unchanged. Execute still revalidates topology under the cluster
// lock before consuming the one-time grant.
func (server *Server) plannedOperationForApproval(ctx context.Context, request adapter.OperationRequest) (model.OperationRecord, error) {
	key := strings.TrimSpace(request.IdempotencyKey)
	if key != "" {
		if existing, found := server.store.OperationByIdempotencyKey(key); found {
			if !operationRecordMatchesRequest(existing, request) {
				return existing, fmt.Errorf("%w: operation idempotency key belongs to another intent", store.ErrConflict)
			}
			if existing.Status != model.OperationPlanned {
				return existing, fmt.Errorf("%w: operation is not awaiting approval", store.ErrConflict)
			}
			if strings.TrimSpace(existing.Observation) != "" && strings.TrimSpace(existing.Plan.Digest) != "" {
				return existing, nil
			}
		}
	}
	record, _, err := server.workflow.Plan(ctx, request)
	return record, err
}

func (server *Server) operationsCollection(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		query := request.URL.Query()
		if _, lookupByKey := query["idempotency_key"]; lookupByKey {
			if _, filteredByCluster := query["cluster_id"]; filteredByCluster {
				writeError(writer, http.StatusBadRequest, "cluster_id and idempotency_key cannot be combined")
				return
			}
			key := strings.TrimSpace(query.Get("idempotency_key"))
			if key == "" {
				writeError(writer, http.StatusBadRequest, "idempotency_key is required")
				return
			}
			record, found := server.store.OperationByIdempotencyKey(key)
			if !found {
				writeError(writer, http.StatusNotFound, "operation not found")
				return
			}
			writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": publicOperationRecord(record)})
			return
		}
		clusterID := model.ResourceID(strings.TrimSpace(query.Get("cluster_id")))
		if clusterID != "" && !model.ValidResourceID(clusterID) {
			writeError(writer, http.StatusBadRequest, "cluster_id must be a platform UUID")
			return
		}
		if query.Get("view") == "context" {
			context := server.store.OperationConsoleContext(clusterID)
			recent := make([]operationListItem, 0, len(context.Recent))
			for _, operation := range context.Recent {
				recent = append(recent, summarizeOperation(operation))
			}
			writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
				"view": "context", "cluster_id": clusterID, "operation_count": context.OperationCount,
				"running_count": context.RunningCount, "unreviewed_count": context.UnreviewedCount,
				"historical_source_ids": context.HistoricalSourceIDs, "recent": recent,
			}})
			return
		}
		if query.Get("view") == "page" {
			server.operationLogPage(writer, request, clusterID)
			return
		}
		operations := server.store.Operations(clusterID)
		if query.Get("view") == "summary" {
			summaries := make([]operationListItem, 0, len(operations))
			for _, operation := range operations {
				summaries = append(summaries, summarizeOperation(operation))
			}
			writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": summaries})
			return
		}
		for index := range operations {
			operations[index] = publicOperationRecord(operations[index])
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": operations})
	case http.MethodPost:
		payload := operationPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		if authentication, authenticated := requestAuthentication(request); authenticated && authentication.viaSession {
			payload.Operation.RequestedBy = authentication.principal.Username
		}
		if strings.TrimSpace(payload.Operation.RequestedBy) == "" {
			writeError(writer, http.StatusBadRequest, "operation requested_by is required")
			return
		}
		record, reused, err := server.store.CreateOperation(model.OperationRecord{
			Operation: payload.Operation, TargetID: payload.TargetID, IdempotencyKey: payload.IdempotencyKey,
		})
		if err != nil {
			server.writeOperationStoreError(writer, err)
			return
		}
		status := http.StatusCreated
		if reused {
			status = http.StatusOK
		}
		writeDiagnosticJSON(writer, status, map[string]interface{}{"status": "ok", "reused": reused, "result": record})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) writeOperationStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(writer, http.StatusNotFound, "operation not found")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrPostCommitDurability):
		writeError(writer, http.StatusInternalServerError, "operation was committed but persistence durability could not be confirmed")
	default:
		writeError(writer, http.StatusInternalServerError, "operation persistence failed")
	}
}

type operationActionPayload struct {
	ApprovalToken string `json:"approval_token,omitempty"`
}

type operationReviewPayload struct {
	Note string `json:"note"`
}

type operationExecutionResponse struct {
	code    int
	status  string
	message string
}

func publicOperationStatusMessage(status model.OperationStatus) string {
	switch status {
	case model.OperationPlanned:
		return "operation is planned"
	case model.OperationRunning:
		return "operation is running"
	case model.OperationBlocked:
		return "operation was blocked by safety checks"
	case model.OperationSucceeded:
		return "operation completed and verified"
	case model.OperationFailed:
		return "operation failed"
	case model.OperationIndeterminate:
		return "operation outcome requires verification"
	case model.OperationUnsupported:
		return "operation is unsupported"
	default:
		return "operation status is unavailable"
	}
}

func publicOperationOutcomeMessage(status model.OperationStatus, failureClass string) string {
	if status != model.OperationIndeterminate {
		return publicOperationStatusMessage(status)
	}
	switch strings.TrimSpace(failureClass) {
	case "rebuild_failed":
		return "former-primary synchronization failed; verify the current primary is reachable and retry recovery"
	case "rewind_unknown":
		return "former-primary synchronization outcome requires verification before retrying recovery"
	case "fenced", "fence_unknown":
		return "former-primary isolation outcome requires verification before recovery can continue"
	case "promoted_unverified":
		return "primary transition completed, but post-operation verification requires review"
	case "verification_failed", "verification_unknown":
		return "operation changed state, but post-operation verification did not pass"
	default:
		return publicOperationStatusMessage(status)
	}
}

func publicCheckMessage(status model.CheckStatus) string {
	switch status {
	case model.CheckPass:
		return "check passed"
	case model.CheckWarn:
		return "check requires review"
	default:
		return "check failed"
	}
}

// List views retain every record, but fetch full execution evidence on expansion.
type operationListItem struct {
	model.ResourceMeta
	Operation      model.Operation        `json:"operation"`
	TargetID       model.ResourceID       `json:"target_id"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Stage          model.WorkflowStage    `json:"stage"`
	Status         model.OperationStatus  `json:"status"`
	Plan           operationListPlan      `json:"plan"`
	Review         *model.OperationReview `json:"review,omitempty"`
	Message        string                 `json:"message,omitempty"`
	Summary        bool                   `json:"summary"`
}

type operationListPlan struct {
	SourceID model.ResourceID `json:"source_id,omitempty"`
	Checks   []model.Check    `json:"checks,omitempty"`
}

func summarizeOperation(record model.OperationRecord) operationListItem {
	public := publicOperationRecord(record)
	item := operationListItem{
		ResourceMeta: public.ResourceMeta, Operation: public.Operation, TargetID: public.TargetID,
		IdempotencyKey: public.IdempotencyKey, Stage: public.Stage, Status: public.Status,
		Plan: operationListPlan{SourceID: public.Plan.SourceID}, Review: public.Review,
		Message: public.Message, Summary: true,
	}
	if item.Message == "" && public.Execution.Message != "" {
		item.Message = public.Execution.Message
	}
	for _, check := range append(public.Plan.Checks, public.Precheck...) {
		if check.Status == model.CheckFail {
			item.Plan.Checks = []model.Check{{Name: check.Name, Status: check.Status, Message: check.Message}}
			break
		}
	}
	return item
}

func publicOperationRecord(record model.OperationRecord) model.OperationRecord {
	if record.Message != "" {
		record.Message = publicOperationOutcomeMessage(record.Status, record.FailureClass)
	}
	if record.Execution.Message != "" {
		record.Execution.Message = publicOperationOutcomeMessage(record.Execution.Status, record.FailureClass)
	}
	record.Precheck = append([]model.Check{}, record.Precheck...)
	for index := range record.Precheck {
		if record.Precheck[index].Message != "" {
			record.Precheck[index].Message = publicCheckMessage(record.Precheck[index].Status)
		}
	}
	record.Plan.Checks = append([]model.Check{}, record.Plan.Checks...)
	for index := range record.Plan.Checks {
		if record.Plan.Checks[index].Message != "" {
			record.Plan.Checks[index].Message = publicCheckMessage(record.Plan.Checks[index].Status)
		}
	}
	record.Verification.Checks = append([]model.Check{}, record.Verification.Checks...)
	for index := range record.Verification.Checks {
		if record.Verification.Checks[index].Message != "" {
			record.Verification.Checks[index].Message = publicCheckMessage(record.Verification.Checks[index].Status)
		}
	}
	record.Attempts = append([]model.StepAttempt{}, record.Attempts...)
	for index := range record.Attempts {
		if record.Attempts[index].Message != "" {
			record.Attempts[index].Message = publicOperationOutcomeMessage(record.Attempts[index].Status, record.Attempts[index].FailureClass)
		}
	}
	if record.Review != nil {
		review := *record.Review
		if review.Note != "" {
			review.Note = "operator review note recorded"
		}
		record.Review = &review
	}
	return record
}

func publicOperationErrorMessage(err error, record model.OperationRecord) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "topology observation") {
		return "topology observation is unavailable or changed"
	}
	switch {
	case record.Status == model.OperationIndeterminate:
		return publicOperationOutcomeMessage(model.OperationIndeterminate, record.FailureClass)
	case errors.Is(err, adapter.ErrUnsupported):
		return publicOperationStatusMessage(model.OperationUnsupported)
	case errors.Is(err, workflow.ErrOperationInProgress):
		return publicOperationStatusMessage(model.OperationRunning)
	case errors.Is(err, workflow.ErrJournalPersistence):
		return "operation state persistence failed"
	case errors.Is(err, store.ErrValidation):
		return "operation request is invalid"
	case errors.Is(err, store.ErrConflict):
		return "operation state conflict"
	default:
		return "operation action failed"
	}
}

func classifyOperationExecution(err error, execution model.Execution, record model.OperationRecord) operationExecutionResponse {
	switch {
	case errors.Is(err, adapter.ErrUnsupported):
		return operationExecutionResponse{code: http.StatusNotImplemented, status: "unsupported", message: publicOperationStatusMessage(model.OperationUnsupported)}
	case errors.Is(err, workflow.ErrOperationInProgress):
		return operationExecutionResponse{code: http.StatusConflict, status: "running", message: publicOperationStatusMessage(model.OperationRunning)}
	case execution.Status == model.OperationIndeterminate || record.Status == model.OperationIndeterminate:
		return operationExecutionResponse{code: http.StatusInternalServerError, status: "indeterminate", message: publicOperationOutcomeMessage(model.OperationIndeterminate, record.FailureClass)}
	case errors.Is(err, workflow.ErrJournalPersistence):
		return operationExecutionResponse{code: http.StatusInternalServerError, status: "error", message: publicOperationErrorMessage(err, record)}
	case err != nil:
		return operationExecutionResponse{code: http.StatusConflict, status: "error", message: publicOperationErrorMessage(err, record)}
	default:
		return operationExecutionResponse{code: http.StatusOK, status: "ok"}
	}
}

func writeOperationExecutionResponse(writer http.ResponseWriter, err error, execution model.Execution, record model.OperationRecord) {
	response := classifyOperationExecution(err, execution, record)
	payload := map[string]interface{}{"status": response.status, "result": publicOperationRecord(record)}
	if response.message != "" {
		payload["message"] = response.message
	}
	writeDiagnosticJSON(writer, response.code, payload)
}

func publicOperationTimeline(timeline store.OperationTimeline) map[string]interface{} {
	audits := append([]model.AuditEvent{}, timeline.Audits...)
	for index := range audits {
		if audits[index].Message != "" {
			audits[index].Message = string(audits[index].Stage) + " event recorded"
		}
	}
	reports := append([]model.Report{}, timeline.Reports...)
	for index := range reports {
		if reports[index].Summary != "" {
			reports[index].Summary = publicOperationStatusMessage(reports[index].Status)
		}
	}
	return map[string]interface{}{"audits": audits, "reports": reports}
}

func (server *Server) operationResourceRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(tail), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(writer, http.StatusNotFound, "operation route not found")
		return
	}
	operationID := model.ResourceID(parts[0])
	if !model.ValidResourceID(operationID) {
		writeError(writer, http.StatusBadRequest, "operation ID must be a platform UUID")
		return
	}
	if len(parts) == 1 {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		timeline, found := server.store.OperationTimeline(operationID)
		if !found {
			writeError(writer, http.StatusNotFound, "operation not found")
			return
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": publicOperationRecord(timeline.Operation), "timeline": publicOperationTimeline(timeline)})
		return
	}
	record, found := server.store.Operation(operationID)
	if !found {
		writeError(writer, http.StatusNotFound, "operation not found")
		return
	}
	if len(parts) != 2 || request.Method != http.MethodPost {
		writeError(writer, http.StatusNotFound, "operation action not found")
		return
	}
	if parts[1] == "review" {
		payload := operationReviewPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid operation review payload")
			return
		}
		actor := "service-api"
		if authentication, authenticated := requestAuthentication(request); authenticated && strings.TrimSpace(authentication.principal.Username) != "" {
			actor = authentication.principal.Username
		}
		updated, err := server.store.ReviewIndeterminateOperation(operationID, record.MetadataRevision, actor, payload.Note)
		if err != nil {
			server.writeOperationStoreError(writer, err)
			return
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": publicOperationRecord(updated)})
		return
	}
	payload := operationActionPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if server.workflow == nil {
		writeError(writer, http.StatusServiceUnavailable, "workflow service is not configured")
		return
	}
	adapterRequest := adapter.OperationRequest{
		Operation: record.Operation, TargetID: record.TargetID, IdempotencyKey: record.IdempotencyKey,
	}
	switch parts[1] {
	case "precheck":
		updated, checks, err := server.workflow.Precheck(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, updated)
			return
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": updated, "checks": checks}})
		return
	case "plan":
		updated, plan, err := server.workflow.Plan(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, updated)
			return
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": updated, "plan": plan}})
		return
	case "verify":
		verification, err := server.workflow.Verify(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, record)
			return
		}
		writeDiagnosticJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": verification})
		return
	case "execute":
	default:
		writeError(writer, http.StatusNotFound, "operation action not found")
		return
	}
	approvalToken := payload.ApprovalToken
	if authentication, authenticated := requestAuthentication(request); authenticated && authentication.viaSession {
		release := server.lockSessionOperationExecution(record.IdempotencyKey)
		defer release()
		planned, token, err := server.issuePlatformSessionOperationApproval(request.Context(), authentication, adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, planned)
			return
		}
		if planned.ResourceID != record.ResourceID {
			server.writeApprovalError(writer, approval.ErrMismatch)
			return
		}
		record = planned
		adapterRequest.Operation = planned.Operation
		adapterRequest.TargetID = planned.TargetID
		adapterRequest.IdempotencyKey = planned.IdempotencyKey
		approvalToken = token
	} else {
		approved, err := server.approvedOperation(request.Context(), approvalToken, record.Operation, record.TargetID)
		if err != nil {
			server.writeApprovalError(writer, err)
			return
		}
		if approved.ResourceID != record.ResourceID {
			server.writeApprovalError(writer, approval.ErrMismatch)
			return
		}
	}
	execution, err := server.workflow.Execute(request.Context(), adapterRequest, approvalToken)
	updated, _ := server.store.Operation(operationID)
	writeOperationExecutionResponse(writer, err, execution, updated)
}

func (server *Server) writeOperationActionError(writer http.ResponseWriter, err error, record model.OperationRecord) {
	publicRecord := publicOperationRecord(record)
	message := publicOperationErrorMessage(err, record)
	switch {
	case record.Status == model.OperationIndeterminate:
		writeDiagnosticJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "indeterminate", "message": message, "result": publicRecord})
	case errors.Is(err, adapter.ErrUnsupported):
		writeDiagnosticJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "message": message, "result": publicRecord})
	case errors.Is(err, workflow.ErrOperationInProgress), errors.Is(err, store.ErrConflict):
		writeDiagnosticJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	case errors.Is(err, store.ErrValidation):
		writeDiagnosticJSON(writer, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	default:
		writeDiagnosticJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	}
}
