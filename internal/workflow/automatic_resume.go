package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// automaticResumeProgress preserves the immutable terminal operation while an
// idempotent continuation runs. Only the final verified outcome is published
// back to the original record.
type automaticResumeProgress struct {
	mu      sync.RWMutex
	results map[string]string
}

func newAutomaticResumeProgress(attempts []model.StepAttempt) *automaticResumeProgress {
	progress := &automaticResumeProgress{results: make(map[string]string)}
	for _, attempt := range attempts {
		if attempt.Status == model.OperationSucceeded {
			progress.results[attempt.Step] = attempt.Message
		}
	}
	return progress
}

func (progress *automaticResumeProgress) CompleteStep(ctx context.Context, step string, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	step = strings.TrimSpace(step)
	if step == "" {
		return fmt.Errorf("operation step is required")
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if _, completed := progress.results[step]; !completed {
		progress.results[step] = strings.TrimSpace(message)
	}
	return nil
}

func (progress *automaticResumeProgress) StepCompleted(ctx context.Context, step string) (bool, error) {
	_, completed, err := progress.StepResult(ctx, step)
	return completed, err
}

func (progress *automaticResumeProgress) StepResult(ctx context.Context, step string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	progress.mu.RLock()
	defer progress.mu.RUnlock()
	result, completed := progress.results[strings.TrimSpace(step)]
	return result, completed, nil
}

func automaticResumeEligible(record model.OperationRecord, request adapter.OperationRequest) bool {
	return record.Status == model.OperationIndeterminate &&
		record.FailureClass == "promoted_unverified" &&
		record.Operation.Kind == model.OperationFailover &&
		record.Operation.RequestedBy == AutomaticRecoveryActor &&
		record.Operation.ClusterID == request.Operation.ClusterID &&
		record.Operation.Engine == request.Operation.Engine &&
		record.TargetID == request.TargetID &&
		record.IdempotencyKey == request.IdempotencyKey &&
		model.ValidResourceID(record.ResourceID) &&
		model.ValidResourceID(record.Plan.SourceID) &&
		model.ValidResourceID(record.Plan.TargetID) &&
		record.Plan.OperationID == record.ResourceID &&
		record.Plan.ClusterID == record.Operation.ClusterID &&
		record.Plan.TargetID == record.TargetID &&
		strings.TrimSpace(record.Plan.Digest) != ""
}

func automaticResumeSourceFailed(resolved *adapter.ResolvedOperation) bool {
	if resolved == nil || !model.ValidResourceID(resolved.Primary.ResourceID) {
		return false
	}
	switch resolved.Primary.Health.State {
	case model.HealthUnhealthy, model.HealthUnknown:
	default:
		return false
	}
	for _, probe := range resolved.Snapshot.Probes {
		if probe.InstanceID == resolved.Primary.ResourceID && probe.Health.State == model.HealthHealthy {
			return false
		}
	}
	return true
}

func (service *Service) finalizeAutomaticResume(record model.OperationRecord, execution model.Execution, verification model.Verification, success bool, cause error) (model.Execution, error) {
	finalizer, ok := service.atomicFinalizer()
	if !ok {
		return markDurabilityIndeterminate(execution), &journalPersistenceError{err: errors.New("atomic operation finalizer is required for automatic continuation")}
	}
	status := model.OperationIndeterminate
	stage := record.Stage
	failureClass := "promoted_unverified"
	message := verificationOutcomeMessage(verification, cause)
	if success {
		status = model.OperationSucceeded
		stage = model.StageReport
		failureClass = ""
		message = "automatic failover continuation completed and verified"
	}
	execution.OperationID = record.ResourceID
	execution.Status = status
	execution.Message = message
	if execution.ResourceID == "" {
		execution = newDurableExecution(record.ResourceID, status, message, service.now)
	}
	verification.OperationID = record.ResourceID
	operation := record.Operation
	audits := []model.AuditEvent{
		service.auditEvent(operation, model.StageExecute, "automatic failover continuation executed with the immutable operation plan"),
		service.auditEvent(operation, model.StageVerify, message),
		service.auditEvent(operation, model.StageAudit, "automatic failover continuation audit recorded"),
		service.auditEvent(operation, model.StageReport, "automatic failover continuation report generated"),
	}
	updated, err := finalizer.FinalizeOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: stage, Status: status, Execution: &execution, Verification: &verification,
		FailureClass: failureClass, Message: message,
	}, audits, []model.Report{service.terminalReport(operation, execution)})
	if err != nil {
		execution = markDurabilityIndeterminate(execution)
		return execution, &journalPersistenceError{err: fmt.Errorf("atomically persist automatic continuation: %w", err)}
	}
	return terminalRecordResult(updated)
}

