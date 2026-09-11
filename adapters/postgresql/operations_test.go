package postgresql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type postgresqlEndpointStub struct{ executable bool }

func (stub postgresqlEndpointStub) Executable(context.Context) bool { return stub.executable }
func (postgresqlEndpointStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "one primary VIP owner"}}
}
func (postgresqlEndpointStub) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	guarded, cancel := context.WithCancel(ctx)
	return adapter.TransitionAuthorization{Context: guarded, Cancel: cancel, Abort: func(context.Context) error { return nil }, Finalize: func(context.Context) error { return nil }, LeaseID: model.NewResourceID()}, nil
}
func (postgresqlEndpointStub) AuthorizeStableOwner(ctx context.Context, _ adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	guarded, cancel := context.WithCancel(ctx)
	return adapter.StableOwnershipAuthorization{Context: guarded, Cancel: cancel, LeaseID: model.NewResourceID()}, nil
}
func (postgresqlEndpointStub) Transfer(context.Context, adapter.ResolvedOperation) error { return nil }
func (postgresqlEndpointStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "target owns VIP"}
}

type postgresqlNodeControllerStub struct {
	executable bool
	calls      []string
}

func (stub *postgresqlNodeControllerStub) Executable(context.Context) bool { return stub.executable }
func (*postgresqlNodeControllerStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "postgresql_node_controller", Status: model.CheckPass, Message: "restricted node controller is ready"}}
}
func (*postgresqlNodeControllerStub) Status(_ context.Context, _ adapter.ResolvedOperation, instance model.DatabaseInstance) (bool, bool, error) {
	return true, instance.Role != model.RolePrimary, nil
}
func (stub *postgresqlNodeControllerStub) Stop(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "stop")
	return nil
}
func (stub *postgresqlNodeControllerStub) IsStopped(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance) (bool, error) {
	stub.calls = append(stub.calls, "stopped")
	return true, nil
}
func (stub *postgresqlNodeControllerStub) Promote(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "promote")
	return nil
}
func (stub *postgresqlNodeControllerStub) Repoint(_ context.Context, _ adapter.ResolvedOperation, target model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "repoint:"+string(target.ResourceID))
	return nil
}
func (stub *postgresqlNodeControllerStub) Rewind(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "rewind")
	return nil
}
func (stub *postgresqlNodeControllerStub) BaseBackup(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "basebackup")
	return nil
}
func (stub *postgresqlNodeControllerStub) Start(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.ResourceID) error {
	stub.calls = append(stub.calls, "start")
	return nil
}

type postgresqlFailoverSafetyStub struct{}

func (postgresqlFailoverSafetyStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "old_primary_fenced", Status: model.CheckPass, Message: "fence is available"}}
}
func (postgresqlFailoverSafetyStub) Fence(context.Context, adapter.ResolvedOperation) error {
	return nil
}
func (postgresqlFailoverSafetyStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "old_primary_fenced", Status: model.CheckPass, Message: "old primary is fenced"}
}

func TestPostgreSQLExecuteCapabilityRequiresAllRealProviders(t *testing.T) {
	runner := &postgresqlExecutableRunner{}
	controller := &postgresqlNodeControllerStub{executable: true}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, controller, postgresqlFailoverSafetyStub{})
	capabilities := instance.Capabilities(context.Background())
	for _, name := range []adapter.Capability{adapter.CapabilityPrecheck, adapter.CapabilityPlan, adapter.CapabilityExecute, adapter.CapabilityVerify} {
		if !capabilities.Supports(name) {
			t.Fatalf("capability %s is unavailable: %+v", name, capabilities.Features[name])
		}
	}

	for name, candidate := range map[string]*Adapter{
		"query only":      New(&fakeRunner{}),
		"no endpoint":     NewWithProviders(runner, postgresqlEndpointStub{}, controller, postgresqlFailoverSafetyStub{}),
		"no node control": NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{}, postgresqlFailoverSafetyStub{}),
	} {
		if candidate.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
			t.Fatalf("%s configuration advertised execution", name)
		}
	}
}

func TestPostgreSQLSwitchoverPlanStopsAndVerifiesSourceBeforePromotion(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
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
		t.Fatalf("build plan: %v", err)
	}
	if plan.Digest == "" || !plan.Mutating || plan.SourceID != request.Resolved.Primary.ResourceID || plan.TargetID != request.TargetID {
		t.Fatalf("incomplete plan: %+v", plan)
	}
	fenceIndex, captureIndex, stopIndex, verifyIndex, waitIndex, promoteIndex, writableIndex := 0, 0, 0, 0, 0, 0, 0
	for _, step := range plan.Steps {
		switch step.Name {
		case "fence_source_writes":
			fenceIndex = step.Index
		case "capture_source_wal":
			captureIndex = step.Index
		case "stop_source":
			stopIndex = step.Index
		case "verify_source_stopped":
			verifyIndex = step.Index
		case "wait_target_wal":
			waitIndex = step.Index
		case "promote_target":
			promoteIndex = step.Index
		case "activate_target_writes":
			writableIndex = step.Index
		}
	}
	if fenceIndex == 0 || captureIndex <= fenceIndex || stopIndex <= captureIndex || verifyIndex <= stopIndex || waitIndex <= verifyIndex || promoteIndex <= waitIndex || writableIndex <= promoteIndex {
		t.Fatalf("unsafe switchover order: %+v", plan.Steps)
	}
}

func TestPostgreSQLPrecheckRejectsInvalidNativeNodeIdentity(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Target.EngineIdentity["resource_id"] = "invalid-native-node-id"
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name == "native_identity" {
			if check.Status != model.CheckFail {
				t.Fatalf("invalid native identity was accepted: %+v", check)
			}
			return
		}
	}
	t.Fatalf("native identity check is missing: %+v", checks)
}

func TestPostgreSQLPrecheckAcceptsDistinctPlatformAndNativeNodeIdentities(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	primaryNodeID := model.NewResourceID()
	targetNodeID := model.NewResourceID()
	request.Resolved.Primary.EngineIdentity["resource_id"] = string(primaryNodeID)
	request.Resolved.Target.EngineIdentity["resource_id"] = string(targetNodeID)
	request.Resolved.Target.Replication.SourceIdentity["resource_id"] = string(primaryNodeID)
	for index := range request.Resolved.Snapshot.Instances {
		switch request.Resolved.Snapshot.Instances[index].ResourceID {
		case request.Resolved.Primary.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Primary
		case request.Resolved.Target.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}

	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if (check.Name == "native_identity" || check.Name == "target_upstream") && check.Status != model.CheckPass {
			t.Fatalf("distinct platform and native identities were rejected: %+v", checks)
		}
	}
}

func postgresqlRequestWithDistinctNativeNodeIdentities(kind model.OperationKind) (adapter.OperationRequest, model.ResourceID, model.ResourceID) {
	request := postgresqlOperationRequest(kind)
	primaryNodeID := model.NewResourceID()
	targetNodeID := model.NewResourceID()
	request.Resolved.Primary.EngineIdentity["resource_id"] = string(primaryNodeID)
	request.Resolved.Target.EngineIdentity["resource_id"] = string(targetNodeID)
	request.Resolved.Target.Replication.SourceIdentity["resource_id"] = string(primaryNodeID)
	for index := range request.Resolved.Snapshot.Instances {
		switch request.Resolved.Snapshot.Instances[index].ResourceID {
		case request.Resolved.Primary.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Primary
		case request.Resolved.Target.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}
	return request, primaryNodeID, targetNodeID
}

func TestPostgreSQLLiveSwitchoverPrecheckUsesNativeNodeIdentity(t *testing.T) {
	request, primaryNodeID, targetNodeID := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	primaryRow := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: primaryNodeID}, Hostname: "pg-01", Port: 5432}, "", true)
	targetRow := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: targetNodeID}, Hostname: "pg-02", Port: 5432}, primaryNodeID, false)
	postgresqlSetSenderEvidence(primaryRow, targetNodeID)
	postgresqlSetReceiverEvidence(targetRow, request.Resolved.Primary)
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		"pg-01": {primaryRow},
		"pg-02": {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	if err := instance.postgresqlLiveSwitchoverPrecheck(context.Background(), *request.Resolved); err != nil {
		t.Fatalf("live precheck rejected distinct platform and native identities: %v", err)
	}
}

