package mysql

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"testing"
	"time"
)

func TestInstanceWeightsAndAutomaticExclusionPreserveMySQLSafety(t *testing.T) {
	a := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	b := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	req := candidateEvaluationRequest(a, b)
	req.Cluster.FailoverPolicy = &model.InstanceFailoverPolicy{AutomaticEnabled: true, Instances: map[model.ResourceID]model.InstanceFailoverPreference{a.ResourceID: {AutomaticEligible: true, Weight: 10}, b.ResourceID: {AutomaticEligible: true, Weight: 900}}}
	req.Policy.AutomaticSelection = true
	if got := evaluateCandidates(req); got[0].InstanceID != b.ResourceID || got[0].Rank != 1 {
		t.Fatal("weight not used", got)
	}
	p := req.Cluster.FailoverPolicy.Instances[b.ResourceID]
	p.AutomaticEligible = false
	req.Cluster.FailoverPolicy.Instances[b.ResourceID] = p
	if got := evaluateCandidates(req); got[0].InstanceID != a.ResourceID || got[1].Eligible {
		t.Fatal("automatic exclusion ignored", got)
	}
	req.Policy.AutomaticSelection = false
	if got := evaluateCandidates(req); got[0].InstanceID != b.ResourceID || !got[0].Eligible {
		t.Fatal("automatic exclusion blocked manual target", got)
	}
	req.Policy.AutomaticSelection = true
	p.AutomaticEligible = true
	req.Cluster.FailoverPolicy.Instances[b.ResourceID] = p
	req.Instances[1].Replication.ExecutedPosition = testPrimaryServerUUID + ":1-19"
	if got := evaluateCandidates(req); got[0].InstanceID != a.ResourceID {
		t.Fatal("high weight displaced a more complete candidate", got)
	}
	req.Instances[1].Replication.SQLThread = model.ThreadStopped
	if got := evaluateCandidates(req); got[0].InstanceID != a.ResourceID || got[1].Eligible {
		t.Fatal("weight bypassed replication safety", got)
	}
}

func TestAutomaticallyExcludedMySQLInstanceStillRepointsAsFollower(t *testing.T) {
	a := candidateInstance("00000000-0000-4000-8000-000000000010", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	b := candidateInstance("00000000-0000-4000-8000-000000000020", "8.0.44", 0, testPrimaryServerUUID+":1-20")
	req := candidateEvaluationRequest(a, b)
	req.Primary.Health.State = model.HealthUnhealthy
	req.Cluster.FailoverPolicy = &model.InstanceFailoverPolicy{AutomaticEnabled: true, Instances: map[model.ResourceID]model.InstanceFailoverPreference{a.ResourceID: {AutomaticEligible: true, Weight: 10}, b.ResourceID: {AutomaticEligible: false, Weight: 900}}}
	resolved := adapter.ResolvedOperation{Cluster: req.Cluster, Primary: req.Primary, Target: a, AutomaticFailureIncidentAt: time.Now(), Snapshot: model.TopologySnapshot{Instances: append([]model.DatabaseInstance{req.Primary}, req.Instances...), Probes: req.Probes, ObservedAt: req.ObservedAt}}
	followers := New(nil).eligibleFailoverFollowers(&resolved)
	if len(followers) != 1 || followers[0].ResourceID != b.ResourceID {
		t.Fatal("automatic eligibility incorrectly disabled replication follow-up", followers)
	}
}
