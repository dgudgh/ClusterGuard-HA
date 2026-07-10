package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

var discoveryTestTime = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

type fakeDiscoveryAdapter struct {
	adapter.UnsupportedAdapter

	mu                sync.Mutex
	results           map[string]model.DatabaseInstance
	failures          map[string]error
	metricFailures    map[string]error
	calls             []string
	current           int
	maximum           int
	started           chan struct{}
	startedHosts      chan string
	release           <-chan struct{}
	releases          map[string]<-chan struct{}
	discoverAvailable bool
}

func newFakeAdapter() *fakeDiscoveryAdapter {
	return &fakeDiscoveryAdapter{
		UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL),
		results:            map[string]model.DatabaseInstance{},
		failures:           map[string]error{},
		metricFailures:     map[string]error{},
		releases:           map[string]<-chan struct{}{},
		discoverAvailable:  true,
	}
}

func (candidate *fakeDiscoveryAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover: {Available: candidate.discoverAvailable},
		adapter.CapabilityMetrics:  {Available: true},
	}}
}

func (candidate *fakeDiscoveryAdapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	host := request.Endpoint.Hostname
	candidate.mu.Lock()
	candidate.calls = append(candidate.calls, host)
	candidate.current++
	if candidate.current > candidate.maximum {
		candidate.maximum = candidate.current
	}
	result := candidate.results[host]
	err := candidate.failures[host]
	started := candidate.started
	startedHosts := candidate.startedHosts
	release := candidate.release
	if hostRelease, exists := candidate.releases[host]; exists {
		release = hostRelease
	}
	candidate.mu.Unlock()

	defer func() {
		candidate.mu.Lock()
		candidate.current--
		candidate.mu.Unlock()
	}()
	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if startedHosts != nil {
		select {
		case startedHosts <- host:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return adapter.DiscoveryResult{}, ctx.Err()
		}
	}
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	return adapter.DiscoveryResult{Instance: result}, nil
}

func (candidate *fakeDiscoveryAdapter) Metrics(_ context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	candidate.mu.Lock()
	err := candidate.metricFailures[request.Endpoint.Hostname]
	candidate.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []model.MetricSample{{
		ObservedAt: discoveryTestTime,
		Values:     map[string]float64{"host_port": float64(request.Endpoint.Port)},
	}}, nil
}

func (candidate *fakeDiscoveryAdapter) discoveredHosts() []string {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	result := append([]string{}, candidate.calls...)
	sort.Strings(result)
	return result
}

func (candidate *fakeDiscoveryAdapter) setFailure(host string, err error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if err == nil {
		delete(candidate.failures, host)
		return
	}
	candidate.failures[host] = err
}

func newTestService(t *testing.T, repository *store.Repository, candidate *fakeDiscoveryAdapter) *Service {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register fake adapter: %v", err)
	}
	return newTestServiceWithResolver(t, repository, registry, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "secret"}, nil
	}))
}

func newTestServiceWithResolver(t *testing.T, repository *store.Repository, registry *adapter.Registry, resolver CredentialResolver) *Service {
	t.Helper()
	return New(registry, repository, resolver, func() time.Time { return discoveryTestTime })
}

func addEndpoint(t *testing.T, repository *store.Repository, clusterID model.ResourceID, host string, port int, kind model.EndpointKind, active bool) model.Endpoint {
	t.Helper()
	endpoint, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: clusterID,
		Kind:      kind,
		Hostname:  host,
		Port:      port,
		Active:    active,
	})
	if err != nil {
		t.Fatalf("add endpoint %s: %v", host, err)
	}
	return endpoint
}

