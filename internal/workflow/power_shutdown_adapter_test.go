package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func seedPowerCluster(t *testing.T, repository *store.Repository, name string) model.DatabaseCluster {
	t.Helper()
	return seedPowerClusterEngine(t, repository, name, model.EngineMySQL, "mysql", 3306)
}

// seedPowerClusterEngine seeds a two-instance cluster (primary prefix-a,
// replica prefix-b) of the given engine for power lifecycle tests.
func seedPowerClusterEngine(t *testing.T, repository *store.Repository, name string, engine model.Engine, prefix string, port int) model.DatabaseCluster {
	t.Helper()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: engine, DisplayName: name})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	for _, suffix := range []string{"a", "b"} {
		// Native identity is engine-specific: MySQL carries server_uuid,
		// PostgreSQL requires a valid resource_id + numeric system_identifier.
		identity := model.EngineIdentity{"server_uuid": "power-" + name + "-" + suffix}
		if engine == model.EnginePostgreSQL {
			// system_identifier must parse as a uint64; a shared cluster id is
			// realistic (both nodes belong to the same PostgreSQL cluster).
			identity = model.EngineIdentity{
				"resource_id":       string(model.NewResourceID()),
				"system_identifier": "7000000000000000",
			}
		}
		if _, err := repository.ReconcileInstance(model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: engine,
			EngineIdentity: identity,
			DisplayName:    prefix + "-" + suffix, Hostname: prefix + "-" + suffix,
			IPAddress: "192.0.2.1" + suffix, Port: port,
		}); err != nil {
			t.Fatalf("create instance %s: %v", suffix, err)
		}
	}
	return cluster
}

// createPlannedPowerOperation creates an active power operation and walks it
// to shutdown_planned, ready for workflow execution.
func createPlannedPowerOperation(t *testing.T, repository *store.Repository, clusterID model.ResourceID) model.PowerOperation {
	t.Helper()
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID:     clusterID,
		OperationType: model.PowerService,
		Mode:          "lab-test",
		RequestedBy:   "tester",
		AutoRecovery:  true,
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	return operation
}

func newPowerTestAdapter(t *testing.T) (*store.Repository, *PowerShutdownAdapter, model.DatabaseCluster) {
	t.Helper()
	repository := store.NewMemory()
	cluster := seedPowerCluster(t, repository, "adapter")
	adapter := NewPowerShutdownAdapter(repository, nil, "")
	return repository, adapter, cluster
}

// fakePowerTransport records every agent request and instance it was sent to,
// answering from per-command maps so tests can script failures.
type fakePowerTransport struct {
	responses map[string]agent.Response
	failures  map[string]error
	requests  []agent.Request
	instances []model.DatabaseInstance
}

func (transport *fakePowerTransport) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.requests = append(transport.requests, request)
	transport.instances = append(transport.instances, instance)
	if failure := transport.failures[request.Command]; failure != nil {
		return agent.Response{}, failure
	}
	return transport.responses[request.Command], nil
}

func powerOKResponse() agent.Response {
	return agent.Response{Status: agent.StatusOK, Message: "ok"}
}

// resolvedPowerTopology builds the frozen operation context for the seeded
// two-instance cluster, with mysql-a as primary.
func resolvedPowerTopology(t *testing.T, repository *store.Repository, cluster model.DatabaseCluster) (*adapter.ResolvedOperation, model.DatabaseInstance) {
	t.Helper()
	return resolvedPowerTopologyFor(t, repository, cluster, "mysql")
}

// resolvedPowerTopologyFor is the engine-generic form: primary is prefix-a.
func resolvedPowerTopologyFor(t *testing.T, repository *store.Repository, cluster model.DatabaseCluster, prefix string) (*adapter.ResolvedOperation, model.DatabaseInstance) {
	t.Helper()
	instances := repository.Instances(cluster.ResourceID)
	primary := model.DatabaseInstance{}
	for _, instance := range instances {
		if instance.DisplayName == prefix+"-a" {
			primary = instance
		}
	}
	return &adapter.ResolvedOperation{
		Cluster:          cluster,
		Snapshot:         model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: instances},
		Primary:          primary,
		ObservationToken: "obs-token",
		PlanDigest:       "sha256:" + strings.Repeat("a", 64),
	}, primary
}

func transportCommandCount(transport *fakePowerTransport, command string) int {
	count := 0
	for _, request := range transport.requests {
		if request.Command == command {
			count++
		}
	}
	return count
}

