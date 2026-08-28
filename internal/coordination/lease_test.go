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
	records      map[model.ResourceID]LeaseRecord
	replaceCalls int
	putCalls     int
	deleteCalls  int
}

func (store *leaseRecordStore) CoordinationLeases() []LeaseRecord {
	result := make([]LeaseRecord, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result
}

func (store *leaseRecordStore) PutCoordinationLease(record LeaseRecord) error {
	store.putCalls++
	store.records[record.Lease.ResourceID] = record
	return nil
}

func (store *leaseRecordStore) DeleteCoordinationLease(id model.ResourceID) error {
	store.deleteCalls++
	delete(store.records, id)
	return nil
}

func (store *leaseRecordStore) ReplaceCoordinationLeases(records []LeaseRecord) error {
	store.replaceCalls++
	store.records = make(map[model.ResourceID]LeaseRecord, len(records))
	for _, record := range records {
		store.records[record.Lease.ResourceID] = record
	}
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

func TestQuorumLeaseRenewalNeverShortensExistingExpiry(t *testing.T) {
	now := time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC)
	startedAt := now
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	request := endpoint.LeaseRequest{
		ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(),
		OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: time.Minute,
	}
	first, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	request.TTL = 30 * time.Second
	renewed, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(startedAt.Add(time.Minute)) || renewed.ExpiresAt.Before(first.ExpiresAt) {
		t.Fatalf("short renewal reduced lease expiry: first=%s renewed=%s", first.ExpiresAt, renewed.ExpiresAt)
	}
}

func TestQuorumLeaseBatchesStableOwnershipRenewalsInOneDurableMutation(t *testing.T) {
	startedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	now := startedAt
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	requests := make([]endpoint.LeaseRequest, 0, 6)
	for index := 0; index < 6; index++ {
		clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
		requests = append(requests, endpoint.LeaseRequest{
			ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID,
			OwnerID: model.NewResourceID(), TTL: 30 * time.Second,
		})
	}
	if err := store.AcquireStableBatch(context.Background(), requests); err != nil {
		t.Fatalf("initial batch acquire: %v", err)
	}
	if records.replaceCalls != 1 || len(records.records) != len(requests) {
		t.Fatalf("initial batch persisted calls=%d records=%d", records.replaceCalls, len(records.records))
	}

	now = now.Add(10 * time.Second)
	if err := store.AcquireStableBatch(context.Background(), requests); err != nil {
		t.Fatalf("batch with renewal headroom: %v", err)
	}
	if records.replaceCalls != 1 {
		t.Fatalf("leases with twenty seconds of headroom used %d durable mutations, want one initial mutation", records.replaceCalls)
	}
	for _, record := range records.records {
		if !record.Lease.ExpiresAt.Equal(startedAt.Add(30*time.Second)) || !record.UpdatedAt.Equal(startedAt) {
			t.Fatalf("lease changed before entering its renewal window: %+v", record)
		}
	}

	now = startedAt.Add(16 * time.Second)
	if err := store.AcquireStableBatch(context.Background(), requests); err != nil {
		t.Fatalf("batch renewal near expiry: %v", err)
	}
	if records.replaceCalls != 2 {
		t.Fatalf("six near-expiry renewals used %d durable mutations, want one additional batch mutation", records.replaceCalls)
	}
	for _, record := range records.records {
		if !record.Lease.ExpiresAt.Equal(now.Add(30*time.Second)) || !record.UpdatedAt.Equal(now) {
			t.Fatalf("near-expiry lease was not renewed: %+v", record)
		}
	}
}

func TestQuorumLeaseRenewOnlyRequiresAnExistingStableLease(t *testing.T) {
	now := time.Date(2026, time.July, 28, 7, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	endpointID := model.NewResourceID()
	request := endpoint.LeaseRequest{
		ClusterID: model.NewResourceID(), HAEndpointID: endpointID, OperationID: endpointID,
		OwnerID: model.NewResourceID(), TTL: 30 * time.Second, RenewOnly: true,
	}

	if err := store.AcquireStableBatch(context.Background(), []endpoint.LeaseRequest{request}); !errors.Is(err, endpoint.ErrLeaseConflict) {
		t.Fatalf("renew-only missing lease error=%v", err)
	}
	if len(records.records) != 0 {
		t.Fatalf("renew-only request created a lease: %+v", records.records)
	}

	request.RenewOnly = false
	if err := store.AcquireStableBatch(context.Background(), []endpoint.LeaseRequest{request}); err != nil {
		t.Fatalf("seed stable lease: %v", err)
	}
	var seeded LeaseRecord
	for _, record := range records.records {
		seeded = record
	}
	now = now.Add(16 * time.Second)
	request.RenewOnly = true
	if err := store.AcquireStableBatch(context.Background(), []endpoint.LeaseRequest{request}); err != nil {
		t.Fatalf("renew existing stable lease: %v", err)
	}
	renewed := records.records[seeded.Lease.ResourceID]
	if renewed.Lease.ResourceID != seeded.Lease.ResourceID || !renewed.Lease.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("renew-only lease=%+v, seeded=%+v", renewed, seeded)
	}
}

func TestQuorumLeaseBatchIsolatesTransitionConflictToOneCluster(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 30, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	conflictedEndpointID := model.NewResourceID()
	conflicted := endpoint.LeaseRequest{
		ClusterID: model.NewResourceID(), HAEndpointID: conflictedEndpointID, OperationID: conflictedEndpointID,
		OwnerID: model.NewResourceID(), TTL: 30 * time.Second,
	}
	if _, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: conflicted.ClusterID, HAEndpointID: conflicted.HAEndpointID,
		OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second,
	}); err != nil {
		t.Fatalf("seed transition lease: %v", err)
	}
	healthyEndpointID := model.NewResourceID()
	healthy := endpoint.LeaseRequest{
		ClusterID: model.NewResourceID(), HAEndpointID: healthyEndpointID, OperationID: healthyEndpointID,
		OwnerID: model.NewResourceID(), TTL: 30 * time.Second,
	}
	records.replaceCalls = 0
	if err := store.AcquireStableBatch(context.Background(), []endpoint.LeaseRequest{conflicted, healthy}); !errors.Is(err, endpoint.ErrLeaseConflict) {
		t.Fatalf("batch conflict error=%v", err)
	}
	if records.replaceCalls != 1 {
		t.Fatalf("healthy lease was not persisted in one batch mutation: calls=%d", records.replaceCalls)
	}
	foundHealthy := false
	for _, record := range records.records {
		if record.Lease.ClusterID == healthy.ClusterID && record.Lease.OwnerID == healthy.OwnerID {
			foundHealthy = true
		}
	}
	if !foundHealthy {
		t.Fatalf("healthy cluster lease was starved by unrelated transition: records=%+v", records.records)
	}
}

