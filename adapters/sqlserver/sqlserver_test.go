package sqlserver

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type runnerStub struct {
	executable bool
	endpoint   adapter.Endpoint
	statement  string
	output     string
}

func (stub *runnerStub) Executable(context.Context) bool { return stub.executable }
func (stub *runnerStub) Exec(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, statement string) (string, error) {
	stub.endpoint = endpoint
	stub.statement = statement
	if strings.Contains(statement, "primary_count") {
		return "1|1|1", nil
	}
	if stub.output != "" {
		return stub.output, nil
	}
	return "replica_server_name role_desc synchronization_health_desc\nsql02 PRIMARY HEALTHY\nsql01 SECONDARY HEALTHY", nil
}

func sqlServerOperationRequest(kind model.OperationKind) adapter.OperationRequest {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 1},
		Engine:       model.EngineSQLServer, EngineIdentity: model.EngineIdentity{"group_id": "11111111-1111-1111-1111-111111111111"}, DisplayName: "ag-prod",
	}
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: primaryID, MetadataRevision: 1},
		ClusterID:    clusterID, Engine: model.EngineSQLServer, EngineIdentity: model.EngineIdentity{"group_id": "11111111-1111-1111-1111-111111111111", "replica_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		DisplayName: "sql01", Hostname: "sql01", IPAddress: "192.0.2.21", Port: 1433, Role: model.RolePrimary,
		Health: model.Health{State: model.HealthHealthy}, EngineMetadata: map[string]string{"always_on": "enabled", "availability_group_name": "ag-prod"},
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: targetID, MetadataRevision: 1},
		ClusterID:    clusterID, Engine: model.EngineSQLServer, EngineIdentity: model.EngineIdentity{"group_id": "11111111-1111-1111-1111-111111111111", "replica_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
		DisplayName: "sql02", Hostname: "sql02", IPAddress: "192.0.2.22", Port: 1433, Role: model.RoleReplica,
		Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
		EngineMetadata: map[string]string{"always_on": "enabled", "availability_group_name": "ag-prod", "synchronization_state": "SYNCHRONIZED", "availability_mode": "SYNCHRONOUS_COMMIT"},
	}
	return adapter.OperationRequest{
		Operation: model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID, Engine: model.EngineSQLServer, Kind: kind, RequestedBy: "dba"},
		TargetID:  targetID,
		Resolved: &adapter.ResolvedOperation{
			OperationID: model.NewResourceID(), ObservationToken: "sqlserver-observation",
			Cluster: cluster, Snapshot: model.TopologySnapshot{
				ClusterID: clusterID, Instances: []model.DatabaseInstance{primary, target},
				ObservedAt: time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC),
			},
			Primary: primary, Target: target,
		},
		Credentials: adapter.Credentials{Username: "sa", Password: "secret", Database: "master"},
	}
}

func TestSQLServerAlwaysOnCapabilitiesFailClosedWithoutRunner(t *testing.T) {
	instance := New()
	capabilities := instance.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityPrecheck) || !capabilities.Supports(adapter.CapabilityPlan) {
		t.Fatalf("SQL Server precheck and plan should be available: %+v", capabilities.Features)
	}
	if capabilities.Supports(adapter.CapabilityExecute) {
		t.Fatalf("SQL Server execution must fail closed without sqlcmd runner: %+v", capabilities.Features)
	}
	if _, err := instance.Execute(context.Background(), sqlServerOperationRequest(model.OperationSwitchover)); err == nil || !strings.Contains(err.Error(), adapter.ErrUnsupported.Error()) {
		t.Fatalf("execute without runner error=%v", err)
	}
}

