package store

import (
	"crypto/subtle"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type ConsumeApprovalGrantRequest struct {
	GrantID                   model.ResourceID
	TokenHash                 string
	OperationID               model.ResourceID
	ExpectedOperationRevision uint64
	Now                       time.Time
	Transition                model.OperationTransition
}

func validApprovalGrantStatus(status model.ApprovalGrantStatus) bool {
	switch status {
	case model.ApprovalGrantActive, model.ApprovalGrantConsumed, model.ApprovalGrantExpired, model.ApprovalGrantRevoked:
		return true
	default:
		return false
	}
}

func validateApprovalGrant(grant model.ApprovalGrant) error {
	if !model.ValidResourceID(grant.ResourceID) {
		return validationError("approval grant ID is invalid")
	}
	if grant.MetadataRevision == 0 {
		return validationError("approval grant metadata revision is required")
	}
	if strings.TrimSpace(grant.TokenHash) == "" {
		return validationError("approval grant token hash is required")
	}
	if !model.ValidResourceID(grant.OperationID) || !model.ValidResourceID(grant.ClusterID) || !model.ValidResourceID(grant.TargetID) {
		return validationError("approval grant operation, cluster, and target IDs are required")
	}
	if !grant.Engine.Valid() || !validOperationKind(grant.OperationKind) {
		return validationError("approval grant engine or operation kind is invalid")
	}
	if strings.TrimSpace(grant.PlanDigest) == "" || strings.TrimSpace(grant.ObservationDigest) == "" {
		return validationError("approval grant plan and observation digests are required")
	}
	if strings.TrimSpace(grant.IssuedBy) == "" || grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) {
		return validationError("approval grant issuer and validity window are required")
	}
	if !validApprovalGrantStatus(grant.Status) {
		return validationError("approval grant status is invalid")
	}
	if grant.Status == model.ApprovalGrantConsumed {
		if grant.ConsumedAt.IsZero() || !model.ValidResourceID(grant.ConsumedByOperationID) {
			return validationError("consumed approval grant metadata is incomplete")
		}
	} else if !grant.ConsumedAt.IsZero() || grant.ConsumedByOperationID != "" {
		return validationError("unconsumed approval grant contains consumption metadata")
	}
	return nil
}

func (repository *Repository) PutApprovalGrant(grant model.ApprovalGrant) error {
	if err := validateApprovalGrant(grant); err != nil {
		return err
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, found := repository.snapshot.ApprovalGrants[grant.ResourceID]; found {
		return conflictError("approval grant already exists")
	}
	next := repository.snapshot
	next.ApprovalGrants = cloneApprovalGrantMap(repository.snapshot.ApprovalGrants)
	next.ApprovalGrants[grant.ResourceID] = grant
	return repository.commitSnapshotLocked(next)
}

func (repository *Repository) ApprovalGrant(resourceID model.ResourceID) (model.ApprovalGrant, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	grant, found := repository.snapshot.ApprovalGrants[resourceID]
	return grant, found
}

func (repository *Repository) ApprovalGrants() []model.ApprovalGrant {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	grants := make([]model.ApprovalGrant, 0, len(repository.snapshot.ApprovalGrants))
	for _, grant := range repository.snapshot.ApprovalGrants {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(left, right int) bool {
		if grants[left].IssuedAt.Equal(grants[right].IssuedAt) {
			return grants[left].ResourceID < grants[right].ResourceID
		}
		return grants[left].IssuedAt.Before(grants[right].IssuedAt)
	})
	return grants
}

func (repository *Repository) ConsumeApprovalGrant(request ConsumeApprovalGrantRequest) (model.ApprovalGrant, model.OperationRecord, error) {
	if !model.ValidResourceID(request.GrantID) || !model.ValidResourceID(request.OperationID) || request.ExpectedOperationRevision == 0 {
		return model.ApprovalGrant{}, model.OperationRecord{}, validationError("approval consumption identifiers are invalid")
	}
	request.TokenHash = strings.TrimSpace(request.TokenHash)
	if request.TokenHash == "" {
		return model.ApprovalGrant{}, model.OperationRecord{}, validationError("approval token hash is required")
	}
	if request.Transition.Stage != model.StageApprove {
		return model.ApprovalGrant{}, model.OperationRecord{}, validationError("approval consumption must advance the approve stage")
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	grant, found := repository.snapshot.ApprovalGrants[request.GrantID]
	if !found {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval grant does not exist")
	}
	operation, found := repository.snapshot.Operations[request.OperationID]
	if !found {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval operation does not exist")
	}
	if operation.MetadataRevision != request.ExpectedOperationRevision {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("operation metadata revision changed")
	}
	if grant.Status != model.ApprovalGrantActive {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval grant is not active")
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = repository.now().UTC()
	}
	if !now.Before(grant.ExpiresAt) {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval grant has expired")
	}
	if subtle.ConstantTimeCompare([]byte(grant.TokenHash), []byte(request.TokenHash)) != 1 {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval grant token hash does not match")
	}
	if grant.OperationID != operation.ResourceID ||
		grant.ClusterID != operation.Operation.ClusterID ||
		grant.Engine != operation.Operation.Engine ||
		grant.OperationKind != operation.Operation.Kind ||
		grant.TargetID != operation.TargetID ||
		grant.PlanDigest != operation.Plan.Digest ||
		grant.ObservationDigest != operation.Observation {
		return model.ApprovalGrant{}, model.OperationRecord{}, conflictError("approval grant scope does not match operation")
	}

	approved, err := applyOperationTransition(operation, request.Transition, now)
	if err != nil {
		return model.ApprovalGrant{}, model.OperationRecord{}, err
	}
	grant.Status = model.ApprovalGrantConsumed
	grant.ConsumedAt = now
	grant.ConsumedByOperationID = operation.ResourceID
	grant.MetadataRevision++
	grant.UpdatedAt = now

	next := repository.snapshot
	next.ApprovalGrants = cloneApprovalGrantMap(repository.snapshot.ApprovalGrants)
	next.Operations = cloneOperationMap(repository.snapshot.Operations)
	next.ApprovalGrants[grant.ResourceID] = grant
	next.Operations[approved.ResourceID] = cloneOperationRecord(approved)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return grant, cloneOperationRecord(approved), err
	}
	return grant, cloneOperationRecord(approved), nil
}
