package oracle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestCLISQLPlusRunnerKeepsPasswordOutOfProcessArguments(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "sqlplus")
	argumentsFile := filepath.Join(directory, "arguments")
	stdinFile := filepath.Join(directory, "stdin")
	script := `#!/bin/sh
printf '%s\n' "$@" > "${CG_ORACLE_ARGUMENTS}"
cat > "${CG_ORACLE_STDIN}"
printf 'CGPROD|PRIMARY\n'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write sqlplus stub: %v", err)
	}
	t.Setenv("CG_ORACLE_ARGUMENTS", argumentsFile)
	t.Setenv("CG_ORACLE_STDIN", stdinFile)
	secret := "oracle-secret-not-for-argv"
	output, err := (CLISQLPlusRunner{Binary: binary}).QuerySQLPlus(
		context.Background(),
		adapter.Endpoint{IPAddress: "192.0.2.11", Port: 1521},
		adapter.Credentials{Username: "sys", Password: secret, Database: "CGPROD"},
		"SELECT name FROM v$database;",
	)
	if err != nil {
		t.Fatalf("query sqlplus: %v", err)
	}
	if len(output) != 1 || output[0] != "CGPROD|PRIMARY" {
		t.Fatalf("unexpected SQLPlus output: %v", output)
	}
	arguments, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatalf("read SQLPlus arguments: %v", err)
	}
	if strings.Contains(string(arguments), secret) || !strings.Contains(string(arguments), "/nolog") {
		t.Fatalf("unsafe SQLPlus arguments: %q", arguments)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read SQLPlus stdin: %v", err)
	}
	if !strings.Contains(string(stdin), "CONNECT sys/"+secret+"@192.0.2.11:1521/CGPROD as sysdba") {
		t.Fatalf("SQLPlus connection was not sent through stdin: %q", stdin)
	}
}

func TestOracleCLIRunnersRedactPasswordsFromErrors(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "oracle-cli")
	secret := "oracle-error-secret"
	script := `#!/bin/sh
cat >/dev/null
printf 'ORA-01017 credential=%s\n' "${CG_ORACLE_TEST_SECRET}" >&2
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write Oracle CLI stub: %v", err)
	}
	t.Setenv("CG_ORACLE_TEST_SECRET", secret)
	endpoint := adapter.Endpoint{IPAddress: "192.0.2.11", Port: 1521}
	credentials := adapter.Credentials{Username: "sys", Password: secret, Database: "CGPROD"}

	_, sqlErr := (CLISQLPlusRunner{Binary: binary}).QuerySQLPlus(context.Background(), endpoint, credentials, "SELECT 1 FROM dual;")
	if sqlErr == nil || strings.Contains(sqlErr.Error(), secret) || !strings.Contains(sqlErr.Error(), "[REDACTED]") {
		t.Fatalf("unsafe SQLPlus error: %v", sqlErr)
	}
	_, brokerErr := (CLIBrokerRunner{Binary: binary}).RunDGMGRL(context.Background(), endpoint, credentials, []string{"SHOW CONFIGURATION"})
	if brokerErr == nil || strings.Contains(brokerErr.Error(), secret) || !strings.Contains(brokerErr.Error(), "[REDACTED]") {
		t.Fatalf("unsafe DGMGRL error: %v", brokerErr)
	}
}

type brokerRunnerStub struct {
	executable bool
	endpoint   adapter.Endpoint
	commands   []string
	output     []string
}

func (stub *brokerRunnerStub) Executable(context.Context) bool { return stub.executable }
func (stub *brokerRunnerStub) RunDGMGRL(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, commands []string) ([]string, error) {
	stub.endpoint = endpoint
	stub.commands = append([]string{}, commands...)
	if len(stub.output) > 0 {
		return stub.output, nil
	}
	return []string{"Configuration Status: SUCCESS", "Database Role: PRIMARY"}, nil
}

type sqlPlusRunnerStub struct {
	executable bool
	endpoint   adapter.Endpoint
	statement  string
	output     []string
}

type brokerControllerStub struct {
	executable bool
	checks     []model.Check
	statuses   map[model.ResourceID]BrokerControlStatus
	sequences  map[model.ResourceID][]BrokerControlStatus
	statusCall map[model.ResourceID]int
	target     string
	leaseID    model.ResourceID
	events     *[]string
}

