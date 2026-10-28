package store

import (
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

// RegisterDatabaseEndpoints adds inventory only. It never installs or mutates a
// database. Registration, observation invalidation and audit commit atomically.
func (repository *Repository) RegisterDatabaseEndpoints(clusterID model.ResourceID, endpoints []model.Endpoint, actor string) ([]model.Endpoint, error) {
	if len(endpoints) == 0 || len(endpoints) > 32 {
		return nil, validationError("between one and 32 database endpoints are required")
	}
	endpoints = append([]model.Endpoint(nil), endpoints...)
	for i := range endpoints {
		endpoints[i] = normalizeEndpoint(model.Endpoint{ClusterID: clusterID, Kind: model.EndpointDatabase, Hostname: endpoints[i].Hostname, IPAddress: endpoints[i].IPAddress, Port: endpoints[i].Port, Active: true})
		if err := validateEndpoint(endpoints[i]); err != nil {
			return nil, err
		}
	}
	if endpointSetCollision(endpoints) {
		return nil, validationError("duplicate endpoint in registration")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, ok := repository.snapshot.Clusters[clusterID]; !ok {
		return nil, ErrNotFound
	}
	now := repository.now().UTC()
	result := make([]model.Endpoint, 0, len(endpoints))
	added := make([]model.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		var matched *model.Endpoint
		for owner, entries := range repository.snapshot.Endpoints {
			for _, existing := range entries {
				if !existing.Active || existing.Kind != model.EndpointDatabase || !endpointAddressCollision(endpoint, existing) {
					continue
				}
				if owner != clusterID {
					return nil, conflictError("database endpoint belongs to another cluster")
				}
				// A shared alias alone must not silently bind a different address.
				if !strings.EqualFold(endpoint.Hostname, existing.Hostname) || !strings.EqualFold(endpoint.IPAddress, existing.IPAddress) || endpoint.Port != existing.Port {
					return nil, conflictError("database endpoint overlaps existing inventory; use metadata reconciliation")
				}
				copy := existing
				matched = &copy
			}
		}
		if matched != nil {
			result = append(result, *matched)
			continue
		}
		endpoint.ResourceID = model.NewResourceID()
		endpoint.MetadataRevision = 1
		endpoint.CreatedAt, endpoint.UpdatedAt = now, now
		added = append(added, endpoint)
		result = append(result, endpoint)
	}
	if len(added) == 0 {
		return result, nil
	}
	next := repository.snapshot
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	if next.Endpoints[clusterID] == nil {
		next.Endpoints[clusterID] = make(map[model.ResourceID]model.Endpoint)
	}
	for _, endpoint := range added {
		next.Endpoints[clusterID][endpoint.ResourceID] = endpoint
	}
	next.TopologySnapshots = cloneTopologySnapshotMap(repository.snapshot.TopologySnapshots)
	delete(next.TopologySnapshots, clusterID)
	next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	cluster := next.Clusters[clusterID]
	cluster.Health = model.Health{State: model.HealthUnknown}
	cluster.MetadataRevision++
	cluster.UpdatedAt = now
	next.Clusters[clusterID] = cluster
	next.InventoryGenerations = cloneUint64Map(repository.snapshot.InventoryGenerations)
	next.InventoryGenerations[clusterID]++
	next.Audits = append(append([]model.AuditEvent{}, repository.snapshot.Audits...), model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), CreatedAt: now, UpdatedAt: now},
		OperationID:  model.NewResourceID(), Stage: model.StageAudit, Actor: actor,
		Message: fmt.Sprintf("registered %d existing database endpoints in cluster %s", len(added), clusterID),
	})
	if err := repository.commitSnapshotLocked(next); err != nil {
		return nil, err
	}
	return result, nil
}
