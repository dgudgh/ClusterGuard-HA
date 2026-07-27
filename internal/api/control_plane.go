package api

import (
	"context"
	"net/http"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type ControlPlaneStatus struct {
	Mode                    string           `json:"mode"`
	LocalControllerID       model.ResourceID `json:"local_controller_id,omitempty"`
	Role                    string           `json:"role"`
	LeaderID                model.ResourceID `json:"leader_id,omitempty"`
	LeaderAddress           string           `json:"leader_address,omitempty"`
	LeaderAPIAddress        string           `json:"leader_api_address,omitempty"`
	LeaderKnown             bool             `json:"leader_known"`
	VoterCount              int              `json:"voter_count"`
	QuorumConfirmed         bool             `json:"quorum_confirmed"`
	MutationAuthority       bool             `json:"mutation_authority"`
	SnapshotCASActive       bool             `json:"snapshot_cas_active"`
	Term                    uint64           `json:"term"`
	LastIndex               uint64           `json:"last_index"`
	CommitIndex             uint64           `json:"commit_index"`
	AppliedIndex            uint64           `json:"applied_index"`
	StateRevision           uint64           `json:"state_revision"`
	Ready                   bool             `json:"ready"`
	ReadinessReason         string           `json:"readiness_reason"`
	StartedAt               time.Time        `json:"started_at"`
	UptimeSeconds           int64            `json:"uptime_seconds"`
	ClusterCount            int              `json:"cluster_count"`
	ActiveOperations        int              `json:"active_operations"`
	IndeterminateOperations int              `json:"indeterminate_operations"`
	ActiveLifecycleTasks    int              `json:"active_lifecycle_tasks"`
}

type ControlPlaneStatusProvider interface {
	ControlPlaneStatus(context.Context) (ControlPlaneStatus, error)
}

type ControlPlaneStatusProviderFunc func(context.Context) (ControlPlaneStatus, error)

func (provider ControlPlaneStatusProviderFunc) ControlPlaneStatus(ctx context.Context) (ControlPlaneStatus, error) {
	return provider(ctx)
}

func (server *Server) localControlPlaneStatus() ControlPlaneStatus {
	now := time.Now().UTC()
	status := ControlPlaneStatus{
		Mode: "standalone", Role: "standalone", Ready: true, ReadinessReason: "ready",
		StartedAt: server.startedAt, UptimeSeconds: int64(now.Sub(server.startedAt).Seconds()),
	}
	if server.store == nil {
		return status
	}
	status.StateRevision = server.store.StateRevision()
	status.ClusterCount = len(server.store.Clusters())
	for _, operation := range server.store.Operations("") {
		switch operation.Status {
		case model.OperationRunning:
			status.ActiveOperations++
		case model.OperationIndeterminate:
			status.IndeterminateOperations++
		}
	}
	for _, task := range server.store.LifecycleTasks() {
		switch task.Status {
		case lifecycle.TaskPlanned, lifecycle.TaskQueued, lifecycle.TaskRunning, lifecycle.TaskVerifying:
			status.ActiveLifecycleTasks++
		}
	}
	return status
}

func (server *Server) currentControlPlaneStatus(ctx context.Context) (ControlPlaneStatus, error) {
	if server.controlPlane == nil {
		return server.localControlPlaneStatus(), nil
	}
	return server.controlPlane.ControlPlaneStatus(ctx)
}

func (server *Server) healthProbe(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writeProbeJSON(writer, request, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]bool{"live": true}})
}

func (server *Server) readinessProbe(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	status, err := server.currentControlPlaneStatus(ctx)
	if err != nil {
		writeProbeJSON(writer, request, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "error", "result": map[string]interface{}{"ready": false, "reason": "status_unavailable"},
		})
		return
	}
	code := http.StatusOK
	envelopeStatus := "ok"
	if !status.Ready {
		code = http.StatusServiceUnavailable
		envelopeStatus = "error"
	}
	reason := status.ReadinessReason
	if reason == "" {
		reason = "not_ready"
	}
	writeProbeJSON(writer, request, code, map[string]interface{}{
		"status": envelopeStatus, "result": map[string]interface{}{"ready": status.Ready, "reason": reason},
	})
}

func (server *Server) controlPlaneStatusRoute(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	status, err := server.currentControlPlaneStatus(ctx)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "control plane status is temporarily unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": status})
}

func writeProbeJSON(writer http.ResponseWriter, request *http.Request, status int, value interface{}) {
	if request.Method == http.MethodHead {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(status)
		return
	}
	writeJSON(writer, status, value)
}
