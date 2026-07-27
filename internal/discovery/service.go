package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

var ErrInventoryRequired = errors.New("active database endpoint inventory is required")

const (
	maximumParallelProbes    = 4
	credentialFailureSummary = "discovery credentials unavailable"
	databaseFailureSummary   = "database probe failed"
	metricsFailureSummary    = "performance metrics unavailable"
	topologyFailureSummary   = "database topology probe failed"
)

type CredentialResolver interface {
	Resolve(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)
}

type CredentialResolverFunc func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)

func (resolve CredentialResolverFunc) Resolve(ctx context.Context, cluster model.DatabaseCluster, endpoint model.Endpoint) (adapter.Credentials, error) {
	return resolve(ctx, cluster, endpoint)
}

type PublicationFence interface {
	AcquireCluster(context.Context, model.ResourceID) (context.Context, func(), error)
}

type PrimaryFailureObserver interface {
	Record(model.ResourceID, bool, time.Time)
}

type Option func(*Service)

func WithPublicationFence(fence PublicationFence) Option {
	return func(service *Service) {
		service.publicationFence = fence
	}
}

func WithPrimaryFailureObserver(observer PrimaryFailureObserver) Option {
	return func(service *Service) {
		service.primaryFailureObserver = observer
	}
}

type Service struct {
	registry               *adapter.Registry
	repository             *store.Repository
	credentials            CredentialResolver
	publicationFence       PublicationFence
	primaryFailureObserver PrimaryFailureObserver
	now                    func() time.Time
	clusterLocksMu         sync.Mutex
	clusterLocks           map[model.ResourceID]*clusterLock
}

type clusterLock struct {
	gate       chan struct{}
	references int
}

