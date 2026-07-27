package mysql

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	testClusterID         = model.ResourceID("00000000-0000-4000-8000-000000000001")
	testPrimaryID         = model.ResourceID("00000000-0000-4000-8000-000000000002")
	testPrimaryServerUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testErrantServerUUID  = "ffffffff-1111-2222-3333-444444444444"
)

func TestEvaluateCandidatesRanksHealthyAndWarningReplicasAndBlocksUnsafeReplicas(t *testing.T) {
	healthy := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	lagged := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 2, testPrimaryServerUUID+":1-18")
	stopped := candidateInstance("00000000-0000-4000-8000-000000000030", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	stopped.Replication.SQLThread = model.ThreadStopped
	errant := candidateInstance("00000000-0000-4000-8000-000000000040", "8.0.44", 0, testPrimaryServerUUID+":1-20,"+testErrantServerUUID+":1")

	assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(healthy, lagged, stopped, errant))
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	byID := assessmentsByID(t, assessments, 4)
	assertAssessment(t, byID[healthy.ResourceID], true, 1, "low")
	assertAssessment(t, byID[lagged.ResourceID], true, 2, "warning")
	assertAssessment(t, byID[stopped.ResourceID], false, 0, "blocked")
	assertAssessment(t, byID[errant.ResourceID], false, 0, "blocked")
	if got := assessmentCheckStatus(t, byID[lagged.ResourceID], "replication_lag"); got != model.CheckWarn {
		t.Fatalf("lagged replica check = %s, want warning", got)
	}
	if got := assessmentCheckStatus(t, byID[stopped.ResourceID], "replication_threads"); got != model.CheckFail {
		t.Fatalf("stopped replica check = %s, want fail", got)
	}
	if got := assessmentCheckStatus(t, byID[errant.ResourceID], "gtid_consistency"); got != model.CheckFail {
		t.Fatalf("errant replica check = %s, want fail", got)
	}
}

func TestEvaluateCandidatesUsesPlatformUUIDAsDeterministicTieBreaker(t *testing.T) {
	first := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	second := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")

	assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(second, first))
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 2 || assessments[0].InstanceID != first.ResourceID || assessments[0].Rank != 1 || assessments[1].InstanceID != second.ResourceID || assessments[1].Rank != 2 {
		t.Fatalf("candidate order is not stable by platform UUID: %+v", assessments)
	}
}

func TestEvaluateCandidatesPrefersExactVersionWithinCompatibilityFamily(t *testing.T) {
	exact := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	compatible := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.43", 0, testPrimaryServerUUID+":1-20")

	assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(compatible, exact))
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	byID := assessmentsByID(t, assessments, 2)
	if byID[exact.ResourceID].Rank != 1 || byID[compatible.ResourceID].Rank != 2 {
		t.Fatalf("exact version was not preferred: %+v", assessments)
	}
}

func TestEvaluateCandidatesTreatsReleaseFamiliesAsIncompatible(t *testing.T) {
	for _, test := range []struct {
		primaryVersion   string
		candidateVersion string
	}{
		{primaryVersion: "5.7.44", candidateVersion: "8.0.44"},
		{primaryVersion: "8.0.44", candidateVersion: "8.4.10"},
		{primaryVersion: "8.4.10", candidateVersion: "9.7.0"},
		{primaryVersion: "9.7.0", candidateVersion: "8.4.10"},
	} {
		t.Run(test.primaryVersion+"_to_"+test.candidateVersion, func(t *testing.T) {
			request := candidateEvaluationRequest(candidateInstance("00000000-0000-4000-8000-000000000010", test.candidateVersion, 0, testPrimaryServerUUID+":1-20"))
			request.Primary.EngineMetadata["version"] = test.primaryVersion
			assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate candidates: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "version_compatibility") != model.CheckFail {
				t.Fatalf("incompatible release family was not blocked: %+v", assessments)
			}
		})
	}
}

