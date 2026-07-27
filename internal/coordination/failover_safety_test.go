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

type failoverAuthorityStub struct{ err error }

func (stub failoverAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

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
	calls *[]string
	lease endpoint.Lease
	err   error
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
	return stub.lease, nil
}

func (*failoverLeaseStub) Validate(context.Context, endpoint.Lease) error { return nil }
func (*failoverLeaseStub) FinalizeTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
	return lease, nil
}
func (*failoverLeaseStub) RollbackTransition(_ context.Context, lease endpoint.Lease, _ time.Duration) (endpoint.Lease, error) {
	return lease, nil
}
func (*failoverLeaseStub) Release(context.Context, model.ResourceID) error { return nil }

type failoverAgentTransportStub struct {
	calls          *[]string
	requests       []agent.Request
	ownsVIP        bool
	readOnly       bool
	superReadOnly  bool
	serviceRunning bool
	inRecovery     bool
	err            error
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
	if stub.err != nil {
		return agent.Response{}, stub.err
	}
	switch request.Command {
	case agent.CommandSelfIsolate:
		return agent.Response{Status: agent.StatusOK}, nil
	case agent.CommandVIPStatus:
		owns := stub.ownsVIP
		return agent.Response{Status: agent.StatusOK, OwnsVIP: &owns}, nil
	case agent.CommandRoleStatus:
		readOnly, superReadOnly := stub.readOnly, stub.superReadOnly
		return agent.Response{Status: agent.StatusOK, ReadOnly: &readOnly, SuperReadOnly: &superReadOnly}, nil
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
