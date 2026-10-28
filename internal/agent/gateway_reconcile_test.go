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
