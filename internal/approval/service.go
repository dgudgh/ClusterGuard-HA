package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

const (
	approvalSecretBytes = 32
	defaultTTL          = 5 * time.Minute
	maximumTTL          = 15 * time.Minute
	tokenPrefix         = "cgag_"
)

var (
	ErrRequired  = errors.New("approval grant is required")
	ErrInvalid   = errors.New("approval grant is invalid")
	ErrExpired   = errors.New("approval grant has expired")
	ErrConsumed  = errors.New("approval grant has already been consumed")
	ErrMismatch  = errors.New("approval grant does not match this operation")
	ErrStalePlan = errors.New("approval grant plan is stale")
)

type Service struct {
	store  *store.Repository
	random io.Reader
	now    func() time.Time
}

type IssueRequest struct {
	Operation model.OperationRecord
	IssuedBy  string
	TTL       time.Duration
}

type IssuedGrant struct {
	Grant model.ApprovalGrant `json:"grant"`
	Token string              `json:"approval_token"`
}

func New(repository *store.Repository, random io.Reader, now func() time.Time) *Service {
	if random == nil {
		random = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: repository, random: random, now: now}
}

func tokenHash(secret []byte) string {
	digest := sha256.Sum256(secret)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func ParseToken(token string) (model.ResourceID, []byte, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil, ErrRequired
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return "", nil, ErrInvalid
	}
	grantPart, secretPart, found := strings.Cut(strings.TrimPrefix(token, tokenPrefix), ".")
	grantID := model.ResourceID(grantPart)
	if !found || !model.ValidResourceID(grantID) || secretPart == "" {
		return "", nil, ErrInvalid
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretPart)
	if err != nil || len(secret) != approvalSecretBytes {
		return "", nil, ErrInvalid
	}
	return grantID, secret, nil
}

func publicGrant(grant model.ApprovalGrant) model.ApprovalGrant {
	grant.TokenHash = ""
	return grant
}

func validPlannedOperation(record model.OperationRecord) bool {
	return model.ValidResourceID(record.ResourceID) &&
		model.ValidResourceID(record.Operation.ClusterID) &&
		model.ValidResourceID(record.TargetID) &&
		record.Operation.Engine.Valid() &&
		record.Operation.Kind != "" &&
		strings.TrimSpace(record.Observation) != "" &&
		strings.TrimSpace(record.Plan.Digest) != "" &&
		record.Plan.OperationID == record.ResourceID &&
		record.Plan.TargetID == record.TargetID
}

