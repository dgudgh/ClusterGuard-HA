package store

import (
	"errors"
	"sort"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func (repository *Repository) PutLifecycleTask(task lifecycle.Task) (lifecycle.Task, error) {
	if !model.ValidResourceID(task.ResourceID) || !model.ValidResourceID(task.ClusterID) || !task.Status.Valid() {
		return lifecycle.Task{}, validationError("lifecycle task identity or status is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	now := repository.now().UTC()
	existing, existed := repository.snapshot.LifecycleTasks[task.ResourceID]
	if existed {
		if existing.ClusterID != task.ClusterID {
			return lifecycle.Task{}, conflictError("lifecycle task cluster is immutable")
		}
		task.CreatedAt = existing.CreatedAt
		task.MetadataRevision = existing.MetadataRevision + 1
	} else {
		task.CreatedAt = now
		task.MetadataRevision = 1
	}
	task.UpdatedAt = now
	next := repository.snapshot
	next.LifecycleTasks = cloneLifecycleTaskMap(repository.snapshot.LifecycleTasks)
	next.LifecycleTasks[task.ResourceID] = cloneLifecycleTask(task)
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneLifecycleTask(task), err
		}
		return lifecycle.Task{}, err
	}
	return cloneLifecycleTask(task), nil
}

func (repository *Repository) LifecycleTask(resourceID model.ResourceID) (lifecycle.Task, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	task, found := repository.snapshot.LifecycleTasks[resourceID]
	return cloneLifecycleTask(task), found
}

func (repository *Repository) LifecycleTaskByOperationID(operationID model.ResourceID) (lifecycle.Task, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, task := range repository.snapshot.LifecycleTasks {
		if task.OperationID == operationID {
			return cloneLifecycleTask(task), true
		}
	}
	return lifecycle.Task{}, false
}

func (repository *Repository) LifecycleTasks() []lifecycle.Task {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	tasks := make([]lifecycle.Task, 0, len(repository.snapshot.LifecycleTasks))
	for _, task := range repository.snapshot.LifecycleTasks {
		tasks = append(tasks, cloneLifecycleTask(task))
	}
	sort.Slice(tasks, func(i, j int) bool {
		if !tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		}
		return tasks[i].ResourceID > tasks[j].ResourceID
	})
	return tasks
}