func discoveredInstance(host string, port int, nativeID string, role model.InstanceRole, sourceID string) model.DatabaseInstance {
	instance := model.DatabaseInstance{
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": nativeID},
		DisplayName:    host,
		Hostname:       host,
		Port:           port,
		Role:           role,
		Health:         model.Health{State: model.HealthHealthy, ObservedAt: discoveryTestTime},
		EngineMetadata: map[string]string{"version": "8.0.36"},
	}
	if sourceID != "" {
		lag := int64(1)
		instance.Replication = model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"server_uuid": sourceID},
			IOThread:       model.ThreadRunning,
			SQLThread:      model.ThreadRunning,
			LagSeconds:     &lag,
		}
	}
	return instance
}

func TestRefreshRejectsInventoryWithoutActiveDatabaseEndpoints(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "empty"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	addEndpoint(t, repository, cluster.ResourceID, "inactive", 3306, model.EndpointDatabase, false)
	addEndpoint(t, repository, cluster.ResourceID, "vip", 3307, model.EndpointVIP, true)
	service := newTestService(t, repository, newFakeAdapter())

	_, err = service.Refresh(context.Background(), cluster.ResourceID)
	if !errors.Is(err, ErrInventoryRequired) {
		t.Fatalf("empty database inventory error = %v, want %v", err, ErrInventoryRequired)
	}
}

func TestRefreshBuildsLinksAndMetricsOnlyFromRegisteredInventory(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	registered := []model.Endpoint{
		addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true),
		addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true),
		addEndpoint(t, repository, cluster.ResourceID, "mysql-c", 3308, model.EndpointDatabase, true),
	}
	addEndpoint(t, repository, cluster.ResourceID, "mysql-inactive", 3309, model.EndpointDatabase, false)
	addEndpoint(t, repository, cluster.ResourceID, "mysql-vip", 3310, model.EndpointVIP, true)

	candidate := newFakeAdapter()
	candidate.results["mysql-a"] = discoveredInstance("mysql-a", 3306, "native-a", model.RolePrimary, "")
	candidate.results["mysql-b"] = discoveredInstance("mysql-b", 3307, "native-b", model.RoleReplica, "native-a")
	candidate.results["mysql-c"] = discoveredInstance("mysql-c", 3308, "native-c", model.RoleReplica, "native-a")
	candidate.results["not-registered"] = discoveredInstance("not-registered", 3399, "native-extra", model.RoleReplica, "native-a")
	service := newTestService(t, repository, candidate)

	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if len(first.Instances) != 3 || len(first.Links) != 2 || len(first.Probes) != 3 {
		t.Fatalf("unexpected first topology: %+v", first)
	}
	if got, want := candidate.discoveredHosts(), []string{"mysql-a", "mysql-b", "mysql-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probed hosts = %v, want registered inventory %v", got, want)
	}
	assertSnapshotResourceOrder(t, first)

	firstIDs := instanceIDsByNativeIdentity(first.Instances)
	firstLinkIDs := linkResourceIDs(first.Links)
	wantTargets := map[model.ResourceID]bool{firstIDs["native-b"]: true, firstIDs["native-c"]: true}
	for _, link := range first.Links {
		if link.SourceInstanceID != firstIDs["native-a"] || !wantTargets[link.TargetInstanceID] {
			t.Fatalf("native replication identity resolved to the wrong platform edge: %+v", link)
		}
		delete(wantTargets, link.TargetInstanceID)
	}
	if len(wantTargets) != 0 {
		t.Fatalf("replication targets were not resolved: %v", wantTargets)
	}
	second, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !reflect.DeepEqual(instanceIDsByNativeIdentity(second.Instances), firstIDs) {
		t.Fatalf("platform instance UUIDs changed: first=%v second=%v", firstIDs, instanceIDsByNativeIdentity(second.Instances))
	}
	if !reflect.DeepEqual(linkResourceIDs(second.Links), firstLinkIDs) {
		t.Fatalf("replication link UUIDs changed: first=%v second=%v", firstLinkIDs, linkResourceIDs(second.Links))
	}
	assertSnapshotResourceOrder(t, second)

	endpoints := repository.Endpoints(cluster.ResourceID)
	bound := map[model.ResourceID]bool{}
	for _, endpoint := range endpoints {
		if endpoint.Kind == model.EndpointDatabase && endpoint.Active {
			if endpoint.InstanceID == "" {
				t.Fatalf("registered endpoint was not bound: %+v", endpoint)
			}
			bound[endpoint.InstanceID] = true
		}
	}
	if len(bound) != len(registered) {
		t.Fatalf("endpoint bindings = %v, want %d distinct instances", bound, len(registered))
	}
	samples := repository.MetricSamples(cluster.ResourceID)
	if len(samples) != 6 {
		t.Fatalf("stored metric samples = %d, want one per successful endpoint per refresh", len(samples))
	}
	for _, sample := range samples {
		if sample.InstanceID == "" || !bound[sample.InstanceID] {
			t.Fatalf("metric sample was not bound to a reconciled instance: %+v", sample)
		}
	}

	serviceType := reflect.TypeOf(service)
	if serviceType.NumMethod() != 1 || serviceType.Method(0).Name != "Refresh" {
		t.Fatalf("service exposes a probe path outside inventory refresh: methods=%v", exportedMethodNames(serviceType))
	}
}

