package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

type ReconcileResult struct {
	Instance model.DatabaseInstance `json:"instance"`
	Created  bool                   `json:"created"`
	Updated  bool                   `json:"updated"`
}

type DiscoveryObservation struct {
	EndpointID model.ResourceID
	Instance   model.DatabaseInstance
	Metrics    []model.MetricSample
}

type DiscoveryRefresh struct {
	ClusterID    model.ResourceID
	Observations []DiscoveryObservation
	Anomalies    []model.MetadataAnomaly
}

const discoveryMetricSampleLimit = 60

type snapshot struct {
	Clusters         map[model.ResourceID]model.DatabaseCluster               `json:"clusters"`
	Instances        map[model.ResourceID]model.DatabaseInstance              `json:"instances"`
	Endpoints        map[model.ResourceID]map[model.ResourceID]model.Endpoint `json:"endpoints"`
	ReplicationLinks map[model.ResourceID][]model.ReplicationLink             `json:"replication_links"`
	MetricSamples    map[model.ResourceID][]model.MetricSample                `json:"metric_samples"`
	Anomalies        map[model.ResourceID]model.MetadataAnomaly               `json:"anomalies"`
	Audits           []model.AuditEvent                                       `json:"audits"`
	Reports          []model.Report                                           `json:"reports"`
}

type Repository struct {
	mu       sync.RWMutex
	path     string
	snapshot snapshot
	now      func() time.Time
}

func emptySnapshot() snapshot {
	return snapshot{
		Clusters:         map[model.ResourceID]model.DatabaseCluster{},
		Instances:        map[model.ResourceID]model.DatabaseInstance{},
		Endpoints:        map[model.ResourceID]map[model.ResourceID]model.Endpoint{},
		ReplicationLinks: map[model.ResourceID][]model.ReplicationLink{},
		MetricSamples:    map[model.ResourceID][]model.MetricSample{},
		Anomalies:        map[model.ResourceID]model.MetadataAnomaly{},
		Audits:           []model.AuditEvent{},
		Reports:          []model.Report{},
	}
}

func NewMemory() *Repository {
	return &Repository{snapshot: emptySnapshot(), now: time.Now}
}

func Open(path string) (*Repository, error) {
	repository := NewMemory()
	repository.path = strings.TrimSpace(path)
	if repository.path == "" {
		return repository, nil
	}
	contents, err := os.ReadFile(repository.path)
	if os.IsNotExist(err) {
		return repository, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata snapshot: %w", err)
	}
	if err := json.Unmarshal(contents, &repository.snapshot); err != nil {
		return nil, fmt.Errorf("decode metadata snapshot: %w", err)
	}
	if repository.snapshot.Clusters == nil {
		repository.snapshot.Clusters = map[model.ResourceID]model.DatabaseCluster{}
	}
	if repository.snapshot.Instances == nil {
		repository.snapshot.Instances = map[model.ResourceID]model.DatabaseInstance{}
	}
	if repository.snapshot.Endpoints == nil {
		repository.snapshot.Endpoints = map[model.ResourceID]map[model.ResourceID]model.Endpoint{}
	}
	if repository.snapshot.ReplicationLinks == nil {
		repository.snapshot.ReplicationLinks = map[model.ResourceID][]model.ReplicationLink{}
	}
	if repository.snapshot.MetricSamples == nil {
		repository.snapshot.MetricSamples = map[model.ResourceID][]model.MetricSample{}
	}
	if repository.snapshot.Anomalies == nil {
		repository.snapshot.Anomalies = map[model.ResourceID]model.MetadataAnomaly{}
	}
	if repository.snapshot.Audits == nil {
		repository.snapshot.Audits = []model.AuditEvent{}
	}
	if repository.snapshot.Reports == nil {
		repository.snapshot.Reports = []model.Report{}
	}
	return repository, nil
}

func cloneInstance(instance model.DatabaseInstance) model.DatabaseInstance {
	copy := instance
	copy.EngineIdentity = instance.EngineIdentity.Clone()
	copy.Aliases = append([]string{}, instance.Aliases...)
	copy.Replication.SourceIdentity = instance.Replication.SourceIdentity.Clone()
	if instance.Replication.LagSeconds != nil {
		lagSeconds := *instance.Replication.LagSeconds
		copy.Replication.LagSeconds = &lagSeconds
	}
	copy.EngineMetadata = make(map[string]string, len(instance.EngineMetadata))
	for key, value := range instance.EngineMetadata {
		copy.EngineMetadata[key] = value
	}
	return copy
}

func cloneCluster(cluster model.DatabaseCluster) model.DatabaseCluster {
	copy := cluster
	copy.EngineIdentity = cluster.EngineIdentity.Clone()
	return copy
}

func cloneReplicationLink(link model.ReplicationLink) model.ReplicationLink {
	copy := link
	if link.LagSeconds != nil {
		lagSeconds := *link.LagSeconds
		copy.LagSeconds = &lagSeconds
	}
	return copy
}

func cloneMetricSample(sample model.MetricSample) model.MetricSample {
	copy := sample
	copy.Values = make(map[string]float64, len(sample.Values))
	for key, value := range sample.Values {
		copy.Values[key] = value
	}
	return copy
}

