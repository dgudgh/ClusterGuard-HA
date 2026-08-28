package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type TaskStore interface {
	PutLifecycleTask(Task) (Task, error)
	LifecycleTask(model.ResourceID) (Task, bool)
	LifecycleTasks() []Task
}

type LifecycleJournal interface {
	RecordAudit(model.AuditEvent) error
	RecordReport(model.Report) error
}

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type SafetyGuard interface {
	EvaluateLifecycle(context.Context, Request, Plan) error
}

type ClusterLocker interface {
	AcquireCluster(context.Context, model.ResourceID) (context.Context, func(), error)
}

type ApprovalGate interface {
	ValidateLifecycle(context.Context, Request, Plan, string) error
}

type Executor interface {
	Execute(context.Context, Request, Plan, ExecutionSecrets, func(Event)) (ExecutionResult, error)
}

type MetadataCommitter interface {
	Commit(context.Context, Task, ExecutionResult) error
}

type ControllerTarget struct {
	ResourceID model.ResourceID
	NodeName   string
	Hostname   string
	IPAddress  string
}

type ControllerMembership interface {
	AddControllers(context.Context, []ControllerTarget) error
}

type ManagerOption func(*Manager)

func WithControllerMembership(membership ControllerMembership) ManagerOption {
	return func(manager *Manager) { manager.membership = membership }
}

type Manager struct {
	store      TaskStore
	authority  MutationAuthority
	safety     SafetyGuard
	locks      ClusterLocker
	approval   ApprovalGate
	executor   Executor
	committer  MetadataCommitter
	membership ControllerMembership
	now        func() time.Time
}

func NewManager(store TaskStore, authority MutationAuthority, safety SafetyGuard, locks ClusterLocker, approval ApprovalGate, executor Executor, committer MetadataCommitter, now func() time.Time, options ...ManagerOption) *Manager {
	if now == nil {
		now = time.Now
	}
	manager := &Manager{store: store, authority: authority, safety: safety, locks: locks, approval: approval, executor: executor, committer: committer, now: now}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func controllerTargetsForMembership(plan Plan) []ControllerTarget {
	targets := make([]ControllerTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		if target.ReusesNodeSlot || (target.Kind != model.NodeController && target.Kind != model.NodeMixed) {
			continue
		}
		targets = append(targets, ControllerTarget{
			ResourceID: target.NodeID,
			NodeName:   target.NodeName,
			Hostname:   target.Hostname,
			IPAddress:  target.IPAddress,
		})
	}
	return targets
}

func (manager *Manager) configured() bool {
	if manager == nil || manager.store == nil || manager.authority == nil || manager.safety == nil || manager.locks == nil || manager.approval == nil || manager.executor == nil || manager.committer == nil {
		return false
	}
	_, journalConfigured := manager.store.(LifecycleJournal)
	return journalConfigured
}

func (manager *Manager) Execute(ctx context.Context, request Request, plan Plan, secrets ExecutionSecrets, approvalToken string) (Task, error) {
	return manager.execute(ctx, request, plan, secrets, lifecycleExecutionAuthorization{approvalToken: approvalToken})
}

type lifecycleExecutionAuthorization struct {
	approvalToken string
	platformActor string
}

func (authorization lifecycleExecutionAuthorization) platformAuthorized() bool {
	return strings.TrimSpace(authorization.platformActor) != ""
}

func (manager *Manager) ExecuteAuthorized(ctx context.Context, request Request, plan Plan, secrets ExecutionSecrets, actor string) (Task, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Task{}, fmt.Errorf("platform lifecycle actor is required")
	}
	request.RequestedBy = actor
	return manager.execute(ctx, request, plan, secrets, lifecycleExecutionAuthorization{platformActor: actor})
}

