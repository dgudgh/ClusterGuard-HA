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

type snapshot struct {
	Clusters  map[model.ResourceID]model.DatabaseCluster  `json:"clusters"`
	Instances map[model.ResourceID]model.DatabaseInstance `json:"instances"`
	Anomalies map[model.ResourceID]model.MetadataAnomaly  `json:"anomalies"`
	Audits    []model.AuditEvent                          `json:"audits"`
	Reports   []model.Report                              `json:"reports"`
}

type Repository struct {
	mu       sync.RWMutex
	path     string
	snapshot snapshot
	now      func() time.Time
}

func emptySnapshot() snapshot {
	return snapshot{
		Clusters:  map[model.ResourceID]model.DatabaseCluster{},
		Instances: map[model.ResourceID]model.DatabaseInstance{},
		Anomalies: map[model.ResourceID]model.MetadataAnomaly{},
		Audits:    []model.AuditEvent{},
		Reports:   []model.Report{},
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
	return copy
}

func cloneCluster(cluster model.DatabaseCluster) model.DatabaseCluster {
	copy := cluster
	copy.EngineIdentity = cluster.EngineIdentity.Clone()
	return copy
}

func (repository *Repository) persistLocked() error {
	if repository.path == "" {
		return nil
	}
	contents, err := json.MarshalIndent(repository.snapshot, "", "  ")
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

func (repository *Repository) ReconcileInstance(discovered model.DatabaseInstance) (ReconcileResult, error) {
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

	repository.mu.Lock()
	defer repository.mu.Unlock()
	var existingID model.ResourceID
	for resourceID, existing := range repository.snapshot.Instances {
		existingKey, keyErr := identity.InstanceKey(existing.Engine, existing.EngineIdentity)
		if keyErr == nil && existingKey == key {
			existingID = resourceID
			if existing.ClusterID != discovered.ClusterID {
				return ReconcileResult{}, fmt.Errorf("engine identity %s already belongs to a different cluster", key)
			}
			break
		}
	}
	for resourceID, existing := range repository.snapshot.Instances {
		if resourceID != existingID && existing.ClusterID == discovered.ClusterID && endpointCollision(existing, discovered) {
			return ReconcileResult{}, fmt.Errorf("endpoint is already owned by resource %s", resourceID)
		}
	}

	now := repository.now().UTC()
	result := ReconcileResult{}
	if existingID == "" {
		discovered.ResourceID = model.NewResourceID()
		discovered.MetadataRevision = 1
		discovered.CreatedAt = now
		discovered.UpdatedAt = now
		if discovered.DisplayName == "" {
			discovered.DisplayName = discovered.Hostname
		}
		repository.snapshot.Instances[discovered.ResourceID] = cloneInstance(discovered)
		result = ReconcileResult{Instance: cloneInstance(discovered), Created: true}
	} else {
		existing := repository.snapshot.Instances[existingID]
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
		existing.MetadataRevision++
		existing.UpdatedAt = now
		if existing.DisplayName == "" {
			existing.DisplayName = existing.Hostname
		}
		repository.snapshot.Instances[existingID] = cloneInstance(existing)
		result = ReconcileResult{Instance: cloneInstance(existing), Updated: true}
	}
	if err := repository.persistLocked(); err != nil {
		return ReconcileResult{}, err
	}
	return result, nil
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
