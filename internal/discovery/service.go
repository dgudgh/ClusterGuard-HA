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
	maximumParallelProbes = 4
	metricSampleLimit     = 60
)

type CredentialResolver interface {
	Resolve(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)
}

type CredentialResolverFunc func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error)

func (resolve CredentialResolverFunc) Resolve(ctx context.Context, cluster model.DatabaseCluster, endpoint model.Endpoint) (adapter.Credentials, error) {
	return resolve(ctx, cluster, endpoint)
}

type Service struct {
	registry    *adapter.Registry
	repository  *store.Repository
	credentials CredentialResolver
	now         func() time.Time
}

func New(registry *adapter.Registry, repository *store.Repository, credentials CredentialResolver, clock func() time.Time) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		registry:    registry,
		repository:  repository,
		credentials: credentials,
		now:         clock,
	}
}

type endpointProbe struct {
	endpoint  model.Endpoint
	discovery adapter.DiscoveryResult
	metrics   []model.MetricSample
	err       error
}

type reconciledProbe struct {
	instance model.DatabaseInstance
}

func (service *Service) Refresh(ctx context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	if service == nil || service.registry == nil || service.repository == nil {
		return model.TopologySnapshot{}, fmt.Errorf("discovery service is not configured")
	}
	cluster, exists := service.repository.Cluster(clusterID)
	if !exists {
		return model.TopologySnapshot{}, fmt.Errorf("unknown cluster ID: %s", clusterID)
	}
	endpoints := activeDatabaseEndpoints(service.repository.Endpoints(clusterID))
	if len(endpoints) == 0 {
		return model.TopologySnapshot{}, ErrInventoryRequired
	}
	candidate, exists := service.registry.Get(cluster.Engine)
	if !exists {
		return model.TopologySnapshot{}, fmt.Errorf("no adapter registered for %s", cluster.Engine)
	}

	observedAt := service.now().UTC()
	probeResults := service.probeEndpoints(ctx, candidate, cluster, endpoints)
	probes := make([]model.ProbeStatus, 0, len(probeResults))
	reconciled := make([]reconciledProbe, 0, len(probeResults))
	instanceIDsByIdentity := make(map[string]model.ResourceID, len(probeResults))
	metricSamples := make([]model.MetricSample, 0)
	failedProbes := 0
	writablePrimaries := 0

	for _, probe := range probeResults {
		if probe.err != nil {
			failedProbes++
			probes = append(probes, model.ProbeStatus{
				EndpointID: probe.endpoint.ResourceID,
				InstanceID: probe.endpoint.InstanceID,
				Health: model.Health{
					State:      model.HealthUnknown,
					Summary:    probe.err.Error(),
					ObservedAt: observedAt,
				},
			})
			continue
		}

		discovered := probe.discovery.Instance
		discovered.ClusterID = clusterID
		discovered.Engine = cluster.Engine
		if discovered.Hostname == "" {
			discovered.Hostname = probe.endpoint.Hostname
		}
		if discovered.IPAddress == "" {
			discovered.IPAddress = probe.endpoint.IPAddress
		}
		if discovered.Port == 0 {
			discovered.Port = probe.endpoint.Port
		}
		result, err := service.repository.ReconcileInstance(discovered)
		if err != nil {
			return model.TopologySnapshot{}, fmt.Errorf("reconcile endpoint %s: %w", probe.endpoint.ResourceID, err)
		}
		probe.endpoint.InstanceID = result.Instance.ResourceID
		if _, err := service.repository.UpsertEndpoint(probe.endpoint); err != nil {
			return model.TopologySnapshot{}, fmt.Errorf("bind endpoint %s: %w", probe.endpoint.ResourceID, err)
		}

		probes = append(probes, model.ProbeStatus{
			EndpointID: probe.endpoint.ResourceID,
			InstanceID: result.Instance.ResourceID,
			Health:     result.Instance.Health,
		})
		reconciled = append(reconciled, reconciledProbe{instance: result.Instance})
		if result.Instance.Role == model.RolePrimary {
			writablePrimaries++
		}
		if key, keyErr := identity.InstanceKey(result.Instance.Engine, result.Instance.EngineIdentity); keyErr == nil {
			instanceIDsByIdentity[key] = result.Instance.ResourceID
		}
		for _, sample := range probe.metrics {
			sample.InstanceID = result.Instance.ResourceID
			metricSamples = append(metricSamples, sample)
		}
	}

	links := resolveReplicationLinks(clusterID, reconciled, instanceIDsByIdentity)
	if err := service.repository.ReplaceReplicationLinks(clusterID, links); err != nil {
		return model.TopologySnapshot{}, fmt.Errorf("replace replication links: %w", err)
	}
	if len(metricSamples) > 0 {
		if err := service.repository.StoreMetricSamples(clusterID, metricSamples, metricSampleLimit); err != nil {
			return model.TopologySnapshot{}, fmt.Errorf("store metric samples: %w", err)
		}
	}

	anomalies := make([]model.MetadataAnomaly, 0, 1)
	if writablePrimaries > 1 {
		anomalies = append(anomalies, model.MetadataAnomaly{
			ClusterID: clusterID,
			Engine:    cluster.Engine,
			Kind:      "multiple_writable_primaries",
			Severity:  "critical",
			Message:   fmt.Sprintf("discovered %d writable primaries; no primary was selected", writablePrimaries),
		})
	}
	if err := service.repository.ReplaceClusterAnomalies(clusterID, anomalies); err != nil {
		return model.TopologySnapshot{}, fmt.Errorf("replace cluster anomalies: %w", err)
	}

	instances := make([]model.DatabaseInstance, len(reconciled))
	for index, probe := range reconciled {
		instances[index] = probe.instance
	}
	persistedAnomalies := anomaliesForCluster(service.repository.Anomalies(), clusterID)
	persistedLinks := service.repository.ReplicationLinks(clusterID)
	sortSnapshotResources(instances, persistedLinks, probes, persistedAnomalies)
	health := discoveryHealth(observedAt, len(endpoints), failedProbes, writablePrimaries)
	return model.TopologySnapshot{
		ClusterID:  clusterID,
		Instances:  instances,
		Links:      persistedLinks,
		Probes:     probes,
		Health:     health,
		Anomalies:  persistedAnomalies,
		ObservedAt: observedAt,
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
				results[index].err = ctx.Err()
				return
			}

			credentials := adapter.Credentials{}
			if service.credentials != nil {
				resolved, err := service.credentials.Resolve(ctx, cluster, endpoint)
				if err != nil {
					results[index].err = err
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
				results[index].err = err
				return
			}
			results[index].discovery = discovered
			metrics, err := candidate.Metrics(ctx, request)
			if err == nil {
				results[index].metrics = metrics
			}
		}(index, endpoint)
	}
	wait.Wait()
	return results
}