func (service *Service) resumeIndeterminateAutomatic(ctx context.Context, record model.OperationRecord, request adapter.OperationRequest, incidentID string) (model.Execution, error) {
	if service.registry == nil || service.discovery == nil || service.safety == nil || service.locks == nil || service.resolver == nil {
		return model.Execution{}, fmt.Errorf("automatic continuation gates are not configured")
	}
	if _, ok := service.atomicFinalizer(); !ok {
		return model.Execution{}, fmt.Errorf("automatic continuation requires an atomic operation finalizer")
	}
	if !service.claimOperation(record.ResourceID) {
		return model.Execution{OperationID: record.ResourceID, Status: model.OperationRunning, Message: ErrOperationInProgress.Error()}, ErrOperationInProgress
	}
	defer service.releaseOperation(record.ResourceID)

	candidate, registered := service.resolveAdapter(record.Operation)
	if !registered || !candidate.Capabilities(ctx).Supports(adapter.CapabilityExecute) || !candidate.Capabilities(ctx).Supports(adapter.CapabilityVerify) {
		return model.Execution{}, adapter.ErrUnsupported
	}
	request.Operation = record.Operation
	request.Operation.ResourceID = record.ResourceID
	request.TargetID = record.TargetID
	request.Plan = &record.Plan

	observation, err := service.discovery.CaptureObservation(ctx, record.Operation)
	if err != nil {
		return model.Execution{}, fmt.Errorf("capture continuation topology: %w", err)
	}
	request, err = resolveCapturedOperation(ctx, service.resolver, request, observation)
	if err != nil {
		return model.Execution{}, fmt.Errorf("resolve continuation resources: %w", err)
	}
	if !automaticResumeSourceFailed(request.Resolved) {
		return model.Execution{}, fmt.Errorf("automatic continuation blocked because the original primary is not currently proven failed")
	}
	request.Resolved.ObservationToken = record.Plan.ObservationToken
	request.Resolved.PlanDigest = record.Plan.Digest
	if err := service.safety.Evaluate(ctx, record.Operation); err != nil {
		return model.Execution{}, fmt.Errorf("automatic continuation safety guard: %w", err)
	}
	if err := service.audit(record.Operation, model.StageSafetyGuard, "automatic failover continuation safety guard passed"); err != nil {
		return model.Execution{}, err
	}

	leaseCtx, release, err := service.locks.Acquire(ctx, record.Operation)
	if err != nil {
		return model.Execution{}, fmt.Errorf("automatic continuation operation lock: %w", err)
	}
	defer release()
	if err := service.audit(record.Operation, model.StageLock, "automatic failover continuation operation lock acquired"); err != nil {
		return model.Execution{}, err
	}
	if err := service.discovery.RevalidateObservation(leaseCtx, record.Operation, observation); err != nil {
		return model.Execution{}, fmt.Errorf("automatic continuation topology changed: %w", err)
	}
	request, err = resolveCapturedOperation(leaseCtx, service.resolver, request, observation)
	if err != nil {
		return model.Execution{}, fmt.Errorf("re-resolve continuation resources: %w", err)
	}
	if !automaticResumeSourceFailed(request.Resolved) {
		return model.Execution{}, fmt.Errorf("automatic continuation blocked because the original primary failure is no longer current")
	}
	request.Resolved.ObservationToken = record.Plan.ObservationToken
	request.Resolved.PlanDigest = record.Plan.Digest
	request.Progress = newAutomaticResumeProgress(record.Attempts)
	if err := service.audit(record.Operation, model.StageApprove, "automatic recovery continuation authorized for incident "+incidentID); err != nil {
		return model.Execution{}, err
	}
	if leaseErr := lockLeaseFailure(leaseCtx); leaseErr != nil {
		return model.Execution{}, leaseErr
	}

	execution, executeErr := candidate.Execute(leaseCtx, request)
	execution.OperationID = record.ResourceID
	if leaseErr := lockLeaseFailure(leaseCtx); leaseErr != nil {
		executeErr = errors.Join(executeErr, leaseErr)
	}
	executeErr = normalizeAdapterExecutionError(execution, executeErr)
	if executeErr != nil && operationFailureClass(executeErr) == "pre_commit" {
		return execution, executeErr
	}
	verification, verifyErr := detachedVerification(leaseCtx, candidate, request)
	if leaseErr := lockLeaseFailure(leaseCtx); leaseErr != nil {
		verifyErr = errors.Join(verifyErr, leaseErr)
	}
	success := verifyErr == nil && verification.Passed
	cause := errors.Join(executeErr, verifyErr)
	if !verification.Passed && cause == nil {
		cause = errors.New("post-commit verification found failed checks")
	}
	return service.finalizeAutomaticResume(record, execution, verification, success, cause)
}
