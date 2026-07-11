package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

const maximumJSONBodyBytes = 1 << 20

type Server struct {
	registry     *adapter.Registry
	store        *store.Repository
	workflow     *workflow.Service
	refresher    Refresher
	controlToken string
}

type Refresher interface {
	Refresh(context.Context, model.ResourceID) (model.TopologySnapshot, error)
}

type ServerOption func(*Server)

func WithControlToken(token string) ServerOption {
	return func(server *Server) { server.controlToken = strings.TrimSpace(token) }
}

func NewServer(registry *adapter.Registry, repository *store.Repository, service *workflow.Service, refresher Refresher, options ...ServerOption) *Server {
	server := &Server{registry: registry, store: repository, workflow: service, refresher: refresher}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	return server
}

func writeJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]interface{}{"status": "error", "message": message})
}

func decode(request *http.Request, value interface{}) error {
	contents, err := io.ReadAll(io.LimitReader(request.Body, maximumJSONBodyBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maximumJSONBodyBytes {
		return errors.New("request body exceeds maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(server.route)
}

func (server *Server) route(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimSuffix(request.URL.Path, "/")
	if request.Method == http.MethodPost && strings.HasPrefix(path, "/api/v1/") && !server.authorizeControl(writer, request) {
		return
	}
	switch {
	case (request.Method == http.MethodGet || request.Method == http.MethodHead) && (path == "" || path == "/"):
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(consoleHTML)
		}
	case request.Method == http.MethodGet && path == "/api/v1/engines":
		server.engines(writer)
	case request.Method == http.MethodGet && path == "/api/v1/capabilities":
		server.capabilities(writer)
	case request.Method == http.MethodGet && path == "/api/v1/clusters":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Clusters()})
	case request.Method == http.MethodPost && path == "/api/v1/clusters":
		server.registerCluster(writer, request)
	case request.Method == http.MethodGet && path == "/api/v1/metadata/anomalies":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Anomalies()})
	case strings.HasPrefix(path, "/api/v1/clusters/"):
		server.clusterRoute(writer, request, strings.TrimPrefix(path, "/api/v1/clusters/"))
	case strings.HasPrefix(path, "/api/v1/operations/"):
		server.operationRoute(writer, request, strings.TrimPrefix(path, "/api/v1/operations/"))
	case strings.HasPrefix(path, "/api/v1/nodes/sync/"):
		server.unsupported(writer, "node synchronization is not implemented in phase one")
	case strings.HasPrefix(path, "/api/v1/metadata/reconcile/"):
		server.metadataRoute(writer, request, strings.TrimPrefix(path, "/api/v1/metadata/reconcile/"))
	default:
		writeError(writer, http.StatusNotFound, "route not found")
	}
}

