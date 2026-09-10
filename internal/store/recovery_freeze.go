package store

import (
	"context"
	"fmt"

	"clusterguard.io/ha/pkg/model"
)

// SetRecoveryFreeze freezes or unfreezes automatic recovery for a cluster.
// While frozen, the recovery controller and failover safety must not act on
// the cluster — the planned-shutdown protection contract.
func (repository *Repository) SetRecoveryFreeze(ctx context.Context, clusterID model.ResourceID, value bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !model.ValidResourceID(clusterID) {
		return validationError("recovery freeze resource scope is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	cluster, found := repository.snapshot.Clusters[clusterID]
	if !found {
		return notFoundError("unknown cluster ID: %s", clusterID)
	}
	if !value && cluster.RecoveryFreeze && cluster.Recovery != nil && cluster.Recovery.LastRecoveryStatus != "succeeded" {
		return conflictError("disaster recovery protections require verified completion")
	}
	if cluster.RecoveryFreeze == value {
		return nil
	}
	cluster.RecoveryFreeze = value
	cluster.MetadataRevision++
	cluster.UpdatedAt = repository.now().UTC()
	next := repository.snapshot
	next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	next.Clusters[clusterID] = cloneCluster(cluster)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("persist recovery freeze state: %w", err)
	}
	return nil
}

// RecoveryFrozen reports whether automatic recovery is frozen for the cluster.
func (repository *Repository) RecoveryFrozen(ctx context.Context, clusterID model.ResourceID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	cluster, found := repository.snapshot.Clusters[clusterID]
	if !found {
		return false, notFoundError("unknown cluster ID: %s", clusterID)
	}
	return cluster.RecoveryFreeze, nil
}
