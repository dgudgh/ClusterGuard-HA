package store

import (
	"context"
	"fmt"

	"clusterguard.io/ha/pkg/model"
)

func (repository *Repository) SetMaintenance(ctx context.Context, clusterID, instanceID model.ResourceID, value bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !model.ValidResourceID(clusterID) || !model.ValidResourceID(instanceID) {
		return validationError("maintenance resource scope is invalid")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	instance, found := repository.snapshot.Instances[instanceID]
	if !found || instance.ClusterID != clusterID {
		return validationError("maintenance target is not in the selected cluster")
	}
	if instance.Maintenance == value {
		return nil
	}
	instance.Maintenance = value
	instance.MetadataRevision++
	instance.UpdatedAt = repository.now().UTC()
	next := repository.snapshot
	next.Instances = cloneInstanceMap(repository.snapshot.Instances)
	next.Instances[instanceID] = cloneInstance(instance)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("persist maintenance state: %w", err)
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) Maintenance(ctx context.Context, clusterID, instanceID model.ResourceID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	instance, found := repository.snapshot.Instances[instanceID]
	if !found || instance.ClusterID != clusterID {
		return false, validationError("maintenance target is not in the selected cluster")
	}
	return instance.Maintenance, nil
}
