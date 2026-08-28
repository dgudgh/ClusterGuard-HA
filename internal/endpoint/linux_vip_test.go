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
	blocked     map[model.ResourceID]string
	calls       []string
	attempts    []string
	requests    []agent.Request
}

type controllableLeaseStore struct {
	*MemoryLeaseStore
	fail <-chan struct{}
}

func (store controllableLeaseStore) Acquire(ctx context.Context, request LeaseRequest) (Lease, error) {
	select {
	case <-store.fail:
		return Lease{}, errors.New("lease renewal blocked")
	default:
		return store.MemoryLeaseStore.Acquire(ctx, request)
	}
}

func (transport *agentTransportStub) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.attempts = append(transport.attempts, request.Command+":"+string(instance.ResourceID))
	if transport.unavailable[instance.ResourceID] {
		return agent.Response{}, errors.New("agent unavailable")
	}
	if message := transport.blocked[instance.ResourceID]; message != "" {
		return agent.Response{Status: agent.StatusBlocked, Message: message}, nil
	}
	transport.calls = append(transport.calls, request.Command+":"+string(instance.ResourceID))
	transport.requests = append(transport.requests, request)
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
		endpoints: map[model.ResourceID]model.Endpoint{endpointID: {ResourceMeta: model.ResourceMeta{ResourceID: endpointID}, ClusterID: clusterID, InstanceID: primaryID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Active: true}},
	}
	transport := &agentTransportStub{owners: map[model.ResourceID]bool{primaryID: true}, unavailable: map[model.ResourceID]bool{}, blocked: map[model.ResourceID]string{}}
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

func TestLinuxVIPProviderRejectsKubernetesEndpointResource(t *testing.T) {
	provider, resolved, _, _, inventory := vipProviderFixture(t)
	inventory.resources[0].Provider = model.EndpointProviderKubernetesService
	inventory.resources[0].ProviderRef = "database/mysql-writer"
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "kubernetes_service") {
		t.Fatalf("Kubernetes endpoint was accepted by Linux runtime: %+v", checks)
	}
}

func TestFormerPrimaryPrecheckAllowsUnrelatedProbeGapWithStableMajorityLease(t *testing.T) {
	provider, resolved, transport, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transport.unavailable[resolved.Snapshot.Instances[2].ResourceID] = true

	checks := provider.FormerPrimaryPrecheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Name != "former_primary_vip_absent" || checks[0].Status != model.CheckPass {
		t.Fatalf("former-primary endpoint checks=%+v", checks)
	}
	if !strings.Contains(checks[0].Message, "majority") {
		t.Fatalf("partial proof did not explain majority-backed safety: %+v", checks[0])
	}
}