type oracleEndpointProviderStub struct {
	executable  bool
	checks      []model.Check
	verifyCheck model.Check
	events      *[]string
	transferErr error
}

func (stub *oracleEndpointProviderStub) Executable(context.Context) bool { return stub.executable }
func (stub *oracleEndpointProviderStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	if len(stub.checks) > 0 {
		return append([]model.Check{}, stub.checks...)
	}
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "VIP is owned by the current primary"}}
}
func (stub *oracleEndpointProviderStub) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	if stub.events != nil {
		*stub.events = append(*stub.events, "authorize_endpoint")
	}
	guarded, cancel := context.WithCancel(ctx)
	return adapter.TransitionAuthorization{
		Context: guarded,
		Cancel:  cancel,
		Abort: func(context.Context) error {
			if stub.events != nil {
				*stub.events = append(*stub.events, "abort_endpoint")
			}
			return nil
		},
		Finalize: func(context.Context) error {
			if stub.events != nil {
				*stub.events = append(*stub.events, "finalize_endpoint")
			}
			return nil
		},
		LeaseID: model.NewResourceID(),
	}, nil
}
func (stub *oracleEndpointProviderStub) Transfer(context.Context, adapter.ResolvedOperation) error {
	if stub.events != nil {
		*stub.events = append(*stub.events, "transfer_endpoint")
	}
	return stub.transferErr
}
func (stub *oracleEndpointProviderStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	if stub.events != nil {
		*stub.events = append(*stub.events, "verify_endpoint")
	}
	if stub.verifyCheck.Name != "" {
		return stub.verifyCheck
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "VIP follows the Oracle primary"}
}

type parallelVerificationController struct {
	started  chan model.ResourceID
	release  chan struct{}
	statuses map[model.ResourceID]BrokerControlStatus
}

func (controller *parallelVerificationController) Executable(context.Context) bool { return true }
func (controller *parallelVerificationController) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "oracle_agent", Status: model.CheckPass}}
}
func (controller *parallelVerificationController) Switchover(context.Context, adapter.ResolvedOperation, model.ResourceID) error {
	return nil
}
func (controller *parallelVerificationController) Status(
	ctx context.Context,
	_ adapter.ResolvedOperation,
	instance model.DatabaseInstance,
	_ string,
) (BrokerControlStatus, error) {
	controller.started <- instance.ResourceID
	select {
	case <-controller.release:
		return controller.statuses[instance.ResourceID], nil
	case <-ctx.Done():
		return BrokerControlStatus{}, ctx.Err()
	}
}

type oracleDiscoveryProviderStub struct {
	executable bool
	result     adapter.DiscoveryResult
	request    adapter.DiscoverRequest
}

func (stub *oracleDiscoveryProviderStub) Executable(context.Context) bool { return stub.executable }
func (stub *oracleDiscoveryProviderStub) Discover(_ context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	stub.request = request
	return stub.result, nil
}

func (stub *brokerControllerStub) Executable(context.Context) bool { return stub.executable }
func (stub *brokerControllerStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	if len(stub.checks) > 0 {
		return append([]model.Check{}, stub.checks...)
	}
	return []model.Check{{Name: "oracle_agent", Status: model.CheckPass, Message: "broker status is safe"}}
}
func (stub *brokerControllerStub) Switchover(_ context.Context, resolved adapter.ResolvedOperation, leaseID model.ResourceID) error {
	if stub.events != nil {
		*stub.events = append(*stub.events, "broker_switchover")
	}
	stub.target = oracleBrokerName(resolved.Target)
	stub.leaseID = leaseID
	return nil
}
func (stub *brokerControllerStub) Status(_ context.Context, _ adapter.ResolvedOperation, instance model.DatabaseInstance, _ string) (BrokerControlStatus, error) {
	if sequence := stub.sequences[instance.ResourceID]; len(sequence) > 0 {
		if stub.statusCall == nil {
			stub.statusCall = make(map[model.ResourceID]int)
		}
		index := stub.statusCall[instance.ResourceID]
		stub.statusCall[instance.ResourceID] = index + 1
		if index >= len(sequence) {
			index = len(sequence) - 1
		}
		return sequence[index], nil
	}
	if status, found := stub.statuses[instance.ResourceID]; found {
		return status, nil
	}
	return BrokerControlStatus{Database: oracleBrokerName(instance), Role: strings.ToUpper(string(instance.Role)), ConfigurationStatus: "SUCCESS"}, nil
}

