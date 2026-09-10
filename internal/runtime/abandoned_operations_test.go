package runtime

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
)

type abandonedAuthorityStub struct{ err error }

func (stub abandonedAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

func TestAbandonedOperationReconcilerIsLeaderOnly(t *testing.T) {
	repository := store.NewMemory()
	service := workflow.New(nil, nil, nil, nil, nil, repository, workflow.WithOperationStore(repository))
	records, err := reconcileAbandonedOperations(context.Background(), repository, service, abandonedAuthorityStub{err: errors.New("not leader")})
	if err != nil || len(records) != 0 {
		t.Fatalf("follower reconciler mutated operations: records=%d err=%v", len(records), err)
	}
}
