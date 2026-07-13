package recovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type recoveryAuthorityStub struct{ err error }

func (stub recoveryAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

type recoveryFailureEvidenceStub struct {
	incidents map[model.ResourceID]time.Time
}

func (stub recoveryFailureEvidenceStub) Incident(clusterID model.ResourceID, _ time.Time) (time.Time, bool) {
	incident, found := stub.incidents[clusterID]
	return incident, found
}

type recoveryStateStub struct {
	clusters   []model.DatabaseCluster
	snapshots  map[model.ResourceID]model.TopologySnapshot
	operations map[model.ResourceID][]model.OperationRecord
}

func (stub recoveryStateStub) Clusters() []model.DatabaseCluster {
	return append([]model.DatabaseCluster{}, stub.clusters...)
}

func (stub recoveryStateStub) TopologySnapshot(clusterID model.ResourceID) (model.TopologySnapshot, bool) {
	snapshot, found := stub.snapshots[clusterID]
	return snapshot, found
}

func (stub recoveryStateStub) Operations(clusterID model.ResourceID) []model.OperationRecord {
	return append([]model.OperationRecord{}, stub.operations[clusterID]...)
}

type recoverySelectorStub struct {
	targets map[model.ResourceID]model.ResourceID
}

type recoveryCandidateEvaluatorStub struct {
	assessments []model.CandidateAssessment
	request     adapter.CandidateRequest
}

func (stub *recoveryCandidateEvaluatorStub) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	stub.request = request
	return append([]model.CandidateAssessment{}, stub.assessments...), nil
}

func (stub recoverySelectorStub) Select(_ context.Context, cluster model.DatabaseCluster, _ model.TopologySnapshot) (model.ResourceID, error) {
	return stub.targets[cluster.ResourceID], nil
}

type recoveryExecutorStub struct {
	mu       sync.Mutex
	requests []adapter.OperationRequest
	tokens   []string
	err      error
}

func (stub *recoveryExecutorStub) Execute(_ context.Context, request adapter.OperationRequest, token string) (model.Execution, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.requests = append(stub.requests, request)
	stub.tokens = append(stub.tokens, token)
	return model.Execution{Status: model.OperationSucceeded}, stub.err
}

func (stub *recoveryExecutorStub) calls() ([]adapter.OperationRequest, []string) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return append([]adapter.OperationRequest{}, stub.requests...), append([]string{}, stub.tokens...)
}

func recoveryFixture(now time.Time) (model.DatabaseCluster, model.TopologySnapshot, model.ResourceID) {
	clusterID := model.NewResourceID()
	primaryID, targetID := model.NewResourceID(), model.NewResourceID()
	cluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL, DisplayName: "orders"}
	snapshot := model.TopologySnapshot{
		ClusterID: clusterID, ObservedAt: now,
		Instances: []model.DatabaseInstance{
			{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Engine: model.EngineMySQL, Role: model.RolePrimary, Health: model.Health{State: model.HealthUnhealthy}},
			{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, ClusterID: clusterID, Engine: model.EngineMySQL, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy}},
		},
	}
	return cluster, snapshot, targetID
}

func TestControllerExecutesOneAuditedFailoverForStableIncident(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}},
		executor, recoveryAuthorityStub{}, "automatic-approval", 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	requests, tokens := executor.calls()
	if len(requests) != 1 || len(tokens) != 1 {
		t.Fatalf("automatic failover calls=%d tokens=%d", len(requests), len(tokens))
	}
	request := requests[0]
	if request.Operation.ClusterID != cluster.ResourceID || request.Operation.Engine != model.EngineMySQL || request.Operation.Kind != model.OperationFailover ||
		request.Operation.RequestedBy != AutomaticRecoveryActor || request.TargetID != targetID || tokens[0] != "automatic-approval" {
		t.Fatalf("automatic failover request=%+v token=%q", request, tokens[0])
	}
	prefix := automaticFailoverPrefix(cluster.ResourceID, snapshot.Instances[0].ResourceID, incident)
	if request.IdempotencyKey != prefix+"1" {
		t.Fatalf("idempotency key=%q, want %q", request.IdempotencyKey, prefix+"1")
	}
}