func TestEvaluateCandidatesFailsClosedOnUnsafeOrMissingState(t *testing.T) {
	for _, test := range []struct {
		name      string
		checkName string
		mutate    func(*model.DatabaseInstance)
	}{
		{name: "outside inventory", checkName: "inventory_membership", mutate: func(instance *model.DatabaseInstance) {
			instance.ClusterID = model.ResourceID("00000000-0000-4000-8000-000000000099")
		}},
		{name: "primary role", checkName: "candidate_role", mutate: func(instance *model.DatabaseInstance) { instance.Role = model.RolePrimary }},
		{name: "promotion disabled", checkName: "promotion_eligibility", mutate: func(instance *model.DatabaseInstance) { instance.PromotionEligible = false }},
		{name: "writable replica", checkName: "replica_read_only", mutate: func(instance *model.DatabaseInstance) {
			instance.EngineMetadata["read_only"] = "false"
			instance.EngineMetadata["super_read_only"] = "false"
		}},
		{name: "maintenance", checkName: "maintenance", mutate: func(instance *model.DatabaseInstance) { instance.Maintenance = true }},
		{name: "stopped IO thread", checkName: "replication_threads", mutate: func(instance *model.DatabaseInstance) { instance.Replication.IOThread = model.ThreadStopped }},
		{name: "missing source", checkName: "replication_source", mutate: func(instance *model.DatabaseInstance) { instance.Replication.SourceIdentity = nil }},
		{name: "unknown lag", checkName: "replication_lag", mutate: func(instance *model.DatabaseInstance) { instance.Replication.LagSeconds = nil }},
		{name: "excessive lag", checkName: "replication_lag", mutate: func(instance *model.DatabaseInstance) { lag := int64(6); instance.Replication.LagSeconds = &lag }},
		{name: "GTID disabled", checkName: "gtid_mode", mutate: func(instance *model.DatabaseInstance) { instance.EngineMetadata["gtid_mode"] = "OFF" }},
		{name: "malformed GTID", checkName: "gtid_consistency", mutate: func(instance *model.DatabaseInstance) { instance.Replication.ExecutedPosition = "not-a-gtid" }},
		{name: "missing version", checkName: "version_compatibility", mutate: func(instance *model.DatabaseInstance) { delete(instance.EngineMetadata, "version") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
			test.mutate(&instance)
			assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(instance))
			if err != nil {
				t.Fatalf("evaluate candidates: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessments[0].Rank != 0 {
				t.Fatalf("unsafe candidate was eligible: %+v", assessments)
			}
			if got := assessmentCheckStatus(t, assessments[0], test.checkName); got != model.CheckFail {
				t.Fatalf("%s check = %s, want fail", test.checkName, got)
			}
		})
	}
}

func TestEvaluateCandidatesRejectsStaleOrUndatedBoundProbe(t *testing.T) {
	for _, test := range []struct {
		name       string
		observedAt time.Time
	}{
		{name: "stale", observedAt: time.Date(2026, time.July, 11, 11, 59, 59, 0, time.UTC)},
		{name: "undated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
			request := candidateEvaluationRequest(instance)
			request.Probes[0].DiscoveryObservedAt = test.observedAt
			assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate candidates: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "reachability") != model.CheckFail {
				t.Fatalf("%s bound probe from another cycle was accepted: %+v", test.name, assessments)
			}
		})
	}
}

func TestEvaluateCandidatesWarnsForExplicitFailedUnboundProbe(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	request := candidateEvaluationRequest(instance)
	request.Probes = append(request.Probes, model.ProbeStatus{
		EndpointID: model.ResourceID("00000000-0000-4000-8000-000000000050"),
		Health:     model.Health{State: model.HealthUnknown, Summary: "probe failed"},
	})

	assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 1 || !assessments[0].Eligible || assessments[0].Rank != 1 || assessments[0].RiskLevel != "warning" {
		t.Fatalf("failed unbound probe must warn without blocking: %+v", assessments)
	}
	if got := assessmentCheckStatus(t, assessments[0], "probe_coverage"); got != model.CheckWarn {
		t.Fatalf("probe coverage check = %s, want warning", got)
	}
}

