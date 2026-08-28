package endpoint

import (
	"context"
	"fmt"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// ProviderRouter selects the writer-endpoint implementation from the active
// endpoint resource of each cluster. It keeps host VIP and Kubernetes Service
// mutations in separate trust domains while exposing one adapter interface.
type ProviderRouter struct {
	inventory Inventory
	providers map[model.EndpointProviderKind]adapter.HAEndpointProvider
}

func NewProviderRouter(inventory Inventory, providers map[model.EndpointProviderKind]adapter.HAEndpointProvider) *ProviderRouter {
	copy := make(map[model.EndpointProviderKind]adapter.HAEndpointProvider, len(providers))
	for kind, provider := range providers {
		if kind.Valid() && provider != nil {
			copy[kind] = provider
		}
	}
	return &ProviderRouter{inventory: inventory, providers: copy}
}

func (router *ProviderRouter) Executable(ctx context.Context) bool {
	if router == nil || router.inventory == nil {
		return false
	}
	for _, provider := range router.providers {
		if provider != nil && provider.Executable(ctx) {
			return true
		}
	}
	return false
}

func (router *ProviderRouter) provider(clusterID model.ResourceID) (adapter.HAEndpointProvider, error) {
	if router == nil || router.inventory == nil || !model.ValidResourceID(clusterID) {
		return nil, fmt.Errorf("writer endpoint provider router is not configured")
	}
	var selected model.EndpointProviderKind
	found := 0
	for _, resource := range router.inventory.HAEndpoints(clusterID) {
		candidate, exists := router.inventory.Endpoint(resource.EndpointID)
		if !exists || !candidate.Active {
			continue
		}
		kind := resource.Provider
		if kind == "" && resource.Kind == model.EndpointVIP && candidate.Kind == model.EndpointVIP {
			kind = model.EndpointProviderLinuxVIP
		}
		if !kind.Valid() {
			continue
		}
		found++
		selected = kind
	}
	if found != 1 {
		return nil, fmt.Errorf("exactly one active writer endpoint resource is required")
	}
	provider := router.providers[selected]
	if provider == nil {
		return nil, fmt.Errorf("writer endpoint provider %s is not enabled", selected)
	}
	return provider, nil
}

func (router *ProviderRouter) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: err.Error()}}
	}
	return provider.Precheck(ctx, resolved)
}

func (router *ProviderRouter) AuthorizeTransition(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	return provider.AuthorizeTransition(ctx, resolved)
}

func (router *ProviderRouter) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return err
	}
	return provider.Transfer(ctx, resolved)
}

func (router *ProviderRouter) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	return provider.Verify(ctx, resolved)
}

func (router *ProviderRouter) AuthorizeStableOwner(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, err
	}
	authorizer, ok := provider.(adapter.StableHAEndpointAuthorizer)
	if !ok {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("writer endpoint provider does not support stable ownership authorization")
	}
	return authorizer.AuthorizeStableOwner(ctx, resolved)
}

func (router *ProviderRouter) FormerPrimaryPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	provider, err := router.provider(resolved.Cluster.ResourceID)
	if err != nil {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: err.Error()}}
	}
	prechecker, ok := provider.(interface {
		FormerPrimaryPrecheck(context.Context, adapter.ResolvedOperation) []model.Check
	})
	if !ok {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: "writer endpoint provider does not support former-primary validation"}}
	}
	return prechecker.FormerPrimaryPrecheck(ctx, resolved)
}

func (router *ProviderRouter) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	provider, err := router.provider(cluster.ResourceID)
	if err != nil {
		return OwnershipObservation{}, err
	}
	observer, ok := provider.(interface {
		ObserveOwnership(context.Context, model.DatabaseCluster, model.TopologySnapshot) (OwnershipObservation, error)
	})
	if !ok {
		return OwnershipObservation{}, fmt.Errorf("writer endpoint provider does not expose ownership observation")
	}
	return observer.ObserveOwnership(ctx, cluster, snapshot)
}
