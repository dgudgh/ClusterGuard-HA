package agent

import (
	"context"
	"errors"
	"strings"
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

type durableReconcileRoleStub struct {
	*reconcileRoleStub
	status    MySQLIsolationStatus
	statusErr error
}

func (roles *durableReconcileRoleStub) PersistReadOnly(ctx context.Context, policy ClusterPolicy, readOnly bool) error {
	if err := roles.reconcileRoleStub.PersistReadOnly(ctx, policy, readOnly); err != nil {
		return err
	}
	roles.status.DatabaseReachable = true
	roles.status.ServiceRunning = true
	roles.status.ReadOnly = readOnly
	roles.status.SuperReadOnly = readOnly
	roles.status.RestartReadOnly = readOnly
	roles.status.PersistedReadOnly = readOnly
	return nil
}

func (roles *durableReconcileRoleStub) IsolationStatus(context.Context, ClusterPolicy) (MySQLIsolationStatus, error) {
	return roles.status, roles.statusErr
}

func (client reconcileDecisionStub) Decision(context.Context, ClusterPolicy) (ReconcileResponse, error) {
	return client.response, client.err
}

func reconcilePolicy() ClusterPolicy {
	return ClusterPolicy{ClusterID: model.NewResourceID(), InstanceID: model.NewResourceID(), VIP: "192.0.2.100", Interface: "ens160", Prefix: 24, MySQLPort: 3306}
}

func reconcilePostgreSQLPolicy() ClusterPolicy {
	policy := reconcilePolicy()
	policy.Engine = model.EnginePostgreSQL
	policy.PostgreSQLPort = 5432
	return policy
}

func TestReconcilerPostgreSQLKeepVIPRequiresRunningPrimary(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	vip := &reconcileVIPStub{}
	roles := &reconcileRoleStub{}
	calls := []string{}
	postgresql := &fakePostgreSQLController{calls: &calls, running: true, inRecovery: false}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID(),
	}}
	results, err := NewReconciler(vip, roles, decision, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err != nil || len(results) != 1 || results[0].Action != ReconcileKeepVIP || vip.acquires != 1 || vip.releases != 0 {
		t.Fatalf("PostgreSQL keep VIP results=%+v vip=%+v err=%v", results, vip, err)
	}
	if len(roles.persisted) != 0 || roles.statusCalls != 0 || len(calls) != 1 || calls[0] != "postgresql_status" {
		t.Fatalf("PostgreSQL reconcile crossed engine boundary: roles=%+v calls=%v", roles, calls)
	}
}

func TestReconcilerPostgreSQLSelfIsolationReleasesVIPAndStopsWriter(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	events := []string{}
	vip := &reconcileVIPStub{owns: true, events: &events}
	roles := &reconcileRoleStub{events: &events}
	postgresql := &fakePostgreSQLController{calls: &events, running: true}
	results, err := NewReconciler(vip, roles, reconcileDecisionStub{err: errors.New("leader unavailable")}, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
		t.Fatalf("PostgreSQL self isolation results=%+v err=%v", results, err)
	}
	if len(events) != 4 || events[0] != "vip_release" || events[1] != "postgresql_status" || events[2] != "postgresql_stop" || events[3] != "postgresql_status" || len(roles.persisted) != 0 {
		t.Fatalf("PostgreSQL reconcile self-isolation did not stop the writer after releasing VIP: events=%v roles=%+v", events, roles)
	}
	if postgresql.running || !strings.Contains(results[0].Message, "writer stopped") {
		t.Fatalf("PostgreSQL self-isolation did not prove the writer stopped: running=%t result=%+v", postgresql.running, results[0])
	}
}

func TestReconcilerPostgreSQLSelfIsolationKeepsStreamingStandbyRunning(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	events := []string{}
	vip := &reconcileVIPStub{events: &events}
	roles := &reconcileRoleStub{events: &events}
	postgresql := &fakePostgreSQLController{calls: &events, running: true, inRecovery: true}
	results, err := NewReconciler(vip, roles, reconcileDecisionStub{err: errors.New("VIP owned by current primary")}, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
		t.Fatalf("PostgreSQL standby isolation results=%+v err=%v", results, err)
	}
	if strings.Contains(strings.Join(events, ","), "postgresql_stop") || len(events) != 2 || events[0] != "vip_release" || events[1] != "postgresql_status" {
		t.Fatalf("healthy PostgreSQL standby was stopped: events=%v", events)
	}
}

func TestReconcilerPostgreSQLSelfIsolationDoesNotStopTransientService(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	events := []string{}
	vip := &reconcileVIPStub{events: &events}
	roles := &reconcileRoleStub{events: &events}
	postgresql := &fakePostgreSQLController{calls: &events, running: false}
	results, err := NewReconciler(vip, roles, reconcileDecisionStub{err: errors.New("local PostgreSQL service is transitioning")}, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
		t.Fatalf("PostgreSQL transient isolation results=%+v err=%v", results, err)
	}
	if strings.Contains(strings.Join(events, ","), "postgresql_stop") || len(events) != 2 || events[0] != "vip_release" || events[1] != "postgresql_status" {
		t.Fatalf("transitioning PostgreSQL service was stopped: events=%v", events)
	}
}

