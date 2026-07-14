package endpoint

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/model"
)

var ErrLeaseConflict = errors.New("endpoint lease conflict")

type LeaseRequest struct {
	ClusterID       model.ResourceID
	HAEndpointID    model.ResourceID
	OperationID     model.ResourceID
	OwnerID         model.ResourceID
	PreviousOwnerID model.ResourceID
	TTL             time.Duration
}

type Lease struct {
	ResourceID   model.ResourceID
	ClusterID    model.ResourceID
	HAEndpointID model.ResourceID
	OperationID  model.ResourceID
	OwnerID      model.ResourceID
	ExpiresAt    time.Time
	Active       bool
}

func SameLeaseIdentity(current, presented Lease) bool {
	return current.ResourceID == presented.ResourceID &&
		current.ClusterID == presented.ClusterID &&
		current.HAEndpointID == presented.HAEndpointID &&
		current.OperationID == presented.OperationID &&
		current.OwnerID == presented.OwnerID &&
		current.Active == presented.Active
}

func CanHandoffStableLease(current Lease, request LeaseRequest) bool {
	return model.ValidResourceID(request.PreviousOwnerID) &&
		current.OperationID == request.HAEndpointID &&
		request.OperationID != request.HAEndpointID &&
		current.OwnerID == request.PreviousOwnerID &&
		request.OwnerID != request.PreviousOwnerID
}

type LeaseStore interface {
	Acquire(context.Context, LeaseRequest) (Lease, error)
	Validate(context.Context, Lease) error
	Release(context.Context, model.ResourceID) error
}

type MemoryLeaseStore struct {
	mu      sync.Mutex
	now     func() time.Time
	leases  map[model.ResourceID]Lease
	Blocked bool
}

func NewMemoryLeaseStore(now func() time.Time) *MemoryLeaseStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryLeaseStore{now: now, leases: make(map[model.ResourceID]Lease)}
}

func (store *MemoryLeaseStore) Acquire(ctx context.Context, request LeaseRequest) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.Blocked {
		return Lease{}, fmt.Errorf("%w: lease store is blocked", ErrLeaseConflict)
	}
	now := store.now().UTC()
	if request.TTL <= 0 {
		request.TTL = 30 * time.Second
	}
	for resourceID, lease := range store.leases {
		if !lease.Active || !lease.ExpiresAt.After(now) {
			delete(store.leases, resourceID)
			continue
		}
		if lease.ClusterID != request.ClusterID || lease.HAEndpointID != request.HAEndpointID {
			continue
		}
		if lease.OperationID == request.OperationID && lease.OwnerID == request.OwnerID {
			lease.ExpiresAt = now.Add(request.TTL)
			store.leases[resourceID] = lease
			return lease, nil
		}
		if CanHandoffStableLease(lease, request) {
			delete(store.leases, resourceID)
			continue
		}
		return Lease{}, fmt.Errorf("%w: active endpoint lease belongs to another operation", ErrLeaseConflict)
	}
	lease := Lease{
		ResourceID: model.NewResourceID(), ClusterID: request.ClusterID, HAEndpointID: request.HAEndpointID,
		OperationID: request.OperationID, OwnerID: request.OwnerID, ExpiresAt: now.Add(request.TTL), Active: true,
	}
	store.leases[lease.ResourceID] = lease
	return lease, nil
}

func (store *MemoryLeaseStore) Validate(ctx context.Context, lease Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, found := store.leases[lease.ResourceID]
	if !found || !current.Active || !current.ExpiresAt.After(store.now().UTC()) || !SameLeaseIdentity(current, lease) {
		return fmt.Errorf("%w: endpoint lease is missing, expired, or changed", ErrLeaseConflict)
	}
	return nil
}

func (store *MemoryLeaseStore) Release(ctx context.Context, resourceID model.ResourceID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.leases, resourceID)
	return nil
}
