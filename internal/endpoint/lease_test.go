package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestMemoryLeaseStoreRejectsConflictingOwnerUntilExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	store := NewMemoryLeaseStore(func() time.Time { return now })
	request := LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second}
	first, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	request.OperationID = model.NewResourceID()
	request.OwnerID = model.NewResourceID()
	if _, err := store.Acquire(context.Background(), request); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("conflicting lease error=%v", err)
	}
	if err := store.Validate(context.Background(), first); err != nil {
		t.Fatalf("validate active lease: %v", err)
	}
}

func TestMemoryLeaseStoreRenewsSameStableOwnershipIntent(t *testing.T) {
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	store := NewMemoryLeaseStore(func() time.Time { return now })
	request := LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second}
	first, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	renewed, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ResourceID != first.ResourceID || !renewed.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("renewed lease=%+v", renewed)
	}
	if err := store.Validate(context.Background(), first); err != nil {
		t.Fatalf("validate in-flight lease snapshot after renewal: %v", err)
	}
}

func TestMemoryLeaseStoreAtomicallyHandsStableOwnershipToTransition(t *testing.T) {
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	store := NewMemoryLeaseStore(func() time.Time { return now })
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	sourceID, targetID := model.NewResourceID(), model.NewResourceID()
	stable, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: targetID,
		PreviousOwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("handoff stable lease: %v", err)
	}
	if transition.ResourceID == stable.ResourceID || transition.OwnerID != targetID || len(store.leases) != 1 {
		t.Fatalf("transition=%+v stable=%+v leases=%+v", transition, stable, store.leases)
	}
}

func TestMemoryLeaseStoreFinalizesTransitionWithoutWaitingForTTL(t *testing.T) {
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	store := NewMemoryLeaseStore(func() time.Time { return now })
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	sourceID, targetID := model.NewResourceID(), model.NewResourceID()
	if _, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: sourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transition, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: targetID,
		PreviousOwnerID: sourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	stable, err := store.FinalizeTransition(context.Background(), transition, 30*time.Second)
	if err != nil {
		t.Fatalf("finalize transition: %v", err)
	}
	if stable.ResourceID != transition.ResourceID || stable.OperationID != endpointID || stable.OwnerID != targetID || len(store.leases) != 1 {
		t.Fatalf("stable=%+v transition=%+v leases=%+v", stable, transition, store.leases)
	}
	if _, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: sourceID,
		PreviousOwnerID: targetID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatalf("immediate reverse handoff: %v", err)
	}
}

func TestMemoryLeaseStoreRejectsStableHandoffWithWrongPreviousOwner(t *testing.T) {
	store := NewMemoryLeaseStore(time.Now)
	clusterID, endpointID := model.NewResourceID(), model.NewResourceID()
	if _, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: endpointID, OwnerID: model.NewResourceID(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := store.Acquire(context.Background(), LeaseRequest{
		ClusterID: clusterID, HAEndpointID: endpointID, OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(),
		PreviousOwnerID: model.NewResourceID(),
	})
	if !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("wrong-owner handoff error=%v", err)
	}
}
