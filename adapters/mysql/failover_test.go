package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type failoverSafetyStub struct {
	checks     []model.Check
	fenceCalls int
	fenceErr   error
	verify     model.Check
}

func (safety *failoverSafetyStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return append([]model.Check{}, safety.checks...)
}

func (safety *failoverSafetyStub) Fence(context.Context, adapter.ResolvedOperation) error {
	safety.fenceCalls++
	return safety.fenceErr
}

func (safety *failoverSafetyStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	if safety.verify.Name == "" {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary is isolated"}
	}
	return safety.verify
}

func passingFailoverSafety() *failoverSafetyStub {
	return &failoverSafetyStub{checks: []model.Check{
		{Name: "stable_primary_failure", Status: model.CheckPass, Message: "six consecutive failure samples span 30 seconds"},
		{Name: "controller_quorum", Status: model.CheckPass, Message: "controller majority is healthy"},
		{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary can be isolated before promotion"},
	}}
}

func failoverRequestFixture() adapter.OperationRequest {
	request := threeNodeSwitchoverRequestFixture()
	oldTargetID := request.Resolved.Target.ResourceID
	oldSiblingID := request.Resolved.Snapshot.Instances[2].ResourceID
	stableTargetID := model.ResourceID("00000000-0000-4000-8000-000000000010")
	stableSiblingID := model.ResourceID("00000000-0000-4000-8000-000000000020")
	request.TargetID = stableTargetID
	request.Resolved.Target.ResourceID = stableTargetID
	request.Operation.Kind = model.OperationFailover
	request.Resolved.Primary.Health.State = model.HealthUnhealthy
	request.Resolved.Primary.Health.Summary = "primary is unreachable"
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == oldTargetID {
			request.Resolved.Snapshot.Instances[index].ResourceID = stableTargetID
		}
		if request.Resolved.Snapshot.Instances[index].ResourceID == oldSiblingID {
			request.Resolved.Snapshot.Instances[index].ResourceID = stableSiblingID
		}
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Primary.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Primary
		}
	}
	for index := range request.Resolved.Snapshot.Probes {
		if request.Resolved.Snapshot.Probes[index].InstanceID == oldTargetID {
			request.Resolved.Snapshot.Probes[index].InstanceID = stableTargetID
		}
		if request.Resolved.Snapshot.Probes[index].InstanceID == oldSiblingID {
			request.Resolved.Snapshot.Probes[index].InstanceID = stableSiblingID
		}
		if request.Resolved.Snapshot.Probes[index].InstanceID == request.Resolved.Primary.ResourceID {
			request.Resolved.Snapshot.Probes[index].Health.State = model.HealthUnhealthy
		}
	}
	return request
}

func sourceLossFailoverRequestFixture() adapter.OperationRequest {
	request := failoverRequestFixture()
	request.Resolved.Primary.Health.State = model.HealthUnknown
	for index := range request.Resolved.Snapshot.Instances {
		instance := &request.Resolved.Snapshot.Instances[index]
		if instance.ResourceID == request.Resolved.Primary.ResourceID {
			instance.Health.State = model.HealthUnknown
			continue
		}
		instance.Health.State = model.HealthDegraded
		instance.PromotionEligible = false
		instance.Replication.IOThread = model.ThreadConnecting
		instance.Replication.SQLThread = model.ThreadRunning
		instance.Replication.LagSeconds = nil
		instance.Replication.LastIOError = "Error reconnecting to source"
		if instance.ResourceID == request.Resolved.Target.ResourceID {
			request.Resolved.Target = *instance
		}
	}
	for index := range request.Resolved.Snapshot.Probes {
		probe := &request.Resolved.Snapshot.Probes[index]
		if probe.InstanceID != request.Resolved.Primary.ResourceID {
			probe.Health.State = model.HealthDegraded
		}
	}
	return request
}

func TestFailoverBlocksUnfencedOldPrimary(t *testing.T) {
	request := failoverRequestFixture()
	safety := passingFailoverSafety()
	safety.checks[2].Status = model.CheckFail
	checks, err := NewWithSafetyProviders(nil, passingEndpointProvider(), UnsupportedMaintenanceStore{}, safety).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "old_primary_fenced") {
		t.Fatalf("unfenced old primary was accepted: %+v", checks)
	}
}

func TestFailoverRequiresStableWindowAndControllerQuorum(t *testing.T) {
	for _, name := range []string{"stable_primary_failure", "controller_quorum"} {
		t.Run(name, func(t *testing.T) {
			request := failoverRequestFixture()
			safety := passingFailoverSafety()
			for index := range safety.checks {
				if safety.checks[index].Name == name {
					safety.checks[index].Status = model.CheckFail
				}
			}
			checks, err := NewWithSafetyProviders(nil, passingEndpointProvider(), UnsupportedMaintenanceStore{}, safety).Precheck(context.Background(), request)
			if err != nil {
				t.Fatalf("precheck: %v", err)
			}
			if !failedCheck(checks, name) {
				t.Fatalf("missing blocker %q: %+v", name, checks)
			}
		})
	}
}

func TestFailoverPlansReachableReplicasWhileTheirSourceReconnects(t *testing.T) {
	request := sourceLossFailoverRequestFixture()
	adapterInstance := NewWithSafetyProviders(nil, passingEndpointProvider(), UnsupportedMaintenanceStore{}, passingFailoverSafety())
	checks, err := adapterInstance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck source reconnect: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("safe source reconnect failover was blocked by %+v", check)
		}
	}
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan source reconnect failover: %v", err)
	}
	followerSteps := 0
	for _, step := range plan.Steps {
		if strings.HasPrefix(step.Name, "reparent_failover_follower_") {
			followerSteps++
		}
	}
	if followerSteps != 1 {
		t.Fatalf("source reconnect follower steps=%d, want 1: %+v", followerSteps, plan.Steps)
	}
}