func (stub *sqlPlusRunnerStub) Executable(context.Context) bool { return stub.executable }
func (stub *sqlPlusRunnerStub) QuerySQLPlus(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, statement string) ([]string, error) {
	stub.endpoint = endpoint
	stub.statement = statement
	return append([]string{}, stub.output...), nil
}

func oracleOperationRequest(kind model.OperationKind) adapter.OperationRequest {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 1},
		Engine:       model.EngineOracle, EngineIdentity: model.EngineIdentity{"dbid": "1234567890", "db_unique_name": "CGPROD"}, DisplayName: "oracle-dg-prod",
	}
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: primaryID, MetadataRevision: 1},
		ClusterID:    clusterID, Engine: model.EngineOracle, EngineIdentity: model.EngineIdentity{"dbid": "1234567890", "db_unique_name": "CGPROD1"},
		DisplayName: "CGPROD1", Hostname: "ora01", IPAddress: "192.0.2.11", Port: 1521, Role: model.RolePrimary,
		Health: model.Health{State: model.HealthHealthy}, EngineMetadata: map[string]string{"data_guard_broker": "enabled"},
	}
	target := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: targetID, MetadataRevision: 1},
		ClusterID:    clusterID, Engine: model.EngineOracle, EngineIdentity: model.EngineIdentity{"dbid": "1234567890", "db_unique_name": "CGPROD2"},
		DisplayName: "CGPROD2", Hostname: "ora02", IPAddress: "192.0.2.12", Port: 1521, Role: model.RoleStandby,
		Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true, EngineMetadata: map[string]string{"data_guard_broker": "enabled"},
	}
	operationID := model.NewResourceID()
	return adapter.OperationRequest{
		Operation: model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: operationID}, ClusterID: clusterID, Engine: model.EngineOracle, Kind: kind, RequestedBy: "dba"},
		TargetID:  targetID,
		Resolved: &adapter.ResolvedOperation{
			OperationID: operationID, ObservationToken: "oracle-observation",
			Cluster: cluster, Snapshot: model.TopologySnapshot{ClusterID: clusterID, Instances: []model.DatabaseInstance{primary, target}},
			Primary: primary, Target: target, PlanDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Credentials: adapter.Credentials{Username: "sys", Password: "secret", Database: "CGPROD"},
	}
}

func TestOracleDGBrokerCapabilitiesFailClosedWithoutRunner(t *testing.T) {
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, UnsupportedBrokerController{})
	capabilities := instance.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityPrecheck) || !capabilities.Supports(adapter.CapabilityPlan) {
		t.Fatalf("Oracle precheck and plan should be available: %+v", capabilities.Features)
	}
	if capabilities.Supports(adapter.CapabilityExecute) {
		t.Fatalf("Oracle execution must fail closed without DGMGRL runner: %+v", capabilities.Features)
	}
	if _, err := instance.Execute(context.Background(), oracleOperationRequest(model.OperationSwitchover)); !strings.Contains(err.Error(), adapter.ErrUnsupported.Error()) {
		t.Fatalf("execute without runner error=%v", err)
	}
}

func TestOracleDGBrokerDiscoveryCanUseRestrictedAgentProvider(t *testing.T) {
	clusterID := model.NewResourceID()
	provider := &oracleDiscoveryProviderStub{
		executable: true,
		result: adapter.DiscoveryResult{Instance: model.DatabaseInstance{
			ClusterID: clusterID, Engine: model.EngineOracle,
			EngineIdentity: model.EngineIdentity{
				"dbid": "1234567890", "db_unique_name": "REPORTDB", "instance_name": "mesdb",
			},
			Role: model.RoleStandby,
		}},
	}
	instance := NewWithDiscoveryProvider(
		UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, provider, UnsupportedBrokerController{},
	)
	if !instance.Capabilities(context.Background()).Supports(adapter.CapabilityDiscover) {
		t.Fatal("restricted Oracle agent discovery must be advertised")
	}
	request := adapter.DiscoverRequest{
		ClusterID: clusterID,
		Endpoint:  adapter.Endpoint{Hostname: "orcl", IPAddress: "192.0.2.11", Port: 1521},
	}
	discovery, err := instance.Discover(context.Background(), request)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Instance.EngineIdentity["db_unique_name"] != "REPORTDB" ||
		provider.request.Endpoint.IPAddress != "192.0.2.11" {
		t.Fatalf("discovery=%+v request=%+v", discovery, provider.request)
	}
}

