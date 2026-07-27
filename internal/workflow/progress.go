package workflow

import (
	"context"
	"fmt"
	"time"

	"clusterguard.io/ha/pkg/model"
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
	if err := ctx.Err(); err != nil {
		return err
	}
	record, found := progress.operations.Operation(progress.operationID)
	if !found {
		return fmt.Errorf("operation progress record does not exist")
	}
	var maximum uint64
	for _, attempt := range record.Attempts {
		if attempt.Step != step {
			continue
		}
		if attempt.Status == model.OperationSucceeded {
			return nil
		}
		if attempt.Attempt > maximum {
			maximum = attempt.Attempt
		}
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
	return err
}
