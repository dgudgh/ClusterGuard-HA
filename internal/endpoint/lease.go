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
	RenewOnly       bool
}

type Lease struct {
	ResourceID      model.ResourceID `json:"resource_id"`
	ClusterID       model.ResourceID `json:"cluster_id"`
	HAEndpointID    model.ResourceID `json:"ha_endpoint_id"`
	OperationID     model.ResourceID `json:"operation_id"`
	OwnerID         model.ResourceID `json:"owner_id"`
	PreviousOwnerID model.ResourceID `json:"previous_owner_id,omitempty"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Active          bool             `json:"active"`
}

func SameLeaseIdentity(current, presented Lease) bool {
	return current.ResourceID == presented.ResourceID &&
		current.ClusterID == presented.ClusterID &&
		current.HAEndpointID == presented.HAEndpointID &&
		current.OperationID == presented.OperationID &&
		current.OwnerID == presented.OwnerID &&
		current.PreviousOwnerID == presented.PreviousOwnerID &&
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
	FinalizeTransition(context.Context, Lease, time.Duration) (Lease, error)
	RollbackTransition(context.Context, Lease, time.Duration) (Lease, error)
	Release(context.Context, model.ResourceID) error
}

// CurrentLeaseReader exposes the active majority-backed ownership decision to
// fencing verification without creating or renewing a lease.
type CurrentLeaseReader interface {
	Current(context.Context, model.ResourceID, model.ResourceID) (Lease, error)
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
		if lease.OperationID == request.OperationID && lease.OwnerID == request.OwnerID && lease.PreviousOwnerID == request.PreviousOwnerID {
			if requestedExpiry := now.Add(request.TTL); requestedExpiry.After(lease.ExpiresAt) {
				lease.ExpiresAt = requestedExpiry
			}
			store.leases[resourceID] = lease
			return lease, nil
		}
		if !request.RenewOnly && CanHandoffStableLease(lease, request) {
			lease.OperationID = request.OperationID
			lease.OwnerID = request.OwnerID
			lease.PreviousOwnerID = request.PreviousOwnerID
			lease.ExpiresAt = now.Add(request.TTL)
			store.leases[resourceID] = lease
			return lease, nil
		}
		return Lease{}, fmt.Errorf("%w: active endpoint lease belongs to another operation", ErrLeaseConflict)
	}
	if request.RenewOnly {
		return Lease{}, fmt.Errorf("%w: renew-only endpoint lease is missing or expired", ErrLeaseConflict)
	}
	lease := Lease{
		ResourceID: model.NewResourceID(), ClusterID: request.ClusterID, HAEndpointID: request.HAEndpointID,
		OperationID: request.OperationID, OwnerID: request.OwnerID, PreviousOwnerID: request.PreviousOwnerID,
		ExpiresAt: now.Add(request.TTL), Active: true,
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

func (store *MemoryLeaseStore) Current(ctx context.Context, clusterID, haEndpointID model.ResourceID) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	var selected Lease
	for resourceID, lease := range store.leases {
		if !lease.Active || !lease.ExpiresAt.After(now) {
			delete(store.leases, resourceID)
			continue
		}
		if lease.ClusterID != clusterID || lease.HAEndpointID != haEndpointID {
			continue
		}
		if selected.ResourceID != "" {
			return Lease{}, fmt.Errorf("%w: multiple active endpoint leases", ErrLeaseConflict)
		}
		selected = lease
	}
	if selected.ResourceID == "" {
		return Lease{}, fmt.Errorf("%w: active endpoint lease is missing", ErrLeaseConflict)
	}
	return selected, nil
}

func (store *MemoryLeaseStore) FinalizeTransition(ctx context.Context, transition Lease, ttl time.Duration) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	current, found := store.leases[transition.ResourceID]
	if !found || !current.Active || !current.ExpiresAt.After(now) || !SameLeaseIdentity(current, transition) || current.OperationID == current.HAEndpointID {
		return Lease{}, fmt.Errorf("%w: transition lease is missing, expired, changed, or already stable", ErrLeaseConflict)
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	current.OperationID = current.HAEndpointID
	current.PreviousOwnerID = ""
	current.ExpiresAt = now.Add(ttl)
	store.leases[current.ResourceID] = current
	return current, nil
}

func (store *MemoryLeaseStore) RollbackTransition(ctx context.Context, transition Lease, ttl time.Duration) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	current, found := store.leases[transition.ResourceID]
	if !found || !current.Active || !current.ExpiresAt.After(now) || !SameLeaseIdentity(current, transition) ||
		current.OperationID == current.HAEndpointID || !model.ValidResourceID(current.PreviousOwnerID) {
		return Lease{}, fmt.Errorf("%w: transition lease is missing, expired, changed, or cannot be rolled back", ErrLeaseConflict)
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	current.OperationID = current.HAEndpointID
	current.OwnerID = current.PreviousOwnerID
	current.PreviousOwnerID = ""
	current.ExpiresAt = now.Add(ttl)
	store.leases[current.ResourceID] = current
	return current, nil
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