func TestControllerDoesNothingWithoutLeaderMajorityOrStableIncident(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	for _, testCase := range []struct {
		name      string
		authority recoveryAuthorityStub
		evidence  recoveryFailureEvidenceStub
	}{
		{name: "no majority", authority: recoveryAuthorityStub{err: errors.New("no quorum")}, evidence: recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: now.Add(-30 * time.Second)}}},
		{name: "unstable failure", authority: recoveryAuthorityStub{}, evidence: recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			executor := &recoveryExecutorStub{}
			controller := NewController(
				recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}},
				testCase.evidence, recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}},
				executor, testCase.authority, "automatic-approval", 30*time.Second, func() time.Time { return now },
			)
			if err := controller.RunOnce(context.Background()); err != nil {
				t.Fatalf("run once: %v", err)
			}
			if requests, _ := executor.calls(); len(requests) != 0 {
				t.Fatalf("unsafe automatic failover requests=%+v", requests)
			}
		})
	}
}

func TestControllerDoesNotRepeatSucceededOrIndeterminateIncident(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	previousLeaderIncident := incident.Add(-time.Minute)
	for _, status := range []model.OperationStatus{model.OperationSucceeded, model.OperationIndeterminate} {
		executor := &recoveryExecutorStub{}
		controller := NewController(
			recoveryStateStub{
				clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
				operations: map[model.ResourceID][]model.OperationRecord{cluster.ResourceID: {{
					ResourceMeta:   model.ResourceMeta{UpdatedAt: now.Add(-time.Minute)},
					IdempotencyKey: automaticFailoverPrefix(cluster.ResourceID, snapshot.Instances[0].ResourceID, previousLeaderIncident) + "1", Status: status,
					Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationFailover, RequestedBy: AutomaticRecoveryActor},
				}}},
			},
			recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
			recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}, executor, recoveryAuthorityStub{}, "approval", 30*time.Second, func() time.Time { return now },
		)
		if err := controller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run completed incident: %v", err)
		}
		if requests, _ := executor.calls(); len(requests) != 0 {
			t.Fatalf("status %s repeated incident: %+v", status, requests)
		}
	}
}

func TestControllerRetriesBlockedIncidentAfterBackoff(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	prefix := automaticFailoverPrefix(cluster.ResourceID, snapshot.Instances[0].ResourceID, incident)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
			operations: map[model.ResourceID][]model.OperationRecord{cluster.ResourceID: {{
				ResourceMeta:   model.ResourceMeta{UpdatedAt: now.Add(-31 * time.Second)},
				IdempotencyKey: prefix + "1", Status: model.OperationBlocked,
				Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationFailover, RequestedBy: AutomaticRecoveryActor},
			}}},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}, executor, recoveryAuthorityStub{}, "approval", 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry blocked incident: %v", err)
	}
	requests, _ := executor.calls()
	if len(requests) != 1 || !strings.HasSuffix(requests[0].IdempotencyKey, "2") {
		t.Fatalf("blocked incident retry requests=%+v", requests)
	}
}

func TestMySQLCandidateSelectorUsesTheRankOneEligibleReplica(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	otherID := model.NewResourceID()
	snapshot.Instances = append(snapshot.Instances, model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: otherID}, ClusterID: cluster.ResourceID,
		Engine: model.EngineMySQL, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
	})
	evaluator := &recoveryCandidateEvaluatorStub{assessments: []model.CandidateAssessment{
		{InstanceID: otherID, Eligible: true, Rank: 2},
		{InstanceID: targetID, Eligible: true, Rank: 1},
	}}
	selector := NewMySQLCandidateSelector(evaluator)
	selected, err := selector.Select(context.Background(), cluster, snapshot)
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if selected != targetID {
		t.Fatalf("selected=%s, want rank-one %s", selected, targetID)
	}
	if evaluator.request.Primary.Role != model.RolePrimary || len(evaluator.request.Instances) != 2 || evaluator.request.Policy.MaximumLagSeconds != 30 || !evaluator.request.Policy.RequireGTID {
		t.Fatalf("candidate evaluation request=%+v", evaluator.request)
	}
}

func TestControllerUsesConfiguredPollingInterval(t *testing.T) {
	controller := NewController(nil, nil, nil, nil, nil, "", 30*time.Second, nil, WithInterval(7*time.Second))
	if controller.interval != 7*time.Second {
		t.Fatalf("controller interval=%s", controller.interval)
	}
}