func cloneReplicationLinkMap(links map[model.ResourceID][]model.ReplicationLink) map[model.ResourceID][]model.ReplicationLink {
	copy := make(map[model.ResourceID][]model.ReplicationLink, len(links))
	for clusterID, clusterLinks := range links {
		copiedLinks := make([]model.ReplicationLink, len(clusterLinks))
		for index, link := range clusterLinks {
			copiedLinks[index] = cloneReplicationLink(link)
		}
		copy[clusterID] = copiedLinks
	}
	return copy
}

func cloneMetricSampleMap(samples map[model.ResourceID][]model.MetricSample) map[model.ResourceID][]model.MetricSample {
	copy := make(map[model.ResourceID][]model.MetricSample, len(samples))
	for clusterID, clusterSamples := range samples {
		copiedSamples := make([]model.MetricSample, len(clusterSamples))
		for index, sample := range clusterSamples {
			copiedSamples[index] = cloneMetricSample(sample)
		}
		copy[clusterID] = copiedSamples
	}
	return copy
}

func cloneInstanceMap(instances map[model.ResourceID]model.DatabaseInstance) map[model.ResourceID]model.DatabaseInstance {
	copy := make(map[model.ResourceID]model.DatabaseInstance, len(instances))
	for resourceID, instance := range instances {
		copy[resourceID] = cloneInstance(instance)
	}
	return copy
}

func cloneAnomalyMap(anomalies map[model.ResourceID]model.MetadataAnomaly) map[model.ResourceID]model.MetadataAnomaly {
	copy := make(map[model.ResourceID]model.MetadataAnomaly, len(anomalies))
	for resourceID, anomaly := range anomalies {
		copy[resourceID] = anomaly
	}
	return copy
}

func cloneDiscoverySnapshot(value snapshot) snapshot {
	copy := value
	copy.Instances = cloneInstanceMap(value.Instances)
	copy.Endpoints = cloneEndpointMap(value.Endpoints)
	copy.ReplicationLinks = cloneReplicationLinkMap(value.ReplicationLinks)
	copy.MetricSamples = cloneMetricSampleMap(value.MetricSamples)
	copy.Anomalies = cloneAnomalyMap(value.Anomalies)
	return copy
}

func (repository *Repository) persistLocked() error {
	return repository.persistSnapshotLocked(repository.snapshot)
}

func (repository *Repository) persistSnapshotLocked(value snapshot) error {
	if repository.path == "" {
		return nil
	}
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode metadata snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(repository.path), 0750); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(repository.path), ".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create metadata snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure metadata snapshot: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write metadata snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close metadata snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, repository.path); err != nil {
		return fmt.Errorf("publish metadata snapshot: %w", err)
	}
	return nil
}

func (repository *Repository) UpsertCluster(cluster model.DatabaseCluster) (model.DatabaseCluster, error) {
	if !cluster.Engine.Valid() {
		return model.DatabaseCluster{}, fmt.Errorf("unsupported engine: %s", cluster.Engine)
	}
	if cluster.ResourceID == "" {
		cluster.ResourceID = model.NewResourceID()
	}
	now := repository.now().UTC()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if existing, ok := repository.snapshot.Clusters[cluster.ResourceID]; ok {
		cluster.CreatedAt = existing.CreatedAt
		cluster.MetadataRevision = existing.MetadataRevision + 1
	} else {
		cluster.CreatedAt = now
		cluster.MetadataRevision = 1
	}
	cluster.UpdatedAt = now
	repository.snapshot.Clusters[cluster.ResourceID] = cloneCluster(cluster)
	if err := repository.persistLocked(); err != nil {
		return model.DatabaseCluster{}, err
	}
	return cloneCluster(cluster), nil
}

func (repository *Repository) Clusters() []model.DatabaseCluster {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	clusters := make([]model.DatabaseCluster, 0, len(repository.snapshot.Clusters))
	for _, cluster := range repository.snapshot.Clusters {
		clusters = append(clusters, cloneCluster(cluster))
	}
	sort.Slice(clusters, func(i int, j int) bool { return clusters[i].DisplayName < clusters[j].DisplayName })
	return clusters
}

func (repository *Repository) Cluster(id model.ResourceID) (model.DatabaseCluster, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	cluster, ok := repository.snapshot.Clusters[id]
	return cloneCluster(cluster), ok
}

func validateEndpoint(endpoint model.Endpoint) error {
	if endpoint.ClusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	if endpoint.Port <= 0 || endpoint.Port > 65535 {
		return fmt.Errorf("invalid endpoint port: %d", endpoint.Port)
	}
	return nil
}

func endpointAddressCollision(left model.Endpoint, right model.Endpoint) bool {
	for _, leftAddress := range model.EndpointAddress(left.Hostname, left.IPAddress, left.Port) {
		for _, rightAddress := range model.EndpointAddress(right.Hostname, right.IPAddress, right.Port) {
			if strings.EqualFold(leftAddress, rightAddress) {
				return true
			}
		}
	}
	return false
}

func endpointSetCollision(endpoints []model.Endpoint) bool {
	for index, endpoint := range endpoints {
		if !endpoint.Active {
			continue
		}
		for _, candidate := range endpoints[index+1:] {
			if candidate.Active && endpointAddressCollision(endpoint, candidate) {
				return true
			}
		}
	}
	return false
}

