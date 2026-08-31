package coordination

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type failoverAuthorityStub struct {
	err  error
	term uint64
}

func (stub failoverAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }
func (stub failoverAuthorityStub) LeadershipEpoch() uint64                        { return stub.term }

type failoverInventoryStub struct {
	haEndpoint model.HAEndpoint
	endpoint   model.Endpoint
}

func (stub failoverInventoryStub) HAEndpoints(model.ResourceID) []model.HAEndpoint {
	return []model.HAEndpoint{stub.haEndpoint}
}

func (stub failoverInventoryStub) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	return stub.endpoint, resourceID == stub.endpoint.ResourceID
}

type failoverLeaseStub struct {
	calls        *[]string
	lease        endpoint.Lease
	currentLease *endpoint.Lease
	err          error
	validateErr  error
	currentErr   error
}

func (stub *failoverLeaseStub) Acquire(_ context.Context, request endpoint.LeaseRequest) (endpoint.Lease, error) {
	*stub.calls = append(*stub.calls, "lease")
	if stub.err != nil {
		return endpoint.Lease{}, stub.err
	}
	stub.lease.ClusterID = request.ClusterID
	stub.lease.HAEndpointID = request.HAEndpointID
	stub.lease.OperationID = request.OperationID
	stub.lease.OwnerID = request.OwnerID
	stub.lease.PreviousOwnerID = request.PreviousOwnerID
	return stub.lease, nil
}

func (stub *failoverLeaseStub) Validate(context.Context, endpoint.Lease) error {
	return stub.validateErr
}
func (stub *failoverLeaseStub) Current(_ context.Context, _, _ model.ResourceID) (endpoint.Lease, error) {
	if stub.calls != nil {
		*stub.calls = append(*stub.calls, "current_lease")
	}
	if stub.currentLease != nil {
		return *stub.currentLease, stub.currentErr
	}
	return stub.lease, stub.currentErr
}
func (*failoverLeaseStub) FinalizeTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
	return lease, nil
}
func (*failoverLeaseStub) RollbackTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
	return lease, nil
}
func (*failoverLeaseStub) Release(context.Context, model.ResourceID) error { return nil }

type failoverAgentTransportStub struct {
	calls             *[]string
	requests          []agent.Request
	ownsVIP           bool
	readOnly          bool
	superReadOnly     bool
	serviceRunning    bool
	databaseReachable bool
	restartReadOnly   bool
	persistedReadOnly bool
	durableEvidence   bool
	inRecovery        bool
	err               error
	onSend            func(agent.Request)
}

type failoverExternalFencerStub struct {
	calls     *[]string
	fenced    bool
	fenceErr  error
	statusErr error
}

func (stub *failoverExternalFencerStub) Fence(_ context.Context, _ ExternalFenceRequest) error {
	*stub.calls = append(*stub.calls, "external_fence")
	if stub.fenceErr != nil {
		return stub.fenceErr
	}
	stub.fenced = true
	return nil
}

func (stub *failoverExternalFencerStub) Status(_ context.Context, _ ExternalFenceRequest) (bool, error) {
	*stub.calls = append(*stub.calls, "external_status")
	return stub.fenced, stub.statusErr
}