func TestOracleTopologyDefersLinkUntilCompleteClusterObservation(t *testing.T) {
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, UnsupportedBrokerController{})
	discovery := adapter.DiscoveryResult{Instance: model.DatabaseInstance{
		Engine: model.EngineOracle,
		EngineIdentity: model.EngineIdentity{
			"dbid": "1234567890", "db_unique_name": "REPORTDB", "instance_name": "mesdb",
		},
		Role: model.RoleStandby,
	}}
	topology, err := instance.Topology(context.Background(), adapter.DiscoverRequest{}, discovery)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(topology.Links) != 0 {
		t.Fatalf("member-local Oracle discovery emitted an incomplete source identity: %+v", topology.Links)
	}
}

func TestOracleDGBrokerSwitchoverUsesRestrictedControllerAndVerifiesBothRoles(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	zero := int64(0)
	controller := &brokerControllerStub{executable: true, statuses: map[model.ResourceID]BrokerControlStatus{
		request.Resolved.Primary.ResourceID: {
			Database: "CGPROD1", Role: "PHYSICAL STANDBY", ConfigurationStatus: "SUCCESS",
		},
		request.Resolved.Target.ResourceID: {
			Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
			TransportLagSeconds: &zero, ApplyLagSeconds: &zero,
		},
	}}
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
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
	if plan.ObservationToken != request.Resolved.ObservationToken ||
		plan.ResourceRevisions[request.Resolved.Cluster.ResourceID] != request.Resolved.Cluster.MetadataRevision ||
		plan.ResourceRevisions[request.Resolved.Primary.ResourceID] != request.Resolved.Primary.MetadataRevision ||
		plan.ResourceRevisions[request.Resolved.Target.ResourceID] != request.Resolved.Target.MetadataRevision {
		t.Fatalf("Oracle plan did not pin the observed resources: %+v", plan)
	}
	recomputed, err := oracleOperationPlanDigest(plan)
	if err != nil || plan.Digest == "" || recomputed != plan.Digest {
		t.Fatalf("Oracle plan digest=%q recomputed=%q err=%v", plan.Digest, recomputed, err)
	}
	leaseID := model.NewResourceID()
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	execution, err := instance.Execute(adapter.WithOperationLeaseID(context.Background(), leaseID), request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationRunning || controller.target != "CGPROD2" || controller.leaseID != leaseID {
		t.Fatalf("unexpected execution=%+v controller=%+v", execution, controller)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v", verification, err)
	}
}

func TestOracleDGBrokerSwitchoverCouplesRoleAndVIPTransition(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	zero := int64(0)
	events := make([]string, 0, 5)
	controller := &brokerControllerStub{
		executable: true,
		events:     &events,
		statuses: map[model.ResourceID]BrokerControlStatus{
			request.Resolved.Primary.ResourceID: {
				Database: "CGPROD1", Role: "PHYSICAL STANDBY", ConfigurationStatus: "SUCCESS",
			},
			request.Resolved.Target.ResourceID: {
				Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
				TransportLagSeconds: &zero, ApplyLagSeconds: &zero,
			},
		},
	}
	endpointProvider := &oracleEndpointProviderStub{executable: true, events: &events}
	instance := NewWithRuntimeProviders(
		UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, UnsupportedBrokerDiscoveryProvider{},
		controller, endpointProvider,
	)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	stepNames := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		stepNames = append(stepNames, step.Name)
	}
	for _, expected := range []string{"authorize_target_transition", "dgbroker_switchover", "transfer_writer_endpoint", "verify_writer_endpoint"} {
		if !strings.Contains(strings.Join(stepNames, ","), expected) {
			t.Fatalf("Oracle VIP-coupled plan is missing %s: %v", expected, stepNames)
		}
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	execution, err := instance.Execute(
		adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()),
		request,
	)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute=%+v err=%v", execution, err)
	}
	if got, want := strings.Join(events, ","), "authorize_endpoint,broker_switchover,transfer_endpoint,verify_endpoint,finalize_endpoint"; got != want {
		t.Fatalf("Oracle role/VIP order=%q want %q", got, want)
	}
	if controller.leaseID == "" {
		t.Fatal("Broker switchover did not receive the endpoint transition lease")
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v", verification, err)
	}
	var endpointVerified bool
	for _, check := range verification.Checks {
		if check.Name == "writer_endpoint_owner" && check.Status == model.CheckPass {
			endpointVerified = true
		}
	}
	if !endpointVerified {
		t.Fatalf("Oracle verification omitted VIP ownership: %+v", verification.Checks)
	}
}

