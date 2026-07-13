package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type NodeLifecycleManager interface {
	Execute(context.Context, lifecycle.Request, lifecycle.Plan, lifecycle.ExecutionSecrets, string) (lifecycle.Task, error)
}

type LifecycleSecretProvider interface {
	ResolveLifecycleSecrets(context.Context, lifecycle.Request) (lifecycle.ExecutionSecrets, error)
}

type LifecycleSecretProviderFunc func(context.Context, lifecycle.Request) (lifecycle.ExecutionSecrets, error)

func (provider LifecycleSecretProviderFunc) ResolveLifecycleSecrets(ctx context.Context, request lifecycle.Request) (lifecycle.ExecutionSecrets, error) {
	return provider(ctx, request)
}

type nodeSyncPayload struct {
	ClusterID     model.ResourceID     `json:"cluster_id"`
	Action        lifecycle.Action     `json:"action"`
	Targets       []lifecycle.Target   `json:"targets"`
	SyncMethod    lifecycle.SyncMethod `json:"sync_method"`
	RequestedBy   string               `json:"requested_by,omitempty"`
	ApprovalToken string               `json:"approval_token,omitempty"`
}

func (payload nodeSyncPayload) request() lifecycle.Request {
	return lifecycle.Request{
		ClusterID: payload.ClusterID, Action: payload.Action, Targets: append([]lifecycle.Target{}, payload.Targets...),
		SyncMethod: payload.SyncMethod, RequestedBy: strings.TrimSpace(payload.RequestedBy),
	}
}

type nodeSyncVerifyPayload struct {
	TaskID model.ResourceID `json:"task_id"`
}

func primaryLifecycleDonor(topology model.TopologySnapshot) (lifecycle.Donor, error) {
	var primary model.DatabaseInstance
	for _, instance := range topology.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if primary.ResourceID != "" {
			return lifecycle.Donor{}, fmt.Errorf("topology has multiple primary instances")
		}
		primary = instance
	}
	if primary.ResourceID == "" || primary.Health.State != model.HealthHealthy {
		return lifecycle.Donor{}, fmt.Errorf("a unique healthy current primary is required")
	}
	version := strings.TrimSpace(primary.EngineMetadata["version"])
	serverUUID := strings.TrimSpace(primary.EngineIdentity["server_uuid"])
	if version == "" || serverUUID == "" || primary.Port <= 0 {
		return lifecycle.Donor{}, fmt.Errorf("current primary version and native identity are required")
	}
	return lifecycle.Donor{
		InstanceID: primary.ResourceID, Hostname: primary.Hostname, IPAddress: primary.IPAddress,
		Port: primary.Port, ServerUUID: serverUUID, Version: version,
	}, nil
}

func (server *Server) prepareNodeSync(payload nodeSyncPayload) (lifecycle.Request, lifecycle.Plan, error) {
	request := payload.request()
	cluster, found := server.store.Cluster(request.ClusterID)
	if !found || cluster.Engine != model.EngineMySQL {
		return lifecycle.Request{}, lifecycle.Plan{}, fmt.Errorf("registered MySQL cluster is required")
	}
	topology, found := server.store.TopologySnapshot(request.ClusterID)
	if !found || topology.ObservedAt.IsZero() {
		return lifecycle.Request{}, lifecycle.Plan{}, fmt.Errorf("a current topology observation is required")
	}
	donor, err := primaryLifecycleDonor(topology)
	if err != nil {
		return lifecycle.Request{}, lifecycle.Plan{}, err
	}
	request.Donor = donor
	for _, node := range server.store.Nodes() {
		if node.Active && (node.Kind == model.NodeController || node.Kind == model.NodeMixed) {
			request.CurrentControllerCount++
		}
	}
	activeVIPs := 0
	for _, resource := range server.store.HAEndpoints(request.ClusterID) {
		endpoint, endpointFound := server.store.Endpoint(resource.EndpointID)
		if !endpointFound || !endpoint.Active || resource.Kind != model.EndpointVIP {
			continue
		}
		activeVIPs++
		request.VIP = endpoint.IPAddress
	}
	if activeVIPs > 1 {
		return lifecycle.Request{}, lifecycle.Plan{}, fmt.Errorf("cluster has multiple active HA endpoints")
	}
	capabilities := server.lifecycleCap
	capabilities.SourceVersion = donor.Version
	if capabilities.XtraBackupVersions != nil {
		copyVersions := make(map[string]bool, len(capabilities.XtraBackupVersions))
		for version, available := range capabilities.XtraBackupVersions {
			copyVersions[version] = available
		}
		capabilities.XtraBackupVersions = copyVersions
	}
	plan := lifecycle.BuildPlanWithInventory(request, capabilities, server.store.Nodes())
	return request, plan, nil
}