func (stub *failoverAgentTransportStub) Send(_ context.Context, _ model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	*stub.calls = append(*stub.calls, request.Command)
	stub.requests = append(stub.requests, request)
	if stub.onSend != nil {
		stub.onSend(request)
	}
	if stub.err != nil {
		return agent.Response{}, stub.err
	}
	switch request.Command {
	case agent.CommandSelfIsolate:
		stub.persistedReadOnly = true
		return agent.Response{Status: agent.StatusOK}, nil
	case agent.CommandVIPStatus:
		owns := stub.ownsVIP
		return agent.Response{Status: agent.StatusOK, OwnsVIP: &owns}, nil
	case agent.CommandRoleStatus:
		readOnly, superReadOnly := stub.readOnly, stub.superReadOnly
		if !stub.durableEvidence {
			return agent.Response{Status: agent.StatusOK, ReadOnly: &readOnly, SuperReadOnly: &superReadOnly}, nil
		}
		running, reachable := stub.serviceRunning, stub.databaseReachable
		restartReadOnly, persistedReadOnly := stub.restartReadOnly, stub.persistedReadOnly
		return agent.Response{
			Status: agent.StatusOK, ReadOnly: &readOnly, SuperReadOnly: &superReadOnly,
			ServiceRunning: &running, DatabaseReachable: &reachable,
			RestartReadOnly: &restartReadOnly, PersistedReadOnly: &persistedReadOnly,
		}, nil
	case agent.CommandPostgreSQLStatus:
		running, inRecovery := stub.serviceRunning, stub.inRecovery
		return agent.Response{Status: agent.StatusOK, ServiceRunning: &running, InRecovery: &inRecovery}, nil
	default:
		return agent.Response{Status: agent.StatusBlocked}, nil
	}
}

func TestGuardedFailoverSafetyUsesPostgreSQLServiceStateForIsolation(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	resolved.Cluster.Engine = model.EnginePostgreSQL
	resolved.Primary.Engine = model.EnginePostgreSQL
	resolved.Primary.Port = 5432
	resolved.Target.Engine = model.EnginePostgreSQL
	resolved.Target.Port = 5432
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	transport := &failoverAgentTransportStub{calls: &calls, ownsVIP: false, serviceRunning: false}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, transport, "agent-secret", func() time.Time { return now })
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass {
		t.Fatalf("stopped PostgreSQL primary isolation=%+v", check)
	}
	want := []string{agent.CommandVIPStatus, agent.CommandPostgreSQLStatus}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("PostgreSQL isolation probes=%v want=%v", calls, want)
	}
	for _, request := range transport.requests {
		if request.Engine != model.EnginePostgreSQL {
			t.Fatalf("PostgreSQL safety request engine=%q request=%+v", request.Engine, request)
		}
	}
	transport.serviceRunning = true
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckFail {
		t.Fatalf("running PostgreSQL primary was accepted as isolated: %+v", check)
	}
}

func failoverSafetyFixture(t *testing.T) (adapter.ResolvedOperation, failoverInventoryStub, *FailureWindow, time.Time) {
	t.Helper()
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	operationID := model.NewResourceID()
	endpointID := model.NewResourceID()
	haEndpointID := model.NewResourceID()
	resolved := adapter.ResolvedOperation{
		OperationID: operationID,
		Cluster:     model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL},
		Primary:     model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Engine: model.EngineMySQL, IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary},
		Target:      model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, ClusterID: clusterID, Engine: model.EngineMySQL, IPAddress: "192.0.2.11", Port: 3306, Role: model.RoleReplica},
		PlanDigest:  "sha256:0123456789abcdef",
	}
	resolved.Snapshot = model.TopologySnapshot{ClusterID: clusterID, Instances: []model.DatabaseInstance{resolved.Primary, resolved.Target}}
	inventory := failoverInventoryStub{
		haEndpoint: model.HAEndpoint{ResourceMeta: model.ResourceMeta{ResourceID: haEndpointID}, ClusterID: clusterID, EndpointID: endpointID, Kind: model.EndpointVIP, Interface: "ens160", Prefix: 24},
		endpoint:   model.Endpoint{ResourceMeta: model.ResourceMeta{ResourceID: endpointID}, ClusterID: clusterID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Active: true},
	}
	now := time.Date(2026, time.July, 13, 18, 0, 30, 0, time.UTC)
	window := NewFailureWindow(6, 30*time.Second)
	return resolved, inventory, window, now
}

func recordStableFailure(window *FailureWindow, clusterID model.ResourceID, now time.Time) {
	start := now.Add(-30 * time.Second)
	window.Record(clusterID, true, start)
	for index := 1; index <= 6; index++ {
		window.Record(clusterID, true, start.Add(time.Duration(index)*5*time.Second))
	}
}

