package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type switchoverSQLClient struct {
	mu                    sync.Mutex
	primaryHost           string
	targetHost            string
	version               string
	primaryReadOnly       bool
	primarySuperReadOnly  bool
	primaryReplication    bool
	primarySourceUUID     string
	primaryIO             bool
	primarySQL            bool
	targetReadOnly        bool
	targetSuperReadOnly   bool
	targetReplication     bool
	targetLag             int64
	primaryUUID           string
	targetUUID            string
	primaryGTID           string
	targetExecuted        string
	executed              []string
	failStatement         string
	failStatementConsumed bool
	failAfterStatement    string
	failTargetStatement   string
	onTargetPromotion     func()
	onTargetReset         func()
}

func newSwitchoverSQLClient(request adapter.OperationRequest) *switchoverSQLClient {
	return &switchoverSQLClient{
		primaryHost:          request.Resolved.Primary.Hostname,
		targetHost:           request.Resolved.Target.Hostname,
		version:              request.Resolved.Target.EngineMetadata["version"],
		primaryReadOnly:      false,
		primarySuperReadOnly: false,
		targetReadOnly:       true,
		targetSuperReadOnly:  true,
		targetReplication:    true,
		primaryUUID:          primaryUUID,
		targetUUID:           targetUUID,
		primaryGTID:          request.Resolved.Primary.EngineMetadata["gtid_executed"],
		targetExecuted:       request.Resolved.Target.Replication.ExecutedPosition,
	}
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (client *switchoverSQLClient) Query(ctx context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	host := endpoint.Hostname
	if host == "" {
		host = endpoint.IPAddress
	}
	if query == identityQuery {
		isPrimary := host == client.primaryHost
		readOnly := client.targetReadOnly
		superReadOnly := client.targetSuperReadOnly
		serverUUID := client.targetUUID
		serverID := "11"
		gtid := client.targetExecuted
		if isPrimary {
			readOnly = client.primaryReadOnly
			superReadOnly = client.primarySuperReadOnly
			serverUUID = client.primaryUUID
			serverID = "10"
			gtid = client.primaryGTID
		}
		return []Row{{
			"server_uuid": serverUUID, "hostname": host, "port": "3306", "server_id": serverID,
			"version": client.version, "read_only": boolString(readOnly), "super_read_only": boolString(superReadOnly),
			"gtid_mode": "ON", "gtid_executed": gtid, "log_bin": "ON", "binlog_format": "ROW",
		}}, nil
	}
	if query == replicaStatusQuery {
		if strings.HasPrefix(client.version, "5.7.") {
			return nil, &QueryError{Code: 1064, Output: "ERROR 1064 unsupported", Err: errors.New("exit status 1")}
		}
		return client.replicationRows(host), nil
	}
	if query == slaveStatusQuery {
		return client.replicationRows(host), nil
	}
	if query == gtidPositionQuery {
		return []Row{{"gtid_executed": client.primaryGTID}}, nil
	}
	if strings.HasPrefix(query, "SELECT WAIT_FOR_EXECUTED_GTID_SET(") {
		if client.targetExecuted == client.primaryGTID {
			return []Row{{"wait_result": "0"}}, nil
		}
		return []Row{{"wait_result": "1"}}, nil
	}
	return nil, fmt.Errorf("unexpected query %q", query)
}

func (client *switchoverSQLClient) replicationRows(host string) []Row {
	configured := client.targetReplication
	sourceUUID := client.primaryUUID
	lag := client.targetLag
	executed := client.targetExecuted
	ioRunning := true
	sqlRunning := true
	if host == client.primaryHost {
		configured = client.primaryReplication
		sourceUUID = client.primarySourceUUID
		lag = 0
		executed = client.primaryGTID
		ioRunning = client.primaryIO
		sqlRunning = client.primarySQL
	} else if host != client.targetHost {
		return nil
	}
	if !configured {
		return nil
	}
	ioState := "No"
	if ioRunning {
		ioState = "Yes"
	}
	sqlState := "No"
	if sqlRunning {
		sqlState = "Yes"
	}
	return []Row{{
		"Source_UUID": sourceUUID, "Master_UUID": sourceUUID,
		"Replica_IO_Running": ioState, "Replica_SQL_Running": sqlState,
		"Slave_IO_Running": ioState, "Slave_SQL_Running": sqlState,
		"Seconds_Behind_Source": strconv.FormatInt(lag, 10), "Seconds_Behind_Master": strconv.FormatInt(lag, 10),
		"Executed_Gtid_Set": executed,
	}}
}

func (client *switchoverSQLClient) Exec(ctx context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, statement string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	host := endpoint.Hostname
	if host == "" {
		host = endpoint.IPAddress
	}
	client.executed = append(client.executed, host+" "+statement)
	if statement == client.failStatement && !client.failStatementConsumed {
		client.failStatementConsumed = true
		return errors.New("injected SQL execution failure")
	}
	if host == client.targetHost && statement == client.failTargetStatement {
		return errors.New("injected target fencing failure")
	}
	if host == client.primaryHost {
		switch statement {
		case setSuperReadOnlyOn:
			client.primarySuperReadOnly = true
			client.primaryReadOnly = true
		case setReadOnlyOn:
			client.primaryReadOnly = true
		case "STOP REPLICA", "STOP SLAVE":
			client.primaryIO = false
			client.primarySQL = false
		case "RESET REPLICA ALL", "RESET SLAVE ALL":
			client.primaryReplication = false
			client.primarySourceUUID = ""
		case "START REPLICA", "START SLAVE":
			client.primaryReplication = true
			client.primaryIO = true
			client.primarySQL = true
		default:
			if strings.HasPrefix(statement, "CHANGE REPLICATION SOURCE TO") || strings.HasPrefix(statement, "CHANGE MASTER TO") {
				client.primaryReplication = true
				client.primarySourceUUID = client.targetUUID
			}
		}
	}
	if host == client.targetHost {
		switch statement {
		case "RESET REPLICA ALL", "RESET SLAVE ALL":
			client.targetReplication = false
			if client.onTargetReset != nil {
				client.onTargetReset()
			}
		case "START REPLICA", "START SLAVE":
			client.targetReplication = true
		case setSuperReadOnlyOff:
			if client.onTargetPromotion != nil {
				client.onTargetPromotion()
			}
			client.targetSuperReadOnly = false
		case setReadOnlyOff:
			client.targetReadOnly = false
		case setSuperReadOnlyOn:
			client.targetSuperReadOnly = true
			client.targetReadOnly = true
		case setReadOnlyOn:
			client.targetReadOnly = true
		default:
			if strings.HasPrefix(statement, "CHANGE REPLICATION SOURCE TO") || strings.HasPrefix(statement, "CHANGE MASTER TO") {
				client.targetReplication = true
			}
		}
	}
	if statement == client.failAfterStatement {
		return errors.New("injected post-commit SQL execution failure")
	}
	return nil
}

func (client *switchoverSQLClient) statements() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string{}, client.executed...)
}

