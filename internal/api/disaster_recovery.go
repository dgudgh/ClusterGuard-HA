package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

type DisasterRecoveryManager interface {
	Preflight(context.Context, model.RecoveryTask) error
	Execute(context.Context, model.ResourceID, uint64) (model.RecoveryTask, error)
}

func WithDisasterRecovery(manager DisasterRecoveryManager, run func(func(context.Context))) ServerOption {
	return func(server *Server) { server.disasterRecovery = manager; server.runRecovery = run }
}

func publicRecoveryTask(task model.RecoveryTask) model.RecoveryTask {
	task.RequestedBy = redact.Text(task.RequestedBy)
	task.Message = redact.Text(task.Message)
	members := make([]model.DatabaseInstance, len(task.Members))
	for i, member := range task.Members {
		identity := model.EngineIdentity{}
		for _, key := range []string{"resource_id", "server_uuid", "system_identifier"} {
			if value := member.EngineIdentity[key]; value != "" {
				identity[key] = value
			}
		}
		members[i] = model.DatabaseInstance{ResourceMeta: member.ResourceMeta, ClusterID: member.ClusterID, Engine: member.Engine, EngineIdentity: identity, NodeID: member.NodeID, Hostname: member.Hostname, IPAddress: member.IPAddress, Port: member.Port, Role: member.Role}
	}
	task.Members = members
	task.Events = append([]model.RecoveryEvent{}, task.Events...)
	for i := range task.Events {
		task.Events[i].Message = redact.Text(task.Events[i].Message)
	}
	return task
}

func (server *Server) disasterRecoveryRoute(w http.ResponseWriter, r *http.Request, clusterID model.ResourceID, action string) {
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}
	if cluster.Engine != model.EngineMySQL && cluster.Engine != model.EnginePostgreSQL {
		writeError(w, http.StatusBadRequest, "disaster recovery supports MySQL and PostgreSQL")
		return
	}
	if action == "recovery/status" && r.Method == http.MethodGet {
		tasks := server.store.RecoveryTasks(clusterID)
		if tasks == nil {
			tasks = []model.RecoveryTask{}
		}
		for i := range tasks {
			tasks[i] = publicRecoveryTask(tasks[i])
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"available": server.disasterRecovery != nil && server.runRecovery != nil, "tasks": tasks, "recovery": cluster.Recovery}})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "recovery command requires POST")
		return
	}
	if server.disasterRecovery == nil || server.runRecovery == nil {
		writeError(w, http.StatusServiceUnavailable, "disaster recovery executor is not configured")
		return
	}
	switch action {
	case "recovery/plan":
		actor := "service-api"
		if authentication, ok := requestAuthentication(r); ok {
			actor = authentication.principal.Username
		}
		task, err := server.store.PlanRecovery(r.Context(), clusterID, actor)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		checkCtx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		ready := true
		message := ""
		if err = server.disasterRecovery.Preflight(checkCtx, task); err != nil {
			ready = false
			message = redact.Bounded(err.Error(), 2048)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"task": publicRecoveryTask(task), "ready": ready, "message": message}})
	case "recovery/execute":
		var payload struct {
			TaskID             model.ResourceID `json:"task_id"`
			Revision           uint64           `json:"revision"`
			ConfirmCluster     string           `json:"confirm_cluster"`
			AcknowledgeFencing bool             `json:"acknowledge_fencing"`
		}
		if err := decode(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid recovery confirmation")
			return
		}
		if !payload.AcknowledgeFencing || payload.ConfirmCluster != cluster.DisplayName {
			writeError(w, http.StatusBadRequest, "confirm the exact cluster name and all-member fencing before recovery")
			return
		}
		task, found := server.store.RecoveryTask(payload.TaskID)
		if !found || task.ClusterID != clusterID || task.MetadataRevision != payload.Revision || (task.Stage != model.RecoveryPlanned && task.Stage != model.RecoveryBlocked) {
			writeError(w, http.StatusConflict, "recovery confirmation is stale; reload the recovery plan")
			return
		}
		if _, running := server.recoveryExecutions.LoadOrStore(task.ResourceID, true); running {
			writeError(w, http.StatusConflict, "recovery execution is already starting")
			return
		}
		server.runRecovery(func(ctx context.Context) {
			defer server.recoveryExecutions.Delete(task.ResourceID)
			ctx, cancel := context.WithTimeout(ctx, time.Hour)
			defer cancel()
			_, err := server.disasterRecovery.Execute(ctx, task.ResourceID, payload.Revision)
			if err != nil {
				_ = server.store.RecordRecoveryStartFailure(context.WithoutCancel(ctx), task.ResourceID, payload.Revision, redact.Bounded(err.Error(), 2048))
			}
		})
		writeJSON(w, http.StatusAccepted, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"task_id": task.ResourceID, "message": "recovery submitted; progress is persisted independently of this window"}})
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown recovery action %s", strings.TrimPrefix(action, "recovery/")))
	}
}
