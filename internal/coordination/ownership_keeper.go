package coordination

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type OwnershipInventory interface {
	Clusters() []model.DatabaseCluster
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
	HAEndpoints(model.ResourceID) []model.HAEndpoint
	Endpoint(model.ResourceID) (model.Endpoint, bool)
	CommitHAEndpointOwner(model.ResourceID, model.ResourceID, model.ResourceID, bool) error
}

type OwnershipObserver interface {
	ObserveOwnership(context.Context, model.DatabaseCluster, model.TopologySnapshot) (endpoint.OwnershipObservation, error)
}

type OwnershipLeaseStore interface {
	Acquire(context.Context, endpoint.LeaseRequest) (endpoint.Lease, error)
}

type OwnershipKeeper struct {
	inventory    OwnershipInventory
	observer     OwnershipObserver
	leases       OwnershipLeaseStore
	authority    MutationAuthority
	now          func() time.Time
	interval     time.Duration
	maxAge       time.Duration
	probeTimeout time.Duration
}

type OwnershipKeeperOption func(*OwnershipKeeper)

func WithOwnershipProbeTimeout(timeout time.Duration) OwnershipKeeperOption {
	return func(keeper *OwnershipKeeper) {
		if timeout > 0 {
			keeper.probeTimeout = timeout
		}
	}
}

func NewOwnershipKeeper(inventory OwnershipInventory, observer OwnershipObserver, leases OwnershipLeaseStore, authority MutationAuthority, now func() time.Time, interval, maxAge time.Duration, options ...OwnershipKeeperOption) *OwnershipKeeper {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if maxAge <= 0 {
		maxAge = 15 * time.Second
	}
	keeper := &OwnershipKeeper{inventory: inventory, observer: observer, leases: leases, authority: authority, now: now, interval: interval, maxAge: maxAge, probeTimeout: 6 * time.Second}
	for _, option := range options {
		if option != nil {
			option(keeper)
		}
	}
	return keeper
}

func writablePrimary(snapshot model.TopologySnapshot) (model.DatabaseInstance, error) {
	var primary model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if primary.ResourceID != "" {
			return model.DatabaseInstance{}, fmt.Errorf("topology has multiple primary instances")
		}
		primary = instance
	}
	if !model.ValidResourceID(primary.ResourceID) {
		return model.DatabaseInstance{}, fmt.Errorf("topology has no current primary")
	}
	if primary.Health.State != model.HealthHealthy || !strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["read_only"]), "false") || !strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["super_read_only"]), "false") {
		return model.DatabaseInstance{}, fmt.Errorf("current primary is not proven healthy and writable")
	}
	return primary, nil
}

func (keeper *OwnershipKeeper) reconcileCluster(ctx context.Context, cluster model.DatabaseCluster, now time.Time) error {
	snapshot, found := keeper.inventory.TopologySnapshot(cluster.ResourceID)
	if !found || snapshot.ObservedAt.IsZero() || now.Sub(snapshot.ObservedAt) > keeper.maxAge || snapshot.ObservedAt.After(now.Add(keeper.interval)) {
		return fmt.Errorf("cluster %s topology is stale or unavailable", cluster.ResourceID)
	}
	observation, err := keeper.observer.ObserveOwnership(ctx, cluster, snapshot)
	if err != nil {
		return fmt.Errorf("cluster %s VIP observation: %w", cluster.ResourceID, err)
	}
	if !observation.Complete || !model.ValidResourceID(observation.HAEndpointID) || len(observation.OwnerIDs) > 1 {
		return fmt.Errorf("cluster %s VIP ownership coverage is unsafe", cluster.ResourceID)
	}
	primary, primaryErr := writablePrimary(snapshot)
	bootstrap := false
	if primaryErr != nil {
		if len(observation.OwnerIDs) != 0 || observation.CanonicalOwnerID != observation.EndpointOwnerID {
			return fmt.Errorf("cluster %s reboot bootstrap requires zero VIP owners and matching canonical metadata", cluster.ResourceID)
		}
		primary, err = RebootBootstrapCandidate(snapshot, observation.CanonicalOwnerID, now, keeper.maxAge)
		if err != nil {
			return fmt.Errorf("cluster %s: %w; reboot bootstrap blocked: %v", cluster.ResourceID, primaryErr, err)
		}
		bootstrap = true
	}
	if len(observation.OwnerIDs) == 1 && observation.OwnerIDs[0] != primary.ResourceID {
		return fmt.Errorf("cluster %s VIP is owned by a non-primary instance", cluster.ResourceID)
	}
	if len(observation.OwnerIDs) == 0 && (observation.CanonicalOwnerID != primary.ResourceID || observation.EndpointOwnerID != primary.ResourceID) {
		return fmt.Errorf("cluster %s zero-owner bootstrap metadata does not select the current primary", cluster.ResourceID)
	}
	if bootstrap && len(observation.OwnerIDs) != 0 {
		return fmt.Errorf("cluster %s reboot bootstrap requires zero VIP owners", cluster.ResourceID)
	}
	healthy := len(observation.OwnerIDs) == 1
	if err := keeper.inventory.CommitHAEndpointOwner(cluster.ResourceID, observation.HAEndpointID, primary.ResourceID, healthy); err != nil {
		return fmt.Errorf("cluster %s commit VIP owner: %w", cluster.ResourceID, err)
	}
	_, err = keeper.leases.Acquire(ctx, endpoint.LeaseRequest{
		ClusterID: cluster.ResourceID, HAEndpointID: observation.HAEndpointID,
		OperationID: observation.HAEndpointID, OwnerID: primary.ResourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("cluster %s renew VIP ownership lease: %w", cluster.ResourceID, err)
	}
	return nil
}

func (keeper *OwnershipKeeper) RunOnce(ctx context.Context) error {
	if keeper == nil || keeper.inventory == nil || keeper.observer == nil || keeper.leases == nil || keeper.authority == nil {
		return fmt.Errorf("VIP ownership keeper is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := keeper.authority.RequireMutationAuthority(ctx); err != nil {
		return nil
	}
	now := keeper.now().UTC()
	var failures []error
	for _, cluster := range keeper.inventory.Clusters() {
		probeContext, cancel := context.WithTimeout(ctx, keeper.probeTimeout)
		err := keeper.reconcileCluster(probeContext, cluster, now)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (keeper *OwnershipKeeper) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	_ = keeper.RunOnce(ctx)
	ticker := time.NewTicker(keeper.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = keeper.RunOnce(ctx)
		}
	}
}
