package store

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

var (
	ErrValidation           = errors.New("repository validation failed")
	ErrConflict             = errors.New("repository conflict")
	ErrStaleObservation     = errors.New("stale topology observation")
	ErrInventoryChanged     = errors.New("discovery inventory changed")
	ErrPostCommitDurability = errors.New("metadata snapshot committed with durability warning")
)

func validationError(format string, arguments ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, arguments...))
}

func conflictError(format string, arguments ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, arguments...))
}

type postCommitDurabilityError struct {
	cause error
}

func (failure *postCommitDurabilityError) Error() string {
	return fmt.Sprintf("%s: sync metadata directory: %v", ErrPostCommitDurability, failure.cause)
}

func (failure *postCommitDurabilityError) Unwrap() error { return failure.cause }

func (failure *postCommitDurabilityError) Is(target error) bool {
	return target == ErrPostCommitDurability || errors.Is(failure.cause, target)
}

func (*postCommitDurabilityError) Committed() bool { return true }

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
	ClusterID             model.ResourceID
	InventoryGeneration   uint64
	Observations          []DiscoveryObservation
	NativeLinks           []model.NativeReplicationLink
	TopologyAuthoritative bool
	Probes                []model.ProbeStatus
	Health                model.Health
	ObservedAt            time.Time
	Anomalies             []model.MetadataAnomaly
}

type DiscoveryInventory struct {
	Cluster    model.DatabaseCluster
	Endpoints  []model.Endpoint
	Generation uint64
}

type MetadataCoordinates struct {
	Instance   model.DatabaseInstance
	EndpointID model.ResourceID
}

type discoveryMetricCandidate struct {
	endpointID model.ResourceID
	sample     model.MetricSample
	complete   bool
}

const discoveryMetricSampleLimit = 60

type snapshot struct {
	Clusters              map[model.ResourceID]model.DatabaseCluster               `json:"clusters"`
	Nodes                 map[model.ResourceID]model.DatabaseNode                  `json:"nodes"`
	Instances             map[model.ResourceID]model.DatabaseInstance              `json:"instances"`
	Endpoints             map[model.ResourceID]map[model.ResourceID]model.Endpoint `json:"endpoints"`
	HAEndpoints           map[model.ResourceID]model.HAEndpoint                    `json:"ha_endpoints"`
	CoordinationLeases    map[model.ResourceID]coordination.LeaseRecord            `json:"coordination_leases"`
	OperationLocks        map[model.ResourceID]coordination.OperationLockRecord    `json:"operation_locks"`
	LifecycleTasks        map[model.ResourceID]lifecycle.Task                      `json:"lifecycle_tasks"`
	ReplicationLinks      map[model.ResourceID][]model.ReplicationLink             `json:"replication_links"`
	MetricSamples         map[model.ResourceID][]model.MetricSample                `json:"metric_samples"`
	TopologySnapshots     map[model.ResourceID]model.TopologySnapshot              `json:"topology_snapshots"`
	ObservationWatermarks map[model.ResourceID]time.Time                           `json:"observation_watermarks"`
	InventoryGenerations  map[model.ResourceID]uint64                              `json:"inventory_generations"`
	Anomalies             map[model.ResourceID]model.MetadataAnomaly               `json:"anomalies"`
	Operations            map[model.ResourceID]model.OperationRecord               `json:"operations"`
	OperationKeys         map[string]model.ResourceID                              `json:"operation_keys"`
	Audits                []model.AuditEvent                                       `json:"audits"`
	Reports               []model.Report                                           `json:"reports"`
}

type Repository struct {
	mu              sync.RWMutex
	mutationMu      sync.Mutex
	consensusCommit sync.Mutex
	stateRevision   uint64
	path            string
	snapshot        snapshot
	consensus       SnapshotConsensus
	now             func() time.Time
	syncFile        func(*os.File) error
	syncDirectory   func(string) error
}

func emptySnapshot() snapshot {
	return snapshot{
		Clusters:              map[model.ResourceID]model.DatabaseCluster{},
		Nodes:                 map[model.ResourceID]model.DatabaseNode{},
		Instances:             map[model.ResourceID]model.DatabaseInstance{},
		Endpoints:             map[model.ResourceID]map[model.ResourceID]model.Endpoint{},
		HAEndpoints:           map[model.ResourceID]model.HAEndpoint{},
		CoordinationLeases:    map[model.ResourceID]coordination.LeaseRecord{},
		OperationLocks:        map[model.ResourceID]coordination.OperationLockRecord{},
		LifecycleTasks:        map[model.ResourceID]lifecycle.Task{},
		ReplicationLinks:      map[model.ResourceID][]model.ReplicationLink{},
		MetricSamples:         map[model.ResourceID][]model.MetricSample{},
		TopologySnapshots:     map[model.ResourceID]model.TopologySnapshot{},
		ObservationWatermarks: map[model.ResourceID]time.Time{},
		InventoryGenerations:  map[model.ResourceID]uint64{},
		Anomalies:             map[model.ResourceID]model.MetadataAnomaly{},
		Operations:            map[model.ResourceID]model.OperationRecord{},
		OperationKeys:         map[string]model.ResourceID{},
		Audits:                []model.AuditEvent{},
		Reports:               []model.Report{},
	}
}

func NewMemory() *Repository {
	return &Repository{
		snapshot:      emptySnapshot(),
		now:           time.Now,
		syncFile:      func(file *os.File) error { return file.Sync() },
		syncDirectory: syncMetadataDirectory,
	}
}

func syncMetadataDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
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
	decoded, metadata, err := decodeSnapshotState(contents)
	if err != nil {
		return nil, fmt.Errorf("decode metadata snapshot: %w", err)
	}
	repository.snapshot = decoded
	if metadata.StateRevision != nil {
		repository.stateRevision = *metadata.StateRevision
	}
	return repository, nil
}

