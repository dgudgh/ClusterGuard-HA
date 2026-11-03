package postgresql

import (
	"clusterguard.io/ha/pkg/model"
	"testing"
)

func TestInstanceWeightsAndAutomaticExclusionPreservePGWALSafety(t *testing.T) {
	req := postgresqlCandidateRequest()
	a := postgresqlCandidate(postgresqlCandidateA, "0/5000060", 0)
	b := postgresqlCandidate(postgresqlCandidateB, "0/5000060", 0)
	req.Instances = []model.DatabaseInstance{a, b}
	req.Probes = []model.ProbeStatus{postgresqlCandidateProbe(postgresqlCandidateA, req.ObservedAt), postgresqlCandidateProbe(postgresqlCandidateB, req.ObservedAt)}
	req.Cluster.FailoverPolicy = &model.InstanceFailoverPolicy{AutomaticEnabled: true, Instances: map[model.ResourceID]model.InstanceFailoverPreference{a.ResourceID: {AutomaticEligible: true, Weight: 10}, b.ResourceID: {AutomaticEligible: true, Weight: 900}}}
	req.Policy.AutomaticSelection = true
	if got := evaluateCandidates(req); got[0].InstanceID != b.ResourceID || !got[0].Eligible {
		t.Fatal("PG weight not applied", got)
	}
	p := req.Cluster.FailoverPolicy.Instances[b.ResourceID]
	p.AutomaticEligible = false
	req.Cluster.FailoverPolicy.Instances[b.ResourceID] = p
	if got := evaluateCandidates(req); got[0].InstanceID != a.ResourceID || got[1].Eligible {
		t.Fatal("PG automatic exclusion ignored", got)
	}
	req.Policy.AutomaticSelection = false
	if got := evaluateCandidates(req); got[0].InstanceID != b.ResourceID || !got[0].Eligible {
		t.Fatal("PG manual selection blocked", got)
	}
	p.AutomaticEligible = true
	req.Cluster.FailoverPolicy.Instances[b.ResourceID] = p
	req.Policy.AutomaticSelection = true
	req.Instances[1].Replication.ExecutedPosition = "0/5000050"
	if got := evaluateCandidates(req); got[0].InstanceID != a.ResourceID {
		t.Fatal("high weight overrode newer WAL", got)
	}
}
