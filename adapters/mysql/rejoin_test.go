package mysql

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func formerPrimaryRejoinFixture() adapter.OperationRequest {
	request := switchoverRequestFixture()
	formerPrimary := request.Resolved.Primary
	currentPrimary := request.Resolved.Target
	currentPrimary.Role = model.RolePrimary
	currentPrimary.Replication = model.ReplicationStatus{}
	currentPrimary.EngineMetadata["read_only"] = "false"
	currentPrimary.EngineMetadata["super_read_only"] = "false"
	currentPrimary.EngineMetadata["gtid_executed"] = primaryUUID + ":1-120," + targetUUID + ":1-20"
	currentPrimary.EngineMetadata["gtid_purged"] = primaryUUID + ":1-80"
	formerPrimary.Role = model.RoleUnknown
	formerPrimary.Replication = model.ReplicationStatus{}
	formerPrimary.EngineMetadata["read_only"] = "true"
	formerPrimary.EngineMetadata["super_read_only"] = "true"
	formerPrimary.EngineMetadata["gtid_executed"] = primaryUUID + ":1-100"
	request.Operation.Kind = model.OperationFormerPrimaryRejoin
	request.TargetID = formerPrimary.ResourceID
	request.Resolved.Primary = currentPrimary
	request.Resolved.Target = formerPrimary
	request.Resolved.Snapshot.Instances = []model.DatabaseInstance{currentPrimary, formerPrimary}
	return request
}

func TestFormerPrimaryFastRejoinRequiresSubsetGTID(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if planHasBlockingChecks(checks) || !passedCheck(checks, "former_primary_gtid_subset") {
		t.Fatalf("safe former primary was blocked: %+v", checks)
	}
}

func TestFormerPrimaryFastRejoinAcceptsReachableDegradedFencedNode(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	request.Resolved.Target.Health = model.Health{
		State:   model.HealthDegraded,
		Summary: "MySQL instance is read-only with no replication source",
	}
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !passedCheck(checks, "former_primary_read_only") || planHasBlockingChecks(checks) {
		t.Fatalf("reachable fenced former primary was blocked: %+v", checks)
	}
}

func TestFormerPrimaryWithErrantGTIDRequiresRebuild(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	request.Resolved.Target.EngineMetadata["gtid_executed"] += "," + extraUUID + ":1"
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "former_primary_gtid_subset") || !failedCheck(checks, "rebuild_required") {
		t.Fatalf("errant former primary did not require rebuild: %+v", checks)
	}
}

func TestFormerPrimaryMissingPurgedGTIDRequiresRebuild(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	request.Resolved.Primary.EngineMetadata["gtid_purged"] = primaryUUID + ":1-110"
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !passedCheck(checks, "former_primary_gtid_subset") || !failedCheck(checks, "required_binlog_available") || !failedCheck(checks, "rebuild_required") {
		t.Fatalf("purged former-primary recovery gap did not require rebuild: %+v", checks)
	}
}

func TestFormerPrimaryRejoinRefusesLocalVIP(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	provider := endpointProviderStub{executable: true, checks: []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "VIP has multiple owners"}}}
	checks, err := NewWithEndpointProvider(nil, provider).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "former_primary_endpoint_absent") {
		t.Fatalf("former-primary writer endpoint ownership was not blocked: %+v", checks)
	}
}

func TestFormerPrimaryRejoinAcceptsGenericWriterEndpointEvidence(t *testing.T) {
	checks := []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckPass}}
	if !rejoinEndpointSafe(checks) {
		t.Fatal("generic writer endpoint evidence was not accepted for former-primary rejoin")
	}
}

func TestFormerPrimaryRejoinExecutesAndVerifiesHealthyReplica(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	adapterInstance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
	state := client.nodes[request.Resolved.Target.Hostname]
	if !state.readOnly || !state.superReadOnly || state.sourceUUID != targetUUID || !state.ioRunning || !state.sqlRunning {
		t.Fatalf("former primary state=%+v", state)
	}
}

func TestFormerPrimaryRejoinRetryDoesNotResetHealthyChannel(t *testing.T) {
	request := formerPrimaryRejoinFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	adapterInstance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Progress = &progressCollector{}
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	before := len(client.statements)
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(client.statements) != before {
		t.Fatalf("retry changed the healthy replication channel: before=%d after=%d statements=%v", before, len(client.statements), client.statements)
	}
}

func passedCheck(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckPass {
			return true
		}
	}
	return false
}
