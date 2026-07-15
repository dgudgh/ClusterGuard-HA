package endpoint

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type vipInventoryStub struct {
	resources []model.HAEndpoint
	endpoints map[model.ResourceID]model.Endpoint
	updates   int
	updateErr error
}

func (inventory vipInventoryStub) HAEndpoints(model.ResourceID) []model.HAEndpoint {
	return append([]model.HAEndpoint{}, inventory.resources...)
}

func (inventory vipInventoryStub) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	value, found := inventory.endpoints[resourceID]
	return value, found
}

func (inventory *vipInventoryStub) CommitHAEndpointOwner(clusterID, resourceID, ownerID model.ResourceID, healthy bool) error {
	if inventory.updateErr != nil {
		return inventory.updateErr
	}
	for index := range inventory.resources {
		resource := &inventory.resources[index]
		if resource.ResourceID != resourceID || resource.ClusterID != clusterID {
			continue
		}
		resource.OwnerID = ownerID
		resource.Healthy = healthy
		endpoint := inventory.endpoints[resource.EndpointID]
		endpoint.InstanceID = ownerID
		inventory.endpoints[resource.EndpointID] = endpoint
		inventory.updates++
		return nil
	}
	return errors.New("HA endpoint not found")
}

type agentTransportStub struct {
	mu          sync.Mutex
	owners      map[model.ResourceID]bool
	unavailable map[model.ResourceID]bool
	calls       []string
}

func (transport *agentTransportStub) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.unavailable[instance.ResourceID] {
		return agent.Response{}, errors.New("agent unavailable")
	}
	transport.calls = append(transport.calls, request.Command+":"+string(instance.ResourceID))
	switch request.Command {
	case agent.CommandVIPStatus:
		owns := transport.owners[instance.ResourceID]
		return agent.Response{Status: agent.StatusOK, OwnsVIP: &owns}, nil
	case agent.CommandVIPRelease:
		transport.owners[instance.ResourceID] = false
		return agent.Response{Status: agent.StatusOK}, nil
	case agent.CommandVIPAcquire:
		transport.owners[instance.ResourceID] = true
		return agent.Response{Status: agent.StatusOK}, nil
	default:
		return agent.Response{Status: agent.StatusBlocked}, nil
	}
}

func vipProviderFixture(t *testing.T) (*LinuxVIPProvider, adapter.ResolvedOperation, *agentTransportStub, *MemoryLeaseStore, *vipInventoryStub) {
	t.Helper()
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	siblingID := model.NewResourceID()
	endpointID := model.NewResourceID()
	haID := model.NewResourceID()
	instances := []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary},
		{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, ClusterID: clusterID, Hostname: "mysql-b", IPAddress: "192.0.2.11", Port: 3306, Role: model.RoleReplica},
		{ResourceMeta: model.ResourceMeta{ResourceID: siblingID}, ClusterID: clusterID, Hostname: "mysql-c", IPAddress: "192.0.2.12", Port: 3306, Role: model.RoleReplica},
	}
	inventory := &vipInventoryStub{
		resources: []model.HAEndpoint{{ResourceMeta: model.ResourceMeta{ResourceID: haID}, ClusterID: clusterID, EndpointID: endpointID, Kind: model.EndpointVIP, OwnerID: primaryID, Interface: "ens160", Prefix: 24}},
		endpoints: map[model.ResourceID]model.Endpoint{endpointID: {ResourceMeta: model.ResourceMeta{ResourceID: endpointID}, ClusterID: clusterID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Active: true}},
	}
	transport := &agentTransportStub{owners: map[model.ResourceID]bool{primaryID: true}, unavailable: map[model.ResourceID]bool{}}
	now := time.Date(2026, time.July, 13, 13, 0, 0, 0, time.UTC)
	leases := NewMemoryLeaseStore(func() time.Time { return now })
	provider := NewLinuxVIPProvider(inventory, transport, leases, "agent-secret", func() time.Time { return now })
	resolved := adapter.ResolvedOperation{
		OperationID: model.NewResourceID(),
		Cluster:     model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL},
		Snapshot:    model.TopologySnapshot{ClusterID: clusterID, Instances: instances, ObservedAt: now},
		Primary:     instances[0], Target: instances[1], PlanDigest: "sha256:0123456789abcdef",
	}
	return provider, resolved, transport, leases, inventory
}