func New(registry *adapter.Registry, repository *store.Repository, credentials CredentialResolver, clock func() time.Time, options ...Option) *Service {
	if clock == nil {
		clock = time.Now
	}
	service := &Service{
		registry:     registry,
		repository:   repository,
		credentials:  credentials,
		now:          clock,
		clusterLocks: make(map[model.ResourceID]*clusterLock),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

type endpointProbe struct {
	endpoint  model.Endpoint
	discovery adapter.DiscoveryResult
	topology  adapter.TopologyResult
	metrics   []model.MetricSample
	failure   probeFailure
}

type probeFailure uint8

const (
	probeSucceeded probeFailure = iota
	probeCredentialsFailed
	probeDatabaseFailed
	probeMetricsFailed
	probeTopologyFailed
	probeUnsupported
)

func (service *Service) Refresh(ctx context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	if service == nil || service.registry == nil || service.repository == nil {
		return model.TopologySnapshot{}, fmt.Errorf("discovery service is not configured")
	}
	unlock, err := service.lockCluster(ctx, clusterID)
	if err != nil {
		return model.TopologySnapshot{}, err
	}
	defer unlock()
	releasePublicationFence, err := service.acquirePublicationFence(ctx, clusterID)
	if err != nil {
		return model.TopologySnapshot{}, err
	}
	defer releasePublicationFence()
	refresh, err := service.prepareRefresh(ctx, clusterID)
	if err != nil {
		return model.TopologySnapshot{}, err
	}
	snapshot, err := service.repository.ApplyDiscoveryRefresh(refresh)
	return service.finishRefresh(clusterID, refresh.ObservedAt, snapshot, err)
}

func (service *Service) prepareRefresh(ctx context.Context, clusterID model.ResourceID) (store.DiscoveryRefresh, error) {
	inventory, exists := service.repository.DiscoveryInventory(clusterID)
	if !exists {
		return store.DiscoveryRefresh{}, fmt.Errorf("unknown cluster ID: %s", clusterID)
	}
	cluster := inventory.Cluster
	endpoints := activeDatabaseEndpoints(inventory.Endpoints)
	if len(endpoints) == 0 {
		return store.DiscoveryRefresh{}, ErrInventoryRequired
	}
	candidate, exists := service.registry.Get(cluster.Engine)
	if !exists {
		return store.DiscoveryRefresh{}, adapter.ErrUnsupported
	}
	capabilities := candidate.Capabilities(ctx)
	if !capabilities.Supports(adapter.CapabilityDiscover) {
		return store.DiscoveryRefresh{}, adapter.ErrUnsupported
	}
	topologyAvailable := capabilities.Supports(adapter.CapabilityTopology)
	metricsAvailable := capabilities.Supports(adapter.CapabilityMetrics)

	observedAt := service.now().UTC()
	probeResults := service.probeEndpoints(ctx, candidate, cluster, endpoints, topologyAvailable, metricsAvailable)
	if err := ctx.Err(); err != nil {
		return store.DiscoveryRefresh{}, err
	}
	observations := make([]store.DiscoveryObservation, 0, len(probeResults))
	writablePrimaryIdentities := make(map[string]struct{})
	credentialFailures := 0
	databaseFailures := 0
	metricFailures := 0
	nativeLinks := make([]model.NativeReplicationLink, 0)
	probes := make([]model.ProbeStatus, 0, len(probeResults))
	for _, probe := range probeResults {
		if probe.failure == probeUnsupported {
			return store.DiscoveryRefresh{}, adapter.ErrUnsupported
		}
		status := model.ProbeStatus{EndpointID: probe.endpoint.ResourceID, InstanceID: probe.endpoint.InstanceID}
		if probe.failure == probeCredentialsFailed {
			credentialFailures++
			status.Health = model.Health{State: model.HealthUnknown, Summary: credentialFailureSummary, ObservedAt: observedAt}
			probes = append(probes, status)
			continue
		}
		if probe.failure == probeDatabaseFailed {
			databaseFailures++
			status.Health = model.Health{State: model.HealthUnknown, Summary: databaseFailureSummary, ObservedAt: observedAt}
			probes = append(probes, status)
			continue
		}
		if probe.failure == probeTopologyFailed {
			return store.DiscoveryRefresh{}, fmt.Errorf(topologyFailureSummary)
		}
		discovered := probe.discovery.Instance
		status.DiscoveryObservedAt = observedAt
		discovered.ClusterID = clusterID
		discovered.Engine = cluster.Engine
		engineMetadata := make(map[string]string, len(discovered.EngineMetadata)+1)
		for key, value := range discovered.EngineMetadata {
			engineMetadata[key] = value
		}
		discovered.EngineMetadata = engineMetadata
		if discovered.Hostname != "" {
			discovered.EngineMetadata["reported_hostname"] = discovered.Hostname
		}
		discovered.Hostname = probe.endpoint.Hostname
		discovered.IPAddress = probe.endpoint.IPAddress
		discovered.Port = probe.endpoint.Port
		metrics := probe.metrics
		if probe.failure == probeMetricsFailed {
			metricFailures++
			metrics = nil
			status.Health = model.Health{State: model.HealthDegraded, Summary: metricsFailureSummary, ObservedAt: observedAt}
		} else {
			status.Health = discovered.Health
			for _, sample := range metrics {
				if sample.ObservedAt.After(status.MetricsObservedAt) {
					status.MetricsObservedAt = sample.ObservedAt
				}
			}
			if status.Health.ObservedAt.IsZero() {
				status.Health.ObservedAt = observedAt
			}
		}
		probes = append(probes, status)
		observations = append(observations, store.DiscoveryObservation{
			EndpointID: probe.endpoint.ResourceID,
			Instance:   discovered,
			Metrics:    metrics,
		})
		for _, link := range probe.topology.Links {
			nativeLinks = append(nativeLinks, model.NativeReplicationLink{
				SourceIdentity: link.SourceIdentity.Clone(),
				TargetIdentity: link.TargetIdentity.Clone(),
				Healthy:        link.Healthy,
				LagSeconds:     cloneLagSeconds(link.LagSeconds),
			})
		}
		if discovered.Role == model.RolePrimary {
			if key, err := identity.InstanceKey(discovered.Engine, discovered.EngineIdentity); err == nil {
				writablePrimaryIdentities[key] = struct{}{}
			}
		}
	}

	anomalies := make([]model.MetadataAnomaly, 0, 1)
	writablePrimaries := len(writablePrimaryIdentities)
	if writablePrimaries > 1 {
		anomalies = append(anomalies, model.MetadataAnomaly{
			ClusterID: clusterID,
			Engine:    cluster.Engine,
			Kind:      "multiple_writable_primaries",
			Severity:  "critical",
			Message:   fmt.Sprintf("discovered %d writable primaries; no primary was selected", writablePrimaries),
		})
	}
	nativeLinks = append(nativeLinks, roleBasedNativeLinks(cluster.Engine, observations, nativeLinks)...)
	health := discoveryHealth(observedAt, len(endpoints), credentialFailures, databaseFailures, metricFailures, writablePrimaries)
	clusterIdentity, err := discoveredClusterIdentity(cluster, observations, len(endpoints))
	if err != nil {
		return store.DiscoveryRefresh{}, err
	}
	if err := ctx.Err(); err != nil {
		return store.DiscoveryRefresh{}, err
	}
	return store.DiscoveryRefresh{
		ClusterID:             clusterID,
		ClusterIdentity:       clusterIdentity,
		InventoryGeneration:   inventory.Generation,
		Observations:          observations,
		NativeLinks:           nativeLinks,
		TopologyAuthoritative: topologyAvailable,
		Probes:                probes,
		Health:                health,
		ObservedAt:            observedAt,
		Anomalies:             anomalies,
	}, nil
}

func discoveredClusterIdentity(cluster model.DatabaseCluster, observations []store.DiscoveryObservation, inventorySize int) (model.EngineIdentity, error) {
	if !discoveryClusterIdentityRequired(cluster.Engine) {
		return nil, nil
	}
	var observedIdentity model.EngineIdentity
	var observedKey string
	for _, observation := range observations {
		candidate := discoveryClusterIdentity(cluster.Engine, observation.Instance.EngineIdentity)
		key, err := identity.ClusterKey(cluster.Engine, candidate)
		if err != nil {
			return nil, fmt.Errorf("%s discovery returned an invalid cluster identity", cluster.Engine)
		}
		if observedKey != "" && observedKey != key {
			return nil, fmt.Errorf("%s discovery returned mixed cluster identities", cluster.Engine)
		}
		observedIdentity = candidate
		observedKey = key
	}
	if len(cluster.EngineIdentity) > 0 {
		persistedKey, err := identity.ClusterKey(cluster.Engine, cluster.EngineIdentity)
		if err != nil {
			return nil, fmt.Errorf("registered %s cluster identity is invalid", cluster.Engine)
		}
		if observedKey != "" && persistedKey != observedKey {
			return nil, fmt.Errorf("%s cluster identity does not match the registered cluster", cluster.Engine)
		}
		if observedKey == "" {
			return nil, nil
		}
		return observedIdentity.Clone(), nil
	}
	if observedKey == "" || len(observations) != inventorySize {
		if observedKey != "" {
			return nil, fmt.Errorf("%s discovery returned incomplete cluster identity evidence: observed %d of %d endpoints", cluster.Engine, len(observations), inventorySize)
		}
		return nil, nil
	}
	return observedIdentity.Clone(), nil
}

func discoveryClusterIdentityRequired(engine model.Engine) bool {
	switch engine {
	case model.EnginePostgreSQL, model.EngineOracle, model.EngineSQLServer:
		return true
	default:
		return false
	}
}

func discoveryClusterIdentity(engine model.Engine, source model.EngineIdentity) model.EngineIdentity {
	switch engine {
	case model.EnginePostgreSQL:
		return model.EngineIdentity{"system_identifier": source["system_identifier"]}
	case model.EngineOracle:
		return model.EngineIdentity{"dbid": source["dbid"]}
	case model.EngineSQLServer:
		return model.EngineIdentity{"group_id": source["group_id"]}
	default:
		return nil
	}
}

func (service *Service) acquirePublicationFence(ctx context.Context, clusterID model.ResourceID) (func(), error) {
	if service.publicationFence != nil {
		_, releasePublicationFence, err := service.publicationFence.AcquireCluster(ctx, clusterID)
		if err != nil {
			return nil, fmt.Errorf("acquire discovery publication fence: %w", err)
		}
		return releasePublicationFence, nil
	}
	return func() {}, nil
}

func (service *Service) finishRefresh(clusterID model.ResourceID, observedAt time.Time, snapshot model.TopologySnapshot, err error) (model.TopologySnapshot, error) {
	if err != nil {
		if errors.Is(err, store.ErrPostCommitDurability) {
			sortSnapshotResources(snapshot.Instances, snapshot.Links, snapshot.Probes, snapshot.Anomalies)
			return snapshot, fmt.Errorf("apply discovery refresh: %w", err)
		}
		return model.TopologySnapshot{}, fmt.Errorf("apply discovery refresh: %w", err)
	}

	sortSnapshotResources(snapshot.Instances, snapshot.Links, snapshot.Probes, snapshot.Anomalies)
	if service.primaryFailureObserver != nil {
		service.primaryFailureObserver.Record(clusterID, primaryProbeUnavailable(snapshot), observedAt)
	}
	return snapshot, nil
}

type preparedRefresh struct {
	refresh store.DiscoveryRefresh
	release func()
	err     error
}

// RefreshBatch probes clusters concurrently and publishes the complete
// scheduler round with one repository mutation. Per-cluster locks and
// publication fences remain held until the shared snapshot commits.
func (service *Service) RefreshBatch(ctx context.Context, clusterIDs []model.ResourceID) (map[model.ResourceID]model.TopologySnapshot, error) {
	if service == nil || service.registry == nil || service.repository == nil {
		return nil, fmt.Errorf("discovery service is not configured")
	}
	ordered := append([]model.ResourceID{}, clusterIDs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for index, clusterID := range ordered {
		if !model.ValidResourceID(clusterID) {
			return nil, fmt.Errorf("discovery batch contains an invalid cluster ID")
		}
		if index > 0 && clusterID == ordered[index-1] {
			return nil, fmt.Errorf("discovery batch contains duplicate cluster ID: %s", clusterID)
		}
	}
	if len(ordered) == 0 {
		return map[model.ResourceID]model.TopologySnapshot{}, nil
	}

	results := make([]preparedRefresh, len(ordered))
	parallel := maximumScheduledRefreshes
	if len(ordered) < parallel {
		parallel = len(ordered)
	}
	gate := make(chan struct{}, parallel)
	var wait sync.WaitGroup
	for index, clusterID := range ordered {
		wait.Add(1)
		go func(index int, clusterID model.ResourceID) {
			defer wait.Done()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			unlock, err := service.lockCluster(ctx, clusterID)
			if err != nil {
				results[index].err = err
				return
			}
			releaseFence, err := service.acquirePublicationFence(ctx, clusterID)
			if err != nil {
				unlock()
				results[index].err = err
				return
			}
			refresh, err := service.prepareRefresh(ctx, clusterID)
			if err != nil {
				releaseFence()
				unlock()
				results[index].err = err
				return
			}
			results[index] = preparedRefresh{
				refresh: refresh,
				release: func() {
					releaseFence()
					unlock()
				},
			}
		}(index, clusterID)
	}
	wait.Wait()

	refreshes := make([]store.DiscoveryRefresh, 0, len(results))
	failures := make([]error, 0)
	for _, result := range results {
		if result.release != nil {
			defer result.release()
		}
		if result.err != nil {
			failures = append(failures, result.err)
			continue
		}
		refreshes = append(refreshes, result.refresh)
	}
	if len(refreshes) == 0 {
		return nil, errors.Join(failures...)
	}
	published, publishErr := service.repository.ApplyDiscoveryRefreshBatch(refreshes)
	if publishErr != nil && !errors.Is(publishErr, store.ErrPostCommitDurability) {
		return nil, errors.Join(append(failures, fmt.Errorf("apply discovery refresh batch: %w", publishErr))...)
	}
	for _, refresh := range refreshes {
		snapshot, found := published[refresh.ClusterID]
		if !found {
			continue
		}
		sortSnapshotResources(snapshot.Instances, snapshot.Links, snapshot.Probes, snapshot.Anomalies)
		published[refresh.ClusterID] = snapshot
		if service.primaryFailureObserver != nil {
			service.primaryFailureObserver.Record(refresh.ClusterID, primaryProbeUnavailable(snapshot), refresh.ObservedAt)
		}
	}
	if publishErr != nil {
		failures = append(failures, fmt.Errorf("apply discovery refresh batch: %w", publishErr))
	}
	return published, errors.Join(failures...)
}

func primaryProbeUnavailable(snapshot model.TopologySnapshot) bool {
	primaryID := model.ResourceID("")
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if primaryID != "" {
			return false
		}
		primaryID = instance.ResourceID
	}
	if primaryID == "" {
		return false
	}
	boundProbeFound := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != primaryID {
			continue
		}
		boundProbeFound = true
		if !probe.DiscoveryObservedAt.IsZero() && probe.DiscoveryObservedAt.Equal(snapshot.ObservedAt) {
			return false
		}
	}
	return boundProbeFound
}

func (service *Service) lockCluster(ctx context.Context, clusterID model.ResourceID) (func(), error) {
	service.clusterLocksMu.Lock()
	lock := service.clusterLocks[clusterID]
	if lock == nil {
		lock = &clusterLock{gate: make(chan struct{}, 1)}
		service.clusterLocks[clusterID] = lock
	}
	lock.references++
	service.clusterLocksMu.Unlock()

	releaseReference := func() {
		service.clusterLocksMu.Lock()
		lock.references--
		if lock.references == 0 && service.clusterLocks[clusterID] == lock {
			delete(service.clusterLocks, clusterID)
		}
		service.clusterLocksMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		releaseReference()
		return nil, err
	}
	select {
	case lock.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.gate
			releaseReference()
			return nil, err
		}
	case <-ctx.Done():
		releaseReference()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-lock.gate
			releaseReference()
		})
	}, nil
}

