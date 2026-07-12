package workflow

import (
	"context"
	"fmt"
	"time"

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
	candidate, found := service.registry.Get(request.Operation.Engine)
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
	observationLabel := fmt.Sprintf("%s@%s", observation.ClusterID, observation.ObservedAt.UTC().Format(time.RFC3339Nano))
	if record.Observation != "" && record.Observation != observationLabel {
		return record, nil, model.OperationPlan{}, fmt.Errorf("topology observation changed since the operation was created")
	}
	record, err = service.advanceDurable(record.ResourceID, model.StageDiscover, model.OperationTransition{
		Status: model.OperationPlanned, Observation: observationLabel, Message: "topology observation validated",
	})
	if err != nil {
		return model.OperationRecord{}, nil, model.OperationPlan{}, err
	}
	request, err = service.resolver.Resolve(ctx, request)
	if err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		return record, nil, model.OperationPlan{}, err
	}
	record, err = service.advanceDurable(record.ResourceID, model.StagePrecheck, model.OperationTransition{
		Status: model.OperationPlanned, Message: "adapter precheck completed",
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
