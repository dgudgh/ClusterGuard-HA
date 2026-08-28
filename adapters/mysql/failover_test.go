package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type failoverSafetyStub struct {
	checks      []model.Check
	fenceCalls  int
	verifyCalls int
	fenceErr    error
	verify      model.Check
}

func (safety *failoverSafetyStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return append([]model.Check{}, safety.checks...)
}

func (safety *failoverSafetyStub) Fence(context.Context, adapter.ResolvedOperation) error {
	safety.fenceCalls++
	return safety.fenceErr
}

func (safety *failoverSafetyStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	safety.verifyCalls++
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

type reactivatingFailoverSemiSyncClient struct {
	*threeNodeSQLClient
	mu            sync.Mutex
	sourceEnabled bool
	sourceStatus  bool
	sourceClients int
	enableError   error
	statements    []string
}

func (client *reactivatingFailoverSemiSyncClient) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]Row, error) {
	switch query {
	case semiSyncVariablesQuery:
		client.mu.Lock()
		enabled := client.sourceEnabled
		client.mu.Unlock()
		return []Row{
			{"Variable_name": "rpl_semi_sync_source_enabled", "Value": boolString(enabled)},
			{"Variable_name": "rpl_semi_sync_replica_enabled", "Value": "ON"},
			{"Variable_name": "rpl_semi_sync_source_wait_for_replica_count", "Value": "1"},
			{"Variable_name": "rpl_semi_sync_source_timeout", "Value": "10000"},
			{"Variable_name": "rpl_semi_sync_source_wait_no_replica", "Value": "ON"},
			{"Variable_name": "rpl_semi_sync_source_wait_point", "Value": "AFTER_SYNC"},
		}, nil
	case semiSyncStatusQuery:
		client.mu.Lock()
		status := client.sourceStatus
		clients := client.sourceClients
		client.mu.Unlock()
		return []Row{
			{"Variable_name": "Rpl_semi_sync_source_status", "Value": boolString(status)},
			{"Variable_name": "Rpl_semi_sync_source_clients", "Value": strconv.Itoa(clients)},
			{"Variable_name": "Rpl_semi_sync_replica_status", "Value": "ON"},
		}, nil
	default:
		return client.threeNodeSQLClient.Query(ctx, endpoint, credentials, query)
	}
}

func (client *reactivatingFailoverSemiSyncClient) Exec(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, statement string) error {
	switch statement {
	case disableSemiSyncSource:
		client.mu.Lock()
		client.sourceEnabled = false
		client.sourceStatus = false
		client.statements = append(client.statements, statement)
		client.mu.Unlock()
		return nil
	case enableSemiSyncSource:
		client.mu.Lock()
		if client.enableError != nil {
			err := client.enableError
			client.mu.Unlock()
			return err
		}
		client.sourceEnabled = true
		client.sourceStatus = client.sourceClients > 0
		client.statements = append(client.statements, statement)
		client.mu.Unlock()
		return nil
	default:
		return client.threeNodeSQLClient.Exec(ctx, endpoint, credentials, statement)
	}
}

func (client *reactivatingFailoverSemiSyncClient) semiSyncStatements() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string{}, client.statements...)
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

func TestFailoverPrecheckRequiresReadySemiSyncCandidateWhenConfigured(t *testing.T) {
	request := failoverRequestFixture()
	addReadySemiSyncEvidence(&request.Resolved.Target, false)
	adapterInstance := NewWithSafetyProviders(
		nil,
		passingEndpointProvider(),
		UnsupportedMaintenanceStore{},
		passingFailoverSafety(),
	).RequireSemiSync(true)

	checks, err := adapterInstance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck ready semi-sync candidate: %v", err)
	}
	if !passedCheckNamed(checks, "semi_sync_durability") {
		t.Fatalf("ready semi-sync candidate was not accepted: %+v", checks)
	}

	request.Resolved.Target.EngineMetadata["semi_sync_replica_status"] = "false"
	checks, err = adapterInstance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck stopped semi-sync candidate: %v", err)
	}
	if !failedCheckNamed(checks, "semi_sync_durability") {
		t.Fatalf("stopped semi-sync candidate was not blocked: %+v", checks)
	}
}

