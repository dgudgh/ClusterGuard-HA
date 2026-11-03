package coordination

import (
	"context"
	"errors"
	"testing"
	"time"
)

type ownershipReadBarrier struct {
	ownershipAuthorityStub
	apply func()
	err   error
	reads int
}

func (a *ownershipReadBarrier) SynchronizeRead(context.Context) error {
	a.reads++
	if a.err != nil {
		return a.err
	}
	if a.apply != nil {
		a.apply()
		a.apply = nil
	}
	return nil
}
func TestOwnershipRenewalReadsAppliedStateBeforeObserving(t *testing.T) {
	now := time.Now().UTC()
	inventory, observer, leases, primary, _ := ownershipKeeperFixture(now)
	snapshot := inventory.snapshots[primary.ClusterID]
	delete(inventory.snapshots, primary.ClusterID)
	authority := &ownershipReadBarrier{apply: func() { inventory.snapshots[primary.ClusterID] = snapshot }}
	keeper := NewOwnershipKeeper(inventory, observer, leases, authority, func() time.Time { return now }, time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil || authority.reads != 1 || len(leases.requests) != 1 {
		t.Fatalf("renewal read old pre-replay snapshot: err=%v reads=%d requests=%d", err, authority.reads, len(leases.requests))
	}
	calls := observer.calls
	authority.err = errors.New("barrier failed")
	if err := keeper.RunOnce(context.Background()); err == nil || observer.calls != calls || len(leases.requests) != 1 {
		t.Fatal("barrier failure still observed/mutated ownership")
	}
	// Read synchronization never replaces the original majority check.
	authority.err = nil
	authority.ownershipAuthorityStub.err = errors.New("minority")
	n := authority.reads
	if err := keeper.RunOnce(context.Background()); err != nil || authority.reads != n {
		t.Fatalf("minority attempted authorization read: %v", err)
	}
}
