package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func durableStageOrder(stage model.WorkflowStage) int {
	for index, candidate := range []model.WorkflowStage{
		model.StageDiscover, model.StagePrecheck, model.StagePlan, model.StageSafetyGuard, model.StageLock,
		model.StageApprove, model.StageExecute, model.StageVerify, model.StageAudit, model.StageReport,
	} {
		if candidate == stage {
			return index + 1
		}
	}
	return 0
}

func durableTerminalStatus(status model.OperationStatus) bool {
	switch status {
	case model.OperationBlocked, model.OperationSucceeded, model.OperationFailed, model.OperationIndeterminate, model.OperationUnsupported:
		return true
	default:
		return false
	}
}

func (service *Service) claimOperation(resourceID model.ResourceID) bool {
	service.inflightMu.Lock()
	defer service.inflightMu.Unlock()
	if _, found := service.inflight[resourceID]; found {
		return false
	}
	service.inflight[resourceID] = struct{}{}
	return true
}

func (service *Service) releaseOperation(resourceID model.ResourceID) {
	service.inflightMu.Lock()
	defer service.inflightMu.Unlock()
	delete(service.inflight, resourceID)
}

func operationFailureClass(err error) string {
	var classified interface{ FailureClass() string }
	if errors.As(err, &classified) {
		return classified.FailureClass()
	}
	return ""
}

func detachedVerification(ctx context.Context, candidate adapter.DatabaseHAAdapter, request adapter.OperationRequest) (model.Verification, error) {
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return candidate.Verify(verifyCtx, request)
}

func verificationOutcomeMessage(verification model.Verification, err error) string {
	if err != nil {
		return "post-commit verification failed: " + err.Error()
	}
	if !verification.Passed {
		return "post-commit verification found failed checks"
	}
	return "post-commit verification passed"
}

func terminalRecordResult(record model.OperationRecord) (model.Execution, error) {
	execution := record.Execution
	if execution.OperationID == "" {
		execution.OperationID = record.ResourceID
		execution.Status = record.Status
		execution.Message = record.Message
	}
	switch record.Status {
	case model.OperationSucceeded:
		return execution, nil
	case model.OperationUnsupported:
		return execution, adapter.ErrUnsupported
	default:
		message := record.Message
		if message == "" {
			message = "operation ended with status " + string(record.Status)
		}
		return execution, errors.New(message)
	}
}

func (service *Service) durableRecord(resourceID model.ResourceID) (model.OperationRecord, error) {
	record, found := service.operations.Operation(resourceID)
	if !found {
		return model.OperationRecord{}, fmt.Errorf("durable operation record disappeared")
	}
	return record, nil
}

func (service *Service) advanceDurable(resourceID model.ResourceID, stage model.WorkflowStage, transition model.OperationTransition) (model.OperationRecord, error) {
	record, err := service.durableRecord(resourceID)
	if err != nil {
		return model.OperationRecord{}, err
	}
	if durableStageOrder(record.Stage) > durableStageOrder(stage) {
		return record, nil
	}
	transition.Stage = stage
	if transition.Status == "" {
		transition.Status = model.OperationRunning
	}
	updated, transitionErr := service.operations.TransitionOperation(record.ResourceID, record.MetadataRevision, transition)
	if transitionErr != nil && isCommittedWarning(transitionErr) {
		return service.durableRecord(resourceID)
	}
	return updated, transitionErr
}

func (service *Service) putDurablePlan(record model.OperationRecord, plan model.OperationPlan) (model.OperationRecord, error) {
	if record.Plan.Digest != "" {
		if record.Plan.Digest != plan.Digest {
			return model.OperationRecord{}, fmt.Errorf("persisted operation plan differs from current topology")
		}
		return record, nil
	}
	updated, err := service.operations.PutOperationPlan(record.ResourceID, record.MetadataRevision, plan)
	if err != nil && isCommittedWarning(err) {
		return service.durableRecord(record.ResourceID)
	}
	return updated, err
}

func (service *Service) atomicFinalizer() (OperationFinalizer, bool) {
	operationFinalizer, operationSupports := service.operations.(OperationFinalizer)
	journalFinalizer, journalSupports := service.journal.(OperationFinalizer)
	if !operationSupports || !journalSupports {
		return nil, false
	}
	operationValue := reflect.ValueOf(operationFinalizer)
	journalValue := reflect.ValueOf(journalFinalizer)
	if !operationValue.IsValid() || !journalValue.IsValid() || operationValue.Type() != journalValue.Type() || !operationValue.Comparable() {
		return nil, false
	}
	if operationValue.Interface() != journalValue.Interface() {
		return nil, false
	}
	return operationFinalizer, true
}

