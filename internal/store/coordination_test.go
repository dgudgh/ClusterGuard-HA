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

func TestCoordinationLeaseBatchUsesOneConsensusCommit(t *testing.T) {
	repository := NewMemory()
	consensus := &snapshotConsensusStub{apply: repository.ApplyReplicatedState}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}
	now := time.Date(2026, time.July, 14, 13, 0, 0, 0, time.UTC)
	records := make([]coordination.LeaseRecord, 0, 6)
	for index := 0; index < 6; index++ {
		endpointID := model.NewResourceID()
		records = append(records, coordination.LeaseRecord{
			Lease: endpoint.Lease{
				ResourceID: model.NewResourceID(), ClusterID: model.NewResourceID(), HAEndpointID: endpointID,
				OperationID: endpointID, OwnerID: model.NewResourceID(), ExpiresAt: now.Add(30 * time.Second), Active: true,
			},
			CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := repository.ReplaceCoordinationLeases(records); err != nil {
		t.Fatalf("replace coordination leases: %v", err)
	}
	if len(consensus.commits) != 1 {
		t.Fatalf("six lease records used %d consensus commits, want one", len(consensus.commits))
	}
	if persisted := repository.CoordinationLeases(); len(persisted) != len(records) {
		t.Fatalf("persisted lease records=%d want=%d", len(persisted), len(records))
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
