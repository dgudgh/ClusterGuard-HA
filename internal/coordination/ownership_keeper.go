package coordination

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/observability"
	"clusterguard.io/ha/pkg/model"
)

type OwnershipInventory interface {
	Clusters() []model.DatabaseCluster
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
	HAEndpoints(model.ResourceID) []model.HAEndpoint
	Endpoint(model.ResourceID) (model.Endpoint, bool)
	CommitObservedHAEndpointOwner(model.ResourceID, model.ResourceID, uint64, uint64, model.ResourceID, bool) error
}

type OwnershipOperationInventory interface {
	Operations(model.ResourceID) []model.OperationRecord
}

type OwnershipObserver interface {
	ObserveOwnership(context.Context, model.DatabaseCluster, model.TopologySnapshot) (endpoint.OwnershipObservation, error)
}

type OwnershipLeaseStore interface {
	AcquireStableBatch(context.Context, []endpoint.LeaseRequest) error
}

type OwnershipKeeper struct {
	inventory     OwnershipInventory
	observer      OwnershipObserver
	leases        OwnershipLeaseStore
	authority     MutationAuthority
	now           func() time.Time
	interval      time.Duration
	maxAge        time.Duration
	probeTimeout  time.Duration
	onError       func(error)
	errorReminder *observability.ErrorReminder
}

type OwnershipKeeperOption func(*OwnershipKeeper)

func WithOwnershipProbeTimeout(timeout time.Duration) OwnershipKeeperOption {
	return func(keeper *OwnershipKeeper) {
		if timeout > 0 {
			keeper.probeTimeout = timeout
		}
	}
}

func WithOwnershipErrorHandler(handler func(error)) OwnershipKeeperOption {
	return func(keeper *OwnershipKeeper) {
		if handler != nil {
			keeper.onError = handler
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
	keeper := &OwnershipKeeper{
		inventory: inventory, observer: observer, leases: leases, authority: authority, now: now,
		interval: interval, maxAge: maxAge, probeTimeout: 6 * time.Second,
		onError:       func(err error) { log.Printf("VIP ownership reconciliation failed: %v", err) },
		errorReminder: observability.NewErrorReminder(5*time.Minute, now),
	}
	for _, option := range options {
		if option != nil {
			option(keeper)
		}
	}
	return keeper
}

func (keeper *OwnershipKeeper) reportError(err error) {
	if keeper == nil || errors.Is(err, context.Canceled) {
		return
	}
	if keeper.errorReminder != nil && !keeper.errorReminder.ShouldReport(err) {
		return
	}
	if err != nil && keeper.onError != nil {
		keeper.onError(err)
	}
}

func writablePrimary(snapshot model.TopologySnapshot) (model.DatabaseInstance, error) {
	var primary model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if instance.Health.State == model.HealthHealthy && primaryProvenWritable(instance) && currentSuccessfulProbe(snapshot, instance.ResourceID) {
			if primary.ResourceID != "" {
				return model.DatabaseInstance{}, fmt.Errorf("topology has multiple current writable primary instances")
			}
			primary = instance
			continue
		}
		if !currentFailedProbe(snapshot, instance.ResourceID) {
			return model.DatabaseInstance{}, fmt.Errorf("topology has an ambiguous additional primary instance: %s", instance.ResourceID)
		}
	}
	if !model.ValidResourceID(primary.ResourceID) {
		return model.DatabaseInstance{}, fmt.Errorf("topology has no current healthy writable primary")
	}
	return primary, nil
}

func currentSuccessfulProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if !probe.DiscoveryObservedAt.Equal(snapshot.ObservedAt) || probe.Health.State != model.HealthHealthy {
			return false
		}
	}
	return found
}

func currentFailedProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if !probe.DiscoveryObservedAt.IsZero() || !probe.Health.ObservedAt.Equal(snapshot.ObservedAt) {
			return false
		}
		if probe.Health.State != model.HealthUnknown && probe.Health.State != model.HealthUnhealthy {
			return false
		}
	}
	return found
}

