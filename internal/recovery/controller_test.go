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

type mutableRecoveryFailureEvidence struct {
	mu        sync.Mutex
	incidents map[model.ResourceID]time.Time
}

func (evidence *mutableRecoveryFailureEvidence) Incident(clusterID model.ResourceID, _ time.Time) (time.Time, bool) {
	evidence.mu.Lock()
	defer evidence.mu.Unlock()
	incident, found := evidence.incidents[clusterID]
	return incident, found
}

func (evidence *mutableRecoveryFailureEvidence) set(clusterID model.ResourceID, incident time.Time) {
	evidence.mu.Lock()
	defer evidence.mu.Unlock()
	evidence.incidents[clusterID] = incident
}

type recoveryStateStub struct {
	clusters    []model.DatabaseCluster
	snapshots   map[model.ResourceID]model.TopologySnapshot
	operations  map[model.ResourceID][]model.OperationRecord
	haEndpoints map[model.ResourceID][]model.HAEndpoint
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

func (stub recoveryStateStub) HAEndpoints(clusterID model.ResourceID) []model.HAEndpoint {
	return append([]model.HAEndpoint{}, stub.haEndpoints[clusterID]...)
}

type recoverySelectorStub struct {
	targets map[model.ResourceID]model.ResourceID
	sources *[]model.ResourceID
}

type recoveryCandidateEvaluatorStub struct {
	assessments []model.CandidateAssessment
	request     adapter.CandidateRequest
}

func (stub *recoveryCandidateEvaluatorStub) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	stub.request = request
	return append([]model.CandidateAssessment{}, stub.assessments...), nil
}

func (stub recoverySelectorStub) Select(_ context.Context, cluster model.DatabaseCluster, _ model.TopologySnapshot, sourceID model.ResourceID) (model.ResourceID, error) {
	if stub.sources != nil {
		*stub.sources = append(*stub.sources, sourceID)
	}
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

type blockingRecoveryExecutor struct {
	firstCluster model.ResourceID
	started      chan model.ResourceID
	releaseFirst chan struct{}
}

func (executor *blockingRecoveryExecutor) ExecuteAutomatic(ctx context.Context, request adapter.OperationRequest, _ string) (model.Execution, error) {
	select {
	case executor.started <- request.Operation.ClusterID:
	case <-ctx.Done():
		return model.Execution{}, ctx.Err()
	}
	if request.Operation.ClusterID == executor.firstCluster {
		select {
		case <-executor.releaseFirst:
		case <-ctx.Done():
			return model.Execution{}, ctx.Err()
		}
	}
	return model.Execution{Status: model.OperationSucceeded}, nil
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
		request.Operation.RequestedBy != AutomaticRecoveryActor || request.TargetID != targetID || !request.AutomaticFailureIncidentAt.Equal(incident) {
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

func TestControllerUsesHAEndpointOwnerWhenFailedProbeClearsTheRuntimeRole(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleUnknown
	snapshot.Probes = []model.ProbeStatus{
		{InstanceID: sourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable, Health: model.Health{State: model.HealthUnhealthy, ObservedAt: now}},
		{InstanceID: targetID, Outcome: model.ProbeOutcomeReachable, DiscoveryObservedAt: now, Health: model.Health{State: model.HealthHealthy, ObservedAt: now}},
	}
	sources := make([]model.ResourceID, 0, 1)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters:  []model.DatabaseCluster{cluster},
			snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
			haEndpoints: map[model.ResourceID][]model.HAEndpoint{cluster.ResourceID: {{
				ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID,
				Kind: model.EndpointVIP, DesiredRole: model.RolePrimary, OwnerID: sourceID,
			}}},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: now.Add(-30 * time.Second)}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}, sources: &sources},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	requests, _ := executor.calls()
	if len(requests) != 1 || len(sources) != 1 || sources[0] != sourceID {
		t.Fatalf("automatic failover requests=%+v source IDs=%+v, want source %s", requests, sources, sourceID)
	}
	if requests[0].SourceID != sourceID {
		t.Fatalf("automatic failover workflow source=%s, want %s", requests[0].SourceID, sourceID)
	}
}