func terminalReportStatus(status model.OperationStatus) bool {
	switch status {
	case model.OperationBlocked, model.OperationSucceeded, model.OperationFailed, model.OperationIndeterminate, model.OperationUnsupported:
		return true
	default:
		return false
	}
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

func cloneTopologySnapshot(value model.TopologySnapshot) model.TopologySnapshot {
	copy := value
	copy.Instances = make([]model.DatabaseInstance, len(value.Instances))
	for index, instance := range value.Instances {
		copy.Instances[index] = cloneInstance(instance)
	}
	copy.Links = make([]model.ReplicationLink, len(value.Links))
	for index, link := range value.Links {
		copy.Links[index] = cloneReplicationLink(link)
	}
	copy.Probes = append([]model.ProbeStatus{}, value.Probes...)
	copy.Anomalies = append([]model.MetadataAnomaly{}, value.Anomalies...)
	return copy
}

func cloneOperationPlan(plan model.OperationPlan) model.OperationPlan {
	copy := plan
	copy.Checks = append([]model.Check{}, plan.Checks...)
	copy.Steps = append([]model.PlanStep{}, plan.Steps...)
	copy.ResourceRevisions = make(map[model.ResourceID]uint64, len(plan.ResourceRevisions))
	for resourceID, revision := range plan.ResourceRevisions {
		copy.ResourceRevisions[resourceID] = revision
	}
	return copy
}

func cloneOperationRecord(operation model.OperationRecord) model.OperationRecord {
	copy := operation
	copy.Plan = cloneOperationPlan(operation.Plan)
	copy.Attempts = append([]model.StepAttempt{}, operation.Attempts...)
	copy.Verification.Checks = append([]model.Check{}, operation.Verification.Checks...)
	return copy
}

func cloneOperationMap(operations map[model.ResourceID]model.OperationRecord) map[model.ResourceID]model.OperationRecord {
	copy := make(map[model.ResourceID]model.OperationRecord, len(operations))
	for resourceID, operation := range operations {
		copy[resourceID] = cloneOperationRecord(operation)
	}
	return copy
}

func cloneOperationKeyMap(keys map[string]model.ResourceID) map[string]model.ResourceID {
	copy := make(map[string]model.ResourceID, len(keys))
	for key, resourceID := range keys {
		copy[key] = resourceID
	}
	return copy
}

func cloneClusterMap(clusters map[model.ResourceID]model.DatabaseCluster) map[model.ResourceID]model.DatabaseCluster {
	copy := make(map[model.ResourceID]model.DatabaseCluster, len(clusters))
	for resourceID, cluster := range clusters {
		copy[resourceID] = cloneCluster(cluster)
	}
	return copy
}

func cloneTopologySnapshotMap(values map[model.ResourceID]model.TopologySnapshot) map[model.ResourceID]model.TopologySnapshot {
	copy := make(map[model.ResourceID]model.TopologySnapshot, len(values))
	for clusterID, value := range values {
		copy[clusterID] = cloneTopologySnapshot(value)
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
	copy.Clusters = cloneClusterMap(value.Clusters)
	copy.Nodes = cloneNodeMap(value.Nodes)
	copy.Instances = cloneInstanceMap(value.Instances)
	copy.Endpoints = cloneEndpointMap(value.Endpoints)
	copy.HAEndpoints = cloneHAEndpointMap(value.HAEndpoints)
	copy.CoordinationLeases = cloneCoordinationLeaseMap(value.CoordinationLeases)
	copy.OperationLocks = cloneOperationLockMap(value.OperationLocks)
	copy.LifecycleTasks = cloneLifecycleTaskMap(value.LifecycleTasks)
	copy.ReplicationLinks = cloneReplicationLinkMap(value.ReplicationLinks)
	copy.MetricSamples = cloneMetricSampleMap(value.MetricSamples)
	copy.TopologySnapshots = cloneTopologySnapshotMap(value.TopologySnapshots)
	copy.ObservationWatermarks = cloneTimeMap(value.ObservationWatermarks)
	copy.InventoryGenerations = cloneUint64Map(value.InventoryGenerations)
	copy.Anomalies = cloneAnomalyMap(value.Anomalies)
	copy.Operations = cloneOperationMap(value.Operations)
	copy.OperationKeys = cloneOperationKeyMap(value.OperationKeys)
	return copy
}

func cloneNode(node model.DatabaseNode) model.DatabaseNode {
	copy := node
	copy.Aliases = append([]string{}, node.Aliases...)
	return copy
}

func cloneNodeMap(nodes map[model.ResourceID]model.DatabaseNode) map[model.ResourceID]model.DatabaseNode {
	copy := make(map[model.ResourceID]model.DatabaseNode, len(nodes))
	for resourceID, node := range nodes {
		copy[resourceID] = cloneNode(node)
	}
	return copy
}

func cloneHAEndpointMap(values map[model.ResourceID]model.HAEndpoint) map[model.ResourceID]model.HAEndpoint {
	copy := make(map[model.ResourceID]model.HAEndpoint, len(values))
	for resourceID, endpoint := range values {
		copy[resourceID] = endpoint
	}
	return copy
}

func cloneCoordinationLeaseMap(values map[model.ResourceID]coordination.LeaseRecord) map[model.ResourceID]coordination.LeaseRecord {
	copy := make(map[model.ResourceID]coordination.LeaseRecord, len(values))
	for resourceID, lease := range values {
		copy[resourceID] = lease
	}
	return copy
}

func cloneOperationLockMap(values map[model.ResourceID]coordination.OperationLockRecord) map[model.ResourceID]coordination.OperationLockRecord {
	copy := make(map[model.ResourceID]coordination.OperationLockRecord, len(values))
	for resourceID, lock := range values {
		copy[resourceID] = lock
	}
	return copy
}

func cloneLifecycleTask(task lifecycle.Task) lifecycle.Task {
	copy := task
	copy.Request.Targets = append([]lifecycle.Target{}, task.Request.Targets...)
	copy.Plan.Targets = append([]lifecycle.TargetPlan{}, task.Plan.Targets...)
	copy.Plan.Checks = append([]model.Check{}, task.Plan.Checks...)
	copy.Stages = append([]lifecycle.StageState{}, task.Stages...)
	copy.Checks = append([]model.Check{}, task.Checks...)
	copy.LogTail = append([]string{}, task.LogTail...)
	return copy
}

func cloneLifecycleTaskMap(values map[model.ResourceID]lifecycle.Task) map[model.ResourceID]lifecycle.Task {
	copy := make(map[model.ResourceID]lifecycle.Task, len(values))
	for resourceID, task := range values {
		copy[resourceID] = cloneLifecycleTask(task)
	}
	return copy
}

func cloneTimeMap(values map[model.ResourceID]time.Time) map[model.ResourceID]time.Time {
	copy := make(map[model.ResourceID]time.Time, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func cloneUint64Map(values map[model.ResourceID]uint64) map[model.ResourceID]uint64 {
	copy := make(map[model.ResourceID]uint64, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func (repository *Repository) persistSnapshotLocked(value snapshot) error {
	return repository.persistSnapshotRevisionLocked(value, repository.stateRevision)
}

func (repository *Repository) persistSnapshotRevisionLocked(value snapshot, stateRevision uint64) error {
	if repository.path == "" {
		repository.snapshot = value
		repository.stateRevision = stateRevision
		return nil
	}
	contents, err := encodeSnapshotRevision(value, stateRevision, "")
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
	if err := repository.syncFile(temporary); err != nil {
		temporary.Close()
		return fmt.Errorf("sync metadata snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close metadata snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, repository.path); err != nil {
		return fmt.Errorf("publish metadata snapshot: %w", err)
	}
	// Rename is the commit boundary. Keep live state aligned with the file even
	// when the subsequent directory sync cannot confirm crash durability.
	repository.snapshot = value
	repository.stateRevision = stateRevision
	if err := repository.syncDirectory(filepath.Dir(repository.path)); err != nil {
		return &postCommitDurabilityError{cause: err}
	}
	return nil
}

func (repository *Repository) UpsertCluster(cluster model.DatabaseCluster) (model.DatabaseCluster, error) {
	if !cluster.Engine.Valid() {
		return model.DatabaseCluster{}, validationError("unsupported engine: %s", cluster.Engine)
	}
	displayName := strings.TrimSpace(cluster.DisplayName)
	if displayName == "" {
		return model.DatabaseCluster{}, validationError("cluster display name is required")
	}
	cluster.DisplayName = displayName
	if cluster.ResourceID == "" {
		cluster.ResourceID = model.NewResourceID()
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if existing, ok := repository.snapshot.Clusters[cluster.ResourceID]; ok && cluster.Engine != existing.Engine {
		return model.DatabaseCluster{}, conflictError("cluster engine is immutable")
	}
	candidateIdentityKey, hasCandidateIdentity, err := validatedClusterIdentityKey(cluster.Engine, cluster.EngineIdentity)
	if err != nil {
		return model.DatabaseCluster{}, err
	}
	for resourceID, existing := range repository.snapshot.Clusters {
		if resourceID != cluster.ResourceID && strings.EqualFold(strings.TrimSpace(existing.DisplayName), cluster.DisplayName) {
			return model.DatabaseCluster{}, conflictError("cluster display name already exists")
		}
		if resourceID != cluster.ResourceID && hasCandidateIdentity {
			existingKey, existingHasIdentity, _ := validatedClusterIdentityKey(existing.Engine, existing.EngineIdentity)
			if existingHasIdentity && existingKey == candidateIdentityKey {
				return model.DatabaseCluster{}, conflictError("cluster native identity already exists")
			}
		}
	}
	now := repository.now().UTC()
	if existing, ok := repository.snapshot.Clusters[cluster.ResourceID]; ok {
		existingKey, existingHasIdentity, _ := validatedClusterIdentityKey(existing.Engine, existing.EngineIdentity)
		if existingHasIdentity {
			if !hasCandidateIdentity || candidateIdentityKey != existingKey {
				return model.DatabaseCluster{}, conflictError("cluster native identity is immutable")
			}
			cluster.EngineIdentity = existing.EngineIdentity.Clone()
		}
		cluster.CreatedAt = existing.CreatedAt
		cluster.MetadataRevision = existing.MetadataRevision + 1
	} else {
		cluster.CreatedAt = now
		cluster.MetadataRevision = 1
	}
	cluster.UpdatedAt = now
	next := repository.snapshot
	next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	next.Clusters[cluster.ResourceID] = cloneCluster(cluster)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return model.DatabaseCluster{}, err
	}
	repository.snapshot = next
	return cloneCluster(cluster), nil
}

func validatedClusterIdentityKey(engine model.Engine, engineIdentity model.EngineIdentity) (string, bool, error) {
	if len(engineIdentity) == 0 {
		return "", false, nil
	}
	key, err := identity.ClusterKey(engine, engineIdentity)
	if err != nil {
		return "", false, validationError("invalid cluster native identity")
	}
	return key, true, nil
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

func (repository *Repository) DiscoveryInventory(clusterID model.ResourceID) (DiscoveryInventory, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	cluster, found := repository.snapshot.Clusters[clusterID]
	if !found {
		return DiscoveryInventory{}, false
	}
	result := DiscoveryInventory{Cluster: cloneCluster(cluster), Generation: repository.snapshot.InventoryGenerations[clusterID]}
	result.Endpoints = make([]model.Endpoint, 0, len(repository.snapshot.Endpoints[clusterID]))
	for _, endpoint := range repository.snapshot.Endpoints[clusterID] {
		result.Endpoints = append(result.Endpoints, endpoint)
	}
	sort.Slice(result.Endpoints, func(i, j int) bool { return result.Endpoints[i].ResourceID < result.Endpoints[j].ResourceID })
	return result, true
}

func (repository *Repository) ObservationWatermark(clusterID model.ResourceID) (time.Time, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	watermark, found := repository.snapshot.ObservationWatermarks[clusterID]
	return watermark, found
}

func validateEndpoint(endpoint model.Endpoint) error {
	if endpoint.ClusterID == "" {
		return validationError("cluster ID is required")
	}
	if endpoint.Port <= 0 || endpoint.Port > 65535 {
		return validationError("invalid endpoint port: %d", endpoint.Port)
	}
	if endpoint.Active && endpoint.Kind == model.EndpointDatabase && endpoint.Hostname == "" && endpoint.IPAddress == "" {
		return validationError("active database endpoint address is required")
	}
	return nil
}

func normalizeEndpoint(endpoint model.Endpoint) model.Endpoint {
	endpoint.Hostname = strings.TrimSpace(endpoint.Hostname)
	endpoint.IPAddress = strings.TrimSpace(endpoint.IPAddress)
	return endpoint
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

func validateEndpointInstanceBinding(instances map[model.ResourceID]model.DatabaseInstance, endpoint model.Endpoint) error {
	if endpoint.InstanceID == "" {
		return nil
	}
	instance, found := instances[endpoint.InstanceID]
	if !found {
		return validationError("endpoint instance binding is unknown")
	}
	if instance.ClusterID != endpoint.ClusterID {
		return validationError("endpoint instance binding belongs to a different cluster")
	}
	return nil
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
		return model.DatabaseCluster{}, nil, validationError("unsupported engine: %s", cluster.Engine)
	}
	displayName := strings.TrimSpace(cluster.DisplayName)
	if displayName == "" {
		return model.DatabaseCluster{}, nil, validationError("cluster display name is required")
	}
	cluster.DisplayName = displayName
	candidateIdentityKey, hasCandidateIdentity, err := validatedClusterIdentityKey(cluster.Engine, cluster.EngineIdentity)
	if err != nil {
		return model.DatabaseCluster{}, nil, err
	}
	if cluster.ResourceID == "" {
		cluster.ResourceID = model.NewResourceID()
	}
	endpointIDs := map[model.ResourceID]struct{}{cluster.ResourceID: {}}
	for index := range endpoints {
		if endpoints[index].ClusterID != "" && endpoints[index].ClusterID != cluster.ResourceID {
			return model.DatabaseCluster{}, nil, validationError("endpoint cluster ID does not match cluster")
		}
		endpoints[index].ClusterID = cluster.ResourceID
		endpoints[index] = normalizeEndpoint(endpoints[index])
		if err := validateEndpoint(endpoints[index]); err != nil {
			return model.DatabaseCluster{}, nil, err
		}
		if endpoints[index].ResourceID == "" {
			endpoints[index].ResourceID = model.NewResourceID()
		}
		if _, exists := endpointIDs[endpoints[index].ResourceID]; exists {
			return model.DatabaseCluster{}, nil, validationError("duplicate resource ID: %s", endpoints[index].ResourceID)
		}
		endpointIDs[endpoints[index].ResourceID] = struct{}{}
	}
	if endpointSetCollision(endpoints) {
		return model.DatabaseCluster{}, nil, validationError("duplicate active endpoint address")
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[cluster.ResourceID]; exists {
		return model.DatabaseCluster{}, nil, conflictError("cluster already exists: %s", cluster.ResourceID)
	}
	for _, existing := range repository.snapshot.Clusters {
		if strings.EqualFold(strings.TrimSpace(existing.DisplayName), cluster.DisplayName) {
			return model.DatabaseCluster{}, nil, conflictError("cluster display name already exists")
		}
		if hasCandidateIdentity {
			existingKey, existingHasIdentity, _ := validatedClusterIdentityKey(existing.Engine, existing.EngineIdentity)
			if existingHasIdentity && existingKey == candidateIdentityKey {
				return model.DatabaseCluster{}, nil, conflictError("cluster native identity already exists")
			}
		}
	}
	for _, endpoint := range endpoints {
		if err := validateEndpointInstanceBinding(repository.snapshot.Instances, endpoint); err != nil {
			return model.DatabaseCluster{}, nil, err
		}
		if _, exists := endpointClusterForID(repository.snapshot.Endpoints, endpoint.ResourceID); exists {
			return model.DatabaseCluster{}, nil, conflictError("endpoint resource already exists: %s", endpoint.ResourceID)
		}
		if !endpoint.Active || endpoint.Kind != model.EndpointDatabase {
			continue
		}
		for _, clusterEndpoints := range repository.snapshot.Endpoints {
			for _, existing := range clusterEndpoints {
				if existing.Active && existing.Kind == model.EndpointDatabase && endpointAddressCollision(endpoint, existing) {
					return model.DatabaseCluster{}, nil, conflictError("active database endpoint address is already registered")
				}
			}
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
	next.InventoryGenerations = cloneUint64Map(repository.snapshot.InventoryGenerations)
	next.InventoryGenerations[cluster.ResourceID] = 1
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			resultEndpoints := append([]model.Endpoint{}, endpoints...)
			return cloneCluster(cluster), resultEndpoints, err
		}
		return model.DatabaseCluster{}, nil, err
	}
	repository.snapshot = next
	resultEndpoints := make([]model.Endpoint, len(endpoints))
	copy(resultEndpoints, endpoints)
	return cloneCluster(cluster), resultEndpoints, nil
}

func (repository *Repository) UpsertEndpoint(endpoint model.Endpoint) (model.Endpoint, error) {
	endpoint = normalizeEndpoint(endpoint)
	if err := validateEndpoint(endpoint); err != nil {
		return model.Endpoint{}, err
	}
	if endpoint.ResourceID == "" {
		endpoint.ResourceID = model.NewResourceID()
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.snapshot.Clusters[endpoint.ClusterID]; !exists {
		return model.Endpoint{}, fmt.Errorf("unknown cluster ID: %s", endpoint.ClusterID)
	}
	if err := validateEndpointInstanceBinding(repository.snapshot.Instances, endpoint); err != nil {
		return model.Endpoint{}, err
	}
	if existingClusterID, exists := endpointClusterForID(repository.snapshot.Endpoints, endpoint.ResourceID); exists && existingClusterID != endpoint.ClusterID {
		return model.Endpoint{}, fmt.Errorf("endpoint resource already belongs to cluster %s", existingClusterID)
	}
	for _, clusterEndpoints := range repository.snapshot.Endpoints {
		for resourceID, existing := range clusterEndpoints {
			if resourceID != endpoint.ResourceID && endpoint.Active && endpoint.Kind == model.EndpointDatabase && existing.Active && existing.Kind == model.EndpointDatabase && endpointAddressCollision(endpoint, existing) {
				return model.Endpoint{}, fmt.Errorf("active database endpoint address is already owned by resource %s", resourceID)
			}
		}
	}
	now := repository.now().UTC()
	existing, existed := repository.snapshot.Endpoints[endpoint.ClusterID][endpoint.ResourceID]
	if existed {
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
	if endpointInvalidatesTopology(existing, existed, endpoint) {
		next.TopologySnapshots = cloneTopologySnapshotMap(repository.snapshot.TopologySnapshots)
		delete(next.TopologySnapshots, endpoint.ClusterID)
		next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
		cluster := next.Clusters[endpoint.ClusterID]
		cluster.Health = model.Health{State: model.HealthUnknown}
		cluster.MetadataRevision++
		cluster.UpdatedAt = now
		next.Clusters[endpoint.ClusterID] = cluster
		next.InventoryGenerations = cloneUint64Map(repository.snapshot.InventoryGenerations)
		next.InventoryGenerations[endpoint.ClusterID]++
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return model.Endpoint{}, err
	}
	repository.snapshot = next
	return endpoint, nil
}

func endpointInvalidatesTopology(existing model.Endpoint, existed bool, replacement model.Endpoint) bool {
	existingActiveDatabase := existed && existing.Active && existing.Kind == model.EndpointDatabase
	replacementActiveDatabase := replacement.Active && replacement.Kind == model.EndpointDatabase
	if existingActiveDatabase != replacementActiveDatabase {
		return true
	}
	return existingActiveDatabase && (existing.Hostname != replacement.Hostname || existing.IPAddress != replacement.IPAddress || existing.Port != replacement.Port || existing.InstanceID != replacement.InstanceID)
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
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
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
	if err := repository.commitSnapshotLocked(next); err != nil {
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
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
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
	if err := repository.commitSnapshotLocked(next); err != nil {
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
	if discovered.NodeID == "" {
		discovered.NodeID = nodeIDForCoordinates(candidate.Nodes, discovered.Hostname, discovered.IPAddress)
	}
	if discovered.NodeID != "" {
		if _, found := candidate.Nodes[discovered.NodeID]; !found {
			return ReconcileResult{}, validationError("database instance node is not registered")
		}
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
	if discovered.NodeID != "" {
		existing.NodeID = discovered.NodeID
	}
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

func nodeIDForCoordinates(nodes map[model.ResourceID]model.DatabaseNode, hostname, ipAddress string) model.ResourceID {
	hostname = strings.TrimSpace(hostname)
	ipAddress = strings.TrimSpace(ipAddress)
	matched := model.ResourceID("")
	for resourceID, node := range nodes {
		if !node.Active || !nodeMatchesCoordinate(node, hostname, ipAddress) {
			continue
		}
		if matched != "" {
			return ""
		}
		matched = resourceID
	}
	return matched
}

func nodeMatchesCoordinate(node model.DatabaseNode, hostname, ipAddress string) bool {
	if hostname != "" && strings.EqualFold(node.Hostname, hostname) {
		return true
	}
	if ipAddress != "" && node.IPAddress == ipAddress {
		return true
	}
	for _, alias := range node.Aliases {
		if (hostname != "" && strings.EqualFold(alias, hostname)) || (ipAddress != "" && alias == ipAddress) {
			return true
		}
	}
	return false
}

func (repository *Repository) ReconcileInstance(discovered model.DatabaseInstance) (ReconcileResult, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	now := repository.now().UTC()
	next := repository.snapshot
	next.Instances = cloneInstanceMap(repository.snapshot.Instances)
	result, err := reconcileInstanceCandidate(&next, discovered, now)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return ReconcileResult{}, err
	}
	repository.snapshot = next
	return result, nil
}

func (repository *Repository) ReconcileMetadataCoordinates(update MetadataCoordinates) (model.DatabaseInstance, model.Endpoint, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	existing, found := repository.snapshot.Instances[update.Instance.ResourceID]
	if !found || existing.ClusterID != update.Instance.ClusterID {
		return model.DatabaseInstance{}, model.Endpoint{}, validationError("unknown database instance")
	}
	cluster, found := repository.snapshot.Clusters[existing.ClusterID]
	if !found || cluster.Engine != existing.Engine || update.Instance.Engine != existing.Engine {
		return model.DatabaseInstance{}, model.Endpoint{}, validationError("metadata engine does not match canonical inventory")
	}
	existingIdentityKey, err := identity.InstanceKey(existing.Engine, existing.EngineIdentity)
	if err != nil {
		return model.DatabaseInstance{}, model.Endpoint{}, validationError("canonical database instance identity is invalid")
	}
	requestedIdentityKey, err := identity.InstanceKey(update.Instance.Engine, update.Instance.EngineIdentity)
	if err != nil || requestedIdentityKey != existingIdentityKey {
		return model.DatabaseInstance{}, model.Endpoint{}, validationError("metadata native identity does not match canonical inventory")
	}
	if update.Instance.Port <= 0 || update.Instance.Port > 65535 {
		return model.DatabaseInstance{}, model.Endpoint{}, validationError("invalid database endpoint port")
	}
	if update.Instance.NodeID != "" {
		if _, found := repository.snapshot.Nodes[update.Instance.NodeID]; !found {
			return model.DatabaseInstance{}, model.Endpoint{}, validationError("database instance node is not registered")
		}
	}
	bound := make([]model.Endpoint, 0)
	for _, endpoint := range repository.snapshot.Endpoints[existing.ClusterID] {
		if endpoint.Active && endpoint.Kind == model.EndpointDatabase && endpoint.InstanceID == existing.ResourceID {
			bound = append(bound, endpoint)
		}
	}
	var selected model.Endpoint
	if update.EndpointID == "" {
		if len(bound) != 1 {
			return model.DatabaseInstance{}, model.Endpoint{}, validationError("endpoint_id is required when an instance has multiple active endpoints")
		}
		selected = bound[0]
	} else {
		for _, endpoint := range bound {
			if endpoint.ResourceID == update.EndpointID {
				selected = endpoint
				break
			}
		}
		if selected.ResourceID == "" {
			return model.DatabaseInstance{}, model.Endpoint{}, validationError("endpoint_id is not an active endpoint bound to the instance")
		}
	}

	replacementEndpoint := selected
	replacementEndpoint.Hostname = update.Instance.Hostname
	replacementEndpoint.IPAddress = update.Instance.IPAddress
	replacementEndpoint.Port = update.Instance.Port
	replacementEndpoint = normalizeEndpoint(replacementEndpoint)
	if err := validateEndpoint(replacementEndpoint); err != nil {
		return model.DatabaseInstance{}, model.Endpoint{}, err
	}
	for _, clusterEndpoints := range repository.snapshot.Endpoints {
		for resourceID, endpoint := range clusterEndpoints {
			if resourceID != selected.ResourceID && endpoint.Active && endpoint.Kind == model.EndpointDatabase && endpointAddressCollision(replacementEndpoint, endpoint) {
				return model.DatabaseInstance{}, model.Endpoint{}, conflictError("active database endpoint address is already registered")
			}
		}
	}

	now := repository.now().UTC()
	replacement := cloneInstance(existing)
	for _, alias := range model.EndpointAddress(selected.Hostname, selected.IPAddress, selected.Port) {
		replacement.Aliases = appendAlias(replacement.Aliases, alias)
	}
	for _, alias := range update.Instance.Aliases {
		replacement.Aliases = appendAlias(replacement.Aliases, alias)
	}
	replacement.DisplayName = update.Instance.DisplayName
	replacement.Hostname = replacementEndpoint.Hostname
	replacement.IPAddress = replacementEndpoint.IPAddress
	replacement.Port = update.Instance.Port
	if update.Instance.NodeID != "" {
		replacement.NodeID = update.Instance.NodeID
	}
	replacement.MetadataRevision++
	replacement.UpdatedAt = now
	replacementEndpoint.MetadataRevision++
	replacementEndpoint.UpdatedAt = now

	next := repository.snapshot
	next.Instances = cloneInstanceMap(repository.snapshot.Instances)
	next.Instances[replacement.ResourceID] = cloneInstance(replacement)
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	next.Endpoints[existing.ClusterID][selected.ResourceID] = replacementEndpoint
	next.TopologySnapshots = cloneTopologySnapshotMap(repository.snapshot.TopologySnapshots)
	delete(next.TopologySnapshots, existing.ClusterID)
	next.InventoryGenerations = cloneUint64Map(repository.snapshot.InventoryGenerations)
	next.InventoryGenerations[existing.ClusterID]++
	next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	updatedCluster := next.Clusters[existing.ClusterID]
	updatedCluster.Health = model.Health{State: model.HealthUnknown}
	updatedCluster.MetadataRevision++
	updatedCluster.UpdatedAt = now
	next.Clusters[existing.ClusterID] = updatedCluster
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneInstance(replacement), replacementEndpoint, err
		}
		return model.DatabaseInstance{}, model.Endpoint{}, err
	}
	repository.snapshot = next
	return cloneInstance(replacement), replacementEndpoint, nil
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

func healthPriority(state model.HealthState) int {
	switch state {
	case model.HealthUnhealthy:
		return 4
	case model.HealthUnknown:
		return 3
	case model.HealthDegraded:
		return 2
	case model.HealthHealthy:
		return 1
	default:
		return 0
	}
}

func hasCurrentDiscoveryEvidence(probes []model.ProbeStatus, instanceID model.ResourceID) bool {
	for _, probe := range probes {
		if probe.InstanceID == instanceID && !probe.DiscoveryObservedAt.IsZero() {
			return true
		}
	}
	return false
}

func strictTopologyHealth(observedAt time.Time, fallback model.Health, probes []model.ProbeStatus, instances []model.DatabaseInstance, links []model.ReplicationLink) model.Health {
	healthy := len(probes) > 0 && len(instances) > 0
	for _, probe := range probes {
		if probe.Health.State != model.HealthHealthy || probe.DiscoveryObservedAt.IsZero() {
			healthy = false
		}
	}
	currentPrimaries := 0
	healthyIncomingLinks := make(map[model.ResourceID]bool)
	for _, link := range links {
		if !link.Healthy {
			healthy = false
		} else {
			healthyIncomingLinks[link.TargetInstanceID] = true
		}
	}
	for _, instance := range instances {
		if instance.Health.State != model.HealthHealthy {
			healthy = false
		}
		switch instance.Role {
		case model.RolePrimary:
			if hasCurrentDiscoveryEvidence(probes, instance.ResourceID) {
				currentPrimaries++
			}
		case model.RoleReplica:
			if instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning || !healthyIncomingLinks[instance.ResourceID] {
				healthy = false
			}
		default:
			healthy = false
		}
	}
	if currentPrimaries != 1 {
		healthy = false
	}
	if healthy {
		return model.Health{State: model.HealthHealthy, Summary: "latest topology observation is complete and healthy", ObservedAt: observedAt}
	}
	summary := "latest topology observation is incomplete or unhealthy"
	if fallback.State != model.HealthHealthy && strings.TrimSpace(fallback.Summary) != "" {
		summary = fallback.Summary
	}
	return model.Health{State: model.HealthDegraded, Summary: summary, ObservedAt: observedAt}
}

func (repository *Repository) ApplyDiscoveryRefresh(refresh DiscoveryRefresh) (model.TopologySnapshot, error) {
	if refresh.ClusterID == "" {
		return model.TopologySnapshot{}, fmt.Errorf("cluster ID is required")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	cluster, exists := repository.snapshot.Clusters[refresh.ClusterID]
	if !exists {
		return model.TopologySnapshot{}, fmt.Errorf("unknown cluster ID: %s", refresh.ClusterID)
	}

	observedAt := refresh.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = repository.now().UTC()
	}
	if watermark, found := repository.snapshot.ObservationWatermarks[refresh.ClusterID]; found && !observedAt.After(watermark) {
		return model.TopologySnapshot{}, fmt.Errorf("%w: observed at %s is not after %s", ErrStaleObservation, observedAt.Format(time.RFC3339Nano), watermark.Format(time.RFC3339Nano))
	}
	currentGeneration := repository.snapshot.InventoryGenerations[refresh.ClusterID]
	if refresh.InventoryGeneration == 0 || refresh.InventoryGeneration != currentGeneration {
		return model.TopologySnapshot{}, fmt.Errorf("%w: captured generation %d, current generation %d", ErrInventoryChanged, refresh.InventoryGeneration, currentGeneration)
	}
	next := cloneDiscoverySnapshot(repository.snapshot)
	activeEndpoints := make(map[model.ResourceID]model.Endpoint)
	for endpointID, endpoint := range next.Endpoints[refresh.ClusterID] {
		if endpoint.Active && endpoint.Kind == model.EndpointDatabase {
			activeEndpoints[endpointID] = endpoint
		}
	}

	observations := append([]DiscoveryObservation{}, refresh.Observations...)
	sort.SliceStable(observations, func(i int, j int) bool { return observations[i].EndpointID < observations[j].EndpointID })
	observedByEndpoint := make(map[model.ResourceID]model.ResourceID, len(observations))
	observedInstances := make(map[model.ResourceID]model.DatabaseInstance)
	discoveryEvidence := make(map[model.ResourceID]time.Time, len(observations))
	metricsEvidence := make(map[model.ResourceID]time.Time, len(observations))
	metricCandidates := make(map[model.ResourceID][]discoveryMetricCandidate)
	for _, observation := range observations {
		endpoint, endpointExists := activeEndpoints[observation.EndpointID]
		if !endpointExists {
			return model.TopologySnapshot{}, fmt.Errorf("unknown active database endpoint ID: %s", observation.EndpointID)
		}
		if _, duplicate := observedByEndpoint[observation.EndpointID]; duplicate {
			return model.TopologySnapshot{}, fmt.Errorf("duplicate observation endpoint ID: %s", observation.EndpointID)
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
		result, err := reconcileInstanceCandidate(&next, discovered, observedAt)
		if err != nil {
			return model.TopologySnapshot{}, fmt.Errorf("reconcile endpoint %s: %w", observation.EndpointID, err)
		}
		if _, err := bindDiscoveryEndpoint(&next, refresh.ClusterID, observation.EndpointID, result.Instance.ResourceID, observedAt); err != nil {
			return model.TopologySnapshot{}, err
		}
		activeEndpoints[observation.EndpointID] = next.Endpoints[refresh.ClusterID][observation.EndpointID]
		observedByEndpoint[observation.EndpointID] = result.Instance.ResourceID
		observedInstances[result.Instance.ResourceID] = result.Instance
		discoveryEvidence[observation.EndpointID] = observedAt
		for _, sample := range observation.Metrics {
			sample.InstanceID = result.Instance.ResourceID
			metricCandidates[result.Instance.ResourceID] = append(metricCandidates[result.Instance.ResourceID], discoveryMetricCandidate{
				endpointID: observation.EndpointID, sample: sample, complete: completeDiscoveryMetricSample(sample),
			})
		}
	}
	metricSamples := make([]model.MetricSample, 0, len(metricCandidates))
	for _, candidates := range metricCandidates {
		selected := candidates[0]
		for _, candidate := range candidates[1:] {
			if betterDiscoveryMetricCandidate(candidate, selected) {
				selected = candidate
			}
		}
		metricSamples = append(metricSamples, selected.sample)
		metricsEvidence[selected.endpointID] = selected.sample.ObservedAt
	}

	providedProbes := make(map[model.ResourceID]model.ProbeStatus, len(refresh.Probes))
	for _, probe := range refresh.Probes {
		if _, exists := activeEndpoints[probe.EndpointID]; !exists {
			return model.TopologySnapshot{}, fmt.Errorf("probe endpoint is not active inventory: %s", probe.EndpointID)
		}
		if _, duplicate := providedProbes[probe.EndpointID]; duplicate {
			return model.TopologySnapshot{}, fmt.Errorf("duplicate probe endpoint ID: %s", probe.EndpointID)
		}
		providedProbes[probe.EndpointID] = probe
	}
	if len(refresh.Probes) > 0 && len(providedProbes) != len(activeEndpoints) {
		return model.TopologySnapshot{}, fmt.Errorf("probe coverage does not match active database inventory")
	}

	probes := make([]model.ProbeStatus, 0, len(activeEndpoints))
	currentHealth := make(map[model.ResourceID]model.Health)
	probeHealthy := make(map[model.ResourceID]bool)
	probeSeen := make(map[model.ResourceID]bool)
	for endpointID, endpoint := range activeEndpoints {
		probe, provided := providedProbes[endpointID]
		if !provided {
			probe = model.ProbeStatus{EndpointID: endpointID, Health: model.Health{State: model.HealthUnknown, ObservedAt: observedAt}}
			if instanceID, observed := observedByEndpoint[endpointID]; observed {
				probe.InstanceID = instanceID
				probe.Health = observedInstances[instanceID].Health
			}
		}
		probe.EndpointID = endpointID
		probe.InstanceID = endpoint.InstanceID
		probe.DiscoveryObservedAt = discoveryEvidence[endpointID]
		probe.MetricsObservedAt = metricsEvidence[endpointID]
		if probe.Health.ObservedAt.IsZero() {
			probe.Health.ObservedAt = observedAt
		}
		probes = append(probes, probe)
		if probe.InstanceID == "" {
			continue
		}
		probeSeen[probe.InstanceID] = true
		if !probeHealthy[probe.InstanceID] && healthPriority(probe.Health.State) == healthPriority(model.HealthHealthy) {
			probeHealthy[probe.InstanceID] = true
		}
		if existing, ok := currentHealth[probe.InstanceID]; !ok || healthPriority(probe.Health.State) > healthPriority(existing.State) {
			currentHealth[probe.InstanceID] = probe.Health
		}
	}
	for instanceID := range probeSeen {
		for _, probe := range probes {
			if probe.InstanceID == instanceID && probe.Health.State != model.HealthHealthy {
				probeHealthy[instanceID] = false
			}
		}
	}

	activeInstanceIDs := make(map[model.ResourceID]struct{})
	instances := make([]model.DatabaseInstance, 0)
	instanceIDsByIdentity := make(map[string]model.ResourceID)
	for _, endpoint := range activeEndpoints {
		if endpoint.InstanceID == "" {
			continue
		}
		if _, duplicate := activeInstanceIDs[endpoint.InstanceID]; duplicate {
			continue
		}
		instance, exists := next.Instances[endpoint.InstanceID]
		if !exists || instance.ClusterID != refresh.ClusterID {
			return model.TopologySnapshot{}, fmt.Errorf("bound endpoint references unknown cluster instance")
		}
		if health, ok := currentHealth[instance.ResourceID]; ok {
			instance.Health = health
			next.Instances[instance.ResourceID] = cloneInstance(instance)
		}
		activeInstanceIDs[instance.ResourceID] = struct{}{}
		instances = append(instances, cloneInstance(instance))
		if key, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity); err == nil {
			instanceIDsByIdentity[key] = instance.ResourceID
		}
	}

	successfulTargets := make(map[model.ResourceID]struct{}, len(observedInstances))
	linksByEdge := make(map[replicationEdge]model.ReplicationLink)
	for instanceID := range observedInstances {
		successfulTargets[instanceID] = struct{}{}
	}
	for _, existing := range next.ReplicationLinks[refresh.ClusterID] {
		if _, sourceActive := activeInstanceIDs[existing.SourceInstanceID]; !sourceActive {
			continue
		}
		if _, targetActive := activeInstanceIDs[existing.TargetInstanceID]; !targetActive {
			continue
		}
		if _, targetObserved := successfulTargets[existing.TargetInstanceID]; targetObserved {
			continue
		}
		existing.Healthy = false
		linksByEdge[replicationEdge{existing.SourceInstanceID, existing.TargetInstanceID}] = existing
	}
	if refresh.TopologyAuthoritative {
		for _, nativeLink := range refresh.NativeLinks {
			sourceKey, sourceErr := identity.InstanceKey(cluster.Engine, nativeLink.SourceIdentity)
			targetKey, targetErr := identity.InstanceKey(cluster.Engine, nativeLink.TargetIdentity)
			if sourceErr != nil || targetErr != nil {
				continue
			}
			sourceInstanceID, sourceExists := instanceIDsByIdentity[sourceKey]
			targetInstanceID, targetExists := instanceIDsByIdentity[targetKey]
			if !sourceExists || !targetExists || sourceInstanceID == targetInstanceID {
				continue
			}
			if _, targetObserved := successfulTargets[targetInstanceID]; !targetObserved {
				continue
			}
			link := model.ReplicationLink{
				ClusterID: refresh.ClusterID, SourceInstanceID: sourceInstanceID, TargetInstanceID: targetInstanceID,
				Healthy:    nativeLink.Healthy && probeHealthy[sourceInstanceID] && probeHealthy[targetInstanceID],
				LagSeconds: nativeLink.LagSeconds,
			}
			linksByEdge[replicationEdge{sourceInstanceID, targetInstanceID}] = link
		}
	} else {
		for _, instance := range observedInstances {
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
			link := model.ReplicationLink{
				ClusterID: refresh.ClusterID, SourceInstanceID: sourceInstanceID, TargetInstanceID: instance.ResourceID,
				Healthy: probeHealthy[sourceInstanceID] && probeHealthy[instance.ResourceID] &&
					instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning,
				LagSeconds: instance.Replication.LagSeconds,
			}
			linksByEdge[replicationEdge{sourceInstanceID, instance.ResourceID}] = link
		}
	}
	links := make([]model.ReplicationLink, 0, len(linksByEdge))
	for _, link := range linksByEdge {
		links = append(links, link)
	}
	if err := replaceDiscoveryLinks(&next, refresh.ClusterID, links, observedAt); err != nil {
		return model.TopologySnapshot{}, err
	}
	appendDiscoveryMetricSamples(&next, refresh.ClusterID, metricSamples, discoveryMetricSampleLimit)
	if err := replaceDiscoveryAnomalies(&next, refresh.ClusterID, refresh.Anomalies, observedAt); err != nil {
		return model.TopologySnapshot{}, err
	}

	persistedAnomalies := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range next.Anomalies {
		if anomaly.ClusterID == refresh.ClusterID {
			persistedAnomalies = append(persistedAnomalies, anomaly)
		}
	}
	persistedLinks := make([]model.ReplicationLink, len(next.ReplicationLinks[refresh.ClusterID]))
	for index, link := range next.ReplicationLinks[refresh.ClusterID] {
		persistedLinks[index] = cloneReplicationLink(link)
	}
	refresh.Health = strictTopologyHealth(observedAt, refresh.Health, probes, instances, persistedLinks)
	cluster.Health = refresh.Health
	cluster.UpdatedAt = observedAt
	cluster.MetadataRevision++
	next.Clusters[refresh.ClusterID] = cloneCluster(cluster)
	sort.Slice(instances, func(i, j int) bool { return instances[i].ResourceID < instances[j].ResourceID })
	sort.Slice(persistedLinks, func(i, j int) bool { return persistedLinks[i].ResourceID < persistedLinks[j].ResourceID })
	sort.Slice(probes, func(i, j int) bool { return probes[i].EndpointID < probes[j].EndpointID })
	sort.Slice(persistedAnomalies, func(i, j int) bool { return persistedAnomalies[i].ResourceID < persistedAnomalies[j].ResourceID })
	published := model.TopologySnapshot{
		ClusterID: refresh.ClusterID, Instances: instances, Links: persistedLinks, Probes: probes,
		Health: refresh.Health, Anomalies: persistedAnomalies, ObservedAt: observedAt,
	}
	next.TopologySnapshots[refresh.ClusterID] = cloneTopologySnapshot(published)
	next.ObservationWatermarks[refresh.ClusterID] = observedAt
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneTopologySnapshot(published), err
		}
		return model.TopologySnapshot{}, err
	}
	repository.snapshot = next
	return cloneTopologySnapshot(published), nil
}

func completeDiscoveryMetricSample(sample model.MetricSample) bool {
	for _, name := range []string{"questions_total", "transactions_total", "slow_queries_total", "connections", "running_threads", "buffer_pool_hit_ratio"} {
		value, found := sample.Values[name]
		if !found || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func betterDiscoveryMetricCandidate(candidate discoveryMetricCandidate, current discoveryMetricCandidate) bool {
	if candidate.complete != current.complete {
		return candidate.complete
	}
	if !candidate.sample.ObservedAt.Equal(current.sample.ObservedAt) {
		return candidate.sample.ObservedAt.After(current.sample.ObservedAt)
	}
	return candidate.endpointID < current.endpointID
}

func (repository *Repository) TopologySnapshot(clusterID model.ResourceID) (model.TopologySnapshot, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	value, exists := repository.snapshot.TopologySnapshots[clusterID]
	if !exists {
		return model.TopologySnapshot{}, false
	}
	result := cloneTopologySnapshot(value)
	for index, observed := range result.Instances {
		canonical, found := repository.snapshot.Instances[observed.ResourceID]
		if !found {
			continue
		}
		overlaid := cloneInstance(observed)
		overlaid.ResourceMeta = canonical.ResourceMeta
		overlaid.ClusterID = canonical.ClusterID
		overlaid.NodeID = canonical.NodeID
		overlaid.DisplayName = canonical.DisplayName
		overlaid.Hostname = canonical.Hostname
		overlaid.IPAddress = canonical.IPAddress
		overlaid.Port = canonical.Port
		overlaid.Aliases = append([]string{}, canonical.Aliases...)
		overlaid.Maintenance = canonical.Maintenance
		result.Instances[index] = overlaid
	}
	return result, true
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

func (repository *Repository) Instance(instanceID model.ResourceID) (model.DatabaseInstance, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	instance, found := repository.snapshot.Instances[instanceID]
	return cloneInstance(instance), found
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
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
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
	if err := repository.commitSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) RecordAudit(event model.AuditEvent) error {
	now := repository.now().UTC()
	if event.ResourceID == "" {
		event.ResourceID = model.NewResourceID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	event.MetadataRevision = 1
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.Audits = append(append([]model.AuditEvent{}, repository.snapshot.Audits...), event)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) RecordReport(report model.Report) error {
	if !terminalReportStatus(report.Status) {
		return validationError("report status must be terminal")
	}
	now := repository.now().UTC()
	if report.ResourceID == "" {
		report.ResourceID = model.NewResourceID()
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.Reports = append([]model.Report{}, repository.snapshot.Reports...)
	updated := false
	for index, existing := range next.Reports {
		if existing.ResourceID != report.ResourceID {
			continue
		}
		report.CreatedAt = existing.CreatedAt
		report.UpdatedAt = now
		report.MetadataRevision = existing.MetadataRevision + 1
		next.Reports[index] = report
		updated = true
		break
	}
	if !updated {
		if report.CreatedAt.IsZero() {
			report.CreatedAt = now
		}
		report.UpdatedAt = now
		report.MetadataRevision = 1
		next.Reports = append(next.Reports, report)
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
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

func (repository *Repository) Report(resourceID model.ResourceID) (model.Report, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, report := range repository.snapshot.Reports {
		if report.ResourceID == resourceID {
			return report, true
		}
	}
	return model.Report{}, false
}