func (server *Server) authorizeControl(writer http.ResponseWriter, request *http.Request) bool {
	if server.controlToken == "" {
		writeError(writer, http.StatusServiceUnavailable, "control API authentication is not configured")
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(server.controlToken)) != 1 {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="clusterguard-control"`)
		writeError(writer, http.StatusUnauthorized, "valid control token is required")
		return false
	}
	return true
}

func (server *Server) engines(writer http.ResponseWriter) {
	result := make([]adapter.Capabilities, 0, len(server.registry.Engines()))
	for _, engine := range server.registry.Engines() {
		candidate, _ := server.registry.Get(engine)
		result = append(result, candidate.Capabilities(context.Background()))
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": result})
}

func (server *Server) capabilities(writer http.ResponseWriter) {
	server.engines(writer)
}

type operationPayload struct {
	Operation     model.Operation   `json:"operation"`
	TargetID      model.ResourceID  `json:"target_id,omitempty"`
	Parameters    map[string]string `json:"parameters,omitempty"`
	ApprovalToken string            `json:"approval_token,omitempty"`
}

func (server *Server) operationRoute(writer http.ResponseWriter, request *http.Request, action string) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := operationPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	candidate, ok := server.registry.Get(payload.Operation.Engine)
	if !ok {
		server.unsupported(writer, "adapter is not registered for this engine")
		return
	}
	adapterRequest := adapter.OperationRequest{Operation: payload.Operation, TargetID: payload.TargetID, Parameters: payload.Parameters}
	switch action {
	case "precheck":
		if !candidate.Capabilities(request.Context()).Supports(adapter.CapabilityPrecheck) {
			server.unsupported(writer, "operation precheck is unsupported")
			return
		}
		checks, err := candidate.Precheck(request.Context(), adapterRequest)
		if err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": checks})
	case "plan":
		if !candidate.Capabilities(request.Context()).Supports(adapter.CapabilityPlan) {
			server.unsupported(writer, "operation planning is unsupported")
			return
		}
		plan, err := candidate.BuildPlan(request.Context(), adapterRequest)
		if err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": plan})
	case "execute":
		execution, err := server.workflow.Execute(request.Context(), adapterRequest, payload.ApprovalToken)
		if errors.Is(err, adapter.ErrUnsupported) {
			writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "result": execution, "message": execution.Message})
			return
		}
		if errors.Is(err, workflow.ErrJournalPersistence) {
			writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{"status": "error", "result": execution, "message": "workflow journal persistence failed"})
			return
		}
		if err != nil {
			writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "error", "result": execution, "message": err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": execution})
	case "verify":
		if !candidate.Capabilities(request.Context()).Supports(adapter.CapabilityVerify) {
			server.unsupported(writer, "operation verification is unsupported")
			return
		}
		verification, err := candidate.Verify(request.Context(), adapterRequest)
		if err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": verification})
	default:
		writeError(writer, http.StatusNotFound, "operation route not found")
	}
}

type metadataPayload struct {
	Operation     model.Operation        `json:"operation"`
	Instance      model.DatabaseInstance `json:"instance"`
	EndpointID    model.ResourceID       `json:"endpoint_id,omitempty"`
	ApprovalToken string                 `json:"approval_token,omitempty"`
}

func (server *Server) metadataRoute(writer http.ResponseWriter, request *http.Request, action string) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := metadataPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.validateMetadataPayload(payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid metadata reconciliation target")
		return
	}
	candidate, ok := server.registry.Get(payload.Instance.Engine)
	if !ok || !candidate.Capabilities(request.Context()).Supports(adapter.CapabilityMetadataReconcile) {
		server.unsupported(writer, "metadata reconciliation is unsupported for this engine")
		return
	}
	metadataRequest := adapter.MetadataRequest{ClusterID: payload.Instance.ClusterID, Instance: payload.Instance}
	switch action {
	case "precheck":
		checks, err := candidate.MetadataPrecheck(request.Context(), metadataRequest)
		if err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": checks})
	case "plan":
		plan, err := candidate.ReconcileMetadata(request.Context(), metadataRequest)
		if err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": plan})
	case "execute":
		var reconciled model.DatabaseInstance
		var endpoint model.Endpoint
		var commitErr error
		execution, err := server.workflow.ExecuteMetadata(request.Context(), payload.Operation, metadataRequest, payload.ApprovalToken, func() error {
			reconciled, endpoint, commitErr = server.store.ReconcileMetadataCoordinates(store.MetadataCoordinates{Instance: payload.Instance, EndpointID: payload.EndpointID})
			return commitErr
		})
		if errors.Is(err, adapter.ErrUnsupported) {
			writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "result": execution, "message": execution.Message})
			return
		}
		if commitErr != nil {
			switch {
			case errors.Is(commitErr, store.ErrValidation):
				writeError(writer, http.StatusBadRequest, "invalid metadata reconciliation")
			case errors.Is(commitErr, store.ErrConflict):
				writeError(writer, http.StatusConflict, "metadata reconciliation conflicts with inventory")
			default:
				writeError(writer, http.StatusInternalServerError, "metadata reconciliation persistence failed")
			}
			return
		}
		if err != nil {
			if errors.Is(err, workflow.ErrJournalPersistence) {
				writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{
					"status": "error", "message": "workflow journal persistence failed",
					"result": map[string]interface{}{"execution": execution, "reconciled": reconciled, "endpoint": endpoint},
				})
			} else {
				writeError(writer, http.StatusConflict, "metadata reconciliation failed")
			}
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"execution": execution, "reconciled": reconciled, "endpoint": endpoint}})
	case "verify":
		if _, err := candidate.ReconcileMetadata(request.Context(), metadataRequest); err != nil {
			writeError(writer, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": "metadata identity is valid"})
	default:
		writeError(writer, http.StatusNotFound, "metadata route not found")
	}
}

func (server *Server) validateMetadataPayload(payload metadataPayload) error {
	if strings.TrimSpace(payload.Instance.Hostname) == "" && strings.TrimSpace(payload.Instance.IPAddress) == "" {
		return errors.New("active database endpoint address is required")
	}
	canonical, found := server.store.Instance(payload.Instance.ResourceID)
	if !found {
		return errors.New("unknown metadata instance")
	}
	cluster, found := server.store.Cluster(canonical.ClusterID)
	if !found || canonical.Engine != cluster.Engine || payload.Instance.ClusterID != canonical.ClusterID || payload.Instance.Engine != canonical.Engine {
		return errors.New("metadata engine does not match canonical inventory")
	}
	if payload.Operation.Engine != "" && payload.Operation.Engine != canonical.Engine {
		return errors.New("operation engine does not match canonical inventory")
	}
	if payload.Operation.ClusterID != "" && payload.Operation.ClusterID != canonical.ClusterID {
		return errors.New("operation cluster does not match canonical inventory")
	}
	canonicalKey, err := identity.InstanceKey(canonical.Engine, canonical.EngineIdentity)
	if err != nil {
		return err
	}
	payloadKey, err := identity.InstanceKey(payload.Instance.Engine, payload.Instance.EngineIdentity)
	if err != nil || payloadKey != canonicalKey {
		return errors.New("metadata native identity does not match canonical inventory")
	}
	return nil
}

func (server *Server) unsupported(writer http.ResponseWriter, message string) {
	writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "message": message})
}