func TestFailedPrimaryHAOwnerRequiresCurrentDatabaseFailureEvidence(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cluster, snapshot, _ := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleUnknown
	resource := model.HAEndpoint{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID,
		Kind: model.EndpointVIP, DesiredRole: model.RolePrimary, OwnerID: sourceID,
	}
	for _, testCase := range []struct {
		name    string
		probe   model.ProbeStatus
		allowed bool
	}{
		{name: "missing evidence"},
		{name: "stale database failure", probe: model.ProbeStatus{InstanceID: sourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable, Health: model.Health{State: model.HealthUnhealthy, ObservedAt: now.Add(-time.Second)}}},
		{name: "credentials unavailable", probe: model.ProbeStatus{InstanceID: sourceID, Outcome: model.ProbeOutcomeCredentialsUnavailable, Health: model.Health{State: model.HealthUnknown, ObservedAt: now}}},
		{name: "current database failure", probe: model.ProbeStatus{InstanceID: sourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable, Health: model.Health{State: model.HealthUnhealthy, ObservedAt: now}}, allowed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := snapshot
			if testCase.probe.InstanceID != "" {
				candidate.Probes = []model.ProbeStatus{testCase.probe}
			}
			selected, ok := failedPrimary(candidate, []model.HAEndpoint{resource})
			if ok != testCase.allowed || (ok && selected != sourceID) {
				t.Fatalf("failed primary selected=%s allowed=%t, want allowed=%t", selected, ok, testCase.allowed)
			}
		})
	}
}

