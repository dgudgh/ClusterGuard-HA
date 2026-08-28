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

func TestBuildChangeSourceStatementUsesVersionDialectAndEscapesSecrets(t *testing.T) {
	target := model.DatabaseInstance{Hostname: "mysql'new", Port: 3307}
	credentials := adapter.Credentials{Username: "repl'user", Password: "p\\ass'word"}
	tests := []struct {
		version   string
		want      []string
		forbidden []string
	}{
		{
			version:   "5.7.44",
			want:      []string{"SET SESSION sql_mode='NO_BACKSLASH_ESCAPES'; CHANGE MASTER TO", "MASTER_HOST='mysql''new'", "MASTER_USER='repl''user'", "MASTER_PASSWORD='p\\ass''word'", "MASTER_AUTO_POSITION=1", "MASTER_CONNECT_RETRY=5", "MASTER_RETRY_COUNT=86400"},
			forbidden: []string{"GET_MASTER_PUBLIC_KEY", "GET_SOURCE_PUBLIC_KEY"},
		},
		{
			version:   "8.0.21",
			want:      []string{"CHANGE MASTER TO", "MASTER_AUTO_POSITION=1", "MASTER_CONNECT_RETRY=5", "MASTER_RETRY_COUNT=86400", "GET_MASTER_PUBLIC_KEY=1"},
			forbidden: []string{"GET_SOURCE_PUBLIC_KEY"},
		},
		{
			version:   "8.0.22",
			want:      []string{"CHANGE REPLICATION SOURCE TO", "SOURCE_AUTO_POSITION=1", "SOURCE_CONNECT_RETRY=5", "SOURCE_RETRY_COUNT=86400", "GET_SOURCE_PUBLIC_KEY=1"},
			forbidden: []string{"GET_MASTER_PUBLIC_KEY"},
		},
		{
			version:   "8.4.10",
			want:      []string{"SET SESSION sql_mode='NO_BACKSLASH_ESCAPES'; CHANGE REPLICATION SOURCE TO", "SOURCE_HOST='mysql''new'", "SOURCE_USER='repl''user'", "SOURCE_PASSWORD='p\\ass''word'", "SOURCE_AUTO_POSITION=1", "SOURCE_CONNECT_RETRY=5", "SOURCE_RETRY_COUNT=86400", "GET_SOURCE_PUBLIC_KEY=1"},
			forbidden: []string{"GET_MASTER_PUBLIC_KEY"},
		},
		{
			version:   "9.7.0",
			want:      []string{"CHANGE REPLICATION SOURCE TO", "SOURCE_AUTO_POSITION=1", "SOURCE_CONNECT_RETRY=5", "SOURCE_RETRY_COUNT=86400", "GET_SOURCE_PUBLIC_KEY=1"},
			forbidden: []string{"GET_MASTER_PUBLIC_KEY"},
		},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			statement, err := buildChangeSourceStatement(test.version, target, credentials)
			if err != nil {
				t.Fatalf("build statement: %v", err)
			}
			for _, fragment := range test.want {
				if !strings.Contains(statement, fragment) {
					t.Fatalf("statement missing %q: %s", fragment, statement)
				}
			}
			for _, fragment := range test.forbidden {
				if strings.Contains(statement, fragment) {
					t.Fatalf("statement unexpectedly contains %q: %s", fragment, statement)
				}
			}
		})
	}
}

type reparentNodeState struct {
	uuid                    string
	readOnly                bool
	superReadOnly           bool
	sourceUUID              string
	lagSeconds              int64
	ioRunning               bool
	ioConnecting            bool
	sqlRunning              bool
	lastSQLError            string
	gtid                    string
	startProbeFailures      int
	remainingProbeFailures  int
	startLagUnknownProbes   int
	remainingLagUnknown     int
	startLagExcessiveProbes int
	remainingLagExcessive   int
}

type threeNodeSQLClient struct {
	mu                      sync.Mutex
	version                 string
	nodes                   map[string]*reparentNodeState
	queries                 []string
	statements              []string
	credentials             []string
	beforeWritable          func()
	ignoreWritable          bool
	replicationCredentialOK bool
}