func TestOracleDGBrokerSwitchoverBlocksUnsafeVIPOwnership(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	endpointProvider := &oracleEndpointProviderStub{
		executable: true,
		checks: []model.Check{{
			Name: "writer_endpoint_provider", Status: model.CheckFail,
			Message: "VIP must be owned only by the current primary",
		}},
	}
	instance := NewWithRuntimeProviders(
		UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, UnsupportedBrokerDiscoveryProvider{},
		&brokerControllerStub{executable: true}, endpointProvider,
	)
	checks, err := instance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasFailedCheck(checks) {
		t.Fatalf("unsafe Oracle VIP ownership did not block: %+v", checks)
	}
}

func TestOracleDGBrokerSwitchoverReportsIndeterminateWhenVIPTransferFails(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	instance := NewWithRuntimeProviders(
		UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, UnsupportedBrokerDiscoveryProvider{},
		&brokerControllerStub{executable: true},
		&oracleEndpointProviderStub{executable: true, transferErr: fmt.Errorf("target VIP acquire failed")},
	)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	execution, err := instance.Execute(
		adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()),
		request,
	)
	if err == nil || execution.Status != model.OperationIndeterminate ||
		!strings.Contains(execution.Message, "VIP") {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
}

func TestOracleVerificationChecksBothBrokerMembersConcurrently(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	zero := int64(0)
	controller := &parallelVerificationController{
		started: make(chan model.ResourceID, 2),
		release: make(chan struct{}),
		statuses: map[model.ResourceID]BrokerControlStatus{
			request.Resolved.Primary.ResourceID: {
				Database: "CGPROD1", Role: "PHYSICAL STANDBY", ConfigurationStatus: "SUCCESS",
			},
			request.Resolved.Target.ResourceID: {
				Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
				TransportLagSeconds: &zero, ApplyLagSeconds: &zero,
			},
		},
	}
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
	instance.verifyMaxAttempts = 1
	bindOracleOperationTestPlan(t, instance, &request)
	done := make(chan model.Verification, 1)
	go func() {
		verification, _ := instance.Verify(context.Background(), request)
		done <- verification
	}()

	var first model.ResourceID
	select {
	case first = <-controller.started:
	case <-time.After(time.Second):
		close(controller.release)
		t.Fatal("Oracle verification did not start member probes")
	}
	select {
	case second := <-controller.started:
		if first == second {
			close(controller.release)
			t.Fatalf("Oracle verification queried the same member twice: %s", first)
		}
	case <-time.After(100 * time.Millisecond):
		close(controller.release)
		<-done
		t.Fatal("Oracle verification serialized Broker member checks")
	}
	close(controller.release)
	if verification := <-done; !verification.Passed {
		t.Fatalf("parallel Oracle verification failed: %+v", verification)
	}
}

func TestOracleDGBrokerSwitchoverRejectsMissingOperationLease(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, &brokerControllerStub{executable: true})
	bindOracleOperationTestPlan(t, instance, &request)
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
}

