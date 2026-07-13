package endpoint

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Inventory interface {
	HAEndpoints(model.ResourceID) []model.HAEndpoint
	Endpoint(model.ResourceID) (model.Endpoint, bool)
	CommitHAEndpointOwner(model.ResourceID, model.ResourceID, model.ResourceID, bool) error
}

type AgentTransport interface {
	Send(context.Context, model.DatabaseInstance, agent.Request) (agent.Response, error)
}

type LinuxVIPProvider struct {
	inventory Inventory
	transport AgentTransport
	leases    LeaseStore
	secret    string
	now       func() time.Time
}

func NewLinuxVIPProvider(inventory Inventory, transport AgentTransport, leases LeaseStore, secret string, now func() time.Time) *LinuxVIPProvider {
	if now == nil {
		now = time.Now
	}
	return &LinuxVIPProvider{inventory: inventory, transport: transport, leases: leases, secret: secret, now: now}
}

func (provider *LinuxVIPProvider) Executable(context.Context) bool {
	return provider != nil && provider.inventory != nil && provider.transport != nil && provider.leases != nil && strings.TrimSpace(provider.secret) != ""
}

type endpointResource struct {
	resource model.HAEndpoint
	endpoint model.Endpoint
}

type OwnershipObservation struct {
	HAEndpointID     model.ResourceID   `json:"ha_endpoint_id"`
	CanonicalOwnerID model.ResourceID   `json:"canonical_owner_id,omitempty"`
	EndpointOwnerID  model.ResourceID   `json:"endpoint_owner_id,omitempty"`
	OwnerIDs         []model.ResourceID `json:"owner_ids"`
	Complete         bool               `json:"complete"`
}

func (provider *LinuxVIPProvider) resource(clusterID model.ResourceID) (endpointResource, error) {
	resources := provider.inventory.HAEndpoints(clusterID)
	active := make([]model.HAEndpoint, 0, 1)
	for _, resource := range resources {
		endpoint, found := provider.inventory.Endpoint(resource.EndpointID)
		if found && resource.Kind == model.EndpointVIP && endpoint.Kind == model.EndpointVIP && endpoint.Active {
			active = append(active, resource)
		}
	}
	if len(active) != 1 {
		return endpointResource{}, fmt.Errorf("exactly one active VIP resource is required")
	}
	endpoint, found := provider.inventory.Endpoint(active[0].EndpointID)
	if !found || strings.TrimSpace(endpoint.IPAddress) == "" || strings.TrimSpace(active[0].Interface) == "" || active[0].Prefix < 1 || active[0].Prefix > 32 {
		return endpointResource{}, fmt.Errorf("active VIP resource is incomplete")
	}
	return endpointResource{resource: active[0], endpoint: endpoint}, nil
}

type ownerObservation struct {
	instance model.DatabaseInstance
	owns     bool
	err      error
}

func (provider *LinuxVIPProvider) signedRequest(resolved adapter.ResolvedOperation, resource endpointResource, command string, leaseID model.ResourceID) (agent.Request, error) {
	digest := strings.TrimSpace(resolved.PlanDigest)
	if digest == "" {
		digest = "sha256:precheck:" + string(resolved.OperationID)
	}
	request := agent.Request{
		Command: command, ClusterID: resolved.Cluster.ResourceID, OperationID: resolved.OperationID,
		LeaseID: leaseID, PlanDigest: digest, ExpiresAt: provider.now().UTC().Add(30 * time.Second),
		VIP: resource.endpoint.IPAddress, Interface: resource.resource.Interface, Prefix: resource.resource.Prefix,
	}
	signature, err := agent.SignRequest(request, provider.secret)
	if err != nil {
		return agent.Request{}, err
	}
	request.Signature = signature
	return request, nil
}

func (provider *LinuxVIPProvider) observe(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource) []ownerObservation {
	observations := make([]ownerObservation, len(resolved.Snapshot.Instances))
	var wait sync.WaitGroup
	for index, instance := range resolved.Snapshot.Instances {
		wait.Add(1)
		go func(index int, instance model.DatabaseInstance) {
			defer wait.Done()
			observations[index].instance = instance
			request, err := provider.signedRequest(resolved, resource, agent.CommandVIPStatus, "")
			if err != nil {
				observations[index].err = err
				return
			}
			response, err := provider.transport.Send(ctx, instance, request)
			if err != nil || response.Status != agent.StatusOK || response.OwnsVIP == nil {
				if err == nil {
					err = fmt.Errorf("agent returned incomplete VIP status")
				}
				observations[index].err = err
				return
			}
			observations[index].owns = *response.OwnsVIP
		}(index, instance)
	}
	wait.Wait()
	return observations
}

func observationSummary(observations []ownerObservation) ([]model.ResourceID, bool) {
	owners := make([]model.ResourceID, 0, 1)
	complete := len(observations) > 0
	for _, observation := range observations {
		if observation.err != nil {
			complete = false
		}
		if observation.owns {
			owners = append(owners, observation.instance.ResourceID)
		}
	}
	return owners, complete
}