func TestFailedPrimaryRejectsConflictingHAEndpointOwners(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleUnknown
	snapshot.Probes = []model.ProbeStatus{{InstanceID: sourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable, Health: model.Health{State: model.HealthUnhealthy, ObservedAt: now}}}
	resources := []model.HAEndpoint{
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, DesiredRole: model.RolePrimary, OwnerID: sourceID},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Kind: model.EndpointListener, DesiredRole: model.RolePrimary, OwnerID: targetID},
	}
	if selected, ok := failedPrimary(snapshot, resources); ok || selected != "" {
		t.Fatalf("conflicting HA owners selected unsafe source=%s", selected)
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

func TestControllerResumesPromotedUnverifiedAutomaticFailoverBeforeSelectingAgain(t *testing.T) {
	now := time.Date(2026, time.August, 12, 2, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleReplica
	snapshot.Instances[1].Role = model.RolePrimary
	idempotencyKey := automaticFailoverPrefix(cluster.ResourceID, sourceID, incident) + "1"
	operationID := model.NewResourceID()
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters:  []model.DatabaseCluster{cluster},
			snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
			operations: map[model.ResourceID][]model.OperationRecord{cluster.ResourceID: {{
				ResourceMeta:   model.ResourceMeta{ResourceID: operationID, UpdatedAt: now.Add(-time.Second)},
				IdempotencyKey: idempotencyKey,
				Status:         model.OperationIndeterminate,
				FailureClass:   "promoted_unverified",
				TargetID:       targetID,
				Operation: model.Operation{
					ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationFailover,
					RequestedBy: AutomaticRecoveryActor,
				},
				Plan: model.OperationPlan{OperationID: operationID, ClusterID: cluster.ResourceID, SourceID: sourceID, TargetID: targetID},
			}}},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)

	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("resume promoted-unverified automatic failover: %v", err)
	}
	requests, incidents := executor.calls()
	if len(requests) != 1 || len(incidents) != 1 {
		t.Fatalf("continuation calls requests=%+v incidents=%+v", requests, incidents)
	}
	if requests[0].IdempotencyKey != idempotencyKey || requests[0].TargetID != targetID || requests[0].Parameters["trigger"] != "resume_promoted_unverified" {
		t.Fatalf("continuation request=%+v", requests[0])
	}
	if incidents[0] != strings.TrimSuffix(automaticFailoverPrefix(cluster.ResourceID, sourceID, incident), ":") {
		t.Fatalf("continuation incident=%q", incidents[0])
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

func TestControllerRetriesTransientTopologyChangeAfterTwoSeconds(t *testing.T) {
	now := time.Date(2026, time.August, 13, 3, 0, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-8 * time.Second)
	prefix := automaticFailoverPrefix(cluster.ResourceID, snapshot.Instances[0].ResourceID, incident)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{
			clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot},
			operations: map[model.ResourceID][]model.OperationRecord{cluster.ResourceID: {{
				ResourceMeta:   model.ResourceMeta{UpdatedAt: now.Add(-3 * time.Second)},
				IdempotencyKey: prefix + "1", Status: model.OperationBlocked, FailureClass: "pre_commit",
				Message:   "topology observation changed under operation lock",
				Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationFailover, RequestedBy: AutomaticRecoveryActor},
			}}},
		},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}, executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry transient topology change: %v", err)
	}
	requests, _ := executor.calls()
	if len(requests) != 1 || !strings.HasSuffix(requests[0].IdempotencyKey, "2") {
		t.Fatalf("transient topology retry requests=%+v", requests)
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
	selected, err := selector.Select(context.Background(), cluster, snapshot, snapshot.Instances[0].ResourceID)
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

func TestMySQLCandidateSelectorUsesExplicitFailedSourceAfterDiscoveryClearsItsRole(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleUnknown
	evaluator := &recoveryCandidateEvaluatorStub{assessments: []model.CandidateAssessment{{InstanceID: targetID, Eligible: true, Rank: 1}}}
	selector := NewMySQLCandidateSelector(evaluator)
	selected, err := selector.Select(context.Background(), cluster, snapshot, sourceID)
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if selected != targetID || evaluator.request.Primary.ResourceID != sourceID || evaluator.request.Primary.Role != model.RoleUnknown || len(evaluator.request.Instances) != 1 {
		t.Fatalf("selected=%s request=%+v", selected, evaluator.request)
	}
}

func TestMySQLCandidateSelectorRejectsASecondObservedPrimary(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	cluster, snapshot, _ := recoveryFixture(now)
	sourceID := snapshot.Instances[0].ResourceID
	snapshot.Instances[0].Role = model.RoleUnknown
	snapshot.Instances[1].Role = model.RolePrimary
	selector := NewMySQLCandidateSelector(&recoveryCandidateEvaluatorStub{})
	if _, err := selector.Select(context.Background(), cluster, snapshot, sourceID); err == nil || !strings.Contains(err.Error(), "another primary") {
		t.Fatalf("second observed primary was not blocked: %v", err)
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
	selected, err := selector.Select(context.Background(), cluster, snapshot, snapshot.Instances[0].ResourceID)
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

func TestPostgreSQLCandidateSelectorReportsWhenNoStandbyIsYetEligible(t *testing.T) {
	now := time.Date(2026, time.August, 23, 11, 0, 0, 0, time.UTC)
	cluster, snapshot, _ := recoveryFixture(now)
	cluster.Engine = model.EnginePostgreSQL
	for index := range snapshot.Instances {
		snapshot.Instances[index].Engine = model.EnginePostgreSQL
		if snapshot.Instances[index].Role == model.RoleReplica {
			snapshot.Instances[index].Role = model.RoleStandby
		}
	}
	selector := NewPostgreSQLCandidateSelector(&recoveryCandidateEvaluatorStub{})
	if _, err := selector.Select(context.Background(), cluster, snapshot, snapshot.Instances[0].ResourceID); err == nil || !strings.Contains(err.Error(), "no eligible") {
		t.Fatalf("missing PostgreSQL candidate was not observable: %v", err)
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
			if _, err := selector.Select(context.Background(), cluster, testCase.snapshot, model.NewResourceID()); err == nil {
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

func TestControllerRunDoesNotHeadOfLineBlockStaggeredClusterFailures(t *testing.T) {
	now := time.Date(2026, time.July, 28, 9, 47, 0, 0, time.UTC)
	firstCluster, firstSnapshot, firstTargetID := recoveryFixture(now)
	secondCluster, secondSnapshot, secondTargetID := recoveryFixture(now)
	evidence := &mutableRecoveryFailureEvidence{incidents: map[model.ResourceID]time.Time{
		firstCluster.ResourceID: now.Add(-30 * time.Second),
	}}
	executor := &blockingRecoveryExecutor{
		firstCluster: firstCluster.ResourceID,
		started:      make(chan model.ResourceID, 8),
		releaseFirst: make(chan struct{}),
	}
	controller := NewController(
		recoveryStateStub{
			clusters: []model.DatabaseCluster{firstCluster, secondCluster},
			snapshots: map[model.ResourceID]model.TopologySnapshot{
				firstCluster.ResourceID:  firstSnapshot,
				secondCluster.ResourceID: secondSnapshot,
			},
		},
		evidence,
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{
			firstCluster.ResourceID:  firstTargetID,
			secondCluster.ResourceID: secondTargetID,
		}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
		WithInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		controller.Run(ctx)
		close(done)
	}()
	defer func() {
		close(executor.releaseFirst)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("automatic recovery did not stop after cancellation")
		}
	}()

	select {
	case clusterID := <-executor.started:
		if clusterID != firstCluster.ResourceID {
			t.Fatalf("first recovery cluster=%s, want %s", clusterID, firstCluster.ResourceID)
		}
	case <-time.After(time.Second):
		t.Fatal("first recovery did not start")
	}
	evidence.set(secondCluster.ResourceID, now.Add(-30*time.Second))

	select {
	case clusterID := <-executor.started:
		if clusterID != secondCluster.ResourceID {
			t.Fatalf("second recovery cluster=%s, want %s", clusterID, secondCluster.ResourceID)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("staggered cluster failure was blocked behind the active recovery")
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

func TestControllerSkipsRecoveryWhileClusterIsFrozen(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	cluster.RecoveryFreeze = true
	incident := now.Add(-30 * time.Second)
	executor := &recoveryExecutorStub{}
	controller := NewController(
		recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover on frozen cluster: %v", err)
	}
	requests, incidents := executor.calls()
	if len(requests) != 0 || len(incidents) != 0 {
		t.Fatalf("frozen cluster triggered failover calls=%d incidents=%d", len(requests), len(incidents))
	}
}
