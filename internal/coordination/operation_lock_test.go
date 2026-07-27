package coordination

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type operationLockAuthorityStub struct{ err error }

func (stub operationLockAuthorityStub) RequireMutationAuthority(context.Context) error {
	return stub.err
}

type mutableOperationLockAuthorityStub struct {
	mu  sync.Mutex
	err error
}

func (stub *mutableOperationLockAuthorityStub) RequireMutationAuthority(context.Context) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.err
}

func (stub *mutableOperationLockAuthorityStub) fail(err error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.err = err
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

	_, release, err := locks.Acquire(context.Background(), first)
	if err != nil {
		t.Fatalf("acquire first operation lock: %v", err)
	}
	if _, _, err := locks.Acquire(context.Background(), second); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("concurrent operation lock error=%v", err)
	}
	release()
	_, secondRelease, err := locks.Acquire(context.Background(), second)
	if err != nil {
		t.Fatalf("acquire operation lock after release: %v", err)
	}
	secondRelease()
}

func TestOperationLocksExposeDurableLeaseIDToAdapterContext(t *testing.T) {
	records := newOperationLockRecordStoreStub()
	locks := NewOperationLocks(records, operationLockAuthorityStub{}, time.Minute, time.Now)
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
	}
	leaseCtx, release, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire operation lock: %v", err)
	}
	defer release()
	leaseID := adapter.OperationLeaseID(leaseCtx)
	if !model.ValidResourceID(leaseID) {
		t.Fatalf("durable lease ID missing from adapter context: %q", leaseID)
	}
	records.mu.Lock()
	record, found := records.records[leaseID]
	records.mu.Unlock()
	if !found || record.OperationID != operation.ResourceID || record.ClusterID != operation.ClusterID {
		t.Fatalf("context lease does not identify persisted lock: %+v found=%t", record, found)
	}
}

func TestOperationLocksRequireMajorityAndExpireAbandonedOwner(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	records := newOperationLockRecordStoreStub()
	clusterID := model.NewResourceID()
	blocked := NewOperationLocks(records, operationLockAuthorityStub{err: errors.New("no quorum")}, time.Minute, func() time.Time { return now })
	if _, _, err := blocked.AcquireCluster(context.Background(), clusterID); err == nil {
		t.Fatal("operation lock was granted without controller majority")
	}

	locks := NewOperationLocks(records, operationLockAuthorityStub{}, time.Minute, func() time.Time { return now })
	if _, _, err := locks.AcquireCluster(context.Background(), clusterID); err != nil {
		t.Fatalf("acquire abandoned lock: %v", err)
	}
	now = now.Add(61 * time.Second)
	_, release, err := locks.AcquireCluster(context.Background(), clusterID)
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
	_, firstRelease, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire first operation lock: %v", err)
	}
	if _, _, err := locks.Acquire(context.Background(), operation); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("same durable operation acquired the lock twice: %v", err)
	}
	firstRelease()
}

func TestOperationLocksRenewWhileTheHolderIsAlive(t *testing.T) {
	records := newOperationLockRecordStoreStub()
	locks := NewOperationLocks(records, operationLockAuthorityStub{}, 120*time.Millisecond, time.Now)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	_, release, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire renewable operation lock: %v", err)
	}
	time.Sleep(260 * time.Millisecond)
	if _, _, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: operation.ClusterID}); !errors.Is(err, ErrOperationLockConflict) {
		t.Fatalf("live operation lock expired instead of renewing: %v", err)
	}
	release()
	_, replacement, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: operation.ClusterID})
	if err != nil {
		t.Fatalf("released renewable lock blocked replacement: %v", err)
	}
	replacement()
}

func TestOperationLocksCancelLeaseContextWhenRenewalLosesAuthority(t *testing.T) {
	records := newOperationLockRecordStoreStub()
	authority := &mutableOperationLockAuthorityStub{}
	locks := NewOperationLocks(records, authority, 60*time.Millisecond, time.Now)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}

	leaseCtx, release, err := locks.Acquire(context.Background(), operation)
	if err != nil {
		t.Fatalf("acquire renewable operation lock: %v", err)
	}
	defer release()
	authority.fail(errors.New("leader majority lost"))

	select {
	case <-leaseCtx.Done():
		cause := context.Cause(leaseCtx)
		if !errors.Is(cause, ErrOperationLockLeaseLost) || !strings.Contains(cause.Error(), "leader majority lost") {
			t.Fatalf("lease cancellation cause=%v", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("operation lease context was not canceled after renewal lost mutation authority")
	}
}
