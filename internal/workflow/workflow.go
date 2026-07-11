package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

type SafetyGuard interface {
	Evaluate(context.Context, model.Operation) error
}

type LockManager interface {
	Acquire(context.Context, model.Operation) (func(), error)
}

type ApprovalValidator interface {
	Validate(context.Context, model.Operation, string) error
}

type Journal interface {
	RecordAudit(model.AuditEvent)
	RecordReport(model.Report)
}

type MemoryJournal struct {
	mu      sync.RWMutex
	audits  []model.AuditEvent
	reports []model.Report
}

func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{} }

func (journal *MemoryJournal) RecordAudit(event model.AuditEvent) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.audits = append(journal.audits, event)
}

func (journal *MemoryJournal) RecordReport(report model.Report) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.reports = append(journal.reports, report)
}

func (journal *MemoryJournal) Audits() []model.AuditEvent {
	journal.mu.RLock()
	defer journal.mu.RUnlock()
	return append([]model.AuditEvent{}, journal.audits...)
}

func (journal *MemoryJournal) Reports() []model.Report {
	journal.mu.RLock()
	defer journal.mu.RUnlock()
	return append([]model.Report{}, journal.reports...)
}

type Service struct {
	registry *adapter.Registry
	safety   SafetyGuard
	locks    LockManager
	approval ApprovalValidator
	journal  Journal
	now      func() time.Time
}

func New(registry *adapter.Registry, safety SafetyGuard, locks LockManager, approval ApprovalValidator, journal Journal) *Service {
	return &Service{registry: registry, safety: safety, locks: locks, approval: approval, journal: journal, now: time.Now}
}

func (service *Service) audit(operation model.Operation, stage model.WorkflowStage, message string) {
	if service.journal == nil {
		return
	}
	now := service.now().UTC()
	service.journal.RecordAudit(model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operation.ResourceID,
		Stage:        stage,
		Actor:        operation.RequestedBy,
		Message:      message,
	})
}

func (service *Service) report(operation model.Operation, execution model.Execution) {
	if service.journal == nil {
		return
	}
	now := service.now().UTC()
	service.journal.RecordReport(model.Report{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operation.ResourceID,
		Title:        string(operation.Kind) + " report",
		Summary:      execution.Message,
	})
}

func hasBlockingCheck(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func (service *Service) unsupported(operation model.Operation, message string) (model.Execution, error) {
	execution := model.Execution{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: service.now().UTC(), UpdatedAt: service.now().UTC()}, OperationID: operation.ResourceID, Status: model.OperationUnsupported, Message: message}
	service.audit(operation, model.StagePrecheck, message)
	service.audit(operation, model.StageReport, "unsupported operation reported")
	service.report(operation, execution)
	return execution, adapter.ErrUnsupported
}

