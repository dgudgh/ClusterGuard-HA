package workflow

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"testing"
)

func TestAutomaticExecutionPolicyGuardRejectsExplicitOffAndExcludedInstance(t *testing.T) {
	id := model.NewResourceID()
	policy := &model.InstanceFailoverPolicy{AutomaticEnabled: false, Instances: map[model.ResourceID]model.InstanceFailoverPreference{id: {AutomaticEligible: true, Weight: 100}}}
	revision := uint64(0)
	req := adapter.OperationRequest{AutomaticPolicyRevision: &revision, TargetID: id, Resolved: &adapter.ResolvedOperation{Cluster: model.DatabaseCluster{FailoverPolicy: policy}}}
	if automaticInstancePolicyCheck(req) == nil {
		t.Fatal("automatic disabled bypassed")
	}
	policy.AutomaticEnabled = true
	policy.Instances[id] = model.InstanceFailoverPreference{Weight: 100}
	if automaticInstancePolicyCheck(req) == nil {
		t.Fatal("excluded target bypassed")
	}
	policy.Instances[id] = model.InstanceFailoverPreference{AutomaticEligible: true, Weight: 100}
	if err := automaticInstancePolicyCheck(req); err != nil {
		t.Fatal(err)
	}
	revision = 1
	if automaticInstancePolicyCheck(req) == nil {
		t.Fatal("stale candidate selection was accepted")
	}
}
