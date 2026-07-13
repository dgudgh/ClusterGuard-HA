package agent

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type reconcileVIPStub struct {
	owns           bool
	acquires       int
	releases       int
	acquireErr     error
	statusSequence []reconcileVIPStatus
	statusCalls    int
	events         *[]string
}

type reconcileVIPStatus struct {
	owns bool
	err  error
}

func (vip *reconcileVIPStub) Status(context.Context, ClusterPolicy) (bool, error) {
	if vip.events != nil {
		*vip.events = append(*vip.events, "vip_status")
	}
	if vip.statusCalls < len(vip.statusSequence) {
		result := vip.statusSequence[vip.statusCalls]
		vip.statusCalls++
		return result.owns, result.err
	}
	vip.statusCalls++
	return vip.owns, nil
}
func (vip *reconcileVIPStub) Acquire(context.Context, ClusterPolicy) error {
	if vip.events != nil {
		*vip.events = append(*vip.events, "vip_acquire")
	}
	vip.acquires++
	if vip.acquireErr != nil {
		return vip.acquireErr
	}
	vip.owns = true
	return nil
}
func (vip *reconcileVIPStub) Release(context.Context, ClusterPolicy) error {
	if vip.events != nil {
		*vip.events = append(*vip.events, "vip_release")
	}
	vip.releases++
	vip.owns = false
	return nil
}

type reconcileRoleStub struct {
	readOnly, superReadOnly bool
	persisted               []bool
	statusSequence          []reconcileRoleStatus
	statusCalls             int
	persistWritableErr      error
	events                  *[]string
}

type reconcileRoleStatus struct {
	readOnly      bool
	superReadOnly bool
	err           error
}

func (roles *reconcileRoleStub) Status(context.Context, ClusterPolicy) (bool, bool, error) {
	if roles.events != nil {
		*roles.events = append(*roles.events, "role_status")
	}
	if roles.statusCalls < len(roles.statusSequence) {
		result := roles.statusSequence[roles.statusCalls]
		roles.statusCalls++
		return result.readOnly, result.superReadOnly, result.err
	}
	roles.statusCalls++
	return roles.readOnly, roles.superReadOnly, nil
}
func (roles *reconcileRoleStub) PersistReadOnly(_ context.Context, _ ClusterPolicy, readOnly bool) error {
	if roles.events != nil {
		event := "role_writable"
		if readOnly {
			event = "role_read_only"
		}
		*roles.events = append(*roles.events, event)
	}
	roles.persisted = append(roles.persisted, readOnly)
	if !readOnly && roles.persistWritableErr != nil {
		return roles.persistWritableErr
	}
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

func TestReconcilerFailsClosedWhenLeaderDecisionIsMissing(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{owns: true}
	roles := &reconcileRoleStub{}
	decision := reconcileDecisionStub{err: errors.New("leader unavailable")}
	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate || vip.releases != 1 || len(roles.persisted) != 1 || !roles.persisted[0] {
		t.Fatalf("fail-closed results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
	}
}

func TestReconcilerTreatsControllerDirectedIsolationAsHealthyConvergence(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{owns: true}
	roles := &reconcileRoleStub{}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate,
	}}
	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate || vip.releases != 1 || len(roles.persisted) != 1 || !roles.persisted[0] {
		t.Fatalf("expected isolation results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
	}
}

func TestReconcilerBootstrapsRebootedPrimaryInLeaseAuthorizedOrder(t *testing.T) {
	policy := reconcilePolicy()
	events := []string{}
	vip := &reconcileVIPStub{events: &events}
	roles := &reconcileRoleStub{readOnly: true, superReadOnly: true, events: &events}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileBootstrapPrimary, LeaseID: model.NewResourceID(),
	}}
	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileBootstrapPrimary {
		t.Fatalf("bootstrap results=%+v err=%v", results, err)
	}
	wantEvents := []string{"role_status", "vip_status", "vip_acquire", "role_writable", "role_status", "vip_status"}
	if len(events) != len(wantEvents) {
		t.Fatalf("bootstrap events=%v want=%v", events, wantEvents)
	}
	for index := range wantEvents {
		if events[index] != wantEvents[index] {
			t.Fatalf("bootstrap events=%v want=%v", events, wantEvents)
		}
	}
	if !vip.owns || roles.readOnly || roles.superReadOnly || vip.releases != 0 || len(roles.persisted) != 1 || roles.persisted[0] {
		t.Fatalf("bootstrap vip=%+v roles=%+v", vip, roles)
	}
}

func TestReconcilerBootstrapFailuresAlwaysReleaseVIPAndFenceMySQL(t *testing.T) {
	policy := reconcilePolicy()
	tests := []struct {
		name  string
		vip   *reconcileVIPStub
		roles *reconcileRoleStub
	}{
		{name: "partial initial read only", vip: &reconcileVIPStub{}, roles: &reconcileRoleStub{readOnly: false, superReadOnly: true}},
		{name: "VIP acquire failed", vip: &reconcileVIPStub{acquireErr: errors.New("address conflict")}, roles: &reconcileRoleStub{readOnly: true, superReadOnly: true}},
		{name: "writable transition failed", vip: &reconcileVIPStub{}, roles: &reconcileRoleStub{readOnly: true, superReadOnly: true, persistWritableErr: errors.New("mysql rejected role change")}},
		{name: "role verification failed", vip: &reconcileVIPStub{}, roles: &reconcileRoleStub{
			readOnly: true, superReadOnly: true,
			statusSequence: []reconcileRoleStatus{{readOnly: true, superReadOnly: true}, {readOnly: true, superReadOnly: false}},
		}},
		{name: "VIP verification failed", vip: &reconcileVIPStub{statusSequence: []reconcileVIPStatus{{owns: false}, {err: errors.New("VIP probe failed")}}}, roles: &reconcileRoleStub{readOnly: true, superReadOnly: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := reconcileDecisionStub{response: ReconcileResponse{
				ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileBootstrapPrimary, LeaseID: model.NewResourceID(),
			}}
			results, err := NewReconciler(test.vip, test.roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
			if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
				t.Fatalf("fail-closed results=%+v err=%v", results, err)
			}
			if test.vip.owns || test.vip.releases != 1 || !test.roles.readOnly || !test.roles.superReadOnly || len(test.roles.persisted) == 0 || !test.roles.persisted[len(test.roles.persisted)-1] {
				t.Fatalf("fail-closed vip=%+v roles=%+v", test.vip, test.roles)
			}
		})
	}
}
