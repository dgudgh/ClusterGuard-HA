package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type compositeLockStub struct {
	name    string
	calls   *[]string
	failure error
}

type workflowAuthorityStub struct{ err error }

func (stub workflowAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

type workflowMaintenanceStub struct{ err error }

func (stub workflowMaintenanceStub) Check(context.Context) error { return stub.err }

func (stub compositeLockStub) Acquire(ctx context.Context, _ model.Operation) (context.Context, func(), error) {
	return stub.acquire(ctx)
}

func (stub compositeLockStub) AcquireCluster(ctx context.Context, _ model.ResourceID) (context.Context, func(), error) {
	return stub.acquire(ctx)
}

func (stub compositeLockStub) acquire(ctx context.Context) (context.Context, func(), error) {
	*stub.calls = append(*stub.calls, "acquire:"+stub.name)
	if stub.failure != nil {
		return nil, nil, stub.failure
	}
	return ctx, func() { *stub.calls = append(*stub.calls, "release:"+stub.name) }, nil
}

func TestMemoryLockReleaseIsIdempotent(t *testing.T) {
	locks := NewMemoryLocks()
	clusterID := model.NewResourceID()
	firstContext, firstRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if !model.ValidResourceID(adapter.OperationLeaseID(firstContext)) {
		t.Fatal("memory lock did not attach a valid operation lease ID")
	}
	firstRelease()
	_, secondRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire second lock: %v", err)
	}
	defer secondRelease()

	firstRelease()
	blockedContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, thirdRelease, err := locks.AcquireCluster(blockedContext, clusterID); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			thirdRelease()
		}
		t.Fatalf("a repeated stale release removed the active replacement lock: %v", err)
	}
}

func TestMemoryLockWaitsForCurrentHolderAndHonorsContext(t *testing.T) {
	locks := NewMemoryLocks()
	clusterID := model.NewResourceID()
	_, firstRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}

	type result struct {
		release func()
		err     error
	}
	acquired := make(chan result, 1)
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		_, release, acquireErr := locks.AcquireCluster(waitContext, clusterID)
		acquired <- result{release: release, err: acquireErr}
	}()

	select {
	case value := <-acquired:
		if value.release != nil {
			value.release()
		}
		t.Fatalf("contending lock did not wait: %v", value.err)
	case <-time.After(20 * time.Millisecond):
	}

	firstRelease()
	select {
	case value := <-acquired:
		if value.err != nil {
			t.Fatalf("waiting lock failed after release: %v", value.err)
		}
		value.release()
	case <-time.After(time.Second):
		t.Fatal("waiting lock was not awakened after release")
	}
}

func TestMemoryLockGrantsContendersInArrivalOrder(t *testing.T) {
	locks := NewMemoryLocks()
	clusterID := model.NewResourceID()
	_, releaseHolder, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}

	type result struct {
		name    string
		release func()
		err     error
	}
	acquired := make(chan result, 2)
	queue := func(name string) {
		go func() {
			_, release, acquireErr := locks.AcquireCluster(context.Background(), clusterID)
			acquired <- result{name: name, release: release, err: acquireErr}
		}()
	}
	queue("discovery")
	waitForMemoryLockQueueLength(t, locks, clusterID, 1)
	queue("automatic-resume")
	waitForMemoryLockQueueLength(t, locks, clusterID, 2)

	releaseHolder()
	first := <-acquired
	if first.err != nil {
		t.Fatalf("first contender failed: %v", first.err)
	}
	if first.name != "discovery" {
		first.release()
		t.Fatalf("lock granted to %s before the older discovery waiter", first.name)
	}
	select {
	case unexpected := <-acquired:
		unexpected.release()
		first.release()
		t.Fatalf("second contender acquired before first released: %s", unexpected.name)
	case <-time.After(20 * time.Millisecond):
	}

	first.release()
	second := <-acquired
	if second.err != nil {
		t.Fatalf("second contender failed: %v", second.err)
	}
	if second.name != "automatic-resume" {
		second.release()
		t.Fatalf("unexpected second contender: %s", second.name)
	}
	second.release()
}

