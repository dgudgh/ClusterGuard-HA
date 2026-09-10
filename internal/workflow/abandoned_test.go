package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type planAuditFailingRepository struct {
	*store.Repository
	failed bool
}

type stageTransitionFailingRepository struct {
	*store.Repository
	stage  model.WorkflowStage
	failed bool
}

func (repository *stageTransitionFailingRepository) TransitionOperation(resourceID model.ResourceID, revision uint64, transition model.OperationTransition) (model.OperationRecord, error) {
	if transition.Stage == repository.stage && transition.Status == model.OperationRunning && !repository.failed {
		repository.failed = true
		return model.OperationRecord{}, errors.New("stage transition unavailable")
	}
	return repository.Repository.TransitionOperation(resourceID, revision, transition)
}

func (repository *planAuditFailingRepository) RecordAudit(event model.AuditEvent) error {
	if event.Stage == model.StagePlan && !repository.failed {
		repository.failed = true
		return errors.New("plan audit unavailable")
	}
	return repository.Repository.RecordAudit(event)
}

func abandonedOperationFixture(t *testing.T, repository *store.Repository, stage model.WorkflowStage) model.OperationRecord {
	t.Helper()
	record, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{
			ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationFailover,
			RequestedBy: AutomaticRecoveryActor,
		},
		TargetID: model.NewResourceID(), IdempotencyKey: "abandoned-" + string(stage) + "-" + string(model.NewResourceID()),
	})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: stage, Status: model.OperationRunning, Message: "operation in progress",
	})
	if err != nil {
		t.Fatalf("make operation running: %v", err)
	}
	return record
}

func TestReconcileAbandonedOperationsClassifiesMutationBoundary(t *testing.T) {
	for _, test := range []struct {
		name             string
		stage            model.WorkflowStage
		wantStatus       model.OperationStatus
		wantFailureClass string
		wantReview       bool
	}{
		{name: "plan is pre-commit", stage: model.StagePlan, wantStatus: model.OperationFailed, wantFailureClass: "abandoned_pre_commit"},
		{name: "execute is uncertain", stage: model.StageExecute, wantStatus: model.OperationIndeterminate, wantFailureClass: "abandoned_post_commit", wantReview: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := store.NewMemory()
			record := abandonedOperationFixture(t, repository, test.stage)
			service := New(nil, nil, nil, nil, nil, repository, WithOperationStore(repository))
			service.now = func() time.Time { return record.UpdatedAt.Add(31 * time.Minute) }

			reconciled, err := service.ReconcileAbandonedOperations(context.Background(), 30*time.Minute)
			if err != nil {
				t.Fatalf("reconcile abandoned operation: %v", err)
			}
			if len(reconciled) != 1 {
				t.Fatalf("reconciled operations=%d, want 1", len(reconciled))
			}
			updated, found := repository.Operation(record.ResourceID)
			if !found || updated.Status != test.wantStatus || updated.FailureClass != test.wantFailureClass || updated.RequiresReview() != test.wantReview {
				t.Fatalf("reconciled record: found=%t record=%+v", found, updated)
			}
			if len(repository.Audits()) != 2 || len(repository.Reports()) != 1 {
				t.Fatalf("terminal timeline audits=%d reports=%d", len(repository.Audits()), len(repository.Reports()))
			}
		})
	}
}

func TestReconcileAbandonedOperationsPreservesFreshWork(t *testing.T) {
	repository := store.NewMemory()
	fresh := abandonedOperationFixture(t, repository, model.StagePlan)
	service := New(nil, nil, nil, nil, nil, repository, WithOperationStore(repository))
	service.now = func() time.Time { return fresh.UpdatedAt.Add(29 * time.Minute) }
	reconciled, err := service.ReconcileAbandonedOperations(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("reconcile fresh operation: %v", err)
	}
	if len(reconciled) != 0 {
		t.Fatalf("reconciled fresh operations=%d, want 0", len(reconciled))
	}
	if record, found := repository.Operation(fresh.ResourceID); !found || record.Status != model.OperationRunning {
		t.Fatalf("fresh operation changed: found=%t record=%+v", found, record)
	}
}

