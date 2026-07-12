package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

var ErrJournalPersistence = errors.New("workflow journal persistence failed")
var ErrOperationInProgress = errors.New("operation is already in progress")

type journalPersistenceError struct {
	err error
}

func (failure *journalPersistenceError) Error() string {
	return fmt.Sprintf("%s: %v", ErrJournalPersistence, failure.err)
}

func (failure *journalPersistenceError) Unwrap() error { return failure.err }

func (failure *journalPersistenceError) Is(target error) bool {
	return target == ErrJournalPersistence || errors.Is(failure.err, target)
}

type SafetyGuard interface {
	Evaluate(context.Context, model.Operation) error
}

type ObservationToken struct {
	ClusterID  model.ResourceID
	ObservedAt time.Time
}

type DiscoveryValidator interface {
	CaptureObservation(context.Context, model.Operation) (ObservationToken, error)
	RevalidateObservation(context.Context, model.Operation, ObservationToken) error
}

type LockManager interface {
	Acquire(context.Context, model.Operation) (func(), error)
}

type ApprovalValidator interface {
	Validate(context.Context, model.Operation, string) error
}

type Journal interface {
	RecordAudit(model.AuditEvent) error
	RecordReport(model.Report) error
}

type MemoryJournal struct {
	mu      sync.RWMutex
	audits  []model.AuditEvent
	reports []model.Report
}

func NewMemoryJournal() *MemoryJournal { return &MemoryJournal{} }

func (journal *MemoryJournal) RecordAudit(event model.AuditEvent) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.audits = append(journal.audits, event)
	return nil
}

func (journal *MemoryJournal) RecordReport(report model.Report) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for index, existing := range journal.reports {
		if existing.ResourceID == report.ResourceID {
			journal.reports[index] = report
			return nil
		}
	}
	journal.reports = append(journal.reports, report)
	return nil
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
	registry   *adapter.Registry
	discovery  DiscoveryValidator
	safety     SafetyGuard
	locks      LockManager
	approval   ApprovalValidator
	journal    Journal
	operations OperationStore
	resolver   OperationResolver
	inflightMu sync.Mutex
	inflight   map[model.ResourceID]struct{}
	now        func() time.Time
}

type Option func(*Service)

func WithOperationStore(operations OperationStore) Option {
	return func(service *Service) { service.operations = operations }
}

func WithOperationResolver(resolver OperationResolver) Option {
	return func(service *Service) { service.resolver = resolver }
}