func newThreeNodeSQLClient(request adapter.OperationRequest) *threeNodeSQLClient {
	nodes := map[string]*reparentNodeState{}
	for _, instance := range request.Resolved.Snapshot.Instances {
		nodes[instance.Hostname] = &reparentNodeState{
			uuid:     strings.ToLower(instance.EngineIdentity["server_uuid"]),
			readOnly: instance.Role != model.RolePrimary, superReadOnly: instance.Role != model.RolePrimary,
			sourceUUID:   strings.ToLower(instance.Replication.SourceIdentity["server_uuid"]),
			ioRunning:    instance.Replication.IOThread == model.ThreadRunning,
			ioConnecting: instance.Replication.IOThread == model.ThreadConnecting,
			sqlRunning:   instance.Replication.SQLThread == model.ThreadRunning,
			lastSQLError: instance.Replication.LastSQLError,
			gtid:         request.Resolved.Primary.EngineMetadata["gtid_executed"],
		}
	}
	return &threeNodeSQLClient{version: "8.0.46", nodes: nodes, replicationCredentialOK: true}
}

func (client *threeNodeSQLClient) Query(_ context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]Row, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	node := client.nodes[endpoint.Hostname]
	if node == nil {
		return nil, errors.New("unknown node")
	}
	client.queries = append(client.queries, endpoint.Hostname+" "+query)
	switch {
	case query == replicationCredentialProbeQuery:
		if credentials.Username != "replicator" || credentials.Password != "replication-secret" || !client.replicationCredentialOK {
			return nil, &QueryError{Code: 1045, Output: "ERROR 1045 access denied", Err: errors.New("exit status 1")}
		}
		return []Row{{"credential_ready": "1"}}, nil
	case query == identityQuery:
		return []Row{{
			"server_uuid": node.uuid, "hostname": endpoint.Hostname, "port": strconv.Itoa(endpoint.Port), "server_id": "10",
			"version": client.version, "read_only": boolString(node.readOnly), "super_read_only": boolString(node.superReadOnly),
			"gtid_mode": "ON", "gtid_executed": node.gtid, "log_bin": "ON", "binlog_format": "ROW",
		}}, nil
	case query == replicaStatusQuery || query == slaveStatusQuery:
		if node.sourceUUID == "" {
			return nil, nil
		}
		ioState := "No"
		if node.remainingProbeFailures > 0 {
			node.remainingProbeFailures--
		} else if node.ioConnecting {
			ioState = "Connecting"
		} else if node.ioRunning {
			ioState = "Yes"
		}
		sqlState := "No"
		if node.sqlRunning {
			sqlState = "Yes"
		}
		lag := strconv.FormatInt(node.lagSeconds, 10)
		if node.remainingLagUnknown > 0 {
			node.remainingLagUnknown--
			lag = "NULL"
		} else if node.remainingLagExcessive > 0 {
			node.remainingLagExcessive--
			lag = strconv.FormatInt(defaultMaximumReplicationLagSeconds+1, 10)
		} else if node.ioConnecting {
			lag = "NULL"
		}
		return []Row{{
			"Source_UUID": node.sourceUUID, "Master_UUID": node.sourceUUID,
			"Replica_IO_Running": ioState, "Replica_SQL_Running": sqlState,
			"Slave_IO_Running": ioState, "Slave_SQL_Running": sqlState,
			"Seconds_Behind_Source": lag, "Seconds_Behind_Master": lag, "Executed_Gtid_Set": node.gtid,
			"Last_SQL_Error": node.lastSQLError,
		}}, nil
	case query == gtidPositionQuery:
		return []Row{{"gtid_executed": node.gtid}}, nil
	case strings.HasPrefix(query, "SELECT WAIT_FOR_EXECUTED_GTID_SET("):
		const prefix = "SELECT WAIT_FOR_EXECUTED_GTID_SET('"
		const suffix = "', 30) AS wait_result"
		if strings.HasPrefix(query, prefix) && strings.HasSuffix(query, suffix) {
			node.gtid = strings.TrimSuffix(strings.TrimPrefix(query, prefix), suffix)
		}
		return []Row{{"wait_result": "0"}}, nil
	default:
		return nil, fmt.Errorf("unexpected query %q", query)
	}
}

func TestFollowerVerificationAcceptsOnlyBoundedHealthyReplicationLag(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	follower := request.Resolved.Snapshot.Instances[2]
	target := request.Resolved.Target

	tests := []struct {
		name       string
		lagSeconds int64
		want       model.CheckStatus
	}{
		{name: "active workload lag within policy", lagSeconds: 1, want: model.CheckPass},
		{name: "lag at policy boundary", lagSeconds: defaultMaximumReplicationLagSeconds, want: model.CheckPass},
		{name: "lag above policy", lagSeconds: defaultMaximumReplicationLagSeconds + 1, want: model.CheckFail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newThreeNodeSQLClient(request)
			state := client.nodes[follower.Hostname]
			state.sourceUUID = strings.ToLower(target.EngineIdentity["server_uuid"])
			state.lagSeconds = test.lagSeconds
			adapterInstance := New(client)

			checks, writable := adapterInstance.verifyFollower(context.Background(), follower, target, adapter.Credentials{Username: "operator"})
			if writable {
				t.Fatal("read-only follower was reported writable")
			}
			var replication model.Check
			for _, check := range checks {
				if check.Name == "follower_replication_"+string(follower.ResourceID) {
					replication = check
					break
				}
			}
			if replication.Status != test.want {
				t.Fatalf("lag=%d replication check=%+v, want %s", test.lagSeconds, replication, test.want)
			}
		})
	}
}

