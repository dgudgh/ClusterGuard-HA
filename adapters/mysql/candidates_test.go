package mysql

import (
	"context"
	"testing"

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
		{name: "unknown reachability", checkName: "reachability", mutate: func(instance *model.DatabaseInstance) { instance.Health.State = model.HealthUnknown }},
		{name: "promotion disabled", checkName: "promotion_eligibility", mutate: func(instance *model.DatabaseInstance) { instance.PromotionEligible = false }},
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

func TestEvaluateCandidatesWarnsForExplicitFailedUnboundProbe(t *testing.T) {
	instance := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	request := candidateEvaluationRequest(instance)
	request.Probes = []model.ProbeStatus{{
		EndpointID: model.ResourceID("00000000-0000-4000-8000-000000000050"),
		Health:     model.Health{State: model.HealthUnknown, Summary: "probe failed"},
	}}

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

func candidateEvaluationRequest(instances ...model.DatabaseInstance) adapter.CandidateRequest {
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
		Instances: instances,
		Policy:    model.CandidatePolicy{MaximumLagSeconds: 5, RequireGTID: true},
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
		EngineMetadata: map[string]string{"version": version, "gtid_mode": "ON"},
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