func TestEvaluateCandidatesRequiresHealthyBoundProbeEvidence(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	for _, test := range []struct {
		name             string
		probes           []model.ProbeStatus
		coverageMustWarn bool
	}{
		{name: "empty probes", coverageMustWarn: true},
		{name: "no bound probe", probes: []model.ProbeStatus{{
			EndpointID: model.ResourceID("00000000-0000-4000-8000-000000000060"),
			InstanceID: model.ResourceID("00000000-0000-4000-8000-000000000099"),
			Health:     model.Health{State: model.HealthHealthy},
		}}},
		{name: "bound unknown probe", probes: []model.ProbeStatus{candidateProbe(instance.ResourceID, model.HealthUnknown)}, coverageMustWarn: true},
		{name: "bound degraded probe", probes: []model.ProbeStatus{candidateProbe(instance.ResourceID, model.HealthDegraded)}, coverageMustWarn: true},
		{name: "bound unhealthy probe", probes: []model.ProbeStatus{candidateProbe(instance.ResourceID, model.HealthUnhealthy)}, coverageMustWarn: true},
		{name: "mixed healthy and unknown bound probes", probes: []model.ProbeStatus{
			candidateProbe(instance.ResourceID, model.HealthHealthy),
			candidateProbe(instance.ResourceID, model.HealthUnknown),
		}, coverageMustWarn: true},
		{name: "mixed healthy and degraded bound probes", probes: []model.ProbeStatus{
			candidateProbe(instance.ResourceID, model.HealthHealthy),
			candidateProbe(instance.ResourceID, model.HealthDegraded),
		}, coverageMustWarn: true},
		{name: "mixed healthy and unhealthy bound probes", probes: []model.ProbeStatus{
			candidateProbe(instance.ResourceID, model.HealthHealthy),
			candidateProbe(instance.ResourceID, model.HealthUnhealthy),
		}, coverageMustWarn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := candidateEvaluationRequest(instance)
			request.Probes = test.probes
			assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate candidates: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessments[0].Rank != 0 {
				t.Fatalf("candidate without healthy bound probe was eligible: %+v", assessments)
			}
			for _, checkName := range []string{"inventory_membership", "reachability"} {
				if got := assessmentCheckStatus(t, assessments[0], checkName); got != model.CheckFail {
					t.Fatalf("%s check = %s, want fail", checkName, got)
				}
			}
			if test.coverageMustWarn && assessmentCheckStatus(t, assessments[0], "probe_coverage") != model.CheckWarn {
				t.Fatalf("probe coverage did not warn: %+v", assessments[0])
			}
		})
	}
}

func TestEvaluateCandidatesUsesBoundProbeInsteadOfCandidateHealthField(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	instance.Health.State = model.HealthUnknown

	assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(instance))
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 1 || !assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "reachability") != model.CheckPass {
		t.Fatalf("healthy bound probe was not authoritative: %+v", assessments)
	}
}

func TestEvaluateCandidatesUsesPrimaryGlobalGTIDAndReplicaExecutedGTID(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	request := candidateEvaluationRequest(instance)
	request.Primary.Replication.ExecutedPosition = testPrimaryServerUUID + ":1-100," + testErrantServerUUID + ":1"

	assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 1 || !assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "gtid_consistency") != model.CheckPass {
		t.Fatalf("evaluator did not use authoritative primary global GTID: %+v", assessments)
	}
}

func TestEvaluateCandidatesWarnsForPrimaryOwnedGTIDFromLaterReplicaSample(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-21")
	request := candidateEvaluationRequest(instance)
	request.Primary.Health.ObservedAt = request.ObservedAt.Add(-25 * time.Millisecond)
	request.Instances[0].Health.ObservedAt = request.ObservedAt

	assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 1 || !assessments[0].Eligible || assessments[0].RiskLevel != "warning" {
		t.Fatalf("later replica sample owned by the primary must require live validation without blocking: %+v", assessments)
	}
	if got := assessmentCheckStatus(t, assessments[0], "gtid_consistency"); got != model.CheckWarn {
		t.Fatalf("GTID consistency check = %s, want warning", got)
	}
}

func TestEvaluateCandidatesFailsClosedOnGTIDComparisonOverflow(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, "")
	request := candidateEvaluationRequest(instance)
	request.Primary.EngineMetadata["gtid_executed"] = testPrimaryServerUUID + ":1-18446744073709551615," + testErrantServerUUID + ":1"

	assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	if len(assessments) != 1 || assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "gtid_consistency") != model.CheckFail {
		t.Fatalf("GTID comparison overflow did not block candidate: %+v", assessments)
	}
	if assessments[0].DataLossRisk != "unknown" {
		t.Fatalf("overflow data loss risk = %q, want unknown", assessments[0].DataLossRisk)
	}
}