func cloneEndpointMap(endpoints map[model.ResourceID]map[model.ResourceID]model.Endpoint) map[model.ResourceID]map[model.ResourceID]model.Endpoint {
	copy := make(map[model.ResourceID]map[model.ResourceID]model.Endpoint, len(endpoints))
	for clusterID, clusterEndpoints := range endpoints {
		copiedEndpoints := make(map[model.ResourceID]model.Endpoint, len(clusterEndpoints))
		for endpointID, endpoint := range clusterEndpoints {
			copiedEndpoints[endpointID] = endpoint
		}
		copy[clusterID] = copiedEndpoints
	}
	return copy
}

func endpointClusterForID(endpoints map[model.ResourceID]map[model.ResourceID]model.Endpoint, endpointID model.ResourceID) (model.ResourceID, bool) {
	for clusterID, clusterEndpoints := range endpoints {
		if _, ok := clusterEndpoints[endpointID]; ok {
			return clusterID, true
		}
	}
	return "", false
}

func (repository *Repository) CreateClusterWithEndpoints(cluster model.DatabaseCluster, endpoints []model.Endpoint) (model.DatabaseCluster, []model.Endpoint, error) {
	if !cluster.Engine.Valid() {
		return model.DatabaseCluster{}, nil, fmt.Errorf("unsupported engine: %s", cluster.Engine)
	}
	if cluster.ResourceID == "" {
		cluster.ResourceID = model.NewResourceID()
	}
	endpointIDs := map[model.ResourceID]struct{}{cluster.ResourceID: {}}
	for index := range endpoints {
		if endpoints[index].ClusterID != "" && endpoints[index].ClusterID != cluster.ResourceID {
			return model.DatabaseCluster{}, nil, fmt.Errorf("endpoint cluster ID does not match cluster")
		}
		endpoints[index].ClusterID = cluster.ResourceID
		if err := validateEndpoint(endpoints[index]); err != nil {
			return model.DatabaseCluster{}, nil, err
		}
		if endpoints[index].ResourceID == "" {
			endpoints[index].ResourceID = model.NewResourceID()
		}
		if _, exists := endpointIDs[endpoints[index].ResourceID]; exists {
			return model.DatabaseCluster{}, nil, fmt.Errorf("duplicate resource ID: %s", endpoints[index].ResourceID)
		}
		endpointIDs[endpoints[index].ResourceID] = struct{}{}
	}
	if endpointSetCollision(endpoints) {
		return model.DatabaseCluster{}, nil, fmt.Errorf("duplicate active endpoint address")
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[cluster.ResourceID]; exists {
		return model.DatabaseCluster{}, nil, fmt.Errorf("cluster already exists: %s", cluster.ResourceID)
	}
	for _, endpoint := range endpoints {
		if _, exists := endpointClusterForID(repository.snapshot.Endpoints, endpoint.ResourceID); exists {
			return model.DatabaseCluster{}, nil, fmt.Errorf("endpoint resource already exists: %s", endpoint.ResourceID)
		}
	}
	now := repository.now().UTC()
	cluster.MetadataRevision = 1
	cluster.CreatedAt = now
	cluster.UpdatedAt = now
	for index := range endpoints {
		endpoints[index].MetadataRevision = 1
		endpoints[index].CreatedAt = now
		endpoints[index].UpdatedAt = now
	}

	next := repository.snapshot
	next.Clusters = make(map[model.ResourceID]model.DatabaseCluster, len(repository.snapshot.Clusters)+1)
	for resourceID, existing := range repository.snapshot.Clusters {
		next.Clusters[resourceID] = existing
	}
	next.Clusters[cluster.ResourceID] = cloneCluster(cluster)
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	next.Endpoints[cluster.ResourceID] = make(map[model.ResourceID]model.Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		next.Endpoints[cluster.ResourceID][endpoint.ResourceID] = endpoint
	}
	if err := repository.persistSnapshotLocked(next); err != nil {
		return model.DatabaseCluster{}, nil, err
	}
	repository.snapshot = next
	resultEndpoints := make([]model.Endpoint, len(endpoints))
	copy(resultEndpoints, endpoints)
	return cloneCluster(cluster), resultEndpoints, nil
}

func (repository *Repository) UpsertEndpoint(endpoint model.Endpoint) (model.Endpoint, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return model.Endpoint{}, err
	}
	if endpoint.ResourceID == "" {
		endpoint.ResourceID = model.NewResourceID()
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[endpoint.ClusterID]; !exists {
		return model.Endpoint{}, fmt.Errorf("unknown cluster ID: %s", endpoint.ClusterID)
	}
	if existingClusterID, exists := endpointClusterForID(repository.snapshot.Endpoints, endpoint.ResourceID); exists && existingClusterID != endpoint.ClusterID {
		return model.Endpoint{}, fmt.Errorf("endpoint resource already belongs to cluster %s", existingClusterID)
	}
	for resourceID, existing := range repository.snapshot.Endpoints[endpoint.ClusterID] {
		if resourceID != endpoint.ResourceID && endpoint.Active && existing.Active && endpointAddressCollision(endpoint, existing) {
			return model.Endpoint{}, fmt.Errorf("active endpoint address is already owned by resource %s", resourceID)
		}
	}
	now := repository.now().UTC()
	if existing, exists := repository.snapshot.Endpoints[endpoint.ClusterID][endpoint.ResourceID]; exists {
		endpoint.CreatedAt = existing.CreatedAt
		endpoint.MetadataRevision = existing.MetadataRevision + 1
	} else {
		endpoint.CreatedAt = now
		endpoint.MetadataRevision = 1
	}
	endpoint.UpdatedAt = now
	next := repository.snapshot
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	if next.Endpoints[endpoint.ClusterID] == nil {
		next.Endpoints[endpoint.ClusterID] = map[model.ResourceID]model.Endpoint{}
	}
	next.Endpoints[endpoint.ClusterID][endpoint.ResourceID] = endpoint
	if err := repository.persistSnapshotLocked(next); err != nil {
		return model.Endpoint{}, err
	}
	repository.snapshot = next
	return endpoint, nil
}

func (repository *Repository) Endpoints(clusterID model.ResourceID) []model.Endpoint {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	endpoints := make([]model.Endpoint, 0, len(repository.snapshot.Endpoints[clusterID]))
	for _, endpoint := range repository.snapshot.Endpoints[clusterID] {
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i int, j int) bool {
		if endpoints[i].Hostname != endpoints[j].Hostname {
			return endpoints[i].Hostname < endpoints[j].Hostname
		}
		if endpoints[i].IPAddress != endpoints[j].IPAddress {
			return endpoints[i].IPAddress < endpoints[j].IPAddress
		}
		if endpoints[i].Port != endpoints[j].Port {
			return endpoints[i].Port < endpoints[j].Port
		}
		return endpoints[i].ResourceID < endpoints[j].ResourceID
	})
	return endpoints
}

