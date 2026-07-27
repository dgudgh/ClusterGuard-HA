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
	"time"

	"clusterguard.io/ha/internal/approval"
	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

const maximumJSONBodyBytes = 1 << 20

const requestIDHeader = "X-Request-ID"

type Server struct {
	registry       *adapter.Registry
	store          *store.Repository
	workflow       *workflow.Service
	approvals      *approval.Service
	authentication *platformauth.Service
	refresher      Refresher
	controlToken   string
	monitorToken   string
	agentSecret    string
	lifecycle      NodeLifecycleManager
	lifecycleCap   lifecycle.Capabilities
	lifecycleSec   LifecycleSecretProvider
	authority      MutationAuthority
	mutationRPC    MutationRPC
	secureCookies  bool
	controlPlane   ControlPlaneStatusProvider
	startedAt      time.Time
}

type Refresher interface {
	Refresh(context.Context, model.ResourceID) (model.TopologySnapshot, error)
}

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type LeaderLocator interface {
	Leader() (model.ResourceID, string, bool)
}

type LeaderAPILocator interface {
	LeaderAPIAddress(model.ResourceID) (string, bool)
}

type ServerOption func(*Server)

func WithControlToken(token string) ServerOption {
	return func(server *Server) { server.controlToken = strings.TrimSpace(token) }
}

func WithApprovalService(service *approval.Service) ServerOption {
	return func(server *Server) { server.approvals = service }
}

func WithAuthentication(service *platformauth.Service) ServerOption {
	return func(server *Server) { server.authentication = service }
}

func WithMonitoringToken(token string) ServerOption {
	return func(server *Server) { server.monitorToken = strings.TrimSpace(token) }
}

func WithAgentReconcileSecret(secret string) ServerOption {
	return func(server *Server) { server.agentSecret = strings.TrimSpace(secret) }
}

func WithNodeLifecycle(manager NodeLifecycleManager, capabilities lifecycle.Capabilities, secrets LifecycleSecretProvider) ServerOption {
	return func(server *Server) {
		server.lifecycle = manager
		server.lifecycleCap = capabilities
		server.lifecycleSec = secrets
	}
}

func WithMutationAuthority(authority MutationAuthority) ServerOption {
	return func(server *Server) { server.authority = authority }
}

func WithMutationRPC(client MutationRPC) ServerOption {
	return func(server *Server) { server.mutationRPC = client }
}

func WithSecureCookies(enabled bool) ServerOption {
	return func(server *Server) { server.secureCookies = enabled }
}

func WithControlPlaneStatus(provider ControlPlaneStatusProvider) ServerOption {
	return func(server *Server) { server.controlPlane = provider }
}

func NewServer(registry *adapter.Registry, repository *store.Repository, service *workflow.Service, refresher Refresher, options ...ServerOption) *Server {
	server := &Server{registry: registry, store: repository, workflow: service, refresher: refresher, startedAt: time.Now().UTC()}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	return server
}

func writeJSON(writer http.ResponseWriter, status int, value interface{}) {
	if status >= http.StatusBadRequest {
		if source, ok := value.(map[string]interface{}); ok {
			copy := make(map[string]interface{}, len(source)+1)
			for key, item := range source {
				copy[key] = item
			}
			if requestID := writer.Header().Get(requestIDHeader); requestID != "" {
				copy["request_id"] = requestID
			}
			value = copy
		}
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]interface{}{"status": "error", "message": message})
}

func decode(request *http.Request, value interface{}) error {
	contents, err := readJSONBody(request)
	if err != nil {
		return err
	}
	return decodeJSON(contents, value)
}

func decodePreservingBody(request *http.Request, value interface{}) error {
	contents, err := readJSONBody(request)
	if err != nil {
		return err
	}
	request.Body = io.NopCloser(bytes.NewReader(contents))
	request.ContentLength = int64(len(contents))
	return decodeJSON(contents, value)
}

func readJSONBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, errors.New("request body is required")
	}
	contents, err := io.ReadAll(io.LimitReader(request.Body, maximumJSONBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumJSONBodyBytes {
		return nil, errors.New("request body exceeds maximum size")
	}
	return contents, nil
}

func decodeJSON(contents []byte, value interface{}) error {
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
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set(requestIDHeader, requestID(request.Header.Get(requestIDHeader)))
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if server.requestIsSecure(request) {
			writer.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		server.route(writer, request)
	})
}

