package coordination

import (
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
)

func TestAgentAuthorizationTrackerKeepsLatestPositiveAuthorizationPerLeadershipTerm(t *testing.T) {
	tracker := NewAgentAuthorizationTracker()
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	now := time.Now().UTC()

	tracker.Record(agent.ReconcileResponse{
		ClusterID: clusterID, InstanceID: instanceID, Action: agent.ReconcileKeepVIP,
		LeaseID: model.NewResourceID(), ValidUntil: now.Add(10 * time.Second),
	}, 7)
	tracker.Record(agent.ReconcileResponse{
		ClusterID: clusterID, InstanceID: instanceID, Action: agent.ReconcileKeepVIP,
		LeaseID: model.NewResourceID(), ValidUntil: now.Add(5 * time.Second),
	}, 7)

	validUntil, found := tracker.AuthorizationUntil(clusterID, instanceID, 7)
	if !found || !validUntil.Equal(now.Add(10*time.Second)) {
		t.Fatalf("authorization=(%s,%t), want latest positive authorization", validUntil, found)
	}
	if _, found := tracker.AuthorizationUntil(clusterID, instanceID, 8); found {
		t.Fatal("authorization from a previous Raft term remained trusted")
	}
}

func TestAgentAuthorizationTrackerDoesNotErasePossibleCachedAuthorizationOnIsolationDecision(t *testing.T) {
	tracker := NewAgentAuthorizationTracker()
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	validUntil := time.Now().UTC().Add(10 * time.Second)

	tracker.Record(agent.ReconcileResponse{
		ClusterID: clusterID, InstanceID: instanceID, Action: agent.ReconcileKeepVIP,
		LeaseID: model.NewResourceID(), ValidUntil: validUntil,
	}, 3)
	tracker.Record(agent.ReconcileResponse{
		ClusterID: clusterID, InstanceID: instanceID, Action: agent.ReconcileSelfIsolate,
		ValidUntil: validUntil.Add(time.Second),
	}, 3)

	stored, found := tracker.AuthorizationUntil(clusterID, instanceID, 3)
	if !found || !stored.Equal(validUntil) {
		t.Fatalf("cached authorization=(%s,%t), want %s", stored, found, validUntil)
	}
}

func TestAgentAuthorizationTrackerSerializesDecisionIssuanceWithTransitionCommit(t *testing.T) {
	tracker := NewAgentAuthorizationTracker()
	releaseDecision := tracker.BeginDecision()
	transitionStarted := make(chan struct{})
	transitionAcquired := make(chan struct{})
	go func() {
		close(transitionStarted)
		releaseTransition := tracker.BeginTransition()
		close(transitionAcquired)
		releaseTransition()
	}()
	<-transitionStarted

	select {
	case <-transitionAcquired:
		t.Fatal("transition commit raced ahead of an in-flight authorization decision")
	case <-time.After(20 * time.Millisecond):
	}
	releaseDecision()
	select {
	case <-transitionAcquired:
	case <-time.After(time.Second):
		t.Fatal("transition commit did not continue after authorization decision completed")
	}
}