func TestFailoverReactivatesSemiSyncBeforeEndpointTransfer(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	addReadySemiSyncEvidence(&request.Resolved.Target, false)
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Target.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}
	client := &reactivatingFailoverSemiSyncClient{
		threeNodeSQLClient: newThreeNodeSQLClient(request),
		sourceEnabled:      true,
		sourceStatus:       false,
		sourceClients:      1,
	}
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithSafetyProviders(
		client,
		provider,
		UnsupportedMaintenanceStore{},
		passingFailoverSafety(),
	).RequireSemiSync(true)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	collector := &progressCollector{}
	request.Progress = collector

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
	wantStatements := []string{disableSemiSyncSource, enableSemiSyncSource}
	if got := client.semiSyncStatements(); fmt.Sprint(got) != fmt.Sprint(wantStatements) {
		t.Fatalf("semi-sync statements=%v, want %v", got, wantStatements)
	}
	activationIndex := -1
	transferIndex := -1
	for index, step := range collector.steps {
		switch step {
		case "activate_failover_semisync_source":
			activationIndex = index
		case "transfer_failover_endpoint":
			transferIndex = index
		}
	}
	if activationIndex < 0 || transferIndex < 0 || activationIndex >= transferIndex {
		t.Fatalf("semi-sync activation must be durable before failover endpoint transfer: %v", collector.steps)
	}
}