func TestRefreshRepresentsFirstAndKnownProbeFailuresWithoutInventingInstancesOrLinks(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "failures"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	never := addEndpoint(t, repository, cluster.ResourceID, "never", 3306, model.EndpointDatabase, true)
	known := addEndpoint(t, repository, cluster.ResourceID, "known", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results["known"] = discoveredInstance("known", 3307, "known-native", model.RolePrimary, "")
	candidate.setFailure("never", errors.New("offline"))
	service := newTestService(t, repository, candidate)

	first, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	firstProbes := probesByEndpoint(first.Probes)
	if len(first.Instances) != 1 || len(first.Links) != 0 || firstProbes[never.ResourceID].InstanceID != "" || firstProbes[never.ResourceID].Health.State != model.HealthUnknown {
		t.Fatalf("never-successful endpoint invented topology: %+v", first)
	}
	knownID := firstProbes[known.ResourceID].InstanceID
	if knownID == "" || firstProbes[known.ResourceID].Health.State != model.HealthHealthy {
		t.Fatalf("successful endpoint was not bound: %+v", firstProbes[known.ResourceID])
	}

	candidate.setFailure("known", errors.New("temporarily unavailable"))
	second, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("failed-probe refresh: %v", err)
	}
	secondProbes := probesByEndpoint(second.Probes)
	if len(second.Instances) != 0 || len(second.Links) != 0 {
		t.Fatalf("failed probes retained or invented current topology: %+v", second)
	}
	if secondProbes[known.ResourceID].InstanceID != knownID || secondProbes[known.ResourceID].Health.State != model.HealthUnknown {
		t.Fatalf("known failed probe lost its UUID or health state: %+v", secondProbes[known.ResourceID])
	}
	if secondProbes[never.ResourceID].InstanceID != "" || len(repository.Instances(cluster.ResourceID)) != 1 {
		t.Fatalf("failed probes changed durable instance identity: probes=%+v instances=%+v", second.Probes, repository.Instances(cluster.ResourceID))
	}

	candidate.setFailure("known", nil)
	third, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	thirdProbe := probesByEndpoint(third.Probes)[known.ResourceID]
	if thirdProbe.InstanceID != knownID || thirdProbe.Health.State != model.HealthHealthy {
		t.Fatalf("recovered endpoint did not resume its stable identity: %+v", thirdProbe)
	}
}

