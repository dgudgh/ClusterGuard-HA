package api

import (
	"net/http"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type leadershipEpochProvider interface {
	LeadershipEpoch() uint64
}

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
	// An automatic failover whose promotion committed but whose verification
	// did not finish is resumed under the immutable original operation ID. Its
	// durable record intentionally remains terminal until continuation verifies
	// the complete topology. Keep the target authorized only while a fresh,
	// majority-replicated transition lease names that exact operation and target.
	if found && operation.TargetID == instanceID && operation.Status == model.OperationIndeterminate &&
		operation.Stage == model.StageVerify && operation.FailureClass == "promoted_unverified" &&
		operation.Operation.Kind == model.OperationFailover &&
		operation.Operation.RequestedBy == "clusterguard-automatic-recovery" &&
		operation.Plan.OperationID == operation.ResourceID && operation.Plan.ClusterID == operation.Operation.ClusterID &&
		operation.Plan.TargetID == operation.TargetID && model.ValidResourceID(operation.Plan.SourceID) &&
		lease.PreviousOwnerID == operation.Plan.SourceID && lease.OwnerID == operation.TargetID {
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

func (server *Server) transitionSourceAuthorizes(instanceID model.ResourceID, lease endpoint.Lease) bool {
	if lease.OperationID == lease.HAEndpointID || lease.PreviousOwnerID != instanceID {
		return false
	}
	operation, found := server.store.Operation(lease.OperationID)
	if !found || operation.TargetID != lease.OwnerID || operation.Status != model.OperationRunning ||
		(operation.Stage != model.StageExecute && operation.Stage != model.StageVerify) {
		return false
	}
	if operation.Operation.Kind != model.OperationSwitchover {
		return false
	}
	return !model.ValidResourceID(operation.Plan.SourceID) || operation.Plan.SourceID == instanceID
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
	if err := decodePreservingBody(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid agent reconcile request")
		return
	}
	now := time.Now().UTC()
	if err := agent.VerifyReconcileRequest(payload, server.agentSecret, now); err != nil {
		writeError(writer, http.StatusUnauthorized, "agent reconcile request authentication failed")
		return
	}
	// Upgrade maintenance blocks new operations, not signed decisions for an
	// existing writer lease. Quorum, lease expiry, and local fencing still apply.
	if !server.authorizeLeaderQuorum(writer, request) {
		return
	}
	releaseDecision := func() {}
	if server.agentAuthz != nil {
		releaseDecision = server.agentAuthz.BeginDecision()
	}
	defer releaseDecision()
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
	leadershipEpoch := uint64(0)
	if provider, found := server.authority.(leadershipEpochProvider); found {
		leadershipEpoch = provider.LeadershipEpoch()
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
			evidence.TransitionSource = server.transitionSourceAuthorizes(payload.InstanceID, lease)
			evidence.StableTarget = lease.OperationID == lease.HAEndpointID && lease.OwnerID == payload.InstanceID && validBootstrapLeaseRecord(leaseRecord, now)
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
	if decision.Action == coordination.SelfIsolationKeepVIP || decision.Action == coordination.SelfIsolationHoldTransitionSource || decision.Action == coordination.SelfIsolationBootstrapPrimary {
		response.Action = agent.ReconcileKeepVIP
		if evidence.TransitionTarget {
			response.Action = agent.ReconcileTransitionTarget
		}
		if decision.Action == coordination.SelfIsolationHoldTransitionSource {
			response.Action = agent.ReconcileTransitionSource
		}
		if decision.Action == coordination.SelfIsolationBootstrapPrimary {
			response.Action = agent.ReconcileBootstrapPrimary
		}
		response.LeaseID = evidence.Lease.ResourceID
		response.ValidUntil = evidence.Lease.ExpiresAt
		if response.ValidUntil.After(now.Add(10 * time.Second)) {
			response.ValidUntil = now.Add(10 * time.Second)
		}
	} else {
		response.Action = agent.ReconcileSelfIsolate
	}
	if cluster, found := server.store.Cluster(payload.ClusterID); found && cluster.DisasterRecoveryActive() {
		response.Action = agent.ReconcileSelfIsolate
		response.LeaseID = ""
		response.Reason = "disaster recovery freeze prohibits normal writer and VIP authorization"
		response.ValidUntil = now.Add(10 * time.Second)
		if task, expiresAt, allowed := server.store.RecoveryAuthorization(payload.ClusterID, payload.InstanceID, now); allowed {
			response.RecoveryTaskID = task.ResourceID
			response.LeaseID = task.LeaseID
			response.Action = agent.ReconcileRecoveryPrimary
			response.Reason = "recovery-only primary permit requires a verified local business-access guard; VIP remains prohibited"
			if task.PrimaryID != payload.InstanceID {
				response.Action = agent.ReconcileRecoveryReplica
				response.Reason = "recovery replica reconstruction requires a verified local offline-mode guard; VIP remains prohibited"
			}
			if task.Stage == model.RecoveryFencing || task.Stage == model.RecoveryInspecting {
				response.Action = agent.ReconcileRecoveryPrepare
				response.Reason = "recovery preparation is isolated by offline mode and does not authorize a primary or VIP"
			}
			if expiresAt.Before(response.ValidUntil) {
				response.ValidUntil = expiresAt
			}
			if task.Stage == model.RecoveryCommitted {
				// A committed topology alone is not a business writer lease.
				if evidence.CanonicalOwnerID == task.PrimaryID && evidence.EndpointOwnerID == task.PrimaryID && evidence.Lease.OwnerID == task.PrimaryID && evidence.Lease.OperationID == evidence.Lease.HAEndpointID && evidence.Lease.Active && evidence.Lease.ExpiresAt.After(now) {
					response.Action = agent.ReconcileRecoveryActivate
					response.Reason = "Recovery Commit and majority writer lease authorize guarded activation"
					if evidence.Lease.ExpiresAt.Before(response.ValidUntil) {
						response.ValidUntil = evidence.Lease.ExpiresAt
					}
				}
			}
		}
	}
	if err := agent.SignReconcileResponse(&response, server.agentSecret); err != nil {
		writeError(writer, http.StatusInternalServerError, "sign agent reconcile response failed")
		return
	}
	if server.agentAuthz != nil && leadershipEpoch > 0 {
		provider, found := server.authority.(leadershipEpochProvider)
		if !found || provider.LeadershipEpoch() != leadershipEpoch {
			writeError(writer, http.StatusServiceUnavailable, "controller leadership changed while issuing Agent authorization")
			return
		}
		server.agentAuthz.Record(response, leadershipEpoch)
	}
	writeJSON(writer, http.StatusOK, response)
}
