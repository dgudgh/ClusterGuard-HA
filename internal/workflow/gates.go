package workflow

import (
	"context"
	"fmt"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

type TopologyReader interface {
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
}

type TopologyDiscovery struct {
	Reader TopologyReader
}

func (gate TopologyDiscovery) RequireObservation(ctx context.Context, operation model.Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if gate.Reader == nil {
		return fmt.Errorf("topology reader is not configured")
	}
	if operation.ClusterID == "" {
		return fmt.Errorf("cluster ID is required for discovery validation")
	}
	snapshot, found := gate.Reader.TopologySnapshot(operation.ClusterID)
	if !found || snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("a current topology observation is required")
	}
	if snapshot.ClusterID != "" && snapshot.ClusterID != operation.ClusterID {
		return fmt.Errorf("topology observation belongs to another cluster")
	}
	return nil
}

type AllowAllSafety struct{}

func (AllowAllSafety) Evaluate(context.Context, model.Operation) error { return nil }

type MemoryLocks struct {
	mu     sync.Mutex
	active map[string]bool
}

func NewMemoryLocks() *MemoryLocks { return &MemoryLocks{active: map[string]bool{}} }

func (locks *MemoryLocks) Acquire(_ context.Context, operation model.Operation) (func(), error) {
	key := string(operation.ClusterID)
	if key == "" {
		key = string(operation.ResourceID)
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if locks.active[key] {
		return nil, fmt.Errorf("an operation lock is already active for this resource")
	}
	locks.active[key] = true
	return func() {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		delete(locks.active, key)
	}, nil
}

type TokenApproval struct {
	ExpectedToken string
}

func (approval TokenApproval) Validate(_ context.Context, _ model.Operation, token string) error {
	if token == "" {
		return fmt.Errorf("approval token is required")
	}
	if approval.ExpectedToken != "" && token != approval.ExpectedToken {
		return fmt.Errorf("approval token is invalid")
	}
	return nil
}
