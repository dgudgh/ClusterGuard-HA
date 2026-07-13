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

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type ClusterLocker interface {
	AcquireCluster(context.Context, model.ResourceID) (func(), error)
}

type Executor interface {
	Execute(context.Context, Request, ExecutionSecrets, func(Event)) (ExecutionResult, error)
}

type MetadataCommitter interface {
	Commit(context.Context, Task, ExecutionResult) error
}

type Manager struct {
	store     TaskStore
	authority MutationAuthority
	locks     ClusterLocker
	executor  Executor
	committer MetadataCommitter
	now       func() time.Time
}

func NewManager(store TaskStore, authority MutationAuthority, locks ClusterLocker, executor Executor, committer MetadataCommitter, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{store: store, authority: authority, locks: locks, executor: executor, committer: committer, now: now}
}

func (manager *Manager) configured() bool {
	return manager != nil && manager.store != nil && manager.authority != nil && manager.locks != nil && manager.executor != nil && manager.committer != nil
}

func (manager *Manager) Execute(ctx context.Context, request Request, plan Plan, secrets ExecutionSecrets) (Task, error) {
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
		ClusterID:    request.ClusterID, Request: request, Plan: plan, Status: TaskQueued,
	}
	var err error
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return Task{}, fmt.Errorf("persist queued lifecycle task: %w", err)
	}
	interrupt := func(cause error) (Task, error) {
		task.Status = TaskInterrupted
		task.Message = "lifecycle execution lost leader-backed mutation authority"
		task, _ = manager.store.PutLifecycleTask(task)
		return task, cause
	}
	if err := manager.authority.RequireMutationAuthority(ctx); err != nil {
		return interrupt(err)
	}
	release, err := manager.locks.AcquireCluster(ctx, request.ClusterID)
	if err != nil {
		return interrupt(err)
	}
	defer release()
	task.Status = TaskRunning
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return task, fmt.Errorf("persist running lifecycle task: %w", err)
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

	result, executionErr := manager.executor.Execute(ctx, request, secrets, emit)
	secrets = ExecutionSecrets{}
	if eventPersistenceError != nil {
		executionErr = errors.Join(executionErr, eventPersistenceError)
	}
	if executionErr != nil {
		task.Status = TaskFailed
		task.Message = "lifecycle execution failed"
		task, _ = manager.store.PutLifecycleTask(task)
		return task, executionErr
	}
	if err := manager.authority.RequireMutationAuthority(ctx); err != nil {
		return interrupt(err)
	}
	task.Status = TaskVerifying
	task.CurrentStage = StageVerify
	task.Checks = append([]model.Check{}, result.Checks...)
	task.Message = redactLifecycleMessage(result.Message, redactionSecrets)
	if task, err = manager.store.PutLifecycleTask(task); err != nil {
		return task, fmt.Errorf("persist lifecycle verification: %w", err)
	}
	if !result.Verified {
		task.Status = TaskFailed
		task.Message = "lifecycle verification failed; metadata was not changed"
		task, _ = manager.store.PutLifecycleTask(task)
		return task, fmt.Errorf("lifecycle verification failed")
	}
	if err := manager.committer.Commit(ctx, task, result); err != nil {
		task.Status = TaskFailed
		task.Message = "verified lifecycle metadata commit failed"
		task, _ = manager.store.PutLifecycleTask(task)
		return task, err
	}
	task.Status = TaskSucceeded
	task.CurrentStage = StageCommit
	task.Message = "lifecycle execution verified and metadata committed"
	task, err = manager.store.PutLifecycleTask(task)
	return task, err
}

func redactLifecycleMessage(message string, secrets ExecutionSecrets) string {
	for _, secret := range []string{secrets.SSHPassword, secrets.MySQLRootPassword, secrets.ReplicationPassword} {
		if strings.TrimSpace(secret) != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return strings.TrimSpace(message)
}
