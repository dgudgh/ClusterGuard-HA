package api

import (
	"net/http"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

func topologyPrimaryID(snapshot model.TopologySnapshot) model.ResourceID {
	var primaryID model.ResourceID
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if primaryID != "" {
			return ""
		}
		primaryID = instance.ResourceID
	}
	return primaryID
}

func (server *Server) activeVIP(clusterID model.ResourceID) (model.HAEndpoint, model.Endpoint, bool) {
	var selected model.HAEndpoint
	var selectedEndpoint model.Endpoint
	for _, resource := range server.store.HAEndpoints(clusterID) {
		candidate, found := server.store.Endpoint(resource.EndpointID)
		if !found || resource.Kind != model.EndpointVIP || candidate.Kind != model.EndpointVIP || !candidate.Active {
			continue
		}
		if selected.ResourceID != "" {
			return model.HAEndpoint{}, model.Endpoint{}, false
		}
		selected = resource
		selectedEndpoint = candidate
	}
	return selected, selectedEndpoint, selected.ResourceID != ""
}

func (server *Server) activeOwnershipLease(clusterID, endpointID model.ResourceID, now time.Time) (coordination.LeaseRecord, bool) {
	var selected coordination.LeaseRecord
	for _, record := range server.store.CoordinationLeases() {
		lease := record.Lease
		if lease.ClusterID != clusterID || lease.HAEndpointID != endpointID || !lease.Active || !lease.ExpiresAt.After(now) {
			continue
		}
		if selected.Lease.ResourceID != "" {
			return coordination.LeaseRecord{}, false
		}
		selected = record
	}
	return selected, selected.Lease.ResourceID != ""
}

func (server *Server) transitionAuthorizes(instanceID model.ResourceID, lease endpoint.Lease) bool {
	operation, found := server.store.Operation(lease.OperationID)
	if found && operation.TargetID == instanceID && operation.Status == model.OperationRunning && (operation.Stage == model.StageExecute || operation.Stage == model.StageVerify) {
		return true
	}
	if lease.OperationID != lease.HAEndpointID || lease.OwnerID != instanceID {
		return false
	}
	for _, candidate := range server.store.Operations(lease.ClusterID) {
		if candidate.TargetID != instanceID || candidate.Status != model.OperationRunning || (candidate.Stage != model.StageExecute && candidate.Stage != model.StageVerify) {
			continue
		}
		if candidate.Operation.Kind == model.OperationSwitchover || candidate.Operation.Kind == model.OperationFailover {
			return true
		}
	}
	return false
}

func validBootstrapLeaseRecord(record coordination.LeaseRecord, now time.Time) bool {
	updatedAt := record.UpdatedAt.UTC()
	expiresAt := record.Lease.ExpiresAt.UTC()
	if updatedAt.IsZero() || updatedAt.After(now.UTC()) {
		return false
	}
	lifetime := expiresAt.Sub(updatedAt)
	return lifetime > 0 && lifetime <= time.Minute
}

func (server *Server) agentReconcileRoute(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "agent reconcile requires POST")
		return
	}
	if server.agentSecret == "" || server.authority == nil {
		writeError(writer, http.StatusServiceUnavailable, "agent reconcile is not configured")
		return
	}
	payload := agent.ReconcileRequest{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid agent reconcile request")
		return
	}
	now := time.Now().UTC()
	if err := agent.VerifyReconcileRequest(payload, server.agentSecret, now); err != nil {
		writeError(writer, http.StatusUnauthorized, "agent reconcile request authentication failed")
		return
	}
	if !server.authorizeMutation(writer, request) {
		return
	}
	locator, ok := server.authority.(LeaderLocator)
	if !ok {
		writeError(writer, http.StatusServiceUnavailable, "controller leader identity is unavailable")
		return
	}
	controllerID, _, leaderKnown := locator.Leader()
	if !leaderKnown {
		writeError(writer, http.StatusServiceUnavailable, "controller leader identity is unavailable")
		return
	}

	evidence := coordination.SelfIsolationEvidence{LocalInstanceID: payload.InstanceID, Now: now}
	snapshot, snapshotFound := server.store.TopologySnapshot(payload.ClusterID)
	if snapshotFound {
		evidence.CurrentPrimaryID = topologyPrimaryID(snapshot)
	}
	if resource, vipEndpoint, found := server.activeVIP(payload.ClusterID); found {
		evidence.CanonicalOwnerID = resource.OwnerID
		evidence.EndpointOwnerID = vipEndpoint.InstanceID
		if leaseRecord, leaseFound := server.activeOwnershipLease(payload.ClusterID, resource.ResourceID, now); leaseFound {
			lease := leaseRecord.Lease
			evidence.Lease = lease
			evidence.TransitionTarget = server.transitionAuthorizes(payload.InstanceID, lease)
			if snapshotFound && !evidence.TransitionTarget && evidence.CurrentPrimaryID == "" &&
				evidence.CanonicalOwnerID == payload.InstanceID && evidence.EndpointOwnerID == payload.InstanceID &&
				validBootstrapLeaseRecord(leaseRecord, now) {
				candidate, err := coordination.RebootBootstrapCandidate(snapshot, payload.InstanceID, now, 15*time.Second)
				evidence.BootstrapTarget = err == nil && candidate.ResourceID == payload.InstanceID
			}
		}
	}
	decision := coordination.EvaluateSelfIsolation(evidence)
	response := agent.ReconcileResponse{
		ClusterID: payload.ClusterID, InstanceID: payload.InstanceID, Reason: decision.Reason,
		ValidUntil: now.Add(10 * time.Second), ControllerID: controllerID,
	}
	if decision.Action == coordination.SelfIsolationKeepVIP || decision.Action == coordination.SelfIsolationBootstrapPrimary {
		response.Action = agent.ReconcileKeepVIP
		if evidence.TransitionTarget {
			response.Action = agent.ReconcileTransitionTarget
		}
		if decision.Action == coordination.SelfIsolationBootstrapPrimary {
			response.Action = agent.ReconcileBootstrapPrimary
		}
		response.LeaseID = evidence.Lease.ResourceID
		response.ValidUntil = evidence.Lease.ExpiresAt
		if response.ValidUntil.After(now.Add(30 * time.Second)) {
			response.ValidUntil = now.Add(30 * time.Second)
		}
	} else {
		response.Action = agent.ReconcileSelfIsolate
	}
	if err := agent.SignReconcileResponse(&response, server.agentSecret); err != nil {
		writeError(writer, http.StatusInternalServerError, "sign agent reconcile response failed")
		return
	}
	writeJSON(writer, http.StatusOK, response)
}