func TestEvaluateCandidatesReportsCoherentDataLossRiskForGTIDDivergence(t *testing.T) {
	for _, test := range []struct {
		name             string
		executedGTID     string
		wantDataLossRisk string
	}{
		{
			name:             "missing and errant",
			executedGTID:     testPrimaryServerUUID + ":1-18," + testErrantServerUUID + ":1",
			wantDataLossRisk: "2 missing transactions",
		},
		{
			name:             "errant only",
			executedGTID:     testPrimaryServerUUID + ":1-20," + testErrantServerUUID + ":1",
			wantDataLossRisk: "unknown",
		},
		{
			name:             "malformed",
			executedGTID:     "not-a-gtid",
			wantDataLossRisk: "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, test.executedGTID)
			assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(instance))
			if err != nil {
				t.Fatalf("evaluate candidates: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessmentCheckStatus(t, assessments[0], "gtid_consistency") != model.CheckFail {
				t.Fatalf("unsafe GTID divergence was not blocked: %+v", assessments)
			}
			if assessments[0].DataLossRisk != test.wantDataLossRisk {
				t.Fatalf("data loss risk = %q, want %q", assessments[0].DataLossRisk, test.wantDataLossRisk)
			}
		})
	}
}

func TestEvaluateCandidatesWarnsForMissingTransactionsAtZeroLag(t *testing.T) {
	missing := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-19")
	caughtUp := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")

	assessments, err := New(nil).EvaluateCandidates(context.Background(), candidateEvaluationRequest(missing, caughtUp))
	if err != nil {
		t.Fatalf("evaluate candidates: %v", err)
	}
	byID := assessmentsByID(t, assessments, 2)
	assertAssessment(t, byID[caughtUp.ResourceID], true, 1, "low")
	assertAssessment(t, byID[missing.ResourceID], true, 2, "warning")
	if got := assessmentCheckStatus(t, byID[missing.ResourceID], "gtid_consistency"); got != model.CheckWarn {
		t.Fatalf("missing transaction GTID check = %s, want warning", got)
	}
	if got := byID[missing.ResourceID].DataLossRisk; got != "1 missing transactions" {
		t.Fatalf("data loss risk = %q, want missing transaction count", got)
	}
	if assessments[0].InstanceID != caughtUp.ResourceID {
		t.Fatalf("warning-free caught-up candidate did not rank first: %+v", assessments)
	}
}

func TestEvaluateCandidatesAllowsSourceReconnectOnlyForConfirmedFailover(t *testing.T) {
	candidate := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	candidate.Health.State = model.HealthDegraded
	candidate.PromotionEligible = false
	candidate.Replication.IOThread = model.ThreadConnecting
	candidate.Replication.LagSeconds = nil
	candidate.Replication.LastIOError = "Error reconnecting to source"

	strictRequest := candidateEvaluationRequest(candidate)
	strictRequest.Primary.Health.State = model.HealthUnknown
	strictRequest.Probes[0].Health.State = model.HealthDegraded
	strict, err := New(nil).EvaluateCandidates(context.Background(), strictRequest)
	if err != nil {
		t.Fatalf("strict evaluation: %v", err)
	}
	if len(strict) != 1 || strict[0].Eligible {
		t.Fatalf("planned switchover rules accepted a disconnected source: %+v", strict)
	}

	failoverRequest := strictRequest
	failoverRequest.Policy.AllowSourceDisconnected = true
	failover, err := New(nil).EvaluateCandidates(context.Background(), failoverRequest)
	if err != nil {
		t.Fatalf("failover evaluation: %v", err)
	}
	if len(failover) != 1 || !failover[0].Eligible || failover[0].Rank != 1 || failover[0].DataLossRisk != dataLossRiskNone {
		t.Fatalf("safe source-loss failover candidate was blocked: %+v", failover)
	}
	if assessmentCheckStatus(t, failover[0], "replication_threads") != model.CheckWarn || assessmentCheckStatus(t, failover[0], "replication_lag") != model.CheckWarn {
		t.Fatalf("source-loss evidence must remain visible as warnings: %+v", failover[0])
	}
}

