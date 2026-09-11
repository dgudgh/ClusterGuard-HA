package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
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

func TestTerminalOperationStatusContract(t *testing.T) {
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
			if got := terminalOperationStatus(test.status); got != test.terminal {
				t.Fatalf("terminal(%q)=%t want %t", test.status, got, test.terminal)
			}
			report := model.Report{Status: test.status, Title: "status contract", Summary: "retained evidence"}
			if err := NewMemory().RecordReport(report); (err == nil) != test.terminal {
				t.Fatalf("report status %q: %v", test.status, err)
			}
			if _, err := upsertFinalReport(nil, report, model.NewResourceID(), time.Now()); (err == nil) != test.terminal {
				t.Fatalf("atomic report status %q: %v", test.status, err)
			}
		})
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

func TestPlannedOperationHistoryDoesNotExhaustRunningCapacity(t *testing.T) {
	repository := NewMemory()
	for index := 0; index < maximumActiveOperations; index++ {
		request := operationFixture()
		request.IdempotencyKey = fmt.Sprintf("dormant-plan-%d", index)
		if _, _, err := repository.CreateOperation(request); err != nil {
			t.Fatalf("create dormant plan %d: %v", index, err)
		}
	}
	request := operationFixture()
	request.IdempotencyKey = "plan-after-history"
	if _, _, err := repository.CreateOperation(request); err != nil {
		t.Fatalf("dormant plan history exhausted execution capacity: %v", err)
	}
}

func TestIndeterminateOperationCanOnlyBeReconciledByPersistedVerification(t *testing.T) {
	repository := NewMemory()
	request := operationFixture()
	request.IdempotencyKey = "reconcile-verification"
	operation, _, err := repository.CreateOperation(request)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	indeterminate, err := repository.TransitionOperation(operation.ResourceID, operation.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate, Message: "promotion outcome requires verification",
	})
	if err != nil {
		t.Fatalf("mark indeterminate: %v", err)
	}
	verification := model.Verification{Passed: true, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckPass}}}
	reconciled, err := repository.TransitionOperation(indeterminate.ResourceID, indeterminate.MetadataRevision, model.OperationTransition{
		Stage: indeterminate.Stage, Status: model.OperationSucceeded, Verification: &verification, Message: "manual verification passed",
	})
	if err != nil {
		t.Fatalf("reconcile verification: %v", err)
	}
	if reconciled.Status != model.OperationSucceeded || !reconciled.Verification.Passed || reconciled.Verification.OperationID != operation.ResourceID {
		t.Fatalf("reconciled operation=%+v", reconciled)
	}
	if _, err := repository.TransitionOperation(reconciled.ResourceID, reconciled.MetadataRevision, model.OperationTransition{Message: "mutate succeeded terminal"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("succeeded terminal was mutable: %v", err)
	}
}

func TestIndeterminateOperationReviewPreservesOutcomeAndPublishesAuditAtomically(t *testing.T) {
	repository := NewMemory()
	request := operationFixture()
	request.IdempotencyKey = "acknowledge-indeterminate"
	created, _, err := repository.CreateOperation(request)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	indeterminate, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate, FailureClass: "verification_unknown", Message: "manual review required",
	})
	if err != nil {
		t.Fatalf("mark indeterminate: %v", err)
	}
	if !indeterminate.RequiresReview() {
		t.Fatal("unreviewed indeterminate operation did not require review")
	}

	reviewed, err := repository.ReviewIndeterminateOperation(indeterminate.ResourceID, indeterminate.MetadataRevision, "dba", "database role and writer endpoint were checked")
	if err != nil {
		t.Fatalf("review operation: %v", err)
	}
	if reviewed.Status != model.OperationIndeterminate || reviewed.RequiresReview() || reviewed.Review == nil ||
		reviewed.Review.Disposition != model.OperationReviewAcknowledgedIndeterminate || reviewed.Review.ReviewedBy != "dba" || reviewed.Review.ReviewedAt.IsZero() {
		t.Fatalf("review changed or omitted outcome evidence: %+v", reviewed)
	}
	if len(repository.Audits()) != 1 || len(repository.Reports()) != 1 || repository.Reports()[0].Status != model.OperationIndeterminate {
		t.Fatalf("review audit/report evidence missing: audits=%+v reports=%+v", repository.Audits(), repository.Reports())
	}

	// A lost response may be retried with the original revision. Identical input
	// is idempotent, while a different review cannot overwrite the first one.
	retried, err := repository.ReviewIndeterminateOperation(indeterminate.ResourceID, indeterminate.MetadataRevision, "dba", "database role and writer endpoint were checked")
	if err != nil || retried.MetadataRevision != reviewed.MetadataRevision || len(repository.Audits()) != 1 || len(repository.Reports()) != 1 {
		t.Fatalf("idempotent review retry failed: record=%+v err=%v", retried, err)
	}
	if _, err := repository.ReviewIndeterminateOperation(reviewed.ResourceID, reviewed.MetadataRevision, "dba", "replace the original note"); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable review was overwritten: %v", err)
	}

	reviewed.Review.Note = "mutated caller copy"
	persisted, found := repository.Operation(reviewed.ResourceID)
	if !found || persisted.Review == nil || persisted.Review.Note != "database role and writer endpoint were checked" {
		t.Fatalf("review pointer escaped repository cloning: %+v", persisted)
	}
}

