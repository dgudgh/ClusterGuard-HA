package coordination

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type operationLockAuthorityStub struct{ err error }

func (stub operationLockAuthorityStub) RequireMutationAuthority(context.Context) error {
	return stub.err
}

type operationLockRecordStoreStub struct {
	mu      sync.Mutex
	records map[model.ResourceID]OperationLockRecord
}

func newOperationLockRecordStoreStub() *operationLockRecordStoreStub {
	return &operationLockRecordStoreStub{records: make(map[model.ResourceID]OperationLockRecord)}
}

func (store *operationLockRecordStoreStub) CoordinationOperationLocks() []OperationLockRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]OperationLockRecord, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result
}

func (store *operationLockRecordStoreStub) PutCoordinationOperationLock(record OperationLockRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[record.ResourceID] = record
	return nil
}

func (store *operationLockRecordStoreStub) DeleteCoordinationOperationLock(resourceID model.ResourceID) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.records, resourceID)
	return nil
}

func TestOperationLocksRejectConcurrentClusterMutationAndReleaseCleanly(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	records := newOperationLockRecordStoreStub()
	locks := NewOperationLocks(records, operationLockAuthorityStub{}, time.Minute, func() time.Time { return now })
	clusterID := model.NewResourceID()
	first := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID}
	second := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID}

	release, err := locks.Acquire(context.Background(), first)
	if err != nil {
		t.Fatalf("acquire first operation lock: %v", err)
	}
	if _, err := locks.Acquire(context.Background(), second); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("concurrent operation lock error=%v", err)
	}
	release()
	secondRelease, err := locks.Acquire(context.Background(), second)
	if err != nil {
		t.Fatalf("acquire operation lock after release: %v", err)
	}
	secondRelease()
}

func TestOperationLocksRequireMajorityAndExpireAbandonedOwner(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	records := newOperationLockRecordStoreStub()
	clusterID := model.NewResourceID()
	blocked := NewOperationLocks(records, operationLockAuthorityStub{err: errors.New("no quorum")}, time.Minute, func() time.Time { return now })
	if _, err := blocked.AcquireCluster(context.Background(), clusterID); err == nil {
		t.Fatal("operation lock was granted without controller majority")
	}

	locks := NewOperationLocks(records, operationLockAuthorityStub{}, time.Minute, func() time.Time { return now })
	if _, err := locks.AcquireCluster(context.Background(), clusterID); err != nil {
		t.Fatalf("acquire abandoned lock: %v", err)
	}
	now = now.Add(61 * time.Second)
	release, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("expired operation lock blocked new owner: %v", err)
	}
	release()
}

func TestOperationLocksRejectDuplicateProcessForTheSameOperation(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	records := newOperationLockRecordStoreStub()
	locks := NewOperationLocks(records, operationLockAuthorityStub{}, time.Minute, func() time.Time { return now })
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	firstRelease, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire first operation lock: %v", err)
	}
	if _, err := locks.Acquire(context.Background(), operation); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("same durable operation acquired the lock twice: %v", err)
	}
	firstRelease()
}

func TestOperationLocksRenewWhileTheHolderIsAlive(t *testing.T) {
	records := newOperationLockRecordStoreStub()
	locks := NewOperationLocks(records, operationLockAuthorityStub{}, 120*time.Millisecond, time.Now)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	release, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire renewable operation lock: %v", err)
	}
	time.Sleep(260 * time.Millisecond)
	if _, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: operation.ClusterID}); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("live operation lock expired instead of renewing: %v", err)
	}
	release()
	replacement, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: operation.ClusterID})
	if err != nil {
		t.Fatalf("released renewable lock blocked replacement: %v", err)
	}
	replacement()
}
