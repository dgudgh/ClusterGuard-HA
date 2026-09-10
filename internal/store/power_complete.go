package store

import (
	"context"
	"fmt"
	"time"

	"clusterguard.io/ha/pkg/model"
)

// CompletePowerRecovery commits the verified terminal state and releases the
// cluster/member protections in one replicated snapshot. The caller's observed
// topology timestamp binds this commit to the observation it actually checked.
func (r *Repository) CompletePowerRecovery(ctx context.Context, id model.ResourceID, revision uint64, observedAt time.Time) (model.PowerOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.PowerOperation{}, err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	op, found := r.snapshot.PowerOperations[id]
	if !found {
		return op, notFoundError("unknown power operation")
	}
	if op.MetadataRevision != revision || op.State != model.PowerVerifying {
		return op, conflictError("power recovery verification changed")
	}
	topology, found := r.snapshot.TopologySnapshots[op.ClusterID]
	if !found || observedAt.IsZero() || !topology.ObservedAt.Equal(observedAt) {
		return op, conflictError("verified power topology changed")
	}
	cluster := r.snapshot.Clusters[op.ClusterID]
	if cluster.DisasterRecoveryActive() {
		return op, conflictError("disaster recovery owns the cluster protections")
	}
	now := r.now().UTC()
	next := cloneDiscoverySnapshot(r.snapshot)
	op.State = model.PowerCompleted
	op.CompletedAt = now
	op.UpdatedAt = now
	op.MetadataRevision++
	op.Message = "recovery verified; completion and protection release committed atomically"
	next.PowerOperations[id] = clonePowerOperation(op)
	cluster.RecoveryFreeze = false
	cluster.UpdatedAt = now
	cluster.MetadataRevision++
	next.Clusters[op.ClusterID] = cluster
	for id, instance := range next.Instances {
		if instance.ClusterID == op.ClusterID {
			instance.Maintenance = false
			instance.UpdatedAt = now
			instance.MetadataRevision++
			next.Instances[id] = instance
		}
	}
	for i := range topology.Instances {
		topology.Instances[i].Maintenance = false
	}
	next.TopologySnapshots[op.ClusterID] = cloneTopologySnapshot(topology)
	if err := r.commitSnapshotLocked(next); err != nil {
		return model.PowerOperation{}, fmt.Errorf("persist atomic power recovery: %w", err)
	}
	return clonePowerOperation(op), nil
}
