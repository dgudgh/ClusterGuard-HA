package endpoint_test

import (
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type gatewayAuthority struct{ blocked atomic.Bool }

func (a *gatewayAuthority) RequireMutationAuthority(context.Context) error {
	if a.blocked.Load() {
		return errors.New("no majority")
	}
	return nil
}

type gatewayInventory struct {
	*store.Repository
	top model.TopologySnapshot
}

func (i *gatewayInventory) TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool) {
	return i.top, true
}

type gatewayTransport struct {
	states       []endpoint.GatewayStatus
	denyActivate bool
	calls        []model.GatewayGrant
}

func (t *gatewayTransport) Statuses(context.Context, model.ResourceID) ([]endpoint.GatewayStatus, error) {
	out := append([]endpoint.GatewayStatus(nil), t.states...)
	for n := range out {
		out[n].Targets = map[string]string{}
		for id, addr := range t.states[n].Targets {
			out[n].Targets[id] = addr
		}
	}
	return out, nil
}
func (t *gatewayTransport) Apply(ctx context.Context, g model.GatewayGrant, validate func(context.Context) error) error {
	t.calls = append(t.calls, g)
	for n := range t.states {
		if e := validate(ctx); e != nil {
			return e
		}
		if t.denyActivate && g.Mode == "activate" && n == 1 {
			return errors.New("second gateway unavailable")
		}
		s := &t.states[n]
		if g.Epoch < s.LastGrant.Epoch || g.Epoch == s.LastGrant.Epoch && g.Sequence <= s.LastGrant.Sequence {
			return errors.New("old grant")
		}
		s.LastGrant = g
		s.Active = g.Mode == "activate"
	}
	return nil
}

type gatewayFixture struct {
	i              *gatewayInventory
	a              *gatewayAuthority
	l              *coordination.LeaseStore
	p              *endpoint.GatewayProvider
	wire           *gatewayTransport
	c              model.DatabaseCluster
	source, target model.DatabaseInstance
	resource       model.HAEndpoint
}