func TestReconcilerPostgreSQLSelfIsolationPreservesStartingStandby(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	events := []string{}
	vip := &reconcileVIPStub{owns: true, events: &events}
	roles := &reconcileRoleStub{events: &events}
	postgresql := &fakePostgreSQLController{
		calls: &events, running: true, statusErr: errors.New("database system is starting up"), standbyIntent: true,
	}
	results, err := NewReconciler(vip, roles, reconcileDecisionStub{err: errors.New("VIP owned by current primary")}, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
		t.Fatalf("PostgreSQL standby startup results=%+v err=%v", results, err)
	}
	if strings.Contains(strings.Join(events, ","), "postgresql_stop") || !postgresql.running {
		t.Fatalf("safe PostgreSQL standby startup was stopped: events=%v", events)
	}
	if len(events) != 3 || events[0] != "vip_release" || events[1] != "postgresql_status" || events[2] != "postgresql_standby_intent" {
		t.Fatalf("PostgreSQL standby startup evidence order=%v", events)
	}
	if vip.owns || !strings.Contains(results[0].Message, "standby startup") {
		t.Fatalf("standby startup was not isolated from the VIP: vip=%+v result=%+v", vip, results[0])
	}
}

func TestReconcilerPostgreSQLTransitionTargetCannotOwnVIPWhileInRecovery(t *testing.T) {
	policy := reconcilePostgreSQLPolicy()
	vip := &reconcileVIPStub{owns: true}
	roles := &reconcileRoleStub{}
	calls := []string{}
	postgresql := &fakePostgreSQLController{calls: &calls, running: true, inRecovery: true}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, LeaseID: model.NewResourceID(),
	}}
	results, err := NewReconciler(vip, roles, decision, WithPostgreSQLReconcileController(postgresql)).ReconcileAll(
		context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy},
	)
	if err != nil || len(results) != 1 || results[0].Action != ReconcileTransitionTarget || vip.releases != 1 || vip.acquires != 0 {
		t.Fatalf("PostgreSQL transition target results=%+v vip=%+v err=%v", results, vip, err)
	}
	if len(roles.persisted) != 0 || strings.Contains(strings.Join(calls, ","), "postgresql_promote") {
		t.Fatalf("PostgreSQL reconciler performed an unauthorized role mutation: roles=%+v calls=%v", roles, calls)
	}
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

func TestReconcilerCommitsWritableRestartStateOnlyAfterStableKeepDecision(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{}
	base := &reconcileRoleStub{readOnly: true, superReadOnly: true}
	roles := &durableReconcileRoleStub{
		reconcileRoleStub: base,
		status: MySQLIsolationStatus{
			DatabaseReachable: true, ServiceRunning: true,
			RestartReadOnly: true, PersistedReadOnly: true,
		},
	}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID(),
	}}

	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileKeepVIP {
		t.Fatalf("stable keep results=%+v err=%v", results, err)
	}
	if len(base.persisted) != 1 || base.persisted[0] || !vip.owns || vip.acquires != 1 {
		t.Fatalf("writable restart state did not converge before VIP acquisition: roles=%+v vip=%+v", base, vip)
	}
	if base.statusCalls == 0 {
		t.Fatal("runtime writability was not verified after clearing the restart fence")
	}
}

func TestReconcilerSelfIsolatesWhenWritableRestartStateCannotBeCommitted(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{owns: true}
	base := &reconcileRoleStub{persistWritableErr: errors.New("durable write failed")}
	roles := &durableReconcileRoleStub{
		reconcileRoleStub: base,
		status: MySQLIsolationStatus{
			DatabaseReachable: true, ServiceRunning: true,
			RestartReadOnly: true, PersistedReadOnly: true,
		},
	}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, LeaseID: model.NewResourceID(),
	}}

	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err == nil || len(results) != 1 || results[0].Action != ReconcileSelfIsolate {
		t.Fatalf("durable failure results=%+v err=%v", results, err)
	}
	if vip.owns || vip.releases != 1 || len(base.persisted) != 2 || base.persisted[0] || !base.persisted[1] || !base.readOnly || !base.superReadOnly {
		t.Fatalf("durable failure did not fail closed: roles=%+v vip=%+v", base, vip)
	}
}

func TestReconcilerHoldsPreparedTransitionTargetWithoutRewritingReadOnlyState(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{}
	roles := &reconcileRoleStub{readOnly: true, superReadOnly: true}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, LeaseID: model.NewResourceID(),
	}}
	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileTransitionTarget {
		t.Fatalf("transition hold results=%+v err=%v", results, err)
	}
	if vip.acquires != 0 || vip.releases != 0 || vip.statusCalls != 1 || len(roles.persisted) != 0 {
		t.Fatalf("transition hold mutated state: vip=%+v roles=%+v", vip, roles)
	}
}

