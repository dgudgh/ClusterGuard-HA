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

type consensusMembershipStatusStub struct {
	consensusStatusStub
	members []consensus.ControllerMember
}

func (stub consensusMembershipStatusStub) ControllerMembers(context.Context) ([]consensus.ControllerMember, error) {
	return append([]consensus.ControllerMember{}, stub.members...), nil
}

func TestControlPlaneStatusIncludesLiveRaftMembership(t *testing.T) {
	members := []consensus.ControllerMember{
		{ResourceID: model.NewResourceID(), Address: "192.0.2.11:10009", APIAddress: "https://192.0.2.11:3000"},
		{ResourceID: model.NewResourceID(), Address: "192.0.2.12:10009", APIAddress: "https://192.0.2.12:3000"},
		{ResourceID: model.NewResourceID(), Address: "192.0.2.13:10009", APIAddress: "https://192.0.2.13:3000"},
	}
	provider := newControlPlaneStatusProvider(store.NewMemory(), consensusMembershipStatusStub{
		consensusStatusStub: consensusStatusStub{status: consensus.Status{
			Enabled: true, Role: "leader", LeaderKnown: true, LeaderID: members[0].ResourceID,
			QuorumConfirmed: true, MutationAuthority: true, VoterCount: len(members),
		}},
		members: members,
	}, time.Now().UTC())
	status, err := provider.ControlPlaneStatus(context.Background())
	if err != nil || len(status.ControllerMembers) != len(members) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for index, member := range members {
		got := status.ControllerMembers[index]
		if got.ResourceID != member.ResourceID || got.RaftAddress != member.Address || got.APIAddress != member.APIAddress {
			t.Fatalf("member[%d]=%+v want=%+v", index, got, member)
		}
	}
}

func TestControlPlaneStatusIncludesOnlyActiveDataPlaneMembers(t *testing.T) {
	repository := store.NewMemory()
	dataID := model.ResourceID("11111111-1111-4111-8111-111111111111")
	mixedID := model.ResourceID("22222222-2222-4222-8222-222222222222")
	for _, node := range []model.DatabaseNode{
		{ResourceMeta: model.ResourceMeta{ResourceID: mixedID}, NodeName: "cg-mixed-0002", Kind: model.NodeMixed, Hostname: "mixed-b", IPAddress: "192.0.2.32", Active: true},
		{ResourceMeta: model.ResourceMeta{ResourceID: dataID}, NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "data-a", IPAddress: "192.0.2.31", Active: true},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-control-0003", Kind: model.NodeController, Hostname: "control-c", Active: true},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-data-0004", Kind: model.NodeData, Hostname: "retired-d", Active: false},
	} {
		if _, err := repository.PutNode(node); err != nil {
			t.Fatalf("put node %s: %v", node.NodeName, err)
		}
	}
	provider := newControlPlaneStatusProvider(repository, nil, time.Now().UTC())
	status, err := provider.ControlPlaneStatus(context.Background())
	if err != nil || len(status.DataNodeMembers) != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if status.DataNodeMembers[0].ResourceID != dataID || status.DataNodeMembers[1].ResourceID != mixedID {
		t.Fatalf("data members are not stable and sorted: %+v", status.DataNodeMembers)
	}
	if status.DataNodeMembers[1].Kind != model.NodeMixed || status.DataNodeMembers[1].Hostname != "mixed-b" {
		t.Fatalf("mixed member metadata missing: %+v", status.DataNodeMembers[1])
	}
}

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

func TestControlPlaneStatusExcludesReviewedIndeterminateHistory(t *testing.T) {
	repository := store.NewMemory()
	createIndeterminate := func(key string) model.OperationRecord {
		operation, _, err := repository.CreateOperation(model.OperationRecord{
			Operation: model.Operation{ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationFailover},
			TargetID:  model.NewResourceID(), IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		operation, err = repository.TransitionOperation(operation.ResourceID, operation.MetadataRevision, model.OperationTransition{
			Stage: model.StageVerify, Status: model.OperationIndeterminate, Message: "review required",
		})
		if err != nil {
			t.Fatalf("mark %s indeterminate: %v", key, err)
		}
		return operation
	}
	createIndeterminate("runtime-unreviewed")
	reviewed := createIndeterminate("runtime-reviewed")
	if _, err := repository.ReviewIndeterminateOperation(reviewed.ResourceID, reviewed.MetadataRevision, "dba", "live role and endpoint verified"); err != nil {
		t.Fatalf("review operation: %v", err)
	}
	provider := newControlPlaneStatusProvider(repository, nil, time.Now().UTC())
	status, err := provider.ControlPlaneStatus(context.Background())
	if err != nil || status.IndeterminateOperations != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
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
