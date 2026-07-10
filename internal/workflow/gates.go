package workflow

import (
	"context"
	"fmt"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

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
