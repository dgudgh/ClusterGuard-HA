package store

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func testApprovalGrant(now time.Time) model.ApprovalGrant {
	return model.ApprovalGrant{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		TokenHash:         "sha256:0123456789abcdef",
		OperationID:       model.NewResourceID(),
		ClusterID:         model.NewResourceID(),
		Engine:            model.EngineMySQL,
		OperationKind:     model.OperationSwitchover,
		TargetID:          model.NewResourceID(),
		PlanDigest:        "sha256:plan",
		ObservationDigest: "cluster@sha256:observation",
		IssuedBy:          "dba-admin",
		IssuedAt:          now,
		ExpiresAt:         now.Add(5 * time.Minute),
		Status:            model.ApprovalGrantActive,
	}
}

func plannedApprovalOperation(t *testing.T, repository *Repository, now time.Time) model.OperationRecord {
	t.Helper()
	request := operationFixture()
	request.IdempotencyKey = "approval-consume-operation"
	created, _, err := repository.CreateOperation(request)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	observed := string(created.Operation.ClusterID) + "@sha256:observation"
	created, err = repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageDiscover, Observation: observed, Message: "observation captured",
	})
	if err != nil {
		t.Fatalf("record observation: %v", err)
	}
	created, err = repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StagePrecheck, Precheck: []model.Check{{Name: "candidate", Status: model.CheckPass}}, Message: "precheck complete",
	})
	if err != nil {
		t.Fatalf("record precheck: %v", err)
	}
	plan := model.OperationPlan{
		OperationID:      created.ResourceID,
		ClusterID:        created.Operation.ClusterID,
		SourceID:         model.NewResourceID(),
		TargetID:         created.TargetID,
		ObservationToken: observed,
		ResourceRevisions: map[model.ResourceID]uint64{
			created.Operation.ClusterID: 1,
			created.TargetID:            1,
		},
		Steps:    []model.PlanStep{{Index: 1, Name: "promote_target", Owner: "mysql", Mutating: true}},
		Digest:   "sha256:approval-plan",
		Summary:  "planned switchover",
		Mutating: true,
	}
	created, err = repository.PutOperationPlan(created.ResourceID, created.MetadataRevision, plan)
	if err != nil {
		t.Fatalf("put operation plan: %v", err)
	}
	created, err = repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageSafetyGuard, Message: "safety passed",
	})
	if err != nil {
		t.Fatalf("record safety guard: %v", err)
	}
	created, err = repository.TransitionOperation(created.ResourceID, created.MetadataRevision, model.OperationTransition{
		Stage: model.StageLock, Message: "lock acquired",
	})
	if err != nil {
		t.Fatalf("record lock: %v", err)
	}
	return created
}

