package coordination

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

var ErrOperationLockConflict = errors.New("cluster operation lock is already held")
var ErrOperationLockLeaseLost = errors.New("cluster operation lock lease was lost")

type operationLockLeaseLostError struct {
	cause error
}

func (failure *operationLockLeaseLostError) Error() string {
	return fmt.Sprintf("%s: %v", ErrOperationLockLeaseLost, failure.cause)
}

func (failure *operationLockLeaseLostError) Unwrap() error { return failure.cause }

func (failure *operationLockLeaseLostError) Is(target error) bool {
	return target == ErrOperationLockLeaseLost || errors.Is(failure.cause, target)
}

func (failure *operationLockLeaseLostError) FailureClass() string { return "lock_lease_lost" }

type OperationLockRecord struct {
	ResourceID  model.ResourceID `json:"resource_id"`
	ClusterID   model.ResourceID `json:"cluster_id"`
	OperationID model.ResourceID `json:"operation_id"`
	ExpiresAt   time.Time        `json:"expires_at"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type OperationLockRecordStore interface {
	CoordinationOperationLocks() []OperationLockRecord
	PutCoordinationOperationLock(OperationLockRecord) error
	DeleteCoordinationOperationLock(model.ResourceID) error
}

type OperationLocks struct {
	mu        sync.Mutex
	records   OperationLockRecordStore
	authority MutationAuthority
	ttl       time.Duration
	now       func() time.Time
}

func NewOperationLocks(records OperationLockRecordStore, authority MutationAuthority, ttl time.Duration, now func() time.Time) *OperationLocks {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &OperationLocks{records: records, authority: authority, ttl: ttl, now: now}
}

func (locks *OperationLocks) Acquire(ctx context.Context, operation model.Operation) (context.Context, func(), error) {
	if !model.ValidResourceID(operation.ResourceID) {
		return nil, nil, fmt.Errorf("durable operation UUID is required for the cluster lock")
	}
	return locks.acquire(ctx, operation.ClusterID, operation.ResourceID)
}

func (locks *OperationLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (context.Context, func(), error) {
	return locks.acquire(ctx, clusterID, model.NewResourceID())
}

func (locks *OperationLocks) acquire(ctx context.Context, clusterID, operationID model.ResourceID) (context.Context, func(), error) {
	if locks == nil || locks.records == nil || locks.authority == nil {
		return nil, nil, fmt.Errorf("quorum operation lock is not configured")
	}
	if !model.ValidResourceID(clusterID) || !model.ValidResourceID(operationID) {
		return nil, nil, fmt.Errorf("cluster and operation UUIDs are required for the operation lock")
	}
	if err := locks.authority.RequireMutationAuthority(ctx); err != nil {
		return nil, nil, err
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	now := locks.now().UTC()
	for _, record := range locks.records.CoordinationOperationLocks() {
		if !record.ExpiresAt.After(now) {
			if err := locks.records.DeleteCoordinationOperationLock(record.ResourceID); err != nil {
				return nil, nil, fmt.Errorf("expire abandoned operation lock: %w", err)
			}
			continue
		}
		if record.ClusterID == clusterID {
			return nil, nil, fmt.Errorf("%w for cluster %s", ErrOperationLockConflict, clusterID)
		}
	}
	record := OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: clusterID, OperationID: operationID,
		ExpiresAt: now.Add(locks.ttl), CreatedAt: now, UpdatedAt: now,
	}
	if err := locks.records.PutCoordinationOperationLock(record); err != nil {
		return nil, nil, fmt.Errorf("persist quorum operation lock: %w", err)
	}
	leaseCtx, cancelLease := context.WithCancelCause(ctx)
	leaseCtx = adapter.WithOperationLeaseID(leaseCtx, record.ResourceID)
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	go locks.renew(leaseCtx, record, stopRenewal, renewalDone, cancelLease)
	var once sync.Once
	return leaseCtx, func() {
		once.Do(func() {
			close(stopRenewal)
			<-renewalDone
			locks.mu.Lock()
			defer locks.mu.Unlock()
			_ = locks.records.DeleteCoordinationOperationLock(record.ResourceID)
			cancelLease(context.Canceled)
		})
	}, nil
}

func (locks *OperationLocks) renew(leaseCtx context.Context, record OperationLockRecord, stop <-chan struct{}, done chan<- struct{}, cancelLease context.CancelCauseFunc) {
	defer close(done)
	interval := locks.ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval >= locks.ttl {
		interval = locks.ttl / 2
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-leaseCtx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			err := locks.authority.RequireMutationAuthority(ctx)
			cancel()
			if err != nil {
				cancelLease(&operationLockLeaseLostError{cause: fmt.Errorf("renewal authority check failed: %w", err)})
				return
			}
			locks.mu.Lock()
			found := false
			for _, current := range locks.records.CoordinationOperationLocks() {
				if current.ResourceID == record.ResourceID && current.ClusterID == record.ClusterID && current.OperationID == record.OperationID {
					found = true
					break
				}
			}
			if !found {
				locks.mu.Unlock()
				cancelLease(&operationLockLeaseLostError{cause: errors.New("persisted lock record disappeared")})
				return
			}
			now := locks.now().UTC()
			record.ExpiresAt = now.Add(locks.ttl)
			record.UpdatedAt = now
			err = locks.records.PutCoordinationOperationLock(record)
			locks.mu.Unlock()
			if err != nil {
				cancelLease(&operationLockLeaseLostError{cause: fmt.Errorf("persist renewed lock: %w", err)})
				return
			}
		}
	}
}