func activeDatabaseEndpoints(endpoints []model.Endpoint) []model.Endpoint {
	result := make([]model.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Active && endpoint.Kind == model.EndpointDatabase {
			result = append(result, endpoint)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ResourceID < result[j].ResourceID })
	return result
}

func (service *Service) probeEndpoints(ctx context.Context, candidate adapter.DatabaseHAAdapter, cluster model.DatabaseCluster, endpoints []model.Endpoint, topologyAvailable bool, metricsAvailable bool) []endpointProbe {
	results := make([]endpointProbe, len(endpoints))
	semaphore := make(chan struct{}, maximumParallelProbes)
	var wait sync.WaitGroup
	for index, endpoint := range endpoints {
		wait.Add(1)
		go func(index int, endpoint model.Endpoint) {
			defer wait.Done()
			results[index].endpoint = endpoint
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].failure = probeDatabaseFailed
				return
			}

			credentials := adapter.Credentials{}
			if service.credentials != nil {
				resolved, err := service.credentials.Resolve(ctx, cluster, endpoint)
				if err != nil {
					results[index].failure = probeCredentialsFailed
					return
				}
				credentials = resolved
			}
			request := adapter.DiscoverRequest{
				ClusterID: cluster.ResourceID,
				Endpoint: adapter.Endpoint{
					Hostname:  endpoint.Hostname,
					IPAddress: endpoint.IPAddress,
					Port:      endpoint.Port,
				},
				Credentials: credentials,
			}
			discovered, err := candidate.Discover(ctx, request)
			if err != nil {
				if errors.Is(err, adapter.ErrUnsupported) {
					results[index].failure = probeUnsupported
				} else {
					results[index].failure = probeDatabaseFailed
				}
				return
			}
			results[index].discovery = discovered
			if topologyAvailable {
				topology, err := candidate.Topology(ctx, request, discovered)
				if err != nil {
					if errors.Is(err, adapter.ErrUnsupported) {
						results[index].failure = probeUnsupported
					} else {
						results[index].failure = probeTopologyFailed
					}
					return
				}
				results[index].topology = topology
			}
			if metricsAvailable {
				metrics, err := candidate.Metrics(ctx, request)
				if err != nil {
					results[index].failure = probeMetricsFailed
					return
				}
				results[index].metrics = metrics
			}
		}(index, endpoint)
	}
	wait.Wait()
	return results
}

