package store

import (
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

const maximumOperationReviewNoteLength = 1024

func validOperationKind(kind model.OperationKind) bool {
	switch kind {
	case model.OperationSwitchover, model.OperationFailover, model.OperationNodeSync, model.OperationMetadataReconciliation,
		model.OperationFormerPrimaryRejoin, model.OperationReplicationRepair, model.OperationPowerShutdown:
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

func activeOperationStatus(status model.OperationStatus) bool {
	return status == model.OperationRunning
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

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
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
	activeOperations := 0
	for _, existing := range repository.snapshot.Operations {
		if activeOperationStatus(existing.Status) {
			activeOperations++
		}
	}
	if activeOperations >= maximumActiveOperations {
		return model.OperationRecord{}, false, validationError("active operation capacity is exhausted")
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
	if err := repository.commitSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), false, err
	}
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

type OperationTimeline struct {
	Operation model.OperationRecord
	Audits    []model.AuditEvent
	Reports   []model.Report
}

func (repository *Repository) OperationTimeline(resourceID model.ResourceID) (OperationTimeline, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return OperationTimeline{}, false
	}
	timeline := OperationTimeline{Operation: cloneOperationRecord(operation), Audits: []model.AuditEvent{}, Reports: []model.Report{}}
	for _, event := range repository.snapshot.Audits {
		if event.OperationID == resourceID {
			timeline.Audits = append(timeline.Audits, redactAudit(event))
		}
	}
	for _, report := range repository.snapshot.Reports {
		if report.OperationID == resourceID {
			timeline.Reports = append(timeline.Reports, redactReport(report))
		}
	}
	return timeline, true
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
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
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
	if err := repository.commitSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
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

func verificationReconciliationAllowed(operation model.OperationRecord, transition model.OperationTransition) bool {
	if operation.Status != model.OperationIndeterminate || transition.Verification == nil {
		return false
	}
	if transition.Status != model.OperationIndeterminate && transition.Status != model.OperationSucceeded {
		return false
	}
	if transition.Status == model.OperationSucceeded && !transition.Verification.Passed {
		return false
	}
	if transition.Observation != "" || transition.Attempt != nil {
		return false
	}
	if transition.Stage != "" && transition.Stage != operation.Stage && transition.Stage != model.StageReport {
		return false
	}
	return true
}

func applyOperationTransition(operation model.OperationRecord, transition model.OperationTransition, now time.Time) (model.OperationRecord, error) {
	reconcilingVerification := verificationReconciliationAllowed(operation, transition)
	if terminalOperationStatus(operation.Status) && !reconcilingVerification {
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
	if transition.Precheck != nil {
		operation.Precheck = append([]model.Check{}, transition.Precheck...)
		for i := range operation.Precheck {
			operation.Precheck[i].Message = redact.Text(operation.Precheck[i].Message)
		}
	}
	if transition.Attempt != nil {
		if err := validateAttempt(operation.Attempts, *transition.Attempt); err != nil {
			return model.OperationRecord{}, err
		}
		attempt := *transition.Attempt
		attempt.Message = redact.Text(attempt.Message)
		operation.Attempts = append(operation.Attempts, attempt)
	}
	if transition.Execution != nil {
		operation.Execution = *transition.Execution
		operation.Execution.OperationID = operation.ResourceID
		operation.Execution.Message = redact.Text(operation.Execution.Message)
	}
	if transition.Verification != nil {
		operation.Verification = *transition.Verification
		operation.Verification.OperationID = operation.ResourceID
		operation.Verification.Checks = append([]model.Check{}, operation.Verification.Checks...)
		for i := range operation.Verification.Checks {
			operation.Verification.Checks[i].Message = redact.Text(operation.Verification.Checks[i].Message)
		}
	}
	operation.FailureClass = strings.TrimSpace(transition.FailureClass)
	operation.Message = redact.Text(strings.TrimSpace(transition.Message))
	operation.MetadataRevision++
	operation.UpdatedAt = now.UTC()
	operation.Operation.MetadataRevision = operation.MetadataRevision
	operation.Operation.UpdatedAt = operation.UpdatedAt
	return operation, nil
}

func (repository *Repository) TransitionOperation(resourceID model.ResourceID, expectedRevision uint64, transition model.OperationTransition) (model.OperationRecord, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, validationError("operation does not exist")
	}
	if operation.MetadataRevision != expectedRevision {
		return model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	operation, err := applyOperationTransition(operation, transition, repository.now())
	if err != nil {
		return model.OperationRecord{}, err
	}

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.Operations[resourceID] = cloneOperationRecord(operation)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
	return cloneOperationRecord(operation), nil
}

func normalizeOperationReviewInput(reviewedBy, note string) (string, string, error) {
	reviewedBy = strings.TrimSpace(reviewedBy)
	note = strings.TrimSpace(note)
	if reviewedBy == "" || len(reviewedBy) > maximumAuditActorLength {
		return "", "", validationError("operation review actor is invalid")
	}
	if note == "" || len(note) > maximumOperationReviewNoteLength {
		return "", "", validationError("operation review note is required and must not exceed %d characters", maximumOperationReviewNoteLength)
	}
	return reviewedBy, note, nil
}

func validatePersistedOperationReview(operation model.OperationRecord) error {
	if operation.Review == nil {
		return nil
	}
	review := operation.Review
	if (operation.Status != model.OperationIndeterminate && operation.Status != model.OperationSucceeded) || review.ReviewedAt.IsZero() ||
		review.Disposition != model.OperationReviewAcknowledgedIndeterminate {
		return validationError("operation review is inconsistent with the operation outcome")
	}
	_, _, err := normalizeOperationReviewInput(review.ReviewedBy, review.Note)
	return err
}

// ReviewIndeterminateOperation acknowledges an unresolved historical outcome
// without rewriting it as successful or deleting its original evidence.
func (repository *Repository) ReviewIndeterminateOperation(resourceID model.ResourceID, expectedRevision uint64, reviewedBy, note string) (model.OperationRecord, error) {
	reviewedBy, note, err := normalizeOperationReviewInput(reviewedBy, note)
	if err != nil {
		return model.OperationRecord{}, err
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, notFoundError("operation does not exist")
	}
	if operation.Review != nil {
		if operation.Review.ReviewedBy == reviewedBy && operation.Review.Note == note &&
			operation.Review.Disposition == model.OperationReviewAcknowledgedIndeterminate {
			return cloneOperationRecord(operation), nil
		}
		return model.OperationRecord{}, conflictError("operation review is immutable")
	}
	if operation.MetadataRevision != expectedRevision {
		return model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	if operation.Status != model.OperationIndeterminate {
		return model.OperationRecord{}, conflictError("only an indeterminate operation can be acknowledged")
	}

	now := repository.now().UTC()
	operation.Review = &model.OperationReview{
		ReviewedAt: now, ReviewedBy: reviewedBy,
		Disposition: model.OperationReviewAcknowledgedIndeterminate, Note: note,
	}
	operation.MetadataRevision++
	operation.UpdatedAt = now
	operation.Operation.MetadataRevision = operation.MetadataRevision
	operation.Operation.UpdatedAt = now

	audit, err := normalizeFinalAudit(model.AuditEvent{
		OperationID: operation.ResourceID,
		Stage:       model.StageReport,
		Actor:       reviewedBy,
		Message:     "indeterminate outcome acknowledged after operator review: " + note,
	}, operation.ResourceID, now)
	if err != nil {
		return model.OperationRecord{}, err
	}
	report := model.Report{
		OperationID: operation.ResourceID,
		Title:       "indeterminate operation review",
		Status:      model.OperationIndeterminate,
		Summary:     "operator acknowledged the unresolved outcome: " + note,
	}

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.Operations[resourceID] = cloneOperationRecord(operation)
	next.Audits = append(append([]model.AuditEvent{}, repository.snapshot.Audits...), audit)
	next.Reports = append([]model.Report{}, repository.snapshot.Reports...)
	next.Reports, err = upsertFinalReport(next.Reports, report, operation.ResourceID, now)
	if err != nil {
		return model.OperationRecord{}, err
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
	return cloneOperationRecord(operation), nil
}

func normalizeFinalAudit(event model.AuditEvent, operationID model.ResourceID, now time.Time) (model.AuditEvent, error) {
	event = redactAudit(event)
	if event.OperationID != "" && event.OperationID != operationID {
		return model.AuditEvent{}, validationError("audit operation ID does not match operation")
	}
	event.OperationID = operationID
	if err := validateAuditText(event); err != nil {
		return model.AuditEvent{}, err
	}
	if event.ResourceID == "" {
		event.ResourceID = model.NewResourceID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	event.MetadataRevision = 1
	return event, nil
}

func upsertFinalReport(reports []model.Report, report model.Report, operationID model.ResourceID, now time.Time) ([]model.Report, error) {
	report = redactReport(report)
	if !terminalReportStatus(report.Status) {
		return nil, validationError("report status must be terminal")
	}
	if report.OperationID != "" && report.OperationID != operationID {
		return nil, validationError("report operation ID does not match operation")
	}
	if err := validateReportText(report); err != nil {
		return nil, err
	}
	report.OperationID = operationID
	if report.ResourceID == "" {
		report.ResourceID = model.NewResourceID()
	}
	for index, existing := range reports {
		if existing.ResourceID != report.ResourceID {
			continue
		}
		if existing.OperationID != operationID {
			return nil, conflictError("report resource belongs to another operation")
		}
		report.CreatedAt = existing.CreatedAt
		report.UpdatedAt = now
		report.MetadataRevision = existing.MetadataRevision + 1
		reports[index] = report
		return reports, nil
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}
	report.UpdatedAt = now
	report.MetadataRevision = 1
	return append(reports, report), nil
}

func (repository *Repository) finalizeOperationLocked(resourceID model.ResourceID, expectedRevision uint64, transition model.OperationTransition, audits []model.AuditEvent, reports []model.Report) (model.OperationRecord, error) {
	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, validationError("operation does not exist")
	}
	if operation.MetadataRevision != expectedRevision {
		return model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	now := repository.now().UTC()
	operation, err := applyOperationTransition(operation, transition, now)
	if err != nil {
		return model.OperationRecord{}, err
	}

	next := repository.snapshot
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.Operations[resourceID] = cloneOperationRecord(operation)
	next.Audits = append([]model.AuditEvent{}, repository.snapshot.Audits...)
	for _, event := range audits {
		event, err = normalizeFinalAudit(event, resourceID, now)
		if err != nil {
			return model.OperationRecord{}, err
		}
		next.Audits = append(next.Audits, event)
	}
	next.Reports = append([]model.Report{}, repository.snapshot.Reports...)
	for _, report := range reports {
		next.Reports, err = upsertFinalReport(next.Reports, report, resourceID, now)
		if err != nil {
			return model.OperationRecord{}, err
		}
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return cloneOperationRecord(operation), err
	}
	return cloneOperationRecord(operation), nil
}

// FinalizeOperation publishes the terminal operation, its audit events, and its
// reports as one repository snapshot so readers cannot observe a partial result.
func (repository *Repository) FinalizeOperation(resourceID model.ResourceID, expectedRevision uint64, transition model.OperationTransition, audits []model.AuditEvent, reports []model.Report) (model.OperationRecord, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.finalizeOperationLocked(resourceID, expectedRevision, transition, audits, reports)
}

// FinalizeAbandonedOperation atomically proves that a running operation is
// stale and does not own an unexpired distributed lease before publishing its
// terminal timeline. A concurrent progress update or lease acquisition makes
// the candidate ineligible instead of allowing recovery to race the executor.
func (repository *Repository) FinalizeAbandonedOperation(
	resourceID model.ResourceID,
	expectedRevision uint64,
	observedAt time.Time,
	staleBefore time.Time,
	transition model.OperationTransition,
	audits []model.AuditEvent,
	reports []model.Report,
) (model.OperationRecord, bool, error) {
	observedAt = observedAt.UTC()
	staleBefore = staleBefore.UTC()
	if observedAt.IsZero() || staleBefore.IsZero() || staleBefore.After(observedAt) {
		return model.OperationRecord{}, false, validationError("abandoned operation recovery window is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	operation, found := repository.snapshot.Operations[resourceID]
	if !found {
		return model.OperationRecord{}, false, validationError("operation does not exist")
	}
	if operation.MetadataRevision != expectedRevision || operation.Status != model.OperationRunning ||
		operation.UpdatedAt.IsZero() || operation.UpdatedAt.After(staleBefore) {
		return cloneOperationRecord(operation), false, nil
	}
	for _, record := range repository.snapshot.OperationLocks {
		if record.OperationID == resourceID && record.ExpiresAt.After(observedAt) {
			return cloneOperationRecord(operation), false, nil
		}
	}
	finalized, err := repository.finalizeOperationLocked(resourceID, expectedRevision, transition, audits, reports)
	if err != nil {
		return finalized, false, err
	}
	return finalized, true, nil
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
