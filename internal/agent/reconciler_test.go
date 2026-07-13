package agent

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type reconcileVIPStub struct {
	owns     bool
	acquires int
	releases int
}

func (vip *reconcileVIPStub) Status(context.Context, ClusterPolicy) (bool, error) {
	return vip.owns, nil
}
func (vip *reconcileVIPStub) Acquire(context.Context, ClusterPolicy) error {
	vip.acquires++
	vip.owns = true
	return nil
}
func (vip *reconcileVIPStub) Release(context.Context, ClusterPolicy) error {
	vip.releases++
	vip.owns = false
	return nil
}

type reconcileRoleStub struct {
	readOnly, superReadOnly bool
	persisted               []bool
}

func (roles *reconcileRoleStub) Status(context.Context, ClusterPolicy) (bool, bool, error) {
	return roles.readOnly, roles.superReadOnly, nil
}
func (roles *reconcileRoleStub) PersistReadOnly(_ context.Context, _ ClusterPolicy, readOnly bool) error {
	roles.persisted = append(roles.persisted, readOnly)
	roles.readOnly = readOnly
	roles.superReadOnly = readOnly
	return nil
}

type reconcileDecisionStub struct {
	response ReconcileResponse
	err      error
}

func (client reconcileDecisionStub) Decision(context.Context, ClusterPolicy) (ReconcileResponse, error) {
	return client.response, client.err
}

func reconcilePolicy() ClusterPolicy {
	return ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), VIP: "192.0.2.100", Interface: "ens160", Prefix: 24, MySQLPort: 3306}
}

func TestReconcilerBootstrapsVIPOnlyWithSignedKeepDecisionAndWritableMySQL(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{}
	roles := &reconcileRoleStub{}
	reconciler := NewReconciler(vip, roles, reconcileDecisionStub{response: ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID()}})
	results, err := reconciler.ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileKeepVIP || vip.acquires != 1 || vip.releases != 0 || len(roles.persisted) != 0 {
		t.Fatalf("bootstrap results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
	}

	vip = &reconcileVIPStub{}
	roles = &reconcileRoleStub{readOnly: true, superReadOnly: true}
	reconciler = NewReconciler(vip, roles, reconcileDecisionStub{response: ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID()}})
	if _, err := reconciler.ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy}); err == nil || vip.acquires != 0 {
		t.Fatalf("read-only instance acquired VIP: vip=%+v err=%v", vip, err)
	}
}

func TestReconcilerFailsClosedWhenLeaderDecisionIsMissingOrIsolates(t *testing.T) {
	policy := reconcilePolicy()
	for _, test := range []struct {
		name     string
		decision reconcileDecisionStub
	}{
		{name: "unreachable", decision: reconcileDecisionStub{err: errors.New("leader unavailable")}},
		{name: "isolate", decision: reconcileDecisionStub{response: ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			vip := &reconcileVIPStub{owns: true}
			roles := &reconcileRoleStub{}
			results, err := NewReconciler(vip, roles, test.decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
			if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate || vip.releases != 1 || len(roles.persisted) != 1 || !roles.persisted[0] {
				t.Fatalf("fail-closed results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
			}
		})
	}
}