func TestQuorumLeaseAtomicallyHandsStableOwnershipToTransition(t *testing.T) {
	now := time.Date(2026, time.July, 13, 15, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	sourceID, targetID := model.NewResourceID(), model.NewResourceID()
	stable, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: targetID,
		PreviousOwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("handoff stable quorum lease: %v", err)
	}
	if transition.ResourceID != stable.ResourceID || transition.OwnerID != targetID || transition.PreviousOwnerID != sourceID || len(records.records) != 1 {
		t.Fatalf("transition=%+v stable=%+v records=%+v", transition, stable, records.records)
	}
	if records.deleteCalls != 0 || records.putCalls != 2 {
		t.Fatalf("stable handoff mutations put=%d delete=%d, want two upserts and no delete", records.putCalls, records.deleteCalls)
	}
}

func TestQuorumLeaseFinalizesTransitionInOneDurableUpdate(t *testing.T) {
	now := time.Date(2026, time.July, 13, 15, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	sourceID, targetID := model.NewResourceID(), model.NewResourceID()
	if _, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: sourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transition, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: targetID,
		PreviousOwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	stable, err := store.FinalizeTransition(context.Background(), transition, 30*time.Second)
	if err != nil {
		t.Fatalf("finalize transition: %v", err)
	}
	record := records.records[transition.ResourceID]
	if len(records.records) != 1 || stable.ResourceID != transition.ResourceID || stable.OperationID != endpointID || stable.OwnerID != targetID || stable.PreviousOwnerID != "" || record.Lease != stable || !record.UpdatedAt.Equal(now) {
		t.Fatalf("stable=%+v record=%+v records=%+v", stable, record, records.records)
	}
	if _, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: sourceID,
		PreviousOwnerID: targetID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatalf("immediate reverse handoff: %v", err)
	}
}

func TestQuorumLeaseRollsTransitionBackInOneDurableUpdate(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	store := NewLeaseStore(records, authoritativeMembership(t), func() time.Time { return now })
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	sourceID, targetID := model.NewResourceID(), model.NewResourceID()
	if _, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: sourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transition, err := store.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: targetID,
		PreviousOwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	stable, err := store.RollbackTransition(context.Background(), transition, 30*time.Second)
	if err != nil {
		t.Fatalf("rollback transition: %v", err)
	}
	record := records.records[transition.ResourceID]
	if len(records.records) != 1 || stable.ResourceID != transition.ResourceID || stable.OperationID != endpointID || stable.OwnerID != sourceID || stable.PreviousOwnerID != "" || record.Lease != stable || !record.UpdatedAt.Equal(now) {
		t.Fatalf("stable=%+v record=%+v records=%+v", stable, record, records.records)
	}
}
