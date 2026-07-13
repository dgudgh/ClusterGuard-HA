package workflow

import (
	"context"
	"fmt"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type AuthoritySafetyGuard struct {
	Authority MutationAuthority
}

func (guard AuthoritySafetyGuard) Evaluate(ctx context.Context, operation model.Operation) error {
	if guard.Authority == nil {
		return fmt.Errorf("mutation authority safety guard is not configured")
	}
	if !model.ValidResourceID(operation.ResourceID) || !model.ValidResourceID(operation.ClusterID) {
		return fmt.Errorf("operation and cluster UUIDs are required by the safety guard")
	}
	if err := guard.Authority.RequireMutationAuthority(ctx); err != nil {
		return fmt.Errorf("leader-backed controller majority is required: %w", err)
	}
	return nil
}

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

type ClusterLockManager interface {
	Acquire(context.Context, model.Operation) (func(), error)
	AcquireCluster(context.Context, model.ResourceID) (func(), error)
}

type CompositeLocks struct {
	local  ClusterLockManager
	quorum ClusterLockManager
}

func NewCompositeLocks(local, quorum ClusterLockManager) *CompositeLocks {
	return &CompositeLocks{local: local, quorum: quorum}
}

func (locks *CompositeLocks) Acquire(ctx context.Context, operation model.Operation) (func(), error) {
	if locks == nil || locks.local == nil || locks.quorum == nil {
		return nil, fmt.Errorf("composite operation lock is not configured")
	}
	return acquireComposite(
		func() (func(), error) { return locks.local.Acquire(ctx, operation) },
		func() (func(), error) { return locks.quorum.Acquire(ctx, operation) },
	)
}

func (locks *CompositeLocks) AcquireCluster(ctx context.Context, clusterID model.ResourceID) (func(), error) {
	if locks == nil || locks.local == nil || locks.quorum == nil {
		return nil, fmt.Errorf("composite operation lock is not configured")
	}
	return acquireComposite(
		func() (func(), error) { return locks.local.AcquireCluster(ctx, clusterID) },
		func() (func(), error) { return locks.quorum.AcquireCluster(ctx, clusterID) },
	)
}

func acquireComposite(acquireLocal, acquireQuorum func() (func(), error)) (func(), error) {
	releaseLocal, err := acquireLocal()
	if err != nil {
		return nil, err
	}
	releaseQuorum, err := acquireQuorum()
	if err != nil {
		releaseLocal()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseQuorum()
			releaseLocal()
		})
	}, nil
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
	var once sync.Once
	return func() {
		once.Do(func() {
			locks.mu.Lock()
			defer locks.mu.Unlock()
			delete(locks.active, key)
		})
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