type replicationEdge struct {
	sourceInstanceID model.ResourceID
	targetInstanceID model.ResourceID
}

func (repository *Repository) ReplaceReplicationLinks(clusterID model.ResourceID, links []model.ReplicationLink) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[clusterID]; !exists {
		return fmt.Errorf("unknown cluster ID: %s", clusterID)
	}
	now := repository.now().UTC()
	existingByEdge := make(map[replicationEdge]model.ReplicationLink, len(repository.snapshot.ReplicationLinks[clusterID]))
	for _, existing := range repository.snapshot.ReplicationLinks[clusterID] {
		existingByEdge[replicationEdge{existing.SourceInstanceID, existing.TargetInstanceID}] = existing
	}
	replacement := make([]model.ReplicationLink, len(links))
	for index, link := range links {
		if link.ClusterID != "" && link.ClusterID != clusterID {
			return fmt.Errorf("replication link cluster ID does not match cluster")
		}
		link.ClusterID = clusterID
		if existing, exists := existingByEdge[replicationEdge{link.SourceInstanceID, link.TargetInstanceID}]; exists {
			link.ResourceID = existing.ResourceID
			link.CreatedAt = existing.CreatedAt
			link.MetadataRevision = existing.MetadataRevision + 1
		} else {
			if link.ResourceID == "" {
				link.ResourceID = model.NewResourceID()
			}
			link.CreatedAt = now
			link.MetadataRevision = 1
		}
		link.UpdatedAt = now
		replacement[index] = cloneReplicationLink(link)
	}
	next := repository.snapshot
	next.ReplicationLinks = cloneReplicationLinkMap(repository.snapshot.ReplicationLinks)
	next.ReplicationLinks[clusterID] = replacement
	if err := repository.persistSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) ReplicationLinks(clusterID model.ResourceID) []model.ReplicationLink {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	links := make([]model.ReplicationLink, len(repository.snapshot.ReplicationLinks[clusterID]))
	for index, link := range repository.snapshot.ReplicationLinks[clusterID] {
		links[index] = cloneReplicationLink(link)
	}
	sort.Slice(links, func(i int, j int) bool {
		if links[i].SourceInstanceID != links[j].SourceInstanceID {
			return links[i].SourceInstanceID < links[j].SourceInstanceID
		}
		if links[i].TargetInstanceID != links[j].TargetInstanceID {
			return links[i].TargetInstanceID < links[j].TargetInstanceID
		}
		return links[i].ResourceID < links[j].ResourceID
	})
	return links
}

func (repository *Repository) StoreMetricSamples(clusterID model.ResourceID, samples []model.MetricSample, limit int) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	if limit <= 0 {
		return fmt.Errorf("metric sample limit must be positive")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[clusterID]; !exists {
		return fmt.Errorf("unknown cluster ID: %s", clusterID)
	}
	byInstance := make(map[model.ResourceID][]model.MetricSample)
	for _, sample := range repository.snapshot.MetricSamples[clusterID] {
		byInstance[sample.InstanceID] = append(byInstance[sample.InstanceID], cloneMetricSample(sample))
	}
	for _, sample := range samples {
		byInstance[sample.InstanceID] = append(byInstance[sample.InstanceID], cloneMetricSample(sample))
	}
	bounded := make([]model.MetricSample, 0)
	for _, instanceSamples := range byInstance {
		sort.SliceStable(instanceSamples, func(i int, j int) bool {
			return instanceSamples[i].ObservedAt.Before(instanceSamples[j].ObservedAt)
		})
		if len(instanceSamples) > limit {
			instanceSamples = instanceSamples[len(instanceSamples)-limit:]
		}
		bounded = append(bounded, instanceSamples...)
	}
	sort.SliceStable(bounded, func(i int, j int) bool {
		if bounded[i].ObservedAt.Equal(bounded[j].ObservedAt) {
			return bounded[i].InstanceID < bounded[j].InstanceID
		}
		return bounded[i].ObservedAt.Before(bounded[j].ObservedAt)
	})
	next := repository.snapshot
	next.MetricSamples = cloneMetricSampleMap(repository.snapshot.MetricSamples)
	next.MetricSamples[clusterID] = bounded
	if err := repository.persistSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) MetricSamples(clusterID model.ResourceID) []model.MetricSample {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	samples := make([]model.MetricSample, len(repository.snapshot.MetricSamples[clusterID]))
	for index, sample := range repository.snapshot.MetricSamples[clusterID] {
		samples[index] = cloneMetricSample(sample)
	}
	sort.SliceStable(samples, func(i int, j int) bool {
		if samples[i].ObservedAt.Equal(samples[j].ObservedAt) {
			return samples[i].InstanceID < samples[j].InstanceID
		}
		return samples[i].ObservedAt.Before(samples[j].ObservedAt)
	})
	return samples
}