func failoverCheckStatus(checks []model.Check, name string) model.CheckStatus {
	for _, check := range checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

func TestGuardedFailoverSafetyRequiresStableFailureAndControllerQuorum(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	calls := []string{}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, &failoverAgentTransportStub{calls: &calls}, "agent-secret", func() time.Time { return now })
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "stable_primary_failure") != model.CheckFail {
		t.Fatalf("unstable failure was accepted: %+v", checks)
	}

	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	provider = NewGuardedFailoverSafety(window, failoverAuthorityStub{err: ErrNoQuorum}, inventory, &failoverLeaseStub{calls: &calls}, &failoverAgentTransportStub{calls: &calls}, "agent-secret", func() time.Time { return now })
	checks = provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "controller_quorum") != model.CheckFail {
		t.Fatalf("missing controller quorum was accepted: %+v", checks)
	}
}

func TestGuardedFailoverSafetyKeepsBoundAutomaticIncidentAfterFreshnessExpires(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	resolved.AutomaticFailureIncidentAt = now.Add(-30 * time.Second)
	late := now.Add(11 * time.Second)
	calls := []string{}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, &failoverAgentTransportStub{calls: &calls}, "agent-secret", func() time.Time { return late })

	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "stable_primary_failure") != model.CheckPass {
		t.Fatalf("bound automatic failure incident expired during precheck and planning: %+v", checks)
	}

	window.Record(resolved.Cluster.ResourceID, false, late)
	checks = provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "stable_primary_failure") != model.CheckFail {
		t.Fatalf("recovered primary did not invalidate the bound incident: %+v", checks)
	}
}

func TestGuardedFailoverSafetyAcquiresLeaseBeforeFencingAndVerifiesIsolation(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)}}
	transport := &failoverAgentTransportStub{calls: &calls, readOnly: true, superReadOnly: true}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now })
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("fence old primary: %v", err)
	}
	check := provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckPass {
		t.Fatalf("verified isolation check=%+v", check)
	}
	want := []string{"lease", agent.CommandSelfIsolate, agent.CommandVIPStatus, agent.CommandRoleStatus}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("fencing order=%v, want %v", calls, want)
	}
}

func TestGuardedFailoverSafetyFencesStoppedMySQLWithReadOnlyRestartDefaults(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)}}
	transport := &failoverAgentTransportStub{
		calls: &calls, ownsVIP: false, serviceRunning: false, databaseReachable: false,
		restartReadOnly: true, persistedReadOnly: false, durableEvidence: true,
	}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now })
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckPass {
		t.Fatalf("stopped MySQL durable fencing path was rejected: %+v", checks)
	}
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("fence stopped MySQL: %v", err)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass || !strings.Contains(check.Message, "restart") {
		t.Fatalf("stopped MySQL durable isolation verification=%+v", check)
	}
}

func TestGuardedFailoverSafetyRejectsStoppedMySQLWithWritableRestartDefaults(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	transport := &failoverAgentTransportStub{
		calls: &calls, ownsVIP: false, serviceRunning: false, databaseReachable: false,
		restartReadOnly: false, persistedReadOnly: false, durableEvidence: true,
	}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, transport, "agent-secret", func() time.Time { return now })
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckFail {
		t.Fatalf("writable MySQL restart defaults were accepted: %+v", checks)
	}
}

func TestGuardedFailoverSafetyLeaseCanBeReusedByEndpointTransitionAuthorization(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := endpoint.NewMemoryLeaseStore(func() time.Time { return now })
	transport := &failoverAgentTransportStub{calls: &calls, readOnly: true, superReadOnly: true}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now })
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("fence old primary: %v", err)
	}

	lease, err := leases.Acquire(context.Background(), endpoint.LeaseRequest{
		ClusterID:       resolved.Cluster.ResourceID,
		HAEndpointID:    inventory.haEndpoint.ResourceID,
		OperationID:     resolved.OperationID,
		OwnerID:         resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID,
		TTL:             30 * time.Second,
	})
	if err != nil {
		t.Fatalf("reuse failover lease for endpoint transition authorization: %v", err)
	}
	if lease.PreviousOwnerID != resolved.Primary.ResourceID {
		t.Fatalf("lease previous owner=%q, want %q", lease.PreviousOwnerID, resolved.Primary.ResourceID)
	}
}