func TestVIPPrecheckFailsOnUnknownProbeCoverage(t *testing.T) {
	provider, resolved, transport, _, _ := vipProviderFixture(t)
	transport.unavailable[resolved.Snapshot.Instances[2].ResourceID] = true
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "coverage") {
		t.Fatalf("unknown coverage checks=%+v", checks)
	}
}

func TestObserveOwnershipCarriesMetadataRevisionsForConditionalCommit(t *testing.T) {
	provider, resolved, _, _, inventory := vipProviderFixture(t)
	inventory.resources[0].MetadataRevision = 7
	endpointID := inventory.resources[0].EndpointID
	endpointValue := inventory.endpoints[endpointID]
	endpointValue.MetadataRevision = 11
	inventory.endpoints[endpointID] = endpointValue

	observation, err := provider.ObserveOwnership(context.Background(), resolved.Cluster, resolved.Snapshot)
	if err != nil {
		t.Fatalf("observe ownership: %v", err)
	}
	if observation.HAEndpointRevision != 7 || observation.EndpointRevision != 11 {
		t.Fatalf("ownership observation revisions=%+v", observation)
	}
}

func TestVIPPrecheckFailsWithTwoOwners(t *testing.T) {
	provider, resolved, transport, _, _ := vipProviderFixture(t)
	transport.owners[resolved.Target.ResourceID] = true
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "multiple") {
		t.Fatalf("split owner checks=%+v", checks)
	}
}

func TestVIPTransferReleasesEveryNonTargetBeforeAcquire(t *testing.T) {
	provider, resolved, transport, _, inventory := vipProviderFixture(t)
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("transfer VIP: %v", err)
	}
	if transport.owners[resolved.Primary.ResourceID] || !transport.owners[resolved.Target.ResourceID] {
		t.Fatalf("owners after transfer=%+v", transport.owners)
	}
	if inventory.updates != 1 || inventory.resources[0].OwnerID != resolved.Target.ResourceID || !inventory.resources[0].Healthy || inventory.endpoints[inventory.resources[0].EndpointID].InstanceID != resolved.Target.ResourceID {
		t.Fatalf("inventory did not follow physical VIP ownership: %+v", inventory)
	}
	joined := strings.Join(transport.calls, "\n")
	release := strings.Index(joined, agent.CommandVIPRelease+":"+string(resolved.Primary.ResourceID))
	acquire := strings.Index(joined, agent.CommandVIPAcquire+":"+string(resolved.Target.ResourceID))
	if release < 0 || acquire < 0 || release > acquire {
		t.Fatalf("transfer order=%s", joined)
	}
}

func TestVIPAuthorizeTransitionCreatesTargetLeaseWithoutMovingVIP(t *testing.T) {
	provider, resolved, transport, leases, inventory := vipProviderFixture(t)
	stable, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("acquire stable ownership lease: %v", err)
	}
	authorization, err := provider.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatalf("authorize transition: %v", err)
	}
	defer authorization.Cancel()
	if len(leases.leases) != 1 {
		t.Fatalf("transition leases=%d, want 1", len(leases.leases))
	}
	var authorized Lease
	for _, lease := range leases.leases {
		authorized = lease
	}
	if authorized.OperationID != resolved.OperationID || authorized.OwnerID != resolved.Target.ResourceID || !authorized.Active {
		t.Fatalf("transition lease=%+v", authorized)
	}
	if authorized.ResourceID == stable.ResourceID {
		t.Fatalf("stable ownership lease was not replaced during transition: stable=%+v transition=%+v", stable, authorized)
	}
	if len(transport.calls) != 0 || !transport.owners[resolved.Primary.ResourceID] || transport.owners[resolved.Target.ResourceID] {
		t.Fatalf("authorization moved VIP ownership: calls=%v owners=%+v", transport.calls, transport.owners)
	}
	if inventory.updates != 0 || inventory.resources[0].OwnerID != resolved.Primary.ResourceID {
		t.Fatalf("authorization changed canonical owner: %+v", inventory)
	}
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("transfer with prepared lease: %v", err)
	}
	if len(leases.leases) != 1 {
		t.Fatalf("transfer replaced prepared lease: %+v", leases.leases)
	}
	for resourceID := range leases.leases {
		if resourceID != authorized.ResourceID {
			t.Fatalf("transfer used lease %s, want prepared lease %s", resourceID, authorized.ResourceID)
		}
	}
}

