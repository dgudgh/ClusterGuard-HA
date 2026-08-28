package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

// CreatePowerOperation creates a new power lifecycle operation for a cluster.
// At most one active (non-terminal) power operation may exist per cluster, so
// a repeated plan request is rejected instead of stacking shutdowns.
func (repository *Repository) CreatePowerOperation(ctx context.Context, operation model.PowerOperation) (model.PowerOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.PowerOperation{}, err
	}
	if !model.ValidResourceID(operation.ClusterID) {
		return model.PowerOperation{}, validationError("power operation cluster ID is invalid")
	}
	if !operation.OperationType.Valid() {
		return model.PowerOperation{}, validationError("power operation type must be service or poweroff")
	}
	if strings.TrimSpace(operation.RequestedBy) == "" {
		return model.PowerOperation{}, validationError("power operation requester is required")
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	cluster, found := repository.snapshot.Clusters[operation.ClusterID]
	if !found {
		return model.PowerOperation{}, notFoundError("unknown cluster ID: %s", operation.ClusterID)
	}
	for _, existing := range repository.snapshot.PowerOperations {
		if existing.ClusterID == operation.ClusterID && !model.TerminalPowerState(existing.State) {
			return model.PowerOperation{}, conflictError("cluster %s already has an active power operation in state %s", operation.ClusterID, existing.State)
		}
	}

	now := repository.now().UTC()
	operation.ResourceID = model.NewResourceID()
	operation.MetadataRevision = 1
	operation.CreatedAt = now
	operation.UpdatedAt = now
	operation.State = model.PowerNormal
	operation.Engine = cluster.Engine
	if operation.StartedAt.IsZero() {
		operation.StartedAt = now
	}

	next := repository.snapshot
	next.PowerOperations = clonePowerOperationMap(repository.snapshot.PowerOperations)
	next.PowerOperations[operation.ResourceID] = clonePowerOperation(operation)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return clonePowerOperation(operation), fmt.Errorf("persist power operation: %w", err)
	}
	return clonePowerOperation(operation), nil
}

// PowerOperation returns a power operation by resource ID.
func (repository *Repository) PowerOperation(resourceID model.ResourceID) (model.PowerOperation, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	operation, found := repository.snapshot.PowerOperations[resourceID]
	return clonePowerOperation(operation), found
}

// ActivePowerOperation returns the active (non-terminal) power operation for
// a cluster, if any.
func (repository *Repository) ActivePowerOperation(ctx context.Context, clusterID model.ResourceID) (model.PowerOperation, bool) {
	if err := ctx.Err(); err != nil {
		return model.PowerOperation{}, false
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, operation := range repository.snapshot.PowerOperations {
		if operation.ClusterID == clusterID && !model.TerminalPowerState(operation.State) {
			return clonePowerOperation(operation), true
		}
	}
	return model.PowerOperation{}, false
}

// PowerOperationsByCluster returns every power operation recorded for a
// cluster, terminal ones included, most recent first.
func (repository *Repository) PowerOperationsByCluster(clusterID model.ResourceID) []model.PowerOperation {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.PowerOperation, 0)
	for _, operation := range repository.snapshot.PowerOperations {
		if operation.ClusterID == clusterID {
			result = append(result, clonePowerOperation(operation))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if !result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].UpdatedAt.After(result[right].UpdatedAt)
		}
		if !result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].CreatedAt.After(result[right].CreatedAt)
		}
		return result[left].ResourceID > result[right].ResourceID
	})
	return result
}