func TestFailoverExecutesWhenSourceIsReconnectingAndReparentsSibling(t *testing.T) {
	request := sourceLossFailoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithSafetyProviders(client, provider, UnsupportedMaintenanceStore{}, passingFailoverSafety())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build source reconnect failover: %v", err)
	}
	request.Plan = &plan
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute source reconnect failover: execution=%+v err=%v", execution, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify source reconnect failover: verification=%+v err=%v", verification, err)
	}
	for _, instance := range request.Resolved.Snapshot.Instances {
		state := client.nodes[instance.Hostname]
		if instance.ResourceID == request.Resolved.Primary.ResourceID {
			continue
		}
		if instance.ResourceID == request.Resolved.Target.ResourceID {
			if state.readOnly || state.superReadOnly || state.sourceUUID != "" {
				t.Fatalf("promoted target state=%+v", state)
			}
			continue
		}
		if !state.readOnly || !state.superReadOnly || state.sourceUUID != strings.ToLower(request.Resolved.Target.EngineIdentity["server_uuid"]) || !state.ioRunning || !state.sqlRunning {
			t.Fatalf("reparented sibling state=%+v", state)
		}
	}
}

func TestFailoverExecutesFenceBeforePromotionAndVerifies(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{}
	authorizedAtPromotion := false
	client.beforeWritable = func() {
		endpointProvider.mu.Lock()
		defer endpointProvider.mu.Unlock()
		authorizedAtPromotion = endpointProvider.authorized
	}
	safety := passingFailoverSafety()
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, safety)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	if safety.fenceCalls != 1 || endpointProvider.owner != request.TargetID {
		t.Fatalf("failover did not fence and transfer endpoint: fence=%d owner=%s", safety.fenceCalls, endpointProvider.owner)
	}
	if endpointProvider.authorizeCalls != 1 || !authorizedAtPromotion {
		t.Fatalf("failover endpoint transition was not authorized before promotion: calls=%d authorized_at_promotion=%t", endpointProvider.authorizeCalls, authorizedAtPromotion)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
}

func TestFailoverDoesNotTransferEndpointUntilTargetWritableIsProven(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	client.ignoreWritable = true
	endpointProvider := &recordingEndpointProvider{}
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, passingFailoverSafety())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("unproven failover promotion execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if endpointProvider.transferCalls != 0 {
		t.Fatalf("failover endpoint transferred before target writable postcondition: calls=%d", endpointProvider.transferCalls)
	}
	target := client.nodes[request.Resolved.Target.Hostname]
	if !target.readOnly || !target.superReadOnly {
		t.Fatalf("unproven failover target was not re-fenced: %+v", target)
	}
}

func TestFailoverAuthorizationFailureLeavesCandidateReplicationAttached(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{authorizeError: errors.New("stable endpoint lease handoff blocked")}
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, passingFailoverSafety())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("authorization failure execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	target := client.nodes[request.Resolved.Target.Hostname]
	if target.sourceUUID == "" || !target.readOnly || !target.superReadOnly {
		t.Fatalf("authorization failure detached or promoted target: %+v", target)
	}
	for _, statement := range client.statements {
		if strings.HasPrefix(statement, request.Resolved.Target.Hostname+" STOP ") || strings.HasPrefix(statement, request.Resolved.Target.Hostname+" RESET ") {
			t.Fatalf("failover target replication mutated before endpoint authorization: %s", statement)
		}
	}
}

func TestFailoverRejectsTargetWhenLowerRiskCandidateExists(t *testing.T) {
	request := failoverRequestFixture()
	lag := int64(5)
	request.Resolved.Target.Replication.LagSeconds = &lag
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.TargetID {
			request.Resolved.Snapshot.Instances[index].Replication.LagSeconds = &lag
		}
	}
	checks, err := NewWithSafetyProviders(nil, passingEndpointProvider(), UnsupportedMaintenanceStore{}, passingFailoverSafety()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "recommended_candidate") {
		t.Fatalf("higher-risk selected target was accepted: %+v", checks)
	}
}

func TestFailoverRetryVerifiesWithoutRepeatingMutations(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	request.Progress = &progressCollector{}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{}
	safety := passingFailoverSafety()
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, safety)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	statements, fenceCalls, transferCalls := len(client.statements), safety.fenceCalls, endpointProvider.transferCalls
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(client.statements) != statements || safety.fenceCalls != fenceCalls || endpointProvider.transferCalls != transferCalls {
		t.Fatalf("retry repeated mutation: sql=%d/%d fence=%d/%d transfer=%d/%d", len(client.statements), statements, safety.fenceCalls, fenceCalls, endpointProvider.transferCalls, transferCalls)
	}
}

func TestFailoverRetryResumesAfterEndpointTransferFailure(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	request.Progress = &progressCollector{}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{transferError: errors.New("endpoint transfer unavailable")}
	safety := passingFailoverSafety()
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, safety)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	first, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || first.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("first execution=%+v err=%v class=%q", first, err, failureClass(err))
	}
	endpointProvider.transferError = nil
	second, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || second.Status != model.OperationRunning {
		t.Fatalf("resume execution=%+v err=%v", second, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("resume verification=%+v err=%v", verification, err)
	}
}