type recordingEndpointProvider struct {
	mu                  sync.Mutex
	owner               model.ResourceID
	transferCalls       int
	transferError       error
	transferNoEffect    bool
	duplicateOwner      bool
	verifyOverride      *model.Check
	authorizeCalls      int
	finalizeCalls       int
	authorizeError      error
	finalizeError       error
	authorized          bool
	authorizationCancel context.CancelFunc
}

func (provider *recordingEndpointProvider) Executable(context.Context) bool { return true }
func (provider *recordingEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "writer endpoint provider is ready"}}
}
func (provider *recordingEndpointProvider) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.authorizeCalls++
	if provider.authorizeError != nil {
		return adapter.TransitionAuthorization{}, provider.authorizeError
	}
	provider.authorized = true
	guarded, cancel := context.WithCancel(ctx)
	provider.authorizationCancel = cancel
	return adapter.TransitionAuthorization{
		Context: guarded,
		Cancel:  cancel,
		Finalize: func(context.Context) error {
			provider.mu.Lock()
			defer provider.mu.Unlock()
			provider.finalizeCalls++
			return provider.finalizeError
		},
	}, nil
}

func (provider *recordingEndpointProvider) cancelAuthorization() {
	provider.mu.Lock()
	cancel := provider.authorizationCancel
	provider.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (provider *recordingEndpointProvider) Transfer(_ context.Context, resolved adapter.ResolvedOperation) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.transferCalls++
	if provider.transferError != nil {
		return provider.transferError
	}
	if provider.transferNoEffect {
		return nil
	}
	provider.owner = resolved.Target.ResourceID
	return nil
}
func (provider *recordingEndpointProvider) Verify(_ context.Context, resolved adapter.ResolvedOperation) model.Check {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.verifyOverride != nil {
		return *provider.verifyOverride
	}
	if provider.duplicateOwner {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "writer endpoint has multiple owners"}
	}
	if provider.owner != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "writer endpoint is not owned by the selected target"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "writer endpoint has one target owner"}
}

