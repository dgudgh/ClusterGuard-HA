package coordination

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type leaseRecordStore struct {
	records map[model.ResourceID]LeaseRecord
}

func (store *leaseRecordStore) CoordinationLeases() []LeaseRecord {
	result := make([]LeaseRecord, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result
}

func (store *leaseRecordStore) PutCoordinationLease(record LeaseRecord) error {
	store.records[record.Lease.ResourceID] = record
	return nil
}

func (store *leaseRecordStore) DeleteCoordinationLease(id model.ResourceID) error {
	delete(store.records, id)
	return nil
}

func authoritativeMembership(t *testing.T) *Membership {
	t.Helper()
	first, second, third := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	membership, err := NewMembership(first, first, []Controller{controller(first, true), controller(second, true), controller(third, true)})
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return membership
}

func TestQuorumLeaseRejectsConflictAndSurvivesStoreRecreation(t *testing.T) {
	now := time.Date(2026, time.July, 13, 15, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	request := endpoint.LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second}
	lease, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	reopened := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	if err := reopened.Validate(context.Background(), lease); err != nil {
		t.Fatalf("validate recreated store: %v", err)
	}
	request.OperationID = model.NewResourceID()
	request.OwnerID = model.NewResourceID()
	if _, err := reopened.Acquire(context.Background(), request); !errors.Is(err, endpoint.ErrLeaseConflict) {
		t.Fatalf("conflicting lease error=%v", err)
	}
}

func TestQuorumLeaseRejectsGrantWithoutMajority(t *testing.T) {
	first, second, third := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	membership, err := NewMembership(first, first, []Controller{controller(first, true), controller(second, false), controller(third, false)})
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	store := NewLeaseStore(&leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}, membership, time.Now)
	_, err = store.Acquire(context.Background(), endpoint.LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID()})
	if !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("minority lease error=%v", err)
	}
}

func TestQuorumLeaseRenewsSameStableOwnershipIntent(t *testing.T) {
	now := time.Date(2026, time.July, 13, 15, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	request := endpoint.LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second}
	first, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	renewed, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ResourceID != first.ResourceID || !renewed.ExpiresAt.Equal(now.Add(30*time.Second)) || !records.records[first.ResourceID].UpdatedAt.Equal(now) {
		t.Fatalf("renewed lease=%+v record=%+v", renewed, records.records[first.ResourceID])
	}
	if err := store.Validate(context.Background(), first); err != nil {
		t.Fatalf("validate in-flight lease snapshot after renewal: %v", err)
	}
}