func TestFailoverSemiSyncActivationFailureRefencesTargetBeforeEndpointTransfer(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	addReadySemiSyncEvidence(&request.Resolved.Target, false)
	for index := range request.Resolved.Snapshot.Instances {
		if request.Resolved.Snapshot.Instances[index].ResourceID == request.Resolved.Target.ResourceID {
			request.Resolved.Snapshot.Instances[index] = request.Resolved.Target
		}
	}
	client := &reactivatingFailoverSemiSyncClient{
		threeNodeSQLClient: newThreeNodeSQLClient(request),
		sourceEnabled:      true,
		sourceStatus:       false,
		sourceClients:      1,
		enableError:        errors.New("semi-sync source plugin activation failed"),
	}
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithSafetyProviders(
		client,
		provider,
		UnsupportedMaintenanceStore{},
		passingFailoverSafety(),
	).RequireSemiSync(true)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("activation failure execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if provider.transferCalls != 0 {
		t.Fatalf("endpoint transferred before semi-sync activation: calls=%d", provider.transferCalls)
	}
	target := client.nodes[request.Resolved.Target.Hostname]
	if !target.readOnly || !target.superReadOnly {
		t.Fatalf("target remained writable after semi-sync activation failure: %+v", target)
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
	endpointStep := -1
	firstFollowerStep := -1
	for index, step := range plan.Steps {
		if strings.HasPrefix(step.Name, "reparent_failover_follower_") {
			followerSteps++
			if firstFollowerStep < 0 {
				firstFollowerStep = index
			}
		}
		if step.Name == "transfer_failover_endpoint" {
			endpointStep = index
		}
	}
	if followerSteps != 1 {
		t.Fatalf("source reconnect follower steps=%d, want 1: %+v", followerSteps, plan.Steps)
	}
	if endpointStep < 0 || firstFollowerStep < 0 || endpointStep >= firstFollowerStep {
		t.Fatalf("writer endpoint must recover before follower repair: %+v", plan.Steps)
	}
}

func TestSafeLiveFailoverReplicationAllowsOnlySourceOwnedLateGTIDs(t *testing.T) {
	primary := model.DatabaseInstance{
		EngineIdentity: model.EngineIdentity{"server_uuid": primaryUUID},
		EngineMetadata: map[string]string{"gtid_executed": primaryUUID + ":1-100"},
	}
	identity := identityProbe{gtidMode: "ON"}
	base := model.ReplicationStatus{
		SourceIdentity:   model.EngineIdentity{"server_uuid": primaryUUID},
		IOThread:         model.ThreadConnecting,
		SQLThread:        model.ThreadRunning,
		ExecutedPosition: primaryUUID + ":1-100",
	}

	tests := []struct {
		name     string
		mutate   func(*model.ReplicationStatus)
		expected bool
	}{
		{
			name:     "identical snapshot",
			expected: true,
		},
		{
			name: "source-owned transactions received after primary snapshot",
			mutate: func(replication *model.ReplicationStatus) {
				replication.ExecutedPosition = primaryUUID + ":1-110"
			},
			expected: true,
		},
		{
			name: "candidate-owned errant transaction",
			mutate: func(replication *model.ReplicationStatus) {
				replication.ExecutedPosition += "," + targetUUID + ":1"
			},
		},
		{
			name: "foreign errant transaction",
			mutate: func(replication *model.ReplicationStatus) {
				replication.ExecutedPosition += "," + extraUUID + ":1"
			},
		},
		{
			name: "replication SQL error",
			mutate: func(replication *model.ReplicationStatus) {
				replication.LastSQLError = "relay log corruption"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			replication := base
			replication.SourceIdentity = model.EngineIdentity{"server_uuid": primaryUUID}
			if test.mutate != nil {
				test.mutate(&replication)
			}
			if got := safeLiveFailoverReplication(primary, identity, replication); got != test.expected {
				t.Fatalf("safe live failover replication = %t, want %t", got, test.expected)
			}
		})
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
	if endpointProvider.authorizeCalls != 1 || endpointProvider.finalizeCalls != 1 || !authorizedAtPromotion {
		t.Fatalf("failover endpoint transition was not authorized and finalized: authorize_calls=%d finalize_calls=%d authorized_at_promotion=%t", endpointProvider.authorizeCalls, endpointProvider.finalizeCalls, authorizedAtPromotion)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
}

func TestFailoverRejectsMissingTargetReplicationAccountBeforeOldPrimaryFence(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	client.replicationCredentialOK = false
	endpointProvider := &recordingEndpointProvider{}
	safety := passingFailoverSafety()
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, safety)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed || failureClass(err) != "pre_commit" {
		t.Fatalf("missing target replication account execution=%+v err=%v", execution, err)
	}
	if safety.fenceCalls != 0 || endpointProvider.authorizeCalls != 0 {
		t.Fatalf("credential precheck crossed safety boundary: fence=%d authorize=%d", safety.fenceCalls, endpointProvider.authorizeCalls)
	}
	if len(client.statements) != 0 {
		t.Fatalf("credential precheck issued mutating SQL: %v", client.statements)
	}
}

func TestFailoverVerifyRejectsWritableIneligibleInventoryNode(t *testing.T) {
	request := failoverRequestFixture()
	var sibling model.DatabaseInstance
	for index := range request.Resolved.Snapshot.Instances {
		instance := &request.Resolved.Snapshot.Instances[index]
		if instance.ResourceID == request.Resolved.Primary.ResourceID || instance.ResourceID == request.Resolved.Target.ResourceID {
			continue
		}
		instance.Maintenance = true
		instance.PromotionEligible = false
		sibling = *instance
	}
	if sibling.ResourceID == "" {
		t.Fatal("fixture has no sibling inventory node")
	}

	client := newThreeNodeSQLClient(request)
	target := client.nodes[request.Resolved.Target.Hostname]
	target.readOnly = false
	target.superReadOnly = false
	target.sourceUUID = ""
	rogueWriter := client.nodes[sibling.Hostname]
	rogueWriter.readOnly = false
	rogueWriter.superReadOnly = false

	endpointProvider := &recordingEndpointProvider{owner: request.Resolved.Target.ResourceID}
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, passingFailoverSafety())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build failover plan: %v", err)
	}
	request.Plan = &plan

	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify failover: %v", err)
	}
	if verification.Passed || !failedCheck(verification.Checks, "writable_primary_uniqueness") {
		t.Fatalf("writable ineligible inventory node escaped failover verification: %+v", verification.Checks)
	}
}

func TestFailoverFinalizeFailureRefencesPromotedTarget(t *testing.T) {
	request := failoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	endpointProvider := &recordingEndpointProvider{finalizeError: errors.New("quorum lease unavailable")}
	adapterInstance := NewWithSafetyProviders(client, endpointProvider, UnsupportedMaintenanceStore{}, passingFailoverSafety())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("finalize failure execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if endpointProvider.finalizeCalls != 1 {
		t.Fatalf("endpoint transition finalize calls=%d, want 1", endpointProvider.finalizeCalls)
	}
	target := client.nodes[request.Resolved.Target.Hostname]
	if !target.readOnly || !target.superReadOnly {
		t.Fatalf("target remained writable after endpoint lease finalize failure: %+v", target)
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
	if safety.fenceCalls != 1 {
		t.Fatalf("durably fenced failover was fenced again during resume: calls=%d", safety.fenceCalls)
	}
	if safety.verifyCalls < 2 {
		t.Fatalf("resume did not reverify old-primary isolation: calls=%d", safety.verifyCalls)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("resume verification=%+v err=%v", verification, err)
	}
}