func TestGuardedFailoverSafetyRejectsPartialOldPrimaryIsolation(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	for _, testCase := range []struct {
		name          string
		ownsVIP       bool
		readOnly      bool
		superReadOnly bool
	}{
		{name: "VIP still owned", ownsVIP: true, readOnly: true, superReadOnly: true},
		{name: "read only disabled", readOnly: false, superReadOnly: true},
		{name: "super read only disabled", readOnly: true, superReadOnly: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			calls := []string{}
			provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, &failoverAgentTransportStub{
				calls: &calls, ownsVIP: testCase.ownsVIP, readOnly: testCase.readOnly, superReadOnly: testCase.superReadOnly,
			}, "agent-secret", func() time.Time { return now })
			if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckFail {
				t.Fatalf("partial isolation was accepted: %+v", check)
			}
		})
	}
}

func TestGuardedFailoverSafetyFailsClosedWhenAgentIsUnavailable(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	provider := NewGuardedFailoverSafety(window, failoverAuthorityStub{}, inventory, &failoverLeaseStub{calls: &calls}, &failoverAgentTransportStub{calls: &calls, err: errors.New("unreachable")}, "agent-secret", func() time.Time { return now })
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckFail {
		t.Fatalf("unreachable old primary was accepted as isolated: %+v", check)
	}
}

func TestGuardedFailoverSafetyUsesMajorityLeaseAfterAgentAuthorizationExpires(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)},
	}
	transport := &failoverAgentTransportStub{
		calls: &calls, err: errors.New("old node is unreachable"),
		onSend: func(request agent.Request) {
			if request.Command == agent.CommandSelfIsolate {
				now = now.Add(4 * time.Second)
			}
		},
	}
	waited := time.Duration(0)
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now },
		WithAgentQuorumFencing(15*time.Second),
		withFailoverWaiter(func(_ context.Context, duration time.Duration) error { waited = duration; return nil }),
	)
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckPass {
		t.Fatalf("agent quorum fencing path was rejected: %+v", checks)
	}
	if len(calls) != 0 {
		t.Fatalf("agent-quorum precheck contacted failed primary: %v", calls)
	}
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("agent quorum fence old primary: %v", err)
	}
	if waited != 11*time.Second {
		t.Fatalf("remaining agent quorum grace=%s, want 11s after a 4s isolation attempt", waited)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass || !strings.Contains(check.Message, "majority") {
		t.Fatalf("agent quorum fencing verification=%+v", check)
	}
	if !reflect.DeepEqual(calls, []string{
		"lease", agent.CommandSelfIsolate, "lease", "current_lease", agent.CommandVIPStatus,
		agent.CommandVIPStatus, "current_lease",
	}) {
		t.Fatalf("agent quorum fencing calls=%v", calls)
	}
}

func TestGuardedFailoverSafetyWaitsOnlyForTrackedAuthorizationInCurrentLeadershipTerm(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)},
	}
	tracker := NewAgentAuthorizationTracker()
	tracker.Record(agent.ReconcileResponse{
		ClusterID: resolved.Cluster.ResourceID, InstanceID: resolved.Primary.ResourceID,
		Action: agent.ReconcileKeepVIP, LeaseID: model.NewResourceID(), ValidUntil: now.Add(7 * time.Second),
	}, 11)
	transport := &failoverAgentTransportStub{
		calls: &calls, err: errors.New("old node is unreachable"),
		onSend: func(request agent.Request) {
			if request.Command == agent.CommandSelfIsolate {
				now = now.Add(4 * time.Second)
			}
		},
	}
	waited := time.Duration(0)
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{term: 11}, inventory, leases, transport, "agent-secret", func() time.Time { return now },
		WithAgentQuorumFencing(15*time.Second), WithAgentAuthorizationTracker(tracker),
		withFailoverWaiter(func(_ context.Context, duration time.Duration) error { waited = duration; return nil }),
	)

	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("tracked agent-quorum fence old primary: %v", err)
	}
	if waited != 4*time.Second {
		t.Fatalf("tracked authorization wait=%s, want 4s including one-second expiry margin", waited)
	}
}

