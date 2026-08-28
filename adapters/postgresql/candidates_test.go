package postgresql

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	postgresqlCandidateA = "44444444-4444-4444-8444-444444444444"
	postgresqlCandidateB = "55555555-5555-4555-8555-555555555555"
)

func TestEvaluateCandidatesRanksHighestReplayLSNBeforeLag(t *testing.T) {
	request := postgresqlCandidateRequest()
	request.Instances = []model.DatabaseInstance{
		postgresqlCandidate(postgresqlCandidateA, "0/5000040", 1),
		postgresqlCandidate(postgresqlCandidateB, "0/5000050", 3),
	}
	request.Probes = []model.ProbeStatus{
		postgresqlCandidateProbe(postgresqlCandidateA, request.ObservedAt),
		postgresqlCandidateProbe(postgresqlCandidateB, request.ObservedAt),
	}

	assessments, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(assessments) != 2 || assessments[0].InstanceID != postgresqlCandidateB || assessments[0].Rank != 1 || !assessments[0].Eligible {
		t.Fatalf("unexpected ranking: %+v", assessments)
	}
	if assessments[1].InstanceID != postgresqlCandidateA || assessments[1].Rank != 2 || !assessments[1].Eligible {
		t.Fatalf("unexpected second candidate: %+v", assessments)
	}
}

func TestEvaluateCandidatesBlocksUnknownLagTimelineAndIdentityMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*model.DatabaseInstance){
		"unknown lag":       func(instance *model.DatabaseInstance) { instance.Replication.LagSeconds = nil },
		"timeline mismatch": func(instance *model.DatabaseInstance) { instance.EngineMetadata["timeline_id"] = "6" },
		"system mismatch":   func(instance *model.DatabaseInstance) { instance.EngineIdentity["system_identifier"] = "999" },
		"source mismatch": func(instance *model.DatabaseInstance) {
			instance.Replication.SourceIdentity["resource_id"] = postgresqlCandidateB
		},
		"paused replay": func(instance *model.DatabaseInstance) {
			instance.EngineMetadata["replay_paused"] = "true"
			instance.Replication.SQLThread = model.ThreadStopped
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := postgresqlCandidateRequest()
			instance := postgresqlCandidate(postgresqlCandidateA, "0/5000050", 0)
			mutate(&instance)
			request.Instances = []model.DatabaseInstance{instance}
			request.Probes = []model.ProbeStatus{postgresqlCandidateProbe(postgresqlCandidateA, request.ObservedAt)}
			assessments, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible || assessments[0].Rank != 0 || assessments[0].RiskLevel != "blocked" {
				t.Fatalf("unsafe candidate was not blocked: %+v", assessments)
			}
		})
	}
}

func TestEvaluateCandidatesRequiresCurrentBoundProbeEvidence(t *testing.T) {
	request := postgresqlCandidateRequest()
	request.Instances = []model.DatabaseInstance{postgresqlCandidate(postgresqlCandidateA, "0/5000050", 0)}
	request.Probes = []model.ProbeStatus{postgresqlCandidateProbe(postgresqlCandidateA, request.ObservedAt.Add(-time.Second))}
	assessments, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(assessments) != 1 || assessments[0].Eligible {
		t.Fatalf("stale probe candidate was not blocked: %+v", assessments)
	}
}

func TestEvaluateCandidatesAllowsCaughtUpStandbyAfterConfirmedSourceLoss(t *testing.T) {
	request := postgresqlCandidateRequest()
	candidate := postgresqlCandidate(postgresqlCandidateA, "0/5000070", 0)
	candidate.Health.State = model.HealthDegraded
	candidate.PromotionEligible = false
	candidate.Replication.IOThread = model.ThreadStopped
	candidate.Replication.RetrievedPosition = "0/5000070"
	request.Primary.Health.State = model.HealthUnknown
	request.Primary.Role = model.RoleUnknown
	request.Instances = []model.DatabaseInstance{candidate}
	probe := postgresqlCandidateProbe(postgresqlCandidateA, request.ObservedAt)
	probe.Health.State = model.HealthDegraded
	request.Probes = []model.ProbeStatus{probe}

	strict, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("strict evaluate: %v", err)
	}
	if len(strict) != 1 || strict[0].Eligible {
		t.Fatalf("planned candidate rules accepted a disconnected WAL receiver: %+v", strict)
	}

	request.Policy.AllowSourceDisconnected = true
	failover, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("failover evaluate: %v", err)
	}
	if len(failover) != 1 || !failover[0].Eligible || failover[0].Rank != 1 || failover[0].DataLossRisk != "none" {
		t.Fatalf("safe source-loss PostgreSQL candidate was blocked: %+v", failover)
	}
	for _, checkName := range []string{"probe_evidence", "candidate_health", "wal_streaming"} {
		if status := postgresqlAssessmentCheckStatus(t, failover[0], checkName); status != model.CheckWarn {
			t.Fatalf("%s status = %s, want warning: %+v", checkName, status, failover[0])
		}
	}
}

