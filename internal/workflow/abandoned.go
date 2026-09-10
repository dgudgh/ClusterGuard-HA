package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type abandonedOperationLister interface {
	Operations(model.ResourceID) []model.OperationRecord
}

// ReconcileAbandonedOperations closes durable running records whose executor
// disappeared. Stages before adapter execution are safe to fail. Once execute
// has begun, the result is deliberately indeterminate and requires review.
func (service *Service) ReconcileAbandonedOperations(
	ctx context.Context,
	staleAfter time.Duration,
) ([]model.OperationRecord, error) {
	if service == nil || service.operations == nil {
		return nil, fmt.Errorf("operation store is not configured")
	}
	if staleAfter <= 0 {
		return nil, fmt.Errorf("abandoned operation threshold must be positive")
	}
	lister, ok := service.operations.(abandonedOperationLister)
	if !ok {
		return nil, fmt.Errorf("operation store does not support abandoned operation recovery")
	}
	baseFinalizer, ok := service.atomicFinalizer()
	if !ok {
		return nil, fmt.Errorf("atomic operation finalization is required for abandoned operation recovery")
	}
	finalizer, ok := baseFinalizer.(AbandonedOperationFinalizer)
	if !ok {
		return nil, fmt.Errorf("atomic abandoned operation finalization is required")
	}

	now := service.now().UTC()
	staleBefore := now.Add(-staleAfter)
	var reconciled []model.OperationRecord
	var failures []error
	for _, record := range lister.Operations("") {
		if err := ctx.Err(); err != nil {
			return reconciled, errors.Join(append(failures, err)...)
		}
		if record.Status != model.OperationRunning || record.UpdatedAt.IsZero() || record.UpdatedAt.After(now) || now.Sub(record.UpdatedAt) < staleAfter {
			continue
		}
		if _, _, _, _, recognized := abandonedOperationOutcome(record); !recognized {
			failures = append(failures, fmt.Errorf("operation %s has an unrecognized running stage %q", record.ResourceID, record.Stage))
			continue
		}
		if !service.claimOperation(record.ResourceID) {
			continue
		}

		status, failureClass, message, _, _ := abandonedOperationOutcome(record)
		execution := newDurableExecution(record.ResourceID, status, message, service.now)
		if !record.Execution.StartedAt.IsZero() {
			execution.StartedAt = record.Execution.StartedAt.UTC()
		} else {
			execution.StartedAt = record.UpdatedAt.UTC()
		}
		stage := record.Stage
		if status == model.OperationSucceeded {
			stage = model.StageReport
		}
		audits := []model.AuditEvent{
			service.auditEvent(record.Operation, stage, message),
			service.auditEvent(record.Operation, model.StageReport, "abandoned operation recovery report generated"),
		}
		updated, finalized, err := finalizer.FinalizeAbandonedOperation(record.ResourceID, record.MetadataRevision, now, staleBefore, model.OperationTransition{
			Stage: stage, Status: status, Execution: &execution,
			FailureClass: failureClass, Message: message,
		}, audits, []model.Report{service.terminalReport(record.Operation, execution)})
		service.releaseOperation(record.ResourceID)
		if err != nil {
			failures = append(failures, fmt.Errorf("reconcile abandoned operation %s: %w", record.ResourceID, err))
			continue
		}
		if !finalized {
			continue
		}
		reconciled = append(reconciled, updated)
	}
	return reconciled, errors.Join(failures...)
}

func abandonedOperationOutcome(record model.OperationRecord) (model.OperationStatus, string, string, bool, bool) {
	stageOrder := durableStageOrder(record.Stage)
	if stageOrder == 0 {
		return "", "", "", false, false
	}
	if stageOrder < durableStageOrder(model.StageExecute) {
		return model.OperationFailed, "abandoned_pre_commit",
			"operation executor stopped before adapter execution; no database mutation was started", false, true
	}
	if record.Verification.Passed {
		return model.OperationSucceeded, "", "persisted verification proves the abandoned operation completed successfully", true, true
	}
	if record.FailureClass == "promoted_unverified" {
		return model.OperationIndeterminate, record.FailureClass,
			"automatic failover promotion remains unverified after its executor stopped", true, true
	}
	return model.OperationIndeterminate, "abandoned_post_commit",
		"operation executor stopped after adapter execution began; verify database and endpoint state", true, true
}
