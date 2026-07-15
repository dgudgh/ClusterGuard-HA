package store

import (
	"fmt"
	"sort"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/pkg/model"
)

func (repository *Repository) CoordinationLeases() []coordination.LeaseRecord {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	records := make([]coordination.LeaseRecord, 0, len(repository.snapshot.CoordinationLeases))
	for _, record := range repository.snapshot.CoordinationLeases {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Lease.ResourceID < records[j].Lease.ResourceID })
	return records
}

func (repository *Repository) PutCoordinationLease(record coordination.LeaseRecord) error {
	lease := record.Lease
	if !model.ValidResourceID(lease.ResourceID) || !model.ValidResourceID(lease.ClusterID) || !model.ValidResourceID(lease.HAEndpointID) ||
		!model.ValidResourceID(lease.OperationID) || !model.ValidResourceID(lease.OwnerID) || lease.ExpiresAt.IsZero() {
		return validationError("coordination lease is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.CoordinationLeases = cloneCoordinationLeaseMap(repository.snapshot.CoordinationLeases)
	next.CoordinationLeases[lease.ResourceID] = record
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("persist coordination lease: %w", err)
	}
	return nil
}

func (repository *Repository) DeleteCoordinationLease(resourceID model.ResourceID) error {
	if !model.ValidResourceID(resourceID) {
		return validationError("coordination lease ID is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, found := repository.snapshot.CoordinationLeases[resourceID]; !found {
		return nil
	}
	next := repository.snapshot
	next.CoordinationLeases = cloneCoordinationLeaseMap(repository.snapshot.CoordinationLeases)
	delete(next.CoordinationLeases, resourceID)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("delete coordination lease: %w", err)
	}
	return nil
}

func (repository *Repository) ReplaceCoordinationLeases(records []coordination.LeaseRecord) error {
	nextRecords := make(map[model.ResourceID]coordination.LeaseRecord, len(records))
	for _, record := range records {
		lease := record.Lease
		if !model.ValidResourceID(lease.ResourceID) || !model.ValidResourceID(lease.ClusterID) || !model.ValidResourceID(lease.HAEndpointID) ||
			!model.ValidResourceID(lease.OperationID) || !model.ValidResourceID(lease.OwnerID) || lease.ExpiresAt.IsZero() ||
			record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
			return validationError("coordination lease batch contains an invalid record")
		}
		if _, found := nextRecords[lease.ResourceID]; found {
			return validationError("coordination lease batch contains duplicate resource IDs")
		}
		nextRecords[lease.ResourceID] = record
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.CoordinationLeases = nextRecords
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("replace coordination leases: %w", err)
	}
	return nil
}

func (repository *Repository) CoordinationOperationLocks() []coordination.OperationLockRecord {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	records := make([]coordination.OperationLockRecord, 0, len(repository.snapshot.OperationLocks))
	for _, record := range repository.snapshot.OperationLocks {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ResourceID < records[j].ResourceID })
	return records
}

func (repository *Repository) PutCoordinationOperationLock(record coordination.OperationLockRecord) error {
	if !model.ValidResourceID(record.ResourceID) || !model.ValidResourceID(record.ClusterID) || !model.ValidResourceID(record.OperationID) ||
		record.ExpiresAt.IsZero() || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return validationError("coordination operation lock is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.OperationLocks = cloneOperationLockMap(repository.snapshot.OperationLocks)
	next.OperationLocks[record.ResourceID] = record
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("persist coordination operation lock: %w", err)
	}
	return nil
}

func (repository *Repository) DeleteCoordinationOperationLock(resourceID model.ResourceID) error {
	if !model.ValidResourceID(resourceID) {
		return validationError("coordination operation lock ID is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, found := repository.snapshot.OperationLocks[resourceID]; !found {
		return nil
	}
	next := repository.snapshot
	next.OperationLocks = cloneOperationLockMap(repository.snapshot.OperationLocks)
	delete(next.OperationLocks, resourceID)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return fmt.Errorf("delete coordination operation lock: %w", err)
	}
	return nil
}
