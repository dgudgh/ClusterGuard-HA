package postgresql

import (
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type pgGatewayAuthority struct{}

func (pgGatewayAuthority) RequireMutationAuthority(context.Context) error { return nil }

type pgGatewayWire struct {
	states []endpoint.GatewayStatus
	state  *postgresqlExecutionState
}

func (w *pgGatewayWire) Statuses(context.Context, model.ResourceID) ([]endpoint.GatewayStatus, error) {
	return append([]endpoint.GatewayStatus(nil), w.states...), nil
}
func (w *pgGatewayWire) Apply(ctx context.Context, g model.GatewayGrant, validate func(context.Context) error) error {
	if err := validate(ctx); err != nil {
		return err
	}
	if g.Mode == "activate" && (!w.state.stopped || !w.state.promoted) {
		return fmt.Errorf("route activated before source stopped and target promoted")
	}
	w.state.trace = append(w.state.trace, "gateway_"+g.Mode)
	for i := range w.states {
		w.states[i].LastGrant = g
		w.states[i].Active = g.Mode == "activate"
	}
	return nil
}
func TestNativePostgreSQLSwitchoverDrivesGatewayWithRealMajorityLease(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Credentials.Username = "operator"
	request.ReplicationCredentials.Username = "replicator"
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	r := store.NewMemory()
	resolved := request.Resolved
	resolved.Cluster.DisplayName = "pg-gateway"
	c, entries, err := r.CreateClusterWithEndpoints(resolved.Cluster, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: resolved.Primary.Hostname, Port: 5432, Active: true}, {Kind: model.EndpointDatabase, Hostname: resolved.Target.Hostname, Port: 5432, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	resolved.Cluster = c
	for i, original := range []model.DatabaseInstance{resolved.Primary, resolved.Target} {
		original.ClusterID = c.ResourceID
		registered, e := r.ReconcileInstance(original)
		if e != nil {
			t.Fatal(e)
		}
		original = registered.Instance
		entry := entries[i]
		entry.InstanceID = original.ResourceID
		if _, e = r.UpsertEndpoint(entry); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			resolved.Primary = original
		} else {
			resolved.Target = original
		}
		resolved.Snapshot.Instances[i] = original
		resolved.Snapshot.Probes[i].InstanceID = original.ResourceID
	}
	request.TargetID = resolved.Target.ResourceID
	resource, _, err := r.PutHAEndpoint(store.HAEndpointSpec{ClusterID: c.ResourceID, Kind: model.EndpointService, Provider: model.EndpointProviderGateway, ProviderRef: "pg", Hostname: "pg-entry.example", Port: 5432, OwnerID: resolved.Primary.ResourceID, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	state := &postgresqlExecutionState{finalLSN: "0/5000060", followers: map[model.ResourceID]model.ResourceID{}}
	targets := map[string]string{string(resolved.Primary.ResourceID): resolved.Primary.Hostname + ":5432", string(resolved.Target.ResourceID): resolved.Target.Hostname + ":5432"}
	wire := &pgGatewayWire{state: state, states: []endpoint.GatewayStatus{{InstanceID: "pg-a", ClusterID: c.ResourceID, Targets: targets}, {InstanceID: "pg-b", ClusterID: c.ResourceID, Targets: targets}}}
	leases := coordination.NewLeaseStore(r, pgGatewayAuthority{}, nil)
	provider := endpoint.NewGatewayProvider(r, leases, map[string]endpoint.GatewayTransport{"pg": wire})
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, provider, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Plan = &plan
	resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execution=%+v err=%v trace=%v", execution, err, state.trace)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v trace=%v", verification, err, state.trace)
	}
	trace := strings.Join(state.trace, "\n")
	previous := -1
	for _, step := range []string{"gateway_withdraw", "fence_writes:", "stop_source", "verify_source_stopped", "wait_wal", "promote_target", "gateway_activate", "rewind_source"} {
		i := strings.Index(trace, step)
		if i < 0 || i <= previous {
			t.Fatalf("unsafe/missing %s in %s", step, trace)
		}
		previous = i
	}
	lease, err := leases.Current(context.Background(), c.ResourceID, resource.ResourceID)
	if err != nil || lease.OwnerID != resolved.Target.ResourceID || lease.OperationID != resource.ResourceID || !lease.ExpiresAt.After(time.Now()) {
		t.Fatal("PG stable majority lease missing", lease, err)
	}
}