func TestGuardedFailoverSafetyFallsBackToFullGraceAfterLeadershipTermChanges(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	tracker := NewAgentAuthorizationTracker()
	tracker.Record(agent.ReconcileResponse{
		ClusterID: resolved.Cluster.ResourceID, InstanceID: resolved.Primary.ResourceID,
		Action: agent.ReconcileKeepVIP, LeaseID: model.NewResourceID(), ValidUntil: now.Add(7 * time.Second),
	}, 10)
	waited := time.Duration(0)
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{term: 11}, inventory,
		&failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)}},
		&failoverAgentTransportStub{calls: &calls, err: errors.New("old node is unreachable")},
		"agent-secret", func() time.Time { return now }, WithAgentQuorumFencing(15*time.Second),
		WithAgentAuthorizationTracker(tracker),
		withFailoverWaiter(func(_ context.Context, duration time.Duration) error { waited = duration; return nil }),
	)

	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("fallback agent-quorum fence old primary: %v", err)
	}
	if waited != 15*time.Second {
		t.Fatalf("leadership-term fallback wait=%s, want 15s", waited)
	}
}

func TestGuardedFailoverSafetyKeepsAdmittedFailureThroughAgentGrace(t *testing.T) {
	resolved, inventory, window, initial := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, initial)
	now := initial
	leases := endpoint.NewMemoryLeaseStore(func() time.Time { return now })
	calls := []string{}
	transport := &failoverAgentTransportStub{calls: &calls, err: errors.New("old node is unreachable")}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now },
		WithAgentQuorumFencing(15*time.Second),
		withFailoverWaiter(func(_ context.Context, duration time.Duration) error {
			now = now.Add(duration)
			return nil
		}),
	)

	// Discovery publication is intentionally held by the workflow while the
	// Agent grace runs. The last observation therefore becomes older than the
	// FailureWindow's normal ten-second publication interval.
	if !window.Stable(resolved.Cluster.ResourceID, now) {
		t.Fatal("fixture must begin with a stable failure")
	}
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("agent-quorum failover rejected its admitted failure during grace: %v", err)
	}
	if window.Stable(resolved.Cluster.ResourceID, now) {
		t.Fatal("failure evidence should be stale after the Agent grace without a discovery publication")
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass {
		t.Fatalf("agent-quorum failover verification=%+v", check)
	}
}

func TestAgentQuorumFencingAuthorizesPostgreSQLAfterWriterSelfIsolationGrace(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	resolved.Cluster.Engine = model.EnginePostgreSQL
	resolved.Primary.Engine = model.EnginePostgreSQL
	resolved.Primary.Port = 5432
	resolved.Target.Engine = model.EnginePostgreSQL
	resolved.Target.Port = 5432
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(time.Minute)},
	}
	waited := time.Duration(0)
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases,
		&failoverAgentTransportStub{calls: &calls, err: errors.New("old PostgreSQL node is unreachable")},
		"agent-secret", func() time.Time { return now }, WithAgentQuorumFencing(15*time.Second),
		withFailoverWaiter(func(_ context.Context, duration time.Duration) error { waited = duration; return nil }),
	)
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckPass {
		t.Fatalf("PostgreSQL Agent quorum fencing path was rejected: %+v", checks)
	}
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("PostgreSQL Agent quorum fence: %v", err)
	}
	if waited != 15*time.Second {
		t.Fatalf("PostgreSQL Agent fencing grace=%s, want 15s", waited)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass {
		t.Fatalf("PostgreSQL Agent quorum verification=%+v", check)
	}
}