func (service *Service) finishDurable(recordID model.ResourceID, operation model.Operation, stage model.WorkflowStage, execution model.Execution, failureClass string, cause error, operationCommitted bool) (model.Execution, error) {
	return service.finishDurableWithAudits(recordID, operation, stage, execution, failureClass, cause, operationCommitted, nil)
}

func (service *Service) finishDurableWithAudits(recordID model.ResourceID, operation model.Operation, stage model.WorkflowStage, execution model.Execution, failureClass string, cause error, operationCommitted bool, finalAudits []model.AuditEvent) (model.Execution, error) {
	record, err := service.durableRecord(recordID)
	if err != nil {
		return execution, err
	}
	if durableStageOrder(record.Stage) > durableStageOrder(stage) {
		stage = record.Stage
	}
	if execution.OperationID == "" {
		execution.OperationID = recordID
	}
	if execution.Status == "" {
		execution.Status = model.OperationFailed
	}
	if execution.Message == "" && cause != nil {
		execution.Message = cause.Error()
	}
	if finalizer, ok := service.atomicFinalizer(); ok {
		audits := append([]model.AuditEvent{}, finalAudits...)
		audits = append(audits, service.auditEvent(operation, stage, execution.Message))
		if stage != model.StageReport {
			audits = append(audits, service.auditEvent(operation, model.StageReport, "terminal operation report generated"))
		}
		_, finalizeErr := finalizer.FinalizeOperation(recordID, record.MetadataRevision, model.OperationTransition{
			Stage: stage, Status: execution.Status, Execution: &execution,
			FailureClass: failureClass, Message: execution.Message,
		}, audits, []model.Report{service.terminalReport(operation, execution)})
		if finalizeErr == nil {
			return execution, cause
		}
		if operationCommitted {
			execution = markDurabilityIndeterminate(execution)
			return execution, &journalPersistenceError{err: fmt.Errorf("atomically persist terminal operation timeline: %w", finalizeErr)}
		}
		return journalFailure(execution, fmt.Errorf("atomically persist terminal operation timeline: %w", finalizeErr))
	}
	if len(finalAudits) != 0 {
		atomicErr := errors.New("atomic operation finalizer is not configured")
		if operationCommitted {
			execution.Status = model.OperationIndeterminate
			execution.Message = "operation committed and verified, but atomic terminal persistence is unavailable"
			return service.finishDurable(recordID, operation, model.StageVerify, execution, "journal", &journalPersistenceError{err: atomicErr}, true)
		}
		return journalFailure(execution, atomicErr)
	}
	var journalErr error
	if err := service.audit(operation, stage, execution.Message); err != nil {
		journalErr = firstJournalError(journalErr, err)
	}
	if stage != model.StageReport {
		if err := service.audit(operation, model.StageReport, "terminal operation report generated"); err != nil {
			journalErr = firstJournalError(journalErr, err)
		}
	}
	reportedExecution := execution
	if journalErr != nil {
		if operationCommitted {
			reportedExecution = markIndeterminate(reportedExecution)
		} else {
			reportedExecution.Status = model.OperationFailed
			reportedExecution.Message = "workflow journal persistence failed"
		}
	}
	if err := service.report(operation, reportedExecution, operationCommitted); err != nil {
		journalErr = firstJournalError(journalErr, err)
	}
	if journalErr != nil {
		if operationCommitted {
			execution = markIndeterminate(execution)
		} else {
			execution.Status = model.OperationFailed
			execution.Message = "workflow journal persistence failed"
		}
		cause = &journalPersistenceError{err: journalErr}
		if failureClass == "" {
			failureClass = "journal"
		}
	}
	record, err = service.durableRecord(recordID)
	if err != nil {
		return execution, err
	}
	if _, err := service.operations.TransitionOperation(recordID, record.MetadataRevision, model.OperationTransition{
		Stage: stage, Status: execution.Status, Execution: &execution,
		FailureClass: failureClass, Message: execution.Message,
	}); err != nil && !isCommittedWarning(err) {
		if operationCommitted {
			execution = markDurabilityIndeterminate(execution)
			return execution, &journalPersistenceError{err: fmt.Errorf("persist terminal operation state: %w", err)}
		}
		return journalFailure(execution, err)
	}
	return execution, cause
}