func TestSwitchoverWaitsForEveryMissingReplicaAfterSourceFence(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	request.Resolved.Target.Replication.ExecutedPosition = primaryUUID + ":1-99"
	sibling := &request.Resolved.Snapshot.Instances[2]
	sibling.Replication.ExecutedPosition = primaryUUID + ":1-99"

	client := newThreeNodeSQLClient(request)
	client.nodes[request.Resolved.Target.Hostname].gtid = primaryUUID + ":1-99"
	client.nodes[sibling.Hostname].gtid = primaryUUID + ":1-99"
	adapterInstance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute missing-GTID switchover: execution=%+v err=%v", execution, err)
	}
	waited := map[string]bool{}
	for _, query := range client.queries {
		if strings.Contains(query, "WAIT_FOR_EXECUTED_GTID_SET") {
			waited[strings.Fields(query)[0]] = true
		}
	}
	for _, instance := range request.Resolved.Snapshot.Instances {
		if !waited[instance.Hostname] {
			t.Fatalf("instance %s did not receive a GTID catch-up wait: queries=%v", instance.Hostname, client.queries)
		}
	}
}

func TestReparentFollowerAcceptsEmptyTargetGTIDForZeroTransactionCluster(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	follower := request.Resolved.Snapshot.Instances[2]
	target := request.Resolved.Target
	client := newThreeNodeSQLClient(request)
	client.nodes[target.Hostname].gtid = ""
	client.nodes[follower.Hostname].gtid = ""
	adapterInstance := New(client)

	err := adapterInstance.reparentFollower(
		context.Background(),
		follower,
		target,
		adapter.Credentials{Username: "operator", Password: "operation-secret"},
		adapter.Credentials{Username: "replicator", Password: "replication-secret"},
	)
	if err != nil {
		t.Fatalf("reparent zero-transaction follower: %v", err)
	}
	for _, query := range client.queries {
		if strings.Contains(query, "WAIT_FOR_EXECUTED_GTID_SET") {
			t.Fatalf("empty target GTID triggered an invalid catch-up wait: %s", query)
		}
	}
	state := client.nodes[follower.Hostname]
	if state.sourceUUID != strings.ToLower(target.EngineIdentity["server_uuid"]) || !state.ioRunning || !state.sqlRunning {
		t.Fatalf("follower did not converge on the selected target: %+v", state)
	}
}

func TestReparentFollowerWaitsForObservableLagBeforeReturning(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	follower := request.Resolved.Snapshot.Instances[2]
	target := request.Resolved.Target
	client := newThreeNodeSQLClient(request)
	state := client.nodes[follower.Hostname]
	state.startLagUnknownProbes = 3
	adapterInstance := New(client)

	err := adapterInstance.reparentFollower(
		context.Background(),
		follower,
		target,
		adapter.Credentials{Username: "operator", Password: "operation-secret"},
		adapter.Credentials{Username: "replicator", Password: "replication-secret"},
	)
	if err != nil {
		t.Fatalf("reparent follower with initially unknown lag: %v", err)
	}
	if state.remainingLagUnknown != 0 {
		t.Fatalf("reparent returned before replication lag became observable: remaining=%d", state.remainingLagUnknown)
	}
}

func TestReparentFollowerWaitsForBoundedStableLagBeforeReturning(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	follower := request.Resolved.Snapshot.Instances[2]
	target := request.Resolved.Target
	client := newThreeNodeSQLClient(request)
	state := client.nodes[follower.Hostname]
	state.startLagExcessiveProbes = 3
	adapterInstance := New(client)

	err := adapterInstance.reparentFollower(
		context.Background(),
		follower,
		target,
		adapter.Credentials{Username: "operator", Password: "operation-secret"},
		adapter.Credentials{Username: "replicator", Password: "replication-secret"},
	)
	if err != nil {
		t.Fatalf("reparent follower while lag converges: %v", err)
	}
	if state.remainingLagExcessive != 0 {
		t.Fatalf("reparent returned before lag entered the verification bound: %+v", state)
	}
}