func TestPostgreSQLVerificationUsesNativeNodeAndSourceIdentities(t *testing.T) {
	request, primaryNodeID, targetNodeID := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	primaryRow := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: primaryNodeID}, Hostname: "pg-01", Port: 5432}, "", true)
	targetRow := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: targetNodeID}, Hostname: "pg-02", Port: 5432}, primaryNodeID, false)
	postgresqlSetSenderEvidence(primaryRow, targetNodeID)
	postgresqlSetReceiverEvidence(targetRow, request.Resolved.Primary)
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		"pg-01": {primaryRow},
		"pg-02": {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	check := instance.postgresqlInstanceVerificationCheck(
		context.Background(), *request.Resolved, "target", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckPass {
		t.Fatalf("verification rejected distinct platform and native identities: %+v", check)
	}
}

type postgresqlTransientVerificationRunner struct {
	sourceRow         Row
	targetRow         Row
	transientFailures int
	calls             int
}

func (runner *postgresqlTransientVerificationRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	if query != identityQuery {
		return nil, errors.New("unexpected query")
	}
	if endpoint.Hostname == "pg-01" {
		return []Row{runner.sourceRow}, nil
	}
	runner.calls++
	if runner.calls <= runner.transientFailures {
		return nil, errors.New("PostgreSQL is still starting")
	}
	return []Row{runner.targetRow}, nil
}

func (*postgresqlTransientVerificationRunner) Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error {
	return nil
}

func TestPostgreSQLInstanceVerificationWaitsForTransientStartup(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	sourceRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, request.Resolved.Primary.ResourceID)
	runner := &postgresqlTransientVerificationRunner{
		sourceRow:         sourceRow,
		targetRow:         targetRow,
		transientFailures: 2,
	}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check := instance.postgresqlInstanceVerificationCheck(
		context.Background(), *request.Resolved, "target", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckPass || runner.calls != 3 {
		t.Fatalf("transient PostgreSQL startup was not allowed to converge: check=%+v calls=%d", check, runner.calls)
	}
}

func TestPostgreSQLInstanceVerificationAcceptsStreamingStandbyWithoutTimeLag(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	sourceRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, request.Resolved.Primary.ResourceID)
	targetRow["lag_seconds"] = ""
	runner := &postgresqlTransientVerificationRunner{sourceRow: sourceRow, targetRow: targetRow}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check := instance.postgresqlInstanceVerificationCheck(
		context.Background(), *request.Resolved, "target", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckPass || runner.calls != 1 {
		t.Fatalf("healthy streaming standby without a replay timestamp was rejected: check=%+v calls=%d", check, runner.calls)
	}
}

func TestPostgreSQLInstanceVerificationFailsClosedOnIdentityMismatch(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	foreign := request.Resolved.Target
	foreign.ResourceID = model.NewResourceID()
	foreign.EngineIdentity = request.Resolved.Target.EngineIdentity.Clone()
	foreign.EngineIdentity["resource_id"] = string(foreign.ResourceID)
	sourceRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, foreign, request.Resolved.Primary.ResourceID)
	runner := &postgresqlTransientVerificationRunner{
		sourceRow: sourceRow,
		targetRow: targetRow,
	}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check := instance.postgresqlInstanceVerificationCheck(
		context.Background(), *request.Resolved, "target", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckFail || runner.calls != 1 {
		t.Fatalf("foreign PostgreSQL identity was retried or accepted: check=%+v calls=%d", check, runner.calls)
	}
}

func TestPostgreSQLPlanAcceptsEquivalentRefreshedResourceRevision(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.Cluster.MetadataRevision++
	request.Resolved.Primary.MetadataRevision++
	request.Resolved.Target.MetadataRevision++
	for index := range request.Resolved.Snapshot.Instances {
		request.Resolved.Snapshot.Instances[index].MetadataRevision++
	}
	if err := validatePostgreSQLPlan(request, model.OperationSwitchover, false); err != nil {
		t.Fatalf("equivalent topology refresh invalidated the approved plan: %v", err)
	}
}

func TestPostgreSQLPlanRejectsMissingPinnedResourceRevision(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	plan.ResourceRevisions[request.Resolved.Target.ResourceID] = 0
	plan.Digest, err = postgresqlPlanDigest(plan)
	if err != nil {
		t.Fatalf("digest modified plan: %v", err)
	}
	request.Plan = &plan
	if err := validatePostgreSQLPlan(request, model.OperationSwitchover, false); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("missing pinned target revision was accepted: %v", err)
	}
}

func TestPostgreSQLPlanRejectsTamperedDigest(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	// Mutate the plan while keeping the approved digest: the immutable-plan
	// guard must reject it instead of executing a plan that was never approved.
	plan.TargetID = model.NewResourceID()
	request.Plan = &plan
	if err := validatePostgreSQLPlan(request, model.OperationSwitchover, false); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered plan digest was accepted: %v", err)
	}
}

func postgresqlFailoverOperationRequest() adapter.OperationRequest {
	request := postgresqlOperationRequest(model.OperationFailover)
	request.Resolved.Primary.Health = model.Health{State: model.HealthUnhealthy, Summary: "primary unreachable", ObservedAt: request.Resolved.Snapshot.ObservedAt}
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Primary.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Primary
		}
	}
	return request
}

func TestPostgreSQLFailoverPlanFencesAndVerifiesOldPrimaryBeforePromotion(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	fenceIndex, verifyIndex, authorizeIndex, promoteIndex, endpointIndex := 0, 0, 0, 0, 0
	for _, step := range plan.Steps {
		switch step.Name {
		case "fence_old_primary":
			fenceIndex = step.Index
		case "verify_old_primary_fenced":
			verifyIndex = step.Index
		case "authorize_target_transition":
			authorizeIndex = step.Index
		case "promote_target":
			promoteIndex = step.Index
		case "transfer_writer_endpoint":
			endpointIndex = step.Index
		}
	}
	if fenceIndex == 0 || verifyIndex <= fenceIndex || authorizeIndex <= verifyIndex || promoteIndex <= authorizeIndex || endpointIndex <= promoteIndex {
		t.Fatalf("unsafe PostgreSQL failover plan: %+v", plan.Steps)
	}
}

func postgresqlSourceLossFailoverRequest() adapter.OperationRequest {
	request := postgresqlFailoverOperationRequest()
	request.Resolved.Primary.Health.State = model.HealthUnknown
	request.Resolved.Primary.Role = model.RoleUnknown
	request.Resolved.Target.Health.State = model.HealthDegraded
	request.Resolved.Target.PromotionEligible = false
	request.Resolved.Target.Replication.IOThread = model.ThreadStopped
	request.Resolved.Target.Replication.RetrievedPosition = "0/5000070"
	request.Resolved.Target.Replication.ExecutedPosition = "0/5000070"
	request.Resolved.Target.EngineMetadata["receive_lsn"] = "0/5000070"
	request.Resolved.Target.EngineMetadata["replay_lsn"] = "0/5000070"
	for index := range request.Resolved.Snapshot.Instances {
		switch request.Resolved.Snapshot.Instances[index].ResourceID {
		case request.Resolved.Primary.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Primary
		case request.Resolved.Target.ResourceID:
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}
	for index := range request.Resolved.Snapshot.Probes {
		if request.Resolved.Snapshot.Probes[index].InstanceID == request.Resolved.Target.ResourceID {
			request.Resolved.Snapshot.Probes[index].Health.State = model.HealthDegraded
		}
	}
	return request
}

func TestPostgreSQLFailoverPrecheckAllowsCaughtUpReachableStandbyAfterSourceLoss(t *testing.T) {
	request := postgresqlSourceLossFailoverRequest()
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("safe source-loss failover was blocked: %+v", checks)
		}
	}
	for _, name := range []string{"target_probe_evidence", "target_standby", "target_streaming"} {
		if status := postgresqlOperationCheckStatus(t, checks, name); status != model.CheckWarn {
			t.Fatalf("%s status = %s, want warning: %+v", name, status, checks)
		}
	}
}

func TestPostgreSQLLiveFailoverPrecheckAllowsCaughtUpStandbyAfterSourceLoss(t *testing.T) {
	request := postgresqlSourceLossFailoverRequest()
	row := postgresqlLiveRow(request.Resolved.Target, postgresqlNativeNodeID(request.Resolved.Primary), false)
	row["wal_receiver_status"] = ""
	row["receive_lsn"] = "0/5000070"
	row["replay_lsn"] = "0/5000070"
	row["lag_seconds"] = "0"
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{"pg-02": {row}}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	if err := instance.postgresqlLiveFailoverTargetPrecheck(context.Background(), *request.Resolved); err != nil {
		t.Fatalf("live source-loss failover target was blocked: %v", err)
	}
}

func postgresqlOperationCheckStatus(t *testing.T, checks []model.Check, name string) model.CheckStatus {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check.Status
		}
	}
	t.Fatalf("check %q is missing: %+v", name, checks)
	return ""
}

type postgresqlExecutableRunner struct {
	rowsByHost map[string][]Row
}

func (runner *postgresqlExecutableRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	if query == postgresqlOperationPrivilegesQuery {
		return []Row{{"superuser": "false", "alter_system": "true", "reload_config": "true", "signal_backends": "true"}}, nil
	}
	if rows := runner.rowsByHost[endpoint.Hostname]; rows != nil {
		return rows, nil
	}
	return nil, errors.New("unexpected query")
}
func (*postgresqlExecutableRunner) Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error {
	return nil
}

type postgresqlPrivilegeRunner struct {
	rows    []Row
	err     error
	queries []string
}

