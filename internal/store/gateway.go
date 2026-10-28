package store

import (
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"math"
	"time"
)

func cloneGatewayGrants(in map[model.ResourceID]model.GatewayGrant) map[model.ResourceID]model.GatewayGrant {
	if in == nil {
		return nil
	}
	out := make(map[model.ResourceID]model.GatewayGrant, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (r *Repository) GatewayGrant(cluster model.ResourceID) model.GatewayGrant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot.GatewayPublications[cluster]
}

// ReserveGatewayGrant commits the ordering fence through the repository's
// consensus path before network publication. It compares the CURRENT lease
// under the same lock; expired/stale owners cannot allocate new route versions.
func (r *Repository) ReserveGatewayGrant(presented endpoint.Lease, operation model.ResourceID, mode string, newEpoch bool) (model.GatewayGrant, error) {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	resource, ok := r.snapshot.HAEndpoints[presented.HAEndpointID]
	if !ok || resource.ClusterID != presented.ClusterID || resource.Provider != model.EndpointProviderGateway || resource.Kind != model.EndpointService {
		return model.GatewayGrant{}, validationError("gateway resource is unavailable")
	}
	entry, ok := r.snapshot.Endpoints[resource.ClusterID][resource.EndpointID]
	if !ok || !entry.Active || entry.Kind != model.EndpointService {
		return model.GatewayGrant{}, validationError("gateway endpoint is inactive")
	}
	var current endpoint.Lease
	for _, record := range r.snapshot.CoordinationLeases {
		if record.Lease.ResourceID == presented.ResourceID {
			current = record.Lease
		}
	}
	if !endpoint.SameLeaseIdentity(current, presented) || !current.Active || !current.ExpiresAt.After(now.Add(time.Second)) {
		return model.GatewayGrant{}, conflictError("gateway publication requires the current majority lease")
	}
	if !model.ValidResourceID(operation) || (mode != "activate" && mode != "withdraw") || (newEpoch && mode != "withdraw") {
		return model.GatewayGrant{}, validationError("invalid gateway publication")
	}
	if current.OperationID != current.HAEndpointID && current.OperationID != operation {
		return model.GatewayGrant{}, conflictError("gateway operation does not own transition lease")
	}
	old := r.snapshot.GatewayPublications[current.ClusterID]
	grant := model.GatewayGrant{ClusterID: current.ClusterID, TargetID: current.OwnerID, LeaseID: current.ResourceID, OperationID: operation, Mode: mode, IssuedAt: now, ExpiresAt: now.Add(15 * time.Second)}
	if current.ExpiresAt.Before(grant.ExpiresAt) {
		grant.ExpiresAt = current.ExpiresAt
	}
	if newEpoch {
		if old.Epoch == math.MaxUint64 {
			return grant, conflictError("gateway epoch exhausted")
		}
		grant.Epoch = old.Epoch + 1
		grant.Sequence = 1
	} else {
		if old.Epoch == 0 || old.Sequence == math.MaxUint64 || old.TargetID != current.OwnerID || old.LeaseID != current.ResourceID || old.OperationID != operation {
			return grant, conflictError("gateway grant binding changed; withdrawal barrier required")
		}
		grant.Epoch = old.Epoch
		grant.Sequence = old.Sequence + 1
	}
	next := r.snapshot
	next.GatewayPublications = cloneGatewayGrants(r.snapshot.GatewayPublications)
	if next.GatewayPublications == nil {
		next.GatewayPublications = map[model.ResourceID]model.GatewayGrant{}
	}
	next.GatewayPublications[current.ClusterID] = grant
	if err := r.commitSnapshotLocked(next); err != nil {
		return model.GatewayGrant{}, err
	}
	return grant, nil
}