func (server *Server) nodeSyncRoute(writer http.ResponseWriter, request *http.Request, action string) {
	if action == "tasks" || strings.HasPrefix(action, "tasks/") {
		server.nodeSyncTaskRoute(writer, request, strings.TrimPrefix(action, "tasks"))
		return
	}
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if action == "verify" {
		server.verifyNodeSyncTask(writer, request)
		return
	}
	if action != "precheck" && action != "plan" && action != "execute" {
		writeError(writer, http.StatusNotFound, "node synchronization route not found")
		return
	}
	payload := nodeSyncPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid node synchronization payload")
		return
	}
	prepared, plan, err := server.prepareNodeSync(payload)
	if err != nil {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	switch action {
	case "precheck":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"blocked": plan.Blocked, "checks": plan.Checks}})
	case "plan":
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": plan})
	case "execute":
		if plan.Blocked {
			writeJSON(writer, http.StatusConflict, map[string]interface{}{"status": "blocked", "message": "node synchronization plan is blocked", "result": plan})
			return
		}
		if server.lifecycle == nil || server.lifecycleSec == nil {
			writeError(writer, http.StatusServiceUnavailable, "node lifecycle execution is not configured")
			return
		}
		secrets, err := server.lifecycleSec.ResolveLifecycleSecrets(request.Context(), prepared)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "node lifecycle credentials are unavailable")
			return
		}
		task, err := server.lifecycle.Execute(request.Context(), prepared, plan, secrets, payload.ApprovalToken)
		secrets = lifecycle.ExecutionSecrets{}
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, store.ErrValidation) {
				status = http.StatusBadRequest
			} else if errors.Is(err, store.ErrConflict) {
				status = http.StatusConflict
			}
			writeJSON(writer, status, map[string]interface{}{"status": "error", "message": "node lifecycle execution did not complete", "result": task})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": task})
	}
}

func (server *Server) verifyNodeSyncTask(writer http.ResponseWriter, request *http.Request) {
	payload := nodeSyncVerifyPayload{}
	if err := decode(request, &payload); err != nil || !model.ValidResourceID(payload.TaskID) {
		writeError(writer, http.StatusBadRequest, "valid lifecycle task_id is required")
		return
	}
	task, found := server.store.LifecycleTask(payload.TaskID)
	if !found {
		writeError(writer, http.StatusNotFound, "lifecycle task not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"task_id": task.ResourceID, "status": task.Status, "verified": task.Status == lifecycle.TaskSucceeded, "checks": task.Checks, "message": task.Message,
	}})
}

func (server *Server) nodeSyncTaskRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tail = strings.TrimPrefix(tail, "/")
	if tail == "" {
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.store.LifecycleTasks()})
		return
	}
	resourceID := model.ResourceID(tail)
	if strings.Contains(tail, "/") || !model.ValidResourceID(resourceID) {
		writeError(writer, http.StatusNotFound, "lifecycle task not found")
		return
	}
	task, found := server.store.LifecycleTask(resourceID)
	if !found {
		writeError(writer, http.StatusNotFound, "lifecycle task not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": task})
}