func TestSwitchoverVerifyRequiresExplicitEndpointOwnershipPass(t *testing.T) {
	tests := []struct {
		name  string
		check model.Check
	}{
		{name: "missing evidence"},
		{name: "warning evidence", check: model.Check{Name: "writer_endpoint_owner", Status: model.CheckWarn, Message: "one host could not be probed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterInstance, request, _, provider := executableSwitchoverFixture(t)
			if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
				t.Fatalf("execute: %v", err)
			}
			provider.verifyOverride = &test.check
			verification, err := adapterInstance.Verify(context.Background(), request)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if verification.Passed {
				t.Fatalf("verification passed without explicit endpoint ownership evidence: %+v", verification.Checks)
			}
		})
	}
}

func executableSwitchoverFixture(t *testing.T) (*Adapter, adapter.OperationRequest, *switchoverSQLClient, *recordingEndpointProvider) {
	t.Helper()
	request := switchoverRequestFixture()
	client := newSwitchoverSQLClient(request)
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithEndpointProvider(client, provider)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	return adapterInstance, request, client, provider
}

type classifiedFailure interface {
	FailureClass() string
}

type progressCollector struct {
	mu    sync.Mutex
	steps []string
}

func (collector *progressCollector) CompleteStep(_ context.Context, step string, _ string) error {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.steps = append(collector.steps, step)
	return nil
}

func (collector *progressCollector) StepCompleted(_ context.Context, step string) (bool, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, candidate := range collector.steps {
		if candidate == step {
			return true, nil
		}
	}
	return false, nil
}

func failureClass(err error) string {
	var classified classifiedFailure
	if errors.As(err, &classified) {
		return classified.FailureClass()
	}
	return ""
}

func TestSwitchoverExecuteAndVerifyHappyPath(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	capabilities := adapterInstance.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityExecute) || !capabilities.Supports(adapter.CapabilityVerify) {
		t.Fatalf("executable test adapter did not advertise operation support: %+v", capabilities)
	}
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
	want := []string{
		"db-primary " + setSuperReadOnlyOn,
		"db-primary " + setReadOnlyOn,
		"db-replica STOP REPLICA",
		"db-replica RESET REPLICA ALL",
		"db-replica " + setSuperReadOnlyOff,
		"db-replica " + setReadOnlyOff,
		"db-primary " + setSuperReadOnlyOn,
		"db-primary " + setReadOnlyOn,
		"db-primary CHANGE REPLICATION SOURCE TO SOURCE_HOST='192.0.2.11', SOURCE_PORT=3306, SOURCE_USER='replicator', SOURCE_PASSWORD='replication-secret', SOURCE_AUTO_POSITION=1",
		"db-primary START REPLICA",
	}
	got := client.statements()
	if len(got) != len(want) {
		t.Fatalf("statements=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("statement %d=%q, want %q", index, got[index], want[index])
		}
	}
	if provider.owner != request.TargetID || provider.transferCalls != 1 || provider.finalizeCalls != 1 {
		t.Fatalf("endpoint transfer was not coupled and finalized: owner=%s transfer_calls=%d finalize_calls=%d", provider.owner, provider.transferCalls, provider.finalizeCalls)
	}
}

