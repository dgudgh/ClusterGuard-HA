package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"clusterguard.io/ha/internal/buildinfo"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type ControlPlaneStatus struct {
	Mode                           string                   `json:"mode"`
	LocalControllerID              model.ResourceID         `json:"local_controller_id,omitempty"`
	Role                           string                   `json:"role"`
	LeaderID                       model.ResourceID         `json:"leader_id,omitempty"`
	LeaderAddress                  string                   `json:"leader_address,omitempty"`
	LeaderAPIAddress               string                   `json:"leader_api_address,omitempty"`
	LeaderKnown                    bool                     `json:"leader_known"`
	VoterCount                     int                      `json:"voter_count"`
	QuorumConfirmed                bool                     `json:"quorum_confirmed"`
	MutationAuthority              bool                     `json:"mutation_authority"`
	SnapshotCASActive              bool                     `json:"snapshot_cas_active"`
	ReplicatedLogCompressionActive bool                     `json:"replicated_log_compression_active"`
	Term                           uint64                   `json:"term"`
	LastIndex                      uint64                   `json:"last_index"`
	CommitIndex                    uint64                   `json:"commit_index"`
	AppliedIndex                   uint64                   `json:"applied_index"`
	StateRevision                  uint64                   `json:"state_revision"`
	Ready                          bool                     `json:"ready"`
	ReadinessReason                string                   `json:"readiness_reason"`
	StartedAt                      time.Time                `json:"started_at"`
	UptimeSeconds                  int64                    `json:"uptime_seconds"`
	ClusterCount                   int                      `json:"cluster_count"`
	ActiveOperations               int                      `json:"active_operations"`
	IndeterminateOperations        int                      `json:"indeterminate_operations"`
	ActiveLifecycleTasks           int                      `json:"active_lifecycle_tasks"`
	UpdateMaintenanceActive        bool                     `json:"update_maintenance_active"`
	ControllerMembers              []ControllerMemberStatus `json:"controller_members,omitempty"`
	DataNodeMembers                []DataNodeMemberStatus   `json:"data_node_members"`
}

// ControllerMemberStatus exposes the live Raft voter identity and its trusted
// transport endpoints. The software updater uses this list to reject stale
// deployment inventories before entering maintenance.
type ControllerMemberStatus struct {
	ResourceID  model.ResourceID `json:"resource_id"`
	RaftAddress string           `json:"raft_address"`
	APIAddress  string           `json:"api_address,omitempty"`
}

// DataNodeMemberStatus exposes the immutable identity of every active data
// plane host. Upgrade tooling compares this inventory with each remote
// node.json before touching a package so expansion cannot leave a host behind.
type DataNodeMemberStatus struct {
	ResourceID model.ResourceID `json:"resource_id"`
	NodeName   string           `json:"node_name"`
	Kind       model.NodeKind   `json:"kind"`
	Hostname   string           `json:"hostname,omitempty"`
	IPAddress  string           `json:"ip_address,omitempty"`
}

func ActiveDataNodeMembers(nodes []model.DatabaseNode) []DataNodeMemberStatus {
	members := make([]DataNodeMemberStatus, 0)
	for _, node := range nodes {
		if !node.Active || (node.Kind != model.NodeData && node.Kind != model.NodeMixed) {
			continue
		}
		members = append(members, DataNodeMemberStatus{
			ResourceID: node.ResourceID,
			NodeName:   node.NodeName,
			Kind:       node.Kind,
			Hostname:   node.Hostname,
			IPAddress:  node.IPAddress,
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ResourceID < members[j].ResourceID })
	return members
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
		DataNodeMembers: []DataNodeMemberStatus{},
	}
	if server.store == nil {
		return status
	}
	status.StateRevision = server.store.StateRevision()
	status.ClusterCount = len(server.store.Clusters())
	status.DataNodeMembers = ActiveDataNodeMembers(server.store.Nodes())
	for _, operation := range server.store.Operations("") {
		switch operation.Status {
		case model.OperationRunning:
			status.ActiveOperations++
		case model.OperationIndeterminate:
			if operation.RequiresReview() {
				status.IndeterminateOperations++
			}
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
	var status ControlPlaneStatus
	var err error
	if server.controlPlane == nil {
		status = server.localControlPlaneStatus()
	} else {
		status, err = server.controlPlane.ControlPlaneStatus(ctx)
		if err != nil {
			return ControlPlaneStatus{}, err
		}
	}
	if server.maintenance != nil && server.maintenance.Check(ctx) != nil {
		status.UpdateMaintenanceActive = true
	}
	return status, nil
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

func (server *Server) platformVersionRoute(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"result": buildinfo.Current("clusterguard"),
	})
}

func writeProbeJSON(writer http.ResponseWriter, request *http.Request, status int, value interface{}) {
	if request.Method == http.MethodHead {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(status)
		return
	}
	writeJSON(writer, status, value)
}