func powerRequest(clusterID model.ResourceID, operationID model.ResourceID) adapter.OperationRequest {
	return adapter.OperationRequest{
		Operation: model.Operation{
			ResourceMeta: model.ResourceMeta{ResourceID: operationID},
			ClusterID:    clusterID,
			Kind:         model.OperationPowerShutdown,
			RequestedBy:  "tester",
		},
	}
}

func TestPowerShutdownAdapterEngineAndCapabilities(t *testing.T) {
	_, shutdown, _ := newPowerTestAdapter(t)
	if shutdown.Engine() != adapter.WildcardEngine {
		t.Fatalf("engine=%s, want wildcard", shutdown.Engine())
	}
	capabilities := shutdown.Capabilities(context.Background())
	for _, capability := range []adapter.Capability{adapter.CapabilityPrecheck, adapter.CapabilityPlan} {
		if !capabilities.Supports(capability) {
			t.Errorf("capability %s should be supported", capability)
		}
	}
	for _, capability := range []adapter.Capability{adapter.CapabilityExecute, adapter.CapabilityVerify} {
		if capabilities.Supports(capability) {
			t.Errorf("capability %s must be unavailable without an agent transport", capability)
		}
	}
	for _, capability := range []adapter.Capability{adapter.CapabilityDiscover, adapter.CapabilityNodeSync, adapter.CapabilityMetrics} {
		if capabilities.Supports(capability) {
			t.Errorf("capability %s should be unsupported", capability)
		}
	}
}

func TestPowerShutdownAdapterPrecheckRequiresShutdownPlanned(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	request := powerRequest(cluster.ResourceID, model.NewResourceID())

	// No active power operation blocks.
	checks, err := shutdown.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasBlockingCheck(checks) {
		t.Fatal("expected a blocking check without an active power operation")
	}

	// A fresh operation in normal state blocks too.
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerService, RequestedBy: "tester",
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	if checks, err = shutdown.Precheck(context.Background(), request); err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasBlockingCheck(checks) {
		t.Fatal("expected a blocking check in normal state")
	}

	// A planned operation still blocks when no signed Agent transport is wired.
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	if checks, err = shutdown.Precheck(context.Background(), request); err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasBlockingCheck(checks) {
		t.Fatalf("expected Agent readiness to block shutdown, got %+v", checks)
	}

	shutdown.transport = &fakePowerTransport{}
	shutdown.secret = "agent-secret"
	if checks, err = shutdown.Precheck(context.Background(), request); err != nil {
		t.Fatalf("precheck with Agent: %v", err)
	}
	if hasBlockingCheck(checks) {
		t.Fatalf("expected a passing check with a signed Agent transport, got %+v", checks)
	}
}

func TestPowerShutdownAdapterBuildPlan(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	primary := model.DatabaseInstance{}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if instance.DisplayName == "mysql-a" {
			primary = instance
		}
	}
	request.TargetID = primary.ResourceID
	request.Resolved = &adapter.ResolvedOperation{
		Cluster: cluster, Primary: primary, ObservationToken: "obs-token",
		Snapshot: model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: repository.Instances(cluster.ResourceID)},
	}
	plan, err := shutdown.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if len(plan.Steps) != 7 {
		t.Fatalf("expected 7 plan steps, got %d", len(plan.Steps))
	}
	for _, step := range plan.Steps {
		if !step.Mutating {
			t.Errorf("step %s must be marked mutating", step.Name)
		}
	}
	if plan.Steps[0].Name != "freeze_recovery" || plan.Steps[1].Name != "set_maintenance" {
		t.Fatalf("unexpected first steps: %+v", plan.Steps[:2])
	}
	if plan.Steps[4].Name != "release_vip_and_isolate" || plan.Steps[5].Name != "stop_replicas" || plan.Steps[6].Name != "stop_primary" {
		t.Fatalf("unexpected shutdown steps: %+v", plan.Steps[4:])
	}
	if plan.OperationID != request.Operation.ResourceID || plan.ClusterID != cluster.ResourceID ||
		plan.SourceID != primary.ResourceID || plan.TargetID != primary.ResourceID {
		t.Fatalf("plan linkage fields missing: %+v", plan)
	}
	if plan.ObservationToken != "obs-token" || plan.Digest == "" || len(plan.ResourceRevisions) == 0 || !plan.Mutating {
		t.Fatalf("plan decoration incomplete: %+v", plan)
	}
	// The plan digest must be deterministic across calls.
	again, err := shutdown.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if again.Digest != plan.Digest {
		t.Fatalf("plan digest is not deterministic: %s != %s", again.Digest, plan.Digest)
	}
}

