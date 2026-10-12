package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type fencedGTIDSQLClient struct {
	*switchoverSQLClient
	overrideFenced  bool
	fencedRows      []Row
	fencedError     error
	emptyWaitResult string
}

func (client *fencedGTIDSQLClient) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]Row, error) {
	client.mu.Lock()
	fenced := client.primarySuperReadOnly
	client.mu.Unlock()
	if client.overrideFenced && fenced && endpoint.Hostname == client.primaryHost && query == gtidPositionQuery {
		return client.fencedRows, client.fencedError
	}
	if query == "SELECT WAIT_FOR_EXECUTED_GTID_SET('', 30) AS wait_result" && client.emptyWaitResult != "" {
		return []Row{{"wait_result": client.emptyWaitResult}}, nil
	}
	return client.switchoverSQLClient.Query(ctx, endpoint, credentials, query)
}

func emptyGTIDSwitchoverRequest() adapter.OperationRequest {
	request := switchoverRequestFixture()
	request.Resolved.Primary.EngineMetadata["gtid_executed"] = ""
	request.Resolved.Target.EngineMetadata["gtid_executed"] = ""
	request.Resolved.Target.Replication.ExecutedPosition = ""
	for index := range request.Resolved.Snapshot.Instances {
		request.Resolved.Snapshot.Instances[index].EngineMetadata["gtid_executed"] = ""
		request.Resolved.Snapshot.Instances[index].Replication.ExecutedPosition = ""
	}
	return request
}

func TestSwitchoverExecuteAndVerifyEmptyGTIDSet(t *testing.T) {
	request := emptyGTIDSwitchoverRequest()
	client := newSwitchoverSQLClient(request)
	provider := &recordingEndpointProvider{}
	instance := NewWithEndpointProvider(client, provider)
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build empty-set plan: %v", err)
	}
	request.Plan = &plan
	progress := &progressCollector{}
	request.Progress = progress
	execution, err := instance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute empty-set switchover: %+v err=%v", execution, err)
	}
	verification, err := instance.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("verify empty-set switchover: %+v err=%v", verification, err)
	}
	for _, step := range []string{"fence_source", "capture_source_gtid", "wait_target_gtid", "promote_target", "transfer_writer_endpoint"} {
		if done, _ := progress.StepCompleted(context.Background(), step); !done {
			t.Fatalf("empty-set switchover skipped %s", step)
		}
	}
	if !client.primaryReadOnly || !client.primarySuperReadOnly || !client.primaryReplication || client.targetReadOnly || client.targetSuperReadOnly || client.targetReplication {
		t.Fatal("empty-set switchover did not leave the former primary read-only and replicating, and the target uniquely writable")
	}
}

func TestSwitchoverRejectsInvalidFencedGTIDResponse(t *testing.T) {
	tests := []struct {
		name string
		rows []Row
		err  error
	}{
		{name: "query failed", err: errors.New("injected GTID query failure")},
		{name: "no rows"},
		{name: "multiple rows", rows: []Row{{"gtid_executed": ""}, {"gtid_executed": ""}}},
		{name: "missing field", rows: []Row{{}}},
		{name: "malformed set", rows: []Row{{"gtid_executed": "not-a-gtid"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := switchoverRequestFixture()
			client := &fencedGTIDSQLClient{switchoverSQLClient: newSwitchoverSQLClient(request), overrideFenced: true, fencedRows: test.rows, fencedError: test.err}
			instance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
			plan, err := instance.BuildPlan(context.Background(), request)
			if err != nil {
				t.Fatalf("build plan: %v", err)
			}
			request.Plan = &plan
			execution, err := instance.Execute(context.Background(), request)
			if err == nil || execution.Status != model.OperationBlocked || failureClass(err) != "fenced" {
				t.Fatalf("invalid fenced GTID response was accepted: %+v err=%v", execution, err)
			}
			if !client.primaryReadOnly || !client.primarySuperReadOnly || !client.targetReadOnly || !client.targetSuperReadOnly || !client.targetReplication {
				t.Fatal("invalid GTID response changed protected source/target state")
			}
			for _, statement := range client.statements() {
				if strings.HasPrefix(statement, client.targetHost+" ") {
					t.Fatalf("invalid GTID response mutated target: %s", statement)
				}
			}
		})
	}
}

func TestSwitchoverEmptyGTIDSetStillRequiresTargetWait(t *testing.T) {
	request := emptyGTIDSwitchoverRequest()
	client := &fencedGTIDSQLClient{switchoverSQLClient: newSwitchoverSQLClient(request), emptyWaitResult: "1"}
	instance := NewWithEndpointProvider(client, &recordingEndpointProvider{})
	plan, err := instance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := instance.Execute(context.Background(), request)
	if err == nil || execution.Status != model.OperationBlocked || !strings.Contains(execution.Message, "target did not execute") {
		t.Fatalf("empty-set target wait was skipped: %+v err=%v", execution, err)
	}
	if !client.targetReadOnly || !client.targetSuperReadOnly || !client.targetReplication {
		t.Fatal("unsatisfied target wait promoted the target")
	}
}