func TestEvaluateFailoverCandidateStillBlocksUnsafeReplicationState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*model.DatabaseInstance)
	}{
		{name: "IO thread stopped", mutate: func(instance *model.DatabaseInstance) { instance.Replication.IOThread = model.ThreadStopped }},
		{name: "SQL thread stopped", mutate: func(instance *model.DatabaseInstance) { instance.Replication.SQLThread = model.ThreadStopped }},
		{name: "SQL error", mutate: func(instance *model.DatabaseInstance) { instance.Replication.LastSQLError = "duplicate key" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
			candidate.Health.State = model.HealthDegraded
			candidate.PromotionEligible = false
			candidate.Replication.IOThread = model.ThreadConnecting
			candidate.Replication.LagSeconds = nil
			test.mutate(&candidate)
			request := candidateEvaluationRequest(candidate)
			request.Primary.Health.State = model.HealthUnhealthy
			request.Probes[0].Health.State = model.HealthDegraded
			request.Policy.AllowSourceDisconnected = true
			assessments, err := New(nil).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate failover candidate: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible {
				t.Fatalf("unsafe failover candidate was accepted: %+v", assessments)
			}
		})
	}
}

func candidateEvaluationRequest(instances ...model.DatabaseInstance) adapter.CandidateRequest {
	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	probes := make([]model.ProbeStatus, 0, len(instances))
	for _, instance := range instances {
		probe := candidateProbe(instance.ResourceID, model.HealthHealthy)
		probe.DiscoveryObservedAt = observedAt
		probes = append(probes, probe)
	}
	return adapter.CandidateRequest{
		Cluster: model.DatabaseCluster{
			ResourceMeta: model.ResourceMeta{ResourceID: testClusterID},
			Engine:       model.EngineMySQL,
		},
		Primary: model.DatabaseInstance{
			ResourceMeta:   model.ResourceMeta{ResourceID: testPrimaryID},
			ClusterID:      testClusterID,
			Engine:         model.EngineMySQL,
			EngineIdentity: model.EngineIdentity{"server_uuid": testPrimaryServerUUID},
			Role:           model.RolePrimary,
			Health:         model.Health{State: model.HealthHealthy},
			EngineMetadata: map[string]string{"version": "8.0.44", "gtid_mode": "ON", "gtid_executed": testPrimaryServerUUID + ":1-20"},
		},
		Instances:  instances,
		Probes:     probes,
		ObservedAt: observedAt,
		Policy:     model.CandidatePolicy{MaximumLagSeconds: 5, RequireGTID: true},
	}
}

func candidateProbe(instanceID model.ResourceID, state model.HealthState) model.ProbeStatus {
	return model.ProbeStatus{
		EndpointID: instanceID,
		InstanceID: instanceID,
		Health:     model.Health{State: state},
	}
}

func candidateInstance(id, version string, lag int64, executedGTID string) model.DatabaseInstance {
	return model.DatabaseInstance{
		ResourceMeta:      model.ResourceMeta{ResourceID: model.ResourceID(id)},
		ClusterID:         testClusterID,
		Engine:            model.EngineMySQL,
		EngineIdentity:    model.EngineIdentity{"server_uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"},
		Role:              model.RoleReplica,
		Health:            model.Health{State: model.HealthHealthy},
		PromotionEligible: true,
		Replication: model.ReplicationStatus{
			SourceIdentity:   model.EngineIdentity{"server_uuid": testPrimaryServerUUID},
			IOThread:         model.ThreadRunning,
			SQLThread:        model.ThreadRunning,
			LagSeconds:       &lag,
			ExecutedPosition: executedGTID,
		},
		EngineMetadata: map[string]string{"version": version, "gtid_mode": "ON", "read_only": "true", "super_read_only": "true"},
	}
}

func assessmentsByID(t *testing.T, assessments []model.CandidateAssessment, want int) map[model.ResourceID]model.CandidateAssessment {
	t.Helper()
	if len(assessments) != want {
		t.Fatalf("assessment count = %d, want %d: %+v", len(assessments), want, assessments)
	}
	result := make(map[model.ResourceID]model.CandidateAssessment, len(assessments))
	for _, assessment := range assessments {
		result[assessment.InstanceID] = assessment
	}
	return result
}

func assertAssessment(t *testing.T, assessment model.CandidateAssessment, eligible bool, rank int, risk string) {
	t.Helper()
	if assessment.Eligible != eligible || assessment.Rank != rank || assessment.RiskLevel != risk {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
}

func assessmentCheckStatus(t *testing.T, assessment model.CandidateAssessment, name string) model.CheckStatus {
	t.Helper()
	for _, check := range assessment.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	t.Fatalf("assessment %s has no %s check: %+v", assessment.InstanceID, name, assessment.Checks)
	return ""
}
