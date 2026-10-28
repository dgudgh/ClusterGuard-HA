package endpoint

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"time"
)

type GatewayView struct {
	AuthorityUnconfirmed bool             `json:"authority_unconfirmed"`
	ClusterID            model.ResourceID `json:"cluster_id"`
	OwnerID              model.ResourceID `json:"owner_id"`
	Ready                bool             `json:"ready"`
	LeaseValid           bool             `json:"lease_valid"`
	LeaseExpiresAt       time.Time        `json:"lease_expires_at"`
	Statuses             []GatewayStatus  `json:"gateways"`
}

func (p *GatewayProvider) InspectGateway(ctx context.Context, cluster model.ResourceID) (GatewayView, error) {
	view := GatewayView{ClusterID: cluster}
	r, client, err := p.resource(cluster)
	if err != nil {
		return view, err
	}
	view.OwnerID = r.resource.OwnerID
	view.Statuses, err = p.statuses(ctx, r, client)
	if err != nil {
		return view, err
	}
	lease, err := p.current(ctx, r, r.resource.OwnerID)
	if err != nil {
		view.AuthorityUnconfirmed = true
		return view, nil
	}
	view.LeaseValid = true
	view.LeaseExpiresAt = lease.ExpiresAt
	last := p.inventory.GatewayGrant(cluster)
	view.Ready = lease.OperationID == lease.HAEndpointID && lease.PreviousOwnerID == "" && last.Epoch > 0
	for _, status := range view.Statuses {
		view.Ready = view.Ready && status.Active && status.LastGrant.TargetID == view.OwnerID && status.LastGrant.LeaseID == lease.ResourceID && sameGatewayGrant(status.LastGrant, last)
	}
	return view, nil
}
func (router *ProviderRouter) InspectGateway(ctx context.Context, cluster model.ResourceID) (GatewayView, error) {
	provider, err := router.provider(cluster)
	if err != nil {
		return GatewayView{}, err
	}
	inspector, ok := provider.(interface {
		InspectGateway(context.Context, model.ResourceID) (GatewayView, error)
	})
	if !ok {
		return GatewayView{}, errors.New("active writer endpoint is not an enabled gateway")
	}
	return inspector.InspectGateway(ctx, cluster)
}
