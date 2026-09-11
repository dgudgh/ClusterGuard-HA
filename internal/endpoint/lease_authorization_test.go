package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type observedLeaseStore struct {
	*MemoryLeaseStore
	renewals chan Lease
}

func (s *observedLeaseStore) Acquire(ctx context.Context, request LeaseRequest) (Lease, error) {
	lease, err := s.MemoryLeaseStore.Acquire(ctx, request)
	if err == nil && request.RenewOnly {
		s.renewals <- lease
	}
	return lease, err
}

func TestTransitionFinalizeKeepsStableRenewalAndFailsClosedOnLoss(t *testing.T) {
	s := &observedLeaseStore{MemoryLeaseStore: NewMemoryLeaseStore(nil), renewals: make(chan Lease, 100)}
	endpointID := model.NewResourceID()
	resolved := adapter.ResolvedOperation{OperationID: model.NewResourceID(),
		Cluster: model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
		Primary: model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
		Target:  model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
	}
	auth, err := authorizeTransitionLease(context.Background(), s, resolved, endpointID, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cancel()
	if err := auth.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := auth.Finalize(context.Background()); err != nil {
		t.Fatalf("repeat finalize: %v", err)
	}
	if err := auth.Abort(context.Background()); err == nil {
		t.Fatal("abort accepted after finalize")
	}
	deadline := time.After(time.Second)
	for renewed := 0; renewed < 3; {
		select {
		case lease := <-s.renewals:
			if lease.OperationID != endpointID {
				continue
			}
			if lease.ResourceID != auth.LeaseID || lease.PreviousOwnerID != "" || lease.OwnerID != resolved.Target.ResourceID {
				t.Fatalf("changed stable identity: %+v", lease)
			}
			renewed++
		case <-auth.Context.Done():
			t.Fatalf("finalized authorization canceled: %v", context.Cause(auth.Context))
		case <-deadline:
			t.Fatal("stable lease was not renewed")
		}
	}
	if err := s.Release(context.Background(), auth.LeaseID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-auth.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("lost lease did not revoke authorization")
	}
	if _, err := s.Current(context.Background(), resolved.Cluster.ResourceID, endpointID); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("renewal recreated lost ownership: %v", err)
	}
}

func TestMemorySingleAcquireRenewOnlyNeverCreatesOrHandsOff(t *testing.T) {
	now := time.Now()
	s := NewMemoryLeaseStore(func() time.Time { return now })
	req := LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: time.Second, RenewOnly: true}
	req.OperationID = req.HAEndpointID
	if _, err := s.Acquire(context.Background(), req); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("missing renew-only: %v", err)
	}
	req.RenewOnly = false
	seed, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.RenewOnly = true
	if got, err := s.Acquire(context.Background(), req); err != nil || !SameLeaseIdentity(seed, got) {
		t.Fatalf("existing renewal: %+v %v", got, err)
	}
	handoff := req
	handoff.OperationID, handoff.PreviousOwnerID, handoff.OwnerID = model.NewResourceID(), req.OwnerID, model.NewResourceID()
	if _, err := s.Acquire(context.Background(), handoff); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("renew-only handed off: %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := s.Acquire(context.Background(), req); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expired renew-only: %v", err)
	}
}