func TestReconcileAbandonedOperationsPreservesLeasedAndInflightWork(t *testing.T) {
	repository := store.NewMemory()
	leased := abandonedOperationFixture(t, repository, model.StageExecute)
	inflight := abandonedOperationFixture(t, repository, model.StagePlan)
	service := New(nil, nil, nil, nil, nil, repository, WithOperationStore(repository))
	now := leased.UpdatedAt.Add(31 * time.Minute)
	service.now = func() time.Time { return now }
	if err := repository.PutCoordinationOperationLock(coordination.OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: leased.Operation.ClusterID, OperationID: leased.ResourceID,
		CreatedAt: leased.UpdatedAt, UpdatedAt: leased.UpdatedAt, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("persist operation lease: %v", err)
	}
	if !service.claimOperation(inflight.ResourceID) {
		t.Fatal("claim inflight operation")
	}
	defer service.releaseOperation(inflight.ResourceID)

	reconciled, err := service.ReconcileAbandonedOperations(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("reconcile protected operations: %v", err)
	}
	if len(reconciled) != 0 {
		t.Fatalf("reconciled protected operations=%d, want 0", len(reconciled))
	}
	for _, id := range []model.ResourceID{leased.ResourceID, inflight.ResourceID} {
		if record, found := repository.Operation(id); !found || record.Status != model.OperationRunning {
			t.Fatalf("protected operation %s changed: found=%t record=%+v", id, found, record)
		}
	}
}

func TestReconcileAbandonedOperationsPreservesPostCommitEvidence(t *testing.T) {
	for _, test := range []struct {
		name             string
		failureClass     string
		verification     model.Verification
		wantStatus       model.OperationStatus
		wantFailureClass string
	}{
		{name: "verified outcome succeeds", verification: model.Verification{Passed: true, ObservedAt: time.Now().UTC()}, wantStatus: model.OperationSucceeded},
		{name: "automatic promotion remains resumable", failureClass: "promoted_unverified", wantStatus: model.OperationIndeterminate, wantFailureClass: "promoted_unverified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := store.NewMemory()
			record := abandonedOperationFixture(t, repository, model.StageVerify)
			record, err := repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
				Stage: model.StageVerify, Status: model.OperationRunning, Verification: &test.verification,
				FailureClass: test.failureClass, Message: "persisted post-commit evidence",
			})
			if err != nil {
				t.Fatalf("persist operation evidence: %v", err)
			}
			service := New(nil, nil, nil, nil, nil, repository, WithOperationStore(repository))
			service.now = func() time.Time { return record.UpdatedAt.Add(31 * time.Minute) }
			reconciled, err := service.ReconcileAbandonedOperations(context.Background(), 30*time.Minute)
			if err != nil || len(reconciled) != 1 {
				t.Fatalf("reconcile evidence: records=%d err=%v", len(reconciled), err)
			}
			if reconciled[0].Status != test.wantStatus || reconciled[0].FailureClass != test.wantFailureClass {
				t.Fatalf("reconciled evidence=%+v", reconciled[0])
			}
		})
	}
}

func TestDurableWorkflowTerminalizesPreCommitJournalFailure(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := &planAuditFailingRepository{Repository: store.NewMemory()}
	candidate := newDurableAdapter()
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
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, repository,
		WithOperationStore(repository), WithOperationResolver(resolver))

	execution, err := service.Execute(context.Background(), request, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationFailed {
		t.Fatalf("journal failure result=%+v err=%v", execution, err)
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationFailed || record.Stage != model.StagePlan || record.FailureClass != "journal" {
		t.Fatalf("journal failure was not terminalized: found=%t record=%+v", found, record)
	}
	if candidate.executeCalls != 0 {
		t.Fatalf("adapter executed after plan journal failure: calls=%d", candidate.executeCalls)
	}
}

func TestDurableWorkflowExitGuardTerminalizesRunningRecord(t *testing.T) {
	request, resolved := durableRequestFixture()
	repository := &stageTransitionFailingRepository{Repository: store.NewMemory(), stage: model.StageSafetyGuard}
	candidate := newDurableAdapter()
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
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, repository,
		WithOperationStore(repository), WithOperationResolver(resolver))

	if _, err := service.Execute(context.Background(), request, "approved"); err == nil {
		t.Fatal("workflow ignored stage transition failure")
	}
	record, found := repository.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || record.Status != model.OperationFailed || record.Stage != model.StagePlan || record.FailureClass != "interrupted_pre_commit" {
		t.Fatalf("exit guard left a running operation: found=%t record=%+v", found, record)
	}
	if candidate.executeCalls != 0 {
		t.Fatalf("adapter executed after durable transition failure: calls=%d", candidate.executeCalls)
	}
}
