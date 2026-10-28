package store

import (
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"testing"
	"time"
)

type gatewayStoreAuthority struct{}

func (gatewayStoreAuthority) RequireMutationAuthority(context.Context) error { return nil }
func TestGatewayGrantReservationRequiresExactLiveLeaseAndPersistsOrder(t *testing.T) {
	r := NewMemory()
	c, owner := seedHAEndpointCluster(t, r, "gateway", 3306)
	resource, _, e := r.PutHAEndpoint(HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Hostname: "database.example", Port: 63306, Provider: model.EndpointProviderGateway, ProviderRef: "site", OwnerID: owner.ResourceID, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	leases := coordination.NewLeaseStore(r, gatewayStoreAuthority{}, nil)
	lease, e := leases.Acquire(context.Background(), endpoint.LeaseRequest{ClusterID: c.ResourceID, HAEndpointID: resource.ResourceID, OwnerID: owner.ResourceID, OperationID: resource.ResourceID, TTL: 3 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.PutHAEndpoint(HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Hostname: "other.example", Port: 63306, Provider: model.EndpointProviderGateway, ProviderRef: "different", OwnerID: owner.ResourceID, Active: true}); e == nil {
		t.Fatal("live writer lease allowed a gateway rebind")
	}
	first, e := r.ReserveGatewayGrant(lease, resource.ResourceID, "withdraw", true)
	if e != nil {
		t.Fatal(e)
	}
	if first.Epoch != 1 || first.Sequence != 1 || first.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatal("grant lifetime/order is wrong", first)
	}
	second, e := r.ReserveGatewayGrant(lease, resource.ResourceID, "activate", false)
	if e != nil {
		t.Fatal(e)
	}
	if second.Epoch != first.Epoch || second.Sequence != 2 {
		t.Fatal("activation lost fence")
	}
	// Reopening a serialized consensus snapshot retains the route fence.
	copy := cloneDiscoverySnapshot(r.snapshot)
	encoded, e := canonicalSnapshotContents(copy)
	if e != nil {
		t.Fatal(e)
	}
	var restored snapshot
	if e = json.Unmarshal(encoded, &restored); e != nil {
		t.Fatal(e)
	}
	copy, e = normalizeSnapshot(restored)
	if e != nil {
		t.Fatal(e)
	}
	other := NewMemory()
	other.snapshot = copy
	third, e := other.ReserveGatewayGrant(lease, resource.ResourceID, "withdraw", true)
	if e != nil || third.Epoch != 2 {
		t.Fatal("snapshot lost monotonic fence", e)
	}
	stale := lease
	stale.OwnerID = model.NewResourceID()
	if _, e = r.ReserveGatewayGrant(stale, resource.ResourceID, "withdraw", true); e == nil {
		t.Fatal("foreign lease allocated a route")
	}
	expired := lease
	expired.ExpiresAt = time.Now().Add(-time.Second)
	if e = r.PutCoordinationLease(coordination.LeaseRecord{Lease: expired, CreatedAt: time.Now(), UpdatedAt: time.Now()}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ReserveGatewayGrant(lease, resource.ResourceID, "activate", false); e == nil {
		t.Fatal("expired actual lease allocated route")
	}
	if r.GatewayGrant(c.ResourceID) != second {
		t.Fatal("rejected grant mutated fence")
	}
}