func (repository *Repository) FindInstanceByIdentity(clusterID model.ResourceID, engine model.Engine, engineIdentity model.EngineIdentity) (model.DatabaseInstance, bool) {
	if !engine.Valid() {
		return model.DatabaseInstance{}, false
	}
	key, err := identity.InstanceKey(engine, engineIdentity)
	if err != nil {
		return model.DatabaseInstance{}, false
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, instance := range repository.snapshot.Instances {
		if instance.ClusterID != clusterID || instance.Engine != engine {
			continue
		}
		instanceKey, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity)
		if err == nil && instanceKey == key {
			return cloneInstance(instance), true
		}
	}
	return model.DatabaseInstance{}, false
}

func appendAlias(aliases []string, alias string) []string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return aliases
	}
	for _, existing := range aliases {
		if existing == alias {
			return aliases
		}
	}
	return append(aliases, alias)
}

func sameEndpoint(left model.DatabaseInstance, right model.DatabaseInstance) bool {
	return strings.EqualFold(left.Hostname, right.Hostname) && left.IPAddress == right.IPAddress && left.Port == right.Port
}

func endpointCollision(left model.DatabaseInstance, right model.DatabaseInstance) bool {
	for _, leftAddress := range model.EndpointAddress(left.Hostname, left.IPAddress, left.Port) {
		for _, rightAddress := range model.EndpointAddress(right.Hostname, right.IPAddress, right.Port) {
			if strings.EqualFold(leftAddress, rightAddress) {
				return true
			}
		}
	}
	return false
}

func reconcileInstanceCandidate(candidate *snapshot, discovered model.DatabaseInstance, now time.Time) (ReconcileResult, error) {
	if discovered.ClusterID == "" {
		return ReconcileResult{}, fmt.Errorf("cluster ID is required")
	}
	if !discovered.Engine.Valid() {
		return ReconcileResult{}, fmt.Errorf("unsupported engine: %s", discovered.Engine)
	}
	if discovered.Port <= 0 || discovered.Port > 65535 {
		return ReconcileResult{}, fmt.Errorf("invalid database endpoint port: %d", discovered.Port)
	}
	key, err := identity.InstanceKey(discovered.Engine, discovered.EngineIdentity)
	if err != nil {
		return ReconcileResult{}, err
	}

	var existingID model.ResourceID
	for resourceID, existing := range candidate.Instances {
		existingKey, keyErr := identity.InstanceKey(existing.Engine, existing.EngineIdentity)
		if keyErr == nil && existingKey == key {
			existingID = resourceID
			if existing.ClusterID != discovered.ClusterID {
				return ReconcileResult{}, fmt.Errorf("engine identity %s already belongs to a different cluster", key)
			}
			break
		}
	}
	for resourceID, existing := range candidate.Instances {
		if resourceID != existingID && existing.ClusterID == discovered.ClusterID && endpointCollision(existing, discovered) {
			return ReconcileResult{}, fmt.Errorf("endpoint is already owned by resource %s", resourceID)
		}
	}

	if existingID == "" {
		discovered.ResourceID = model.NewResourceID()
		discovered.MetadataRevision = 1
		discovered.CreatedAt = now
		discovered.UpdatedAt = now
		if discovered.DisplayName == "" {
			discovered.DisplayName = discovered.Hostname
		}
		candidate.Instances[discovered.ResourceID] = cloneInstance(discovered)
		return ReconcileResult{Instance: cloneInstance(discovered), Created: true}, nil
	}

	existing := candidate.Instances[existingID]
	if !sameEndpoint(existing, discovered) {
		for _, alias := range model.EndpointAddress(existing.Hostname, existing.IPAddress, existing.Port) {
			existing.Aliases = appendAlias(existing.Aliases, alias)
		}
	}
	for _, alias := range discovered.Aliases {
		existing.Aliases = appendAlias(existing.Aliases, alias)
	}
	existing.NodeID = discovered.NodeID
	existing.EngineIdentity = discovered.EngineIdentity.Clone()
	existing.DisplayName = discovered.DisplayName
	existing.Hostname = discovered.Hostname
	existing.IPAddress = discovered.IPAddress
	existing.Port = discovered.Port
	existing.Role = discovered.Role
	existing.Health = discovered.Health
	existing.Replication = discovered.Replication
	existing.Replication.SourceIdentity = discovered.Replication.SourceIdentity.Clone()
	if discovered.Replication.LagSeconds != nil {
		lagSeconds := *discovered.Replication.LagSeconds
		existing.Replication.LagSeconds = &lagSeconds
	}
	existing.Maintenance = discovered.Maintenance
	existing.PromotionEligible = discovered.PromotionEligible
	existing.EngineMetadata = make(map[string]string, len(discovered.EngineMetadata))
	for key, value := range discovered.EngineMetadata {
		existing.EngineMetadata[key] = value
	}
	existing.MetadataRevision++
	existing.UpdatedAt = now
	if existing.DisplayName == "" {
		existing.DisplayName = existing.Hostname
	}
	candidate.Instances[existingID] = cloneInstance(existing)
	return ReconcileResult{Instance: cloneInstance(existing), Updated: true}, nil
}

