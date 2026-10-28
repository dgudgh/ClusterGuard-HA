package mysql

import (
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"fmt"
	"testing"
	"time"
)

type gatewaySQLAuthority struct{}

func (gatewaySQLAuthority) RequireMutationAuthority(context.Context) error { return nil }

type gatewaySQLTransport struct {
	states                   []endpoint.GatewayStatus
	sql                      *switchoverSQLClient
	withdrawBeforeFence      bool
	activationAfterPromotion bool
}

func (g *gatewaySQLTransport) Statuses(context.Context, model.ResourceID) ([]endpoint.GatewayStatus, error) {
	return append([]endpoint.GatewayStatus(nil), g.states...), nil
}
func (g *gatewaySQLTransport) Apply(ctx context.Context, grant model.GatewayGrant, validate func(context.Context) error) error {
	if e := validate(ctx); e != nil {
		return e
	}
	if grant.Mode == "withdraw" {
		g.withdrawBeforeFence = !g.sql.primaryReadOnly && !g.sql.primarySuperReadOnly
	}
	if grant.Mode == "activate" {
		if !g.sql.primaryReadOnly || !g.sql.primarySuperReadOnly || g.sql.targetReadOnly || g.sql.targetSuperReadOnly {
			return fmt.Errorf("activation before SQL role convergence")
		}
		g.activationAfterPromotion = true
	}
	for n := range g.states {
		g.states[n].Active = grant.Mode == "activate"
		g.states[n].LastGrant = grant
	}
	return nil
}
func TestNativeMySQLSwitchoverDrivesGatewayWithRealMajorityLease(t *testing.T) {
	request := switchoverRequestFixture()
	r := store.NewMemory()
	resolved := request.Resolved
	c, entries, e := r.CreateClusterWithEndpoints(resolved.Cluster, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: resolved.Primary.Hostname, IPAddress: resolved.Primary.IPAddress, Port: resolved.Primary.Port, Active: true}, {Kind: model.EndpointDatabase, Hostname: resolved.Target.Hostname, IPAddress: resolved.Target.IPAddress, Port: resolved.Target.Port, Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	for n, original := range []model.DatabaseInstance{resolved.Primary, resolved.Target} {
		result, e := r.ReconcileInstance(original)
		if e != nil {
			t.Fatal(e)
		}
		original.ResourceID = result.Instance.ResourceID
		original.ClusterID = c.ResourceID
		entry := entries[n]
		entry.InstanceID = original.ResourceID
		if _, e = r.UpsertEndpoint(entry); e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			resolved.Primary = original
		} else {
			resolved.Target = original
		}
		resolved.Snapshot.Instances[n] = original
		resolved.Snapshot.Probes[n].InstanceID = original.ResourceID
	}
	resolved.OperationID = request.Operation.ResourceID
	request.TargetID = resolved.Target.ResourceID
	resource, _, e := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway, ProviderRef: "test", Hostname: "database.example", Port: 63306, OwnerID: resolved.Primary.ResourceID, Active: true})
	if e != nil {
		t.Fatal(e)
	}
	sql := newSwitchoverSQLClient(request)
	targets := map[string]string{string(resolved.Primary.ResourceID): fmt.Sprintf("%s:%d", resolved.Primary.Hostname, resolved.Primary.Port), string(resolved.Target.ResourceID): fmt.Sprintf("%s:%d", resolved.Target.Hostname, resolved.Target.Port)}
	transport := &gatewaySQLTransport{sql: sql, states: []endpoint.GatewayStatus{{InstanceID: "gateway-a", ClusterID: c.ResourceID, Targets: targets}, {InstanceID: "gateway-b", ClusterID: c.ResourceID, Targets: targets}}}
	leases := coordination.NewLeaseStore(r, gatewaySQLAuthority{}, nil)
	provider := endpoint.NewGatewayProvider(r, leases, map[string]endpoint.GatewayTransport{"test": transport})
	instance := NewWithEndpointProvider(sql, provider)
	plan, e := instance.BuildPlan(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	request.Plan = &plan
	request.Progress = &progressCollector{}
	execution, e := instance.Execute(context.Background(), request)
	if e != nil || execution.Status != model.OperationRunning {
		t.Fatal("execute", execution, e)
	}
	verification, e := instance.Verify(context.Background(), request)
	if e != nil || !verification.Passed {
		t.Fatal("verify", verification, e)
	}
	stable, e := leases.Current(context.Background(), c.ResourceID, resource.ResourceID)
	if e != nil || stable.OwnerID != resolved.Target.ResourceID || stable.OperationID != resource.ResourceID || stable.ExpiresAt.Before(time.Now()) {
		t.Fatal("not a live stable majority lease", stable, e)
	}
	if !transport.withdrawBeforeFence || !transport.activationAfterPromotion {
		t.Fatal("gateway did not follow SQL isolation/promotion ordering")
	}
	if !sql.primaryReadOnly || !sql.primarySuperReadOnly || !sql.primaryReplication || sql.targetReadOnly || sql.targetSuperReadOnly || sql.targetReplication {
		t.Fatal("SQL roles were not safely switched")
	}
}
