package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestRecoveryRuntimeLockAllowsPublicationAndRetainsQuorumExclusion(t *testing.T) {
	repo := store.NewMemory()
	authority := runtimeFailoverAuthority{}
	locks := newRuntimeLocks(repo, authority)
	manager := newDisasterManager(repo, authority, locks, nil, nil)
	op := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	leaseCtx, release, err := manager.Locks.Acquire(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !model.ValidResourceID(adapter.OperationLeaseID(leaseCtx)) {
		t.Fatal("missing durable recovery lease")
	}
	_, publish, err := locks.publication.AcquireCluster(ctx, op.ClusterID)
	if err != nil {
		t.Fatalf("recovery blocked its own verification: %v", err)
	}
	publish()
	_, other, err := locks.operations.Acquire(ctx, model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: op.ClusterID})
	if err == nil {
		other()
		t.Fatal("concurrent mutation accepted")
	}
	if !errors.Is(err, coordination.ErrOperationLockConflict) {
		t.Fatalf("unexpected exclusion failure: %v", err)
	}
}
