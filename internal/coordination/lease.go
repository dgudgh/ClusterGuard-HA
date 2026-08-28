package coordination

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	ReplaceCoordinationLeases([]LeaseRecord) error
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
	if request.TTL <= 0 || request.TTL > time.Minute {
		request.TTL = 30 * time.Second
	}
	for _, record := range store.records.CoordinationLeases() {
		lease := record.Lease
		if !lease.Active || !lease.ExpiresAt.After(now) {
			_ = store.records.DeleteCoordinationLease(lease.ResourceID)
			continue
		}
		if lease.ClusterID != request.ClusterID || lease.HAEndpointID != request.HAEndpointID {
			continue
		}
		if lease.OperationID == request.OperationID && lease.OwnerID == request.OwnerID && lease.PreviousOwnerID == request.PreviousOwnerID {
			if requestedExpiry := now.Add(request.TTL); requestedExpiry.After(lease.ExpiresAt) {
				lease.ExpiresAt = requestedExpiry
			}
			record.Lease = lease
			record.UpdatedAt = now
			if err := store.records.PutCoordinationLease(record); err != nil {
				return endpoint.Lease{}, fmt.Errorf("renew quorum lease: %w", err)
			}
			return lease, nil
		}
		if endpoint.CanHandoffStableLease(lease, request) {
			lease.OperationID = request.OperationID
			lease.OwnerID = request.OwnerID
			lease.PreviousOwnerID = request.PreviousOwnerID
			lease.ExpiresAt = now.Add(request.TTL)
			record.Lease = lease
			record.UpdatedAt = now
			if err := store.records.PutCoordinationLease(record); err != nil {
				return endpoint.Lease{}, fmt.Errorf("promote stable ownership lease to transition: %w", err)
			}
			return lease, nil
		}
		return endpoint.Lease{}, fmt.Errorf("%w: active quorum lease belongs to another operation", endpoint.ErrLeaseConflict)
	}
	lease := endpoint.Lease{
		ResourceID: model.NewResourceID(), ClusterID: request.ClusterID, HAEndpointID: request.HAEndpointID,
		OperationID: request.OperationID, OwnerID: request.OwnerID, PreviousOwnerID: request.PreviousOwnerID,
		ExpiresAt: now.Add(request.TTL), Active: true,
	}
	if err := store.records.PutCoordinationLease(LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
		return endpoint.Lease{}, fmt.Errorf("persist quorum lease: %w", err)
	}
	return lease, nil
}

// AcquireStableBatch renews the controller-owned steady-state leases with one
// durable metadata mutation. A transition conflict remains local to its
// endpoint so one active operation cannot starve unrelated clusters.
func (store *LeaseStore) AcquireStableBatch(ctx context.Context, requests []endpoint.LeaseRequest) error {
	if err := store.authorize(ctx); err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	now := store.now().UTC()
	next := make(map[model.ResourceID]LeaseRecord)
	activeByScope := make(map[string]model.ResourceID)
	duplicateScopes := make(map[string]bool)
	changed := false
	for _, record := range store.records.CoordinationLeases() {
		lease := record.Lease
		if !lease.Active || !lease.ExpiresAt.After(now) {
			changed = true
			continue
		}
		next[lease.ResourceID] = record
		scope := leaseScope(lease.ClusterID, lease.HAEndpointID)
		if _, found := activeByScope[scope]; found {
			duplicateScopes[scope] = true
			continue
		}
		activeByScope[scope] = lease.ResourceID
	}

	failures := make([]error, 0)
	seenRequests := make(map[string]bool, len(requests))
	for _, request := range requests {
		if !model.ValidResourceID(request.ClusterID) || !model.ValidResourceID(request.HAEndpointID) ||
			request.OperationID != request.HAEndpointID || !model.ValidResourceID(request.OwnerID) ||
			request.PreviousOwnerID != "" {
			failures = append(failures, fmt.Errorf("cluster %s: stable endpoint lease request is invalid", request.ClusterID))
			continue
		}
		scope := leaseScope(request.ClusterID, request.HAEndpointID)
		if seenRequests[scope] {
			failures = append(failures, fmt.Errorf("cluster %s: duplicate stable endpoint lease request", request.ClusterID))
			continue
		}
		seenRequests[scope] = true
		if duplicateScopes[scope] {
			failures = append(failures, fmt.Errorf("cluster %s: %w: multiple active quorum leases", request.ClusterID, endpoint.ErrLeaseConflict))
			continue
		}
		ttl := request.TTL
		if ttl <= 0 || ttl > time.Minute {
			ttl = 30 * time.Second
		}
		if resourceID, found := activeByScope[scope]; found {
			record := next[resourceID]
			if record.Lease.OperationID != request.OperationID || record.Lease.OwnerID != request.OwnerID {
				failures = append(failures, fmt.Errorf("cluster %s: %w: active quorum lease belongs to another operation", request.ClusterID, endpoint.ErrLeaseConflict))
				continue
			}
			// The ownership keeper runs more frequently than the lease TTL so it
			// can react quickly to a changed owner. Do not replicate an otherwise
			// identical lease until half of its TTL has elapsed.
			if record.Lease.ExpiresAt.Sub(now) > ttl/2 {
				continue
			}
			record.Lease.ExpiresAt = now.Add(ttl)
			record.UpdatedAt = now
			next[resourceID] = record
			changed = true
			continue
		}
		if request.RenewOnly {
			failures = append(failures, fmt.Errorf("cluster %s: %w: stable quorum lease is missing or expired", request.ClusterID, endpoint.ErrLeaseConflict))
			continue
		}
		lease := endpoint.Lease{
			ResourceID: model.NewResourceID(), ClusterID: request.ClusterID, HAEndpointID: request.HAEndpointID,
			OperationID: request.OperationID, OwnerID: request.OwnerID, ExpiresAt: now.Add(ttl), Active: true,
		}
		next[lease.ResourceID] = LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}
		activeByScope[scope] = lease.ResourceID
		changed = true
	}
	if changed {
		records := make([]LeaseRecord, 0, len(next))
		for _, record := range next {
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].Lease.ResourceID < records[j].Lease.ResourceID })
		if err := store.records.ReplaceCoordinationLeases(records); err != nil {
			failures = append(failures, fmt.Errorf("persist stable quorum lease batch: %w", err))
		}
	}
	return errors.Join(failures...)
}

