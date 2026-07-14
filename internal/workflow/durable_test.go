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
	executeCalls       int
	verifyCalls        int
	executeError       error
	executeStarted     chan struct{}
	executeRelease     chan struct{}
	afterExecute       func()
	verificationResult *model.Verification
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
	if candidate.afterExecute != nil {
		candidate.afterExecute()
	}
	if candidate.executeError != nil {
		return model.Execution{Status: model.OperationIndeterminate, Message: candidate.executeError.Error()}, candidate.executeError
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

func (candidate *durableAdapter) Verify(ctx context.Context, _ adapter.OperationRequest) (model.Verification, error) {
	candidate.verifyCalls++
	if err := ctx.Err(); err != nil {
		return model.Verification{}, err
	}
	if candidate.verificationResult != nil {
		return *candidate.verificationResult, nil
	}
	return model.Verification{Passed: true, Checks: []model.Check{{Name: "verified", Status: model.CheckPass}}}, nil
}

type durableCommittedFailure struct{}

func (durableCommittedFailure) Error() string {
	return "target promotion outcome requires verification"
}
func (durableCommittedFailure) FailureClass() string { return "promoted_unverified" }

type failingAtomicRepository struct {
	*store.Repository
	err error
}

func (repository *failingAtomicRepository) FinalizeOperation(model.ResourceID, uint64, model.OperationTransition, []model.AuditEvent, []model.Report) (model.OperationRecord, error) {
	return model.OperationRecord{}, repository.err
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

func TestDurableWorkflowLocksTopologyBeforeCapturingPlanObservation(t *testing.T) {
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
	want := []string{"gate:safety", "gate:lock", "gate:discover", "gate:revalidate", "gate:approval", "gate:release"}
	if len(trace) != len(want) {
		t.Fatalf("workflow gate trace: got %v want %v", trace, want)
	}
	for index := range want {
		if trace[index] != want[index] {
			t.Fatalf("workflow gate trace[%d]: got %q want %q", index, trace[index], want[index])
		}
	}
}

func TestDurableWorkflowDoesNotPublishFinalAuditsBeforeAtomicFinalization(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := &failingAtomicRepository{Repository: store.NewMemory(), err: errors.New("terminal snapshot unavailable")}
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
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("atomic finalization failure result=%+v err=%v", execution, err)
	}
	for _, event := range repository.Audits() {
		if event.Message == "verification passed" || event.Message == "operation audit recorded" {
			t.Fatalf("final audit escaped failed atomic publication: %+v", event)
		}
	}
	if len(repository.Reports()) != 0 {
		t.Fatalf("terminal report escaped failed atomic publication: %+v", repository.Reports())
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status == model.OperationSucceeded {
		t.Fatalf("failed atomic publication exposed success: found=%t record=%+v", found, record)
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
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationRunning || record.Stage != model.StageExecute {
		t.Fatalf("running adapter execution was not durably visible at execute stage: found=%t record=%+v", found, record)
	}

	duplicate, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrOperationInProgress) || duplicate.Status != model.OperationRunning {
		t.Fatalf("concurrent duplicate result=%+v err=%v", duplicate, err)
	}
	record, found = repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || durableTerminalStatus(record.Status) {
		t.Fatalf("concurrent duplicate terminalized shared operation: found=%t record=%+v", found, record)
	}

	close(candidate.executeRelease)
	first := <-firstResult
	if first.err != nil || first.execution.Status != model.OperationSucceeded {
		t.Fatalf("first execution did not complete: result=%+v err=%v", first.execution, first.err)
	}
}

func TestDurablePrecheckAndPlanPersistReadOnlyStages(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)

	record, checks, err := service.Precheck(context.Background(), request)
	if err != nil || len(checks) != 1 || checks[0].Status != model.CheckPass {
		t.Fatalf("precheck: record=%+v checks=%+v err=%v", record, checks, err)
	}
	if record.Stage != model.StagePrecheck || record.Status != model.OperationPlanned || record.Observation == "" {
		t.Fatalf("precheck stage was not persisted: %+v", record)
	}

	record, plan, err := service.Plan(context.Background(), request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if record.Stage != model.StagePlan || record.Status != model.OperationPlanned || plan.Digest == "" || record.Plan.Digest != plan.Digest {
		t.Fatalf("plan stage was not persisted: record=%+v plan=%+v", record, plan)
	}
}

func TestDurableWorkflowStillVerifiesAfterCommittedAdapterError(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	candidate.executeError = durableCommittedFailure{}
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)

	execution, err := service.Execute(context.Background(), request, "approved")
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("committed error result=%+v err=%v", execution, err)
	}
	if candidate.verifyCalls != 1 {
		t.Fatalf("post-commit verification calls=%d, want 1", candidate.verifyCalls)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || !record.Verification.Passed || record.Status != model.OperationIndeterminate {
		t.Fatalf("post-commit verification evidence was not durable: found=%t record=%+v", found, record)
	}
}

func TestDurableWorkflowVerificationSurvivesCallerCancellationAfterMutation(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	candidate.afterExecute = cancel
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)

	execution, err := service.Execute(ctx, request, "approved")
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("detached verification result=%+v err=%v", execution, err)
	}
	if candidate.verifyCalls != 1 {
		t.Fatalf("verification calls=%d, want 1", candidate.verifyCalls)
	}
}