func (runner *postgresqlPrivilegeRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	runner.queries = append(runner.queries, query)
	return runner.rows, runner.err
}

func (*postgresqlPrivilegeRunner) Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error {
	return nil
}

func TestPostgreSQLSwitchoverPrecheckBlocksMissingOperationPrivileges(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	runner := &postgresqlPrivilegeRunner{rows: []Row{{
		"alter_system": "false", "reload_config": "true", "signal_backends": "true",
	}}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name != "postgresql_operation_privileges" {
			continue
		}
		if check.Status != model.CheckFail || !strings.Contains(check.Message, "ALTER SYSTEM") {
			t.Fatalf("missing operation privileges were not explained: %+v", check)
		}
		if len(runner.queries) != 1 {
			t.Fatalf("privilege probes=%d queries=%v", len(runner.queries), runner.queries)
		}
		return
	}
	t.Fatalf("PostgreSQL operation privilege check is missing: %+v", checks)
}

func TestPostgreSQLSwitchoverPrecheckFailsClosedWhenPrivilegeProbeFails(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	runner := &postgresqlPrivilegeRunner{err: errors.New("permission probe unavailable")}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name == "postgresql_operation_privileges" {
			if check.Status != model.CheckFail || !strings.Contains(check.Message, "could not be verified") {
				t.Fatalf("privilege probe failure did not fail closed: %+v", check)
			}
			return
		}
	}
	t.Fatalf("PostgreSQL operation privilege check is missing: %+v", checks)
}

func TestPostgreSQLSwitchoverPrecheckRefreshesTransientTimelineMismatch(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Primary.EngineMetadata["timeline_id"] = "8"
	request.Resolved.Target.EngineMetadata["timeline_id"] = "7"

	primaryRow := postgresqlLiveRow(request.Resolved.Primary, "", true)
	primaryRow["timeline_id"] = "8"
	targetRow := postgresqlLiveRow(request.Resolved.Target, postgresqlNativeNodeID(request.Resolved.Primary), false)
	targetRow["timeline_id"] = "8"
	postgresqlSetSenderEvidence(primaryRow, postgresqlNativeNodeID(request.Resolved.Target))
	postgresqlSetReceiverEvidence(targetRow, request.Resolved.Primary)
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name != "timeline" {
			continue
		}
		if check.Status != model.CheckPass || !strings.Contains(check.Message, "live") {
			t.Fatalf("live timeline evidence did not clear the stale snapshot: %+v", check)
		}
		return
	}
	t.Fatalf("timeline check is missing: %+v", checks)
}

func TestPostgreSQLSwitchoverPrecheckKeepsTimelineBlockedWhenLiveProbeFails(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Primary.EngineMetadata["timeline_id"] = "8"
	request.Resolved.Target.EngineMetadata["timeline_id"] = "7"

	primaryRow := postgresqlLiveRow(request.Resolved.Primary, "", true)
	primaryRow["timeline_id"] = "8"
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if status := postgresqlOperationCheckStatus(t, checks, "timeline"); status != model.CheckFail {
		t.Fatalf("unavailable live evidence must fail closed, got %s", status)
	}
}

func TestPostgreSQLSwitchoverPrecheckKeepsTimelineBlockedOnLiveIdentityMismatch(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Primary.EngineMetadata["timeline_id"] = "8"
	request.Resolved.Target.EngineMetadata["timeline_id"] = "7"

	primaryRow := postgresqlLiveRow(request.Resolved.Primary, "", true)
	primaryRow["timeline_id"] = "8"
	targetRow := postgresqlLiveRow(request.Resolved.Target, postgresqlNativeNodeID(request.Resolved.Primary), false)
	targetRow["timeline_id"] = "8"
	targetRow["node_id"] = string(model.NewResourceID())
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if status := postgresqlOperationCheckStatus(t, checks, "timeline"); status != model.CheckFail {
		t.Fatalf("mismatched live identity must fail closed, got %s", status)
	}
}

func TestPostgreSQLSwitchoverPrecheckRefreshesTransientWALPositionLag(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Primary.EngineMetadata["current_lsn"] = "0/5000070"

	primaryRow := postgresqlLiveRow(request.Resolved.Primary, "", true)
	primaryRow["current_lsn"] = "0/5000070"
	targetRow := postgresqlLiveRow(request.Resolved.Target, postgresqlNativeNodeID(request.Resolved.Primary), false)
	targetRow["receive_lsn"] = "0/5000070"
	targetRow["replay_lsn"] = "0/5000070"
	postgresqlSetSenderEvidence(primaryRow, postgresqlNativeNodeID(request.Resolved.Target))
	postgresqlSetReceiverEvidence(targetRow, request.Resolved.Primary)
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name != "wal_position" {
			continue
		}
		if check.Status != model.CheckPass || !strings.Contains(check.Message, "live") {
			t.Fatalf("live WAL evidence did not clear the stale snapshot: %+v", check)
		}
		return
	}
	t.Fatalf("WAL position check is missing: %+v", checks)
}

func TestPostgreSQLSwitchoverPrecheckDoesNotRefreshMultipleSnapshotFailures(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Resolved.Primary.EngineMetadata["current_lsn"] = "0/5000070"
	request.Resolved.Target.EngineMetadata["timeline_id"] = "6"

	primaryRow := postgresqlLiveRow(request.Resolved.Primary, "", true)
	primaryRow["current_lsn"] = "0/5000070"
	targetRow := postgresqlLiveRow(request.Resolved.Target, postgresqlNativeNodeID(request.Resolved.Primary), false)
	targetRow["receive_lsn"] = "0/5000070"
	targetRow["replay_lsn"] = "0/5000070"
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if status := postgresqlOperationCheckStatus(t, checks, "timeline"); status != model.CheckFail {
		t.Fatalf("multiple snapshot failures must keep timeline blocked, got %s", status)
	}
	if status := postgresqlOperationCheckStatus(t, checks, "wal_position"); status != model.CheckFail {
		t.Fatalf("multiple snapshot failures must keep WAL position blocked, got %s", status)
	}
}

func postgresqlOperationRequest(kind model.OperationKind) adapter.OperationRequest {
	now := time.Now().UTC().Truncate(time.Second)
	clusterID := model.ResourceID(testPostgreSQLClusterID)
	primaryID := model.ResourceID(testPostgreSQLPrimaryID)
	targetID := model.ResourceID(postgresqlCandidateA)
	lag := int64(0)
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: primaryID, MetadataRevision: 3}, ClusterID: clusterID,
		Engine: model.EnginePostgreSQL, EngineIdentity: model.EngineIdentity{"resource_id": string(primaryID), "system_identifier": "7428625847249870011"},
		Hostname: "pg-01", Port: 5432, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy, ObservedAt: now},
		EngineMetadata: map[string]string{"timeline_id": "7", "current_lsn": "0/5000060", "in_recovery": "false", "transaction_read_only": "false"},
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: targetID, MetadataRevision: 4}, ClusterID: clusterID,
		Engine: model.EnginePostgreSQL, EngineIdentity: model.EngineIdentity{"resource_id": string(targetID), "system_identifier": "7428625847249870011"},
		Hostname: "pg-02", Port: 5432, Role: model.RoleStandby, Health: model.Health{State: model.HealthHealthy, ObservedAt: now}, PromotionEligible: true,
		Replication:    model.ReplicationStatus{SourceIdentity: model.EngineIdentity{"resource_id": string(primaryID), "system_identifier": "7428625847249870011"}, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag, ExecutedPosition: "0/5000060"},
		EngineMetadata: map[string]string{"timeline_id": "7", "replay_lsn": "0/5000060", "replay_paused": "false", "in_recovery": "true", "transaction_read_only": "true"},
	}
	cluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 2}, Engine: model.EnginePostgreSQL, EngineIdentity: model.EngineIdentity{"system_identifier": "7428625847249870011"}}
	snapshot := model.TopologySnapshot{ClusterID: clusterID, Instances: []model.DatabaseInstance{primary, target}, ObservedAt: now, Probes: []model.ProbeStatus{
		{EndpointID: model.NewResourceID(), InstanceID: primaryID, DiscoveryObservedAt: now, Health: primary.Health},
		{EndpointID: model.NewResourceID(), InstanceID: targetID, DiscoveryObservedAt: now, Health: target.Health},
	}}
	operationID := model.NewResourceID()
	return adapter.OperationRequest{
		Operation: model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: operationID}, ClusterID: clusterID, Engine: model.EnginePostgreSQL, Kind: kind}, TargetID: targetID,
		Resolved: &adapter.ResolvedOperation{OperationID: operationID, ObservationToken: "pg-observation", Cluster: cluster, Snapshot: snapshot, Primary: primary, Target: target},
	}
}

