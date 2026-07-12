package store

import (
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

func validOperationKind(kind model.OperationKind) bool {
	switch kind {
	case model.OperationSwitchover, model.OperationFailover, model.OperationNodeSync, model.OperationMetadataReconciliation:
		return true
	default:
		return false
	}
}

func validOperationStatus(status model.OperationStatus) bool {
	switch status {
	case model.OperationPlanned, model.OperationBlocked, model.OperationRunning, model.OperationSucceeded,
		model.OperationFailed, model.OperationIndeterminate, model.OperationUnsupported:
		return true
	default:
		return false
	}
}

func terminalOperationStatus(status model.OperationStatus) bool {
	switch status {
	case model.OperationBlocked, model.OperationSucceeded, model.OperationFailed, model.OperationIndeterminate, model.OperationUnsupported:
		return true
	default:
		return false
	}
}

func workflowStageOrder(stage model.WorkflowStage) int {
	switch stage {
	case model.StageDiscover:
		return 1
	case model.StagePrecheck:
		return 2
	case model.StagePlan:
		return 3
	case model.StageSafetyGuard:
		return 4
	case model.StageLock:
		return 5
	case model.StageApprove:
		return 6
	case model.StageExecute:
		return 7
	case model.StageVerify:
		return 8
	case model.StageAudit:
		return 9
	case model.StageReport:
		return 10
	default:
		return 0
	}
}

func operationIntentMatches(existing model.OperationRecord, requested model.OperationRecord) bool {
	return existing.Operation.ClusterID == requested.Operation.ClusterID &&
		existing.Operation.Engine == requested.Operation.Engine &&
		existing.Operation.Kind == requested.Operation.Kind &&
		existing.TargetID == requested.TargetID
}

func validateOperationRequest(operation model.OperationRecord) error {
	if !model.ValidResourceID(operation.Operation.ClusterID) {
		return validationError("operation cluster ID is invalid")
	}
	if !model.ValidResourceID(operation.TargetID) {
		return validationError("operation target ID is invalid")
	}
	if !operation.Operation.Engine.Valid() {
		return validationError("operation engine is invalid")
	}
	if !validOperationKind(operation.Operation.Kind) {
		return validationError("operation kind is invalid")
	}
	if strings.TrimSpace(operation.IdempotencyKey) == "" {
		return validationError("operation idempotency key is required")
	}
	return nil
}

func (repository *Repository) CreateOperation(operation model.OperationRecord) (model.OperationRecord, bool, error) {
	operation.IdempotencyKey = strings.TrimSpace(operation.IdempotencyKey)
	if err := validateOperationRequest(operation); err != nil {
		return model.OperationRecord{}, false, err
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if resourceID, found := repository.snapshot.OperationKeys[operation.IdempotencyKey]; found {
		existing, exists := repository.snapshot.Operations[resourceID]
		if !exists {
			return model.OperationRecord{}, false, conflictError("operation idempotency index is corrupt")
		}
		if !operationIntentMatches(existing, operation) {
			return model.OperationRecord{}, false, conflictError("operation idempotency key is already used by another intent")
		}
		return cloneOperationRecord(existing), true, nil
	}

	now := repository.now().UTC()
	operation.ResourceID = model.NewResourceID()
	operation.MetadataRevision = 1
	operation.CreatedAt = now
	operation.UpdatedAt = now
	operation.Operation.ResourceID = operation.ResourceID
	operation.Operation.MetadataRevision = 1
	operation.Operation.CreatedAt = now
	operation.Operation.UpdatedAt = now
	operation.Operation.Status = model.OperationPlanned
	operation.Stage = model.StageDiscover
	operation.Status = model.OperationPlanned
	operation.Attempts = []model.StepAttempt{}

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.OperationKeys = cloneOperationKeyMap(repository.snapshot.OperationKeys)
	next.Operations[operation.ResourceID] = cloneOperationRecord(operation)
	next.OperationKeys[operation.IdempotencyKey] = operation.ResourceID
	if err := repository.persistSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), false, err
	}
	repository.snapshot = next
	return cloneOperationRecord(operation), false, nil
}

func (repository *Repository) Operation(resourceID model.ResourceID) (model.OperationRecord, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	operation, found := repository.snapshot.Operations[resourceID]
	return cloneOperationRecord(operation), found
}

func (repository *Repository) OperationByIdempotencyKey(key string) (model.OperationRecord, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	resourceID, found := repository.snapshot.OperationKeys[strings.TrimSpace(key)]
	if !found {
		return model.OperationRecord{}, false
	}
	operation, found := repository.snapshot.Operations[resourceID]
	return cloneOperationRecord(operation), found
}

func validateOperationPlan(operation model.OperationRecord, plan model.OperationPlan) error {
	if plan.OperationID != operation.ResourceID {
		return validationError("operation plan ID does not match operation")
	}
	if plan.ClusterID != operation.Operation.ClusterID {
		return validationError("operation plan cluster does not match operation")
	}
	if !model.ValidResourceID(plan.SourceID) {
		return validationError("operation plan source ID is invalid")
	}
	if plan.TargetID != operation.TargetID {
		return validationError("operation plan target does not match operation")
	}
	if strings.TrimSpace(plan.ObservationToken) == "" {
		return validationError("operation plan observation token is required")
	}
	if strings.TrimSpace(plan.Digest) == "" {
		return validationError("operation plan digest is required")
	}
	if len(plan.ResourceRevisions) == 0 {
		return validationError("operation plan resource revisions are required")
	}
	for resourceID, revision := range plan.ResourceRevisions {
		if !model.ValidResourceID(resourceID) || revision == 0 {
			return validationError("operation plan contains an invalid resource revision")
		}
	}
	if len(plan.Steps) == 0 {
		return validationError("operation plan steps are required")
	}
	for index, step := range plan.Steps {
		if step.Index != index+1 || strings.TrimSpace(step.Name) == "" || strings.TrimSpace(step.Owner) == "" {
			return validationError("operation plan steps must be ordered and named")
		}
	}
	return nil
}