func TestSQLServerAlwaysOnSwitchoverUsesFailoverStatement(t *testing.T) {
	runner := &runnerStub{executable: true}
	instance := NewWithRunner(runner)
	request := sqlServerOperationRequest(model.OperationSwitchover)
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("unexpected blocking check: %+v", check)
		}
	}
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.Mutating || plan.SourceID != request.Resolved.Primary.ResourceID || plan.TargetID != request.TargetID {
		t.Fatalf("bad plan: %+v", plan)
	}
	if plan.ObservationToken != request.Resolved.ObservationToken || plan.Digest == "" ||
		plan.ResourceRevisions[request.Resolved.Cluster.ResourceID] == 0 ||
		plan.ResourceRevisions[request.Resolved.Primary.ResourceID] == 0 ||
		plan.ResourceRevisions[request.Resolved.Target.ResourceID] == 0 {
		t.Fatalf("plan is missing immutable evidence: %+v", plan)
	}
	recomputed, err := sqlServerOperationPlanDigest(plan)
	if err != nil || recomputed != plan.Digest {
		t.Fatalf("plan digest=%q recomputed=%q err=%v", plan.Digest, recomputed, err)
	}
	request.Plan = &plan
	leaseContext := adapter.WithOperationLeaseID(context.Background(), model.NewResourceID())
	execution, err := instance.Execute(leaseContext, request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationRunning || runner.endpoint.Hostname != "sql02" {
		t.Fatalf("unexpected execution=%+v endpoint=%+v", execution, runner.endpoint)
	}
	if runner.statement != "ALTER AVAILABILITY GROUP [ag-prod] FAILOVER" {
		t.Fatalf("unexpected Always On statement: %q", runner.statement)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v", verification, err)
	}
}

func TestSQLServerAlwaysOnRejectsMissingOrChangedDurablePlan(t *testing.T) {
	runner := &runnerStub{executable: true}
	instance := NewWithRunner(runner)
	request := sqlServerOperationRequest(model.OperationSwitchover)
	leaseContext := adapter.WithOperationLeaseID(context.Background(), model.NewResourceID())
	if execution, err := instance.Execute(leaseContext, request); err == nil || execution.Status != model.OperationFailed || runner.statement != "" {
		t.Fatalf("missing plan execution=%+v err=%v statement=%q", execution, err, runner.statement)
	}
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	plan.TargetID = model.NewResourceID()
	request.Plan = &plan
	if execution, err := instance.Execute(leaseContext, request); err == nil || execution.Status != model.OperationFailed || runner.statement != "" {
		t.Fatalf("changed plan execution=%+v err=%v statement=%q", execution, err, runner.statement)
	}
}

func TestSQLServerAlwaysOnRequiresDurableOperationLease(t *testing.T) {
	runner := &runnerStub{executable: true}
	instance := NewWithRunner(runner)
	request := sqlServerOperationRequest(model.OperationSwitchover)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || runner.statement != "" {
		t.Fatalf("lease-less execution=%+v err=%v statement=%q", execution, err, runner.statement)
	}
}

func TestSQLServerAlwaysOnBlocksUnsafeFailoverByDefault(t *testing.T) {
	runner := &runnerStub{executable: true}
	instance := NewWithRunner(runner)
	request := sqlServerOperationRequest(model.OperationFailover)
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasFailedCheck(checks) {
		t.Fatalf("forced failover without data-loss approval should block: %+v", checks)
	}
	execution, err := instance.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute should return blocked result, not error: %v", err)
	}
	if execution.Status != model.OperationBlocked || runner.statement != "" {
		t.Fatalf("unsafe failover was not blocked cleanly: execution=%+v statement=%q", execution, runner.statement)
	}
}

func TestSQLServerAlwaysOnDiscoverAndCandidateAssessment(t *testing.T) {
	output := strings.Join([]string{
		"11111111-1111-1111-1111-111111111111|bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb|ag-prod|sql02|SECONDARY|CONNECTED|HEALTHY|SYNCHRONIZED|SYNCHRONOUS_COMMIT|0|0",
	}, "\n")
	instance := NewWithRunner(&runnerStub{executable: true, output: output})
	discovery, err := instance.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID:   model.NewResourceID(),
		Endpoint:    adapter.Endpoint{Hostname: "sql02", IPAddress: "192.0.2.22", Port: 1433},
		Credentials: adapter.Credentials{Username: "sa", Password: "secret", Database: "master"},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Instance.Role != model.RoleReplica || !discovery.Instance.PromotionEligible || discovery.Instance.EngineIdentity["replica_id"] == "" {
		t.Fatalf("unexpected SQL Server discovery: %+v", discovery.Instance)
	}
	primary := sqlServerOperationRequest(model.OperationSwitchover).Resolved.Primary
	primary.ClusterID = discovery.Instance.ClusterID
	assessments, err := instance.EvaluateCandidates(context.Background(), adapter.CandidateRequest{Primary: primary, Instances: []model.DatabaseInstance{primary, discovery.Instance}})
	if err != nil || len(assessments) != 1 || !assessments[0].Eligible || assessments[0].Rank != 1 {
		t.Fatalf("assessments=%+v err=%v", assessments, err)
	}
	topology, err := instance.Topology(context.Background(), adapter.DiscoverRequest{}, discovery)
	if err != nil || len(topology.Links) != 0 {
		t.Fatalf("local SQL Server probe must defer AG link reconciliation: topology=%+v err=%v", topology, err)
	}
}