// TransitionPowerOperation validates the state-machine transition (CAS on the
// metadata revision), applies the optional snapshot/message/approver, and
// persists the change atomically. Terminal states never transition.
func (repository *Repository) TransitionPowerOperation(
	ctx context.Context,
	resourceID model.ResourceID,
	expectedRevision uint64,
	targetState model.PowerState,
	approvedBy string,
	message string,
	powerSnapshot *model.PowerSnapshot,
) (model.PowerOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.PowerOperation{}, err
	}
	if !model.ValidResourceID(resourceID) {
		return model.PowerOperation{}, validationError("power operation ID is invalid")
	}
	if !targetState.Valid() {
		return model.PowerOperation{}, validationError("power state is invalid: %s", targetState)
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	operation, found := repository.snapshot.PowerOperations[resourceID]
	if !found {
		return model.PowerOperation{}, notFoundError("unknown power operation ID: %s", resourceID)
	}
	if operation.MetadataRevision != expectedRevision {
		return model.PowerOperation{}, conflictError("power operation metadata revision changed")
	}
	if model.TerminalPowerState(operation.State) {
		return model.PowerOperation{}, conflictError("power operation is already terminal: %s", operation.State)
	}
	if !model.ValidPowerTransition(operation.State, targetState) {
		return model.PowerOperation{}, conflictError("invalid power state transition from %s to %s", operation.State, targetState)
	}

	now := repository.now().UTC()
	operation.State = targetState
	operation.MetadataRevision++
	operation.UpdatedAt = now
	if strings.TrimSpace(approvedBy) != "" {
		operation.ApprovedBy = approvedBy
	}
	if strings.TrimSpace(message) != "" {
		operation.Message = message
	}
	if powerSnapshot != nil {
		operation.Snapshot = powerSnapshot
	}
	if model.TerminalPowerState(targetState) {
		operation.CompletedAt = now
	}

	next := repository.snapshot
	next.PowerOperations = clonePowerOperationMap(repository.snapshot.PowerOperations)
	next.PowerOperations[resourceID] = clonePowerOperation(operation)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return clonePowerOperation(operation), fmt.Errorf("persist power transition: %w", err)
	}
	return clonePowerOperation(operation), nil
}

// ApplyPowerProtections freezes automatic recovery and places every cluster
// instance in maintenance. Called when the shutdown actually begins; all
// protection must stay active until the recovery verifies healthy.
func (repository *Repository) ApplyPowerProtections(ctx context.Context, clusterID model.ResourceID) error {
	if err := repository.SetRecoveryFreeze(ctx, clusterID, true); err != nil {
		return fmt.Errorf("apply power freeze: %w", err)
	}
	for _, instance := range repository.Instances(clusterID) {
		if err := repository.SetMaintenance(ctx, clusterID, instance.ResourceID, true); err != nil {
			return fmt.Errorf("apply power maintenance to %s: %w", instance.ResourceID, err)
		}
	}
	return nil
}

// ReleasePowerProtections unfreezes automatic recovery and clears instance
// maintenance. Called only when verification passed (COMPLETED). FAILED keeps
// every protection active (fail-closed).
func (repository *Repository) ReleasePowerProtections(ctx context.Context, clusterID model.ResourceID) error {
	if err := repository.SetRecoveryFreeze(ctx, clusterID, false); err != nil {
		return fmt.Errorf("release power freeze: %w", err)
	}
	for _, instance := range repository.Instances(clusterID) {
		if err := repository.SetMaintenance(ctx, clusterID, instance.ResourceID, false); err != nil {
			return fmt.Errorf("release power maintenance from %s: %w", instance.ResourceID, err)
		}
	}
	return nil
}

// IsClusterPowerProtected reports whether the cluster's latest power
// operation is in a state that must keep recovery and failover blocked.
// FAILED remains protected even though it is terminal; a later verified
// COMPLETED lifecycle supersedes historical failures. The authoritative gate
// remains the RecoveryFreeze flag set by ApplyPowerProtections.
func (repository *Repository) IsClusterPowerProtected(clusterID model.ResourceID) (bool, model.PowerState) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	latest := model.PowerOperation{}
	found := false
	for _, operation := range repository.snapshot.PowerOperations {
		if operation.ClusterID != clusterID {
			continue
		}
		if !found || operation.UpdatedAt.After(latest.UpdatedAt) ||
			(operation.UpdatedAt.Equal(latest.UpdatedAt) && operation.CreatedAt.After(latest.CreatedAt)) ||
			(operation.UpdatedAt.Equal(latest.UpdatedAt) && operation.CreatedAt.Equal(latest.CreatedAt) && operation.ResourceID > latest.ResourceID) {
			latest = operation
			found = true
		}
	}
	if !found {
		return false, model.PowerNormal
	}
	switch latest.State {
	case model.PowerShuttingDown, model.PowerPoweredOff, model.PowerBootDetected,
		model.PowerRecovering, model.PowerVerifying, model.PowerFailed:
		return true, latest.State
	default:
		return false, model.PowerNormal
	}
}