func (repository *Repository) PutOperationPlan(resourceID model.ResourceID, expectedRevision uint64, plan model.OperationPlan) (model.OperationRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, validationError("operation does not exist")
	}
	if operation.MetadataRevision != expectedRevision {
		return model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	if terminalOperationStatus(operation.Status) {
		return model.OperationRecord{}, conflictError("terminal operation cannot accept a plan")
	}
	if err := validateOperationPlan(operation, plan); err != nil {
		return model.OperationRecord{}, err
	}
	if operation.Plan.Digest != "" {
		if operation.Plan.Digest != plan.Digest {
			return model.OperationRecord{}, conflictError("operation plan is immutable")
		}
		return cloneOperationRecord(operation), nil
	}

	now := repository.now().UTC()
	plan.ResourceID = model.NewResourceID()
	plan.MetadataRevision = 1
	plan.CreatedAt = now
	plan.UpdatedAt = now
	plan.Stage = model.StagePlan
	operation.Plan = cloneOperationPlan(plan)
	operation.Stage = model.StagePlan
	operation.MetadataRevision++
	operation.UpdatedAt = now
	operation.Operation.MetadataRevision = operation.MetadataRevision
	operation.Operation.UpdatedAt = now

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.Operations[resourceID] = cloneOperationRecord(operation)
	if err := repository.persistSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
	repository.snapshot = next
	return cloneOperationRecord(operation), nil
}

func validateAttempt(existing []model.StepAttempt, attempt model.StepAttempt) error {
	if strings.TrimSpace(attempt.Step) == "" || attempt.Attempt == 0 || !validOperationStatus(attempt.Status) || attempt.StartedAt.IsZero() {
		return validationError("operation step attempt is invalid")
	}
	var maximum uint64
	for _, candidate := range existing {
		if candidate.Step == attempt.Step && candidate.Attempt > maximum {
			maximum = candidate.Attempt
		}
	}
	if attempt.Attempt <= maximum {
		return conflictError("operation step attempt is not monotonic")
	}
	return nil
}

func (repository *Repository) TransitionOperation(resourceID model.ResourceID, expectedRevision uint64, transition model.OperationTransition) (model.OperationRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, validationError("operation does not exist")
	}
	if operation.MetadataRevision != expectedRevision {
		return model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	if terminalOperationStatus(operation.Status) {
		return model.OperationRecord{}, conflictError("terminal operation is immutable")
	}
	if transition.Stage != "" {
		currentOrder := workflowStageOrder(operation.Stage)
		nextOrder := workflowStageOrder(transition.Stage)
		if nextOrder == 0 || nextOrder < currentOrder {
			return model.OperationRecord{}, conflictError("operation stage transition is invalid")
		}
		operation.Stage = transition.Stage
	}
	if transition.Status != "" {
		if !validOperationStatus(transition.Status) {
			return model.OperationRecord{}, validationError("operation status is invalid")
		}
		operation.Status = transition.Status
		operation.Operation.Status = transition.Status
	}
	if transition.Observation != "" {
		if operation.Observation != "" && operation.Observation != transition.Observation {
			return model.OperationRecord{}, conflictError("operation observation token is immutable")
		}
		operation.Observation = transition.Observation
	}
	if transition.Attempt != nil {
		if err := validateAttempt(operation.Attempts, *transition.Attempt); err != nil {
			return model.OperationRecord{}, err
		}
		operation.Attempts = append(operation.Attempts, *transition.Attempt)
	}
	if transition.Execution != nil {
		operation.Execution = *transition.Execution
		operation.Execution.OperationID = operation.ResourceID
	}
	if transition.Verification != nil {
		operation.Verification = *transition.Verification
		operation.Verification.OperationID = operation.ResourceID
	}
	operation.FailureClass = strings.TrimSpace(transition.FailureClass)
	operation.Message = strings.TrimSpace(transition.Message)
	operation.MetadataRevision++
	operation.UpdatedAt = repository.now().UTC()
	operation.Operation.MetadataRevision = operation.MetadataRevision
	operation.Operation.UpdatedAt = operation.UpdatedAt

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.Operations[resourceID] = cloneOperationRecord(operation)
	if err := repository.persistSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
	repository.snapshot = next
	return cloneOperationRecord(operation), nil
}

func (repository *Repository) Operations(clusterID model.ResourceID) []model.OperationRecord {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.OperationRecord, 0)
	for _, operation := range repository.snapshot.Operations {
		if clusterID == "" || operation.Operation.ClusterID == clusterID {
			result = append(result, cloneOperationRecord(operation))
		}
	}
	return result
}

func operationSummary(operation model.OperationRecord) string {
	return fmt.Sprintf("%s %s", operation.Operation.Engine, operation.Operation.Kind)
}
