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
	mu        sync.Mutex
	requests  []adapter.OperationRequest
	incidents []string
	err       error
}

func (stub *recoveryExecutorStub) ExecuteAutomatic(_ context.Context, request adapter.OperationRequest, incidentID string) (model.Execution, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.requests = append(stub.requests, request)
	stub.incidents = append(stub.incidents, incidentID)
	return model.Execution{Status: model.OperationSucceeded}, stub.err
}

func (stub *recoveryExecutorStub) calls() ([]adapter.OperationRequest, []string) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return append([]adapter.OperationRequest{}, stub.requests...), append([]string{}, stub.incidents...)
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
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	requests, incidents := executor.calls()
	if len(requests) != 1 || len(incidents) != 1 {
		t.Fatalf("automatic failover calls=%d incidents=%d", len(requests), len(incidents))
	}
	request := requests[0]
	if request.Operation.ClusterID != cluster.ResourceID || request.Operation.Engine != model.EngineMySQL || request.Operation.Kind != model.OperationFailover ||
		request.Operation.RequestedBy != AutomaticRecoveryActor || request.TargetID != targetID {
		t.Fatalf("automatic failover request=%+v", request)
	}
	prefix := automaticFailoverPrefix(cluster.ResourceID, snapshot.Instances[0].ResourceID, incident)
	if incidents[0] != strings.TrimSuffix(prefix, ":") {
		t.Fatalf("incident ID=%q, want %q", incidents[0], strings.TrimSuffix(prefix, ":"))
	}
	if request.IdempotencyKey != prefix+"1" {
		t.Fatalf("idempotency key=%q, want %q", request.IdempotencyKey, prefix+"1")
	}
}

func TestControllerExecutesPostgreSQLFailoverOnlyForPostgreSQLClusters(t *testing.T) {
	now := time.Date(2026, time.July, 20, 11, 30, 0, 0, time.UTC)
	postgresCluster, postgresSnapshot, postgresTargetID := recoveryFixture(now)
	postgresCluster.Engine = model.EnginePostgreSQL
	postgresCluster.DisplayName = "payments-postgresql"
	for index := range postgresSnapshot.Instances {
		postgresSnapshot.Instances[index].Engine = model.EnginePostgreSQL
		if postgresSnapshot.Instances[index].Role == model.RoleReplica {
			postgresSnapshot.Instances[index].Role = model.RoleStandby
		}
	}
	mysqlCluster, mysqlSnapshot, mysqlTargetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters: []model.DatabaseCluster{mysqlCluster, postgresCluster},
			snapshots: map[model.ResourceID]model.TopologySnapshot{
				mysqlCluster.ResourceID:    mysqlSnapshot,
				postgresCluster.ResourceID: postgresSnapshot,
			},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{
			mysqlCluster.ResourceID:    incident,
			postgresCluster.ResourceID: incident,
		}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{
			mysqlCluster.ResourceID:    mysqlTargetID,
			postgresCluster.ResourceID: postgresTargetID,
		}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
		WithEngine(model.EnginePostgreSQL),
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run PostgreSQL automatic failover: %v", err)
	}
	requests, _ := executor.calls()
	if len(requests) != 1 {
		t.Fatalf("PostgreSQL recovery requests=%+v", requests)
	}
	request := requests[0]
	if request.Operation.ClusterID != postgresCluster.ResourceID || request.Operation.Engine != model.EnginePostgreSQL || request.TargetID != postgresTargetID {
		t.Fatalf("PostgreSQL automatic failover request=%+v", request)
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
				executor, testCase.authority, 30*time.Second, func() time.Time { return now },
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
			recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}, executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
		)
		if err := controller.RunOnce(context.Background()); err != nil {
			t.Fatalf("run completed incident: %v", err)
		}
		if requests, _ := executor.calls(); len(requests) != 0 {
			t.Fatalf("status %s repeated incident: %+v", status, requests)
		}
	}
}

func TestControllerAllowsNewIncidentAfterSourceReturnsToPrimary(t *testing.T) {
	now := time.Date(2026, time.July, 21, 7, 10, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	previousIncident := now.Add(-2 * time.Hour)
	currentIncident := now.Add(-30 * time.Second)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters:  []model.DatabaseCluster{cluster},
			snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
			operations: map[model.ResourceID][]model.OperationRecord{cluster.ResourceID: {
				{
					ResourceMeta:   model.ResourceMeta{UpdatedAt: now.Add(-90 * time.Minute)},
					IdempotencyKey: automaticFailoverPrefix(cluster.ResourceID, sourceID, previousIncident) + "1",
					Status:         model.OperationSucceeded,
					Operation: model.Operation{
						ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationFailover,
						RequestedBy: AutomaticRecoveryActor,
					},
					TargetID: targetID,
				},
				{
					ResourceMeta:   model.ResourceMeta{UpdatedAt: now.Add(-time.Minute)},
					IdempotencyKey: "controlled-return-to-primary",
					Status:         model.OperationSucceeded,
					Operation: model.Operation{
						ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover,
						RequestedBy: "dba",
					},
					TargetID: sourceID,
				},
			}},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: currentIncident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run new source tenure incident: %v", err)
	}
	requests, _ := executor.calls()
	if len(requests) != 1 {
		t.Fatalf("new primary tenure recovery requests=%+v", requests)
	}
	wantKey := automaticFailoverPrefix(cluster.ResourceID, sourceID, currentIncident) + "1"
	if requests[0].IdempotencyKey != wantKey {
		t.Fatalf("new primary tenure idempotency key=%q, want %q", requests[0].IdempotencyKey, wantKey)
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
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}, executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
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
	if evaluator.request.Primary.Role != model.RolePrimary || len(evaluator.request.Instances) != 2 || evaluator.request.Policy.MaximumLagSeconds != 30 || !evaluator.request.Policy.RequireGTID || !evaluator.request.Policy.AllowSourceDisconnected {
		t.Fatalf("candidate evaluation request=%+v", evaluator.request)
	}
}

