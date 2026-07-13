package coordination

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/model"
)

var ErrOperationLockConflict = errors.New("cluster operation lock is already held")

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

func (locks *OperationLocks) Acquire(ctx context.Context, operation model.Operation) (func(), error) {
	if !model.ValidResourceID(operation.ResourceID) {
		return nil, fmt.Errorf("durable operation UUID is required for the cluster lock")
	}
	return locks.acquire(ctx, operation.ClusterID, operation.ResourceID)
}

func (locks *OperationLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (func(), error) {
	return locks.acquire(ctx, clusterID, model.NewResourceID())
}

func (locks *OperationLocks) acquire(ctx context.Context, clusterID, operationID model.ResourceID) (func(), error) {
	if locks == nil || locks.records == nil || locks.authority == nil {
		return nil, fmt.Errorf("quorum operation lock is not configured")
	}
	if !model.ValidResourceID(clusterID) || !model.ValidResourceID(operationID) {
		return nil, fmt.Errorf("cluster and operation UUIDs are required for the operation lock")
	}
	if err := locks.authority.RequireMutationAuthority(ctx); err != nil {
		return nil, err
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	now := locks.now().UTC()
	for _, record := range locks.records.CoordinationOperationLocks() {
		if !record.ExpiresAt.After(now) {
			if err := locks.records.DeleteCoordinationOperationLock(record.ResourceID); err != nil {
				return nil, fmt.Errorf("expire abandoned operation lock: %w", err)
			}
			continue
		}
		if record.ClusterID == clusterID {
			return nil, fmt.Errorf("%w for cluster %s", ErrOperationLockConflict, clusterID)
		}
	}
	record := OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: clusterID, OperationID: operationID,
		ExpiresAt: now.Add(locks.ttl), CreatedAt: now, UpdatedAt: now,
	}
	if err := locks.records.PutCoordinationOperationLock(record); err != nil {
		return nil, fmt.Errorf("persist quorum operation lock: %w", err)
	}
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	go locks.renew(record, stopRenewal, renewalDone)
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopRenewal)
			<-renewalDone
			locks.mu.Lock()
			defer locks.mu.Unlock()
			_ = locks.records.DeleteCoordinationOperationLock(record.ResourceID)
		})
	}, nil
}

func (locks *OperationLocks) renew(record OperationLockRecord, stop <-chan struct{}, done chan<- struct{}) {
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
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			err := locks.authority.RequireMutationAuthority(ctx)
			cancel()
			if err != nil {
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
				return
			}
			now := locks.now().UTC()
			record.ExpiresAt = now.Add(locks.ttl)
			record.UpdatedAt = now
			err = locks.records.PutCoordinationOperationLock(record)
			locks.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}