func resolveReplicationLinks(clusterID model.ResourceID, probes []reconciledProbe, instanceIDsByIdentity map[string]model.ResourceID) []model.ReplicationLink {
	links := make([]model.ReplicationLink, 0)
	for _, probe := range probes {
		sourceIdentity := probe.instance.Replication.SourceIdentity
		if len(sourceIdentity) == 0 {
			continue
		}
		key, err := identity.InstanceKey(probe.instance.Engine, sourceIdentity)
		if err != nil {
			continue
		}
		sourceInstanceID, exists := instanceIDsByIdentity[key]
		if !exists || sourceInstanceID == probe.instance.ResourceID {
			continue
		}
		links = append(links, model.ReplicationLink{
			ClusterID:        clusterID,
			SourceInstanceID: sourceInstanceID,
			TargetInstanceID: probe.instance.ResourceID,
			Healthy: probe.instance.Health.State == model.HealthHealthy &&
				probe.instance.Replication.IOThread == model.ThreadRunning &&
				probe.instance.Replication.SQLThread == model.ThreadRunning,
			LagSeconds: probe.instance.Replication.LagSeconds,
		})
	}
	return links
}

func anomaliesForCluster(anomalies []model.MetadataAnomaly, clusterID model.ResourceID) []model.MetadataAnomaly {
	result := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range anomalies {
		if anomaly.ClusterID == clusterID {
			result = append(result, anomaly)
		}
	}
	return result
}

func sortSnapshotResources(instances []model.DatabaseInstance, links []model.ReplicationLink, probes []model.ProbeStatus, anomalies []model.MetadataAnomaly) {
	sort.Slice(instances, func(i, j int) bool { return instances[i].ResourceID < instances[j].ResourceID })
	sort.Slice(links, func(i, j int) bool { return links[i].ResourceID < links[j].ResourceID })
	sort.Slice(probes, func(i, j int) bool { return probes[i].EndpointID < probes[j].EndpointID })
	sort.Slice(anomalies, func(i, j int) bool { return anomalies[i].ResourceID < anomalies[j].ResourceID })
}

func discoveryHealth(observedAt time.Time, endpointCount int, failedProbes int, writablePrimaries int) model.Health {
	health := model.Health{
		State:      model.HealthHealthy,
		Summary:    fmt.Sprintf("discovered all %d registered database endpoints", endpointCount),
		ObservedAt: observedAt,
	}
	if failedProbes > 0 {
		health.State = model.HealthDegraded
		health.Summary = fmt.Sprintf("%d of %d registered database endpoint probes failed", failedProbes, endpointCount)
	}
	if writablePrimaries > 1 {
		health.State = model.HealthDegraded
		health.Summary = fmt.Sprintf("discovered %d writable primaries", writablePrimaries)
	}
	return health
}
