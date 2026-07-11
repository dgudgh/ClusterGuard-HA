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
)

type CredentialResolver interface {
	Resolve(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)
}

type CredentialResolverFunc func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)

func (resolve CredentialResolverFunc) Resolve(ctx context.Context, cluster model.DatabaseCluster, endpoint model.Endpoint) (adapter.Credentials, error) {
	return resolve(ctx, cluster, endpoint)
}

type Service struct {
	registry       *adapter.Registry
	repository     *store.Repository
	credentials    CredentialResolver
	now            func() time.Time
	clusterLocksMu sync.Mutex
	clusterLocks   map[model.ResourceID]*clusterLock
}

type clusterLock struct {
	mutex      sync.Mutex
	references int
}

func New(registry *adapter.Registry, repository *store.Repository, credentials CredentialResolver, clock func() time.Time) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		registry:     registry,
		repository:   repository,
		credentials:  credentials,
		now:          clock,
		clusterLocks: make(map[model.ResourceID]*clusterLock),
	}
}

type endpointProbe struct {
	endpoint  model.Endpoint
	discovery adapter.DiscoveryResult
	metrics   []model.MetricSample
	failure   probeFailure
}

type probeFailure uint8

const (
	probeSucceeded probeFailure = iota
	probeCredentialsFailed
	probeDatabaseFailed
	probeMetricsFailed
	probeUnsupported
)

func (service *Service) Refresh(ctx context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	if service == nil || service.registry == nil || service.repository == nil {
		return model.TopologySnapshot{}, fmt.Errorf("discovery service is not configured")
	}
	unlock := service.lockCluster(clusterID)
	defer unlock()
	inventory, exists := service.repository.DiscoveryInventory(clusterID)
	if !exists {
		return model.TopologySnapshot{}, fmt.Errorf("unknown cluster ID: %s", clusterID)
	}
	cluster := inventory.Cluster
	endpoints := activeDatabaseEndpoints(inventory.Endpoints)
	if len(endpoints) == 0 {
		return model.TopologySnapshot{}, ErrInventoryRequired
	}
	candidate, exists := service.registry.Get(cluster.Engine)
	if !exists {
		return model.TopologySnapshot{}, adapter.ErrUnsupported
	}
	if !candidate.Capabilities(ctx).Supports(adapter.CapabilityDiscover) {
		return model.TopologySnapshot{}, adapter.ErrUnsupported
	}

	observedAt := service.now().UTC()
	probeResults := service.probeEndpoints(ctx, candidate, cluster, endpoints)
	observations := make([]store.DiscoveryObservation, 0, len(probeResults))
	writablePrimaryIdentities := make(map[string]struct{})
	credentialFailures := 0
	databaseFailures := 0
	metricFailures := 0
	probes := make([]model.ProbeStatus, 0, len(probeResults))
	for _, probe := range probeResults {
		if probe.failure == probeUnsupported {
			return model.TopologySnapshot{}, adapter.ErrUnsupported
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
	health := discoveryHealth(observedAt, len(endpoints), credentialFailures, databaseFailures, metricFailures, writablePrimaries)
	snapshot, err := service.repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           clusterID,
		InventoryGeneration: inventory.Generation,
		Observations:        observations,
		Probes:              probes,
		Health:              health,
		ObservedAt:          observedAt,
		Anomalies:           anomalies,
	})
	if err != nil {
		return model.TopologySnapshot{}, fmt.Errorf("apply discovery refresh: %w", err)
	}

	sortSnapshotResources(snapshot.Instances, snapshot.Links, snapshot.Probes, snapshot.Anomalies)
	return snapshot, nil
}

func (service *Service) lockCluster(clusterID model.ResourceID) func() {
	service.clusterLocksMu.Lock()
	lock := service.clusterLocks[clusterID]
	if lock == nil {
		lock = &clusterLock{}
		service.clusterLocks[clusterID] = lock
	}
	lock.references++
	service.clusterLocksMu.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		service.clusterLocksMu.Lock()
		lock.references--
		if lock.references == 0 && service.clusterLocks[clusterID] == lock {
			delete(service.clusterLocks, clusterID)
		}
		service.clusterLocksMu.Unlock()
	}
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

func (service *Service) probeEndpoints(ctx context.Context, candidate adapter.DatabaseHAAdapter, cluster model.DatabaseCluster, endpoints []model.Endpoint) []endpointProbe {
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
			metrics, err := candidate.Metrics(ctx, request)
			if err != nil {
				results[index].failure = probeMetricsFailed
				return
			}
			results[index].metrics = metrics
		}(index, endpoint)
	}
	wait.Wait()
	return results
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