func TestRefreshMarksMultipleWritablePrimariesCriticalAndDegraded(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "split-brain"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	addEndpoint(t, repository, cluster.ResourceID, "mysql-b", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results["mysql-a"] = discoveredInstance("mysql-a", 3306, "native-a", model.RolePrimary, "")
	candidate.results["mysql-b"] = discoveredInstance("mysql-b", 3307, "native-b", model.RolePrimary, "")
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if snapshot.Health.State != model.HealthDegraded || len(snapshot.Anomalies) != 1 {
		t.Fatalf("multiple primaries did not degrade the cluster: %+v", snapshot)
	}
	anomaly := snapshot.Anomalies[0]
	if anomaly.ClusterID != cluster.ResourceID || anomaly.Kind != "multiple_writable_primaries" || anomaly.Severity != "critical" {
		t.Fatalf("unexpected multiple-primary anomaly: %+v", anomaly)
	}
	if len(snapshot.Instances) != 2 || snapshot.Instances[0].Role != model.RolePrimary || snapshot.Instances[1].Role != model.RolePrimary {
		t.Fatalf("discovery implicitly selected a primary: %+v", snapshot.Instances)
	}
	persisted := anomaliesForClusterID(repository.Anomalies(), cluster.ResourceID)
	if len(persisted) != 1 || persisted[0].Kind != anomaly.Kind {
		t.Fatalf("cluster anomaly was not persisted: %+v", persisted)
	}
}

func TestRefreshBoundsConcurrentEndpointProbesAtFour(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "parallel"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	candidate := newFakeAdapter()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	candidate.started = started
	candidate.release = release
	for index := 0; index < 8; index++ {
		host := "mysql-" + string(rune('a'+index))
		addEndpoint(t, repository, cluster.ResourceID, host, 3306+index, model.EndpointDatabase, true)
		candidate.results[host] = discoveredInstance(host, 3306+index, "native-"+host, model.RoleUnknown, "")
	}
	service := newTestService(t, repository, candidate)
	done := make(chan error, 1)
	go func() {
		_, err := service.Refresh(context.Background(), cluster.ResourceID)
		done <- err
	}()

	for index := 0; index < 4; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d probes started before timeout", index)
		}
	}
	select {
	case <-started:
		t.Fatal("a fifth probe started while four probes were blocked")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	candidate.mu.Lock()
	maximum := candidate.maximum
	candidate.mu.Unlock()
	if maximum != 4 {
		t.Fatalf("maximum concurrent probes = %d, want 4", maximum)
	}
}

func TestRefreshFailsClosedWhenDiscoveryIsUnsupported(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		configure          func(*fakeDiscoveryAdapter, string)
		wantDiscoveryCalls int
	}{
		{
			name: "capability unavailable",
			configure: func(candidate *fakeDiscoveryAdapter, _ string) {
				candidate.discoverAvailable = false
			},
		},
		{
			name: "adapter returns unsupported",
			configure: func(candidate *fakeDiscoveryAdapter, host string) {
				candidate.failures[host] = adapter.ErrUnsupported
			},
			wantDiscoveryCalls: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, endpoint := seedDiscoveryRepositoryState(t, repository)
			before := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
			candidate := newFakeAdapter()
			candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "replacement-native", model.RolePrimary, "")
			testCase.configure(candidate, endpoint.Hostname)
			service := newTestService(t, repository, candidate)

			_, err := service.Refresh(context.Background(), cluster.ResourceID)
			if !errors.Is(err, adapter.ErrUnsupported) {
				t.Fatalf("unsupported discovery error = %v, want %v", err, adapter.ErrUnsupported)
			}
			if calls := len(candidate.discoveredHosts()); calls != testCase.wantDiscoveryCalls {
				t.Fatalf("discovery calls = %d, want %d", calls, testCase.wantDiscoveryCalls)
			}
			after := captureDiscoveryRepositoryState(repository, cluster.ResourceID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("unsupported discovery changed repository state:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestRefreshSanitizesCredentialAndDatabaseProbeErrors(t *testing.T) {
	const secret = "super-secret-password"
	for _, testCase := range []struct {
		name        string
		wantSummary string
		configure   func(*testing.T, *store.Repository, model.DatabaseCluster, model.Endpoint, *fakeDiscoveryAdapter) *Service
	}{
		{
			name:        "credential resolver",
			wantSummary: "discovery credentials unavailable",
			configure: func(t *testing.T, repository *store.Repository, _ model.DatabaseCluster, _ model.Endpoint, candidate *fakeDiscoveryAdapter) *Service {
				registry := adapter.NewRegistry()
				if err := registry.Register(candidate); err != nil {
					t.Fatalf("register fake adapter: %v", err)
				}
				return newTestServiceWithResolver(t, repository, registry, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
					return adapter.Credentials{}, errors.New("resolver leaked " + secret)
				}))
			},
		},
		{
			name:        "database adapter",
			wantSummary: "database probe failed",
			configure: func(t *testing.T, repository *store.Repository, _ model.DatabaseCluster, endpoint model.Endpoint, candidate *fakeDiscoveryAdapter) *Service {
				candidate.failures[endpoint.Hostname] = errors.New("client leaked " + secret)
				return newTestService(t, repository, candidate)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "sanitized"})
			if err != nil {
				t.Fatalf("create cluster: %v", err)
			}
			endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
			candidate := newFakeAdapter()
			service := testCase.configure(t, repository, cluster, endpoint, candidate)

			snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if len(snapshot.Probes) != 1 || snapshot.Probes[0].Health.State != model.HealthUnknown || snapshot.Probes[0].Health.Summary != testCase.wantSummary {
				t.Fatalf("unexpected sanitized probe status: %+v", snapshot.Probes)
			}
			if strings.Contains(strings.ToLower(fmtSnapshot(snapshot)), strings.ToLower(secret)) {
				t.Fatalf("snapshot exposed secret-bearing error: %+v", snapshot)
			}
		})
	}
}

