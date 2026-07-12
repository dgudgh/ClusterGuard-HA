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

type switchoverSQLClient struct {
	mu                    sync.Mutex
	primaryHost           string
	targetHost            string
	version               string
	primaryReadOnly       bool
	primarySuperReadOnly  bool
	targetReadOnly        bool
	targetSuperReadOnly   bool
	targetReplication     bool
	primaryGTID           string
	targetExecuted        string
	executed              []string
	failStatement         string
	failStatementConsumed bool
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

func (client *switchoverSQLClient) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
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
		serverUUID := targetUUID
		serverID := "11"
		gtid := client.targetExecuted
		if isPrimary {
			readOnly = client.primaryReadOnly
			superReadOnly = client.primarySuperReadOnly
			serverUUID = primaryUUID
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
	if host != client.targetHost || !client.targetReplication {
		return nil
	}
	return []Row{{
		"Source_UUID": primaryUUID, "Master_UUID": primaryUUID,
		"Replica_IO_Running": "Yes", "Replica_SQL_Running": "Yes",
		"Slave_IO_Running": "Yes", "Slave_SQL_Running": "Yes",
		"Seconds_Behind_Source": "0", "Seconds_Behind_Master": "0",
		"Executed_Gtid_Set": client.targetExecuted,
	}}
}

func (client *switchoverSQLClient) Exec(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, statement string) error {
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
	if host == client.primaryHost {
		switch statement {
		case setSuperReadOnlyOn:
			client.primarySuperReadOnly = true
			client.primaryReadOnly = true
		case setReadOnlyOn:
			client.primaryReadOnly = true
		}
	}
	if host == client.targetHost {
		switch statement {
		case "RESET REPLICA ALL", "RESET SLAVE ALL":
			client.targetReplication = false
		case setSuperReadOnlyOff:
			client.targetSuperReadOnly = false
		case setReadOnlyOff:
			client.targetReadOnly = false
		case setSuperReadOnlyOn:
			client.targetSuperReadOnly = true
			client.targetReadOnly = true
		case setReadOnlyOn:
			client.targetReadOnly = true
		}
	}
	return nil
}

func (client *switchoverSQLClient) statements() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string{}, client.executed...)
}

type recordingEndpointProvider struct {
	mu             sync.Mutex
	owner          model.ResourceID
	transferCalls  int
	transferError  error
	duplicateOwner bool
}

func (provider *recordingEndpointProvider) Executable(context.Context) bool { return true }
func (provider *recordingEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "writer endpoint provider is ready"}}
}
func (provider *recordingEndpointProvider) Transfer(_ context.Context, resolved adapter.ResolvedOperation) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.transferCalls++
	if provider.transferError != nil {
		return provider.transferError
	}
	provider.owner = resolved.Target.ResourceID
	return nil
}
func (provider *recordingEndpointProvider) Verify(_ context.Context, resolved adapter.ResolvedOperation) model.Check {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.duplicateOwner {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "writer endpoint has multiple owners"}
	}
	if provider.owner != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "writer endpoint is not owned by the selected target"}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "writer endpoint has one target owner"}
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
	if provider.owner != request.TargetID || provider.transferCalls != 1 {
		t.Fatalf("endpoint transfer was not coupled to target: owner=%s calls=%d", provider.owner, provider.transferCalls)
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

func TestSwitchoverRetryObservesPostconditionsBeforeMutation(t *testing.T) {
	adapterInstance, request, client, _ := executableSwitchoverFixture(t)
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
