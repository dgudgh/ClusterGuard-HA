package coordination

import (
	"sync"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
)

type agentAuthorizationKey struct {
	clusterID  model.ResourceID
	instanceID model.ResourceID
}

type agentAuthorizationRecord struct {
	leadershipEpoch uint64
	validUntil      time.Time
}

// AgentAuthorizationTracker coordinates short-lived VIP authorizations with
// failover transitions. Decision issuance takes a shared lock; publishing the
// transition lease takes the exclusive lock. This prevents a stale keep-VIP
// response from being issued after a failover transition has started.
type AgentAuthorizationTracker struct {
	gate    sync.RWMutex
	mu      sync.RWMutex
	records map[agentAuthorizationKey]agentAuthorizationRecord
}

func NewAgentAuthorizationTracker() *AgentAuthorizationTracker {
	return &AgentAuthorizationTracker{records: make(map[agentAuthorizationKey]agentAuthorizationRecord)}
}

func (tracker *AgentAuthorizationTracker) BeginDecision() func() {
	if tracker == nil {
		return func() {}
	}
	tracker.gate.RLock()
	return tracker.gate.RUnlock
}

func (tracker *AgentAuthorizationTracker) BeginTransition() func() {
	if tracker == nil {
		return func() {}
	}
	tracker.gate.Lock()
	return tracker.gate.Unlock
}

func positiveAgentAuthorization(action agent.ReconcileAction) bool {
	switch action {
	case agent.ReconcileKeepVIP, agent.ReconcileTransitionTarget, agent.ReconcileTransitionSource, agent.ReconcileBootstrapPrimary:
		return true
	default:
		return false
	}
}

func (tracker *AgentAuthorizationTracker) Record(response agent.ReconcileResponse, leadershipEpoch uint64) {
	if tracker == nil || leadershipEpoch == 0 || !model.ValidResourceID(response.ClusterID) ||
		!model.ValidResourceID(response.InstanceID) || !model.ValidResourceID(response.LeaseID) ||
		!positiveAgentAuthorization(response.Action) || response.ValidUntil.IsZero() {
		return
	}
	key := agentAuthorizationKey{clusterID: response.ClusterID, instanceID: response.InstanceID}
	record := agentAuthorizationRecord{leadershipEpoch: leadershipEpoch, validUntil: response.ValidUntil.UTC()}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	current, found := tracker.records[key]
	if found && current.leadershipEpoch > leadershipEpoch {
		return
	}
	if found && current.leadershipEpoch == leadershipEpoch && !record.validUntil.After(current.validUntil) {
		return
	}
	tracker.records[key] = record
}

func (tracker *AgentAuthorizationTracker) AuthorizationUntil(clusterID, instanceID model.ResourceID, leadershipEpoch uint64) (time.Time, bool) {
	if tracker == nil || leadershipEpoch == 0 || !model.ValidResourceID(clusterID) || !model.ValidResourceID(instanceID) {
		return time.Time{}, false
	}
	tracker.mu.RLock()
	record, found := tracker.records[agentAuthorizationKey{clusterID: clusterID, instanceID: instanceID}]
	tracker.mu.RUnlock()
	if !found || record.leadershipEpoch != leadershipEpoch || record.validUntil.IsZero() {
		return time.Time{}, false
	}
	return record.validUntil, true
}
