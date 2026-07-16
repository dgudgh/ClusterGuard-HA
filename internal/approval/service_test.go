package approval

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func plannedOperation(t *testing.T, repository *store.Repository, now time.Time) model.OperationRecord {
	t.Helper()
	request := model.OperationRecord{
		Operation: model.Operation{
			ClusterID:   model.NewResourceID(),
			Engine:      model.EngineMySQL,
			Kind:        model.OperationSwitchover,
			RequestedBy: "dba",
		},
		TargetID:       model.NewResourceID(),
		IdempotencyKey: "approval-service-plan",
	}
	record, _, err := repository.CreateOperation(request)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	observation := string(record.Operation.ClusterID) + "@sha256:observation"
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: model.StageDiscover, Observation: observation,
	})
	if err != nil {
		t.Fatalf("record observation: %v", err)
	}
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{
		Stage: model.StagePrecheck, Precheck: []model.Check{{Name: "candidate", Status: model.CheckPass}},
	})
	if err != nil {
		t.Fatalf("record precheck: %v", err)
	}
	record, err = repository.PutOperationPlan(record.ResourceID, record.MetadataRevision, model.OperationPlan{
		OperationID:      record.ResourceID,
		ClusterID:        record.Operation.ClusterID,
		SourceID:         model.NewResourceID(),
		TargetID:         record.TargetID,
		ObservationToken: observation,
		ResourceRevisions: map[model.ResourceID]uint64{
			record.Operation.ClusterID: 1,
			record.TargetID:            1,
		},
		Steps:    []model.PlanStep{{Index: 1, Name: "promote_target", Owner: "mysql", Mutating: true}},
		Digest:   "sha256:approval-service-plan",
		Summary:  "planned switchover",
		Mutating: true,
	})
	if err != nil {
		t.Fatalf("put operation plan: %v", err)
	}
	return record
}

func TestIssuePersistsOnlyHashAndReturnsTokenOnce(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	record := plannedOperation(t, repository, now)
	random := bytes.NewReader(bytes.Repeat([]byte{0x2a}, approvalSecretBytes))
	service := New(repository, random, func() time.Time { return now })

	issued, err := service.Issue(context.Background(), IssueRequest{Operation: record, IssuedBy: "approver"})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}
	if !strings.HasPrefix(issued.Token, "cgag_"+string(issued.Grant.ResourceID)+".") {
		t.Fatalf("token=%q", issued.Token)
	}
	if issued.Grant.TokenHash != "" {
		t.Fatalf("public grant exposed token hash: %+v", issued.Grant)
	}
	stored, found := repository.ApprovalGrant(issued.Grant.ResourceID)
	if !found || !strings.HasPrefix(stored.TokenHash, "sha256:") {
		t.Fatalf("stored grant=%+v found=%v", stored, found)
	}
	raw, err := repository.ReplicatedState()
	if err != nil {
		t.Fatalf("replicated state: %v", err)
	}
	if bytes.Contains(raw, []byte(issued.Token)) {
		t.Fatal("replicated state contains plaintext approval token")
	}
	if !issued.Grant.ExpiresAt.Equal(now.Add(defaultTTL)) {
		t.Fatalf("expires_at=%s", issued.Grant.ExpiresAt)
	}
}

func TestIssueRejectsInvalidTTLAndUnplannedOperation(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	service := New(repository, bytes.NewReader(bytes.Repeat([]byte{1}, approvalSecretBytes*3)), func() time.Time { return now })
	record := plannedOperation(t, repository, now)

	if _, err := service.Issue(context.Background(), IssueRequest{Operation: record, IssuedBy: "approver", TTL: maximumTTL + time.Second}); err == nil {
		t.Fatal("oversized approval TTL was accepted")
	}
	record.Plan = model.OperationPlan{}
	if _, err := service.Issue(context.Background(), IssueRequest{Operation: record, IssuedBy: "approver"}); err == nil {
		t.Fatal("unplanned operation received an approval grant")
	}
}

func TestAuthorizeIntentRejectsExpiredConsumedAndMismatchedGrant(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	record := plannedOperation(t, repository, now)
	service := New(repository, bytes.NewReader(bytes.Repeat([]byte{3}, approvalSecretBytes)), func() time.Time { return now })
	issued, err := service.Issue(context.Background(), IssueRequest{Operation: record, IssuedBy: "approver", TTL: time.Minute})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}

	if authorized, err := service.AuthorizeIntent(context.Background(), issued.Token, record.Operation, record.TargetID); err != nil || authorized.OperationID != record.ResourceID {
		t.Fatalf("authorize valid grant: grant=%+v err=%v", authorized, err)
	}
	wrongOperation := record.Operation
	wrongOperation.ClusterID = model.NewResourceID()
	if _, err := service.AuthorizeIntent(context.Background(), issued.Token, wrongOperation, record.TargetID); !errors.Is(err, ErrMismatch) {
		t.Fatalf("scope mismatch error=%v", err)
	}
	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := service.AuthorizeIntent(context.Background(), issued.Token, record.Operation, record.TargetID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired grant error=%v", err)
	}
}

func TestConsumeIsSingleUseAndPlanBound(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	record := plannedOperation(t, repository, now)
	record, err := repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{Stage: model.StageSafetyGuard})
	if err != nil {
		t.Fatalf("record safety: %v", err)
	}
	record, err = repository.TransitionOperation(record.ResourceID, record.MetadataRevision, model.OperationTransition{Stage: model.StageLock})
	if err != nil {
		t.Fatalf("record lock: %v", err)
	}
	service := New(repository, bytes.NewReader(bytes.Repeat([]byte{4}, approvalSecretBytes)), func() time.Time { return now })
	issued, err := service.Issue(context.Background(), IssueRequest{Operation: record, IssuedBy: "approver"})
	if err != nil {
		t.Fatalf("issue grant: %v", err)
	}

	grantID, approved, err := service.Consume(context.Background(), record, issued.Token)
	if err != nil || grantID != issued.Grant.ResourceID || approved.Stage != model.StageApprove {
		t.Fatalf("consume grant: id=%s operation=%+v err=%v", grantID, approved, err)
	}
	if _, _, err := service.Consume(context.Background(), approved, issued.Token); !errors.Is(err, ErrConsumed) {
		t.Fatalf("reused grant error=%v", err)
	}
}

func TestParseTokenRejectsMalformedSecrets(t *testing.T) {
	if _, _, err := ParseToken(""); !errors.Is(err, ErrRequired) {
		t.Fatalf("empty token error=%v", err)
	}
	for _, token := range []string{"cgag_bad", "cgag_not-a-uuid.secret", "cgag_" + string(model.NewResourceID()) + ".short"} {
		if _, _, err := ParseToken(token); !errors.Is(err, ErrInvalid) {
			t.Fatalf("token %q error=%v", token, err)
		}
	}
}
