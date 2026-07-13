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
		version string
		want    []string
	}{
		{version: "5.7.44", want: []string{"CHANGE MASTER TO", "MASTER_HOST='mysql\\'new'", "MASTER_USER='repl\\'user'", "MASTER_PASSWORD='p\\\\ass\\'word'", "MASTER_AUTO_POSITION=1"}},
		{version: "8.4.10", want: []string{"CHANGE REPLICATION SOURCE TO", "SOURCE_HOST='mysql\\'new'", "SOURCE_USER='repl\\'user'", "SOURCE_PASSWORD='p\\\\ass\\'word'", "SOURCE_AUTO_POSITION=1"}},
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
		})
	}
}

type reparentNodeState struct {
	uuid          string
	readOnly      bool
	superReadOnly bool
	sourceUUID    string
	ioRunning     bool
	sqlRunning    bool
	gtid          string
}

type threeNodeSQLClient struct {
	mu          sync.Mutex
	version     string
	nodes       map[string]*reparentNodeState
	statements  []string
	credentials []string
}

func newThreeNodeSQLClient(request adapter.OperationRequest) *threeNodeSQLClient {
	nodes := map[string]*reparentNodeState{}
	for _, instance := range request.Resolved.Snapshot.Instances {
		nodes[instance.Hostname] = &reparentNodeState{
			uuid:     strings.ToLower(instance.EngineIdentity["server_uuid"]),
			readOnly: instance.Role != model.RolePrimary, superReadOnly: instance.Role != model.RolePrimary,
			sourceUUID: strings.ToLower(instance.Replication.SourceIdentity["server_uuid"]),
			ioRunning:  instance.Role == model.RoleReplica, sqlRunning: instance.Role == model.RoleReplica,
			gtid: request.Resolved.Primary.EngineMetadata["gtid_executed"],
		}
	}
	return &threeNodeSQLClient{version: "8.0.46", nodes: nodes}
}

func (client *threeNodeSQLClient) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	node := client.nodes[endpoint.Hostname]
	if node == nil {
		return nil, errors.New("unknown node")
	}
	switch {
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
		if node.ioRunning {
			ioState = "Yes"
		}
		sqlState := "No"
		if node.sqlRunning {
			sqlState = "Yes"
		}
		return []Row{{
			"Source_UUID": node.sourceUUID, "Master_UUID": node.sourceUUID,
			"Replica_IO_Running": ioState, "Replica_SQL_Running": sqlState,
			"Slave_IO_Running": ioState, "Slave_SQL_Running": sqlState,
			"Seconds_Behind_Source": "0", "Seconds_Behind_Master": "0", "Executed_Gtid_Set": node.gtid,
		}}, nil
	case query == gtidPositionQuery:
		return []Row{{"gtid_executed": node.gtid}}, nil
	case strings.HasPrefix(query, "SELECT WAIT_FOR_EXECUTED_GTID_SET("):
		return []Row{{"wait_result": "0"}}, nil
	default:
		return nil, fmt.Errorf("unexpected query %q", query)
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
		node.readOnly = false
	case "STOP REPLICA", "STOP SLAVE":
		node.ioRunning = false
		node.sqlRunning = false
	case "RESET REPLICA ALL", "RESET SLAVE ALL":
		node.sourceUUID = ""
	case "START REPLICA", "START SLAVE":
		node.ioRunning = true
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