func (server *Server) route(writer http.ResponseWriter, request *http.Request) {
	if server.store != nil && mutatingMethod(request.Method) && strings.TrimSpace(request.Header.Get(mutationRPCForwardedHeader)) != "" {
		writer = &mutationRPCRevisionWriter{ResponseWriter: writer, revisions: server.store}
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if path == "/healthz" || path == "/readyz" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if path == "/healthz" {
			server.healthProbe(writer, request)
		} else {
			server.readinessProbe(writer, request)
		}
		return
	}
	if path == "/api/v1/agent/reconcile" {
		server.agentReconcileRoute(writer, request)
		return
	}
	if strings.HasPrefix(path, "/api/v1/monitoring/") {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !server.authorizeMonitoring(writer, request) {
			return
		}
		server.monitoringRoute(writer, strings.TrimPrefix(path, "/api/v1/monitoring/"))
		return
	}
	if path == "/api/v1/auth/login" || path == "/api/v1/auth/me" ||
		path == "/api/v1/auth/logout" || path == "/api/v1/auth/password" {
		server.authRoute(writer, request, path)
		return
	}
	if server.authentication != nil && strings.HasPrefix(path, "/api/v1/") {
		authenticatedRequest, ok := server.authenticatePlatformRequest(writer, request)
		if !ok {
			return
		}
		request = authenticatedRequest
	}
	if mutatingMethod(request.Method) && strings.HasPrefix(path, "/api/v1/") {
		if authentication, authenticated := requestAuthentication(request); authenticated && authentication.viaSession {
			if !server.authorizeSessionMutation(writer, request, authentication.principal, path) {
				return
			}
		} else {
			if !manualGrantExecutionRoute(request.Method, path) && !server.authorizeControl(writer, request) {
				return
			}
		}
		if !server.authorizeMutation(writer, request) {
			return
		}
	}
	switch {
	case (request.Method == http.MethodGet || request.Method == http.MethodHead) && path == "/":
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(consoleHTML)
		}
	case request.Method == http.MethodGet && path == "/api/v1/engines":
		server.engines(writer)
	case request.Method == http.MethodGet && path == "/api/v1/capabilities":
		server.capabilities(writer)
	case request.Method == http.MethodGet && path == "/api/v1/control-plane/status":
		server.controlPlaneStatusRoute(writer, request)
	case request.Method == http.MethodGet && path == "/api/v1/clusters":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Clusters()})
	case request.Method == http.MethodPost && path == "/api/v1/clusters":
		server.registerCluster(writer, request)
	case path == "/api/v1/nodes":
		server.nodesCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/nodes/") && !strings.HasPrefix(path, "/api/v1/nodes/sync/"):
		server.nodeResource(writer, request, strings.TrimPrefix(path, "/api/v1/nodes/"))
	case request.Method == http.MethodGet && path == "/api/v1/metadata/anomalies":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.Anomalies()})
	case path == "/api/v1/approvals" || strings.HasPrefix(path, "/api/v1/approvals/"):
		server.approvalRoute(writer, request, strings.TrimPrefix(path, "/api/v1/approvals"))
	case request.Method == http.MethodGet && (path == "/api/v1/reports" || strings.HasPrefix(path, "/api/v1/reports/")):
		server.reportRoute(writer, request, strings.TrimPrefix(path, "/api/v1/reports"))
	case path == "/api/v1/operations":
		server.operationsCollection(writer, request)
	case strings.HasPrefix(path, "/api/v1/clusters/"):
		server.clusterRoute(writer, request, strings.TrimPrefix(path, "/api/v1/clusters/"))
	case strings.HasPrefix(path, "/api/v1/operations/"):
		tail := strings.TrimPrefix(path, "/api/v1/operations/")
		switch tail {
		case "precheck", "plan", "execute", "verify":
			server.operationRoute(writer, request, tail)
		default:
			server.operationResourceRoute(writer, request, tail)
		}
	case strings.HasPrefix(path, "/api/v1/nodes/sync/"):
		server.nodeSyncRoute(writer, request, strings.TrimPrefix(path, "/api/v1/nodes/sync/"))
	case strings.HasPrefix(path, "/api/v1/metadata/reconcile/"):
		server.metadataRoute(writer, request, strings.TrimPrefix(path, "/api/v1/metadata/reconcile/"))
	default:
		writeError(writer, http.StatusNotFound, "route not found")
	}
}

