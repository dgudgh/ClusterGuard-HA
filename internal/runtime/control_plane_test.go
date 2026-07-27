package runtime

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type consensusStatusStub struct{ status consensus.Status }

func (stub consensusStatusStub) Status(context.Context) consensus.Status { return stub.status }

func TestControlPlaneStatusReadinessUsesLeaderQuorumAndFollowerCatchup(t *testing.T) {
	repository := store.NewMemory()
	startedAt := time.Now().UTC().Add(-10 * time.Minute)
	leaderID := model.NewResourceID()
	tests := []struct {
		name   string
		status consensus.Status
		ready  bool
		reason string
	}{
		{
			name: "leader with quorum", ready: true, reason: "ready",
			status: consensus.Status{Enabled: true, Role: "leader", LeaderKnown: true, LeaderID: leaderID, LeaderAPIAddress: "https://controller-a:8088", QuorumConfirmed: true, MutationAuthority: true, CommitIndex: 12, AppliedIndex: 12},
		},
		{
			name: "leader without quorum", ready: false, reason: "quorum_unavailable",
			status: consensus.Status{Enabled: true, Role: "leader", LeaderKnown: true, LeaderID: leaderID, QuorumConfirmed: false, CommitIndex: 12, AppliedIndex: 12},
		},
		{
			name: "caught up follower", ready: true, reason: "ready",
			status: consensus.Status{Enabled: true, Role: "follower", LeaderKnown: true, LeaderID: leaderID, LeaderAPIAddress: "https://controller-a:8088", CommitIndex: 12, AppliedIndex: 12},
		},
		{
			name: "follower catching up", ready: false, reason: "metadata_catchup",
			status: consensus.Status{Enabled: true, Role: "follower", LeaderKnown: true, LeaderID: leaderID, LeaderAPIAddress: "https://controller-a:8088", CommitIndex: 13, AppliedIndex: 12},
		},
		{
			name: "follower without trusted leader API", ready: false, reason: "leader_api_unavailable",
			status: consensus.Status{Enabled: true, Role: "follower", LeaderKnown: true, LeaderID: leaderID, CommitIndex: 12, AppliedIndex: 12},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := newControlPlaneStatusProvider(repository, consensusStatusStub{status: test.status}, startedAt)
			status, err := provider.ControlPlaneStatus(context.Background())
			if err != nil || status.Ready != test.ready || status.ReadinessReason != test.reason || status.Mode != "raft" {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
}

func TestControlPlaneStatusIncludesRepositoryWorkCounters(t *testing.T) {
	repository := store.NewMemory()
	clusterID := model.NewResourceID()
	if _, err := repository.UpsertCluster(model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL, DisplayName: "status-test"}); err != nil {
		t.Fatalf("put cluster: %v", err)
	}
	planned, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
		TargetID:  model.NewResourceID(), IdempotencyKey: "status-active-operation",
	})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if _, _, err := repository.CreateOperation(model.OperationRecord{
		Operation: model.Operation{ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
		TargetID:  model.NewResourceID(), IdempotencyKey: "status-planned-history",
	}); err != nil {
		t.Fatalf("create planned history: %v", err)
	}
	if _, err := repository.TransitionOperation(planned.ResourceID, planned.MetadataRevision, model.OperationTransition{
		Stage: model.StageExecute, Status: model.OperationRunning,
	}); err != nil {
		t.Fatalf("start operation: %v", err)
	}
	if _, err := repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID, Status: lifecycle.TaskRunning,
	}); err != nil {
		t.Fatalf("put lifecycle task: %v", err)
	}
	provider := newControlPlaneStatusProvider(repository, nil, time.Now().UTC().Add(-time.Minute))
	status, err := provider.ControlPlaneStatus(context.Background())
	if err != nil || !status.Ready || status.Mode != "standalone" || status.ClusterCount != 1 || status.ActiveOperations != 1 || status.ActiveLifecycleTasks != 1 || status.StateRevision == 0 || status.UptimeSeconds < 59 {
		t.Fatalf("standalone status=%+v err=%v", status, err)
	}
}

func TestControlPlaneStatusTreatsDisabledConsensusAsReadyStandalone(t *testing.T) {
	provider := newControlPlaneStatusProvider(
		store.NewMemory(),
		consensusStatusStub{status: consensus.Status{Enabled: false, Role: "disabled"}},
		time.Now().UTC().Add(-time.Minute),
	)
	status, err := provider.ControlPlaneStatus(context.Background())
	if err != nil || !status.Ready || status.Mode != "standalone" || status.Role != "standalone" || status.ReadinessReason != "ready" {
		t.Fatalf("disabled consensus status=%+v err=%v", status, err)
	}
}
