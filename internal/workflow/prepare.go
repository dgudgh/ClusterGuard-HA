package workflow

import (
	"context"
	"fmt"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func (service *Service) Precheck(ctx context.Context, request adapter.OperationRequest) (model.OperationRecord, []model.Check, error) {
	record, checks, _, err := service.prepare(ctx, request, false)
	return record, checks, err
}

func (service *Service) Plan(ctx context.Context, request adapter.OperationRequest) (model.OperationRecord, model.OperationPlan, error) {
	record, _, plan, err := service.prepare(ctx, request, true)
	return record, plan, err
}

func (service *Service) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	if service.registry == nil || service.operations == nil || service.resolver == nil {
		return model.Verification{}, fmt.Errorf("durable operation verification is not configured")
	}
	record, found := service.operations.OperationByIdempotencyKey(request.IdempotencyKey)
	if !found {
		return model.Verification{}, fmt.Errorf("operation does not exist")
	}
	// A successful durable operation already owns immutable verification
	// evidence. Re-running the adapter against the post-operation topology can
	// invalidate the original observation token and incorrectly turn a proven
	// success into a verification failure. Only indeterminate operations need a
	// fresh probe; completed operations return their persisted evidence.
	if record.Status == model.OperationSucceeded {
		verification := record.Verification
		verification.Passed = verification.Successful() && verification.OperationID == record.ResourceID
		if !verification.Passed {
			return verification, fmt.Errorf("persisted verification is incomplete or inconsistent; operation requires review")
		}
		return verification, nil
	}
	candidate, found := service.resolveAdapter(record.Operation)
	if !found || !candidate.Capabilities(ctx).Supports(adapter.CapabilityVerify) {
		return model.Verification{}, adapter.ErrUnsupported
	}
	if !service.claimOperation(record.ResourceID) {
		return model.Verification{}, ErrOperationInProgress
	}
	defer service.releaseOperation(record.ResourceID)
	request.Operation = record.Operation
	request.Operation.ResourceID = record.ResourceID
	request.TargetID = record.TargetID
	request.Plan = &record.Plan
	request, err := service.resolver.Resolve(ctx, request)
	if err != nil {
		return model.Verification{}, err
	}
	verification, err := verifyOperation(ctx, candidate, request)
	if err != nil {
		return verification, err
	}
	message := "manual operation verification failed"
	if verification.Successful() {
		message = "manual operation verification passed"
	}
	if record.Status == model.OperationIndeterminate {
		status := model.OperationIndeterminate
		failureClass := record.FailureClass
		if verification.Successful() {
			status = model.OperationSucceeded
			failureClass = ""
		}
		execution := record.Execution
		execution.OperationID = record.ResourceID
		execution.Status = status
		execution.Message = message
		if finalizer, ok := service.atomicFinalizer(); ok {
			_, err := finalizer.FinalizeOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
				Stage: model.StageReport, Status: status, Execution: &execution, Verification: &verification,
				FailureClass: failureClass, Message: message,
			}, []model.AuditEvent{
				service.auditEvent(record.Operation, model.StageVerify, message),
				service.auditEvent(record.Operation, model.StageReport, "manual verification report generated"),
			}, []model.Report{service.terminalReport(record.Operation, execution)})
			return verification, err
		}
		if err := service.audit(record.Operation, model.StageVerify, message); err != nil {
			return verification, err
		}
		if err := service.report(record.Operation, execution, true); err != nil {
			return verification, err
		}
		if _, err := service.operations.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
			Stage: model.StageReport, Status: status, Execution: &execution, Verification: &verification,
			FailureClass: failureClass, Message: message,
		}); err != nil {
			return verification, err
		}
		return verification, nil
	}
	if err := service.audit(record.Operation, model.StageVerify, message); err != nil {
		return verification, err
	}
	return verification, nil
}

func (service *Service) prepare(ctx context.Context, request adapter.OperationRequest, includePlan bool) (model.OperationRecord, []model.Check, model.OperationPlan, error) {
	if service.registry == nil || service.operations == nil || service.resolver == nil {
		return model.OperationRecord{}, nil, model.OperationPlan{}, fmt.Errorf("durable operation preparation is not configured")
	}
	record, _, err := service.operations.CreateOperation(model.OperationRecord{
		Operation: request.Operation, TargetID: request.TargetID, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil && !isCommittedWarning(err) {
		return model.OperationRecord{}, nil, model.OperationPlan{}, err
	}
	if err != nil {
		persisted, found := service.operations.Operation(record.ResourceID)
		if !found {
			return model.OperationRecord{}, nil, model.OperationPlan{}, err
		}
		record = persisted
	}
	if durableTerminalStatus(record.Status) {
		return record, nil, record.Plan, fmt.Errorf("terminal operation cannot be prepared")
	}
	if !service.claimOperation(record.ResourceID) {
		return record, nil, record.Plan, ErrOperationInProgress
	}
	defer service.releaseOperation(record.ResourceID)

	request.Operation = record.Operation
	request.Operation.ResourceID = record.ResourceID
	request.TargetID = record.TargetID
	candidate, found := service.resolveAdapter(request.Operation)
	if !found || !candidate.Capabilities(ctx).Supports(adapter.CapabilityPrecheck) {
		return record, nil, model.OperationPlan{}, adapter.ErrUnsupported
	}
	if includePlan && !candidate.Capabilities(ctx).Supports(adapter.CapabilityPlan) {
		return record, nil, model.OperationPlan{}, adapter.ErrUnsupported
	}
	if service.discovery == nil {
		return record, nil, model.OperationPlan{}, fmt.Errorf("discovery validation is not configured")
	}
	observation, err := service.discovery.CaptureObservation(ctx, request.Operation)
	if err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	observationLabel := observationLabel(observation)
	if record.Observation != "" && record.Observation != observationLabel {
		return record, nil, model.OperationPlan{}, fmt.Errorf("topology observation changed since the operation was created")
	}
	record, err = service.advanceDurable(record.ResourceID, model.StageDiscover, model.OperationTransition{
		Status: model.OperationPlanned, Observation: observationLabel, Message: "topology observation validated",
	})
	if err != nil {
		return model.OperationRecord{}, nil, model.OperationPlan{}, err
	}
	request, err = resolveCapturedOperation(ctx, service.resolver, request, observation)
	if err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	request.Resolved.ObservationToken = observationLabel
	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	record, err = service.advanceDurable(record.ResourceID, model.StagePrecheck, model.OperationTransition{
		Status: model.OperationPlanned, Precheck: append([]model.Check{}, checks...), Message: "adapter precheck completed",
	})
	if err != nil {
		return model.OperationRecord{}, nil, model.OperationPlan{}, err
	}
	if err := service.audit(request.Operation, model.StagePrecheck, "read-only adapter precheck completed"); err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	if !includePlan {
		return record, checks, model.OperationPlan{}, nil
	}
	plan, err := candidate.BuildPlan(ctx, request)
	if err != nil {
		return record, checks, model.OperationPlan{}, err
	}
	record, err = service.putDurablePlan(record, plan)
	if err != nil {
		return model.OperationRecord{}, checks, model.OperationPlan{}, err
	}
	if err := service.audit(request.Operation, model.StagePlan, "read-only immutable operation plan persisted"); err != nil {
		return record, checks, model.OperationPlan{}, err
	}
	return record, checks, record.Plan, nil
}