func requestID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 128 {
		valid := true
		for index := 0; index < len(value); index++ {
			character := value[index]
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || strings.ContainsRune("._:-", rune(character))) {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return string(model.NewResourceID())
}

func mutatingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func manualGrantExecutionRoute(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if path == "/api/v1/operations/execute" {
		return true
	}
	return strings.HasPrefix(path, "/api/v1/operations/") && strings.HasSuffix(path, "/execute")
}

func (server *Server) authorizeControl(writer http.ResponseWriter, request *http.Request) bool {
	if server.controlToken == "" {
		writeError(writer, http.StatusServiceUnavailable, "control API authentication is not configured")
		return false
	}
	if !server.validControlBearer(request) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="clusterguard-control"`)
		writeError(writer, http.StatusUnauthorized, "valid control token is required")
		return false
	}
	return true
}

func (server *Server) validControlBearer(request *http.Request) bool {
	if server.controlToken == "" {
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	return found && strings.EqualFold(scheme, "Bearer") &&
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(server.controlToken)) == 1
}

func (server *Server) authorizeMonitoring(writer http.ResponseWriter, request *http.Request) bool {
	if server.monitorToken == "" {
		writeError(writer, http.StatusServiceUnavailable, "monitoring API authentication is not configured")
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(server.monitorToken)) != 1 {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="clusterguard-monitoring"`)
		writeError(writer, http.StatusUnauthorized, "valid monitoring token is required")
		return false
	}
	return true
}