func TestOracleDGBrokerVerifyWaitsForFormerPrimaryApplyConvergence(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	zero := int64(0)
	one := int64(1)
	controller := &brokerControllerStub{
		executable: true,
		statuses: map[model.ResourceID]BrokerControlStatus{
			request.Resolved.Primary.ResourceID: {
				Database: "CGPROD1", Role: "PHYSICAL STANDBY", ConfigurationStatus: "SUCCESS",
			},
		},
		sequences: map[model.ResourceID][]BrokerControlStatus{
			request.Resolved.Target.ResourceID: {
				{
					Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
					TransportLagSeconds: &one, ApplyLagSeconds: &one,
				},
				{
					Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS",
					TransportLagSeconds: &zero, ApplyLagSeconds: &zero,
				},
			},
		},
	}
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
	instance.verifyRetryDelay = 0
	bindOracleOperationTestPlan(t, instance, &request)
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%+v err=%v", verification, err)
	}
	if controller.statusCall[request.Resolved.Target.ResourceID] != 2 {
		t.Fatalf("target status calls=%d", controller.statusCall[request.Resolved.Target.ResourceID])
	}
}

func TestOracleDGBrokerSwitchoverReturnsErrorWhenFinalPrecheckBlocks(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	controller := &brokerControllerStub{
		executable: true,
		checks: []model.Check{{
			Name: "oracle_target_zero_lag", Status: model.CheckFail,
			Message: "target standby lag changed before execution",
		}},
	}
	instance := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
	finalChecks := controller.checks
	controller.checks = nil
	bindOracleOperationTestPlan(t, instance, &request)
	controller.checks = finalChecks
	execution, err := instance.Execute(
		adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()),
		request,
	)
	if err == nil || execution.Status != model.OperationBlocked {
		t.Fatalf("blocked final precheck execution=%+v err=%v", execution, err)
	}
	if controller.target != "" {
		t.Fatalf("blocked final precheck reached Broker mutation target=%q", controller.target)
	}
}

func TestOracleDGBrokerPrecheckBlocksWithoutBrokerEvidence(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	delete(request.Resolved.Target.EngineMetadata, "data_guard_broker")
	checks, err := NewWithProviders(&brokerRunnerStub{executable: true}, UnsupportedSQLPlusRunner{}, &brokerControllerStub{executable: true}).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !hasFailedCheck(checks) {
		t.Fatalf("missing broker evidence should block: %+v", checks)
	}
}

func TestOracleDGBrokerSwitchoverUsesLiveBrokerReadinessForHealthyStandby(t *testing.T) {
	request := oracleOperationRequest(model.OperationSwitchover)
	request.Resolved.Target.PromotionEligible = false
	lag := int64(1)
	request.Resolved.Target.Replication.LagSeconds = &lag
	controller := &brokerControllerStub{
		executable: true,
		checks: []model.Check{
			{Name: "oracle_target_ready", Status: model.CheckPass, Message: "Broker target is ready"},
			{Name: "oracle_target_zero_lag", Status: model.CheckWarn, Message: "known transient lag"},
		},
	}
	checks, err := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("Broker-ready planned switchover was blocked by stale candidate eligibility: %+v", checks)
		}
	}
}

func TestOracleDGBrokerDiscoverAndCandidateAssessment(t *testing.T) {
	output := []string{
		"DB Unique Name: CGPROD2",
		"Database Role: PHYSICAL STANDBY",
		"Intended State: APPLY-ON",
		"Transport Lag: 0 seconds",
		"Apply Lag: 0 seconds",
		"Database Status: SUCCESS",
	}
	instance := NewWithBrokerRunner(&brokerRunnerStub{executable: true, output: output})
	clusterID := model.NewResourceID()
	discovery, err := instance.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID:   clusterID,
		Endpoint:    adapter.Endpoint{Hostname: "ora02", IPAddress: "192.0.2.12", Port: 1521},
		Credentials: adapter.Credentials{Username: "sys", Password: "secret", Database: "CGPROD2"},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Instance.Role != model.RoleStandby || !discovery.Instance.PromotionEligible || discovery.Instance.EngineIdentity["db_unique_name"] != "CGPROD2" {
		t.Fatalf("unexpected Oracle discovery: %+v", discovery.Instance)
	}
	primary := oracleOperationRequest(model.OperationSwitchover).Resolved.Primary
	primary.ClusterID = clusterID
	assessments, err := instance.EvaluateCandidates(context.Background(), adapter.CandidateRequest{Primary: primary, Instances: []model.DatabaseInstance{primary, discovery.Instance}, Policy: model.CandidatePolicy{MaximumLagSeconds: 5}})
	if err != nil || len(assessments) != 1 || !assessments[0].Eligible || assessments[0].Rank != 1 {
		t.Fatalf("assessments=%+v err=%v", assessments, err)
	}
}