func (repository *Repository) ReconcileInstance(discovered model.DatabaseInstance) (ReconcileResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	now := repository.now().UTC()
	next := repository.snapshot
	next.Instances = cloneInstanceMap(repository.snapshot.Instances)
	result, err := reconcileInstanceCandidate(&next, discovered, now)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := repository.persistSnapshotLocked(next); err != nil {
		return ReconcileResult{}, err
	}
	repository.snapshot = next
	return result, nil
}

func bindDiscoveryEndpoint(candidate *snapshot, clusterID model.ResourceID, endpointID model.ResourceID, instanceID model.ResourceID, now time.Time) (model.Endpoint, error) {
	endpoint, exists := candidate.Endpoints[clusterID][endpointID]
	if !exists {
		return model.Endpoint{}, fmt.Errorf("unknown endpoint ID: %s", endpointID)
	}
	if !endpoint.Active || endpoint.Kind != model.EndpointDatabase {
		return model.Endpoint{}, fmt.Errorf("endpoint is not an active database endpoint: %s", endpointID)
	}
	endpoint.InstanceID = instanceID
	endpoint.MetadataRevision++
	endpoint.UpdatedAt = now
	candidate.Endpoints[clusterID][endpointID] = endpoint
	return endpoint, nil
}

func replaceDiscoveryLinks(candidate *snapshot, clusterID model.ResourceID, links []model.ReplicationLink, now time.Time) error {
	existingByEdge := make(map[replicationEdge]model.ReplicationLink, len(candidate.ReplicationLinks[clusterID]))
	for _, existing := range candidate.ReplicationLinks[clusterID] {
		existingByEdge[replicationEdge{existing.SourceInstanceID, existing.TargetInstanceID}] = existing
	}
	replacement := make([]model.ReplicationLink, len(links))
	for index, link := range links {
		if link.ClusterID != "" && link.ClusterID != clusterID {
			return fmt.Errorf("replication link cluster ID does not match cluster")
		}
		link.ClusterID = clusterID
		if existing, exists := existingByEdge[replicationEdge{link.SourceInstanceID, link.TargetInstanceID}]; exists {
			link.ResourceID = existing.ResourceID
			link.CreatedAt = existing.CreatedAt
			link.MetadataRevision = existing.MetadataRevision + 1
		} else {
			if link.ResourceID == "" {
				link.ResourceID = model.NewResourceID()
			}
			link.CreatedAt = now
			link.MetadataRevision = 1
		}
		link.UpdatedAt = now
		replacement[index] = cloneReplicationLink(link)
	}
	candidate.ReplicationLinks[clusterID] = replacement
	return nil
}

func appendDiscoveryMetricSamples(candidate *snapshot, clusterID model.ResourceID, samples []model.MetricSample, limit int) {
	byInstance := make(map[model.ResourceID][]model.MetricSample)
	for _, sample := range candidate.MetricSamples[clusterID] {
		byInstance[sample.InstanceID] = append(byInstance[sample.InstanceID], cloneMetricSample(sample))
	}
	for _, sample := range samples {
		byInstance[sample.InstanceID] = append(byInstance[sample.InstanceID], cloneMetricSample(sample))
	}
	bounded := make([]model.MetricSample, 0)
	for _, instanceSamples := range byInstance {
		sort.SliceStable(instanceSamples, func(i int, j int) bool {
			return instanceSamples[i].ObservedAt.Before(instanceSamples[j].ObservedAt)
		})
		if len(instanceSamples) > limit {
			instanceSamples = instanceSamples[len(instanceSamples)-limit:]
		}
		bounded = append(bounded, instanceSamples...)
	}
	sort.SliceStable(bounded, func(i int, j int) bool {
		if bounded[i].ObservedAt.Equal(bounded[j].ObservedAt) {
			return bounded[i].InstanceID < bounded[j].InstanceID
		}
		return bounded[i].ObservedAt.Before(bounded[j].ObservedAt)
	})
	candidate.MetricSamples[clusterID] = bounded
}