func TestAuthorizeStableOwnerRenewsOnlyExistingCurrentPrimaryLease(t *testing.T) {
	provider, resolved, _, leases, inventory := vipProviderFixture(t)
	initial, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := provider.AuthorizeStableOwner(context.Background(), resolved)
	if err != nil {
		t.Fatalf("authorize stable owner: %v", err)
	}
	defer authorization.Cancel()
	current, err := leases.Current(context.Background(), resolved.Cluster.ResourceID, inventory.resources[0].ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.LeaseID != initial.ResourceID || !SameLeaseIdentity(initial, current) || !current.ExpiresAt.After(initial.ExpiresAt) {
		t.Fatalf("stable authorization changed identity or failed to extend lease: initial=%+v current=%+v authorization=%+v", initial, current, authorization)
	}
}

func TestAuthorizeStableOwnerCancelsWhenRenewalFails(t *testing.T) {
	provider, resolved, _, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	provider.transitionRenewInterval = time.Millisecond
	fail := make(chan struct{})
	provider.leases = controllableLeaseStore{MemoryLeaseStore: leases, fail: fail}
	authorization, err := provider.AuthorizeStableOwner(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Cancel()
	close(fail)
	select {
	case <-authorization.Context.Done():
		if cause := context.Cause(authorization.Context); cause == nil || !strings.Contains(cause.Error(), "renew stable primary") {
			t.Fatalf("stable renewal cancellation cause=%v", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("stable ownership authorization ignored renewal failure")
	}
}

func TestAuthorizeStableOwnerRejectsLeaseForDifferentOwner(t *testing.T) {
	provider, resolved, _, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Target.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AuthorizeStableOwner(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "not stable for the current primary") {
		t.Fatalf("foreign stable lease was accepted: %v", err)
	}
}

func TestFormerPrimaryPrecheckRequiresDirectTargetProbe(t *testing.T) {
	provider, resolved, transport, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transport.blocked[resolved.Target.ResourceID] = "agent request is expired or outside the allowed time window"

	checks := provider.FormerPrimaryPrecheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("former-primary target gap checks=%+v", checks)
	}
	if !strings.Contains(checks[0].Message, "expired") || !strings.Contains(checks[0].Message, resolved.Target.Hostname) {
		t.Fatalf("target probe failure lost actionable detail: %+v", checks[0])
	}
}

func TestFormerPrimaryPrecheckRejectsVIPOnFormerPrimary(t *testing.T) {
	provider, resolved, transport, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	transport.owners[resolved.Target.ResourceID] = true

	checks := provider.FormerPrimaryPrecheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "former primary") {
		t.Fatalf("former-primary VIP owner checks=%+v", checks)
	}
}

func TestFormerPrimaryPrecheckRequiresStableMajorityLease(t *testing.T) {
	provider, resolved, _, _, _ := vipProviderFixture(t)
	checks := provider.FormerPrimaryPrecheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "lease") {
		t.Fatalf("missing stable lease checks=%+v", checks)
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

func TestObserveOwnershipReportsSuccessfulPartialProbeCoverage(t *testing.T) {
	provider, resolved, transport, _, _ := vipProviderFixture(t)
	missingID := resolved.Snapshot.Instances[2].ResourceID
	transport.unavailable[missingID] = true

	observation, err := provider.ObserveOwnership(context.Background(), resolved.Cluster, resolved.Snapshot)
	if err != nil {
		t.Fatalf("observe partial ownership: %v", err)
	}
	if observation.Complete {
		t.Fatalf("partial observation was marked complete: %+v", observation)
	}
	observed := make(map[model.ResourceID]bool, len(observation.ObservedInstanceIDs))
	for _, instanceID := range observation.ObservedInstanceIDs {
		observed[instanceID] = true
	}
	if len(observed) != 2 || !observed[resolved.Primary.ResourceID] || !observed[resolved.Target.ResourceID] || observed[missingID] {
		t.Fatalf("successful ownership coverage=%v, observation=%+v", observed, observation)
	}
	if len(observation.OwnerIDs) != 1 || observation.OwnerIDs[0] != resolved.Primary.ResourceID {
		t.Fatalf("partial ownership result=%+v", observation)
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

func TestVIPTransferAllowsOnlyVerifiedIsolatedOldPrimaryProbeGap(t *testing.T) {
	provider, resolved, transport, _, inventory := vipProviderFixture(t)
	transport.unavailable[resolved.Primary.ResourceID] = true
	transport.owners[resolved.Primary.ResourceID] = false

	if err := provider.Transfer(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unverified old-primary probe gap was accepted: %v", err)
	}

	resolved.VerifiedIsolatedSourceID = resolved.Primary.ResourceID
	transport.attempts = nil
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("transfer with independently verified old-primary isolation: %v", err)
	}
	if !transport.owners[resolved.Target.ResourceID] || inventory.resources[0].OwnerID != resolved.Target.ResourceID {
		t.Fatalf("VIP did not converge on target: owners=%+v resource=%+v", transport.owners, inventory.resources[0])
	}
	for _, attempt := range transport.attempts {
		if strings.HasSuffix(attempt, ":"+string(resolved.Primary.ResourceID)) {
			t.Fatalf("VIP transfer re-probed independently isolated source: %v", transport.attempts)
		}
	}
	statusCalls := 0
	for _, attempt := range transport.attempts {
		if strings.HasPrefix(attempt, agent.CommandVIPStatus+":") {
			statusCalls++
		}
	}
	if statusCalls != 4 {
		t.Fatalf("verified failover VIP transfer used %d status probes, want 4: %v", statusCalls, transport.attempts)
	}

	provider, resolved, transport, _, _ = vipProviderFixture(t)
	resolved.VerifiedIsolatedSourceID = resolved.Primary.ResourceID
	transport.unavailable[resolved.Snapshot.Instances[2].ResourceID] = true
	if err := provider.Transfer(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("non-source probe gap was accepted: %v", err)
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
	if authorization.LeaseID != authorized.ResourceID {
		t.Fatalf("authorization lease=%s, want %s", authorization.LeaseID, authorized.ResourceID)
	}
	if authorized.ResourceID != stable.ResourceID {
		t.Fatalf("stable ownership lease was not atomically upgraded during transition: stable=%+v transition=%+v", stable, authorized)
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

func TestVIPAbortTransitionRestoresSourceLease(t *testing.T) {
	provider, resolved, transport, leases, inventory := vipProviderFixture(t)
	if _, err := leases.Acquire(context.Background(), LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: inventory.resources[0].ResourceID,
		OperationID: inventory.resources[0].ResourceID, OwnerID: resolved.Primary.ResourceID, TTL: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	authorization, err := provider.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatalf("authorize transition: %v", err)
	}
	defer authorization.Cancel()
	if authorization.Abort == nil {
		t.Fatal("transition authorization has no abort callback")
	}
	if err := authorization.Abort(context.Background()); err != nil {
		t.Fatalf("abort transition: %v", err)
	}
	if len(leases.leases) != 1 {
		t.Fatalf("leases after abort=%+v", leases.leases)
	}
	for _, lease := range leases.leases {
		if lease.ResourceID != authorization.LeaseID || lease.OperationID != inventory.resources[0].ResourceID || lease.OwnerID != resolved.Primary.ResourceID || lease.PreviousOwnerID != "" {
			t.Fatalf("restored lease=%+v", lease)
		}
	}
	if len(transport.calls) != 0 || !transport.owners[resolved.Primary.ResourceID] || transport.owners[resolved.Target.ResourceID] {
		t.Fatalf("abort moved VIP ownership: calls=%v owners=%+v", transport.calls, transport.owners)
	}
}

func TestVIPSignedRequestBindsDatabaseEngine(t *testing.T) {
	provider, resolved, transport, _, _ := vipProviderFixture(t)
	resolved.Cluster.Engine = model.EnginePostgreSQL
	provider.Precheck(context.Background(), resolved)
	if len(transport.requests) == 0 {
		t.Fatal("VIP precheck sent no agent requests")
	}
	for _, request := range transport.requests {
		if request.Engine != model.EnginePostgreSQL {
			t.Fatalf("VIP request engine=%q, want postgresql", request.Engine)
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

type stableRenewalLeaseStore struct {
	delegate      *MemoryLeaseStore
	stableRenewed chan struct{}
	once          sync.Once
}

func (store *stableRenewalLeaseStore) Acquire(ctx context.Context, request LeaseRequest) (Lease, error) {
	lease, err := store.delegate.Acquire(ctx, request)
	if err == nil && request.OperationID == request.HAEndpointID {
		store.once.Do(func() { close(store.stableRenewed) })
	}
	return lease, err
}

func (store *stableRenewalLeaseStore) Validate(ctx context.Context, lease Lease) error {
	return store.delegate.Validate(ctx, lease)
}

func (store *stableRenewalLeaseStore) FinalizeTransition(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	return store.delegate.FinalizeTransition(ctx, lease, ttl)
}

func (store *stableRenewalLeaseStore) RollbackTransition(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	return store.delegate.RollbackTransition(ctx, lease, ttl)
}

func (store *stableRenewalLeaseStore) Release(ctx context.Context, resourceID model.ResourceID) error {
	return store.delegate.Release(ctx, resourceID)
}

func TestVIPFinalizationRenewsStableLeaseUntilAuthorizationIsCanceled(t *testing.T) {
	provider, resolved, _, leases, _ := vipProviderFixture(t)
	tracking := &stableRenewalLeaseStore{delegate: leases, stableRenewed: make(chan struct{})}
	provider.leases = tracking
	provider.transitionRenewInterval = time.Millisecond

	authorization, err := provider.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatalf("authorize transition: %v", err)
	}
	defer authorization.Cancel()
	if err := authorization.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize transition: %v", err)
	}
	select {
	case <-tracking.stableRenewed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("finalized ownership lease was not renewed while the transition operation remained active")
	}
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

func (store *failingRenewalLeaseStore) RollbackTransition(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	return store.delegate.RollbackTransition(ctx, lease, ttl)
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
