package store

import (
	"path/filepath"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

func TestCoordinationLeasePersistsAcrossRepositoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	lease := endpoint.Lease{
		ResourceID: model.NewResourceID(), ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(),
		OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), ExpiresAt: time.Now().UTC().Add(time.Minute), Active: true,
	}
	if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("put: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	records := reopened.CoordinationLeases()
	if len(records) != 1 || records[0].Lease != lease {
		t.Fatalf("persisted records=%+v", records)
	}
}

func TestCoordinationOperationLockPersistsAcrossRepositoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC()
	record := coordination.OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(),
		ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.PutCoordinationOperationLock(record); err != nil {
		t.Fatalf("put operation lock: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	records := reopened.CoordinationOperationLocks()
	if len(records) != 1 || records[0] != record {
		t.Fatalf("persisted operation locks=%+v", records)
	}
	if err := reopened.DeleteCoordinationOperationLock(record.ResourceID); err != nil {
		t.Fatalf("delete operation lock: %v", err)
	}
	if records := reopened.CoordinationOperationLocks(); len(records) != 0 {
		t.Fatalf("deleted operation lock remains: %+v", records)
	}
}
