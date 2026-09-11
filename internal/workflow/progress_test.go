package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type progressOperationStore struct {
	mu          sync.Mutex
	record      model.OperationRecord
	transitions int
	transition  func(*progressOperationStore, uint64, model.OperationTransition) error
}

func (store *progressOperationStore) CreateOperation(model.OperationRecord) (model.OperationRecord, bool, error) {
	panic("not used")
}

func (store *progressOperationStore) PutOperationPlan(model.ResourceID, uint64, model.OperationPlan) (model.OperationRecord, error) {
	panic("not used")
}

func (store *progressOperationStore) TransitionOperation(_ model.ResourceID, revision uint64, transition model.OperationTransition) (model.OperationRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.transitions++
	if store.transition != nil {
		if err := store.transition(store, revision, transition); err != nil {
			return model.OperationRecord{}, err
		}
	}
	return store.record, nil
}

func (store *progressOperationStore) Operation(model.ResourceID) (model.OperationRecord, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.record, true
}

func (store *progressOperationStore) OperationByIdempotencyKey(string) (model.OperationRecord, bool) {
	panic("not used")
}

func progressRecordFixture() model.OperationRecord {
	resourceID := model.NewResourceID()
	return model.OperationRecord{
		ResourceMeta: model.ResourceMeta{ResourceID: resourceID, MetadataRevision: 7},
		Operation: model.Operation{
			ResourceMeta: model.ResourceMeta{ResourceID: resourceID, MetadataRevision: 7},
			Status:       model.OperationRunning,
		},
		Stage:  model.StageExecute,
		Status: model.OperationRunning,
	}
}

func TestDurableTerminalStatusContract(t *testing.T) {
	for _, test := range []struct {
		status   model.OperationStatus
		terminal bool
	}{
		{model.OperationPlanned, false},
		{model.OperationRunning, false},
		{model.OperationBlocked, true},
		{model.OperationSucceeded, true},
		{model.OperationFailed, true},
		{model.OperationIndeterminate, true},
		{model.OperationUnsupported, true},
		{"", false},
		{"future-status", false},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			if got := durableTerminalStatus(test.status); got != test.terminal {
				t.Fatalf("terminal(%q)=%t want %t", test.status, got, test.terminal)
			}
			if test.terminal {
				operations := &progressOperationStore{record: progressRecordFixture()}
				operations.record.Status = test.status
				progress := repositoryProgress{operations: operations, operationID: operations.record.ResourceID, now: time.Now}
				if err := progress.CompleteStep(context.Background(), "verify", "late completion"); err == nil || operations.transitions != 0 {
					t.Fatalf("terminal progress accepted: status=%q transitions=%d err=%v", test.status, operations.transitions, err)
				}
			}
		})
	}
}

func commitProgressTransition(store *progressOperationStore, revision uint64, transition model.OperationTransition) {
	store.record.MetadataRevision = revision + 1
	store.record.Operation.MetadataRevision = revision + 1
	store.record.Stage = transition.Stage
	store.record.Status = transition.Status
	if transition.Attempt != nil {
		store.record.Attempts = append(store.record.Attempts, *transition.Attempt)
	}
}

func TestRepositoryProgressRetriesTransientSnapshotConflict(t *testing.T) {
	conflict := errors.New("metadata changed while synchronizing controller state")
	operations := &progressOperationStore{record: progressRecordFixture()}
	operations.transition = func(store *progressOperationStore, revision uint64, transition model.OperationTransition) error {
		if store.transitions == 1 {
			return conflict
		}
		commitProgressTransition(store, revision, transition)
		return nil
	}
	progress := repositoryProgress{operations: operations, operationID: operations.record.ResourceID, now: time.Now}

	if err := progress.CompleteStep(context.Background(), "attach_former_primary", "replica attached"); err != nil {
		t.Fatalf("complete step after transient conflict: %v", err)
	}
	if operations.transitions != 2 || len(operations.record.Attempts) != 1 {
		t.Fatalf("transitions=%d attempts=%+v, want one retry and one durable attempt", operations.transitions, operations.record.Attempts)
	}
}

func TestRepositoryProgressAcceptsCommittedTransitionWarning(t *testing.T) {
	warning := errors.New("metadata directory sync warning")
	operations := &progressOperationStore{record: progressRecordFixture()}
	operations.transition = func(store *progressOperationStore, revision uint64, transition model.OperationTransition) error {
		commitProgressTransition(store, revision, transition)
		return warning
	}
	progress := repositoryProgress{operations: operations, operationID: operations.record.ResourceID, now: time.Now}

	if err := progress.CompleteStep(context.Background(), "attach_former_primary", "replica attached"); err != nil {
		t.Fatalf("complete committed step with warning: %v", err)
	}
	if operations.transitions != 1 || len(operations.record.Attempts) != 1 {
		t.Fatalf("transitions=%d attempts=%+v, want committed result without replay", operations.transitions, operations.record.Attempts)
	}
}

func TestRepositoryProgressFailsClosedAfterBoundedRetries(t *testing.T) {
	persistErr := errors.New("controller quorum unavailable")
	operations := &progressOperationStore{record: progressRecordFixture()}
	operations.transition = func(*progressOperationStore, uint64, model.OperationTransition) error { return persistErr }
	progress := repositoryProgress{operations: operations, operationID: operations.record.ResourceID, now: time.Now}

	err := progress.CompleteStep(context.Background(), "attach_former_primary", "replica attached")
	if !errors.Is(err, persistErr) {
		t.Fatalf("complete step error=%v, want persistent quorum error", err)
	}
	if operations.transitions != operationProgressCommitAttempts {
		t.Fatalf("transitions=%d, want bounded retries=%d", operations.transitions, operationProgressCommitAttempts)
	}
}

func TestRepositoryProgressDoesNotRewriteTerminalOperation(t *testing.T) {
	persistErr := errors.New("operation changed concurrently")
	operations := &progressOperationStore{record: progressRecordFixture()}
	operations.transition = func(store *progressOperationStore, _ uint64, _ model.OperationTransition) error {
		store.record.Status = model.OperationIndeterminate
		store.record.Operation.Status = model.OperationIndeterminate
		store.record.Stage = model.StageVerify
		return persistErr
	}
	progress := repositoryProgress{operations: operations, operationID: operations.record.ResourceID, now: time.Now}

	err := progress.CompleteStep(context.Background(), "attach_former_primary", "replica attached")
	if !errors.Is(err, persistErr) || operations.transitions != 1 {
		t.Fatalf("terminal conflict err=%v transitions=%d, want fail-closed without retry", err, operations.transitions)
	}
}
