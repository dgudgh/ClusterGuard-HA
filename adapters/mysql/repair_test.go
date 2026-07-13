package mysql

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type maintenanceStoreStub struct {
	states map[model.ResourceID]bool
}

func (store *maintenanceStoreStub) SetMaintenance(_ context.Context, clusterID, instanceID model.ResourceID, value bool) error {
	if clusterID == "" || instanceID == "" {
		return fmt.Errorf("resource scope is required")
	}
	store.states[instanceID] = value
	return nil
}

func (store *maintenanceStoreStub) Maintenance(_ context.Context, clusterID, instanceID model.ResourceID) (bool, error) {
	if clusterID == "" || instanceID == "" {
		return false, fmt.Errorf("resource scope is required")
	}
	return store.states[instanceID], nil
}

func replicationRepairFixture(action string) adapter.OperationRequest {
	request := switchoverRequestFixture()
	request.Operation.Kind = model.OperationReplicationRepair
	request.Parameters = map[string]string{"action": action}
	return request
}

func TestRepairRejectsSkipTransactionAndResetReplica(t *testing.T) {
	for _, action := range []string{"skip_transaction", "reset_replica", "change_source", "promote"} {
		t.Run(action, func(t *testing.T) {
			checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), replicationRepairFixture(action))
			if err != nil {
				t.Fatalf("precheck: %v", err)
			}
			if !failedCheck(checks, "repair_action_allowlist") {
				t.Fatalf("dangerous repair action %q was not blocked: %+v", action, checks)
			}
		})
	}
}

func TestRepairStartsOnlyRequestedStoppedThread(t *testing.T) {
	request := replicationRepairFixture("start_io_thread")
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	client := newThreeNodeSQLClient(request)
	target := client.nodes[request.Resolved.Target.Hostname]
	target.ioRunning = false
	target.sqlRunning = true
	adapterInstance := NewWithEndpointProvider(client, passingEndpointProvider())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	execution, err := adapterInstance.Execute(context.Background(), request)
	if err != nil || execution.Status != model.OperationRunning {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	if len(client.statements) != 1 || !strings.HasSuffix(client.statements[0], "START REPLICA IO_THREAD") {
		t.Fatalf("repair statements=%v", client.statements)
	}
	if !target.ioRunning || !target.sqlRunning {
		t.Fatalf("repair did not restore only the requested thread: %+v", target)
	}
}

func TestRepairUsesLegacyThreadStatementForMySQL57(t *testing.T) {
	request := replicationRepairFixture("start_io_thread")
	request.Resolved.Target.EngineMetadata["version"] = "5.7.44"
	request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
	client := newThreeNodeSQLClient(request)
	client.version = "5.7.44"
	client.nodes[request.Resolved.Target.Hostname].ioRunning = false
	adapterInstance := NewWithEndpointProvider(client, passingEndpointProvider())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	request.Plan = &plan
	if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(client.statements) != 1 || !strings.HasSuffix(client.statements[0], "START SLAVE IO_THREAD") {
		t.Fatalf("legacy repair statements=%v", client.statements)
	}
}

func TestRepairMaintenanceActionsUpdatePlatformInventory(t *testing.T) {
	for _, test := range []struct {
		action string
		want   bool
	}{
		{action: "begin_maintenance", want: true},
		{action: "end_maintenance", want: false},
	} {
		t.Run(test.action, func(t *testing.T) {
			request := replicationRepairFixture(test.action)
			request.Resolved.Credentials = adapter.Credentials{Username: "operator", Password: "operation-secret"}
			client := newThreeNodeSQLClient(request)
			maintenance := &maintenanceStoreStub{states: map[model.ResourceID]bool{request.TargetID: !test.want}}
			adapterInstance := NewWithProviders(client, passingEndpointProvider(), maintenance)
			plan, err := adapterInstance.BuildPlan(context.Background(), request)
			if err != nil {
				t.Fatalf("build plan: %v", err)
			}
			request.Plan = &plan
			if _, err := adapterInstance.Execute(context.Background(), request); err != nil {
				t.Fatalf("execute: %v", err)
			}
			verification, err := adapterInstance.Verify(context.Background(), request)
			if err != nil || !verification.Passed || maintenance.states[request.TargetID] != test.want {
				t.Fatalf("verification=%+v state=%t err=%v", verification, maintenance.states[request.TargetID], err)
			}
		})
	}
}
