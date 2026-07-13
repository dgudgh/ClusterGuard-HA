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
		clusterID := model.ResourceID(strings.TrimSpace(request.URL.Query().Get("cluster_id")))
		if clusterID != "" && !model.ValidResourceID(clusterID) {
			writeError(writer, http.StatusBadRequest, "cluster_id must be a platform UUID")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Operations(clusterID)})
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

func (server *Server) operationTimeline(operationID model.ResourceID) map[string]interface{} {
	audits := make([]model.AuditEvent, 0)
	for _, event := range server.store.Audits() {
		if event.OperationID == operationID {
			audits = append(audits, event)
		}
	}
	reports := make([]model.Report, 0)
	for _, report := range server.store.Reports() {
		if report.OperationID == operationID {
			reports = append(reports, report)
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
	record, found := server.store.Operation(operationID)
	if !found {
		writeError(writer, http.StatusNotFound, "operation not found")
		return
	}
	if len(parts) == 1 {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": record, "timeline": server.operationTimeline(operationID)})
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
	switch {
	case errors.Is(err, adapter.ErrUnsupported):
		writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "message": execution.Message, "result": updated})
	case errors.Is(err, workflow.ErrOperationInProgress):
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "running", "message": err.Error(), "result": updated})
	case errors.Is(err, workflow.ErrJournalPersistence):
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": "workflow journal persistence failed", "result": updated})
	case err != nil && updated.Status == model.OperationIndeterminate:
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "indeterminate", "message": err.Error(), "result": updated})
	case err != nil:
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": err.Error(), "result": updated})
	default:
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": updated})
	}
}

func (server *Server) writeOperationActionError(writer http.ResponseWriter, err error, record model.OperationRecord) {
	switch {
	case record.Status == model.OperationIndeterminate:
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "indeterminate", "message": err.Error(), "result": record})
	case errors.Is(err, adapter.ErrUnsupported):
		writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "message": err.Error(), "result": record})
	case errors.Is(err, workflow.ErrOperationInProgress), errors.Is(err, store.ErrConflict):
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": err.Error(), "result": record})
	case errors.Is(err, store.ErrValidation):
		writeJSON(writer, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": err.Error(), "result": record})
	default:
		writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "message": err.Error(), "result": record})
	}
}
