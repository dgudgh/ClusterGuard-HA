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

func (gate TopologyDiscovery) CaptureObservation(ctx context.Context, operation model.Operation) (ObservationToken, error) {
	if err := ctx.Err(); err != nil {
		return ObservationToken{}, err
	}
	if gate.Reader == nil {
		return ObservationToken{}, fmt.Errorf("topology reader is not configured")
	}
	if operation.ClusterID == "" {
		return ObservationToken{}, fmt.Errorf("cluster ID is required for discovery validation")
	}
	snapshot, found := gate.Reader.TopologySnapshot(operation.ClusterID)
	if !found || snapshot.ObservedAt.IsZero() {
		return ObservationToken{}, fmt.Errorf("a current topology observation is required")
	}
	if snapshot.ClusterID != "" && snapshot.ClusterID != operation.ClusterID {
		return ObservationToken{}, fmt.Errorf("topology observation belongs to another cluster")
	}
	return ObservationToken{ClusterID: operation.ClusterID, ObservedAt: snapshot.ObservedAt.UTC()}, nil
}

func (gate TopologyDiscovery) RevalidateObservation(ctx context.Context, operation model.Operation, token ObservationToken) error {
	if token.ClusterID != operation.ClusterID || token.ObservedAt.IsZero() {
		return fmt.Errorf("topology observation token does not match the operation")
	}
	current, err := gate.CaptureObservation(ctx, operation)
	if err != nil {
		return err
	}
	if current.ClusterID != token.ClusterID || !current.ObservedAt.Equal(token.ObservedAt) {
		return fmt.Errorf("topology observation changed")
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

func (locks *MemoryLocks) Acquire(ctx context.Context, operation model.Operation) (func(), error) {
	key := string(operation.ClusterID)
	if key == "" {
		key = string(operation.ResourceID)
	}
	return locks.acquire(ctx, key)
}

func (locks *MemoryLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (func(), error) {
	return locks.acquire(ctx, string(clusterID))
}

func (locks *MemoryLocks) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("operation lock resource is required")
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
