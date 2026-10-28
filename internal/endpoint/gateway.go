package endpoint

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GatewayInventory interface {
	Inventory
	Endpoints(model.ResourceID) []model.Endpoint
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
	GatewayGrant(model.ResourceID) model.GatewayGrant
	ReserveGatewayGrant(Lease, model.ResourceID, string, bool) (model.GatewayGrant, error)
}
type GatewayProvider struct {
	inventory GatewayInventory
	leases    LeaseStore
	profiles  map[string]GatewayTransport
	mu        sync.Mutex
}

func NewGatewayProvider(inventory GatewayInventory, leases LeaseStore, profiles map[string]GatewayTransport) *GatewayProvider {
	copy := map[string]GatewayTransport{}
	for name, client := range profiles {
		if name != "" && client != nil {
			copy[name] = client
		}
	}
	return &GatewayProvider{inventory: inventory, leases: leases, profiles: copy}
}
func (p *GatewayProvider) Executable(context.Context) bool {
	return p != nil && p.inventory != nil && p.leases != nil && len(p.profiles) > 0
}
func (p *GatewayProvider) resource(cluster model.ResourceID) (endpointResource, GatewayTransport, error) {
	if !p.Executable(context.Background()) {
		return endpointResource{}, nil, fmt.Errorf("gateway provider is not configured")
	}
	var selected endpointResource
	count := 0
	for _, resource := range p.inventory.HAEndpoints(cluster) {
		entry, ok := p.inventory.Endpoint(resource.EndpointID)
		if ok && entry.Active && resource.Provider != model.EndpointProviderGateway {
			return endpointResource{}, nil, fmt.Errorf("gateway cannot coexist with another active writer endpoint provider")
		}
		if ok && entry.Active && resource.Provider == model.EndpointProviderGateway && resource.Kind == model.EndpointService && entry.Kind == model.EndpointService {
			selected = endpointResource{resource: resource, endpoint: entry}
			count++
		}
	}
	if count != 1 || p.profiles[selected.resource.ProviderRef] == nil {
		return selected, nil, fmt.Errorf("exactly one active gateway endpoint and trusted profile required")
	}
	return selected, p.profiles[selected.resource.ProviderRef], nil
}
func (p *GatewayProvider) current(ctx context.Context, r endpointResource, owner model.ResourceID) (Lease, error) {
	reader, ok := p.leases.(CurrentLeaseReader)
	if !ok {
		return Lease{}, fmt.Errorf("gateway requires current majority lease inspection")
	}
	lease, err := reader.Current(ctx, r.resource.ClusterID, r.resource.ResourceID)
	if err != nil {
		return lease, err
	}
	if lease.OwnerID != owner {
		return Lease{}, fmt.Errorf("majority lease owner differs from selected target")
	}
	if err = p.leases.Validate(ctx, lease); err != nil {
		return lease, err
	}
	return lease, nil
}
func (p *GatewayProvider) statuses(ctx context.Context, r endpointResource, client GatewayTransport) ([]GatewayStatus, error) {
	statuses, err := client.Statuses(ctx, r.resource.ClusterID)
	if err != nil {
		return nil, err
	}
	if len(statuses) < 2 {
		return nil, fmt.Errorf("two gateway observations required")
	}
	// Authenticate both resource ID and exact configured DB address before any
	// mutation. DNS aliases must be explicitly registered as database endpoints.
	seen := map[string]bool{}
	for _, status := range statuses {
		if status.InstanceID == "" || seen[status.InstanceID] {
			return nil, fmt.Errorf("distinct gateway process identities are required")
		}
		seen[status.InstanceID] = true
		if status.ClusterID != r.resource.ClusterID || len(status.Targets) == 0 {
			return nil, fmt.Errorf("gateway identity inventory is unavailable; compatible gateway required")
		}
		for id, address := range status.Targets {
			matched := false
			for _, database := range p.inventory.Endpoints(r.resource.ClusterID) {
				if !database.Active || database.Kind != model.EndpointDatabase || string(database.InstanceID) != id {
					continue
				}
				for _, host := range []string{database.Hostname, database.IPAddress} {
					if host != "" && net.JoinHostPort(host, strconv.Itoa(database.Port)) == address {
						matched = true
					}
				}
			}
			if !matched {
				return nil, fmt.Errorf("gateway backend %s does not match registered database address", id)
			}
		}
	}
	return statuses, nil
}
func (p *GatewayProvider) send(ctx context.Context, client GatewayTransport, lease Lease, grant model.GatewayGrant) error {
	return client.Apply(ctx, grant, func(check context.Context) error { return p.leases.Validate(check, lease) })
}
func (p *GatewayProvider) publish(ctx context.Context, r endpointResource, client GatewayTransport, lease Lease, operation model.ResourceID, activate bool) error {
	grant, err := p.inventory.ReserveGatewayGrant(lease, operation, "withdraw", true)
	if err != nil {
		return err
	}
	if err = p.send(ctx, client, lease, grant); err != nil {
		return fmt.Errorf("gateway withdrawal barrier incomplete: %w", err)
	}
	if !activate {
		return nil
	}
	return p.activate(ctx, client, lease, operation)
}
func (p *GatewayProvider) activate(ctx context.Context, client GatewayTransport, lease Lease, operation model.ResourceID) error {
	grant, err := p.inventory.ReserveGatewayGrant(lease, operation, "activate", false)
	if err != nil {
		return err
	}
	if err = p.send(ctx, client, lease, grant); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		withdraw, reserveErr := p.inventory.ReserveGatewayGrant(lease, operation, "withdraw", false)
		if reserveErr == nil {
			reserveErr = p.send(cleanup, client, lease, withdraw)
		}
		return fmt.Errorf("gateway activation unverified: %w; withdrawal=%v", err, reserveErr)
	}
	return nil
}
func (p *GatewayProvider) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	if resolved.Cluster.Engine != model.EngineMySQL {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "gateway execution currently requires MySQL"}}
	}
	r, client, err := p.resource(resolved.Cluster.ResourceID)
	if err == nil {
		var statuses []GatewayStatus
		statuses, err = p.statuses(ctx, r, client)
		if err == nil {
			for _, status := range statuses {
				if status.Targets[string(resolved.Primary.ResourceID)] == "" || status.Targets[string(resolved.Target.ResourceID)] == "" {
					err = fmt.Errorf("source and target must be configured on every gateway")
					break
				}
			}
		}
	}
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "all gateway identities and source/target bindings verified"}}
}
func (p *GatewayProvider) AuthorizeTransition(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, client, err := p.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	if checks := p.Precheck(ctx, resolved); len(checks) != 1 || checks[0].Status != model.CheckPass {
		return adapter.TransitionAuthorization{}, fmt.Errorf("gateway precheck failed")
	}
	authorization, err := authorizeTransitionLease(ctx, p.leases, resolved, r.resource.ResourceID, 30*time.Second, 5*time.Second)
	if err != nil {
		return authorization, err
	}
	lease, err := p.current(ctx, r, resolved.Target.ResourceID)
	if err == nil {
		err = p.publish(ctx, r, client, lease, resolved.OperationID, false)
	}
	if err != nil {
		rollback := authorization.Abort(context.WithoutCancel(ctx))
		authorization.Cancel()
		return adapter.TransitionAuthorization{}, errors.Join(err, rollback)
	}
	baseFinalize, baseAbort := authorization.Finalize, authorization.Abort
	authorization.Finalize = func(finalize context.Context) error {
		check := p.Verify(finalize, resolved)
		if check.Status != model.CheckPass {
			return fmt.Errorf("gateway final verification: %s", check.Message)
		}
		return baseFinalize(finalize)
	}
	authorization.Abort = func(abort context.Context) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		current, e := p.current(abort, r, resolved.Target.ResourceID)
		if e != nil {
			return e
		}
		grant, e := p.inventory.ReserveGatewayGrant(current, resolved.OperationID, "withdraw", false)
		if e == nil {
			e = p.send(abort, client, current, grant)
		}
		if e != nil {
			return e
		}
		return baseAbort(abort)
	}
	return authorization, nil
}
func (p *GatewayProvider) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, client, err := p.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	lease, err := p.current(ctx, r, resolved.Target.ResourceID)
	if err != nil {
		return err
	}
	if lease.OperationID != resolved.OperationID {
		return fmt.Errorf("gateway transition operation changed")
	}
	statuses, err := p.statuses(ctx, r, client)
	if err != nil {
		return err
	}
	last := p.inventory.GatewayGrant(resolved.Cluster.ResourceID)
	for _, status := range statuses {
		if status.LastGrant.Epoch != last.Epoch || status.LastGrant.LeaseID != lease.ResourceID || status.LastGrant.OperationID != resolved.OperationID || status.LastGrant.TargetID != resolved.Target.ResourceID || status.Active && !sameGatewayGrant(status.LastGrant, last) {
			return fmt.Errorf("gateway withdrawal barrier changed")
		}
	}
	if err = p.activate(ctx, client, lease, resolved.OperationID); err != nil {
		return err
	}
	return p.inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, r.resource.ResourceID, resolved.Target.ResourceID, true)
}
func (p *GatewayProvider) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	r, client, err := p.resource(resolved.Cluster.ResourceID)
	if err == nil {
		var lease Lease
		lease, err = p.current(ctx, r, resolved.Target.ResourceID)
		if err == nil {
			var statuses []GatewayStatus
			statuses, err = p.statuses(ctx, r, client)
			if err == nil {
				last := p.inventory.GatewayGrant(resolved.Cluster.ResourceID)
				for _, status := range statuses {
					if !status.Active || status.LastGrant.LeaseID != lease.ResourceID || status.LastGrant.TargetID != resolved.Target.ResourceID || !sameGatewayGrant(status.LastGrant, last) {
						err = fmt.Errorf("gateway route is not active only for authorized target")
					}
				}
			}
		}
	}
	if err == nil && (r.resource.OwnerID != resolved.Target.ResourceID || r.endpoint.InstanceID != resolved.Target.ResourceID) {
		err = fmt.Errorf("gateway canonical owner metadata is stale")
	}
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "all gateway routes and majority lease match target"}
}
func (p *GatewayProvider) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	r, client, err := p.resource(cluster.ResourceID)
	if err != nil {
		return OwnershipObservation{}, err
	}
	statuses, err := p.statuses(ctx, r, client)
	if err != nil {
		return OwnershipObservation{}, err
	}
	result := OwnershipObservation{HAEndpointID: r.resource.ResourceID, HAEndpointRevision: r.resource.MetadataRevision, EndpointRevision: r.endpoint.MetadataRevision, CanonicalOwnerID: r.resource.OwnerID, EndpointOwnerID: r.endpoint.InstanceID, Complete: true, OwnerIDs: []model.ResourceID{}}
	owners := map[model.ResourceID]bool{}
	for _, status := range statuses {
		if status.Active {
			if !model.ValidResourceID(status.LastGrant.TargetID) {
				return result, fmt.Errorf("invalid gateway owner")
			}
			owners[status.LastGrant.TargetID] = true
		}
	}
	for owner := range owners {
		result.OwnerIDs = append(result.OwnerIDs, owner)
	}
	for _, instance := range snapshot.Instances {
		result.ObservedInstanceIDs = append(result.ObservedInstanceIDs, instance.ResourceID)
	}
	return result, nil
}
func (p *GatewayProvider) FormerPrimaryPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	observation, err := p.ObserveOwnership(ctx, resolved.Cluster, resolved.Snapshot)
	if err == nil {
		for _, owner := range observation.OwnerIDs {
			if owner == resolved.Primary.ResourceID {
				err = fmt.Errorf("former primary still owns a gateway route")
			}
		}
	}
	if err != nil {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: err.Error()}}
	}
	return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckPass, Message: "former primary gateway routes withdrawn"}}
}
func (p *GatewayProvider) AuthorizeStableOwner(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	r, _, err := p.resource(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, err
	}
	return authorizeStableLease(ctx, p.leases, resolved, r.resource.ResourceID, 30*time.Second, 5*time.Second)
}

