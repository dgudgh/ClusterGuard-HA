package endpoint_test

import (
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"context"
	"testing"
	"time"
)

func TestGatewayPostgreSQLUsesNativeWritableEvidenceAndMajority(t *testing.T) {
	f := gatewayFixtureWithEngine(t, 5432, 5433, model.EnginePostgreSQL)
	ctx := context.Background()
	if checks := f.p.Precheck(ctx, f.resolved()); len(checks) != 1 || checks[0].Status != model.CheckPass {
		t.Fatalf("PG gateway precheck rejected: %+v", checks)
	}
	f.bootstrap(t)
	old := f.i.GatewayGrant(f.c.ResourceID)
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	for _, change := range []struct{ name, key, value string }{
		{"recovery", "in_recovery", "true"}, {"read only", "transaction_read_only", "true"}, {"missing role", "in_recovery", ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			f.i.top.Instances[0].EngineMetadata[change.key] = change.value
			_ = keeper.RunOnce(ctx)
			if err := f.p.RefreshStableRoute(ctx, endpoint.LeaseRequest{ClusterID: f.c.ResourceID, HAEndpointID: f.resource.ResourceID, OwnerID: f.source.ResourceID}); err == nil {
				t.Fatal("PG native SQL evidence bypassed on direct route renewal")
			}
			if f.i.GatewayGrant(f.c.ResourceID) != old {
				t.Fatal("unsafe PG SQL state renewed gateway route")
			}
			f.i.top.Instances[0].EngineMetadata[change.key] = "false"
		})
	}
	f.a.blocked.Store(true)
	_ = keeper.RunOnce(ctx)
	if f.i.GatewayGrant(f.c.ResourceID) != old {
		t.Fatal("minority renewed PG route")
	}
	f.a.blocked.Store(false)
	auth, err := f.p.AuthorizeTransition(ctx, f.resolved())
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cancel()
	for _, s := range f.wire.states {
		if s.Active {
			t.Fatal("PG transition skipped withdrawal")
		}
	}
}

func TestGatewayPostgreSQLDoesNotEnableUnadaptedEngines(t *testing.T) {
	f := gatewayFixtureWithEngine(t, 5432, 5433, model.EnginePostgreSQL)
	for _, engine := range []model.Engine{model.EngineOracle, model.EngineSQLServer} {
		resolved := f.resolved()
		resolved.Cluster.Engine = engine
		checks := f.p.Precheck(context.Background(), resolved)
		if len(checks) != 1 || checks[0].Status != model.CheckFail {
			t.Fatalf("unsupported engine %s admitted: %+v", engine, checks)
		}
		if len(f.wire.calls) != 0 {
			t.Fatal("unsupported engine mutated gateway")
		}
	}
}