type postgresqlExecutionState struct {
	trace                []string
	stopped              bool
	promoted             bool
	endpoint             bool
	rewound              bool
	fenced               bool
	rejectMultiStatement bool
	followers            map[model.ResourceID]model.ResourceID
	targetID             model.ResourceID
	stopErr              error
	startErr             error
	stopNoEffect         bool
	targetUnavailable    bool
	repointErr           error
	rewindErr            error
	basebackupErr        error
	fenceErr             error
	captureErr           error
	finalLSN             string
	systemIDs            map[model.ResourceID]string
	receivers            map[model.ResourceID]string
	transferIsolatedID   model.ResourceID
	verifyIsolatedID     model.ResourceID
}

const postgresqlFollowerID = "55555555-5555-4555-8555-555555555555"

func addPostgreSQLFollower(request *adapter.OperationRequest) model.DatabaseInstance {
	now := request.Resolved.Snapshot.ObservedAt
	lag := int64(0)
	follower := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(postgresqlFollowerID), MetadataRevision: 2},
		ClusterID:    request.Resolved.Cluster.ResourceID,
		Engine:       model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       postgresqlFollowerID,
			"system_identifier": "7428625847249870011",
		},
		DisplayName: "pg-03",
		Hostname:    "pg-03",
		Port:        5432,
		Role:        model.RoleStandby,
		Health:      model.Health{State: model.HealthHealthy, ObservedAt: now},
		Replication: model.ReplicationStatus{
			SourceIdentity:   model.EngineIdentity{"resource_id": string(request.Resolved.Primary.ResourceID), "system_identifier": "7428625847249870011"},
			IOThread:         model.ThreadRunning,
			SQLThread:        model.ThreadRunning,
			LagSeconds:       &lag,
			ExecutedPosition: "0/5000060",
		},
		EngineMetadata: map[string]string{
			"timeline_id": "7", "replay_lsn": "0/5000060", "replay_paused": "false",
			"in_recovery": "true", "transaction_read_only": "true",
		},
	}
	request.Resolved.Snapshot.Instances = append(request.Resolved.Snapshot.Instances, follower)
	request.Resolved.Snapshot.Probes = append(request.Resolved.Snapshot.Probes, model.ProbeStatus{
		EndpointID: model.NewResourceID(), InstanceID: follower.ResourceID, DiscoveryObservedAt: now, Health: follower.Health,
	})
	return follower
}

func postgresqlRejoinOperationRequest() adapter.OperationRequest {
	request := postgresqlOperationRequest(model.OperationFormerPrimaryRejoin)
	formerPrimary := request.Resolved.Primary
	currentPrimary := request.Resolved.Target
	currentPrimary.Role = model.RolePrimary
	currentPrimary.PromotionEligible = false
	currentPrimary.Replication = model.ReplicationStatus{}
	currentPrimary.EngineMetadata = map[string]string{
		"timeline_id": "8", "current_lsn": "0/6000060", "in_recovery": "false",
		"transaction_read_only": "false", "wal_log_hints": "true", "data_checksum_version": "0", "version": "16.3",
	}
	formerPrimary.Role = model.RoleUnknown
	formerPrimary.Health = model.Health{State: model.HealthDegraded, Summary: "former primary requires rejoin", ObservedAt: request.Resolved.Snapshot.ObservedAt}
	formerPrimary.EngineMetadata = map[string]string{
		"timeline_id": "7", "current_lsn": "0/5000060", "in_recovery": "false",
		"transaction_read_only": "true", "wal_log_hints": "true", "data_checksum_version": "0", "version": "16.3",
	}
	request.TargetID = formerPrimary.ResourceID
	request.Resolved.Primary = currentPrimary
	request.Resolved.Target = formerPrimary
	request.Resolved.Snapshot.Instances = []model.DatabaseInstance{currentPrimary, formerPrimary}
	return request
}

func TestPostgreSQLFormerPrimaryRejoinPlanStopsBeforeRewind(t *testing.T) {
	request := postgresqlRejoinOperationRequest()
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build rejoin plan: %v", err)
	}
	indexes := map[string]int{}
	for _, step := range plan.Steps {
		indexes[step.Name] = step.Index
	}
	if indexes["stop_former_primary"] == 0 || indexes["verify_former_primary_stopped"] <= indexes["stop_former_primary"] || indexes["rewind_former_primary"] <= indexes["verify_former_primary_stopped"] {
		t.Fatalf("unsafe PostgreSQL rejoin plan: %+v", plan.Steps)
	}
}

func TestPostgreSQLFormerPrimaryRejoinExecuteAndVerify(t *testing.T) {
	request := postgresqlRejoinOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{promoted: true, endpoint: true, finalLSN: "0/6000060"}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build rejoin plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("rejoin execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	stableIndex := strings.Index(joined, "authorize_stable_owner")
	stopIndex := strings.Index(joined, "stop_source")
	verifyIndex := strings.Index(joined, "verify_source_stopped")
	rewindIndex := strings.Index(joined, "rewind_source")
	if stableIndex < 0 || stopIndex <= stableIndex || verifyIndex <= stopIndex || rewindIndex <= verifyIndex {
		t.Fatalf("unsafe PostgreSQL rejoin order:\n%s", joined)
	}
	check, retryable := instance.postgresqlInstanceVerificationCheckOnce(
		context.Background(), *request.Resolved, "former_primary_database", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckPass || retryable {
		t.Fatalf("rejoined PostgreSQL node did not satisfy the immediate database postcondition: check=%+v retryable=%t trace=%v", check, retryable, state.trace)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("rejoin verify=%+v err=%v trace=%v", verification, err, state.trace)
	}
}

func TestPostgreSQLFormerPrimaryRejoinFallsBackToBaseBackup(t *testing.T) {
	request := postgresqlRejoinOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{
		promoted: true, endpoint: true, finalLSN: "0/6000060",
		rewindErr: errors.New("required WAL segment is unavailable"),
	}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build rejoin plan: %v", err)
	}
	progress := &postgresqlProgressCollector{}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = progress

	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("rejoin execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	if rewind := strings.Index(joined, "rewind_source"); rewind < 0 || strings.Index(joined, "basebackup_source") <= rewind {
		t.Fatalf("full base backup did not safely follow failed rewind: trace=%v", state.trace)
	}
	if message := progress.steps["rewind_former_primary"]; !strings.Contains(message, "full base backup") {
		t.Fatalf("fallback rejoin was not recorded durably: steps=%+v", progress.steps)
	}
}

func TestPostgreSQLFormerPrimaryRejoinReportsBothRecoveryFailures(t *testing.T) {
	request := postgresqlRejoinOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{
		promoted: true, endpoint: true, finalLSN: "0/6000060",
		rewindErr: errors.New("rewind failed"), basebackupErr: errors.New("base backup failed"),
	}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build rejoin plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("rejoin execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if !strings.Contains(err.Error(), "rewind") || !strings.Contains(err.Error(), "base backup") {
		t.Fatalf("combined recovery failure is incomplete: %v", err)
	}
}

func TestPostgreSQLFormerPrimaryRejoinVerifyRejectsForeignSystemIdentity(t *testing.T) {
	request := postgresqlRejoinOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	state := &postgresqlExecutionState{
		promoted: true, endpoint: true, rewound: true, finalLSN: "0/6000060",
		systemIDs: map[model.ResourceID]string{request.Resolved.Target.ResourceID: "foreign-system"},
	}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build rejoin plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	verification, err := instance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("rejoin verify: %v", err)
	}
	if verification.Passed {
		t.Fatalf("rejoin verification accepted a foreign system identity: %+v", verification.Checks)
	}
}

func postgresqlNodeSyncOperationRequest() adapter.OperationRequest {
	request := postgresqlRejoinOperationRequest()
	request.Operation.Kind = model.OperationNodeSync
	request.Parameters = map[string]string{"sync_method": "auto"}
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	request.Resolved.Target.EngineIdentity = model.EngineIdentity{"resource_id": string(request.Resolved.Target.ResourceID)}
	request.Resolved.Target.EngineMetadata["wal_log_hints"] = "false"
	request.Resolved.Target.EngineMetadata["data_checksum_version"] = "0"
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Target.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}
	return request
}

func TestPostgreSQLNodeSyncAutoUsesBaseBackupAndVerifiesStandby(t *testing.T) {
	request := postgresqlNodeSyncOperationRequest()
	state := &postgresqlExecutionState{promoted: true, endpoint: true, finalLSN: "0/6000060"}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildNodeSyncPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build node sync plan: %v", err)
	}
	if plan.Summary == "" || postgresqlBlockingChecks(plan.Checks) {
		t.Fatalf("node sync plan blocked: %+v", plan)
	}
	foundBaseBackup := false
	for _, step := range plan.Steps {
		if step.Name == "synchronize_target_pg_basebackup" {
			foundBaseBackup = true
		}
	}
	if !foundBaseBackup {
		t.Fatalf("base backup step missing: %+v", plan.Steps)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}
	execution, err := instance.ExecuteNodeSync(context.Background(), request)
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("node sync execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	if !strings.Contains(joined, "stop_source") || !strings.Contains(joined, "verify_source_stopped") || !strings.Contains(joined, "basebackup_source") {
		t.Fatalf("node sync did not execute guarded base backup: %s", joined)
	}
}