func TestEvaluateSourceLossCandidateKeepsUnsafeEvidenceBlocked(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*adapter.CandidateRequest, *model.DatabaseInstance, *model.ProbeStatus)
	}{
		{name: "primary still healthy", mutate: func(request *adapter.CandidateRequest, _ *model.DatabaseInstance, _ *model.ProbeStatus) {
			request.Primary.Health.State = model.HealthHealthy
		}},
		{name: "source reports standby", mutate: func(request *adapter.CandidateRequest, _ *model.DatabaseInstance, _ *model.ProbeStatus) {
			request.Primary.Role = model.RoleStandby
		}},
		{name: "stale bound probe", mutate: func(request *adapter.CandidateRequest, _ *model.DatabaseInstance, probe *model.ProbeStatus) {
			probe.DiscoveryObservedAt = request.ObservedAt.Add(-time.Second)
		}},
		{name: "unknown lag", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.Replication.LagSeconds = nil
		}},
		{name: "nonzero lag", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			lag := int64(1)
			candidate.Replication.LagSeconds = &lag
		}},
		{name: "receive not replayed", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.Replication.RetrievedPosition = "0/5000080"
		}},
		{name: "replay behind primary", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.Replication.RetrievedPosition = "0/5000050"
			candidate.Replication.ExecutedPosition = "0/5000050"
			candidate.EngineMetadata["receive_lsn"] = "0/5000050"
			candidate.EngineMetadata["replay_lsn"] = "0/5000050"
		}},
		{name: "replay paused", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.EngineMetadata["replay_paused"] = "true"
		}},
		{name: "SQL replay stopped", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.Replication.SQLThread = model.ThreadStopped
		}},
		{name: "not in recovery", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.EngineMetadata["in_recovery"] = "false"
		}},
		{name: "not read only", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.EngineMetadata["transaction_read_only"] = "false"
		}},
		{name: "source identity mismatch", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.Replication.SourceIdentity["resource_id"] = postgresqlCandidateB
		}},
		{name: "timeline mismatch", mutate: func(_ *adapter.CandidateRequest, candidate *model.DatabaseInstance, _ *model.ProbeStatus) {
			candidate.EngineMetadata["timeline_id"] = "8"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
				request := postgresqlCandidateRequest()
				request.Primary.Health.State = model.HealthUnknown
				request.Primary.Role = model.RoleUnknown
				request.Policy.AllowSourceDisconnected = true
			candidate := postgresqlCandidate(postgresqlCandidateA, "0/5000070", 0)
			candidate.Health.State = model.HealthDegraded
			candidate.PromotionEligible = false
			candidate.Replication.IOThread = model.ThreadStopped
			candidate.Replication.RetrievedPosition = "0/5000070"
			probe := postgresqlCandidateProbe(postgresqlCandidateA, request.ObservedAt)
			probe.Health.State = model.HealthDegraded
			test.mutate(&request, &candidate, &probe)
			request.Instances = []model.DatabaseInstance{candidate}
			request.Probes = []model.ProbeStatus{probe}

			assessments, err := New(&fakeRunner{}).EvaluateCandidates(context.Background(), request)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if len(assessments) != 1 || assessments[0].Eligible {
				t.Fatalf("unsafe source-loss candidate was accepted: %+v", assessments)
			}
		})
	}
}

func postgresqlAssessmentCheckStatus(t *testing.T, assessment model.CandidateAssessment, name string) model.CheckStatus {
	t.Helper()
	for _, check := range assessment.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	t.Fatalf("check %q is missing: %+v", name, assessment.Checks)
	return ""
}

func postgresqlCandidateRequest() adapter.CandidateRequest {
	observedAt := time.Now().UTC().Truncate(time.Second)
	clusterID := model.ResourceID(testPostgreSQLClusterID)
	primaryID := model.ResourceID(testPostgreSQLPrimaryID)
	return adapter.CandidateRequest{
		Cluster: model.DatabaseCluster{
			ResourceMeta:   model.ResourceMeta{ResourceID: clusterID},
			Engine:         model.EnginePostgreSQL,
			EngineIdentity: model.EngineIdentity{"system_identifier": "7428625847249870011"},
		},
		Primary: model.DatabaseInstance{
			ResourceMeta:   model.ResourceMeta{ResourceID: primaryID},
			ClusterID:      clusterID,
			Engine:         model.EnginePostgreSQL,
			EngineIdentity: model.EngineIdentity{"resource_id": string(primaryID), "system_identifier": "7428625847249870011"},
			Role:           model.RolePrimary,
			Health:         model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
			EngineMetadata: map[string]string{"timeline_id": "7", "current_lsn": "0/5000060"},
		},
		ObservedAt: observedAt,
		Policy:     model.CandidatePolicy{MaximumLagSeconds: 10},
	}
}

func postgresqlCandidate(resourceID string, replayLSN string, lag int64) model.DatabaseInstance {
	clusterID := model.ResourceID(testPostgreSQLClusterID)
	return model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(resourceID)},
		ClusterID:    clusterID,
		Engine:       model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       resourceID,
			"system_identifier": "7428625847249870011",
		},
		Role:   model.RoleStandby,
		Health: model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			SourceIdentity:   model.EngineIdentity{"resource_id": testPostgreSQLPrimaryID, "system_identifier": "7428625847249870011"},
			IOThread:         model.ThreadRunning,
			SQLThread:        model.ThreadRunning,
			LagSeconds:       &lag,
			ExecutedPosition: replayLSN,
		},
		PromotionEligible: true,
		EngineMetadata: map[string]string{
			"timeline_id":           "7",
			"receive_lsn":           replayLSN,
			"replay_lsn":            replayLSN,
			"replay_paused":         "false",
			"in_recovery":           "true",
			"transaction_read_only": "true",
		},
	}
}

func postgresqlCandidateProbe(resourceID string, observedAt time.Time) model.ProbeStatus {
	return model.ProbeStatus{
		EndpointID:          model.NewResourceID(),
		InstanceID:          model.ResourceID(resourceID),
		DiscoveryObservedAt: observedAt,
		Health:              model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
	}
}