func TestPostgreSQLCandidateSelectorRequiresKnownZeroLag(t *testing.T) {
	now := time.Date(2026, time.July, 20, 11, 45, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	cluster.Engine = model.EnginePostgreSQL
	for index := range snapshot.Instances {
		snapshot.Instances[index].Engine = model.EnginePostgreSQL
		if snapshot.Instances[index].Role == model.RoleReplica {
			snapshot.Instances[index].Role = model.RoleStandby
		}
	}
	evaluator := &recoveryCandidateEvaluatorStub{assessments: []model.CandidateAssessment{{InstanceID: targetID, Eligible: true, Rank: 1}}}
	selector := NewPostgreSQLCandidateSelector(evaluator)
	selected, err := selector.Select(context.Background(), cluster, snapshot)
	if err != nil {
		t.Fatalf("select PostgreSQL candidate: %v", err)
	}
	if selected != targetID {
		t.Fatalf("selected=%s, want %s", selected, targetID)
	}
	if evaluator.request.Policy.MaximumLagSeconds != 0 || evaluator.request.Policy.RequireGTID || !evaluator.request.Policy.AllowSourceDisconnected {
		t.Fatalf("PostgreSQL automatic candidate policy=%+v", evaluator.request.Policy)
	}
}

func TestDatabaseCandidateSelectorRejectsEmptyOrForeignTopology(t *testing.T) {
	clusterID := model.NewResourceID()
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID},
		Engine:       model.EnginePostgreSQL,
	}
	selector := NewPostgreSQLCandidateSelector(&recoveryCandidateEvaluatorStub{})
	for _, testCase := range []struct {
		name     string
		snapshot model.TopologySnapshot
	}{
		{name: "empty", snapshot: model.TopologySnapshot{ClusterID: clusterID}},
		{name: "foreign cluster", snapshot: model.TopologySnapshot{ClusterID: model.NewResourceID()}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := selector.Select(context.Background(), cluster, testCase.snapshot); err == nil {
				t.Fatal("unsafe topology snapshot was accepted")
			}
		})
	}
}

func TestControllerUsesConfiguredPollingInterval(t *testing.T) {
	controller := NewController(nil, nil, nil, nil, nil, 30*time.Second, nil, WithInterval(7*time.Second))
	if controller.interval != 7*time.Second {
		t.Fatalf("controller interval=%s", controller.interval)
	}
}

func TestControllerRunReportsBackgroundFailures(t *testing.T) {
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	controller := NewController(nil, nil, nil, nil, nil, 30*time.Second, nil,
		WithErrorHandler(func(err error) {
			reported <- err
			cancel()
		}),
	)
	done := make(chan struct{})
	go func() {
		controller.Run(ctx)
		close(done)
	}()
	select {
	case err := <-reported:
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("reported recovery error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic recovery swallowed its background failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not stop after cancellation")
	}
}

func TestControllerRateLimitsRepeatedBackgroundFailures(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	reported := make([]string, 0, 3)
	controller := NewController(nil, nil, nil, nil, nil, 30*time.Second, func() time.Time { return now },
		WithErrorHandler(func(err error) { reported = append(reported, err.Error()) }),
	)

	controller.reportError(errors.New("recovery blocked"))
	controller.reportError(errors.New("recovery blocked"))
	if len(reported) != 1 {
		t.Fatalf("duplicate recovery errors reported=%v", reported)
	}
	now = now.Add(5 * time.Minute)
	controller.reportError(errors.New("recovery blocked"))
	if len(reported) != 2 {
		t.Fatalf("recovery reminder missing=%v", reported)
	}
	controller.reportError(nil)
	controller.reportError(errors.New("recovery blocked"))
	if len(reported) != 3 {
		t.Fatalf("successful cycle did not reset suppression=%v", reported)
	}
}

func TestControllerKeepsErrorSuppressedWhileStableIncidentIsInRetryBackoff(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	cluster, _, _ := recoveryFixture(now)
	evidence := recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: now.Add(-time.Minute)}}
	reported := make([]string, 0, 2)
	controller := NewController(
		recoveryStateStub{clusters: []model.DatabaseCluster{cluster}}, evidence, nil, nil, nil,
		30*time.Second, func() time.Time { return now },
		WithErrorHandler(func(err error) { reported = append(reported, err.Error()) }),
	)

	controller.reportError(errors.New("precheck contains blocking checks"))
	controller.reportError(nil)
	controller.reportError(errors.New("precheck contains blocking checks"))
	if len(reported) != 1 {
		t.Fatalf("retry backoff reset duplicate suppression=%v", reported)
	}

	delete(evidence.incidents, cluster.ResourceID)
	controller.reportError(nil)
	controller.reportError(errors.New("precheck contains blocking checks"))
	if len(reported) != 2 {
		t.Fatalf("resolved incident did not reset duplicate suppression=%v", reported)
	}
}
