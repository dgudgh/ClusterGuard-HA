package workflow

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	operationProgressCommitAttempts = 4
	operationProgressRetryDelay     = 15 * time.Millisecond
)

type OperationStore interface {
	CreateOperation(model.OperationRecord) (model.OperationRecord, bool, error)
	PutOperationPlan(model.ResourceID, uint64, model.OperationPlan) (model.OperationRecord, error)
	TransitionOperation(model.ResourceID, uint64, model.OperationTransition) (model.OperationRecord, error)
	Operation(model.ResourceID) (model.OperationRecord, bool)
	OperationByIdempotencyKey(string) (model.OperationRecord, bool)
}

type OperationFinalizer interface {
	FinalizeOperation(model.ResourceID, uint64, model.OperationTransition, []model.AuditEvent, []model.Report) (model.OperationRecord, error)
}

type AbandonedOperationFinalizer interface {
	FinalizeAbandonedOperation(model.ResourceID, uint64, time.Time, time.Time, model.OperationTransition, []model.AuditEvent, []model.Report) (model.OperationRecord, bool, error)
}

type repositoryProgress struct {
	operations  OperationStore
	operationID model.ResourceID
	now         func() time.Time
}

func (progress repositoryProgress) StepCompleted(ctx context.Context, step string) (bool, error) {
	_, completed, err := progress.StepResult(ctx, step)
	return completed, err
}

func (progress repositoryProgress) StepResult(ctx context.Context, step string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	record, found := progress.operations.Operation(progress.operationID)
	if !found {
		return "", false, fmt.Errorf("operation progress record does not exist")
	}
	for _, attempt := range record.Attempts {
		if attempt.Step == step && attempt.Status == model.OperationSucceeded {
			return attempt.Message, true, nil
		}
	}
	return "", false, nil
}

func (progress repositoryProgress) CompleteStep(ctx context.Context, step string, message string) error {
	step = strings.TrimSpace(step)
	message = strings.TrimSpace(message)
	if step == "" {
		return progress.commitFailure(step, "validate", fmt.Errorf("operation step is required"))
	}
	var lastErr error
	for commitAttempt := 0; commitAttempt < operationProgressCommitAttempts; commitAttempt++ {
		if err := ctx.Err(); err != nil {
			return progress.commitFailure(step, "context", err)
		}
		record, found := progress.operations.Operation(progress.operationID)
		if !found {
			return progress.commitFailure(step, "read", fmt.Errorf("operation progress record does not exist"))
		}
		maximum, completed := completedStepAttempt(record.Attempts, step)
		if completed {
			return nil
		}
		if durableTerminalStatus(record.Status) {
			if lastErr != nil {
				return progress.commitFailure(step, "terminal_after_retry", lastErr)
			}
			return progress.commitFailure(step, "terminal_before_commit", fmt.Errorf("terminal operation cannot accept step progress: status=%s stage=%s revision=%d", record.Status, record.Stage, record.MetadataRevision))
		}
		now := progress.now().UTC()
		_, err := progress.operations.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
			Stage:  model.StageExecute,
			Status: model.OperationRunning,
			Attempt: &model.StepAttempt{
				Step: step, Attempt: maximum + 1, Status: model.OperationSucceeded,
				StartedAt: now, FinishedAt: now, Message: message,
			},
			Message: message,
		})
		if err == nil {
			return nil
		}
		lastErr = err
		log.Printf("durable operation step commit retry: operation_id=%s step=%q commit_attempt=%d expected_revision=%d status=%s stage=%s error=%v", progress.operationID, step, commitAttempt+1, record.MetadataRevision, record.Status, record.Stage, err)

		// A Raft commit can return a warning or a metadata CAS conflict after
		// the authoritative state has already advanced. Re-read before retrying
		// so an already committed step is never replayed.
		if current, exists := progress.operations.Operation(progress.operationID); exists {
			if _, completed = completedStepAttempt(current.Attempts, step); completed {
				return nil
			}
			if durableTerminalStatus(current.Status) {
				return progress.commitFailure(step, "terminal_after_commit_error", fmt.Errorf("%w: current_status=%s current_stage=%s current_revision=%d", lastErr, current.Status, current.Stage, current.MetadataRevision))
			}
		}
		if commitAttempt+1 < operationProgressCommitAttempts {
			timer := time.NewTimer(time.Duration(commitAttempt+1) * operationProgressRetryDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return progress.commitFailure(step, "retry_wait", ctx.Err())
			case <-timer.C:
			}
		}
	}
	return progress.commitFailure(step, "retries_exhausted", lastErr)
}

func (progress repositoryProgress) commitFailure(step, phase string, err error) error {
	log.Printf("durable operation step commit failed: operation_id=%s step=%q phase=%s error=%v", progress.operationID, step, phase, err)
	return err
}

func completedStepAttempt(attempts []model.StepAttempt, step string) (uint64, bool) {
	var maximum uint64
	for _, attempt := range attempts {
		if attempt.Step != step {
			continue
		}
		if attempt.Status == model.OperationSucceeded {
			return attempt.Attempt, true
		}
		if attempt.Attempt > maximum {
			maximum = attempt.Attempt
		}
	}
	return maximum, false
}