func TestApprovalGrantConsumptionAtomicallyAdvancesOperation(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	repository := NewMemory()
	repository.now = func() time.Time { return now }
	operation := plannedApprovalOperation(t, repository, now)
	grant := testApprovalGrant(now)
	grant.OperationID = operation.ResourceID
	grant.ClusterID = operation.Operation.ClusterID
	grant.Engine = operation.Operation.Engine
	grant.OperationKind = operation.Operation.Kind
	grant.TargetID = operation.TargetID
	grant.PlanDigest = operation.Plan.Digest
	grant.ObservationDigest = operation.Observation
	if err := repository.PutApprovalGrant(grant); err != nil {
		t.Fatalf("put approval grant: %v", err)
	}

	consumed, approved, err := repository.ConsumeApprovalGrant(ConsumeApprovalGrantRequest{
		GrantID:                   grant.ResourceID,
		TokenHash:                 grant.TokenHash,
		OperationID:               operation.ResourceID,
		ExpectedOperationRevision: operation.MetadataRevision,
		Now:                       now.Add(time.Minute),
		Transition: model.OperationTransition{
			Stage: model.StageApprove, Message: "one-time approval consumed",
		},
	})
	if err != nil {
		t.Fatalf("consume approval grant: %v", err)
	}
	if consumed.Status != model.ApprovalGrantConsumed || consumed.ConsumedByOperationID != operation.ResourceID || consumed.ConsumedAt.IsZero() {
		t.Fatalf("consumed grant=%+v", consumed)
	}
	if approved.Stage != model.StageApprove || approved.Message != "one-time approval consumed" {
		t.Fatalf("approved operation=%+v", approved)
	}

	if _, _, err := repository.ConsumeApprovalGrant(ConsumeApprovalGrantRequest{
		GrantID: grant.ResourceID, TokenHash: grant.TokenHash, OperationID: operation.ResourceID,
		ExpectedOperationRevision: approved.MetadataRevision, Now: now.Add(2 * time.Minute),
		Transition: model.OperationTransition{Stage: model.StageApprove},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("consumed grant was reusable: %v", err)
	}
}

func TestApprovalGrantConsumptionRejectsWrongHashWithoutMutation(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	repository := NewMemory()
	repository.now = func() time.Time { return now }
	operation := plannedApprovalOperation(t, repository, now)
	grant := testApprovalGrant(now)
	grant.OperationID = operation.ResourceID
	grant.ClusterID = operation.Operation.ClusterID
	grant.Engine = operation.Operation.Engine
	grant.OperationKind = operation.Operation.Kind
	grant.TargetID = operation.TargetID
	grant.PlanDigest = operation.Plan.Digest
	grant.ObservationDigest = operation.Observation
	if err := repository.PutApprovalGrant(grant); err != nil {
		t.Fatalf("put approval grant: %v", err)
	}

	if _, _, err := repository.ConsumeApprovalGrant(ConsumeApprovalGrantRequest{
		GrantID: grant.ResourceID, TokenHash: "sha256:wrong", OperationID: operation.ResourceID,
		ExpectedOperationRevision: operation.MetadataRevision, Now: now.Add(time.Minute),
		Transition: model.OperationTransition{Stage: model.StageApprove},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong hash error=%v", err)
	}
	storedGrant, _ := repository.ApprovalGrant(grant.ResourceID)
	storedOperation, _ := repository.Operation(operation.ResourceID)
	if storedGrant.Status != model.ApprovalGrantActive || storedOperation.Stage != model.StageLock {
		t.Fatalf("failed consume mutated state: grant=%+v operation=%+v", storedGrant, storedOperation)
	}
}

func TestApprovalGrantRoundTripsWithoutPlaintextSecret(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	repository := NewMemory()
	grant := testApprovalGrant(now)

	if err := repository.PutApprovalGrant(grant); err != nil {
		t.Fatalf("put approval grant: %v", err)
	}
	stored, found := repository.ApprovalGrant(grant.ResourceID)
	if !found || stored.TokenHash != grant.TokenHash || stored.OperationID != grant.OperationID {
		t.Fatalf("stored grant=%+v found=%v", stored, found)
	}
	raw, err := repository.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	if bytes.Contains(raw, []byte("cgag_")) {
		t.Fatal("replicated state contains a plaintext approval token")
	}
}

func TestApprovalGrantReplicatesAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/metadata.json"
	leader, err := Open(path)
	if err != nil {
		t.Fatalf("open leader repository: %v", err)
	}
	grant := testApprovalGrant(now)
	if err := leader.PutApprovalGrant(grant); err != nil {
		t.Fatalf("put approval grant: %v", err)
	}

	state, err := leader.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	follower := NewMemory()
	if err := follower.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply replicated state: %v", err)
	}
	if replicated, found := follower.ApprovalGrant(grant.ResourceID); !found || replicated.TokenHash != grant.TokenHash {
		t.Fatalf("replicated grant=%+v found=%v", replicated, found)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	if persisted, found := reopened.ApprovalGrant(grant.ResourceID); !found || persisted.TokenHash != grant.TokenHash {
		t.Fatalf("persisted grant=%+v found=%v", persisted, found)
	}
}

func TestApprovalGrantValidationFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*model.ApprovalGrant)
	}{
		{name: "missing token hash", mutate: func(grant *model.ApprovalGrant) { grant.TokenHash = "" }},
		{name: "missing operation", mutate: func(grant *model.ApprovalGrant) { grant.OperationID = "" }},
		{name: "missing plan", mutate: func(grant *model.ApprovalGrant) { grant.PlanDigest = "" }},
		{name: "invalid expiry", mutate: func(grant *model.ApprovalGrant) { grant.ExpiresAt = grant.IssuedAt }},
		{name: "invalid status", mutate: func(grant *model.ApprovalGrant) { grant.Status = "mystery" }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repository := NewMemory()
			grant := testApprovalGrant(now)
			testCase.mutate(&grant)
			if err := repository.PutApprovalGrant(grant); err == nil {
				t.Fatalf("invalid grant was accepted: %+v", grant)
			}
		})
	}
}