func TestReconcilerHoldsControlledTransitionSourceWithoutMutatingRoleOrVIP(t *testing.T) {
	for _, state := range []struct {
		name          string
		readOnly      bool
		superReadOnly bool
		ownsVIP       bool
	}{
		{name: "writable_source_with_vip", ownsVIP: true},
		{name: "fenced_source_with_vip", readOnly: true, superReadOnly: true, ownsVIP: true},
		{name: "fenced_source_after_vip_release", readOnly: true, superReadOnly: true},
	} {
		t.Run(state.name, func(t *testing.T) {
			policy := reconcilePolicy()
			vip := &reconcileVIPStub{owns: state.ownsVIP}
			roles := &reconcileRoleStub{readOnly: state.readOnly, superReadOnly: state.superReadOnly}
			decision := reconcileDecisionStub{response: ReconcileResponse{
				ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionSource, LeaseID: model.NewResourceID(),
			}}
			results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
			if err != nil || len(results) != 1 || results[0].Action != ReconcileTransitionSource {
				t.Fatalf("transition source results=%+v err=%v", results, err)
			}
			if vip.acquires != 0 || vip.releases != 0 || len(roles.persisted) != 0 {
				t.Fatalf("transition source mutated state: vip=%+v roles=%+v", vip, roles)
			}
		})
	}
}

func TestReconcilerTransitionTargetDoesNotPreemptControlledVIPTransfer(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{}
	roles := &reconcileRoleStub{}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, LeaseID: model.NewResourceID(),
	}}
	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileTransitionTarget || vip.acquires != 0 || vip.releases != 0 || len(roles.persisted) != 0 {
		t.Fatalf("promoted transition results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
	}
}

func TestReconcilerDoesNotInterruptLeaseAuthorizedPartialReadOnlyTransition(t *testing.T) {
	for _, state := range []struct {
		name          string
		readOnly      bool
		superReadOnly bool
	}{
		{name: "read_only_remains_on", readOnly: true, superReadOnly: false},
		{name: "super_read_only_remains_on", readOnly: false, superReadOnly: true},
	} {
		t.Run(state.name, func(t *testing.T) {
			policy := reconcilePolicy()
			vip := &reconcileVIPStub{owns: true}
			roles := &reconcileRoleStub{readOnly: state.readOnly, superReadOnly: state.superReadOnly}
			decision := reconcileDecisionStub{response: ReconcileResponse{
				ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, LeaseID: model.NewResourceID(),
			}}
			results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
			if err != nil || len(results) != 1 || results[0].Action != ReconcileTransitionTarget || vip.releases != 1 || len(roles.persisted) != 0 {
				t.Fatalf("partial transition results=%+v vip=%+v roles=%+v err=%v", results, vip, roles, err)
			}
		})
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
	wantEvents := []string{"role_status", "vip_status", "role_writable", "role_status", "vip_acquire", "vip_status"}
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

func TestReconcilerBootstrapRemovesStaleVIPBeforeMakingMySQLWritable(t *testing.T) {
	policy := reconcilePolicy()
	events := []string{}
	vip := &reconcileVIPStub{owns: true, events: &events}
	roles := &reconcileRoleStub{readOnly: true, superReadOnly: true, events: &events}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileBootstrapPrimary, LeaseID: model.NewResourceID(),
	}}

	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileBootstrapPrimary {
		t.Fatalf("bootstrap results=%+v err=%v", results, err)
	}
	wantEvents := []string{"role_status", "vip_status", "vip_release", "role_writable", "role_status", "vip_acquire", "vip_status"}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("bootstrap events=%v want=%v", events, wantEvents)
	}
	if !vip.owns || vip.releases != 1 || vip.acquires != 1 || roles.readOnly || roles.superReadOnly {
		t.Fatalf("bootstrap did not converge safely: vip=%+v roles=%+v", vip, roles)
	}
}

func TestReconcilerTreatsRepeatedBootstrapDecisionAsAlreadyConverged(t *testing.T) {
	policy := reconcilePolicy()
	vip := &reconcileVIPStub{owns: true}
	roles := &reconcileRoleStub{}
	decision := reconcileDecisionStub{response: ReconcileResponse{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileBootstrapPrimary, LeaseID: model.NewResourceID(),
	}}

	results, err := NewReconciler(vip, roles, decision).ReconcileAll(context.Background(), map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy})
	if err != nil || len(results) != 1 || results[0].Action != ReconcileKeepVIP {
		t.Fatalf("repeated bootstrap results=%+v err=%v", results, err)
	}
	if vip.acquires != 0 || vip.releases != 0 || len(roles.persisted) != 0 {
		t.Fatalf("already-converged bootstrap mutated state: vip=%+v roles=%+v", vip, roles)
	}
}

func TestReconcilerBootstrapFailuresAlwaysReleaseVIPAndFenceMySQL(t *testing.T) {
	policy := reconcilePolicy()
	tests := []struct {
		name  string
		vip   *reconcileVIPStub
		roles *reconcileRoleStub
	}{
		{name: "writable without VIP", vip: &reconcileVIPStub{}, roles: &reconcileRoleStub{}},
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