func TestGuardedFailoverSafetyRejectsReachableWritableOldPrimaryAfterAgentGrace(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)},
	}
	transport := &failoverAgentTransportStub{calls: &calls, err: errors.New("temporarily unreachable")}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now },
		WithAgentQuorumFencing(15*time.Second),
		withFailoverWaiter(func(context.Context, time.Duration) error {
			transport.err = nil
			transport.ownsVIP = true
			transport.readOnly = false
			transport.superReadOnly = false
			return nil
		}),
	)
	if err := provider.Fence(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "remains reachable") {
		t.Fatalf("reachable writable old primary error=%v", err)
	}
}

func TestGuardedFailoverSafetyRejectsChangedMajorityLease(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)},
	}
	transport := &failoverAgentTransportStub{calls: &calls, err: errors.New("old node is unreachable")}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases, transport, "agent-secret", func() time.Time { return now },
		WithAgentQuorumFencing(15*time.Second),
		withFailoverWaiter(func(context.Context, time.Duration) error {
			changed := leases.lease
			changed.OwnerID = model.NewResourceID()
			leases.currentLease = &changed
			return nil
		}),
	)
	if err := provider.Fence(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "exact failover transition") {
		t.Fatalf("changed majority lease error=%v", err)
	}
}

func TestGuardedFailoverSafetyVerifiesFinalizedMajorityOwnership(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	inventory.haEndpoint.OwnerID = resolved.Target.ResourceID
	inventory.endpoint.InstanceID = resolved.Target.ResourceID
	calls := []string{}
	leases := &failoverLeaseStub{
		calls: &calls,
		lease: endpoint.Lease{
			ResourceID: model.NewResourceID(), ClusterID: resolved.Cluster.ResourceID,
			HAEndpointID: inventory.haEndpoint.ResourceID, OperationID: inventory.haEndpoint.ResourceID,
			OwnerID: resolved.Target.ResourceID, Active: true, ExpiresAt: now.Add(30 * time.Second),
		},
	}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases,
		&failoverAgentTransportStub{calls: &calls, err: errors.New("old node remains unavailable")},
		"agent-secret", func() time.Time { return now }, WithAgentQuorumFencing(15*time.Second),
	)
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass {
		t.Fatalf("finalized majority ownership verification=%+v", check)
	}
}

func TestGuardedFailoverSafetyUsesVerifiedExternalFencingWhenAgentIsUnreachable(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	leases := &failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(30 * time.Second)}}
	external := &failoverExternalFencerStub{calls: &calls}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory, leases,
		&failoverAgentTransportStub{calls: &calls, err: errors.New("host network partition")},
		"agent-secret", func() time.Time { return now }, WithExternalFencer(external),
	)
	checks := provider.Precheck(context.Background(), resolved)
	if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckPass {
		t.Fatalf("reachable external fence path was rejected: %+v", checks)
	}
	calls = nil
	external.calls = &calls
	if err := provider.Fence(context.Background(), resolved); err != nil {
		t.Fatalf("external fence old primary: %v", err)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass || !strings.Contains(check.Message, "external") {
		t.Fatalf("external fencing verification=%+v", check)
	}
	want := []string{"lease", agent.CommandSelfIsolate, "external_fence", "external_status", agent.CommandVIPStatus, "external_status"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("external fencing order=%v want=%v", calls, want)
	}
}

func TestGuardedFailoverSafetyFailsClosedWhenExternalFenceCannotBeVerified(t *testing.T) {
	resolved, inventory, window, now := failoverSafetyFixture(t)
	recordStableFailure(window, resolved.Cluster.ResourceID, now)
	calls := []string{}
	provider := NewGuardedFailoverSafety(
		window, failoverAuthorityStub{}, inventory,
		&failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true}},
		&failoverAgentTransportStub{calls: &calls, err: errors.New("host network partition")},
		"agent-secret", func() time.Time { return now },
		WithExternalFencer(&failoverExternalFencerStub{calls: &calls, statusErr: errors.New("BMC unreachable")}),
	)
	if err := provider.Fence(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "verify external fencing") {
		t.Fatalf("unverified external fence error=%v", err)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckFail {
		t.Fatalf("unverified external fencing was accepted: %+v", check)
	}
}