func cloneLagSeconds(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func roleBasedNativeLinks(engine model.Engine, observations []store.DiscoveryObservation, existing []model.NativeReplicationLink) []model.NativeReplicationLink {
	if engine != model.EngineOracle && engine != model.EngineSQLServer {
		return nil
	}
	if len(existing) > 0 {
		return nil
	}
	var primary *model.DatabaseInstance
	for index := range observations {
		if observations[index].Instance.Role != model.RolePrimary {
			continue
		}
		if primary != nil {
			return nil
		}
		primary = &observations[index].Instance
	}
	if primary == nil {
		return nil
	}
	primaryClusterKey, err := identity.ClusterKey(engine, discoveryClusterIdentity(engine, primary.EngineIdentity))
	if err != nil {
		return nil
	}
	links := make([]model.NativeReplicationLink, 0)
	for _, observation := range observations {
		instance := observation.Instance
		if instance.Role != model.RoleReplica && instance.Role != model.RoleStandby {
			continue
		}
		clusterKey, err := identity.ClusterKey(engine, discoveryClusterIdentity(engine, instance.EngineIdentity))
		if err != nil || clusterKey != primaryClusterKey {
			continue
		}
		links = append(links, model.NativeReplicationLink{
			SourceIdentity: primary.EngineIdentity.Clone(),
			TargetIdentity: instance.EngineIdentity.Clone(),
			Healthy:        primary.Health.State == model.HealthHealthy && instance.Health.State == model.HealthHealthy,
			LagSeconds:     cloneLagSeconds(instance.Replication.LagSeconds),
		})
	}
	return links
}

func sortSnapshotResources(instances []model.DatabaseInstance, links []model.ReplicationLink, probes []model.ProbeStatus, anomalies []model.MetadataAnomaly) {
	sort.Slice(instances, func(i, j int) bool { return instances[i].ResourceID < instances[j].ResourceID })
	sort.Slice(links, func(i, j int) bool { return links[i].ResourceID < links[j].ResourceID })
	sort.Slice(probes, func(i, j int) bool { return probes[i].EndpointID < probes[j].EndpointID })
	sort.Slice(anomalies, func(i, j int) bool { return anomalies[i].ResourceID < anomalies[j].ResourceID })
}

func discoveryHealth(observedAt time.Time, endpointCount int, credentialFailures int, databaseFailures int, metricFailures int, writablePrimaries int) model.Health {
	health := model.Health{
		State:      model.HealthHealthy,
		Summary:    fmt.Sprintf("discovered all %d registered database endpoints", endpointCount),
		ObservedAt: observedAt,
	}
	if credentialFailures > 0 {
		health.State = model.HealthDegraded
		health.Summary = credentialFailureSummary
	}
	if databaseFailures > 0 {
		health.State = model.HealthDegraded
		health.Summary = databaseFailureSummary
	}
	if metricFailures > 0 {
		health.State = model.HealthDegraded
		health.Summary = metricsFailureSummary
	}
	if writablePrimaries > 1 {
		health.State = model.HealthDegraded
		health.Summary = fmt.Sprintf("discovered %d writable primaries", writablePrimaries)
	}
	return health
}
