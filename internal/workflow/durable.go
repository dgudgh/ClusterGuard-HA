package workflow

import (
	"context"
	"errors"
	"fmt"
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

func (service *Service) finishDurable(recordID model.ResourceID, operation model.Operation, stage model.WorkflowStage, execution model.Execution, failureClass string, cause error, operationCommitted bool) (model.Execution, error) {
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
	if _, err := service.operations.TransitionOperation(recordID, record.MetadataRevision, model.OperationTransition{
		Stage: stage, Status: execution.Status, Execution: &execution,
		FailureClass: failureClass, Message: execution.Message,
	}); err != nil && !isCommittedWarning(err) {
		return execution, err
	}
	if err := service.audit(operation, stage, execution.Message); err != nil {
		return journalFailure(execution, err)
	}
	if stage != model.StageReport {
		if err := service.audit(operation, model.StageReport, "terminal operation report generated"); err != nil {
			return journalFailure(execution, err)
		}
	}
	if err := service.report(operation, execution, operationCommitted); err != nil {
		if operationCommitted {
			return journalIndeterminate(execution, err)
		}
		return journalFailure(execution, err)
	}
	return execution, cause
}

func (service *Service) executeDurable(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error) {
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
	observation, err := service.discovery.CaptureObservation(ctx, operation)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageDiscover, execution, "pre_commit", err, false)
	}
	observationLabel := fmt.Sprintf("%s@%s", observation.ClusterID, observation.ObservedAt.UTC().Format(time.RFC3339Nano))
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

	request, err = service.resolver.Resolve(ctx, request)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageDiscover, execution, "pre_commit", err, false)
	}
	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationFailed, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StagePrecheck, execution, "pre_commit", err, false)
	}
	record, err = service.advanceDurable(record.ResourceID, model.StagePrecheck, model.OperationTransition{Message: "adapter precheck completed"})
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
	if service.safety == nil || service.locks == nil || service.approval == nil {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
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
	request, err = service.resolver.Resolve(ctx, request)
	if err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageLock, execution, "stale_plan", err, false)
	}
	if err := service.approval.Validate(ctx, operation, approvalToken); err != nil {
		execution := newDurableExecution(operation.ResourceID, model.OperationBlocked, err.Error(), service.now)
		return service.finishDurable(record.ResourceID, operation, model.StageApprove, execution, "pre_commit", err, false)
	}
	record, err = service.advanceDurable(record.ResourceID, model.StageApprove, model.OperationTransition{Message: "approval validated"})
	if err != nil {
		return model.Execution{}, err
	}
	if err := service.audit(operation, model.StageApprove, "approval validated"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	request.Plan = &record.Plan
	request.Progress = repositoryProgress{operations: service.operations, operationID: record.ResourceID, now: service.now}
	execution, executeErr := candidate.Execute(ctx, request)
	execution.OperationID = operation.ResourceID
	if executeErr != nil {
		if execution.Status == "" {
			execution.Status = model.OperationFailed
		}
		committed := operationFailureClass(executeErr) != "" && operationFailureClass(executeErr) != "pre_commit"
		return service.finishDurable(record.ResourceID, operation, model.StageExecute, execution, operationFailureClass(executeErr), executeErr, committed)
	}
	if _, err := service.advanceDurable(record.ResourceID, model.StageExecute, model.OperationTransition{Execution: &execution, Message: "adapter execution completed"}); err != nil {
		return markDurabilityIndeterminate(execution), err
	}
	if err := service.audit(operation, model.StageExecute, "adapter execution completed"); err != nil {
		return journalIndeterminate(execution, err)
	}
	verification, verifyErr := candidate.Verify(ctx, request)
	if verifyErr != nil || !verification.Passed {
		if verifyErr != nil {
			execution.Message = verifyErr.Error()
		} else {
			execution.Message = "verification failed"
			verifyErr = errors.New(execution.Message)
		}
		execution.Status = model.OperationFailed
		return service.finishDurable(record.ResourceID, operation, model.StageVerify, execution, "verification", verifyErr, true)
	}
	if _, err := service.advanceDurable(record.ResourceID, model.StageVerify, model.OperationTransition{Verification: &verification, Message: "verification passed"}); err != nil {
		return markDurabilityIndeterminate(execution), err
	}
	if err := service.audit(operation, model.StageVerify, "verification passed"); err != nil {
		return journalIndeterminate(execution, err)
	}
	if err := service.audit(operation, model.StageAudit, "operation audit recorded"); err != nil {
		return journalIndeterminate(execution, err)
	}
	execution.Status = model.OperationSucceeded
	execution.Message = "operation completed and verified"
	return service.finishDurable(record.ResourceID, operation, model.StageReport, execution, "", nil, true)
}

func newDurableExecution(operationID model.ResourceID, status model.OperationStatus, message string, now func() time.Time) model.Execution {
	timestamp := now().UTC()
	return model.Execution{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: timestamp, UpdatedAt: timestamp},
		OperationID:  operationID, Status: status, StartedAt: timestamp, FinishedAt: timestamp, Message: message,
	}
}
