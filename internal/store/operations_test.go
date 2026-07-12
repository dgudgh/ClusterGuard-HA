package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func operationFixture() model.OperationRecord {
	return model.OperationRecord{
		Operation: model.Operation{
			ClusterID:   model.NewResourceID(),
			Engine:      model.EngineMySQL,
			Kind:        model.OperationSwitchover,
			RequestedBy: "dba",
		},
		TargetID:       model.NewResourceID(),
		IdempotencyKey: "switch-20260712-1",
	}
}

func TestOperationCreateIsIdempotentAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}

	request := operationFixture()
	created, reused, err := repository.CreateOperation(request)
	if err != nil || reused {
		t.Fatalf("create operation: reused=%t err=%v", reused, err)
	}
	if !model.ValidResourceID(created.ResourceID) || created.Operation.ResourceID != created.ResourceID {
		t.Fatalf("operation IDs were not established: %+v", created)
	}
	if created.MetadataRevision != 1 || created.Status != model.OperationPlanned || created.Stage != model.StageDiscover {
		t.Fatalf("unexpected initial operation state: %+v", created)
	}

	reusedRecord, reused, err := repository.CreateOperation(request)
	if err != nil || !reused || reusedRecord.ResourceID != created.ResourceID {
		t.Fatalf("idempotent create failed: record=%+v reused=%t err=%v", reusedRecord, reused, err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	persisted, found := reopened.Operation(created.ResourceID)
	if !found || persisted.IdempotencyKey != request.IdempotencyKey || persisted.TargetID != request.TargetID {
		t.Fatalf("operation did not survive restart: found=%t record=%+v", found, persisted)
	}
	byKey, found := reopened.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found || byKey.ResourceID != created.ResourceID {
		t.Fatalf("idempotency index did not survive restart: found=%t record=%+v", found, byKey)
	}
}

func TestOperationIdempotencyKeyRejectsDifferentIntent(t *testing.T) {
	repository := NewMemory()
	request := operationFixture()
	if _, _, err := repository.CreateOperation(request); err != nil {
		t.Fatalf("create operation: %v", err)
	}

	conflict := request
	conflict.TargetID = model.NewResourceID()
	if _, _, err := repository.CreateOperation(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("different target reused idempotency key: %v", err)
	}

	conflict = request
	conflict.Operation.Kind = model.OperationFailover
	if _, _, err := repository.CreateOperation(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("different operation kind reused idempotency key: %v", err)
	}
}

func TestOperationPlanIsImmutableAndUsesRevisionCAS(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}

	plan := model.OperationPlan{
		OperationID:      created.ResourceID,
		ClusterID:        created.Operation.ClusterID,
		SourceID:         model.NewResourceID(),
		TargetID:         created.TargetID,
		ObservationToken: string(created.Operation.ClusterID) + "@" + time.Now().UTC().Format(time.RFC3339Nano),
		ResourceRevisions: map[model.ResourceID]uint64{
			created.Operation.ClusterID: 2,
			created.TargetID:            4,
		},
		Steps:    []model.PlanStep{{Index: 1, Name: "fence_source", Owner: "mysql", Mutating: true}},
		Digest:   "sha256:0123456789abcdef",
		Summary:  "guarded planned switchover",
		Mutating: true,
	}
	withPlan, err := repository.PutOperationPlan(created.ResourceID, created.MetadataRevision, plan)
	if err != nil {
		t.Fatalf("put operation plan: %v", err)
	}
	if withPlan.MetadataRevision != created.MetadataRevision+1 || withPlan.Plan.Digest != plan.Digest {
		t.Fatalf("plan was not persisted: %+v", withPlan)
	}

	changed := plan
	changed.Digest = "sha256:fedcba9876543210"
	if _, err := repository.PutOperationPlan(created.ResourceID, withPlan.MetadataRevision, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("operation plan was mutable: %v", err)
	}
	if _, err := repository.PutOperationPlan(created.ResourceID, created.MetadataRevision, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale operation revision was accepted: %v", err)
	}
}

func TestOperationTransitionPersistsAttemptsAndRejectsStaleRevision(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}

	started := time.Now().UTC()
	transitioned, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage:   model.StagePrecheck,
		Status:  model.OperationRunning,
		Message: "precheck running",
		Attempt: &model.StepAttempt{
			Step:      "precheck",
			Attempt:   1,
			Status:    model.OperationRunning,
			StartedAt: started,
		},
	})
	if err != nil {
		t.Fatalf("transition operation: %v", err)
	}
	if len(transitioned.Attempts) != 1 || transitioned.Attempts[0].Step != "precheck" {
		t.Fatalf("step attempt was not persisted: %+v", transitioned)
	}
	if _, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{Stage: model.StagePlan, Status: model.OperationRunning}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition revision was accepted: %v", err)
	}
}