func replaceDiscoveryAnomalies(candidate *snapshot, clusterID model.ResourceID, anomalies []model.MetadataAnomaly, now time.Time) error {
	existingAnomalies := cloneAnomalyMap(candidate.Anomalies)
	for resourceID, anomaly := range candidate.Anomalies {
		if anomaly.ClusterID == clusterID {
			delete(candidate.Anomalies, resourceID)
		}
	}
	for _, anomaly := range anomalies {
		if anomaly.ClusterID != "" && anomaly.ClusterID != clusterID {
			return fmt.Errorf("anomaly cluster ID does not match cluster")
		}
		anomaly.ClusterID = clusterID
		if anomaly.ResourceID == "" {
			anomaly.ResourceID = model.NewResourceID()
		}
		if existing, exists := existingAnomalies[anomaly.ResourceID]; exists {
			if existing.ClusterID != clusterID {
				return fmt.Errorf("anomaly resource already belongs to cluster %s", existing.ClusterID)
			}
			anomaly.CreatedAt = existing.CreatedAt
			anomaly.MetadataRevision = existing.MetadataRevision + 1
		} else {
			if _, exists := candidate.Anomalies[anomaly.ResourceID]; exists {
				return fmt.Errorf("duplicate anomaly resource ID: %s", anomaly.ResourceID)
			}
			anomaly.CreatedAt = now
			anomaly.MetadataRevision = 1
		}
		anomaly.UpdatedAt = now
		candidate.Anomalies[anomaly.ResourceID] = anomaly
	}
	return nil
}