func primaryProvenWritable(primary model.DatabaseInstance) bool {
	switch primary.Engine {
	case model.EngineMySQL:
		return strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["read_only"]), "false") &&
			strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["super_read_only"]), "false")
	case model.EnginePostgreSQL:
		return strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["in_recovery"]), "false") &&
			strings.EqualFold(strings.TrimSpace(primary.EngineMetadata["transaction_read_only"]), "false")
	default:
		return false
	}
}

func agentOwnershipProtocolSupported(engine model.Engine) bool {
	return engine == model.EngineMySQL || engine == model.EnginePostgreSQL
}

func controlledPostgreSQLEndpointMutationActive(inventory OwnershipInventory, cluster model.DatabaseCluster) bool {
	if cluster.Engine != model.EnginePostgreSQL {
		return false
	}
	operations, ok := inventory.(OwnershipOperationInventory)
	if !ok {
		return false
	}
	for _, record := range operations.Operations(cluster.ResourceID) {
		if record.Status != model.OperationRunning {
			continue
		}
		switch record.Operation.Kind {
		case model.OperationSwitchover, model.OperationFailover, model.OperationFormerPrimaryRejoin:
			return true
		}
	}
	return false
}

func safePartialOwnershipCoverage(snapshot model.TopologySnapshot, observation endpoint.OwnershipObservation, primaryID model.ResourceID) bool {
	if observation.Complete || !model.ValidResourceID(primaryID) ||
		observation.CanonicalOwnerID != primaryID || observation.EndpointOwnerID != primaryID ||
		len(observation.OwnerIDs) != 1 || observation.OwnerIDs[0] != primaryID {
		return false
	}
	instanceIDs := make(map[model.ResourceID]bool, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		if !model.ValidResourceID(instance.ResourceID) || instanceIDs[instance.ResourceID] {
			return false
		}
		instanceIDs[instance.ResourceID] = true
	}
	if len(instanceIDs) == 0 {
		return false
	}
	observedIDs := make(map[model.ResourceID]bool, len(observation.ObservedInstanceIDs))
	for _, instanceID := range observation.ObservedInstanceIDs {
		if !model.ValidResourceID(instanceID) || !instanceIDs[instanceID] || observedIDs[instanceID] {
			return false
		}
		observedIDs[instanceID] = true
	}
	if !observedIDs[primaryID] {
		return false
	}
	return len(observedIDs) > len(instanceIDs)/2
}

