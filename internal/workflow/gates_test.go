package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type compositeLockStub struct {
	name    string
	calls   *[]string
	failure error
}

type workflowAuthorityStub struct{ err error }

func (stub workflowAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

func (stub compositeLockStub) Acquire(context.Context, model.Operation) (func(), error) {
	return stub.acquire()
}

func (stub compositeLockStub) AcquireCluster(context.Context, model.ResourceID) (func(), error) {
	return stub.acquire()
}

func (stub compositeLockStub) acquire() (func(), error) {
	*stub.calls = append(*stub.calls, "acquire:"+stub.name)
	if stub.failure != nil {
		return nil, stub.failure
	}
	return func() { *stub.calls = append(*stub.calls, "release:"+stub.name) }, nil
}

func TestMemoryLockReleaseIsIdempotent(t *testing.T) {
	locks := NewMemoryLocks()
	clusterID := model.NewResourceID()
	firstRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	firstRelease()
	secondRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("acquire second lock: %v", err)
	}
	defer secondRelease()

	firstRelease()
	thirdRelease, err := locks.AcquireCluster(context.Background(), clusterID)
	if err == nil {
		thirdRelease()
		t.Fatal("a repeated stale release removed the active replacement lock")
	}
}

func TestCompositeLocksAcquireLocalThenQuorumAndReleaseInReverse(t *testing.T) {
	calls := []string{}
	locks := NewCompositeLocks(
		compositeLockStub{name: "local", calls: &calls},
		compositeLockStub{name: "quorum", calls: &calls},
	)
	release, err := locks.Acquire(context.Background(), model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()})
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
	if _, err := locks.AcquireCluster(context.Background(), model.NewResourceID()); err == nil {
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