func New(registry *adapter.Registry, discovery DiscoveryValidator, safety SafetyGuard, locks LockManager, approval ApprovalValidator, journal Journal, options ...Option) *Service {
	service := &Service{registry: registry, discovery: discovery, safety: safety, locks: locks, approval: approval, journal: journal, inflight: map[model.ResourceID]struct{}{}, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (service *Service) audit(operation model.Operation, stage model.WorkflowStage, message string) error {
	if service.journal == nil {
		return fmt.Errorf("workflow journal is not configured")
	}
	now := service.now().UTC()
	if err := service.journal.RecordAudit(model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operation.ResourceID,
		Stage:        stage,
		Actor:        operation.RequestedBy,
		Message:      message,
	}); err != nil {
		return fmt.Errorf("persist %s audit event: %w", stage, err)
	}
	return nil
}

func (service *Service) report(operation model.Operation, execution model.Execution, operationCommitted bool) error {
	if service.journal == nil {
		return fmt.Errorf("workflow journal is not configured")
	}
	now := service.now().UTC()
	fallback := execution
	if operationCommitted {
		fallback = markIndeterminate(fallback)
	} else {
		fallback.Status = model.OperationFailed
		fallback.Message = "workflow journal persistence failed"
	}
	report := model.Report{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operation.ResourceID,
		Title:        string(operation.Kind) + " report",
		Status:       fallback.Status,
		Summary:      fallback.Message,
	}
	if err := service.journal.RecordReport(report); err != nil {
		return fmt.Errorf("persist fallback operation report: %w", err)
	}
	report.Status = execution.Status
	report.Summary = execution.Message
	if err := service.journal.RecordReport(report); err != nil {
		if isCommittedWarning(err) {
			// The conservative record was already made durable. After the terminal
			// rename, recovery can observe either the terminal record or fallback.
			return nil
		}
		return fmt.Errorf("persist terminal operation report: %w", err)
	}
	return nil
}

func journalFailure(execution model.Execution, err error) (model.Execution, error) {
	execution.Status = model.OperationFailed
	execution.Message = "workflow journal persistence failed"
	return execution, &journalPersistenceError{err: err}
}

func markIndeterminate(execution model.Execution) model.Execution {
	execution.Status = model.OperationIndeterminate
	execution.Message = "operation committed but workflow journal persistence failed"
	return execution
}

func markDurabilityIndeterminate(execution model.Execution) model.Execution {
	execution.Status = model.OperationIndeterminate
	execution.Message = "operation committed but persistence durability could not be confirmed"
	return execution
}

func isCommittedWarning(err error) bool {
	var warning interface{ Committed() bool }
	return err != nil && errors.As(err, &warning) && warning.Committed()
}

func journalIndeterminate(execution model.Execution, err error) (model.Execution, error) {
	execution = markIndeterminate(execution)
	return execution, &journalPersistenceError{err: err}
}

func firstJournalError(current error, candidate error) error {
	if current != nil {
		return current
	}
	return candidate
}

func (service *Service) recordOutcome(operation model.Operation, execution model.Execution, stage model.WorkflowStage, message string, cause error) (model.Execution, error) {
	if err := service.audit(operation, stage, message); err != nil {
		return journalFailure(execution, err)
	}
	if err := service.report(operation, execution, false); err != nil {
		return journalFailure(execution, err)
	}
	return execution, cause
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
	if err := service.audit(operation, model.StagePrecheck, message); err != nil {
		return journalFailure(execution, err)
	}
	if err := service.audit(operation, model.StageReport, "unsupported operation reported"); err != nil {
		return journalFailure(execution, err)
	}
	if err := service.report(operation, execution, false); err != nil {
		return journalFailure(execution, err)
	}
	return execution, adapter.ErrUnsupported
}

func (service *Service) Execute(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error) {
	if service.operations != nil || service.resolver != nil {
		if service.operations == nil || service.resolver == nil {
			return model.Execution{}, fmt.Errorf("durable workflow requires operation store and resolver")
		}
		return service.executeDurable(ctx, request, approvalToken)
	}
	return service.executeLegacy(ctx, request, approvalToken)
}

func (service *Service) executeLegacy(ctx context.Context, request adapter.OperationRequest, approvalToken string) (model.Execution, error) {
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
	for _, required := range []adapter.Capability{
		adapter.CapabilityPrecheck,
		adapter.CapabilityPlan,
		adapter.CapabilityExecute,
		adapter.CapabilityVerify,
	} {
		if !capabilities.Supports(required) {
			return service.unsupported(operation, fmt.Sprintf("operation %s is unsupported by this adapter", required))
		}
	}
	if service.discovery == nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: "discovery validation is not configured"}
		return service.recordOutcome(operation, execution, model.StageDiscover, execution.Message, fmt.Errorf("%s", execution.Message))
	}
	observation, err := service.discovery.CaptureObservation(ctx, operation)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageDiscover, "discovery observation blocked execution: "+err.Error(), err)
	}
	observationLabel := fmt.Sprintf("%s@%s", observation.ClusterID, observation.ObservedAt.UTC().Format(time.RFC3339Nano))
	if err := service.audit(operation, model.StageDiscover, "topology observation "+observationLabel+" validated"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}

	checks, err := candidate.Precheck(ctx, request)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StagePrecheck, err.Error(), err)
	}
	if err := service.audit(operation, model.StagePrecheck, "adapter precheck completed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if hasBlockingCheck(checks) {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: "precheck contains blocking checks"}
		return service.recordOutcome(operation, execution, model.StagePrecheck, "adapter precheck blocked execution", nil)
	}
	if _, err := candidate.BuildPlan(ctx, request); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StagePlan, err.Error(), err)
	}
	if err := service.audit(operation, model.StagePlan, "adapter operation plan created"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if service.safety == nil || service.locks == nil || service.approval == nil {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
	}
	if err := service.safety.Evaluate(ctx, operation); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageSafetyGuard, "safety guard blocked execution: "+err.Error(), err)
	}
	if err := service.audit(operation, model.StageSafetyGuard, "safety guard passed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	release, err := service.locks.Acquire(ctx, operation)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageLock, "operation lock blocked execution: "+err.Error(), err)
	}
	defer release()
	if err := service.audit(operation, model.StageLock, "operation lock acquired"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if err := service.discovery.RevalidateObservation(ctx, operation, observation); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageLock, "topology observation changed under operation lock: "+err.Error(), err)
	}
	if err := service.audit(operation, model.StageLock, "topology observation "+observationLabel+" revalidated under operation lock"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if err := service.approval.Validate(ctx, operation, approvalToken); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageApprove, "approval blocked execution: "+err.Error(), err)
	}
	if err := service.audit(operation, model.StageApprove, "approval validated"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	execution, err := candidate.Execute(ctx, request)
	if err != nil {
		if execution.Status == "" {
			execution.Status = model.OperationFailed
		}
		execution.OperationID = operation.ResourceID
		return service.recordOutcome(operation, execution, model.StageExecute, err.Error(), err)
	}
	execution.OperationID = operation.ResourceID
	var committedJournalErr error
	if err := service.audit(operation, model.StageExecute, "adapter execution completed"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	verification, err := candidate.Verify(ctx, request)
	if err != nil || !verification.Passed {
		if err != nil {
			execution.Message = err.Error()
		} else {
			execution.Message = "verification failed"
		}
		execution.Status = model.OperationFailed
		if auditErr := service.audit(operation, model.StageVerify, execution.Message); auditErr != nil {
			committedJournalErr = firstJournalError(committedJournalErr, auditErr)
		}
		if committedJournalErr != nil {
			execution = markIndeterminate(execution)
		}
		if reportErr := service.report(operation, execution, true); reportErr != nil {
			committedJournalErr = firstJournalError(committedJournalErr, reportErr)
		}
		if committedJournalErr != nil {
			return journalIndeterminate(execution, committedJournalErr)
		}
		return execution, err
	}
	if err := service.audit(operation, model.StageVerify, "verification passed"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	execution.Status = model.OperationSucceeded
	if execution.Message == "" {
		execution.Message = "operation completed and verified"
	}
	if err := service.audit(operation, model.StageAudit, "operation audit recorded"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if err := service.audit(operation, model.StageReport, "operation report generated"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if committedJournalErr != nil {
		execution = markIndeterminate(execution)
	}
	if err := service.report(operation, execution, true); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if committedJournalErr != nil {
		return journalIndeterminate(execution, committedJournalErr)
	}
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
		return service.recordOutcome(operation, execution, model.StagePrecheck, err.Error(), err)
	}
	if err := service.audit(operation, model.StagePrecheck, "metadata precheck completed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if hasBlockingCheck(checks) {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: "metadata precheck contains blocking checks"}
		return service.recordOutcome(operation, execution, model.StagePrecheck, "metadata precheck blocked reconciliation", nil)
	}
	if _, err := candidate.ReconcileMetadata(ctx, request); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StagePlan, err.Error(), err)
	}
	if err := service.audit(operation, model.StagePlan, "metadata reconciliation plan created"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if service.safety == nil || service.locks == nil || service.approval == nil {
		return model.Execution{}, fmt.Errorf("workflow gates are not configured")
	}
	if err := service.safety.Evaluate(ctx, operation); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageSafetyGuard, "safety guard blocked metadata reconciliation: "+err.Error(), err)
	}
	if err := service.audit(operation, model.StageSafetyGuard, "safety guard passed"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	release, err := service.locks.Acquire(ctx, operation)
	if err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageLock, "operation lock blocked metadata reconciliation: "+err.Error(), err)
	}
	defer release()
	if err := service.audit(operation, model.StageLock, "operation lock acquired"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	if err := service.approval.Validate(ctx, operation, approvalToken); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationBlocked, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageApprove, "approval blocked metadata reconciliation: "+err.Error(), err)
	}
	if err := service.audit(operation, model.StageApprove, "approval validated"); err != nil {
		return journalFailure(model.Execution{OperationID: operation.ResourceID}, err)
	}
	commitErr := commit()
	if commitErr != nil && !isCommittedWarning(commitErr) {
		err := commitErr
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		return service.recordOutcome(operation, execution, model.StageExecute, err.Error(), err)
	}
	var committedJournalErr error
	if err := service.audit(operation, model.StageExecute, "metadata reconciliation committed"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if _, err := identity.InstanceKey(request.Instance.Engine, request.Instance.EngineIdentity); err != nil {
		execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}
		if auditErr := service.audit(operation, model.StageVerify, err.Error()); auditErr != nil {
			committedJournalErr = firstJournalError(committedJournalErr, auditErr)
		}
		if commitErr != nil {
			execution = markDurabilityIndeterminate(execution)
		}
		if committedJournalErr != nil {
			execution = markIndeterminate(execution)
		}
		if reportErr := service.report(operation, execution, true); reportErr != nil {
			committedJournalErr = firstJournalError(committedJournalErr, reportErr)
		}
		if committedJournalErr != nil {
			return journalIndeterminate(execution, committedJournalErr)
		}
		if commitErr != nil {
			return execution, commitErr
		}
		return execution, err
	}
	if err := service.audit(operation, model.StageVerify, "engine identity verified after metadata reconciliation"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	execution := model.Execution{OperationID: operation.ResourceID, Status: model.OperationSucceeded, Message: "metadata reconciliation completed and verified"}
	if err := service.audit(operation, model.StageAudit, "metadata reconciliation audit recorded"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if err := service.audit(operation, model.StageReport, "metadata reconciliation report generated"); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if commitErr != nil {
		execution = markDurabilityIndeterminate(execution)
	}
	if committedJournalErr != nil {
		execution = markIndeterminate(execution)
	}
	if err := service.report(operation, execution, true); err != nil {
		committedJournalErr = firstJournalError(committedJournalErr, err)
	}
	if committedJournalErr != nil {
		return journalIndeterminate(execution, committedJournalErr)
	}
	if commitErr != nil {
		return execution, commitErr
	}
	return execution, nil
}