func TestOracleDGBrokerMetricsExposeBrokerHealthAndLag(t *testing.T) {
	output := []string{
		"DB Unique Name: CGPROD2",
		"Database Role: PHYSICAL STANDBY",
		"Transport Lag: 5 seconds",
		"Apply Lag: 7 seconds",
		"Database Status: SUCCESS",
	}
	instance := NewWithBrokerRunner(&brokerRunnerStub{executable: true, output: output})
	samples, err := instance.Metrics(context.Background(), adapter.DiscoverRequest{
		Endpoint:    adapter.Endpoint{Hostname: "ora02", IPAddress: "192.0.2.12", Port: 1521},
		Credentials: adapter.Credentials{Username: "sys", Password: "secret", Database: "CGPROD2"},
	})
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("unexpected metric sample count: %+v", samples)
	}
	values := samples[0].Values
	want := map[string]float64{
		"broker_status_healthy": 1,
		"transport_lag_seconds": 5,
		"apply_lag_seconds":     7,
		"role_primary":          0,
	}
	for name, expected := range want {
		if actual, ok := values[name]; !ok || actual != expected {
			t.Fatalf("%s=%v present=%t want=%v all=%+v", name, actual, ok, expected, values)
		}
	}
}

func TestOracleSQLPlusDiscoverySupportsEndpointSpecificServiceNames(t *testing.T) {
	output := []string{
		"DATABASE|1234567890|MESDB|reportdb|PHYSICAL STANDBY|MOUNTED|MAXIMUM PERFORMANCE|NOT ALLOWED",
		"INSTANCE|mesdb|orcl|19.0.0.0.0|MOUNTED",
		"PARAMETER|dg_broker_start|FALSE",
		"DATAGUARD_STAT|transport lag|+00 00:00:03",
		"DATAGUARD_STAT|apply lag|+00 00:00:05",
	}
	runner := &sqlPlusRunnerStub{executable: true, output: output}
	instance := NewWithRunners(UnsupportedBrokerRunner{}, runner)
	discovery, err := instance.Discover(context.Background(), adapter.DiscoverRequest{
		ClusterID: model.NewResourceID(),
		Endpoint:  adapter.Endpoint{Hostname: "reportdb", IPAddress: "192.168.102.211", Port: 1521},
		Credentials: adapter.Credentials{
			Username: "sys",
			Password: "secret",
		},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if runner.endpoint.IPAddress != "192.168.102.211" || !strings.Contains(runner.statement, "v$database") {
		t.Fatalf("SQLPlus runner was not used with the registered endpoint: endpoint=%+v statement=%q", runner.endpoint, runner.statement)
	}
	got := discovery.Instance
	if got.Role != model.RoleStandby || got.EngineIdentity["db_unique_name"] != "reportdb" || got.EngineIdentity["dbid"] != "1234567890" {
		t.Fatalf("unexpected SQLPlus discovery: %+v", got)
	}
	if got.EngineMetadata["data_guard_broker"] != "false" || got.PromotionEligible != true {
		t.Fatalf("unexpected SQLPlus broker/candidate state: %+v", got)
	}
	if got.Replication.LagSeconds == nil || *got.Replication.LagSeconds != 5 {
		t.Fatalf("unexpected SQLPlus lag: %+v", got.Replication)
	}
	if instance.Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatalf("SQLPlus-only Oracle adapter must not advertise mutating execution")
	}
}

func TestOracleConnectDescriptorUsesIPAddressAndEndpointHostnameAsServiceFallback(t *testing.T) {
	descriptor := oracleConnectDescriptor(adapter.Endpoint{Hostname: "reportdb", IPAddress: "192.168.102.211", Port: 1521}, adapter.Credentials{})
	if descriptor != "192.168.102.211:1521/reportdb" {
		t.Fatalf("descriptor=%q", descriptor)
	}
}