func (service *Service) executeDurable(ctx context.Context, request adapter.OperationRequest, authorization executionAuthorization) (model.Execution, error) {
	if service.registry == nil {
		return model.Execution{}, fmt.Errorf("adapter registry is not configured")
	}
	record, reused, err := service.operations.CreateOperation(model.OperationRecord{
		Operation: request.Operation, TargetID: request.TargetID, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil && !isCommittedWarning(err) {
		return model.Execution{}, err
	}
	if err != nil {
		persisted, found := service.operations.Operation(record.ResourceID)
		if !found {
			return model.Execution{}, err
		}
		record = persisted
	}
	request.Operation = record.Operation
	request.Operation.ResourceID = record.ResourceID
	request.TargetID = record.TargetID
	if reused && durableTerminalStatus(record.Status) {
		return terminalRecordResult(record)
	}
	operation := request.Operation
	if !service.claimOperation(record.ResourceID) {
		execution := record.Execution
		execution.OperationID = record.ResourceID
		execution.Status = model.OperationRunning
		execution.Message = ErrOperationInProgress.Error()
		_ = service.audit(operation, record.Stage, "duplicate idempotent request joined an active operation")
		return execution, ErrOperationInProgress
	}
	defer service.releaseOperation(record.ResourceID)

	candidate, registered := service.registry.Get(operation.Engine)
	if !registered {
		execution := newDurableExecution(operation.ResourceID, model.OperationUnsupported, "no adapter is registered for the requested engine", service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePrecheck, execution, "unsupported", adapter.ErrUnsupported, false)
	}
	capabilities := candidate.Capabilities(ctx)
	for _, required := range []adapter.Capability{adapter.CapabilityPrecheck, adapter.CapabilityPlan, adapter.CapabilityExecute, adapter.CapabilityVerify} {
		if !capabilities.Supports(required) {
			message := fmt.Sprintf("operation %s is unsupported by this adapter", required)
			execution := newDurableExecution(operation.ResourceID, model.OperationUnsupported, message, service.now)
			return service.finishDurable(record.ResourceID, operation, model.StagePrecheck, execution, "unsupported", adapter.ErrUnsupported, false)
		}
	}
	if service.discovery == nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, "discovery validation is not configured", service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageDiscover, execution, "pre_commit", errors.New(execution.Message), false)
	}
	if service.safety == nil || service.locks == nil || (!authorization.automatic && service.approval == nil) {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
	}
	observation, err := service.discovery.CaptureObservation(ctx, operation)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageDiscover, execution, "pre_commit", err, false)
	}
	observationLabel := observationLabel(observation)
	if record.Observation != "" && record.Observation != observationLabel {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, "topology observation changed since the operation was created", service.now)
		return service.finishDurable(record.ResourceID, operation, record.Stage, execution, "stale_plan", errors.New(execution.Message), false)
	}
	record, err = service.advanceDurable(record.ResourceID, model.StageDiscover, model.OperationTransition{Observation: observationLabel, Message: "topology observation validated"})
	if err != nil {
		return model.Execution{}, err
	}
	if err := service.audit(operation, model.StageDiscover, "topology observation "+observationLabel+" validated"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}

	request, err = resolveCapturedOperation(ctx, service.resolver, request, observation)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageDiscover, execution, "pre_commit", err, false)
	}
	request.Resolved.ObservationToken = observationLabel
	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationFailed, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePrecheck, execution, "pre_commit", err, false)
	}
	record, err = service.advanceDurable(record.ResourceID, model.StagePrecheck, model.OperationTransition{
		Precheck: append([]model.Check{}, checks...), Message: "adapter precheck completed",
	})
	if err != nil {
		return model.Execution{}, err
	}
	if err := service.audit(operation, model.StagePrecheck, "adapter precheck completed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if hasBlockingCheck(checks) {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, "precheck contains blocking checks", service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePrecheck, execution, "pre_commit", errors.New(execution.Message), false)
	}
	plan, err := candidate.BuildPlan(ctx, request)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationFailed, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePlan, execution, "pre_commit", err, false)
	}
	record, err = service.putDurablePlan(record, plan)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePlan, execution, "stale_plan", err, false)
	}
	if err := service.audit(operation, model.StagePlan, "immutable adapter operation plan persisted"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if err := service.safety.Evaluate(ctx, operation); err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageSafetyGuard, execution, "pre_commit", err, false)
	}
	record, err = service.advanceDurable(record.ResourceID, model.StageSafetyGuard, model.OperationTransition{Message: "safety guard passed"})
	if err != nil {
		return model.Execution{}, err
	}
	if err := service.audit(operation, model.StageSafetyGuard, "safety guard passed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	release, err := service.locks.Acquire(ctx, operation)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageLock, execution, "pre_commit", err, false)
	}
	defer release()
	record, err = service.advanceDurable(record.ResourceID, model.StageLock, model.OperationTransition{Message: "operation lock acquired"})
	if err != nil {
		return model.Execution{}, err
	}
	if err := service.audit(operation, model.StageLock, "operation lock acquired"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if err := service.discovery.RevalidateObservation(ctx, operation, observation); err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageLock, execution, "stale_plan", err, false)
	}
	request, err = resolveCapturedOperation(ctx, service.resolver, request, observation)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageLock, execution, "stale_plan", err, false)
	}
	request.Resolved.ObservationToken = observationLabel
	approvalMessage := ""
	if authorization.automatic {
		approvalMessage = "automatic recovery authorized for incident " + authorization.incidentID
		record, err = service.advanceDurable(record.ResourceID, model.StageApprove, model.OperationTransition{Message: approvalMessage})
		if err != nil {
			return model.Execution{}, err
		}
	} else {
		grantID, approved, approvalErr := service.approval.Consume(ctx, record, authorization.approvalToken)
		if approvalErr != nil {
			execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, approvalErr.Error(), service.now)
			return service.finishDurable(record.ResourceID, operation, model.StageApprove, execution, "pre_commit", approvalErr, false)
		}
		record = approved
		approvalMessage = "one-time approval grant " + string(grantID) + " consumed"
	}
	if err := service.audit(operation, model.StageApprove, approvalMessage); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	request.Plan = &record.Plan
	request.Progress = repositoryProgress{operations: service.operations, operationID: record.ResourceID, now: service.now}
	record, err = service.advanceDurable(record.ResourceID, model.StageExecute, model.OperationTransition{Message: "adapter execution started"})
	if err != nil {
		return model.Execution{}, err
	}
	execution, executeErr := candidate.Execute(ctx, request)
	execution.OperationID = operation.ResourceID
	if executeErr != nil {
		if execution.Status == "" {
			execution.Status = model.OperationFailed
		}
		failureClass := operationFailureClass(executeErr)
		committed := failureClass != "" && failureClass != "pre_commit"
		if !committed {
			return service.finishDurable(record.ResourceID, operation, model.StageExecute, execution, failureClass, executeErr, false)
		}

		verification, verifyErr := detachedVerification(ctx, candidate, request)
		verificationMessage := verificationOutcomeMessage(verification, verifyErr)
		if _, err := service.advanceDurable(record.ResourceID, model.StageVerify, model.OperationTransition{
			Verification: &verification,
			FailureClass: failureClass,
			Message:      verificationMessage,
		}); err != nil {
			execution = markDurabilityIndeterminate(execution)
			cause := fmt.Errorf("persist post-commit verification evidence: %w (execution error: %v)", err, executeErr)
			return service.finishDurable(record.ResourceID, operation, model.StageVerify, execution, failureClass, cause, true)
		}
		execution.Status = model.OperationIndeterminate
		execution.Message = executeErr.Error() + "; " + verificationMessage
		cause := executeErr
		if verifyErr != nil {
			cause = fmt.Errorf("%w; verification error: %v", executeErr, verifyErr)
		} else if !verification.Passed {
			cause = fmt.Errorf("%w; post-commit verification failed", executeErr)
		}
		return service.finishDurable(record.ResourceID, operation, model.StageVerify, execution, failureClass, cause, true)
	}
	var committedJournalErr error
	if _, err := service.advanceDurable(record.ResourceID, model.StageExecute, model.OperationTransition{Execution: &execution, Message: "adapter execution completed"}); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	} else if err := service.audit(operation, model.StageExecute, "adapter execution completed"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	verification, verifyErr := detachedVerification(ctx, candidate, request)
	verificationMessage := verificationOutcomeMessage(verification, verifyErr)
	if _, err := service.advanceDurable(record.ResourceID, model.StageVerify, model.OperationTransition{Verification: &verification, Message: verificationMessage}); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if verifyErr != nil || !verification.Passed {
		execution.Message = verificationMessage
		execution.Status = model.OperationIndeterminate
		if verifyErr == nil {
			verifyErr = errors.New(verificationMessage)
		}
		if committedJournalErr != nil {
			verifyErr = &journalPersistenceError{err: fmt.Errorf("%v; %w", verifyErr, committedJournalErr)}
		}
		return service.finishDurable(record.ResourceID, operation, model.StageVerify, execution, "verification", verifyErr, true)
	}
	if committedJournalErr != nil {
		execution.Status = model.OperationIndeterminate
		execution.Message = "operation committed and verified, but workflow journal persistence failed"
		return service.finishDurable(record.ResourceID, operation, model.StageVerify, execution, "journal", &journalPersistenceError{err: committedJournalErr}, true)
	}
	execution.Status = model.OperationSucceeded
	execution.Message = "operation completed and verified"
	return service.finishDurableWithAudits(record.ResourceID, operation, model.StageReport, execution, "", nil, true, []model.AuditEvent{
		service.auditEvent(operation, model.StageVerify, "verification passed"),
		service.auditEvent(operation, model.StageAudit, "operation audit recorded"),
	})
}

func newDurableExecution(operationID model.ResourceID, status model.OperationStatus, message string, now func() time.Time) model.Execution {
	timestamp := now().UTC()
	return model.Execution{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: timestamp, UpdatedAt: timestamp},
		OperationID:  operationID, Status: status, StartedAt: timestamp, FinishedAt: timestamp, Message: message,
	}
}
