package api

import (
	"errors"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

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
			writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": publicOperationRecord(record)})
			return
		}
		clusterID := model.ResourceID(strings.TrimSpace(query.Get("cluster_id")))
		if clusterID != "" && !model.ValidResourceID(clusterID) {
			writeError(writer, http.StatusBadRequest, "cluster_id must be a platform UUID")
			return
		}
		operations := server.store.Operations(clusterID)
		for index := range operations {
			operations[index] = publicOperationRecord(operations[index])
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": operations})
	case http.MethodPost:
		payload := operationPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
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
		writeJSON(writer, status, map[string]interface{}{"status": "ok", "reused": reused, "result": record})
	default:
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (server *Server) writeOperationStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, err.Error())
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

func publicOperationRecord(record model.OperationRecord) model.OperationRecord {
	if record.Message != "" {
		record.Message = publicOperationStatusMessage(record.Status)
	}
	if record.Execution.Message != "" {
		record.Execution.Message = publicOperationStatusMessage(record.Execution.Status)
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
			record.Attempts[index].Message = publicOperationStatusMessage(record.Attempts[index].Status)
		}
	}
	return record
}

func publicOperationErrorMessage(err error, record model.OperationRecord) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "topology observation") {
		return "topology observation is unavailable or changed"
	}
	switch {
	case record.Status == model.OperationIndeterminate:
		return publicOperationStatusMessage(model.OperationIndeterminate)
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
		return operationExecutionResponse{code: http.StatusInternalServerError, status: "indeterminate", message: publicOperationStatusMessage(model.OperationIndeterminate)}
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
	writeJSON(writer, response.code, payload)
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
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": publicOperationRecord(timeline.Operation), "timeline": publicOperationTimeline(timeline)})
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
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": updated, "checks": checks}})
		return
	case "plan":
		updated, plan, err := server.workflow.Plan(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, updated)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": updated, "plan": plan}})
		return
	case "verify":
		verification, err := server.workflow.Verify(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, record)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": verification})
		return
	case "execute":
	default:
		writeError(writer, http.StatusNotFound, "operation action not found")
		return
	}
	execution, err := server.workflow.Execute(request.Context(), adapterRequest, payload.ApprovalToken)
	updated, _ := server.store.Operation(operationID)
	writeOperationExecutionResponse(writer, err, execution, updated)
}

func (server *Server) writeOperationActionError(writer http.ResponseWriter, err error, record model.OperationRecord) {
	publicRecord := publicOperationRecord(record)
	message := publicOperationErrorMessage(err, record)
	switch {
	case record.Status == model.OperationIndeterminate:
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "indeterminate", "message": message, "result": publicRecord})
	case errors.Is(err, adapter.ErrUnsupported):
		writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "message": message, "result": publicRecord})
	case errors.Is(err, workflow.ErrOperationInProgress), errors.Is(err, store.ErrConflict):
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	case errors.Is(err, store.ErrValidation):
		writeJSON(writer, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	default:
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": message, "result": publicRecord})
	}
}