func TestVIPFinalizationAllowsImmediateReverseTransition(t *testing.T) {
	provider, resolved, _, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	authorization, err := provider.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Cancel()
	if err := provider.Transfer(authorization.Context, resolved); err != nil {
		t.Fatal(err)
	}
	if err := authorization.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize transition authorization: %v", err)
	}

	reverse := resolved
	reverse.OperationID = model.NewResourceID()
	reverse.Primary, reverse.Target = resolved.Target, resolved.Primary
	if _, err := provider.AuthorizeTransition(context.Background(), reverse); err != nil {
		t.Fatalf("immediate reverse authorization: %v", err)
	}
}

type failingRenewalLeaseStore struct {
	delegate *MemoryLeaseStore
	mu       sync.Mutex
	calls    int
}

func (store *failingRenewalLeaseStore) Acquire(ctx context.Context, request LeaseRequest) (Lease, error) {
	store.mu.Lock()
	store.calls++
	call := store.calls
	store.mu.Unlock()
	if call > 1 {
		return Lease{}, errors.New("quorum renewal failed")
	}
	return store.delegate.Acquire(ctx, request)
}

func (store *failingRenewalLeaseStore) Validate(ctx context.Context, lease Lease) error {
	return store.delegate.Validate(ctx, lease)
}

func (store *failingRenewalLeaseStore) FinalizeTransition(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	return store.delegate.FinalizeTransition(ctx, lease, ttl)
}

func (store *failingRenewalLeaseStore) Release(ctx context.Context, resourceID model.ResourceID) error {
	return store.delegate.Release(ctx, resourceID)
}

func TestVIPTransitionAuthorizationCancelsWhenLeaseRenewalFails(t *testing.T) {
	provider, resolved, _, leases, _ := vipProviderFixture(t)
	provider.leases = &failingRenewalLeaseStore{delegate: leases}
	provider.transitionRenewInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	authorization, err := provider.AuthorizeTransition(ctx, resolved)
	if err != nil {
		t.Fatalf("authorize transition: %v", err)
	}
	defer authorization.Cancel()
	select {
	case <-authorization.Context.Done():
		if cause := context.Cause(authorization.Context); cause == nil || !strings.Contains(cause.Error(), "renew") {
			t.Fatalf("authorization cancellation cause=%v", cause)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("transition authorization remained active after lease renewal failure")
	}
}

func TestVIPTransferDoesNotAcquireWhenLeaseStoreBlocks(t *testing.T) {
	provider, resolved, transport, leases, _ := vipProviderFixture(t)
	leases.Blocked = true
	if err := provider.Transfer(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("blocked lease error=%v", err)
	}
	for _, call := range transport.calls {
		if strings.HasPrefix(call, agent.CommandVIPAcquire+":") {
			t.Fatalf("VIP acquired without lease: %v", transport.calls)
		}
	}
}

func TestVIPVerifyRequiresExactlyOneTargetOwner(t *testing.T) {
	provider, resolved, transport, _, inventory := vipProviderFixture(t)
	check := provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckFail {
		t.Fatalf("pre-transfer verify=%+v", check)
	}
	transport.owners[resolved.Primary.ResourceID] = false
	transport.owners[resolved.Target.ResourceID] = true
	check = provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckFail || !strings.Contains(check.Message, "metadata") {
		t.Fatalf("physical-only target ownership must not pass=%+v", check)
	}
	if err := inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, inventory.resources[0].ResourceID, resolved.Target.ResourceID, true); err != nil {
		t.Fatal(err)
	}
	check = provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckPass {
		t.Fatalf("physical and metadata target ownership=%+v", check)
	}
}

func TestVIPTransferFailsWhenPhysicalMoveCannotCommitCanonicalOwner(t *testing.T) {
	provider, resolved, _, _, inventory := vipProviderFixture(t)
	inventory.updateErr = errors.New("metadata quorum unavailable")
	if err := provider.Transfer(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata commit error=%v", err)
	}
}