func (repository *Repository) ApplyDiscoveryRefresh(refresh DiscoveryRefresh) (model.TopologySnapshot, error) {
	if refresh.ClusterID == "" {
		return model.TopologySnapshot{}, fmt.Errorf("cluster ID is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	cluster, exists := repository.snapshot.Clusters[refresh.ClusterID]
	if !exists {
		return model.TopologySnapshot{}, fmt.Errorf("unknown cluster ID: %s", refresh.ClusterID)
	}

	now := repository.now().UTC()
	next := cloneDiscoverySnapshot(repository.snapshot)
	observations := append([]DiscoveryObservation{}, refresh.Observations...)
	sort.SliceStable(observations, func(i int, j int) bool { return observations[i].EndpointID < observations[j].EndpointID })
	observedInstances := make(map[model.ResourceID]model.DatabaseInstance)
	instanceIDsByIdentity := make(map[string]model.ResourceID)
	probes := make([]model.ProbeStatus, 0, len(observations))
	metricSamples := make([]model.MetricSample, 0)

	for _, observation := range observations {
		endpoint, endpointExists := next.Endpoints[refresh.ClusterID][observation.EndpointID]
		if !endpointExists {
			return model.TopologySnapshot{}, fmt.Errorf("unknown endpoint ID: %s", observation.EndpointID)
		}
		discovered := observation.Instance
		if discovered.ClusterID != "" && discovered.ClusterID != refresh.ClusterID {
			return model.TopologySnapshot{}, fmt.Errorf("discovered instance cluster ID does not match cluster")
		}
		if discovered.Engine != "" && discovered.Engine != cluster.Engine {
			return model.TopologySnapshot{}, fmt.Errorf("discovered instance engine does not match cluster")
		}
		discovered.ClusterID = refresh.ClusterID
		discovered.Engine = cluster.Engine
		if discovered.Hostname == "" {
			discovered.Hostname = endpoint.Hostname
		}
		if discovered.IPAddress == "" {
			discovered.IPAddress = endpoint.IPAddress
		}
		if discovered.Port == 0 {
			discovered.Port = endpoint.Port
		}
		result, err := reconcileInstanceCandidate(&next, discovered, now)
		if err != nil {
			return model.TopologySnapshot{}, fmt.Errorf("reconcile endpoint %s: %w", observation.EndpointID, err)
		}
		if _, err := bindDiscoveryEndpoint(&next, refresh.ClusterID, observation.EndpointID, result.Instance.ResourceID, now); err != nil {
			return model.TopologySnapshot{}, err
		}
		observedInstances[result.Instance.ResourceID] = result.Instance
		key, err := identity.InstanceKey(result.Instance.Engine, result.Instance.EngineIdentity)
		if err != nil {
			return model.TopologySnapshot{}, err
		}
		instanceIDsByIdentity[key] = result.Instance.ResourceID
		probes = append(probes, model.ProbeStatus{EndpointID: observation.EndpointID, InstanceID: result.Instance.ResourceID, Health: result.Instance.Health})
		for _, sample := range observation.Metrics {
			sample.InstanceID = result.Instance.ResourceID
			metricSamples = append(metricSamples, sample)
		}
	}

	instances := make([]model.DatabaseInstance, 0, len(observedInstances))
	links := make([]model.ReplicationLink, 0)
	for _, instance := range observedInstances {
		instances = append(instances, cloneInstance(instance))
		if len(instance.Replication.SourceIdentity) == 0 {
			continue
		}
		key, err := identity.InstanceKey(instance.Engine, instance.Replication.SourceIdentity)
		if err != nil {
			continue
		}
		sourceInstanceID, sourceExists := instanceIDsByIdentity[key]
		if !sourceExists || sourceInstanceID == instance.ResourceID {
			continue
		}
		links = append(links, model.ReplicationLink{
			ClusterID:        refresh.ClusterID,
			SourceInstanceID: sourceInstanceID,
			TargetInstanceID: instance.ResourceID,
			Healthy: instance.Health.State == model.HealthHealthy &&
				instance.Replication.IOThread == model.ThreadRunning &&
				instance.Replication.SQLThread == model.ThreadRunning,
			LagSeconds: instance.Replication.LagSeconds,
		})
	}
	if err := replaceDiscoveryLinks(&next, refresh.ClusterID, links, now); err != nil {
		return model.TopologySnapshot{}, err
	}
	appendDiscoveryMetricSamples(&next, refresh.ClusterID, metricSamples, discoveryMetricSampleLimit)
	if err := replaceDiscoveryAnomalies(&next, refresh.ClusterID, refresh.Anomalies, now); err != nil {
		return model.TopologySnapshot{}, err
	}
	if err := repository.persistSnapshotLocked(next); err != nil {
		return model.TopologySnapshot{}, err
	}
	repository.snapshot = next

	persistedLinks := make([]model.ReplicationLink, len(next.ReplicationLinks[refresh.ClusterID]))
	for index, link := range next.ReplicationLinks[refresh.ClusterID] {
		persistedLinks[index] = cloneReplicationLink(link)
	}
	persistedAnomalies := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range next.Anomalies {
		if anomaly.ClusterID == refresh.ClusterID {
			persistedAnomalies = append(persistedAnomalies, anomaly)
		}
	}
	sort.Slice(instances, func(i int, j int) bool { return instances[i].ResourceID < instances[j].ResourceID })
	sort.Slice(persistedLinks, func(i int, j int) bool { return persistedLinks[i].ResourceID < persistedLinks[j].ResourceID })
	sort.Slice(probes, func(i int, j int) bool { return probes[i].EndpointID < probes[j].EndpointID })
	sort.Slice(persistedAnomalies, func(i int, j int) bool { return persistedAnomalies[i].ResourceID < persistedAnomalies[j].ResourceID })
	return model.TopologySnapshot{
		ClusterID:  refresh.ClusterID,
		Instances:  instances,
		Links:      persistedLinks,
		Probes:     probes,
		Anomalies:  persistedAnomalies,
		ObservedAt: now,
	}, nil
}

func (repository *Repository) Instances(clusterID model.ResourceID) []model.DatabaseInstance {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	instances := make([]model.DatabaseInstance, 0)
	for _, instance := range repository.snapshot.Instances {
		if clusterID == "" || instance.ClusterID == clusterID {
			instances = append(instances, cloneInstance(instance))
		}
	}
	sort.Slice(instances, func(i int, j int) bool { return instances[i].DisplayName < instances[j].DisplayName })
	return instances
}

func (repository *Repository) Anomalies() []model.MetadataAnomaly {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.MetadataAnomaly, 0, len(repository.snapshot.Anomalies))
	for _, anomaly := range repository.snapshot.Anomalies {
		result = append(result, anomaly)
	}
	sort.Slice(result, func(i int, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (repository *Repository) ReplaceClusterAnomalies(clusterID model.ResourceID, anomalies []model.MetadataAnomaly) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[clusterID]; !exists {
		return fmt.Errorf("unknown cluster ID: %s", clusterID)
	}

	now := repository.now().UTC()
	next := repository.snapshot
	next.Anomalies = cloneAnomalyMap(repository.snapshot.Anomalies)
	for resourceID, anomaly := range next.Anomalies {
		if anomaly.ClusterID == clusterID {
			delete(next.Anomalies, resourceID)
		}
	}
	for _, anomaly := range anomalies {
		if anomaly.ClusterID != "" && anomaly.ClusterID != clusterID {
			return fmt.Errorf("anomaly cluster ID does not match cluster")
		}
		anomaly.ClusterID = clusterID
		if anomaly.ResourceID == "" {
			anomaly.ResourceID = model.NewResourceID()
		}
		if existing, exists := repository.snapshot.Anomalies[anomaly.ResourceID]; exists {
			if existing.ClusterID != clusterID {
				return fmt.Errorf("anomaly resource already belongs to cluster %s", existing.ClusterID)
			}
			anomaly.CreatedAt = existing.CreatedAt
			anomaly.MetadataRevision = existing.MetadataRevision + 1
		} else {
			if _, exists := next.Anomalies[anomaly.ResourceID]; exists {
				return fmt.Errorf("duplicate anomaly resource ID: %s", anomaly.ResourceID)
			}
			anomaly.CreatedAt = now
			anomaly.MetadataRevision = 1
		}
		anomaly.UpdatedAt = now
		next.Anomalies[anomaly.ResourceID] = anomaly
	}
	if err := repository.persistSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) RecordAudit(event model.AuditEvent) {
	now := repository.now().UTC()
	if event.ResourceID == "" {
		event.ResourceID = model.NewResourceID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	event.MetadataRevision = 1
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.snapshot.Audits = append(repository.snapshot.Audits, event)
	_ = repository.persistLocked()
}

func (repository *Repository) RecordReport(report model.Report) {
	now := repository.now().UTC()
	if report.ResourceID == "" {
		report.ResourceID = model.NewResourceID()
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}
	report.UpdatedAt = now
	report.MetadataRevision = 1
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.snapshot.Reports = append(repository.snapshot.Reports, report)
	_ = repository.persistLocked()
}

func (repository *Repository) Audits() []model.AuditEvent {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return append([]model.AuditEvent{}, repository.snapshot.Audits...)
}

func (repository *Repository) Reports() []model.Report {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return append([]model.Report{}, repository.snapshot.Reports...)
}
