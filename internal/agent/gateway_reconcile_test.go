package agent

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"testing"
	"time"
)

func TestGatewayAgentNeverTouchesHostVIPAndFencesOnAuthorizationLoss(t *testing.T) {
	policy := ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), Engine: model.EngineMySQL, EndpointProvider: model.EndpointProviderGateway}
	decision := ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID(), EndpointProvider: model.EndpointProviderGateway}
	vip := &reconcileVIPStub{}
	roles := &reconcileRoleStub{}
	decisions := &reconcileDecisionStub{response: decision}
	r := NewReconciler(vip, roles, decisions)
	if _, e := r.reconcile(context.Background(), policy); e != nil {
		t.Fatal(e)
	}
	if vip.acquires != 0 || vip.releases != 0 || vip.statusCalls != 0 {
		t.Fatal("gateway writer used a host VIP")
	}
	for _, action := range []ReconcileAction{ReconcileTransitionSource, ReconcileTransitionTarget} {
		decisions.response.Action = action
		roles.readOnly = true
		roles.superReadOnly = true
		if _, e := r.reconcile(context.Background(), policy); e != nil {
			t.Fatal(e)
		}
		if !roles.readOnly || len(roles.persisted) > 0 {
			t.Fatal("transition changed SQL state instead of preserving controller authority")
		}
	}
	decisions.err = errors.New("lost controller majority")
	if _, e := r.reconcile(context.Background(), policy); !errors.Is(e, ErrSelfIsolated) {
		t.Fatal("lost authority did not isolate", e)
	}
	if !roles.readOnly || !roles.superReadOnly || len(roles.persisted) == 0 || !roles.persisted[len(roles.persisted)-1] {
		t.Fatal("gateway failed to persist SQL fence")
	}
	if vip.acquires != 0 || vip.releases != 0 || vip.statusCalls != 0 {
		t.Fatal("gateway failure touched local VIP")
	}
	decisions.err = nil
	decisions.response = decision
	decisions.response.EndpointProvider = ""
	roles.readOnly = false
	roles.superReadOnly = false
	if _, e := r.reconcile(context.Background(), policy); !errors.Is(e, ErrSelfIsolated) {
		t.Fatal("legacy VIP grant was accepted by gateway policy", e)
	}
}
func TestGatewayScopeIsSignedAndCannotBeChanged(t *testing.T) {
	now := time.Now().UTC()
	request := ReconcileRequest{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID()}
	response := ReconcileResponse{ClusterID: request.ClusterID, InstanceID: request.InstanceID, Action: ReconcileKeepVIP, Reason: "majority writer", LeaseID: model.NewResourceID(), ControllerID: model.NewResourceID(), EndpointProvider: model.EndpointProviderGateway, ValidUntil: now.Add(10 * time.Second)}
	if e := SignReconcileResponse(&response, "test-secret"); e != nil {
		t.Fatal(e)
	}
	if e := VerifyReconcileResponse(response, request, "test-secret", now); e != nil {
		t.Fatal(e)
	}
	response.EndpointProvider = ""
	if e := VerifyReconcileResponse(response, request, "test-secret", now); e == nil {
		t.Fatal("gateway provider tampering was accepted")
	}
}

type gatewayPostgreSQLController struct {
	*fakePostgreSQLController
	writable  bool
	writerErr error
}

func (c *gatewayPostgreSQLController) Writable(context.Context, ClusterPolicy) (bool, error) {
	return c.writable, c.writerErr
}
func TestGatewayPostgreSQLAgentPreservesHardFencingAndNeverTouchesVIP(t *testing.T) {
	policy := ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), Engine: model.EnginePostgreSQL, EndpointProvider: model.EndpointProviderGateway}
	response := ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID(), EndpointProvider: model.EndpointProviderGateway}
	for _, scenario := range []struct {
		name                        string
		running, recovery, writable bool
		action                      ReconcileAction
		lost                        bool
		wantError                   bool
	}{
		{"writer", true, false, true, ReconcileKeepVIP, false, false},
		{"transition source", false, false, false, ReconcileTransitionSource, false, false},
		{"transition standby", true, true, false, ReconcileTransitionTarget, false, false},
		{"transition promoted", true, false, true, ReconcileTransitionTarget, false, false},
		{"read only writer", true, false, false, ReconcileKeepVIP, false, true},
		{"standby cannot own writer", true, true, false, ReconcileKeepVIP, false, true},
		{"stopped cannot own writer", false, false, false, ReconcileKeepVIP, false, true},
		{"lost writer authority", true, false, true, ReconcileKeepVIP, true, true},
		{"lost standby authority", true, true, false, ReconcileKeepVIP, true, true},
		{"no direct bootstrap promotion", true, true, false, ReconcileBootstrapPrimary, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls []string
			pg := &gatewayPostgreSQLController{fakePostgreSQLController: &fakePostgreSQLController{calls: &calls, running: scenario.running, inRecovery: scenario.recovery}, writable: scenario.writable}
			vip := &reconcileVIPStub{}
			roles := &reconcileRoleStub{}
			decision := response
			decision.Action = scenario.action
			decisions := &reconcileDecisionStub{response: decision}
			if scenario.lost {
				decisions.err = errors.New("no majority")
			}
			r := NewReconciler(vip, roles, decisions, WithPostgreSQLReconcileController(pg))
			_, err := r.reconcile(context.Background(), policy)
			if (err != nil) != scenario.wantError {
				t.Fatalf("error=%v expected=%v", err, scenario.wantError)
			}
			if vip.acquires+vip.releases+vip.statusCalls != 0 {
				t.Fatal("PG gateway touched host VIP")
			}
			if len(roles.persisted) != 0 {
				t.Fatal("PG gateway used MySQL role controller")
			}
			if scenario.wantError && scenario.running && !scenario.recovery && pg.running {
				t.Fatal("unauthorized PG primary not stopped")
			}
			if scenario.lost && scenario.recovery && !pg.running {
				t.Fatal("streaming standby was stopped")
			}
			for _, call := range calls {
				if call == "postgresql_promote" {
					t.Fatal("Agent elected a writer")
				}
			}
		})
	}
}