func TestSwitchoverAuthorizesEndpointBeforeTargetBecomesWritable(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	authorizedAtPromotion := false
	client.onTargetPromotion = func() {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		authorizedAtPromotion = provider.authorized
	}
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if provider.authorizeCalls != 1 || !authorizedAtPromotion {
		t.Fatalf("endpoint transition was not authorized before promotion: calls=%d authorized_at_promotion=%t", provider.authorizeCalls, authorizedAtPromotion)
	}
}

func TestSwitchoverAuthorizationFailureLeavesCandidateReplicationAttached(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	provider.authorizeError = errors.New("stable endpoint lease handoff blocked")
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("authorization failure execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if !client.targetReplication || !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("authorization failure detached or promoted target: replication=%t read_only=%t super_read_only=%t", client.targetReplication, client.targetReadOnly, client.targetSuperReadOnly)
	}
	for _, statement := range client.statements() {
		if strings.HasPrefix(statement, client.targetHost+" STOP ") || strings.HasPrefix(statement, client.targetHost+" RESET ") {
			t.Fatalf("target replication mutated before endpoint authorization: %s", statement)
		}
	}
}

func TestSwitchoverRestoresCandidateReplicationWhenTransitionLeaseIsLostBeforePromotion(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	client.onTargetReset = provider.cancelAuthorization
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("lease loss execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if !client.targetReplication || !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("lease loss left candidate detached: replication=%t read_only=%t super_read_only=%t", client.targetReplication, client.targetReadOnly, client.targetSuperReadOnly)
	}
	joined := strings.Join(client.statements(), "\n")
	if !strings.Contains(joined, client.targetHost+" CHANGE REPLICATION SOURCE TO") || !strings.Contains(joined, client.targetHost+" START REPLICA") {
		t.Fatalf("candidate replication compensation was not executed:\n%s", joined)
	}
}

func TestSwitchoverVerifyRejectsEndpointIdentityDrift(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	client.targetUUID = extraUUID
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verification.Passed || !failedCheck(verification.Checks, "target_identity") {
		t.Fatalf("identity drift passed verification: %+v", verification.Checks)
	}
}

func TestSwitchoverVerifyAcceptsRefreshedPostPromotionTopology(t *testing.T) {
	adapterInstance, request, _, _ := executableSwitchoverFixture(t)
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	request.Resolved.Snapshot.ObservedAt = request.Resolved.Snapshot.ObservedAt.Add(time.Minute)
	request.Resolved.Cluster.MetadataRevision++
	request.Resolved.Primary.MetadataRevision++
	request.Resolved.Primary.Role = model.RoleReplica
	request.Resolved.Target.MetadataRevision++
	request.Resolved.Target.Role = model.RolePrimary
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify refreshed topology: %v", err)
	}
	if !verification.Passed || failedCheck(verification.Checks, "plan_integrity") {
		t.Fatalf("refreshed post-promotion topology failed verification: %+v", verification.Checks)
	}
}

func TestSwitchoverExecuteRecordsEveryCompletedMutationBoundary(t *testing.T) {
	adapterInstance, request, _, _ := executableSwitchoverFixture(t)
	collector := &progressCollector{}
	request.Progress = collector
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"fence_source", "capture_source_gtid", "wait_target_gtid", "authorize_target_transition", "stop_target_replication", "promote_target", "reparent_follower_" + string(request.Resolved.Primary.ResourceID), "transfer_writer_endpoint", "retain_source_read_only"}
	if len(collector.steps) != len(want) {
		t.Fatalf("progress steps=%v, want %v", collector.steps, want)
	}
	for index := range want {
		if collector.steps[index] != want[index] {
			t.Fatalf("progress step %d=%q, want %q", index, collector.steps[index], want[index])
		}
	}
}