func TestPostgreSQLNodeSyncFailsWhenReceiverIsNotStreamingAfterRestore(t *testing.T) {
	request := postgresqlNodeSyncOperationRequest()
	state := &postgresqlExecutionState{
		promoted: true, endpoint: true, finalLSN: "0/6000060",
		receivers: map[model.ResourceID]string{request.Resolved.Target.ResourceID: "stopped"},
	}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildNodeSyncPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build node sync plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}

	execution, err := instance.ExecuteNodeSync(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("node sync accepted a stopped WAL receiver: execution=%+v err=%v", execution, err)
	}
}

func TestPostgreSQLNodeSyncBlocksForeignIdentityAndCrossMajorVersion(t *testing.T) {
	request := postgresqlNodeSyncOperationRequest()
	request.Resolved.Target.EngineIdentity["system_identifier"] = "foreign-system"
	request.Resolved.Target.EngineMetadata["version"] = "15.8"
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	checks, err := instance.NodeSyncPrecheck(context.Background(), request)
	if err != nil {
		t.Fatalf("node sync precheck: %v", err)
	}
	if !postgresqlBlockingChecks(checks) {
		t.Fatalf("foreign or cross-major target was not blocked: %+v", checks)
	}
}

func TestPostgreSQLRepairAllowlistBlocksUnknownAction(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationReplicationRepair)
	request.Parameters = map[string]string{"action": "delete_wal"}
	instance := NewWithProviders(&postgresqlExecutableRunner{}, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("repair precheck: %v", err)
	}
	if !postgresqlBlockingChecks(checks) {
		t.Fatalf("unknown PostgreSQL repair action was not blocked: %+v", checks)
	}
}

type postgresqlExecutionFailoverSafety struct{ state *postgresqlExecutionState }

func (*postgresqlExecutionFailoverSafety) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{
		{Name: "stable_primary_failure", Status: model.CheckPass},
		{Name: "controller_quorum", Status: model.CheckPass},
		{Name: "old_primary_fenced", Status: model.CheckPass},
	}
}
func (safety *postgresqlExecutionFailoverSafety) Fence(context.Context, adapter.ResolvedOperation) error {
	safety.state.trace = append(safety.state.trace, "fence_old_primary")
	safety.state.fenced = true
	return nil
}
func (safety *postgresqlExecutionFailoverSafety) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	if safety.state.fenced {
		return model.Check{Name: "old_primary_fenced", Status: model.CheckPass}
	}
	return model.Check{Name: "old_primary_fenced", Status: model.CheckFail}
}

type postgresqlExecutionRunner struct{ state *postgresqlExecutionState }

func postgresqlLiveRow(instance model.DatabaseInstance, sourceID model.ResourceID, primary bool) Row {
	row := Row{
		"node_id": string(instance.ResourceID), "primary_node_id": string(sourceID),
		"system_identifier": "7428625847249870011", "hostname": instance.Hostname,
		"port": fmt.Sprintf("%d", instance.Port), "version": "16.3", "timeline_id": "7",
		"wal_log_hints": "true", "data_checksum_version": "1",
		"replay_paused": "false", "lag_seconds": "0",
		"receiver_sender_host": "", "receiver_sender_port": "", "replication_senders": "[]",
	}
	if primary {
		row["primary_node_id"] = ""
		row["in_recovery"] = "false"
		row["transaction_read_only"] = "off"
		row["wal_receiver_status"] = ""
		row["current_lsn"] = "0/5000060"
		row["receive_lsn"] = ""
		row["replay_lsn"] = ""
		row["lag_seconds"] = ""
		return row
	}
	row["in_recovery"] = "true"
	row["transaction_read_only"] = "on"
	row["wal_receiver_status"] = "streaming"
	row["current_lsn"] = ""
	row["receive_lsn"] = "0/5000060"
	row["replay_lsn"] = "0/5000060"
	return row
}

func postgresqlSetReceiverEvidence(row Row, source model.DatabaseInstance) {
	host := source.IPAddress
	if host == "" {
		host = source.Hostname
	}
	row["receiver_sender_host"] = host
	row["receiver_sender_port"] = fmt.Sprintf("%d", source.Port)
}

func postgresqlSetSenderEvidence(row Row, targetNativeIDs ...model.ResourceID) {
	senders := make([]string, 0, len(targetNativeIDs))
	for _, targetNativeID := range targetNativeIDs {
		senders = append(senders, fmt.Sprintf(
			`{"application_name":%q,"state":"streaming","client_addr":"127.0.0.1","replay_lsn":"0/5000060"}`,
			targetNativeID,
		))
	}
	row["replication_senders"] = "[" + strings.Join(senders, ",") + "]"
}

func postgresqlBilateralLiveRows(source, target model.DatabaseInstance, bootstrapSourceID model.ResourceID) (Row, Row) {
	sourceProbe := source
	sourceProbe.ResourceID = postgresqlNativeNodeID(source)
	targetProbe := target
	targetProbe.ResourceID = postgresqlNativeNodeID(target)
	sourceRow := postgresqlLiveRow(sourceProbe, "", true)
	targetRow := postgresqlLiveRow(targetProbe, bootstrapSourceID, false)
	postgresqlSetSenderEvidence(sourceRow, postgresqlNativeNodeID(target))
	postgresqlSetReceiverEvidence(targetRow, source)
	return sourceRow, targetRow
}

func (runner *postgresqlExecutionRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	state := runner.state
	switch {
	case query == postgresqlOperationPrivilegesQuery:
		return []Row{{"superuser": "false", "alter_system": "true", "reload_config": "true", "signal_backends": "true"}}, nil
	case query == identityQuery:
		state.trace = append(state.trace, "discover:"+endpoint.Hostname)
		switch endpoint.Hostname {
		case "pg-01":
			row := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(testPostgreSQLPrimaryID)}, Hostname: "pg-01", Port: 5432}, model.ResourceID(postgresqlCandidateA), !state.rewound)
			if !state.rewound {
				postgresqlSetSenderEvidence(row, model.ResourceID(postgresqlCandidateA), model.ResourceID(postgresqlFollowerID))
			} else {
				postgresqlSetReceiverEvidence(row, model.DatabaseInstance{Hostname: "pg-02", Port: 5432})
			}
			state.applyPostgreSQLLiveOverrides(model.ResourceID(testPostgreSQLPrimaryID), row)
			return []Row{row}, nil
		case "pg-02":
			targetID := state.targetID
			if !model.ValidResourceID(targetID) {
				targetID = model.ResourceID(postgresqlCandidateA)
			}
			row := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, Hostname: "pg-02", Port: 5432}, model.ResourceID(testPostgreSQLPrimaryID), state.promoted)
			if state.promoted {
				postgresqlSetSenderEvidence(row, model.ResourceID(testPostgreSQLPrimaryID), model.ResourceID(postgresqlFollowerID))
			} else {
				postgresqlSetReceiverEvidence(row, model.DatabaseInstance{Hostname: "pg-01", Port: 5432})
			}
			state.applyPostgreSQLLiveOverrides(targetID, row)
			return []Row{row}, nil
		case "pg-03":
			sourceID := state.followers[model.ResourceID(postgresqlFollowerID)]
			row := postgresqlLiveRow(model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(postgresqlFollowerID)}, Hostname: "pg-03", Port: 5432}, sourceID, false)
			if sourceID == model.ResourceID(postgresqlCandidateA) {
				postgresqlSetReceiverEvidence(row, model.DatabaseInstance{Hostname: "pg-02", Port: 5432})
			} else {
				postgresqlSetReceiverEvidence(row, model.DatabaseInstance{Hostname: "pg-01", Port: 5432})
			}
			return []Row{row}, nil
		default:
			return nil, fmt.Errorf("unexpected PostgreSQL endpoint %s", endpoint.Hostname)
		}
	case strings.Contains(query, "pg_current_wal_flush_lsn"):
		state.trace = append(state.trace, "capture_wal")
		if state.captureErr != nil {
			return nil, state.captureErr
		}
		return []Row{{"current_lsn": state.finalLSN}}, nil
	case strings.Contains(query, "pg_last_wal_receive_lsn"):
		state.trace = append(state.trace, "wait_wal")
		return []Row{{
			"receive_lsn": state.finalLSN, "replay_lsn": state.finalLSN,
			"receiver_status": "streaming", "receiver_latest_end_lsn": state.finalLSN,
			"replay_paused": "false",
		}}, nil
	default:
		return nil, fmt.Errorf("unexpected PostgreSQL query: %s", query)
	}
}

