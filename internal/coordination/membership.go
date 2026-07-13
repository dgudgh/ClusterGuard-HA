package coordination

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

var (
	ErrNoQuorum  = errors.New("controller quorum is unavailable")
	ErrNotLeader = errors.New("local controller is not the leader")
)

type Controller struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Healthy    bool             `json:"healthy"`
}

type Membership struct {
	mu          sync.RWMutex
	localID     model.ResourceID
	leaderID    model.ResourceID
	controllers map[model.ResourceID]Controller
}

func NewMembership(localID, leaderID model.ResourceID, controllers []Controller) (*Membership, error) {
	if !model.ValidResourceID(localID) || !model.ValidResourceID(leaderID) || len(controllers) < 3 || len(controllers)%2 == 0 {
		return nil, fmt.Errorf("controller membership must contain an odd set of at least three UUID-scoped controllers")
	}
	membership := &Membership{localID: localID, leaderID: leaderID, controllers: make(map[model.ResourceID]Controller, len(controllers))}
	for _, controller := range controllers {
		if !model.ValidResourceID(controller.ResourceID) {
			return nil, fmt.Errorf("controller resource ID is invalid")
		}
		if _, found := membership.controllers[controller.ResourceID]; found {
			return nil, fmt.Errorf("controller membership contains a duplicate resource")
		}
		membership.controllers[controller.ResourceID] = controller
	}
	if _, found := membership.controllers[localID]; !found {
		return nil, fmt.Errorf("local controller is outside membership")
	}
	if _, found := membership.controllers[leaderID]; !found {
		return nil, fmt.Errorf("leader is outside membership")
	}
	return membership, nil
}

func (membership *Membership) RequireMutationAuthority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	membership.mu.RLock()
	defer membership.mu.RUnlock()
	if membership.localID != membership.leaderID {
		return ErrNotLeader
	}
	healthy := 0
	for _, controller := range membership.controllers {
		if controller.Healthy {
			healthy++
		}
	}
	if healthy < len(membership.controllers)/2+1 {
		return ErrNoQuorum
	}
	return nil
}

func (membership *Membership) SetHealth(controllerID model.ResourceID, healthy bool) error {
	membership.mu.Lock()
	defer membership.mu.Unlock()
	controller, found := membership.controllers[controllerID]
	if !found {
		return fmt.Errorf("controller is outside membership")
	}
	controller.Healthy = healthy
	membership.controllers[controllerID] = controller
	return nil
}

func (membership *Membership) Majority() int {
	membership.mu.RLock()
	defer membership.mu.RUnlock()
	return len(membership.controllers)/2 + 1
}