func (keeper *OwnershipKeeper) reconcileCluster(ctx context.Context, cluster model.DatabaseCluster, now time.Time) (endpoint.LeaseRequest, error) {
	snapshot, found := keeper.inventory.TopologySnapshot(cluster.ResourceID)
	if !found || snapshot.ObservedAt.IsZero() || now.Sub(snapshot.ObservedAt) > keeper.maxAge || snapshot.ObservedAt.After(now.Add(keeper.interval)) {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s topology is stale or unavailable", cluster.ResourceID)
	}
	observation, err := keeper.observer.ObserveOwnership(ctx, cluster, snapshot)
	if err != nil {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s VIP observation: %w", cluster.ResourceID, err)
	}
	if !model.ValidResourceID(observation.HAEndpointID) || len(observation.OwnerIDs) > 1 {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s VIP ownership coverage is unsafe", cluster.ResourceID)
	}
	primary, primaryErr := writablePrimary(snapshot)
	bootstrap := false
	bootstrapConvergenceRenewal := false
	if primaryErr != nil {
		if !observation.Complete {
			return endpoint.LeaseRequest{}, fmt.Errorf(
				"cluster %s VIP ownership coverage is unsafe for reboot bootstrap (%d/%d instance probes succeeded)",
				cluster.ResourceID, len(observation.ObservedInstanceIDs), len(snapshot.Instances),
			)
		}
		if observation.CanonicalOwnerID != observation.EndpointOwnerID {
			return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s reboot bootstrap requires matching canonical metadata", cluster.ResourceID)
		}
		switch len(observation.OwnerIDs) {
		case 0:
			bootstrap = true
		case 1:
			if observation.OwnerIDs[0] != observation.CanonicalOwnerID {
				return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s reboot bootstrap owner does not match canonical metadata", cluster.ResourceID)
			}
			bootstrapConvergenceRenewal = true
		default:
			return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s reboot bootstrap ownership is unsafe", cluster.ResourceID)
		}
		primary, err = RebootBootstrapCandidate(snapshot, observation.CanonicalOwnerID, now, keeper.maxAge)
		if err != nil {
			return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s: %w; reboot bootstrap blocked: %v", cluster.ResourceID, primaryErr, err)
		}
	}
	partialRenewal := !observation.Complete
	renewOnly := partialRenewal || bootstrapConvergenceRenewal
	if partialRenewal && !safePartialOwnershipCoverage(snapshot, observation, primary.ResourceID) {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s VIP ownership majority coverage is unsafe", cluster.ResourceID)
	}
	if len(observation.OwnerIDs) == 1 && observation.OwnerIDs[0] != primary.ResourceID {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s VIP is owned by a non-primary instance", cluster.ResourceID)
	}
	if len(observation.OwnerIDs) == 0 && (observation.CanonicalOwnerID != primary.ResourceID || observation.EndpointOwnerID != primary.ResourceID) {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s zero-owner bootstrap metadata does not select the current primary", cluster.ResourceID)
	}
	if bootstrap && len(observation.OwnerIDs) != 0 {
		return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s reboot bootstrap requires zero VIP owners", cluster.ResourceID)
	}
	if !renewOnly {
		healthy := len(observation.OwnerIDs) == 1
		if err := keeper.inventory.CommitObservedHAEndpointOwner(
			cluster.ResourceID, observation.HAEndpointID,
			observation.HAEndpointRevision, observation.EndpointRevision,
			primary.ResourceID, healthy,
		); err != nil {
			return endpoint.LeaseRequest{}, fmt.Errorf("cluster %s commit VIP owner: %w", cluster.ResourceID, err)
		}
	}
	return endpoint.LeaseRequest{
		ClusterID: cluster.ResourceID, HAEndpointID: observation.HAEndpointID,
		OperationID: observation.HAEndpointID, OwnerID: primary.ResourceID, TTL: time.Minute,
		RenewOnly: renewOnly,
	}, nil
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
	registeredClusters := keeper.inventory.Clusters()
	clusters := make([]model.DatabaseCluster, 0, len(registeredClusters))
	for _, cluster := range registeredClusters {
		if cluster.DisasterRecoveryActive() {
			continue
		}
		if agentOwnershipProtocolSupported(cluster.Engine) && !controlledPostgreSQLEndpointMutationActive(keeper.inventory, cluster) {
			clusters = append(clusters, cluster)
		}
	}
	failures := make([]error, len(clusters))
	requests := make([]endpoint.LeaseRequest, len(clusters))
	var wait sync.WaitGroup
	for index, cluster := range clusters {
		wait.Add(1)
		go func(index int, cluster model.DatabaseCluster) {
			defer wait.Done()
			probeContext, cancel := context.WithTimeout(ctx, keeper.probeTimeout)
			defer cancel()
			requests[index], failures[index] = keeper.reconcileCluster(probeContext, cluster, now)
		}(index, cluster)
	}
	wait.Wait()
	validRequests := make([]endpoint.LeaseRequest, 0, len(requests))
	for index, request := range requests {
		if failures[index] == nil {
			validRequests = append(validRequests, request)
		}
	}
	if len(validRequests) > 0 {
		if err := keeper.leases.AcquireStableBatch(ctx, validRequests); err != nil {
			failures = append(failures, fmt.Errorf("renew VIP ownership lease batch: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (keeper *OwnershipKeeper) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	keeper.reportError(keeper.RunOnce(ctx))
	ticker := time.NewTicker(keeper.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			keeper.reportError(keeper.RunOnce(ctx))
		}
	}
}