func TestPostgreSQLReplayReachedUsesDurableReplayBoundary(t *testing.T) {
	expected, err := parseLSN("0/150005A8")
	if err != nil {
		t.Fatalf("parse expected LSN: %v", err)
	}
	tests := []struct {
		name string
		row  Row
		want bool
	}{
		{
			name: "legacy receive and replay are current",
			row:  Row{"receive_lsn": "0/150005A8", "replay_lsn": "0/150005A8", "replay_paused": "false"},
			want: true,
		},
		{
			name: "streaming receiver end proves current timeline is received",
			row: Row{
				"receive_lsn": "0/15000000", "replay_lsn": "0/150005A8", "replay_paused": "false",
				"receiver_status": "streaming", "receiver_latest_end_lsn": "0/150005A8",
			},
			want: true,
		},
		{
			name: "stopped receiver does not invalidate completed replay",
			row: Row{
				"receive_lsn": "0/15000000", "replay_lsn": "0/150005A8", "replay_paused": "false",
				"receiver_status": "stopped", "receiver_latest_end_lsn": "0/150005A8",
			},
			want: true,
		},
		{
			name: "missing receiver evidence does not invalidate completed replay",
			row: Row{
				"receive_lsn": "", "replay_lsn": "0/150005A8", "replay_paused": "false",
				"receiver_status": "", "receiver_latest_end_lsn": "",
			},
			want: true,
		},
		{
			name: "replay must reach the fenced position",
			row: Row{
				"receive_lsn": "0/150005A8", "replay_lsn": "0/150005A0", "replay_paused": "false",
				"receiver_status": "streaming", "receiver_latest_end_lsn": "0/150005A8",
			},
		},
		{
			name: "paused replay is never accepted",
			row: Row{
				"receive_lsn": "0/150005A8", "replay_lsn": "0/150005A8", "replay_paused": "true",
				"receiver_status": "streaming", "receiver_latest_end_lsn": "0/150005A8",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := postgresqlReplayReached(test.row, expected); got != test.want {
				t.Fatalf("postgresqlReplayReached()=%t want=%t row=%+v", got, test.want, test.row)
			}
		})
	}
}

func TestPostgreSQLReplayQueryCarriesReceiverEndEvidence(t *testing.T) {
	for _, fragment := range []string{"pg_stat_wal_receiver", "receiver_status", "receiver_latest_end_lsn", "pg_last_wal_replay_lsn"} {
		if !strings.Contains(postgresqlReplayWALQuery, fragment) {
			t.Fatalf("PostgreSQL replay query is missing %q: %s", fragment, postgresqlReplayWALQuery)
		}
	}
}

func (state *postgresqlExecutionState) applyPostgreSQLLiveOverrides(instanceID model.ResourceID, row Row) {
	if value, found := state.systemIDs[instanceID]; found {
		row["system_identifier"] = value
	}
	if value, found := state.receivers[instanceID]; found {
		row["wal_receiver_status"] = value
	}
}

func (runner *postgresqlExecutionRunner) Exec(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) error {
	if runner.state.rejectMultiStatement && strings.Contains(query, ";") {
		return errors.New("ALTER SYSTEM cannot run inside a transaction block")
	}
	switch {
	case strings.Contains(query, "default_transaction_read_only = 'on'"):
		runner.state.trace = append(runner.state.trace, "fence_writes:"+endpoint.Hostname)
		if runner.state.fenceErr != nil {
			return runner.state.fenceErr
		}
	case strings.Contains(query, "ALTER SYSTEM RESET default_transaction_read_only"):
		runner.state.trace = append(runner.state.trace, "activate_writes:"+endpoint.Hostname)
	case strings.Contains(query, "pg_reload_conf"):
		runner.state.trace = append(runner.state.trace, "reload_conf:"+endpoint.Hostname)
	case strings.Contains(query, "pg_terminate_backend"):
		runner.state.trace = append(runner.state.trace, "terminate_clients:"+endpoint.Hostname)
	default:
		return fmt.Errorf("unexpected PostgreSQL execution: %s", query)
	}
	return nil
}

type postgresqlExecutionNodeController struct{ state *postgresqlExecutionState }

func (*postgresqlExecutionNodeController) Executable(context.Context) bool { return true }
func (*postgresqlExecutionNodeController) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "postgresql_node_controller", Status: model.CheckPass, Message: "ready"}}
}
func (controller *postgresqlExecutionNodeController) Status(_ context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance) (bool, bool, error) {
	if instance.ResourceID == resolved.Target.ResourceID {
		if controller.state.targetUnavailable {
			return false, true, nil
		}
		if instance.ResourceID == model.ResourceID(testPostgreSQLPrimaryID) {
			if controller.state.stopped && !controller.state.rewound {
				return false, false, nil
			}
			return true, controller.state.rewound, nil
		}
		return true, !controller.state.promoted, nil
	}
	if instance.ResourceID == resolved.Primary.ResourceID {
		if controller.state.stopped && !controller.state.rewound {
			return false, false, nil
		}
		return true, controller.state.rewound, nil
	}
	return true, true, nil
}
func (controller *postgresqlExecutionNodeController) Stop(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "stop_source")
	if controller.state.stopErr != nil {
		return controller.state.stopErr
	}
	if !controller.state.stopNoEffect {
		controller.state.stopped = true
	}
	return nil
}
func (controller *postgresqlExecutionNodeController) IsStopped(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance) (bool, error) {
	controller.state.trace = append(controller.state.trace, "verify_source_stopped")
	return controller.state.stopped, nil
}
func (controller *postgresqlExecutionNodeController) Promote(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "promote_target")
	if !controller.state.stopped && !controller.state.fenced {
		return errors.New("source is neither stopped nor fenced")
	}
	controller.state.promoted = true
	return nil
}
func (controller *postgresqlExecutionNodeController) Repoint(_ context.Context, _ adapter.ResolvedOperation, target model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "repoint:"+string(target.ResourceID))
	if controller.state.repointErr != nil {
		return controller.state.repointErr
	}
	if controller.state.followers == nil {
		controller.state.followers = make(map[model.ResourceID]model.ResourceID)
	}
	controller.state.followers[target.ResourceID] = model.ResourceID(postgresqlCandidateA)
	return nil
}
func (controller *postgresqlExecutionNodeController) Rewind(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "rewind_source")
	if controller.state.rewindErr != nil {
		return controller.state.rewindErr
	}
	controller.state.rewound = true
	return nil
}
func (controller *postgresqlExecutionNodeController) BaseBackup(_ context.Context, _ adapter.ResolvedOperation, _ model.DatabaseInstance, _ model.DatabaseInstance, _ model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "basebackup_source")
	if controller.state.basebackupErr != nil {
		return controller.state.basebackupErr
	}
	controller.state.rewound = true
	return nil
}
func (controller *postgresqlExecutionNodeController) Start(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error {
	controller.state.trace = append(controller.state.trace, "start_source")
	if controller.state.startErr != nil {
		return controller.state.startErr
	}
	controller.state.stopped = false
	return nil
}

type postgresqlExecutionEndpointProvider struct{ state *postgresqlExecutionState }

func (*postgresqlExecutionEndpointProvider) Executable(context.Context) bool { return true }
func (*postgresqlExecutionEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "ready"}}
}
func (provider *postgresqlExecutionEndpointProvider) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	provider.state.trace = append(provider.state.trace, "authorize_transition")
	guarded, cancel := context.WithCancel(ctx)
	return adapter.TransitionAuthorization{
		Context: guarded, Cancel: cancel, LeaseID: model.NewResourceID(),
		Abort: func(context.Context) error {
			provider.state.trace = append(provider.state.trace, "abort_transition")
			return nil
		},
		Finalize: func(context.Context) error {
			provider.state.trace = append(provider.state.trace, "finalize_transition")
			return nil
		},
	}, nil
}
func (provider *postgresqlExecutionEndpointProvider) AuthorizeStableOwner(ctx context.Context, _ adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	provider.state.trace = append(provider.state.trace, "authorize_stable_owner")
	guarded, cancel := context.WithCancel(ctx)
	return adapter.StableOwnershipAuthorization{Context: guarded, Cancel: cancel, LeaseID: model.NewResourceID()}, nil
}
func (provider *postgresqlExecutionEndpointProvider) Transfer(_ context.Context, resolved adapter.ResolvedOperation) error {
	provider.state.trace = append(provider.state.trace, "transfer_endpoint")
	provider.state.transferIsolatedID = resolved.VerifiedIsolatedSourceID
	if !provider.state.promoted {
		return errors.New("target is not promoted")
	}
	provider.state.endpoint = true
	return nil
}
func (provider *postgresqlExecutionEndpointProvider) Verify(_ context.Context, resolved adapter.ResolvedOperation) model.Check {
	provider.state.verifyIsolatedID = resolved.VerifiedIsolatedSourceID
	if provider.state.endpoint {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "target owns endpoint"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "target does not own endpoint"}
}

type postgresqlProgressCollector struct{ steps map[string]string }

