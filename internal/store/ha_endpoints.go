package store

import (
	"net"
	"sort"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type HAEndpointSpec struct {
	ClusterID   model.ResourceID
	Kind        model.EndpointKind
	Hostname    string
	IPAddress   string
	Port        int
	Interface   string
	Prefix      int
	Provider    model.EndpointProviderKind
	ProviderRef string
	OwnerID     model.ResourceID
	Active      bool
}

func validateHAEndpointSpec(current snapshot, spec HAEndpointSpec) error {
	if !model.ValidResourceID(spec.ClusterID) {
		return validationError("valid HA endpoint cluster ID is required")
	}
	if _, found := current.Clusters[spec.ClusterID]; !found {
		return validationError("HA endpoint cluster is unknown")
	}
	provider := spec.Provider
	if provider == "" {
		provider = model.EndpointProviderLinuxVIP
	}
	if !provider.Valid() {
		return validationError("HA endpoint provider is invalid")
	}
	if provider == model.EndpointProviderLinuxVIP {
		if spec.Kind != model.EndpointVIP {
			return validationError("Linux VIP provider requires a VIP endpoint")
		}
		ip := net.ParseIP(strings.TrimSpace(spec.IPAddress))
		if ip == nil || ip.To4() == nil {
			return validationError("Linux VIP endpoint requires a valid IPv4 address")
		}
		if strings.TrimSpace(spec.Interface) == "" {
			return validationError("Linux VIP endpoint interface is required")
		}
		if spec.Prefix < 1 || spec.Prefix > 32 {
			return validationError("Linux VIP endpoint prefix must be between 1 and 32")
		}
	}
	if provider == model.EndpointProviderKubernetesService {
		if spec.Kind != model.EndpointService {
			return validationError("Kubernetes Service provider requires a service endpoint")
		}
		if strings.TrimSpace(spec.ProviderRef) == "" || strings.TrimSpace(spec.Hostname) == "" || spec.Port < 1 || spec.Port > 65535 {
			return validationError("Kubernetes Service endpoint requires provider reference, hostname, and port")
		}
		if address := strings.TrimSpace(spec.IPAddress); address != "" && net.ParseIP(address) == nil {
			return validationError("Kubernetes Service endpoint IP address is invalid")
		}
		if strings.TrimSpace(spec.Interface) != "" || spec.Prefix != 0 {
			return validationError("Kubernetes Service endpoint cannot declare a Linux interface or prefix")
		}
	}
	owner, found := current.Instances[spec.OwnerID]
	if !found || owner.ClusterID != spec.ClusterID {
		return validationError("HA endpoint owner is not in the selected cluster inventory")
	}
	return nil
}

func (repository *Repository) PutHAEndpoint(spec HAEndpointSpec) (model.HAEndpoint, model.Endpoint, error) {
	spec.IPAddress = strings.TrimSpace(spec.IPAddress)
	spec.Hostname = strings.TrimSpace(spec.Hostname)
	spec.Interface = strings.TrimSpace(spec.Interface)
	spec.ProviderRef = strings.TrimSpace(spec.ProviderRef)
	if spec.Provider == "" {
		spec.Provider = model.EndpointProviderLinuxVIP
	}
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
	endpoint.Hostname = spec.Hostname
	endpoint.IPAddress = spec.IPAddress
	endpoint.Port = spec.Port
	endpoint.Active = spec.Active
	resource.ClusterID = spec.ClusterID
	resource.EndpointID = endpoint.ResourceID
	resource.Kind = spec.Kind
	resource.DesiredRole = model.RolePrimary
	resource.OwnerID = spec.OwnerID
	resource.Interface = spec.Interface
	resource.Prefix = spec.Prefix
	resource.Provider = spec.Provider
	resource.ProviderRef = spec.ProviderRef
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
	return resource, endpoint, nil
}

// CommitHAEndpointOwner records a physically verified VIP owner without
// changing the stable HA endpoint or endpoint UUIDs.
func (repository *Repository) CommitHAEndpointOwner(clusterID, resourceID, ownerID model.ResourceID, healthy bool) error {
	return repository.commitHAEndpointOwner(clusterID, resourceID, ownerID, healthy, nil, nil)
}

// CommitObservedHAEndpointOwner records a reconciler observation only when the
// HA endpoint metadata still has the revisions seen before the physical probe.
// This prevents a slow background probe from overwriting a newer switchover.
func (repository *Repository) CommitObservedHAEndpointOwner(clusterID, resourceID model.ResourceID, expectedResourceRevision, expectedEndpointRevision uint64, ownerID model.ResourceID, healthy bool) error {
	return repository.commitHAEndpointOwner(clusterID, resourceID, ownerID, healthy, &expectedResourceRevision, &expectedEndpointRevision)
}

func (repository *Repository) commitHAEndpointOwner(clusterID, resourceID, ownerID model.ResourceID, healthy bool, expectedResourceRevision, expectedEndpointRevision *uint64) error {
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
	if expectedResourceRevision != nil && resource.MetadataRevision != *expectedResourceRevision {
		return conflictError("HA endpoint metadata changed after ownership observation")
	}
	if expectedEndpointRevision != nil && endpoint.MetadataRevision != *expectedEndpointRevision {
		return conflictError("VIP endpoint metadata changed after ownership observation")
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
