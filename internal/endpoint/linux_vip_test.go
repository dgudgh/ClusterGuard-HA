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
}

func (inventory vipInventoryStub) HAEndpoints(model.ResourceID) []model.HAEndpoint {
	return append([]model.HAEndpoint{}, inventory.resources...)
}

func (inventory vipInventoryStub) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	value, found := inventory.endpoints[resourceID]
	return value, found
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

func vipProviderFixture(t *testing.T) (*LinuxVIPProvider, adapter.ResolvedOperation, *agentTransportStub, *MemoryLeaseStore) {
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
	inventory := vipInventoryStub{
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
	return provider, resolved, transport, leases
}

func TestVIPPrecheckFailsOnUnknownProbeCoverage(t *testing.T) {
	provider, resolved, transport, _ := vipProviderFixture(t)
	transport.unavailable[resolved.Snapshot.Instances[2].ResourceID] = true
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "coverage") {
		t.Fatalf("unknown coverage checks=%+v", checks)
	}
}

func TestVIPPrecheckFailsWithTwoOwners(t *testing.T) {
	provider, resolved, transport, _ := vipProviderFixture(t)
	transport.owners[resolved.Target.ResourceID] = true
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail || !strings.Contains(checks[0].Message, "multiple") {
		t.Fatalf("split owner checks=%+v", checks)
	}
}

func TestVIPTransferReleasesEveryNonTargetBeforeAcquire(t *testing.T) {
	provider, resolved, transport, _ := vipProviderFixture(t)
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("transfer VIP: %v", err)
	}
	if transport.owners[resolved.Primary.ResourceID] || !transport.owners[resolved.Target.ResourceID] {
		t.Fatalf("owners after transfer=%+v", transport.owners)
	}
	joined := strings.Join(transport.calls, "\n")
	release := strings.Index(joined, agent.CommandVIPRelease+":"+string(resolved.Primary.ResourceID))
	acquire := strings.Index(joined, agent.CommandVIPAcquire+":"+string(resolved.Target.ResourceID))
	if release < 0 || acquire < 0 || release > acquire {
		t.Fatalf("transfer order=%s", joined)
	}
}

func TestVIPTransferDoesNotAcquireWhenLeaseStoreBlocks(t *testing.T) {
	provider, resolved, transport, leases := vipProviderFixture(t)
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
	provider, resolved, transport, _ := vipProviderFixture(t)
	check := provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckFail {
		t.Fatalf("pre-transfer verify=%+v", check)
	}
	transport.owners[resolved.Primary.ResourceID] = false
	transport.owners[resolved.Target.ResourceID] = true
	check = provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckPass {
		t.Fatalf("target-only verify=%+v", check)
	}
}