func TestPowerShutdownAdapterExecuteFailsClosedWithoutAgent(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	execution, err := shutdown.Execute(context.Background(), request)
	if err == nil {
		t.Fatalf("execute without Agent must fail, got %+v", execution)
	}
	if execution.Status != model.OperationFailed || !strings.Contains(execution.Message, "Agent") {
		t.Fatalf("execution=%+v, want explicit Agent failure", execution)
	}

	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("protections must not be applied without an Agent: frozen=%t err=%v", frozen, err)
	}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if instance.Maintenance {
			t.Fatalf("instance %s unexpectedly entered maintenance", instance.ResourceID)
		}
	}
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerShutdownPlanned {
		t.Fatalf("expected operation to remain shutdown_planned, got found=%t state=%s", found, operation.State)
	}
}

func TestPowerShutdownAdapterExecuteFailsClosedWithoutOperationLease(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	transport := &fakePowerTransport{responses: map[string]agent.Response{}}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	execution, err := shutdown.Execute(context.Background(), request)
	if err == nil || !strings.Contains(execution.Message, "operation lock lease") {
		t.Fatalf("execute without operation lease must fail closed, got execution=%+v err=%v", execution, err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("agent received %d requests before lease validation", len(transport.requests))
	}
	frozen, freezeErr := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if freezeErr != nil || frozen {
		t.Fatalf("protections changed before lease validation: frozen=%t err=%v", frozen, freezeErr)
	}
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerShutdownPlanned {
		t.Fatalf("state=%s found=%t, want shutdown_planned", operation.State, found)
	}
}

func TestPowerShutdownAdapterExecuteRejectsUnplannedState(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerService, RequestedBy: "tester",
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	_ = operation
	execution, err := shutdown.Execute(context.Background(), powerRequest(cluster.ResourceID, model.NewResourceID()))
	if err == nil {
		t.Fatalf("expected error for normal state, got %+v", execution)
	}
	if execution.Status != model.OperationFailed {
		t.Fatalf("expected failed execution, got %s", execution.Status)
	}
	// Protections must NOT have been applied.
	frozen, _ := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if frozen {
		t.Fatal("recovery freeze must not be applied on rejected execution")
	}
}

func TestPowerShutdownAdapterVerify(t *testing.T) {
	_, shutdown, cluster := newPowerTestAdapter(t)

	// Before execution nothing is protected → verify fails.
	verification, err := shutdown.Verify(context.Background(), powerRequest(cluster.ResourceID, model.NewResourceID()))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verification.Passed {
		t.Fatal("verify must fail before protections are applied")
	}

	if !hasBlockingCheck(verification.Checks) {
		t.Fatalf("verify without Agent must contain a blocking Agent check: %+v", verification.Checks)
	}
}

func TestPowerShutdownAdapterExecuteRunsAgentShutdown(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	transport := &fakePowerTransport{responses: map[string]agent.Response{
		agent.CommandPowerPrepare:     powerOKResponse(),
		agent.CommandPersistRole:      powerOKResponse(),
		agent.CommandSelfIsolate:      powerOKResponse(),
		agent.CommandMySQLServiceStop: powerOKResponse(),
	}}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	leaseID := model.NewResourceID()
	execution, err := shutdown.Execute(adapter.WithOperationLeaseID(context.Background(), leaseID), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationSucceeded {
		t.Fatalf("status=%s, want succeeded", execution.Status)
	}

	// persist_read_only on both instances (replicas + primary).
	if count := transportCommandCount(transport, agent.CommandPersistRole); count != 2 {
		t.Fatalf("persist_role sends=%d, want 2", count)
	}
	if count := transportCommandCount(transport, agent.CommandPowerPrepare); count != 2 {
		t.Fatalf("power_prepare sends=%d, want 2", count)
	}
	if count := transportCommandCount(transport, agent.CommandSelfIsolate); count != 2 {
		t.Fatalf("self_isolate sends=%d, want 2", count)
	}
	for _, request := range transport.requests {
		if request.Command == agent.CommandPowerPrepare && request.PowerSnapshot == nil {
			t.Fatal("power_prepare request is missing the recovery snapshot")
		}
	}
	// stop on the replica, then the primary.
	if count := transportCommandCount(transport, agent.CommandMySQLServiceStop); count != 2 {
		t.Fatalf("service stop sends=%d, want 2", count)
	}
	stopIndices := []int{}
	for index, request := range transport.requests {
		if request.Command == agent.CommandMySQLServiceStop {
			stopIndices = append(stopIndices, index)
		}
	}
	if len(stopIndices) == 2 && transport.instances[stopIndices[0]].DisplayName != "mysql-b" {
		t.Fatalf("first stop went to %s, want replica mysql-b", transport.instances[stopIndices[0]].DisplayName)
	}
	if len(stopIndices) == 2 && transport.instances[stopIndices[1]].DisplayName != "mysql-a" {
		t.Fatalf("second stop went to %s, want primary mysql-a", transport.instances[stopIndices[1]].DisplayName)
	}

	// Every agent request is signed and scoped to the operation and plan.
	for _, request := range transport.requests {
		if request.Signature == "" || !model.ValidResourceID(request.OperationID) || request.LeaseID != leaseID || !strings.HasPrefix(request.PlanDigest, "sha256:") {
			t.Fatalf("unsigned or unscoped agent request: %+v", request)
		}
	}

	// The power state machine advanced to power_off with protections active.
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerPoweredOff {
		t.Fatalf("state=%s found=%t, want power_off", operation.State, found)
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze after execute: frozen=%t err=%v", frozen, err)
	}
}

func TestPowerShutdownAdapterExecutePoweroffModePowersOffNodes(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerPowerOff, RequestedBy: "tester",
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	transport := &fakePowerTransport{responses: map[string]agent.Response{
		agent.CommandPowerPrepare:     powerOKResponse(),
		agent.CommandPersistRole:      powerOKResponse(),
		agent.CommandSelfIsolate:      powerOKResponse(),
		agent.CommandMySQLServiceStop: powerOKResponse(),
		agent.CommandNodePoweroff:     powerOKResponse(),
	}}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	execution, err := shutdown.Execute(adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationSucceeded {
		t.Fatalf("status=%s, want succeeded", execution.Status)
	}
	if count := transportCommandCount(transport, agent.CommandNodePoweroff); count != 2 {
		t.Fatalf("node poweroff sends=%d, want 2", count)
	}
	// The host poweroff only happens after both services are stopped.
	firstPoweroff := -1
	lastStop := -1
	for index, request := range transport.requests {
		if request.Command == agent.CommandNodePoweroff && firstPoweroff < 0 {
			firstPoweroff = index
		}
		if request.Command == agent.CommandMySQLServiceStop {
			lastStop = index
		}
	}
	if firstPoweroff < 0 || firstPoweroff <= lastStop {
		t.Fatalf("poweroff order: firstPoweroff=%d lastStop=%d, want poweroff after stops", firstPoweroff, lastStop)
	}
}

func TestPowerShutdownAdapterExecuteAgentFailureKeepsProtections(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	transport := &fakePowerTransport{
		responses: map[string]agent.Response{
			agent.CommandPersistRole:  powerOKResponse(),
			agent.CommandPowerPrepare: powerOKResponse(),
			agent.CommandSelfIsolate:  powerOKResponse(),
		},
		failures: map[string]error{agent.CommandMySQLServiceStop: errors.New("ssh: connection refused")},
	}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	execution, err := shutdown.Execute(adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()), request)
	if err == nil {
		t.Fatalf("expected execute failure, got %+v", execution)
	}
	if execution.Status != model.OperationFailed {
		t.Fatalf("status=%s, want failed", execution.Status)
	}
	if !strings.Contains(execution.Message, "stop MySQL") {
		t.Fatalf("failure message=%q, want stop MySQL context", execution.Message)
	}
	// Fail-closed: protections stay and the state machine pauses in
	// shutting_down for the operator to fail or retry.
	frozen, _ := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if !frozen {
		t.Fatal("recovery freeze must remain active after agent failure")
	}
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerShuttingDown {
		t.Fatalf("state=%s found=%t, want shutting_down", operation.State, found)
	}
}

func TestPowerShutdownAdapterVIPIsolationFailureStopsBeforeServiceShutdown(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	transport := &fakePowerTransport{
		responses: map[string]agent.Response{
			agent.CommandPersistRole:  powerOKResponse(),
			agent.CommandPowerPrepare: powerOKResponse(),
		},
		failures: map[string]error{agent.CommandSelfIsolate: errors.New("ip command failed")},
	}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	execution, err := shutdown.Execute(adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()), request)
	if err == nil {
		t.Fatalf("expected isolation failure, got %+v", execution)
	}
	if execution.Status != model.OperationFailed || !strings.Contains(execution.Message, "isolate") {
		t.Fatalf("execution=%+v, want isolation failure", execution)
	}
	if count := transportCommandCount(transport, agent.CommandMySQLServiceStop); count != 0 {
		t.Fatalf("service stop sends=%d, want 0 after isolation failure", count)
	}
	if count := transportCommandCount(transport, agent.CommandSelfIsolate); count != 1 {
		t.Fatalf("self_isolate sends=%d, want fail-closed on first node", count)
	}
	frozen, _ := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if !frozen {
		t.Fatal("recovery freeze must remain active after isolation failure")
	}
}

func TestPowerShutdownAdapterVerifyUsesAgentStatus(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	if err := repository.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("apply protections: %v", err)
	}
	transport := &fakePowerTransport{}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved

	// Services still running → verification fails.
	stopped := false
	running := true
	transport.responses = map[string]agent.Response{agent.CommandMySQLPowerStatus: {Status: agent.StatusOK, ServiceRunning: &running}}
	verification, err := shutdown.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verification.Passed {
		t.Fatalf("verify must fail while services run: %+v", verification.Checks)
	}

	// Services stopped → verification passes.
	transport.responses = map[string]agent.Response{agent.CommandMySQLPowerStatus: {Status: agent.StatusOK, ServiceRunning: &stopped}}
	verification, err = shutdown.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verification.Passed {
		t.Fatalf("verify must pass once services are stopped: %+v", verification.Checks)
	}
}