func (service *Service) Execute(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error) {
	if service.registry == nil {
		return model.Execution{}, fmt.Errorf("adapter registry is not configured")
	}
	operation := request.Operation
	if operation.ResourceID == "" {
		operation.ResourceID = model.NewResourceID()
	}
	operation.Status = model.OperationRunning
	candidate, ok := service.registry.Get(operation.Engine)
	if !ok {
		return service.unsupported(operation, "no adapter is registered for the requested engine")
	}
	capabilities := candidate.Capabilities(ctx)
	if !capabilities.Supports(adapter.CapabilityExecute) {
		return service.unsupported(operation, "operation execution is unsupported by this adapter")
	}

	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StagePrecheck, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StagePrecheck, "adapter precheck completed")
	if hasBlockingCheck(checks) {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: "precheck contains blocking checks"}
		service.report(operation, execution)
		return execution, nil
	}
	if _, err := candidate.BuildPlan(ctx, request); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StagePlan, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StagePlan, "adapter operation plan created")
	if service.safety == nil || service.locks == nil || service.approval == nil {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
	}
	if err := service.safety.Evaluate(ctx, operation); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageSafetyGuard, "safety guard blocked execution: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageSafetyGuard, "safety guard passed")
	release, err := service.locks.Acquire(ctx, operation)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageLock, "operation lock blocked execution: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	defer release()
	service.audit(operation, model.StageLock, "operation lock acquired")
	if err := service.approval.Validate(ctx, operation, approvalToken); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageApprove, "approval blocked execution: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageApprove, "approval validated")
	execution, err := candidate.Execute(ctx, request)
	if err != nil {
		if execution.Status == "" {
			execution.Status = model.OperationFailed
		}
		execution.OperationID = operation.ResourceID
		service.audit(operation, model.StageExecute, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	execution.OperationID = operation.ResourceID
	service.audit(operation, model.StageExecute, "adapter execution completed")
	verification, err := candidate.Verify(ctx, request)
	if err != nil || !verification.Passed {
		if err != nil {
			execution.Message = err.Error()
		} else {
			execution.Message = "verification failed"
		}
		execution.Status = model.OperationFailed
		service.audit(operation, model.StageVerify, execution.Message)
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageVerify, "verification passed")
	execution.Status = model.OperationSucceeded
	if execution.Message == "" {
		execution.Message = "operation completed and verified"
	}
	service.audit(operation, model.StageAudit, "operation audit recorded")
	service.audit(operation, model.StageReport, "operation report generated")
	service.report(operation, execution)
	return execution, nil
}

func (service *Service) ExecuteMetadata(ctx context.Context, operation model.Operation, request adapter.MetadataRequest, approvalToken string, commit func() error) (model.Execution, error) {
	if service.registry == nil || commit == nil {
		return model.Execution{}, fmt.Errorf("metadata workflow is not configured")
	}
	if operation.ResourceID == "" {
		operation.ResourceID = model.NewResourceID()
	}
	operation.Engine = request.Instance.Engine
	operation.ClusterID = request.ClusterID
	operation.Kind = model.OperationMetadataReconciliation
	candidate, ok := service.registry.Get(operation.Engine)
	if !ok || !candidate.Capabilities(ctx).Supports(adapter.CapabilityMetadataReconcile) {
		return service.unsupported(operation, "metadata reconciliation is unsupported by this adapter")
	}
	checks, err := candidate.MetadataPrecheck(ctx, request)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StagePrecheck, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StagePrecheck, "metadata precheck completed")
	if hasBlockingCheck(checks) {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: "metadata precheck contains blocking checks"}
		service.report(operation, execution)
		return execution, nil
	}
	if _, err := candidate.ReconcileMetadata(ctx, request); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StagePlan, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StagePlan, "metadata reconciliation plan created")
	if service.safety == nil || service.locks == nil || service.approval == nil {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
	}
	if err := service.safety.Evaluate(ctx, operation); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageSafetyGuard, "safety guard blocked metadata reconciliation: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageSafetyGuard, "safety guard passed")
	release, err := service.locks.Acquire(ctx, operation)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageLock, "operation lock blocked metadata reconciliation: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	defer release()
	service.audit(operation, model.StageLock, "operation lock acquired")
	if err := service.approval.Validate(ctx, operation, approvalToken); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		service.audit(operation, model.StageApprove, "approval blocked metadata reconciliation: "+err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageApprove, "approval validated")
	if err := commit(); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StageExecute, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageExecute, "metadata reconciliation committed")
	if _, err := identity.InstanceKey(request.Instance.Engine, request.Instance.EngineIdentity); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		service.audit(operation, model.StageVerify, err.Error())
		service.report(operation, execution)
		return execution, err
	}
	service.audit(operation, model.StageVerify, "engine identity verified after metadata reconciliation")
	execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationSucceeded, Message: "metadata reconciliation completed and verified"}
	service.audit(operation, model.StageAudit, "metadata reconciliation audit recorded")
	service.audit(operation, model.StageReport, "metadata reconciliation report generated")
	service.report(operation, execution)
	return execution, nil
}