func (client *threeNodeSQLClient) Exec(_ context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, statement string) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	node := client.nodes[endpoint.Hostname]
	if node == nil {
		return errors.New("unknown node")
	}
	client.statements = append(client.statements, endpoint.Hostname+" "+statement)
	client.credentials = append(client.credentials, credentials.Username)
	switch statement {
	case setSuperReadOnlyOn:
		node.superReadOnly = true
		node.readOnly = true
	case setReadOnlyOn:
		node.readOnly = true
	case setSuperReadOnlyOff:
		node.superReadOnly = false
	case setReadOnlyOff:
		if client.beforeWritable != nil {
			client.beforeWritable()
		}
		if !client.ignoreWritable {
			node.readOnly = false
			node.superReadOnly = false
		}
	case "STOP REPLICA", "STOP SLAVE":
		node.ioRunning = false
		node.ioConnecting = false
		node.sqlRunning = false
	case "RESET REPLICA ALL", "RESET SLAVE ALL":
		node.sourceUUID = ""
	case "START REPLICA", "START SLAVE":
		node.ioRunning = true
		node.ioConnecting = false
		node.sqlRunning = true
		node.remainingProbeFailures = node.startProbeFailures
		node.remainingLagUnknown = node.startLagUnknownProbes
		node.remainingLagExcessive = node.startLagExcessiveProbes
	case "START REPLICA IO_THREAD", "START SLAVE IO_THREAD":
		node.ioRunning = true
	case "START REPLICA SQL_THREAD", "START SLAVE SQL_THREAD":
		node.sqlRunning = true
	default:
		if strings.HasPrefix(statement, "CHANGE REPLICATION SOURCE TO") || strings.HasPrefix(statement, "CHANGE MASTER TO") {
			node.sourceUUID = targetUUID
			return nil
		}
		return fmt.Errorf("unexpected statement %q", statement)
	}
	return nil
}

func TestSwitchoverReparentsFormerPrimaryAndSiblingBeforeVIPTransfer(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithEndpointProvider(client, provider)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	for _, instance := range request.Resolved.Snapshot.Instances {
		state := client.nodes[instance.Hostname]
		if instance.ResourceID == request.TargetID {
			if state.readOnly || state.superReadOnly || state.sourceUUID != "" {
				t.Fatalf("target state=%+v", state)
			}
			continue
		}
		if !state.readOnly || !state.superReadOnly || state.sourceUUID != targetUUID || !state.ioRunning || !state.sqlRunning {
			t.Fatalf("follower %s state=%+v", instance.Hostname, state)
		}
	}
	for index, statement := range client.statements {
		if strings.Contains(statement, "CHANGE REPLICATION SOURCE TO") || strings.Contains(statement, "CHANGE MASTER TO") {
			if client.credentials[index] != "operator" || !strings.Contains(statement, "SOURCE_USER='replicator'") {
				t.Fatalf("change-source used wrong administrative or channel credentials: login=%q statement=%s", client.credentials[index], statement)
			}
		} else if client.credentials[index] != "operator" {
			t.Fatalf("administrative statement used %q credentials: %s", client.credentials[index], statement)
		}
	}
	verification, err := adapterInstance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify: result=%+v err=%v", verification, err)
	}
}

func TestSwitchoverWaitsForFollowerReplicationThreadsAfterRestart(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	sibling := request.Resolved.Snapshot.Instances[2]
	client.nodes[sibling.Hostname].startProbeFailures = 1
	adapterInstance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute with transient follower startup: execution=%+v err=%v", execution, err)
	}
	state := client.nodes[sibling.Hostname]
	if !state.ioRunning || !state.sqlRunning || state.sourceUUID != targetUUID {
		t.Fatalf("follower did not converge after transient startup: %+v", state)
	}
}

func TestSwitchoverLivePrecheckRevalidatesEveryFollowerBeforeMutation(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	client := newThreeNodeSQLClient(request)
	provider := &recordingEndpointProvider{}
	adapterInstance := NewWithEndpointProvider(client, provider)
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
	for _, instance := range request.Resolved.Snapshot.Instances {
		if instance.EngineIdentity["server_uuid"] == extraUUID {
			client.nodes[instance.Hostname].ioRunning = false
		}
	}

	execution, err := adapterInstance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationFailed || failureClass(err) != "pre_commit" {
		t.Fatalf("stale follower execution=%+v err=%v class=%q", execution, err, failureClass(err))
	}
	if len(client.statements) != 0 || provider.transferCalls != 0 {
		t.Fatalf("stale follower allowed mutation: statements=%v transfers=%d", client.statements, provider.transferCalls)
	}
}
