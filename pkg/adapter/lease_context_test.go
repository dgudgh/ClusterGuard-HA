package adapter

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestOperationLeaseIDRoundTrip(t *testing.T) {
	leaseID := model.NewResourceID()
	ctx := WithOperationLeaseID(context.Background(), leaseID)
	if got := OperationLeaseID(ctx); got != leaseID {
		t.Fatalf("operation lease ID=%q want=%q", got, leaseID)
	}
	if got := OperationLeaseID(context.Background()); got != "" {
		t.Fatalf("empty context operation lease ID=%q", got)
	}
}