func TestRefreshMetricsFailureDegradesProbeAndClusterWithoutStoringSample(t *testing.T) {
	const secret = "metrics-client-secret"
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metrics"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[endpoint.Hostname] = discoveredInstance(endpoint.Hostname, endpoint.Port, "native-a", model.RolePrimary, "")
	candidate.metricFailures[endpoint.Hostname] = errors.New("metrics leaked " + secret)
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Instances) != 1 || len(snapshot.Probes) != 1 {
		t.Fatalf("successful discovery was lost after metrics failure: %+v", snapshot)
	}
	if snapshot.Probes[0].Health.State != model.HealthDegraded || snapshot.Probes[0].Health.Summary != "performance metrics unavailable" {
		t.Fatalf("metrics failure did not degrade probe health safely: %+v", snapshot.Probes[0])
	}
	if snapshot.Health.State != model.HealthDegraded || snapshot.Health.Summary != "performance metrics unavailable" {
		t.Fatalf("metrics failure did not degrade cluster health safely: %+v", snapshot.Health)
	}
	if len(repository.MetricSamples(cluster.ResourceID)) != 0 {
		t.Fatalf("metrics failure stored samples: %+v", repository.MetricSamples(cluster.ResourceID))
	}
	if strings.Contains(strings.ToLower(fmtSnapshot(snapshot)), strings.ToLower(secret)) {
		t.Fatalf("snapshot exposed metrics error: %+v", snapshot)
	}
}