func TestPowerShutdownAdapterVerifyPoweroffModeAcceptsUnreachable(t *testing.T) {
	repository, shutdown, cluster := newPowerTestAdapter(t)
	operation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerPowerOff, RequestedBy: "tester",
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	for _, target := range []model.PowerState{model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned, model.PowerShuttingDown, model.PowerPoweredOff} {
		next, err := repository.TransitionPowerOperation(context.Background(), operation.ResourceID,
			operation.MetadataRevision, target, "", "", nil)
		if err != nil {
			t.Fatalf("transition to %s: %v", target, err)
		}
		operation = next
	}
	if err := repository.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("apply protections: %v", err)
	}
	transport := &fakePowerTransport{
		failures: map[string]error{agent.CommandMySQLPowerStatus: errors.New("ssh: host is down")},
	}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	resolved, _ := resolvedPowerTopology(t, repository, cluster)
	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	verification, err := shutdown.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verification.Passed {
		t.Fatalf("verify must pass when powered-off hosts are unreachable: %+v", verification.Checks)
	}
}

func TestPowerShutdownAdapterExecutePostgreSQLEngine(t *testing.T) {
	repository := store.NewMemory()
	cluster := seedPowerClusterEngine(t, repository, "adapter-pg", model.EnginePostgreSQL, "postgresql", 5432)
	shutdown := NewPowerShutdownAdapter(repository, nil, "")
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	resolved, _ := resolvedPowerTopologyFor(t, repository, cluster, "postgresql")
	transport := &fakePowerTransport{responses: map[string]agent.Response{
		agent.CommandPowerPrepare:   powerOKResponse(),
		agent.CommandPostgreSQLStop: powerOKResponse(),
	}}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved
	resolved.OperationID = request.Operation.ResourceID
	leaseID := model.NewResourceID()
	execution, err := shutdown.Execute(adapter.WithOperationLeaseID(context.Background(), leaseID), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationSucceeded {
		t.Fatalf("status=%s, want succeeded", execution.Status)
	}

	// PostgreSQL has no PERSIST_ONLY read-only: the persist step is skipped
	// and no MySQL command may be dispatched.
	if count := transportCommandCount(transport, agent.CommandPersistRole); count != 0 {
		t.Fatalf("persist_role sends=%d, want 0 for postgresql", count)
	}
	if count := transportCommandCount(transport, agent.CommandMySQLServiceStop); count != 0 {
		t.Fatalf("mysql_service_stop sends=%d, want 0", count)
	}
	// postgresql_stop on the replica, then the primary, stamped with the
	// PostgreSQL engine.
	if count := transportCommandCount(transport, agent.CommandPostgreSQLStop); count != 2 {
		t.Fatalf("postgresql_stop sends=%d, want 2", count)
	}
	stopIndices := []int{}
	for index, request := range transport.requests {
		if request.Command == agent.CommandPostgreSQLStop {
			stopIndices = append(stopIndices, index)
			if request.Engine != model.EnginePostgreSQL {
				t.Fatalf("stop request engine=%s, want postgresql", request.Engine)
			}
			if request.LeaseID != leaseID {
				t.Fatalf("stop request lease=%s, want operation lock lease %s", request.LeaseID, leaseID)
			}
		}
	}
	if transport.instances[stopIndices[0]].DisplayName != "postgresql-b" {
		t.Fatalf("first stop went to %s, want replica postgresql-b", transport.instances[stopIndices[0]].DisplayName)
	}
	if transport.instances[stopIndices[1]].DisplayName != "postgresql-a" {
		t.Fatalf("second stop went to %s, want primary postgresql-a", transport.instances[stopIndices[1]].DisplayName)
	}

	// The state machine still advanced with protections active.
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerPoweredOff {
		t.Fatalf("state=%s found=%t, want power_off", operation.State, found)
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze after execute: frozen=%t err=%v", frozen, err)
	}
}

func TestPowerShutdownAdapterBuildPlanPostgreSQLSkipsPersist(t *testing.T) {
	repository := store.NewMemory()
	cluster := seedPowerClusterEngine(t, repository, "adapter-pg-plan", model.EnginePostgreSQL, "postgresql", 5432)
	shutdown := NewPowerShutdownAdapter(repository, nil, "")
	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	resolved, primary := resolvedPowerTopologyFor(t, repository, cluster, "postgresql")
	request.TargetID = primary.ResourceID
	request.Resolved = resolved

	plan, err := shutdown.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if len(plan.Steps) != 5 {
		t.Fatalf("expected 5 plan steps for postgresql, got %d: %+v", len(plan.Steps), plan.Steps)
	}
	for _, step := range plan.Steps {
		if step.Name == "persist_read_only" {
			t.Fatalf("persist_read_only must not appear in a postgresql plan")
		}
	}
	if plan.Steps[2].Name != "prepare_recovery_snapshot" || plan.Steps[3].Name != "stop_replicas" || plan.Steps[4].Name != "stop_primary" {
		t.Fatalf("unexpected tail steps: %+v", plan.Steps[2:])
	}
	if plan.Steps[2].Index != 3 || plan.Steps[3].Index != 4 || plan.Steps[4].Index != 5 {
		t.Fatalf("steps must renumber without the persist step: %+v", plan.Steps[2:])
	}
}

func TestPowerShutdownAdapterVerifyUsesEngineStatusCommand(t *testing.T) {
	repository := store.NewMemory()
	cluster := seedPowerClusterEngine(t, repository, "adapter-pg-verify", model.EnginePostgreSQL, "postgresql", 5432)
	shutdown := NewPowerShutdownAdapter(repository, nil, "")
	createPlannedPowerOperation(t, repository, cluster.ResourceID)
	if err := repository.ApplyPowerProtections(context.Background(), cluster.ResourceID); err != nil {
		t.Fatalf("apply protections: %v", err)
	}
	transport := &fakePowerTransport{}
	shutdown.transport = transport
	shutdown.secret = "agent-secret"

	resolved, _ := resolvedPowerTopologyFor(t, repository, cluster, "postgresql")
	request := powerRequest(cluster.ResourceID, model.NewResourceID())
	request.Resolved = resolved

	stopped := false
	transport.responses = map[string]agent.Response{agent.CommandPostgreSQLStatus: {Status: agent.StatusOK, ServiceRunning: &stopped}}
	verification, err := shutdown.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verification.Passed {
		t.Fatalf("verify must pass with services stopped: %+v", verification.Checks)
	}
	if count := transportCommandCount(transport, agent.CommandPostgreSQLStatus); count != 2 {
		t.Fatalf("postgresql_status sends=%d, want 2", count)
	}
	if count := transportCommandCount(transport, agent.CommandMySQLPowerStatus); count != 0 {
		t.Fatalf("mysql_power_status sends=%d, want 0", count)
	}
}