// RefreshStableRoute is called only AFTER OwnershipKeeper proves current SQL
// health/unique writable owner and renews its stable majority lease. It never
// acquires a lease itself and cannot authorize a pending transition.
func (p *GatewayProvider) RefreshStableRoute(ctx context.Context, request LeaseRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, client, err := p.resource(request.ClusterID)
	if err != nil {
		return err
	}
	lease, err := p.current(ctx, r, request.OwnerID)
	if err != nil {
		return err
	}
	if lease.OperationID != lease.HAEndpointID || lease.PreviousOwnerID != "" {
		return fmt.Errorf("gateway stable renewal blocked by transition")
	}
	snapshot, ok := p.inventory.TopologySnapshot(request.ClusterID)
	if !ok || time.Since(snapshot.ObservedAt) > 15*time.Second || snapshot.ObservedAt.After(time.Now().Add(time.Second)) {
		return fmt.Errorf("fresh gateway writer topology is required")
	}
	primaries := 0
	proven := false
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		primaries++
		if instance.ResourceID == lease.OwnerID && instance.Engine == model.EngineMySQL && instance.Health.State == model.HealthHealthy && strings.EqualFold(instance.EngineMetadata["read_only"], "false") && strings.EqualFold(instance.EngineMetadata["super_read_only"], "false") {
			for _, probe := range snapshot.Probes {
				if probe.InstanceID == instance.ResourceID && probe.DiscoveryObservedAt.Equal(snapshot.ObservedAt) && probe.Health.State == model.HealthHealthy {
					proven = true
				}
			}
		}
	}
	if primaries != 1 || !proven {
		return fmt.Errorf("gateway routing waits for a current uniquely writable SQL primary")
	}
	statuses, err := p.statuses(ctx, r, client)
	if err != nil {
		return err
	}
	last := p.inventory.GatewayGrant(request.ClusterID)
	matching := last.Epoch > 0 && last.TargetID == lease.OwnerID && last.LeaseID == lease.ResourceID && last.Mode == "activate"
	for _, status := range statuses {
		matching = matching && status.Active && sameGatewayGrant(status.LastGrant, last)
	}
	if !matching {
		return p.publish(ctx, r, client, lease, lease.OperationID, true)
	}
	return p.activate(ctx, client, lease, last.OperationID)
}
