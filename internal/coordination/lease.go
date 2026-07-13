package coordination

import (
	"context"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type LeaseRecord struct {
	Lease     endpoint.Lease `json:"lease"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type LeaseRecordStore interface {
	CoordinationLeases() []LeaseRecord
	PutCoordinationLease(LeaseRecord) error
	DeleteCoordinationLease(model.ResourceID) error
}

type LeaseStore struct {
	mu        sync.Mutex
	records   LeaseRecordStore
	authority MutationAuthority
	now       func() time.Time
}

func NewLeaseStore(records LeaseRecordStore, authority MutationAuthority, now func() time.Time) *LeaseStore {
	if now == nil {
		now = time.Now
	}
	return &LeaseStore{records: records, authority: authority, now: now}
}

func (store *LeaseStore) authorize(ctx context.Context) error {
	if store == nil || store.records == nil || store.authority == nil {
		return fmt.Errorf("coordination lease store is not configured")
	}
	return store.authority.RequireMutationAuthority(ctx)
}

func (store *LeaseStore) Acquire(ctx context.Context, request endpoint.LeaseRequest) (endpoint.Lease, error) {
	if err := store.authorize(ctx); err != nil {
		return endpoint.Lease{}, err
	}
	if !model.ValidResourceID(request.ClusterID) || !model.ValidResourceID(request.HAEndpointID) || !model.ValidResourceID(request.OperationID) || !model.ValidResourceID(request.OwnerID) {
		return endpoint.Lease{}, fmt.Errorf("endpoint lease resource scope is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	for _, record := range store.records.CoordinationLeases() {
		lease := record.Lease
		if !lease.Active || !lease.ExpiresAt.After(now) {
			_ = store.records.DeleteCoordinationLease(lease.ResourceID)
			continue
		}
		if lease.ClusterID != request.ClusterID || lease.HAEndpointID != request.HAEndpointID {
			continue
		}
		if lease.OperationID == request.OperationID && lease.OwnerID == request.OwnerID {
			return lease, nil
		}
		return endpoint.Lease{}, fmt.Errorf("%w: active quorum lease belongs to another operation", endpoint.ErrLeaseConflict)
	}
	if request.TTL <= 0 || request.TTL > time.Minute {
		request.TTL = 30 * time.Second
	}
	lease := endpoint.Lease{
		ResourceID: model.NewResourceID(), ClusterID: request.ClusterID, HAEndpointID: request.HAEndpointID,
		OperationID: request.OperationID, OwnerID: request.OwnerID, ExpiresAt: now.Add(request.TTL), Active: true,
	}
	if err := store.records.PutCoordinationLease(LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		return endpoint.Lease{}, fmt.Errorf("persist quorum lease: %w", err)
	}
	return lease, nil
}

func (store *LeaseStore) Validate(ctx context.Context, lease endpoint.Lease) error {
	if err := store.authorize(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.records.CoordinationLeases() {
		if record.Lease.ResourceID == lease.ResourceID && record.Lease == lease && lease.Active && lease.ExpiresAt.After(store.now().UTC()) {
			return nil
		}
	}
	return fmt.Errorf("%w: quorum lease is missing, expired, or changed", endpoint.ErrLeaseConflict)
}

func (store *LeaseStore) Release(ctx context.Context, resourceID model.ResourceID) error {
	if err := store.authorize(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.records.DeleteCoordinationLease(resourceID); err != nil {
		return fmt.Errorf("release quorum lease: %w", err)
	}
	return nil
}
