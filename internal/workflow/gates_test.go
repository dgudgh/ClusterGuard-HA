package workflow

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

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