func (manager *Manager) execute(ctx context.Context, request Request, plan Plan, secrets ExecutionSecrets, authorization lifecycleExecutionAuthorization) (Task, error) {
	if !manager.configured() {
		return Task{}, fmt.Errorf("lifecycle manager is not configured")
	}
	if plan.Blocked || plan.ClusterID != request.ClusterID || plan.Action != request.Action {
		return Task{}, fmt.Errorf("lifecycle plan is blocked or does not match the request")
	}
	redactionSecrets := secrets
	now := manager.now().UTC()
	task := Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), CreatedAt: now, UpdatedAt: now},
		ClusterID:    request.ClusterID, OperationID: model.NewResourceID(), Request: request, Plan: plan, Status: TaskQueued,
	}
	journal := manager.store.(LifecycleJournal)
	actor := strings.TrimSpace(request.RequestedBy)
	if actor == "" {
		actor = "system"
	}
	recordAudit := func(stage model.WorkflowStage, message string) error {
		eventNow := manager.now().UTC()
		return journal.RecordAudit(model.AuditEvent{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), CreatedAt: eventNow, UpdatedAt: eventNow},
			OperationID:  task.OperationID,
			Stage:        stage,
			Actor:        actor,
			Message:      redactLifecycleMessage(message, redactionSecrets),
		})
	}
	var err error
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return Task{}, fmt.Errorf("persist queued lifecycle task: %w", err)
	}
	persistTerminal := func(status TaskStatus, message string, cause error) (Task, error) {
		task.Status = status
		task.Message = message
		persisted, persistErr := manager.store.PutLifecycleTask(task)
		if persistErr == nil {
			task = persisted
		}
		return task, errors.Join(cause, persistErr)
	}
	if err := recordAudit(model.StagePrecheck, "lifecycle request and immutable plan matched"); err != nil {
		return persistTerminal(TaskFailed, "lifecycle precheck audit could not be persisted", err)
	}
	interrupt := func(cause error) (Task, error) {
		return persistTerminal(TaskInterrupted, "lifecycle execution lost leader-backed mutation authority", cause)
	}
	indeterminate := func(message string, cause error) (Task, error) {
		return persistTerminal(TaskIndeterminate, message, cause)
	}
	if err := manager.authority.RequireMutationAuthority(ctx); err != nil {
		return interrupt(err)
	}
	failGate := func(message string, cause error) (Task, error) {
		return persistTerminal(TaskFailed, message, cause)
	}
	if err := manager.safety.EvaluateLifecycle(ctx, request, plan); err != nil {
		auditErr := recordAudit(model.StageSafetyGuard, "lifecycle safety guard blocked execution")
		err = errors.Join(err, auditErr)
		return failGate("lifecycle safety guard blocked execution", err)
	}
	if err := recordAudit(model.StageSafetyGuard, "lifecycle safety guard passed"); err != nil {
		return failGate("lifecycle safety guard audit could not be persisted", err)
	}
	leaseCtx, release, err := manager.locks.AcquireCluster(ctx, request.ClusterID)
	if err != nil {
		err = errors.Join(err, recordAudit(model.StageLock, "cluster operation lock could not be acquired"))
		return interrupt(err)
	}
	defer release()
	if err := recordAudit(model.StageLock, "cluster operation lock acquired"); err != nil {
		return failGate("cluster operation lock audit could not be persisted", err)
	}
	approvalMessage := "platform role authorized lifecycle execution"
	if !authorization.platformAuthorized() {
		if err := manager.approval.ValidateLifecycle(leaseCtx, request, plan, authorization.approvalToken); err != nil {
			err = errors.Join(err, recordAudit(model.StageApprove, "lifecycle approval blocked execution"))
			return failGate("lifecycle approval blocked execution", err)
		}
		approvalMessage = "lifecycle approval validated"
	}
	if err := recordAudit(model.StageApprove, approvalMessage); err != nil {
		return failGate("lifecycle approval audit could not be persisted", err)
	}
	task.Status = TaskRunning
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return task, fmt.Errorf("persist running lifecycle task: %w", err)
	}
	if err := recordAudit(model.StageExecute, "lifecycle execution authorized"); err != nil {
		return failGate("lifecycle execution audit could not be persisted", err)
	}
	if leaseErr := context.Cause(leaseCtx); leaseErr != nil {
		return interrupt(leaseErr)
	}

	var eventPersistenceError error
	emit := func(event Event) {
		if eventPersistenceError != nil {
			return
		}
		task.CurrentStage = event.Stage
		message := redactLifecycleMessage(event.Message, redactionSecrets)
		updated := false
		for index := range task.Stages {
			if task.Stages[index].Stage == event.Stage {
				task.Stages[index] = StageState{Stage: event.Stage, Status: event.Status, Message: message, UpdatedAt: manager.now().UTC()}
				updated = true
				break
			}
		}
		if !updated {
			task.Stages = append(task.Stages, StageState{Stage: event.Stage, Status: event.Status, Message: message, UpdatedAt: manager.now().UTC()})
		}
		if message != "" {
			task.LogTail = append(task.LogTail, message)
			if len(task.LogTail) > 200 {
				task.LogTail = task.LogTail[len(task.LogTail)-200:]
			}
		}
		var persistErr error
		task, persistErr = manager.store.PutLifecycleTask(task)
		if persistErr != nil {
			eventPersistenceError = persistErr
		}
	}

	result, executionErr := manager.executor.Execute(leaseCtx, request, plan, secrets, emit)
	secrets = ExecutionSecrets{}
	if leaseErr := context.Cause(leaseCtx); leaseErr != nil {
		executionErr = errors.Join(executionErr, leaseErr)
	}
	if eventPersistenceError != nil {
		executionErr = errors.Join(executionErr, eventPersistenceError)
	}
	if executionErr != nil {
		message := "lifecycle execution failed; target state requires verification"
		if detail := boundedLifecycleMessage(redactLifecycleMessage(executionErr.Error(), redactionSecrets), 2048); detail != "" {
			message += ": " + detail
		}
		return indeterminate(message, executionErr)
	}
	if err := manager.authority.RequireMutationAuthority(leaseCtx); err != nil {
		return indeterminate("lifecycle execution completed but leader-backed authority was lost before verification", err)
	}
	task.Status = TaskVerifying
	task.CurrentStage = StageVerify
	task.Checks = append([]model.Check{}, result.Checks...)
	task.Message = redactLifecycleMessage(result.Message, redactionSecrets)
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return indeterminate("lifecycle execution completed but verification state could not be persisted", err)
	}
	if !result.Verified {
		return indeterminate("lifecycle verification failed; metadata was not changed", fmt.Errorf("lifecycle verification failed"))
	}
	controllerTargets := controllerTargetsForMembership(plan)
	if len(controllerTargets) > 0 {
		if manager.membership == nil {
			return indeterminate("controller lifecycle verification passed but Raft membership integration is unavailable", fmt.Errorf("controller membership integration is not configured"))
		}
		if err := manager.membership.AddControllers(leaseCtx, controllerTargets); err != nil {
			return indeterminate("controller service installation completed but Raft membership was not committed", err)
		}
		if err := recordAudit(model.StageExecute, fmt.Sprintf("%d controller voter(s) joined the live Raft membership", len(controllerTargets))); err != nil {
			return indeterminate("controller membership was committed but its audit could not be persisted", err)
		}
	}
	if err := recordAudit(model.StageVerify, "lifecycle execution verification passed"); err != nil {
		return indeterminate("lifecycle verification passed but its audit could not be persisted", err)
	}
	if leaseErr := context.Cause(leaseCtx); leaseErr != nil {
		return indeterminate("lifecycle verification passed but the operation lock was lost before metadata commit", leaseErr)
	}
	commitErr := manager.committer.Commit(leaseCtx, task, result)
	if leaseErr := context.Cause(leaseCtx); leaseErr != nil {
		return indeterminate(
			"lifecycle metadata commit outcome is indeterminate because the operation lock was lost",
			errors.Join(commitErr, leaseErr),
		)
	}
	if commitErr != nil {
		message := "verified lifecycle metadata commit failed"
		if detail := boundedLifecycleMessage(redactLifecycleMessage(commitErr.Error(), redactionSecrets), 2048); detail != "" {
			message += ": " + detail
		}
		return indeterminate(message, commitErr)
	}
	emit(Event{Stage: StageCommit, Status: StageSucceeded, Message: "verified lifecycle metadata committed"})
	if eventPersistenceError != nil {
		return indeterminate("lifecycle metadata was committed but its completed stage could not be persisted", eventPersistenceError)
	}
	if err := recordAudit(model.StageAudit, "lifecycle execution audit trail completed"); err != nil {
		return indeterminate("lifecycle metadata was committed but audit finalization failed", err)
	}
	task.ReportID = model.NewResourceID()
	engineName := "MySQL"
	if request.Engine == model.EnginePostgreSQL {
		engineName = "PostgreSQL"
	}
	report := model.Report{
		ResourceMeta: model.ResourceMeta{ResourceID: task.ReportID, CreatedAt: manager.now().UTC(), UpdatedAt: manager.now().UTC()},
		OperationID:  task.OperationID,
		Title:        engineName + " node lifecycle report",
		Status:       model.OperationSucceeded,
		Summary:      fmt.Sprintf("%s %s lifecycle completed for %d target(s); verification passed and metadata was committed", engineName, request.Action, len(plan.Targets)),
	}
	if err := journal.RecordReport(report); err != nil {
		return indeterminate("lifecycle metadata was committed but its report could not be persisted", err)
	}
	if err := recordAudit(model.StageReport, "lifecycle report persisted"); err != nil {
		return indeterminate("lifecycle report was persisted but report audit finalization failed", err)
	}
	task.Status = TaskSucceeded
	task.CurrentStage = StageCommit
	task.Message = "lifecycle execution verified and metadata committed"
	task, err = manager.store.PutLifecycleTask(task)
	return task, err
}

func redactLifecycleMessage(message string, secrets ExecutionSecrets) string {
	for _, secret := range []string{
		secrets.SSHPassword, secrets.MySQLRootPassword,
		secrets.MySQLDiscoveryPassword, secrets.MySQLOperationPassword, secrets.ReplicationPassword,
		secrets.PostgreSQLAdminPassword, secrets.PostgreSQLReplicationPassword,
	} {
		if strings.TrimSpace(secret) != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return strings.TrimSpace(message)
}

func boundedLifecycleMessage(message string, limit int) string {
	runes := []rune(strings.TrimSpace(message))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + " [truncated]"
}