func TestOperationReviewRejectsNonIndeterminateOutcomeAndMissingNote(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if _, err := repository.ReviewIndeterminateOperation(created.ResourceID, created.MetadataRevision, "dba", "reviewed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("planned operation accepted a review: %v", err)
	}
	if _, err := repository.ReviewIndeterminateOperation(created.ResourceID, created.MetadataRevision, "dba", " "); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank review note was accepted: %v", err)
	}
}

func TestReviewedIndeterminateOperationCanStillReconcileToVerifiedSuccess(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	indeterminate, err := repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageVerify, Status: model.OperationIndeterminate, Message: "review required",
	})
	if err != nil {
		t.Fatalf("mark indeterminate: %v", err)
	}
	reviewed, err := repository.ReviewIndeterminateOperation(indeterminate.ResourceID, indeterminate.MetadataRevision, "dba", "live topology checked")
	if err != nil {
		t.Fatalf("review operation: %v", err)
	}
	verification := model.Verification{Passed: true, Checks: []model.Check{{Name: "writer_endpoint_owner", Status: model.CheckPass}}}
	succeeded, err := repository.TransitionOperation(reviewed.ResourceID, reviewed.MetadataRevision, model.OperationTransition{
		Stage: reviewed.Stage, Status: model.OperationSucceeded, Verification: &verification, Message: "verification passed",
	})
	if err != nil {
		t.Fatalf("reconcile reviewed operation: %v", err)
	}
	if succeeded.Status != model.OperationSucceeded || succeeded.Review == nil || succeeded.RequiresReview() {
		t.Fatalf("reconciled operation lost outcome or review evidence: %+v", succeeded)
	}
}

func TestFinalizeOperationAtomicallyPersistsTerminalTimeline(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	execution := model.Execution{OperationID: created.ResourceID, Status: model.OperationSucceeded, Message: "verified"}
	audits := []model.AuditEvent{
		{OperationID: created.ResourceID, Stage: model.StageVerify, Message: "verification passed"},
		{OperationID: created.ResourceID, Stage: model.StageReport, Message: "report generated"},
	}
	reports := []model.Report{{OperationID: created.ResourceID, Title: "switchover report", Status: model.OperationSucceeded, Summary: "verified"}}
	finalized, err := repository.FinalizeOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageReport, Status: model.OperationSucceeded, Execution: &execution, Message: "verified",
	}, audits, reports)
	if err != nil {
		t.Fatalf("finalize operation: %v", err)
	}
	if finalized.Status != model.OperationSucceeded || finalized.Stage != model.StageReport || len(repository.Audits()) != 2 || len(repository.Reports()) != 1 {
		t.Fatalf("atomic finalization mismatch: operation=%+v audits=%+v reports=%+v", finalized, repository.Audits(), repository.Reports())
	}
}

