package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type durableAdapter struct {
	adapter.UnsupportedAdapter
	executeCalls   int
	executeStarted chan struct{}
	executeRelease chan struct{}
}

func newDurableAdapter() *durableAdapter {
	return &durableAdapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL)}
}

func (candidate *durableAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityPrecheck: {Available: true},
		adapter.CapabilityPlan:     {Available: true},
		adapter.CapabilityExecute:  {Available: true, Mutating: true},
		adapter.CapabilityVerify:   {Available: true},
	}}
}

func (candidate *durableAdapter) Precheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return []model.Check{{Name: "ready", Status: model.CheckPass, Message: "ready"}}, nil
}

func (candidate *durableAdapter) BuildPlan(_ context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	resolved := request.Resolved
	return model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: request.Operation.ClusterID,
		SourceID: resolved.Primary.ResourceID, TargetID: request.TargetID, Stage: model.StagePlan,
		ObservationToken: string(request.Operation.ClusterID) + "@" + resolved.Snapshot.ObservedAt.Format(time.RFC3339Nano),
		ResourceRevisions: map[model.ResourceID]uint64{
			resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision,
			resolved.Primary.ResourceID: resolved.Primary.MetadataRevision,
			resolved.Target.ResourceID:  resolved.Target.MetadataRevision,
		},
		Checks: []model.Check{{Name: "ready", Status: model.CheckPass}},
		Steps:  []model.PlanStep{{Index: 1, Name: "execute", Owner: "test", TargetID: request.TargetID, Mutating: true}},
		Digest: "sha256:durable-test", Summary: "durable test plan", Mutating: true,
	}, nil
}

func (candidate *durableAdapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	candidate.executeCalls++
	if candidate.executeStarted != nil {
		close(candidate.executeStarted)
		<-candidate.executeRelease
	}
	if request.Progress == nil {
		return model.Execution{}, errors.New("progress recorder is missing")
	}
	if err := request.Progress.CompleteStep(ctx, "execute", "test mutation completed"); err != nil {
		return model.Execution{Status: model.OperationIndeterminate, Message: err.Error()}, err
	}
	return model.Execution{Status: model.OperationRunning, Message: "executed"}, nil
}

func newDurableWorkflowService(t *testing.T, repository *store.Repository, candidate *durableAdapter, request adapter.OperationRequest, resolved adapter.ResolvedOperation) *Service {
	t.Helper()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	resolver := OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
		candidate.Resolved = &resolved
		candidate.Credentials = resolved.Credentials
		return candidate, nil
	})
	return New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, repository,
		WithOperationStore(repository), WithOperationResolver(resolver))
}

func (candidate *durableAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	return model.Verification{Passed: true, Checks: []model.Check{{Name: "verified", Status: model.CheckPass}}}, nil
}

func durableRequestFixture() (adapter.OperationRequest, adapter.ResolvedOperation) {
	observedAt := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	cluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 2}, Engine: model.EngineMySQL, DisplayName: "mysql-test"}
	primary := model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 3}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Role: model.RolePrimary}
	target := model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 4}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Role: model.RoleReplica}
	resolved := adapter.ResolvedOperation{
		Cluster: cluster, Primary: primary, Target: target,
		Snapshot:    model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: []model.DatabaseInstance{primary, target}, ObservedAt: observedAt},
		Credentials: adapter.Credentials{Username: "clusterguard", Password: "secret"},
	}
	request := adapter.OperationRequest{
		Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"},
		TargetID:  target.ResourceID, IdempotencyKey: "durable-switch-1",
	}
	return request, resolved
}

func TestDurableWorkflowPersistsPlanProgressAndTerminalOutcome(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	registry := adapter.NewRegistry()
	candidate := newDurableAdapter()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	resolver := OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
		candidate.Resolved = &resolved
		candidate.Credentials = resolved.Credentials
		return candidate, nil
	})
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, repository,
		WithOperationStore(repository), WithOperationResolver(resolver))

	execution, err := service.Execute(context.Background(), request, "approved")
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("durable execute: result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationSucceeded || record.Stage != model.StageReport {
		t.Fatalf("terminal operation was not persisted: found=%t record=%+v", found, record)
	}
	if record.Plan.Digest == "" || len(record.Attempts) != 1 || record.Attempts[0].Step != "execute" {
		t.Fatalf("plan or progress was not persisted: %+v", record)
	}
	if candidate.executeCalls != 1 {
		t.Fatalf("adapter execute calls=%d", candidate.executeCalls)
	}

	repeated, err := service.Execute(context.Background(), request, "approved")
	if err != nil || repeated.Status != model.OperationSucceeded || candidate.executeCalls != 1 {
		t.Fatalf("terminal idempotent retry executed again: result=%+v calls=%d err=%v", repeated, candidate.executeCalls, err)
	}
}

func TestDurableWorkflowBlocksChangedObservationBeforeMutation(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	registry := adapter.NewRegistry()
	candidate := newDurableAdapter()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	gate := changingDiscoveryGate{trace: &trace, err: errors.New("topology observation changed")}
	service := New(registry, gate, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, repository,
		WithOperationStore(repository), WithOperationResolver(OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
			candidate.Resolved = &resolved
			return candidate, nil
		})))
	execution, err := service.Execute(context.Background(), request, "approved")
	if err == nil || execution.Status != model.OperationBlocked || candidate.executeCalls != 0 {
		t.Fatalf("changed observation was not blocked: result=%+v calls=%d err=%v", execution, candidate.executeCalls, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationBlocked {
		t.Fatalf("blocked outcome was not durable: found=%t record=%+v", found, record)
	}
}

func TestDurableWorkflowRejectsConcurrentDuplicateWithoutTerminalizingSharedRecord(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	candidate.executeStarted = make(chan struct{})
	candidate.executeRelease = make(chan struct{})
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)

	type result struct {
		execution model.Execution
		err       error
	}
	firstResult := make(chan result, 1)
	go func() {
		execution, err := service.Execute(context.Background(), request, "approved")
		firstResult <- result{execution: execution, err: err}
	}()
	<-candidate.executeStarted

	duplicate, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrOperationInProgress) || duplicate.Status != model.OperationRunning {
		t.Fatalf("concurrent duplicate result=%+v err=%v", duplicate, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || durableTerminalStatus(record.Status) {
		t.Fatalf("concurrent duplicate terminalized shared operation: found=%t record=%+v", found, record)
	}

	close(candidate.executeRelease)
	first := <-firstResult
	if first.err != nil || first.execution.Status != model.OperationSucceeded {
		t.Fatalf("first execution did not complete: result=%+v err=%v", first.execution, first.err)
	}
}