func (provider *LinuxVIPProvider) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	if !provider.Executable(ctx) {
		return OwnershipObservation{}, fmt.Errorf("writer endpoint provider is not configured")
	}
	if cluster.ResourceID == "" || snapshot.ClusterID != cluster.ResourceID {
		return OwnershipObservation{}, fmt.Errorf("VIP observation cluster scope is invalid")
	}
	resource, err := provider.resource(cluster.ResourceID)
	if err != nil {
		return OwnershipObservation{}, err
	}
	resolved := adapter.ResolvedOperation{
		OperationID: resource.resource.ResourceID,
		Cluster:     cluster,
		Snapshot:    snapshot,
		PlanDigest:  "sha256:ownership:" + string(resource.resource.ResourceID),
	}
	owners, complete := observationSummary(provider.observe(ctx, resolved, resource))
	return OwnershipObservation{
		HAEndpointID: resource.resource.ResourceID, CanonicalOwnerID: resource.resource.OwnerID,
		EndpointOwnerID: resource.endpoint.InstanceID, OwnerIDs: owners, Complete: complete,
	}, nil
}

func (provider *LinuxVIPProvider) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	if !provider.Executable(ctx) {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "writer endpoint provider is not configured"}}
	}
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	owners, complete := observationSummary(provider.observe(ctx, resolved, resource))
	if !complete {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "VIP probe coverage is incomplete"}}
	}
	if len(owners) > 1 {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "multiple VIP owners were detected"}}
	}
	if len(owners) != 1 || owners[0] != resolved.Primary.ResourceID {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "VIP must be owned only by the current primary"}}
	}
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "VIP has one current-primary owner with complete probe coverage"}}
}

func (provider *LinuxVIPProvider) sendMutation(ctx context.Context, resolved adapter.ResolvedOperation, resource endpointResource, instance model.DatabaseInstance, command string, lease Lease) error {
	if err := provider.leases.Validate(ctx, lease); err != nil {
		return err
	}
	request, err := provider.signedRequest(resolved, resource, command, lease.ResourceID)
	if err != nil {
		return err
	}
	response, err := provider.transport.Send(ctx, instance, request)
	if err != nil {
		return err
	}
	if response.Status != agent.StatusOK {
		return fmt.Errorf("agent blocked VIP mutation")
	}
	return nil
}

func (provider *LinuxVIPProvider) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	lease, err := provider.leases.Acquire(ctx, LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.resource.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("acquire endpoint lease: %w", err)
	}
	observations := provider.observe(ctx, resolved, resource)
	owners, complete := observationSummary(observations)
	if !complete || len(owners) > 1 {
		return fmt.Errorf("VIP ownership is unsafe before transfer")
	}
	targetAlreadyOwns := len(owners) == 1 && owners[0] == resolved.Target.ResourceID
	if !targetAlreadyOwns {
		for _, observation := range observations {
			if observation.owns && observation.instance.ResourceID != resolved.Target.ResourceID {
				if err := provider.sendMutation(ctx, resolved, resource, observation.instance, agent.CommandVIPRelease, lease); err != nil {
					return fmt.Errorf("release source VIP: %w", err)
				}
			}
		}
		owners, complete = observationSummary(provider.observe(ctx, resolved, resource))
		if !complete || len(owners) != 0 {
			return fmt.Errorf("zero VIP ownership could not be proven")
		}
		if err := provider.sendMutation(ctx, resolved, resource, resolved.Target, agent.CommandVIPAcquire, lease); err != nil {
			return fmt.Errorf("acquire target VIP: %w", err)
		}
	}
	owners, complete = observationSummary(provider.observe(ctx, resolved, resource))
	if !complete || len(owners) != 1 || owners[0] != resolved.Target.ResourceID {
		return fmt.Errorf("target-only VIP ownership could not be verified")
	}
	if err := provider.inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, resource.resource.ResourceID, resolved.Target.ResourceID, true); err != nil {
		return fmt.Errorf("commit VIP ownership metadata: %w", err)
	}
	if check := provider.Verify(ctx, resolved); check.Status != model.CheckPass {
		return fmt.Errorf("physical and metadata VIP ownership did not converge")
	}
	return nil
}

func (provider *LinuxVIPProvider) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	resource, err := provider.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	owners, complete := observationSummary(provider.observe(ctx, resolved, resource))
	if !complete {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "VIP probe coverage is incomplete"}
	}
	if len(owners) != 1 || owners[0] != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "VIP is not owned only by the operation target"}
	}
	if resource.resource.OwnerID != resolved.Target.ResourceID || resource.endpoint.InstanceID != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "physical VIP ownership is correct but canonical ownership metadata is stale"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "VIP physical and metadata ownership match only the operation target"}
}