func TestSwitchoverExecutionFailureClasses(t *testing.T) {
	tests := []struct {
		name       string
		statement  string
		status     model.OperationStatus
		class      string
		endpoint   error
		wantTarget bool
	}{
		{name: "before fence", statement: setSuperReadOnlyOn, status: model.OperationFailed, class: "pre_commit"},
		{name: "after fence", statement: setReadOnlyOn, status: model.OperationBlocked, class: "fenced"},
		{name: "endpoint transfer", endpoint: errors.New("endpoint transfer uncertain"), status: model.OperationIndeterminate, class: "promoted_unverified", wantTarget: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterInstance, request, client, provider := executableSwitchoverFixture(t)
			client.failStatement = test.statement
			provider.transferError = test.endpoint
			execution, err := adapterInstance.Execute(context.Background(), request)
			if err == nil || execution.Status != test.status || failureClass(err) != test.class {
				t.Fatalf("execution=%+v err=%v class=%q", execution, err, failureClass(err))
			}
			if test.wantTarget && (!client.targetReadOnly || !client.targetSuperReadOnly) {
				t.Fatalf("promoted target was not fenced after endpoint failure: read_only=%t super_read_only=%t", client.targetReadOnly, client.targetSuperReadOnly)
			}
		})
	}
}

func TestSwitchoverRefencesTargetWhenFirstPromotionCommandIsUncertain(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	client.failAfterStatement = setSuperReadOnlyOff
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("uncertain promotion result=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("uncertain promotion left target partially writable: read_only=%t super_read_only=%t", client.targetReadOnly, client.targetSuperReadOnly)
	}
}

func TestSwitchoverReportsIndeterminateWhenUncertainPromotionCannotBeRefenced(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	client.failAfterStatement = setSuperReadOnlyOff
	client.failTargetStatement = setSuperReadOnlyOn
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("failed target refence result=%+v err=%v class=%q", execution, err, failureClass(err))
	}
}

func TestSwitchoverExecutionDoesNotExposeProviderErrorDetails(t *testing.T) {
	adapterInstance, request, _, provider := executableSwitchoverFixture(t)
	provider.transferError = errors.New("vip-token=top-secret command=/sbin/ip addr add")
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil {
		t.Fatal("endpoint transfer failure unexpectedly succeeded")
	}
	for _, message := range []string{execution.Message, err.Error()} {
		if strings.Contains(message, "top-secret") || strings.Contains(message, "/sbin/ip") {
			t.Fatalf("unsafe switchover error message %q", message)
		}
	}
}

func TestSwitchoverDoesNotJournalEndpointTransferBeforePostcondition(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	provider.transferNoEffect = true
	collector := &progressCollector{}
	request.Progress = collector
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("unverified transfer result=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	completed, _ := collector.StepCompleted(context.Background(), "transfer_writer_endpoint")
	if completed {
		t.Fatalf("unverified endpoint transfer was journaled complete: %v", collector.steps)
	}
	if !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("target remained writable after unverified endpoint transfer")
	}
}

func TestSwitchoverRetryObservesPostconditionsBeforeMutation(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	request.Progress = &progressCollector{}
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	before := len(client.statements())
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if after := len(client.statements()); after != before {
		t.Fatalf("retry repeated mutations: before=%d after=%d statements=%v", before, after, client.statements())
	}
	if provider.transferCalls != 1 {
		t.Fatalf("retry repeated writer endpoint transfer: calls=%d", provider.transferCalls)
	}
}

