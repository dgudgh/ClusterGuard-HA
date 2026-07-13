package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestMemoryLeaseStoreRejectsConflictingOwnerUntilExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	store := NewMemoryLeaseStore(func() time.Time { return now })
	request := LeaseRequest{ClusterID: model.NewResourceID(), HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: model.NewResourceID(), TTL: 30 * time.Second}
	first, err := store.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	request.OperationID = model.NewResourceID()
	request.OwnerID = model.NewResourceID()
	if _, err := store.Acquire(context.Background(), request); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("conflicting lease error=%v", err)
	}
	if err := store.Validate(context.Background(), first); err != nil {
		t.Fatalf("validate active lease: %v", err)
	}
}