func TestRefreshDeduplicatesTwoEndpointsForOneNativeIdentity(t *testing.T) {
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "aliases"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	second := addEndpoint(t, repository, cluster.ResourceID, "mysql-a.internal", 3307, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[first.Hostname] = discoveredInstance(first.Hostname, first.Port, "shared-native", model.RolePrimary, "")
	candidate.results[second.Hostname] = discoveredInstance(second.Hostname, second.Port, "shared-native", model.RolePrimary, "")
	service := newTestService(t, repository, candidate)

	snapshot, err := service.Refresh(context.Background(), cluster.ResourceID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(snapshot.Instances) != 1 || len(snapshot.Anomalies) != 0 || snapshot.Health.State != model.HealthHealthy {
		t.Fatalf("duplicate native observation created false topology: %+v", snapshot)
	}
	probes := probesByEndpoint(snapshot.Probes)
	if probes[first.ResourceID].InstanceID == "" || probes[first.ResourceID].InstanceID != probes[second.ResourceID].InstanceID {
		t.Fatalf("duplicate native endpoints did not bind one platform UUID: %+v", snapshot.Probes)
	}
	bound := repository.Endpoints(cluster.ResourceID)
	if len(bound) != 2 || bound[0].InstanceID == "" || bound[0].InstanceID != bound[1].InstanceID {
		t.Fatalf("repository endpoint bindings were not deduplicated: %+v", bound)
	}
}

func TestRefreshSerializesSameClusterWhileDifferentClustersProceed(t *testing.T) {
	repository := store.NewMemory()
	firstCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "first"})
	if err != nil {
		t.Fatalf("create first cluster: %v", err)
	}
	secondCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "second"})
	if err != nil {
		t.Fatalf("create second cluster: %v", err)
	}
	firstEndpoint := addEndpoint(t, repository, firstCluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	secondEndpoint := addEndpoint(t, repository, secondCluster.ResourceID, "mysql-b", 3306, model.EndpointDatabase, true)
	candidate := newFakeAdapter()
	candidate.results[firstEndpoint.Hostname] = discoveredInstance(firstEndpoint.Hostname, firstEndpoint.Port, "native-a", model.RolePrimary, "")
	candidate.results[secondEndpoint.Hostname] = discoveredInstance(secondEndpoint.Hostname, secondEndpoint.Port, "native-b", model.RolePrimary, "")
	started := make(chan string, 4)
	releaseFirst := make(chan struct{})
	candidate.startedHosts = started
	candidate.releases[firstEndpoint.Hostname] = releaseFirst
	service := newTestService(t, repository, candidate)

	refresh := func(clusterID model.ResourceID) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, err := service.Refresh(context.Background(), clusterID)
			result <- err
		}()
		return result
	}
	firstRefresh := refresh(firstCluster.ResourceID)
	if host := waitForStartedHost(t, started); host != firstEndpoint.Hostname {
		t.Fatalf("first probe host = %s, want %s", host, firstEndpoint.Hostname)
	}
	queuedSameCluster := refresh(firstCluster.ResourceID)
	differentCluster := refresh(secondCluster.ResourceID)
	if host := waitForStartedHost(t, started); host != secondEndpoint.Hostname {
		t.Fatalf("same-cluster refresh interleaved before independent cluster: started %s", host)
	}
	if err := <-differentCluster; err != nil {
		t.Fatalf("different-cluster refresh: %v", err)
	}
	select {
	case host := <-started:
		t.Fatalf("same-cluster refresh entered adapter while prior refresh was blocked: %s", host)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstRefresh; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if host := waitForStartedHost(t, started); host != firstEndpoint.Hostname {
		t.Fatalf("queued same-cluster probe host = %s, want %s", host, firstEndpoint.Hostname)
	}
	if err := <-queuedSameCluster; err != nil {
		t.Fatalf("queued same-cluster refresh: %v", err)
	}
}

type discoveryRepositoryState struct {
	instances []model.DatabaseInstance
	endpoints []model.Endpoint
	links     []model.ReplicationLink
	metrics   []model.MetricSample
	anomalies []model.MetadataAnomaly
}

func seedDiscoveryRepositoryState(t *testing.T, repository *store.Repository) (model.DatabaseCluster, model.Endpoint) {
	t.Helper()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "seeded"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	endpoint := addEndpoint(t, repository, cluster.ResourceID, "mysql-a", 3306, model.EndpointDatabase, true)
	instance := discoveredInstance(endpoint.Hostname, endpoint.Port, "seed-native", model.RolePrimary, "")
	instance.ClusterID = cluster.ResourceID
	result, err := repository.ReconcileInstance(instance)
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	endpoint.InstanceID = result.Instance.ResourceID
	if _, err := repository.UpsertEndpoint(endpoint); err != nil {
		t.Fatalf("seed endpoint binding: %v", err)
	}
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{SourceInstanceID: result.Instance.ResourceID, TargetInstanceID: model.NewResourceID()}}); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{InstanceID: result.Instance.ResourceID, ObservedAt: discoveryTestTime, Values: map[string]float64{"qps": 1}}}, 60); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(cluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "seed", Severity: "warning"}}); err != nil {
		t.Fatalf("seed anomalies: %v", err)
	}
	return cluster, endpoint
}