func (collector *postgresqlProgressCollector) CompleteStep(_ context.Context, step, message string) error {
	if collector.steps == nil {
		collector.steps = make(map[string]string)
	}
	collector.steps[step] = message
	return nil
}
func (collector *postgresqlProgressCollector) StepCompleted(_ context.Context, step string) (bool, error) {
	_, found := collector.steps[step]
	return found, nil
}
func (collector *postgresqlProgressCollector) StepResult(_ context.Context, step string) (string, bool, error) {
	value, found := collector.steps[step]
	return value, found, nil
}

func postgresqlExecutableOperationFixture(t *testing.T) (*Adapter, adapter.OperationRequest, *postgresqlExecutionState) {
	t.Helper()
	request := postgresqlOperationRequest(model.OperationSwitchover)
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{finalLSN: "0/5000060", followers: make(map[model.ResourceID]model.ResourceID)}
	runner := &postgresqlExecutionRunner{state: state}
	instance := NewWithProviders(runner, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}
	return instance, request, state
}

func TestPostgreSQLSwitchoverVerifyFailsWhenSiblingStillFollowsFormerPrimary(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	follower := addPostgreSQLFollower(&request)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build three-node switchover plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}

	if execution, err := instance.Execute(context.Background(), request); err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	state.followers[follower.ResourceID] = request.Resolved.Primary.ResourceID

	verification, err := instance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verification.Passed {
		t.Fatalf("verification passed while sibling follows former primary: %+v", verification.Checks)
	}
}

func TestPostgreSQLSwitchoverExecuteAndVerifyRealOrdering(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	ordered := []string{
		"authorize_transition", "fence_writes:pg-01", "capture_wal", "stop_source", "verify_source_stopped",
		"wait_wal", "promote_target", "activate_writes:pg-02", "transfer_endpoint", "finalize_transition", "rewind_source",
	}
	previous := -1
	for _, expected := range ordered {
		index := strings.Index(joined, expected)
		if index < 0 || index <= previous {
			t.Fatalf("unsafe or missing %q in trace:\n%s", expected, joined)
		}
		previous = index
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v trace=%v", verification, err, state.trace)
	}
}

