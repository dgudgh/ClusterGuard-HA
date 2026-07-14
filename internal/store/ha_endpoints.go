package store

import (
	"net"
	"sort"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type HAEndpointSpec struct {
	ClusterID model.ResourceID
	Kind      model.EndpointKind
	IPAddress string
	Interface string
	Prefix    int
	OwnerID   model.ResourceID
	Active    bool
}

func validateHAEndpointSpec(current snapshot, spec HAEndpointSpec) error {
	if !model.ValidResourceID(spec.ClusterID) {
		return validationError("valid HA endpoint cluster ID is required")
	}
	if _, found := current.Clusters[spec.ClusterID]; !found {
		return validationError("HA endpoint cluster is unknown")
	}
	if spec.Kind != model.EndpointVIP {
		return validationError("only VIP HA endpoints are supported")
	}
	ip := net.ParseIP(strings.TrimSpace(spec.IPAddress))
	if ip == nil || ip.To4() == nil {
		return validationError("HA endpoint requires a valid IPv4 address")
	}
	if strings.TrimSpace(spec.Interface) == "" {
		return validationError("HA endpoint interface is required")
	}
	if spec.Prefix < 1 || spec.Prefix > 32 {
		return validationError("HA endpoint prefix must be between 1 and 32")
	}
	owner, found := current.Instances[spec.OwnerID]
	if !found || owner.ClusterID != spec.ClusterID {
		return validationError("HA endpoint owner is not in the selected cluster inventory")
	}
	return nil
}

func (repository *Repository) PutHAEndpoint(spec HAEndpointSpec) (model.HAEndpoint, model.Endpoint, error) {
	spec.IPAddress = strings.TrimSpace(spec.IPAddress)
	spec.Interface = strings.TrimSpace(spec.Interface)
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := validateHAEndpointSpec(repository.snapshot, spec); err != nil {
		return model.HAEndpoint{}, model.Endpoint{}, err
	}

	var resource model.HAEndpoint
	var endpoint model.Endpoint
	for _, candidate := range repository.snapshot.HAEndpoints {
		if candidate.ClusterID == spec.ClusterID && candidate.Kind == spec.Kind {
			resource = candidate
			endpoint = repository.snapshot.Endpoints[spec.ClusterID][candidate.EndpointID]
			break
		}
	}
	if spec.Active {
		for _, candidate := range repository.snapshot.HAEndpoints {
			if candidate.ResourceID == resource.ResourceID || candidate.Kind != model.EndpointVIP {
				continue
			}
			candidateEndpoint := repository.snapshot.Endpoints[candidate.ClusterID][candidate.EndpointID]
			if candidateEndpoint.Active && candidateEndpoint.IPAddress == spec.IPAddress {
				return model.HAEndpoint{}, model.Endpoint{}, conflictError("active VIP address is already registered to another cluster")
			}
		}
	}

	now := repository.now().UTC()
	if resource.ResourceID == "" {
		resource.ResourceMeta = model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}
		endpoint.ResourceMeta = model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}
	} else {
		resource.MetadataRevision++
		resource.UpdatedAt = now
		endpoint.MetadataRevision++
		endpoint.UpdatedAt = now
	}
	endpoint.ClusterID = spec.ClusterID
	endpoint.InstanceID = spec.OwnerID
	endpoint.Kind = spec.Kind
	endpoint.Hostname = ""
	endpoint.IPAddress = spec.IPAddress
	endpoint.Port = 0
	endpoint.Active = spec.Active
	resource.ClusterID = spec.ClusterID
	resource.EndpointID = endpoint.ResourceID
	resource.Kind = spec.Kind
	resource.DesiredRole = model.RolePrimary
	resource.OwnerID = spec.OwnerID
	resource.Interface = spec.Interface
	resource.Prefix = spec.Prefix
	resource.Healthy = false

	next := repository.snapshot
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	if next.Endpoints[spec.ClusterID] == nil {
		next.Endpoints[spec.ClusterID] = map[model.ResourceID]model.Endpoint{}
	}
	next.Endpoints[spec.ClusterID][endpoint.ResourceID] = endpoint
	next.HAEndpoints = cloneHAEndpointMap(repository.snapshot.HAEndpoints)
	next.HAEndpoints[resource.ResourceID] = resource
	if err := repository.commitSnapshotLocked(next); err != nil {
		return resource, endpoint, err
	}
	repository.snapshot = next
	return resource, endpoint, nil
}

// CommitHAEndpointOwner records a physically verified VIP owner without
// changing the stable HA endpoint or endpoint UUIDs.
func (repository *Repository) CommitHAEndpointOwner(clusterID, resourceID, ownerID model.ResourceID, healthy bool) error {
	if !model.ValidResourceID(clusterID) || !model.ValidResourceID(resourceID) || !model.ValidResourceID(ownerID) {
		return validationError("HA endpoint ownership scope is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	resource, found := repository.snapshot.HAEndpoints[resourceID]
	if !found || resource.ClusterID != clusterID || resource.Kind != model.EndpointVIP {
		return validationError("HA endpoint resource is outside the selected cluster")
	}
	owner, found := repository.snapshot.Instances[ownerID]
	if !found || owner.ClusterID != clusterID {
		return validationError("HA endpoint owner is outside the selected cluster")
	}
	endpoint, found := repository.snapshot.Endpoints[clusterID][resource.EndpointID]
	if !found || endpoint.Kind != model.EndpointVIP || !endpoint.Active {
		return validationError("active VIP endpoint metadata is unavailable")
	}
	if resource.OwnerID == ownerID && resource.Healthy == healthy && endpoint.InstanceID == ownerID {
		return nil
	}
	now := repository.now().UTC()
	resource.OwnerID = ownerID
	resource.Healthy = healthy
	resource.MetadataRevision++
	resource.UpdatedAt = now
	endpoint.InstanceID = ownerID
	endpoint.MetadataRevision++
	endpoint.UpdatedAt = now

	next := repository.snapshot
	next.HAEndpoints = cloneHAEndpointMap(repository.snapshot.HAEndpoints)
	next.HAEndpoints[resourceID] = resource
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	next.Endpoints[clusterID][endpoint.ResourceID] = endpoint
	if err := repository.commitSnapshotLocked(next); err != nil {
		return err
	}
	repository.snapshot = next
	return nil
}

func (repository *Repository) HAEndpoint(resourceID model.ResourceID) (model.HAEndpoint, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	resource, found := repository.snapshot.HAEndpoints[resourceID]
	return resource, found
}

func (repository *Repository) HAEndpoints(clusterID model.ResourceID) []model.HAEndpoint {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.HAEndpoint, 0)
	for _, resource := range repository.snapshot.HAEndpoints {
		if resource.ClusterID == clusterID {
			result = append(result, resource)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ResourceID < result[j].ResourceID })
	return result
}

func (repository *Repository) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, endpoints := range repository.snapshot.Endpoints {
		if endpoint, found := endpoints[resourceID]; found {
			return endpoint, true
		}
	}
	return model.Endpoint{}, false
}