func leaseScope(clusterID, endpointID model.ResourceID) string {
	return string(clusterID) + "\x00" + string(endpointID)
}

func (store *LeaseStore) Validate(ctx context.Context, lease endpoint.Lease) error {
	if err := store.authorize(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, record := range store.records.CoordinationLeases() {
		if record.Lease.ResourceID == lease.ResourceID && record.Lease.Active && record.Lease.ExpiresAt.After(store.now().UTC()) && endpoint.SameLeaseIdentity(record.Lease, lease) {
			return nil
		}
	}
	return fmt.Errorf("%w: quorum lease is missing, expired, or changed", endpoint.ErrLeaseConflict)
}

func (store *LeaseStore) Current(ctx context.Context, clusterID, haEndpointID model.ResourceID) (endpoint.Lease, error) {
	if err := store.authorize(ctx); err != nil {
		return endpoint.Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	var selected endpoint.Lease
	for _, record := range store.records.CoordinationLeases() {
		lease := record.Lease
		if !lease.Active || !lease.ExpiresAt.After(now) || lease.ClusterID != clusterID || lease.HAEndpointID != haEndpointID {
			continue
		}
		if selected.ResourceID != "" {
			return endpoint.Lease{}, fmt.Errorf("%w: multiple active quorum leases", endpoint.ErrLeaseConflict)
		}
		selected = lease
	}
	if selected.ResourceID == "" {
		return endpoint.Lease{}, fmt.Errorf("%w: active quorum lease is missing", endpoint.ErrLeaseConflict)
	}
	return selected, nil
}

func (store *LeaseStore) FinalizeTransition(ctx context.Context, transition endpoint.Lease, ttl time.Duration) (endpoint.Lease, error) {
	if err := store.authorize(ctx); err != nil {
		return endpoint.Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	for _, record := range store.records.CoordinationLeases() {
		current := record.Lease
		if current.ResourceID != transition.ResourceID {
			continue
		}
		if !current.Active || !current.ExpiresAt.After(now) || !endpoint.SameLeaseIdentity(current, transition) || current.OperationID == current.HAEndpointID {
			break
		}
		if ttl <= 0 || ttl > time.Minute {
			ttl = 30 * time.Second
		}
		current.OperationID = current.HAEndpointID
		current.PreviousOwnerID = ""
		current.ExpiresAt = now.Add(ttl)
		record.Lease = current
		record.UpdatedAt = now
		if err := store.records.PutCoordinationLease(record); err != nil {
			return endpoint.Lease{}, fmt.Errorf("finalize quorum transition lease: %w", err)
		}
		return current, nil
	}
	return endpoint.Lease{}, fmt.Errorf("%w: transition quorum lease is missing, expired, changed, or already stable", endpoint.ErrLeaseConflict)
}

func (store *LeaseStore) RollbackTransition(ctx context.Context, transition endpoint.Lease, ttl time.Duration) (endpoint.Lease, error) {
	if err := store.authorize(ctx); err != nil {
		return endpoint.Lease{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	for _, record := range store.records.CoordinationLeases() {
		current := record.Lease
		if current.ResourceID != transition.ResourceID {
			continue
		}
		if !current.Active || !current.ExpiresAt.After(now) || !endpoint.SameLeaseIdentity(current, transition) ||
			current.OperationID == current.HAEndpointID || !model.ValidResourceID(current.PreviousOwnerID) {
			break
		}
		if ttl <= 0 || ttl > time.Minute {
			ttl = 30 * time.Second
		}
		current.OperationID = current.HAEndpointID
		current.OwnerID = current.PreviousOwnerID
		current.PreviousOwnerID = ""
		current.ExpiresAt = now.Add(ttl)
		record.Lease = current
		record.UpdatedAt = now
		if err := store.records.PutCoordinationLease(record); err != nil {
			return endpoint.Lease{}, fmt.Errorf("rollback quorum transition lease: %w", err)
		}
		return current, nil
	}
	return endpoint.Lease{}, fmt.Errorf("%w: transition quorum lease is missing, expired, changed, or cannot be rolled back", endpoint.ErrLeaseConflict)
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
