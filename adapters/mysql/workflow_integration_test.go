package mysql

import (
	"context"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type integrationGates struct {
	resolved adapter.ResolvedOperation
}

func (gates integrationGates) CaptureObservation(context.Context, model.Operation) (workflow.ObservationToken, error) {
	return workflow.ObservationToken{ClusterID: gates.resolved.Cluster.ResourceID, ObservedAt: gates.resolved.Snapshot.ObservedAt}, nil
}
func (integrationGates) RevalidateObservation(context.Context, model.Operation, workflow.ObservationToken) error {
	return nil
}
func (integrationGates) Evaluate(context.Context, model.Operation) error { return nil }
func (integrationGates) Acquire(context.Context, model.Operation) (func(), error) {
	return func() {}, nil
}
func (integrationGates) Validate(context.Context, model.Operation, string) error { return nil }
func (integrationGates) Consume(_ context.Context, operation model.OperationRecord, _ string) (model.ResourceID, model.OperationRecord, error) {
	operation.Stage = model.StageApprove
	return model.NewResourceID(), operation, nil
}

func TestGuardedMySQLSwitchoverRunsThroughDurableWorkflow(t *testing.T) {
	request := switchoverRequestFixture()
	request.IdempotencyKey = "mysql-integration-switch"
	resolved := *request.Resolved
	client := newSwitchoverSQLClient(request)
	provider := &recordingEndpointProvider{}
	mysqlAdapter := NewWithEndpointProvider(client, provider)
	registry := adapter.NewRegistry()
	if err := registry.Register(mysqlAdapter); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	gates := integrationGates{resolved: resolved}
	service := workflow.New(registry, gates, gates, gates, gates, repository,
		workflow.WithOperationStore(repository),
		workflow.WithOperationResolver(workflow.OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
			candidate.Resolved = &resolved
			candidate.Credentials = resolved.Credentials
			return candidate, nil
		})),
	)

	execution, err := service.Execute(context.Background(), request, "approved")
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("execute: result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationSucceeded || !record.Verification.Passed || len(record.Attempts) != 9 {
		t.Fatalf("durable operation is incomplete: found=%t record=%+v", found, record)
	}
	if len(repository.Audits()) < 9 || len(repository.Reports()) != 1 || repository.Reports()[0].Status != model.OperationSucceeded {
		t.Fatalf("audit/report trail is incomplete: audits=%+v reports=%+v", repository.Audits(), repository.Reports())
	}
	statementCount := len(client.statements())
	repeated, err := service.Execute(context.Background(), request, "approved")
	if err != nil || repeated.Status != model.OperationSucceeded || len(client.statements()) != statementCount {
		t.Fatalf("idempotent retry mutated again: result=%+v statements=%v err=%v", repeated, client.statements(), err)
	}
}
