package store

import (
	"errors"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type snapshotConsensusStub struct {
	commits [][]byte
	err     error
}

func (stub *snapshotConsensusStub) Commit(state []byte) error {
	stub.commits = append(stub.commits, append([]byte{}, state...))
	return stub.err
}

func TestRepositoryPublishesMutationOnlyAfterConsensusCommit(t *testing.T) {
	repository := NewMemory()
	consensus := &snapshotConsensusStub{}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"})
	if err != nil {
		t.Fatalf("consensus-backed mutation: %v", err)
	}
	if len(consensus.commits) != 1 || len(consensus.commits[0]) == 0 {
		t.Fatalf("consensus commits=%d", len(consensus.commits))
	}
	if persisted, found := repository.Cluster(cluster.ResourceID); !found || persisted.DisplayName != "orders" {
		t.Fatalf("consensus-backed cluster=%+v found=%t", persisted, found)
	}

	consensus.err = errors.New("no leader quorum")
	if _, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "blocked"}); err == nil {
		t.Fatal("consensus failure was ignored")
	}
	if len(repository.Clusters()) != 1 {
		t.Fatalf("failed consensus published local state: %+v", repository.Clusters())
	}
}

func TestRepositoryAppliesValidatedReplicatedSnapshotDurably(t *testing.T) {
	leader := NewMemory()
	cluster, err := leader.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "replicated"})
	if err != nil {
		t.Fatalf("create leader state: %v", err)
	}
	state, err := leader.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	path := filepath.Join(t.TempDir(), "metadata.json")
	follower, err := Open(path)
	if err != nil {
		t.Fatalf("open follower repository: %v", err)
	}
	if err := follower.ValidateReplicatedState(state); err != nil {
		t.Fatalf("validate replicated state: %v", err)
	}
	if err := follower.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply replicated state: %v", err)
	}
	if got, found := follower.Cluster(cluster.ResourceID); !found || got.DisplayName != cluster.DisplayName {
		t.Fatalf("follower cluster=%+v found=%t", got, found)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen replicated state: %v", err)
	}
	if _, found := reopened.Cluster(cluster.ResourceID); !found {
		t.Fatal("replicated state did not survive restart")
	}
	if err := follower.ApplyReplicatedState([]byte(`{"clusters":`)); err == nil {
		t.Fatal("malformed replicated state was accepted")
	}
}
