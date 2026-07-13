package mysql

import (
	"context"
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

func TestFailoverExecutesFenceBeforePromotionAndVerifies(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{}
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
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
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