func (server *Server) authorizeMutation(writer http.ResponseWriter, request *http.Request) bool {
	if server.authority == nil {
		return true
	}
	if err := server.authority.RequireMutationAuthority(request.Context()); err == nil {
		return true
	}
	result := map[string]interface{}{}
	leaderAPIAddress := ""
	if locator, ok := server.authority.(LeaderLocator); ok {
		if leaderID, address, found := locator.Leader(); found {
			writer.Header().Set("X-ClusterGuard-Leader-ID", string(leaderID))
			writer.Header().Set("X-ClusterGuard-Leader-Address", address)
			result["leader_id"] = leaderID
			result["leader_address"] = address
			if apiLocator, ok := server.authority.(LeaderAPILocator); ok {
				if apiAddress, found := apiLocator.LeaderAPIAddress(leaderID); found {
					leaderAPIAddress = strings.TrimRight(strings.TrimSpace(apiAddress), "/")
					writer.Header().Set("X-ClusterGuard-Leader-API-Address", leaderAPIAddress)
					result["leader_api_address"] = leaderAPIAddress
				}
			}
		}
	}
	if server.mutationRPC != nil && leaderAPIAddress != "" && strings.TrimSpace(request.Header.Get(mutationRPCForwardedHeader)) == "" {
		if err := server.mutationRPC.Forward(writer, request, leaderAPIAddress); err == nil {
			return false
		}
		writeJSON(writer, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "blocked", "message": "current Raft leader is temporarily unreachable", "result": result,
		})
		return false
	}
	writeJSON(writer, http.StatusServiceUnavailable, map[string]interface{}{
		"status": "blocked", "message": "mutation requires the current Raft leader with controller quorum", "result": result,
	})
	return false
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
	Operation      model.Operation   `json:"operation"`
	TargetID       model.ResourceID  `json:"target_id,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Parameters     map[string]string `json:"parameters,omitempty"`
	ApprovalToken  string            `json:"approval_token,omitempty"`
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
	_, ok := server.registry.Get(payload.Operation.Engine)
	if !ok {
		server.unsupported(writer, "adapter is not registered for this engine")
		return
	}
	if strings.TrimSpace(payload.IdempotencyKey) == "" {
		writeError(writer, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	if server.workflow == nil {
		writeError(writer, http.StatusServiceUnavailable, "workflow service is not configured")
		return
	}
	authentication, platformSession := requestAuthentication(request)
	if platformSession && authentication.viaSession {
		payload.Operation.RequestedBy = authentication.principal.Username
	}
	adapterRequest := adapter.OperationRequest{
		Operation: payload.Operation, TargetID: payload.TargetID,
		IdempotencyKey: payload.IdempotencyKey, Parameters: payload.Parameters,
	}
	approvalToken := payload.ApprovalToken
	if action == "execute" {
		if platformSession && authentication.viaSession {
			record, token, err := server.issuePlatformSessionOperationApproval(request.Context(), authentication, adapterRequest)
			if err != nil {
				server.writeOperationActionError(writer, err, record)
				return
			}
			adapterRequest.Operation = record.Operation
			adapterRequest.TargetID = record.TargetID
			adapterRequest.IdempotencyKey = record.IdempotencyKey
			approvalToken = token
		} else {
			record, err := server.approvedOperation(request.Context(), approvalToken, payload.Operation, payload.TargetID)
			if err != nil {
				server.writeApprovalError(writer, err)
				return
			}
			adapterRequest.Operation = record.Operation
			adapterRequest.TargetID = record.TargetID
			adapterRequest.IdempotencyKey = record.IdempotencyKey
		}
	}
	switch action {
	case "precheck":
		record, checks, err := server.workflow.Precheck(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, record)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": record, "checks": checks}})
	case "plan":
		record, plan, err := server.workflow.Plan(request.Context(), adapterRequest)
		if err != nil {
			server.writeOperationActionError(writer, err, record)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"operation": record, "plan": plan}})
	case "execute":
		execution, err := server.workflow.Execute(request.Context(), adapterRequest, approvalToken)
		record, _ := server.store.OperationByIdempotencyKey(adapterRequest.IdempotencyKey)
		writeOperationExecutionResponse(writer, err, execution, record)
	case "verify":
		verification, err := server.workflow.Verify(request.Context(), adapterRequest)
		if err != nil {
			record, _ := server.store.OperationByIdempotencyKey(payload.IdempotencyKey)
			server.writeOperationActionError(writer, err, record)
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
	authentication, platformSession := requestAuthentication(request)
	if platformSession && authentication.viaSession {
		payload.Operation.RequestedBy = authentication.principal.Username
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
		commit := func() error {
			reconciled, endpoint, commitErr = server.store.ReconcileMetadataCoordinates(store.MetadataCoordinates{Instance: payload.Instance, EndpointID: payload.EndpointID})
			return commitErr
		}
		var execution model.Execution
		var err error
		if platformSession && authentication.viaSession {
			execution, err = server.workflow.ExecuteMetadataAuthorized(
				request.Context(),
				payload.Operation,
				metadataRequest,
				authentication.principal.Username,
				commit,
			)
		} else {
			execution, err = server.workflow.ExecuteMetadata(request.Context(), payload.Operation, metadataRequest, payload.ApprovalToken, commit)
		}
		if errors.Is(err, adapter.ErrUnsupported) {
			writeJSON(writer, http.StatusNotImplemented, map[string]interface{}{"status": "unsupported", "result": execution, "message": execution.Message})
			return
		}
		if writeMetadataExecutionFailure(writer, execution, reconciled, endpoint, commitErr, err) {
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

func writeMetadataExecutionFailure(writer http.ResponseWriter, execution model.Execution, reconciled model.DatabaseInstance, endpoint model.Endpoint, commitErr error, executionErr error) bool {
	result := map[string]interface{}{"execution": execution, "reconciled": reconciled, "endpoint": endpoint}
	if commitErr != nil {
		switch {
		case errors.Is(commitErr, store.ErrPostCommitDurability):
			writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{
				"status": "error", "message": "metadata reconciliation committed with durability warning", "result": result,
			})
		case errors.Is(commitErr, store.ErrValidation):
			writeError(writer, http.StatusBadRequest, "invalid metadata reconciliation")
		case errors.Is(commitErr, store.ErrConflict):
			writeError(writer, http.StatusConflict, "metadata reconciliation conflicts with inventory")
		default:
			writeError(writer, http.StatusInternalServerError, "metadata reconciliation persistence failed")
		}
		return true
	}
	if executionErr == nil {
		return false
	}
	if errors.Is(executionErr, workflow.ErrJournalPersistence) {
		writeJSON(writer, http.StatusInternalServerError, map[string]interface{}{
			"status": "error", "message": "workflow journal persistence failed", "result": result,
		})
	} else {
		writeError(writer, http.StatusConflict, "metadata reconciliation failed")
	}
	return true
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