func captureDiscoveryRepositoryState(repository *store.Repository, clusterID model.ResourceID) discoveryRepositoryState {
	return discoveryRepositoryState{
		instances: repository.Instances(clusterID),
		endpoints: repository.Endpoints(clusterID),
		links:     repository.ReplicationLinks(clusterID),
		metrics:   repository.MetricSamples(clusterID),
		anomalies: anomaliesForClusterID(repository.Anomalies(), clusterID),
	}
}

func fmtSnapshot(snapshot model.TopologySnapshot) string {
	return fmt.Sprintf("%+v", snapshot)
}

func waitForStartedHost(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case host := <-started:
		return host
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for probe to start")
		return ""
	}
}

func probesByEndpoint(probes []model.ProbeStatus) map[model.ResourceID]model.ProbeStatus {
	result := make(map[model.ResourceID]model.ProbeStatus, len(probes))
	for _, probe := range probes {
		result[probe.EndpointID] = probe
	}
	return result
}

func instanceIDsByNativeIdentity(instances []model.DatabaseInstance) map[string]model.ResourceID {
	result := make(map[string]model.ResourceID, len(instances))
	for _, instance := range instances {
		result[instance.EngineIdentity["server_uuid"]] = instance.ResourceID
	}
	return result
}

func linkResourceIDs(links []model.ReplicationLink) map[string]model.ResourceID {
	result := make(map[string]model.ResourceID, len(links))
	for _, link := range links {
		result[string(link.SourceInstanceID)+"->"+string(link.TargetInstanceID)] = link.ResourceID
	}
	return result
}

func assertSnapshotResourceOrder(t *testing.T, snapshot model.TopologySnapshot) {
	t.Helper()
	if !sort.SliceIsSorted(snapshot.Instances, func(i, j int) bool { return snapshot.Instances[i].ResourceID < snapshot.Instances[j].ResourceID }) {
		t.Fatalf("instances are not ordered by resource UUID: %+v", snapshot.Instances)
	}
	if !sort.SliceIsSorted(snapshot.Links, func(i, j int) bool { return snapshot.Links[i].ResourceID < snapshot.Links[j].ResourceID }) {
		t.Fatalf("links are not ordered by resource UUID: %+v", snapshot.Links)
	}
	if !sort.SliceIsSorted(snapshot.Probes, func(i, j int) bool { return snapshot.Probes[i].EndpointID < snapshot.Probes[j].EndpointID }) {
		t.Fatalf("probes are not ordered by endpoint UUID: %+v", snapshot.Probes)
	}
	if !sort.SliceIsSorted(snapshot.Anomalies, func(i, j int) bool { return snapshot.Anomalies[i].ResourceID < snapshot.Anomalies[j].ResourceID }) {
		t.Fatalf("anomalies are not ordered by resource UUID: %+v", snapshot.Anomalies)
	}
}

func exportedMethodNames(candidate reflect.Type) []string {
	result := make([]string, 0, candidate.NumMethod())
	for index := 0; index < candidate.NumMethod(); index++ {
		result = append(result, candidate.Method(index).Name)
	}
	return result
}

func anomaliesForClusterID(anomalies []model.MetadataAnomaly, clusterID model.ResourceID) []model.MetadataAnomaly {
	result := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range anomalies {
		if anomaly.ClusterID == clusterID {
			result = append(result, anomaly)
		}
	}
	return result
}