func newGatewayFixture(t *testing.T) *gatewayFixture { return gatewayFixtureWithPorts(t, 3306, 3307) }
func gatewayFixtureWithPorts(t *testing.T, localPort, cloudPort int) *gatewayFixture {
	t.Helper()
	r := store.NewMemory()
	c, _, e := r.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "hybrid"}, []model.Endpoint{{Kind: model.EndpointDatabase, IPAddress: "127.0.0.1", Port: localPort, Active: true}, {Kind: model.EndpointDatabase, IPAddress: "127.0.0.1", Port: cloudPort, Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	instances := []model.DatabaseInstance{}
	for n, port := range []int{localPort, cloudPort} {
		v, e := r.ReconcileInstance(model.DatabaseInstance{ClusterID: c.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": []string{"local-id", "cloud-id"}[n]}, IPAddress: "127.0.0.1", Port: port})
		if e != nil {
			t.Fatal(e)
		}

		var database model.Endpoint
		for _, entry := range r.Endpoints(c.ResourceID) {
			if entry.Kind == model.EndpointDatabase && entry.Port == port {
				database = entry
			}
		}
		database.InstanceID = v.Instance.ResourceID
		if _, e = r.UpsertEndpoint(database); e != nil {
			t.Fatal(e)
		}

		instances = append(instances, v.Instance)
	}
	resource, _, e := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway, ProviderRef: "hybrid", Hostname: "database.example", Port: 63306, OwnerID: instances[0].ResourceID, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	top := model.TopologySnapshot{ClusterID: c.ResourceID, ObservedAt: now, Instances: instances}
	top.Instances[0].Role = model.RolePrimary
	top.Instances[1].Role = model.RoleReplica
	for n := range top.Instances {
		top.Instances[n].Health = model.Health{State: model.HealthHealthy, ObservedAt: now}
		ro := "true"
		if n == 0 {
			ro = "false"
		}
		top.Instances[n].EngineMetadata = map[string]string{"read_only": ro, "super_read_only": ro}
		top.Probes = append(top.Probes, model.ProbeStatus{InstanceID: instances[n].ResourceID, DiscoveryObservedAt: now, Health: model.Health{State: model.HealthHealthy, ObservedAt: now}})
	}
	inventory := &gatewayInventory{Repository: r, top: top}
	a := &gatewayAuthority{}
	leases := coordination.NewLeaseStore(r, a, nil)
	addresses := map[string]string{string(instances[0].ResourceID): fmt.Sprintf("127.0.0.1:%d", localPort), string(instances[1].ResourceID): fmt.Sprintf("127.0.0.1:%d", cloudPort)}
	wire := &gatewayTransport{states: []endpoint.GatewayStatus{{InstanceID: "gateway-a", ClusterID: c.ResourceID, Targets: addresses}, {InstanceID: "gateway-b", ClusterID: c.ResourceID, Targets: addresses}}}
	return &gatewayFixture{i: inventory, a: a, l: leases, p: endpoint.NewGatewayProvider(inventory, leases, map[string]endpoint.GatewayTransport{"hybrid": wire}), wire: wire, c: c, source: top.Instances[0], target: top.Instances[1], resource: resource}
}
func (f *gatewayFixture) bootstrap(t *testing.T) {
	t.Helper()
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	if e := keeper.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.wire.calls) != 2 || !f.wire.states[0].Active {
		t.Fatal("keeper did not publish majority-backed initial route")
	}
}
func (f *gatewayFixture) resolved() adapter.ResolvedOperation {
	return adapter.ResolvedOperation{OperationID: model.NewResourceID(), Cluster: f.c, Primary: f.source, Target: f.target, Snapshot: f.i.top}
}
func TestGatewayMajorityLeaseTransitionAndKeeperRenewal(t *testing.T) {
	f := newGatewayFixture(t)
	f.bootstrap(t)
	ctx := context.Background()
	resolved := f.resolved()
	initial := f.i.GatewayGrant(f.c.ResourceID)
	auth, e := f.p.AuthorizeTransition(ctx, resolved)
	if e != nil {
		t.Fatal(e)
	}
	defer auth.Cancel()
	if !model.ValidResourceID(auth.LeaseID) || f.wire.states[0].Active || f.wire.states[1].Active {
		t.Fatal("transition did not withdraw both old writers")
	}
	lease, e := f.l.Current(ctx, f.c.ResourceID, f.resource.ResourceID)
	if e != nil || lease.ResourceID != auth.LeaseID || lease.OperationID != resolved.OperationID || lease.OwnerID != f.target.ResourceID {
		t.Fatal("transition is not backed by the actual majority lease", e)
	}
	if e = f.p.Transfer(ctx, resolved); e != nil {
		t.Fatal(e)
	}
	if result := f.p.Verify(ctx, resolved); result.Status != model.CheckPass {
		t.Fatal(result)
	}
	if e = auth.Finalize(ctx); e != nil {
		t.Fatal(e)
	}
	stable, e := f.l.Current(ctx, f.c.ResourceID, f.resource.ResourceID)
	if e != nil || stable.OperationID != f.resource.ResourceID || stable.PreviousOwnerID != "" {
		t.Fatal("lease was not stabilized", e)
	}
	before := f.i.GatewayGrant(f.c.ResourceID)
	if before.Epoch <= initial.Epoch || before.ExpiresAt.After(stable.ExpiresAt) {
		t.Fatal("invalid ordering/lease lifetime")
	}
	for n := range f.i.top.Instances {
		v := &f.i.top.Instances[n]
		v.Role = model.RoleReplica
		v.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
		if v.ResourceID == f.target.ResourceID {
			v.Role = model.RolePrimary
			v.EngineMetadata = map[string]string{"read_only": "false", "super_read_only": "false"}
		}
	}
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	if e = keeper.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	after := f.i.GatewayGrant(f.c.ResourceID)
	if after.Epoch != before.Epoch || after.Sequence <= before.Sequence {
		t.Fatal("steady-state renewal changed owner epoch or did not progress")
	}
	f.a.blocked.Store(true)
	_ = keeper.RunOnce(ctx)
	if f.i.GatewayGrant(f.c.ResourceID) != after {
		t.Fatal("lost majority still published a route")
	}
}
func TestGatewayFailuresNeverAuthorizeUnknownOwner(t *testing.T) {
	t.Run("duplicate gateway process", func(t *testing.T) {
		f := newGatewayFixture(t)
		f.wire.states[1].InstanceID = f.wire.states[0].InstanceID
		if checks := f.p.Precheck(context.Background(), f.resolved()); checks[0].Status != model.CheckFail {
			t.Fatal("two aliases of one gateway passed redundancy check")
		}
	})
	t.Run("wrong backend", func(t *testing.T) {
		f := newGatewayFixture(t)
		f.wire.states[0].Targets[string(f.target.ResourceID)] = "127.0.0.1:9999"
		if checks := f.p.Precheck(context.Background(), f.resolved()); checks[0].Status != model.CheckFail {
			t.Fatal("wrong backend identity accepted")
		}
		if _, e := f.p.AuthorizeTransition(context.Background(), f.resolved()); e == nil {
			t.Fatal("bad binding reached authorization")
		}
		if f.i.GatewayGrant(f.c.ResourceID).Epoch != 0 {
			t.Fatal("bad mapping allocated route")
		}
	})
	t.Run("partial activation", func(t *testing.T) {
		f := newGatewayFixture(t)
		f.bootstrap(t)
		resolved := f.resolved()
		auth, e := f.p.AuthorizeTransition(context.Background(), resolved)
		if e != nil {
			t.Fatal(e)
		}
		defer auth.Cancel()
		f.wire.denyActivate = true
		if e = f.p.Transfer(context.Background(), resolved); e == nil {
			t.Fatal("partial activation accepted")
		}
		for _, status := range f.wire.states {
			if status.Active || status.LastGrant.Mode != "withdraw" {
				t.Fatal("failed activation left writer active")
			}
		}
		if f.p.Verify(context.Background(), resolved).Status == model.CheckPass {
			t.Fatal("failed transfer verified")
		}
	})
	t.Run("fenced SQL owner", func(t *testing.T) {
		f := newGatewayFixture(t)
		f.bootstrap(t)
		last := f.i.GatewayGrant(f.c.ResourceID)
		f.i.top.Instances[0].EngineMetadata["read_only"] = "true"
		if e := f.p.RefreshStableRoute(context.Background(), endpoint.LeaseRequest{ClusterID: f.c.ResourceID, OwnerID: f.source.ResourceID}); e == nil {
			t.Fatal("read-only primary obtained routing renewal")
		}
		if f.i.GatewayGrant(f.c.ResourceID) != last {
			t.Fatal("read-only evidence published")
		}
	})
}