func TestMemoryLockCanceledWaiterDoesNotBlockNextContender(t *testing.T) {
	locks := NewMemoryLocks()
	clusterID := model.NewResourceID()
	_, releaseHolder, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, _, acquireErr := locks.AcquireCluster(canceledContext, clusterID)
		canceled <- acquireErr
	}()
	waitForMemoryLockQueueLength(t, locks, clusterID, 1)

	next := make(chan func(), 1)
	go func() {
		_, release, acquireErr := locks.AcquireCluster(context.Background(), clusterID)
		if acquireErr != nil {
			next <- nil
			return
		}
		next <- release
	}()
	waitForMemoryLockQueueLength(t, locks, clusterID, 2)
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error=%v, want context canceled", err)
	}
	waitForMemoryLockQueueLength(t, locks, clusterID, 1)

	releaseHolder()
	select {
	case release := <-next:
		if release == nil {
			t.Fatal("next contender failed after canceled waiter was removed")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("canceled waiter blocked the next contender")
	}
}

func waitForMemoryLockQueueLength(t *testing.T, locks *MemoryLocks, clusterID model.ResourceID, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		locks.mu.Lock()
		got := len(locks.waiters[string(clusterID)])
		locks.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("memory lock queue did not reach length %d", want)
}

func TestCompositeLocksAcquireLocalThenQuorumAndReleaseInReverse(t *testing.T) {
	calls := []string{}
	locks := NewCompositeLocks(
		compositeLockStub{name: "local", calls: &calls},
		compositeLockStub{name: "quorum", calls: &calls},
	)
	_, release, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()})
	if err != nil {
		t.Fatalf("acquire composite lock: %v", err)
	}
	release()
	want := []string{"acquire:local", "acquire:quorum", "release:quorum", "release:local"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("composite lock calls=%v, want %v", calls, want)
	}
}

func TestCompositeLocksReleaseLocalWhenQuorumAcquireFails(t *testing.T) {
	calls := []string{}
	locks := NewCompositeLocks(
		compositeLockStub{name: "local", calls: &calls},
		compositeLockStub{name: "quorum", calls: &calls, failure: errors.New("no quorum")},
	)
	if _, _, err := locks.AcquireCluster(context.Background(), model.NewResourceID()); err == nil {
		t.Fatal("composite lock ignored quorum failure")
	}
	want := []string{"acquire:local", "acquire:quorum", "release:local"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("composite lock failure calls=%v, want %v", calls, want)
	}
}

func TestAuthoritySafetyGuardRequiresCurrentLeaderMajority(t *testing.T) {
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	if err := (AuthoritySafetyGuard{Authority: workflowAuthorityStub{}}).Evaluate(context.Background(), operation); err != nil {
		t.Fatalf("majority leader was blocked: %v", err)
	}
	if err := (AuthoritySafetyGuard{Authority: workflowAuthorityStub{err: errors.New("no quorum")}}).Evaluate(context.Background(), operation); err == nil {
		t.Fatal("safety guard allowed mutation without leader majority")
	}
	if err := (AuthoritySafetyGuard{}).Evaluate(context.Background(), operation); err == nil {
		t.Fatal("unconfigured authority safety guard allowed mutation")
	}
}

func TestCompositeSafetyGuardBlocksSoftwareUpdateMaintenance(t *testing.T) {
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	guard := NewCompositeSafetyGuard(
		AllowAllSafety{},
		MaintenanceSafetyGuard{Gate: workflowMaintenanceStub{err: errors.New("update active")}},
	)
	if err := guard.Evaluate(context.Background(), operation); err == nil {
		t.Fatal("software update maintenance failed open")
	}

	guard = NewCompositeSafetyGuard(AllowAllSafety{}, MaintenanceSafetyGuard{Gate: workflowMaintenanceStub{}})
	if err := guard.Evaluate(context.Background(), operation); err != nil {
		t.Fatalf("inactive maintenance blocked operation: %v", err)
	}
}
