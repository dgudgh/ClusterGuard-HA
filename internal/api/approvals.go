package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type approvalIssuePayload struct {
	ClusterID      model.ResourceID    `json:"cluster_id"`
	Engine         model.Engine        `json:"engine"`
	OperationKind  model.OperationKind `json:"operation_kind"`
	TargetID       model.ResourceID    `json:"target_id"`
	IssuedBy       string              `json:"issued_by"`
	TTLSeconds     int                 `json:"ttl_seconds,omitempty"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
}

func (server *Server) approvalRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	if server.approvals == nil || server.workflow == nil || server.store == nil {
		writeError(writer, http.StatusServiceUnavailable, "approval service is not configured")
		return
	}
	tail = strings.Trim(strings.TrimSpace(tail), "/")
	if tail == "" {
		switch request.Method {
		case http.MethodGet:
			writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.approvals.Grants()})
		case http.MethodPost:
			server.issueApproval(writer, request)
		default:
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resourceID := model.ResourceID(tail)
	if !model.ValidResourceID(resourceID) {
		writeError(writer, http.StatusBadRequest, "approval grant ID must be a platform UUID")
		return
	}
	grant, found := server.approvals.Grant(resourceID)
	if !found {
		writeError(writer, http.StatusNotFound, "approval grant not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": grant})
}

func (server *Server) issueApproval(writer http.ResponseWriter, request *http.Request) {
	payload := approvalIssuePayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	payload.IssuedBy = strings.TrimSpace(payload.IssuedBy)
	if !model.ValidResourceID(payload.ClusterID) || !model.ValidResourceID(payload.TargetID) ||
		!payload.Engine.Valid() || payload.OperationKind == "" || payload.IssuedBy == "" {
		writeError(writer, http.StatusBadRequest, "approval cluster, engine, operation kind, target, and issuer are required")
		return
	}
	key := strings.TrimSpace(payload.IdempotencyKey)
	if key == "" {
		key = "approval-" + string(model.NewResourceID())
	}
	record, _, err := server.workflow.Plan(request.Context(), adapter.OperationRequest{
		Operation: model.Operation{
			ClusterID: payload.ClusterID, Engine: payload.Engine,
			Kind: payload.OperationKind, RequestedBy: payload.IssuedBy,
		},
		TargetID: payload.TargetID, IdempotencyKey: key,
	})
	if err != nil {
		server.writeOperationActionError(writer, err, record)
		return
	}
	issued, err := server.approvals.Issue(request.Context(), approval.IssueRequest{
		Operation: record,
		IssuedBy:  payload.IssuedBy,
		TTL:       time.Duration(payload.TTLSeconds) * time.Second,
	})
	if err != nil {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"operation": record, "grant": issued.Grant, "approval_token": issued.Token,
	}})
}

func (server *Server) writeApprovalError(writer http.ResponseWriter, err error) {
	code := http.StatusConflict
	if errors.Is(err, approval.ErrRequired) || errors.Is(err, approval.ErrInvalid) {
		code = http.StatusUnauthorized
		writer.Header().Set("WWW-Authenticate", `Bearer realm="clusterguard-approval"`)
	}
	writeJSON(writer, code, map[string]interface{}{"status": "blocked", "message": err.Error()})
}