func TestSwitchoverRetryResumesAfterReparentedEndpointTransferFailure(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	request.Progress = &progressCollector{}
	provider.transferError = errors.New("endpoint transfer unavailable")
	first, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || first.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("first execution=%+v err=%v class=%q", first, err, failureClass(err))
	}
	if !client.primaryReplication || client.primarySourceUUID != client.targetUUID || !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("failed transfer did not leave a resumable fenced topology: primary_replication=%t source=%q target_read_only=%t target_super_read_only=%t", client.primaryReplication, client.primarySourceUUID, client.targetReadOnly, client.targetSuperReadOnly)
	}
	provider.transferError = nil
	second, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || second.Status != model.OperationRunning {
		t.Fatalf("resume execution=%+v err=%v", second, err)
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("resume verification=%+v err=%v", verification, err)
	}
}

func TestSwitchoverResumeRejectsFenceWithoutOperationProgress(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	client.primaryReadOnly = true
	client.primarySuperReadOnly = true
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "pre_commit" {
		t.Fatalf("unowned fence result=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if len(client.statements()) != 0 || provider.transferCalls != 0 {
		t.Fatalf("unowned fence allowed mutation: statements=%v transfers=%d", client.statements(), provider.transferCalls)
	}
}

func TestSwitchoverResumeRevalidatesLiveReplicaBeforeMutation(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	client.primaryReadOnly = true
	client.primarySuperReadOnly = true
	client.targetLag = 5
	request.Progress = &progressCollector{steps: []string{"fence_source"}}
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
		t.Fatalf("stale resume result=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if len(client.statements()) != 0 || provider.transferCalls != 0 {
		t.Fatalf("stale resume allowed mutation: statements=%v transfers=%d", client.statements(), provider.transferCalls)
	}
}

func TestSwitchoverVerifyRejectsDuplicateEndpointOwners(t *testing.T) {
	adapterInstance, request, _, provider := executableSwitchoverFixture(t)
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	provider.duplicateOwner = true
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verification.Passed || !failedCheckNamed(verification.Checks, "writer_endpoint_owner") {
		t.Fatalf("duplicate endpoint owners were not rejected: %+v", verification)
	}
}

func TestSwitchoverExecuteRejectsModifiedPlanBeforeMutation(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	request.Plan.Summary = "modified after approval " + strconv.Itoa(len(request.Plan.Steps))
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed || failureClass(err) != "pre_commit" {
		t.Fatalf("modified plan execution=%+v err=%v", execution, err)
	}
	if len(client.statements()) != 0 {
		t.Fatalf("modified plan caused SQL mutations: %v", client.statements())
	}
}

func TestRecoveryFencingDetachesFromCanceledOperationContext(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	client.targetReadOnly = false
	client.targetSuperReadOnly = false
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapterInstance.fenceInstance(canceled, instanceEndpoint(request.Resolved.Target), request.Resolved.Credentials); err != nil {
		t.Fatalf("detached recovery fencing: %v", err)
	}
	if !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("target was not fenced: read_only=%t super_read_only=%t", client.targetReadOnly, client.targetSuperReadOnly)
	}
}

func TestSwitchoverFencesPromotedTargetWhenEndpointOwnershipIsUnknown(t *testing.T) {
	adapterInstance, request, client, provider := executableSwitchoverFixture(t)
	provider.verifyOverride = &model.Check{Name: "writer_endpoint_owner", Status: model.CheckWarn, Message: "owner probe incomplete"}
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationIndeterminate || failureClass(err) != "promoted_unverified" {
		t.Fatalf("execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if !client.targetReadOnly || !client.targetSuperReadOnly {
		t.Fatalf("target remained writable with unknown endpoint ownership: read_only=%t super_read_only=%t", client.targetReadOnly, client.targetSuperReadOnly)
	}
}

func TestSwitchoverExecuteRevalidatesLiveReplicationBeforeFirstMutation(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
	client.targetLag = 5
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed || failureClass(err) != "pre_commit" {
		t.Fatalf("live replication drift execution=%+v err=%v", execution, err)
	}
	if len(client.statements()) != 0 {
		t.Fatalf("live precheck failure issued mutating SQL: %v", client.statements())
	}
}