func TestSQLServerAsyncReplicaStaysHealthyButIsNotSynchronizedCandidate(t *testing.T) {
	output := "11111111-1111-1111-1111-111111111111|cccccccc-cccc-cccc-cccc-cccccccccccc|ag-prod|sql03|SECONDARY|CONNECTED|HEALTHY|SYNCHRONIZING|ASYNCHRONOUS_COMMIT|8|4"
	instance := NewWithRunner(&runnerStub{executable: true, output: output})
	discovery, err := instance.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID:   model.NewResourceID(),
		Endpoint:    adapter.Endpoint{Hostname: "sql03", IPAddress: "192.0.2.23", Port: 1433},
		Credentials: adapter.Credentials{Username: "monitor", Password: "secret", Database: "master"},
	})
	if err != nil {
		t.Fatalf("discover asynchronous replica: %v", err)
	}
	if discovery.Instance.Health.State != model.HealthHealthy || discovery.Instance.PromotionEligible {
		t.Fatalf("unexpected asynchronous replica state: %+v", discovery.Instance)
	}
	if !strings.Contains(discovery.Instance.Health.Summary, "synchronizing") || discovery.Instance.Replication.LagSeconds != nil {
		t.Fatalf("asynchronous evidence was misrepresented: %+v", discovery.Instance)
	}
	primary := sqlServerOperationRequest(model.OperationSwitchover).Resolved.Primary
	primary.ClusterID = discovery.Instance.ClusterID
	assessments, err := instance.EvaluateCandidates(context.Background(), adapter.CandidateRequest{
		Primary: primary, Instances: []model.DatabaseInstance{primary, discovery.Instance},
	})
	if err != nil || len(assessments) != 1 || assessments[0].Eligible {
		t.Fatalf("assessments=%+v err=%v", assessments, err)
	}
	if assessments[0].Checks[2].Status != model.CheckFail || assessments[0].Checks[3].Status != model.CheckFail {
		t.Fatalf("asynchronous candidate evidence must fail sync checks: %+v", assessments[0].Checks)
	}
}

func TestSQLCmdRunnerPrefersRegisteredIPAddress(t *testing.T) {
	script := t.TempDir() + "/sqlcmd"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\"\n"), 0o700); err != nil {
		t.Fatalf("write sqlcmd stub: %v", err)
	}
	output, err := (SQLCmdRunner{Binary: script}).Exec(
		context.Background(),
		adapter.Endpoint{Hostname: "stale-name", IPAddress: "192.0.2.44", Port: 1433},
		adapter.Credentials{Username: "monitor", Password: "secret", Database: "master"},
		"SELECT 1",
	)
	if err != nil {
		t.Fatalf("execute sqlcmd stub: %v", err)
	}
	if output != "192.0.2.44,1433" {
		t.Fatalf("sqlcmd server=%q, want registered IP address", output)
	}
}

func TestSQLServerAlwaysOnMetricsExposeSynchronizationAndQueues(t *testing.T) {
	output := strings.Join([]string{
		"11111111-1111-1111-1111-111111111111|bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb|ag-prod|sql02|SECONDARY|CONNECTED|HEALTHY|SYNCHRONIZED|SYNCHRONOUS_COMMIT|128|256",
	}, "\n")
	instance := NewWithRunner(&runnerStub{executable: true, output: output})
	samples, err := instance.Metrics(context.Background(), adapter.DiscoverRequest{
		Endpoint:    adapter.Endpoint{Hostname: "sql02", IPAddress: "192.0.2.22", Port: 1433},
		Credentials: adapter.Credentials{Username: "sa", Password: "secret", Database: "master"},
	})
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("unexpected metric sample count: %+v", samples)
	}
	values := samples[0].Values
	want := map[string]float64{
		"always_on_healthy":    1,
		"connected":            1,
		"synchronized":         1,
		"synchronous_commit":   1,
		"log_send_queue_bytes": 128 * 1024,
		"redo_queue_bytes":     256 * 1024,
		"role_primary":         0,
		"role_secondary":       1,
	}
	for name, expected := range want {
		if actual, ok := values[name]; !ok || actual != expected {
			t.Fatalf("%s=%v present=%t want=%v all=%+v", name, actual, ok, expected, values)
		}
	}
}