func TestPostgreSQLSwitchoverRollsBackStoppedSourceBeforePromotion(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.targetUnavailable = true

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed {
		t.Fatalf("safe rollback must return a failed operation: execution=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if state.stopped || state.promoted {
		t.Fatalf("rollback left an unsafe database role: stopped=%t promoted=%t trace=%v", state.stopped, state.promoted, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	for _, expected := range []string{"stop_source", "verify_source_stopped", "start_source", "activate_writes:pg-01", "abort_transition"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("safe rollback is missing %q: trace=%v", expected, state.trace)
		}
	}
	if strings.Index(joined, "start_source") > strings.Index(joined, "abort_transition") {
		t.Fatalf("transition lease was released before the source was restored: trace=%v", state.trace)
	}
}

func TestPostgreSQLSwitchoverMutationBudgetCoversPhysicalRejoin(t *testing.T) {
	if postgresqlSwitchoverMutationTimeout() < 30*time.Minute {
		t.Fatalf("PostgreSQL switchover mutation budget is too short for VIP transfer and physical rejoin: %s", postgresqlSwitchoverMutationTimeout())
	}
}

func TestPostgreSQLSwitchoverFallsBackToBaseBackupWhenRewindCannotReadOldWAL(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.rewindErr = errors.New("could not find previous WAL record")
	progress := &postgresqlProgressCollector{}
	request.Progress = progress

	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	rewind := strings.Index(joined, "rewind_source")
	basebackup := strings.Index(joined, "basebackup_source")
	if rewind < 0 || basebackup <= rewind {
		t.Fatalf("full synchronization did not follow failed rewind: trace=%v", state.trace)
	}
	if message := progress.steps["rewind_former_primary"]; !strings.Contains(message, "full base backup") {
		t.Fatalf("fallback recovery was not recorded durably: steps=%+v", progress.steps)
	}
	verification, verifyErr := instance.Verify(context.Background(), request)
	if verifyErr != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v trace=%v", verification, verifyErr, state.trace)
	}
}

func TestPostgreSQLSwitchoverRemainsIndeterminateWhenRewindAndBaseBackupFail(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.rewindErr = errors.New("could not find previous WAL record")
	state.basebackupErr = errors.New("base backup failed")

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if !strings.Contains(err.Error(), "rewind") || !strings.Contains(err.Error(), "base backup") {
		t.Fatalf("combined recovery failure is incomplete: %v", err)
	}
	joined := strings.Join(state.trace, "\n")
	if !strings.Contains(joined, "rewind_source") || !strings.Contains(joined, "basebackup_source") {
		t.Fatalf("both safe recovery methods were not attempted: trace=%v", state.trace)
	}
}

func TestPostgreSQLSwitchoverRecordsEndpointTransferBeforeFollowerRejoin(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	addPostgreSQLFollower(&request)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build three-node switchover plan: %v", err)
	}
	progress := &postgresqlProgressCollector{}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = progress
	state.repointErr = errors.New("repoint follower failed")

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if !state.endpoint {
		t.Fatalf("endpoint was not transferred before follower rejoin failure: trace=%v", state.trace)
	}
	if _, found := progress.steps["transfer_writer_endpoint"]; !found {
		t.Fatalf("successful endpoint transfer was not durably recorded before follower rejoin failure: steps=%+v trace=%v", progress.steps, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	finalize := strings.Index(joined, "finalize_transition")
	repoint := strings.Index(joined, "repoint:"+postgresqlFollowerID)
	if finalize < 0 || repoint < 0 || finalize > repoint {
		t.Fatalf("endpoint lease was not stabilized before follower rejoin: trace=%v", state.trace)
	}
}

func TestPostgreSQLSwitchoverExecutesAlterSystemOutsideMultiStatementBatch(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.rejectMultiStatement = true
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	for _, expected := range []string{
		"fence_writes:pg-01", "reload_conf:pg-01", "terminate_clients:pg-01",
		"activate_writes:pg-02", "reload_conf:pg-02",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("standalone PostgreSQL control statement %q is missing:\n%s", expected, joined)
		}
	}
}

func TestPostgreSQLSwitchoverAbortsTransitionWhenWriteFenceFails(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.fenceErr = errors.New("write fence failed")
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	restore := strings.Index(joined, "activate_writes:pg-01")
	abort := strings.Index(joined, "abort_transition")
	if restore < 0 || abort <= restore || strings.Contains(joined, "stop_source") || strings.Contains(joined, "promote_target") {
		t.Fatalf("unsafe pre-commit cleanup trace:\n%s", joined)
	}
}

func TestPostgreSQLSwitchoverAbortsTransitionWhenFencedWALCaptureFails(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.captureErr = errors.New("WAL capture failed")
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	restore := strings.Index(joined, "activate_writes:pg-01")
	abort := strings.Index(joined, "abort_transition")
	if restore < 0 || abort <= restore || strings.Contains(joined, "stop_source") || strings.Contains(joined, "promote_target") {
		t.Fatalf("unsafe WAL-capture cleanup trace:\n%s", joined)
	}
}

func TestPostgreSQLSwitchoverRestoresWritesWhenStopFails(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.stopErr = errors.New("systemctl stop failed")
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status == model.OperationRunning {
		t.Fatalf("execute=%+v err=%v", execution, err)
	}
	joined := strings.Join(state.trace, "\n")
	if !strings.Contains(joined, "activate_writes:pg-01") || !strings.Contains(joined, "abort_transition") || strings.Contains(joined, "promote_target") {
		t.Fatalf("source write restoration trace=%s", joined)
	}
}

func TestPostgreSQLSwitchoverAbortsTransitionWhenStopDoesNotTakeEffect(t *testing.T) {
	instance, request, state := postgresqlExecutableOperationFixture(t)
	state.stopNoEffect = true
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed {
		t.Fatalf("execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	joined := strings.Join(state.trace, "\n")
	if !strings.Contains(joined, "activate_writes:pg-01") || !strings.Contains(joined, "abort_transition") || strings.Contains(joined, "promote_target") {
		t.Fatalf("ineffective stop cleanup trace=%s", joined)
	}
}

func TestPostgreSQLFailoverExecuteAndVerifyRealOrdering(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	follower := addPostgreSQLFollower(&request)
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{finalLSN: "0/5000060", followers: make(map[model.ResourceID]model.ResourceID)}
	runner := &postgresqlExecutionRunner{state: state}
	instance := NewWithProviders(runner, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("failover execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if state.transferIsolatedID != request.Resolved.Primary.ResourceID {
		t.Fatalf("failover endpoint transfer did not receive verified isolated source: got=%s want=%s", state.transferIsolatedID, request.Resolved.Primary.ResourceID)
	}
	joined := strings.Join(state.trace, "\n")
	ordered := []string{"fence_old_primary", "authorize_transition", "promote_target", "activate_writes:pg-02", "transfer_endpoint", "finalize_transition"}
	previous := -1
	for _, expected := range ordered {
		index := strings.Index(joined, expected)
		if index < 0 || index <= previous {
			t.Fatalf("unsafe or missing %q in PostgreSQL failover trace:\n%s", expected, joined)
		}
		previous = index
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("failover verify=%+v err=%v trace=%v", verification, err, state.trace)
	}
	if state.verifyIsolatedID != request.Resolved.Primary.ResourceID {
		t.Fatalf("failover endpoint verification did not receive verified isolated source: got=%s want=%s", state.verifyIsolatedID, request.Resolved.Primary.ResourceID)
	}
	state.followers[follower.ResourceID] = request.Resolved.Primary.ResourceID
	verification, err = instance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify failed follower source: %v", err)
	}
	if verification.Passed {
		t.Fatalf("failover verification passed while sibling follows old primary: %+v", verification.Checks)
	}
}

func TestPostgreSQLFailoverSkipsFollowerAlreadyUnavailableAtDecisionTime(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	follower := addPostgreSQLFollower(&request)
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == follower.ResourceID {
			request.Resolved.Snapshot.Instances[index].Role = model.RoleUnknown
			request.Resolved.Snapshot.Instances[index].Health = model.Health{
				State: model.HealthUnhealthy, Summary: "service unavailable", ObservedAt: request.Resolved.Snapshot.ObservedAt,
			}
		}
	}
	for index := range request.Resolved.Snapshot.Probes {
		if request.Resolved.Snapshot.Probes[index].InstanceID == follower.ResourceID {
			request.Resolved.Snapshot.Probes[index].Outcome = model.ProbeOutcomeDatabaseUnavailable
			request.Resolved.Snapshot.Probes[index].Health = model.Health{
				State: model.HealthUnhealthy, Summary: "service unavailable", ObservedAt: request.Resolved.Snapshot.ObservedAt,
			}
		}
	}
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{
		finalLSN: "0/5000060", followers: make(map[model.ResourceID]model.ResourceID),
		repointErr: errors.New("unavailable follower must not be repointed during core failover"),
	}
	instance := NewWithProviders(
		&postgresqlExecutionRunner{state: state},
		&postgresqlExecutionEndpointProvider{state: state},
		&postgresqlExecutionNodeController{state: state},
		&postgresqlExecutionFailoverSafety{state: state},
	)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	for _, step := range plan.Steps {
		if step.Name == "repoint_follower_"+string(follower.ResourceID) {
			t.Fatalf("unavailable follower was included in the failover mutation plan: %+v", plan.Steps)
		}
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}

	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("failover execute=%+v err=%v trace=%v", execution, err, state.trace)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("core failover must verify with an already unavailable follower: verification=%+v err=%v", verification, err)
	}
	if status := postgresqlOperationCheckStatus(t, verification.Checks, "standby_unavailable_"+string(follower.ResourceID)); status != model.CheckWarn {
		t.Fatalf("unavailable follower must be reported as a warning, got %s: %+v", status, verification.Checks)
	}
}

func TestPostgreSQLFailoverContinuationSkipsExpiredPrecheckAfterPromotion(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	follower := addPostgreSQLFollower(&request)
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{
		finalLSN:   "0/5000060",
		followers:  make(map[model.ResourceID]model.ResourceID),
		repointErr: errors.New("follower became unavailable after promotion"),
	}
	instance := NewWithProviders(
		&postgresqlExecutionRunner{state: state},
		&postgresqlExecutionEndpointProvider{state: state},
		&postgresqlExecutionNodeController{state: state},
		&postgresqlExecutionFailoverSafety{state: state},
	)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	progress := &postgresqlProgressCollector{steps: map[string]string{
		"fence_old_primary":           "completed",
		"verify_old_primary_fenced":   "completed",
		"authorize_target_transition": "completed",
		"promote_target":              "completed",
		"activate_target_writes":      "completed",
		"transfer_writer_endpoint":    "completed",
	}}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = progress
	state.fenced = true
	state.promoted = true
	state.endpoint = true

	request.Resolved.Target.Role = model.RolePrimary
	request.Resolved.Target.Replication = model.ReplicationStatus{}
	for index := range request.Resolved.Snapshot.Instances {
		snapshotInstance := &request.Resolved.Snapshot.Instances[index]
		switch snapshotInstance.ResourceID {
		case request.Resolved.Target.ResourceID:
			snapshotInstance.Role = model.RolePrimary
			snapshotInstance.Replication = model.ReplicationStatus{}
		case follower.ResourceID:
			snapshotInstance.Role = model.RoleUnknown
			snapshotInstance.Health = model.Health{State: model.HealthUnhealthy, Summary: "service unavailable", ObservedAt: request.Resolved.Snapshot.ObservedAt}
		}
	}
	for index := range request.Resolved.Snapshot.Probes {
		if request.Resolved.Snapshot.Probes[index].InstanceID == follower.ResourceID {
			request.Resolved.Snapshot.Probes[index].Outcome = model.ProbeOutcomeDatabaseUnavailable
			request.Resolved.Snapshot.Probes[index].Health = model.Health{State: model.HealthUnhealthy, Summary: "service unavailable", ObservedAt: request.Resolved.Snapshot.ObservedAt}
		}
	}

	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("post-promotion continuation reran an expired precheck: execution=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if strings.Contains(strings.Join(state.trace, ","), "repoint:"+string(follower.ResourceID)) {
		t.Fatalf("continuation tried to mutate an unavailable follower: %v", state.trace)
	}
}

func TestPostgreSQLFailoverContinuationRestoresMissingRecordedWriterEndpoint(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.ReplicationCredentials = adapter.Credentials{Username: "replicator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	request.Resolved.ReplicationCredentials = request.ReplicationCredentials
	state := &postgresqlExecutionState{
		finalLSN: "0/5000060", followers: make(map[model.ResourceID]model.ResourceID),
	}
	instance := NewWithProviders(
		&postgresqlExecutionRunner{state: state},
		&postgresqlExecutionEndpointProvider{state: state},
		&postgresqlExecutionNodeController{state: state},
		&postgresqlExecutionFailoverSafety{state: state},
	)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	progress := &postgresqlProgressCollector{steps: make(map[string]string)}
	for _, step := range plan.Steps {
		if step.Mutating {
			progress.steps[step.Name] = "completed"
		}
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = progress
	state.fenced = true
	state.promoted = true
	state.endpoint = false
	request.Resolved.Target.Role = model.RolePrimary
	request.Resolved.Target.Replication = model.ReplicationStatus{}
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Target.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}

	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("continuation did not reconcile missing endpoint: execution=%+v err=%v trace=%v", execution, err, state.trace)
	}
	if !state.endpoint {
		t.Fatal("continuation trusted historical progress instead of restoring the missing writer endpoint")
	}
	if !strings.Contains(strings.Join(state.trace, ","), "transfer_endpoint") {
		t.Fatalf("continuation did not perform an idempotent endpoint transfer: %v", state.trace)
	}
}

func TestPostgreSQLFailoverBlocksBeforeFenceWhenLiveTargetIdentityChanged(t *testing.T) {
	request := postgresqlFailoverOperationRequest()
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	state := &postgresqlExecutionState{finalLSN: "0/5000060", targetID: model.NewResourceID()}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, &postgresqlExecutionEndpointProvider{state: state}, &postgresqlExecutionNodeController{state: state}, &postgresqlExecutionFailoverSafety{state: state})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	request.Progress = &postgresqlProgressCollector{}

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status == model.OperationRunning {
		t.Fatalf("failover accepted changed live target: execution=%+v err=%v", execution, err)
	}
	if strings.Contains(strings.Join(state.trace, ","), "fence_old_primary") {
		t.Fatalf("old primary was fenced before live target identity validation: %v", state.trace)
	}
}

func TestPostgreSQLRepairCollectBlocksChangedLiveTargetIdentity(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationReplicationRepair)
	request.Parameters = map[string]string{"action": postgresqlRepairCollect}
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	state := &postgresqlExecutionState{targetID: model.NewResourceID()}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, postgresqlEndpointStub{executable: true}, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build repair plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status == model.OperationRunning || execution.Status == model.OperationSucceeded {
		t.Fatalf("repair accepted changed live target: execution=%+v err=%v", execution, err)
	}
}

func TestPostgreSQLRepairVerifyRejectsChangedLiveTargetIdentity(t *testing.T) {
	request := postgresqlOperationRequest(model.OperationReplicationRepair)
	request.Parameters = map[string]string{"action": postgresqlRepairCollect}
	request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
	request.Resolved.Credentials = request.Credentials
	state := &postgresqlExecutionState{targetID: model.NewResourceID()}
	instance := NewWithProviders(&postgresqlExecutionRunner{state: state}, postgresqlEndpointStub{executable: true}, &postgresqlExecutionNodeController{state: state}, postgresqlFailoverSafetyStub{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build repair plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	verification, err := instance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("repair verify: %v", err)
	}
	if verification.Passed {
		t.Fatalf("repair verification accepted changed live identity: %+v", verification.Checks)
	}
}