func TestFinalizeAbandonedOperationChecksProgressAndLeaseAtomically(t *testing.T) {
	repository := NewMemory()
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now.Add(-time.Hour) }
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	created, err = repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StagePlan, Status: model.OperationRunning, Message: "planned",
	})
	if err != nil {
		t.Fatalf("make operation running: %v", err)
	}
	repository.now = func() time.Time { return now }
	transition := model.OperationTransition{Stage: model.StagePlan, Status: model.OperationFailed, FailureClass: "abandoned_pre_commit", Message: "executor stopped"}
	audits := []model.AuditEvent{{OperationID: created.ResourceID, Stage: model.StagePlan, Message: "executor stopped"}}
	reports := []model.Report{{OperationID: created.ResourceID, Title: "recovery report", Status: model.OperationFailed, Summary: "executor stopped"}}

	if err := repository.PutCoordinationOperationLock(coordination.OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: created.Operation.ClusterID, OperationID: created.ResourceID,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("put live operation lock: %v", err)
	}
	if _, finalized, err := repository.FinalizeAbandonedOperation(created.ResourceID, created.MetadataRevision, now, now.Add(-30*time.Minute), transition, audits, reports); err != nil || finalized {
		t.Fatalf("live lease allowed abandoned finalization: finalized=%t err=%v", finalized, err)
	}
	if err := repository.DeleteCoordinationOperationLock(repository.CoordinationOperationLocks()[0].ResourceID); err != nil {
		t.Fatalf("delete operation lock: %v", err)
	}
	if _, finalized, err := repository.FinalizeAbandonedOperation(created.ResourceID, created.MetadataRevision+1, now, now.Add(-30*time.Minute), transition, audits, reports); err != nil || finalized {
		t.Fatalf("revision race allowed abandoned finalization: finalized=%t err=%v", finalized, err)
	}
	finalized, applied, err := repository.FinalizeAbandonedOperation(created.ResourceID, created.MetadataRevision, now, now.Add(-30*time.Minute), transition, audits, reports)
	if err != nil || !applied || finalized.Status != model.OperationFailed || len(repository.Audits()) != 1 || len(repository.Reports()) != 1 {
		t.Fatalf("abandoned finalization mismatch: applied=%t operation=%+v audits=%d reports=%d err=%v", applied, finalized, len(repository.Audits()), len(repository.Reports()), err)
	}
}

func TestFinalizeOperationPublishesNothingWhenSnapshotPersistenceFails(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	repository.path = t.TempDir()
	_, err = repository.FinalizeOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageReport, Status: model.OperationSucceeded, Message: "must not publish",
	}, []model.AuditEvent{{OperationID: created.ResourceID, Stage: model.StageReport}}, []model.Report{{OperationID: created.ResourceID, Title: "report", Status: model.OperationSucceeded}})
	if err == nil {
		t.Fatal("atomic finalization ignored persistence failure")
	}
	persisted, found := repository.Operation(created.ResourceID)
	if !found || persisted.Status == model.OperationSucceeded || len(repository.Audits()) != 0 || len(repository.Reports()) != 0 {
		t.Fatalf("failed atomic finalization published partial state: operation=%+v audits=%+v reports=%+v", persisted, repository.Audits(), repository.Reports())
	}
}

func TestOperationTimelineReadsOperationAuditsAndReportsTogether(t *testing.T) {
	repository := NewMemory()
	created, _, err := repository.CreateOperation(operationFixture())
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	otherOperationID := model.NewResourceID()
	if _, err := repository.FinalizeOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageReport, Status: model.OperationSucceeded, Message: "verified",
	}, []model.AuditEvent{
		{OperationID: otherOperationID, Stage: model.StageReport, Message: "other"},
	}, nil); err == nil {
		t.Fatal("cross-operation audit was accepted")
	}
	if _, err := repository.FinalizeOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageReport, Status: model.OperationSucceeded, Message: "verified",
	}, []model.AuditEvent{{OperationID: created.ResourceID, Stage: model.StageReport, Message: "report generated"}},
		[]model.Report{{OperationID: created.ResourceID, Title: "report", Status: model.OperationSucceeded, Summary: "verified"}}); err != nil {
		t.Fatalf("finalize operation: %v", err)
	}
	timeline, found := repository.OperationTimeline(created.ResourceID)
	if !found || timeline.Operation.Status != model.OperationSucceeded || len(timeline.Audits) != 1 || len(timeline.Reports) != 1 {
		t.Fatalf("timeline=%+v found=%t", timeline, found)
	}
}