func (service *Service) Issue(ctx context.Context, request IssueRequest) (IssuedGrant, error) {
	if err := ctx.Err(); err != nil {
		return IssuedGrant{}, err
	}
	if service == nil || service.store == nil || service.random == nil || service.now == nil {
		return IssuedGrant{}, fmt.Errorf("approval service is not configured")
	}
	request.IssuedBy = strings.TrimSpace(request.IssuedBy)
	if request.IssuedBy == "" {
		return IssuedGrant{}, fmt.Errorf("approval issuer is required")
	}
	if !validPlannedOperation(request.Operation) {
		return IssuedGrant{}, fmt.Errorf("a durable planned operation is required")
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < 0 || ttl > maximumTTL {
		return IssuedGrant{}, fmt.Errorf("approval grant TTL must be between 1ns and %s", maximumTTL)
	}
	secret := make([]byte, approvalSecretBytes)
	if _, err := io.ReadFull(service.random, secret); err != nil {
		return IssuedGrant{}, fmt.Errorf("generate approval secret: %w", err)
	}
	now := service.now().UTC()
	grant := model.ApprovalGrant{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		TokenHash:         tokenHash(secret),
		OperationID:       request.Operation.ResourceID,
		ClusterID:         request.Operation.Operation.ClusterID,
		Engine:            request.Operation.Operation.Engine,
		OperationKind:     request.Operation.Operation.Kind,
		TargetID:          request.Operation.TargetID,
		PlanDigest:        request.Operation.Plan.Digest,
		ObservationDigest: request.Operation.Observation,
		IssuedBy:          request.IssuedBy,
		IssuedAt:          now,
		ExpiresAt:         now.Add(ttl),
		Status:            model.ApprovalGrantActive,
	}
	if err := service.store.PutApprovalGrant(grant); err != nil {
		return IssuedGrant{}, err
	}
	token := tokenPrefix + string(grant.ResourceID) + "." + base64.RawURLEncoding.EncodeToString(secret)
	return IssuedGrant{Grant: publicGrant(grant), Token: token}, nil
}

func (service *Service) loadToken(ctx context.Context, token string) (model.ApprovalGrant, string, error) {
	if err := ctx.Err(); err != nil {
		return model.ApprovalGrant{}, "", err
	}
	if service == nil || service.store == nil || service.now == nil {
		return model.ApprovalGrant{}, "", fmt.Errorf("approval service is not configured")
	}
	grantID, secret, err := ParseToken(token)
	if err != nil {
		return model.ApprovalGrant{}, "", err
	}
	grant, found := service.store.ApprovalGrant(grantID)
	hash := tokenHash(secret)
	if !found || subtle.ConstantTimeCompare([]byte(grant.TokenHash), []byte(hash)) != 1 {
		return model.ApprovalGrant{}, "", ErrInvalid
	}
	switch grant.Status {
	case model.ApprovalGrantConsumed:
		return model.ApprovalGrant{}, "", ErrConsumed
	case model.ApprovalGrantActive:
	case model.ApprovalGrantExpired:
		return model.ApprovalGrant{}, "", ErrExpired
	default:
		return model.ApprovalGrant{}, "", ErrInvalid
	}
	if !service.now().UTC().Before(grant.ExpiresAt) {
		return model.ApprovalGrant{}, "", ErrExpired
	}
	return grant, hash, nil
}

func grantMatchesIntent(grant model.ApprovalGrant, operation model.Operation, targetID model.ResourceID) bool {
	return grant.ClusterID == operation.ClusterID &&
		grant.Engine == operation.Engine &&
		grant.OperationKind == operation.Kind &&
		grant.TargetID == targetID
}

func (service *Service) AuthorizeIntent(ctx context.Context, token string, operation model.Operation, targetID model.ResourceID) (model.ApprovalGrant, error) {
	grant, _, err := service.loadToken(ctx, token)
	if err != nil {
		return model.ApprovalGrant{}, err
	}
	if !grantMatchesIntent(grant, operation, targetID) {
		return model.ApprovalGrant{}, ErrMismatch
	}
	return publicGrant(grant), nil
}

func (service *Service) Consume(ctx context.Context, operation model.OperationRecord, token string) (model.ResourceID, model.OperationRecord, error) {
	grant, hash, err := service.loadToken(ctx, token)
	if err != nil {
		return "", model.OperationRecord{}, err
	}
	if !grantMatchesIntent(grant, operation.Operation, operation.TargetID) || grant.OperationID != operation.ResourceID {
		return "", model.OperationRecord{}, ErrMismatch
	}
	if grant.PlanDigest != operation.Plan.Digest || grant.ObservationDigest != operation.Observation {
		return "", model.OperationRecord{}, ErrStalePlan
	}
	consumed, approved, err := service.store.ConsumeApprovalGrant(store.ConsumeApprovalGrantRequest{
		GrantID:                   grant.ResourceID,
		TokenHash:                 hash,
		OperationID:               operation.ResourceID,
		ExpectedOperationRevision: operation.MetadataRevision,
		Now:                       service.now().UTC(),
		Transition: model.OperationTransition{
			Stage:   model.StageApprove,
			Message: "one-time approval grant consumed",
		},
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			current, found := service.store.ApprovalGrant(grant.ResourceID)
			if found && current.Status == model.ApprovalGrantConsumed {
				return "", model.OperationRecord{}, ErrConsumed
			}
		}
		return "", model.OperationRecord{}, err
	}
	return consumed.ResourceID, approved, nil
}

func (service *Service) Grants() []model.ApprovalGrant {
	if service == nil || service.store == nil {
		return nil
	}
	grants := service.store.ApprovalGrants()
	for index := range grants {
		grants[index] = publicGrant(grants[index])
	}
	return grants
}

func (service *Service) Grant(resourceID model.ResourceID) (model.ApprovalGrant, bool) {
	if service == nil || service.store == nil {
		return model.ApprovalGrant{}, false
	}
	grant, found := service.store.ApprovalGrant(resourceID)
	return publicGrant(grant), found
}