func TestDurableWorkflowPersistsFailedPostCommitVerificationAsIndeterminate(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	candidate.verificationResult = &model.Verification{Passed: false, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "endpoint owner is unknown"}}}
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)

	execution, err := service.Execute(context.Background(), request, "approved")
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("failed verification result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationIndeterminate || record.Verification.Passed || len(record.Verification.Checks) != 1 {
		t.Fatalf("failed verification evidence was not persisted conservatively: found=%t record=%+v", found, record)
	}
}

func TestManualVerificationReconcilesIndeterminateOperation(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	candidate.verificationResult = &model.Verification{Passed: false, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckFail}}}
	service := newDurableWorkflowService(t, repository, candidate, request, resolved)
	if _, err := service.Execute(context.Background(), request, "approved"); err == nil {
		t.Fatal("initial failed verification unexpectedly succeeded")
	}
	candidate.verificationResult = &model.Verification{Passed: true, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckPass}}}
	verification, err := service.Verify(context.Background(), request)
	if err != nil || !verification.Passed {
		t.Fatalf("manual verification result=%+v err=%v", verification, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationSucceeded || record.Stage != model.StageReport || !record.Verification.Passed {
		t.Fatalf("manual verification did not reconcile operation: found=%t record=%+v", found, record)
	}
}

func TestDurableWorkflowStillVerifiesAfterCommittedExecutionAuditFailure(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	journal := stageFailingJournal{MemoryJournal: NewMemoryJournal(), stage: model.StageExecute, err: errors.New("execute audit unavailable")}
	resolver := OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
		candidate.Resolved = &resolved
		candidate.Credentials = resolved.Credentials
		return candidate, nil
	})
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal,
		WithOperationStore(repository), WithOperationResolver(resolver))

	execution, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("audit failure result=%+v err=%v", execution, err)
	}
	if candidate.verifyCalls != 1 {
		t.Fatalf("verification calls=%d, want 1", candidate.verifyCalls)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || !record.Verification.Passed || record.Status != model.OperationIndeterminate {
		t.Fatalf("audit failure verification evidence was not durable: found=%t record=%+v", found, record)
	}
}

func TestDurableWorkflowDoesNotPersistSuccessBeforeReportAudit(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	journal := stageFailingJournal{MemoryJournal: NewMemoryJournal(), stage: model.StageReport, err: errors.New("report audit unavailable")}
	resolver := OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
		candidate.Resolved = &resolved
		candidate.Credentials = resolved.Credentials
		return candidate, nil
	})
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal,
		WithOperationStore(repository), WithOperationResolver(resolver))

	execution, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("report audit failure result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationIndeterminate {
		t.Fatalf("report audit failure left a non-conservative terminal record: found=%t record=%+v", found, record)
	}
}

func TestDurableWorkflowPersistsIndeterminateWhenVerificationAuditFails(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := store.NewMemory()
	candidate := newDurableAdapter()
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	trace := []string{}
	journal := stageFailingJournal{MemoryJournal: NewMemoryJournal(), stage: model.StageVerify, err: errors.New("verification audit unavailable")}
	resolver := OperationResolverFunc(func(_ context.Context, candidate adapter.OperationRequest) (adapter.OperationRequest, error) {
		candidate.Resolved = &resolved
		candidate.Credentials = resolved.Credentials
		return candidate, nil
	})
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal,
		WithOperationStore(repository), WithOperationResolver(resolver))

	execution, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("verification audit failure result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationIndeterminate || !record.Verification.Passed {
		t.Fatalf("verification audit failure did not persist conservative evidence: found=%t record=%+v", found, record)
	}
}
