package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type snapshotConsensusStub struct {
	commits [][]byte
	err     error
	apply   func([]byte) error
}

func (stub *snapshotConsensusStub) Commit(state []byte) error {
	stub.commits = append(stub.commits, append([]byte{}, state...))
	if stub.err != nil {
		return stub.err
	}
	if stub.apply != nil {
		return stub.apply(state)
	}
	return nil
}

type synchronizingSnapshotConsensusStub struct {
	snapshotConsensusStub
	synchronize func() error
	syncCalls   int
}

type gatedSnapshotConsensusStub struct {
	snapshotConsensusStub
	active bool
}

func (stub *gatedSnapshotConsensusStub) SnapshotCASActive() bool { return stub.active }

func (stub *synchronizingSnapshotConsensusStub) Synchronize() error {
	stub.syncCalls++
	if stub.synchronize == nil {
		return nil
	}
	return stub.synchronize()
}

func TestRepositoryDrainsReplicatedBacklogBeforeConsensusMutation(t *testing.T) {
	base := NewMemory()
	baseCluster, err := base.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "base"})
	if err != nil {
		t.Fatalf("create base cluster: %v", err)
	}
	baseState, err := base.ReplicatedState()
	if err != nil {
		t.Fatalf("encode base state: %v", err)
	}

	remote := NewMemory()
	if err := remote.ApplyReplicatedState(baseState); err != nil {
		t.Fatalf("seed remote state: %v", err)
	}
	remoteCluster, err := remote.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "remote"})
	if err != nil {
		t.Fatalf("create remote cluster: %v", err)
	}
	pendingState, err := remote.ReplicatedState()
	if err != nil {
		t.Fatalf("encode pending state: %v", err)
	}

	repository := NewMemory()
	if err := repository.ApplyReplicatedState(baseState); err != nil {
		t.Fatalf("seed local state: %v", err)
	}
	consensus := &synchronizingSnapshotConsensusStub{}
	consensus.synchronize = func() error { return repository.ApplyReplicatedState(pendingState) }
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}

	_, err = repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "stale-local"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale local mutation error=%v, want repository conflict", err)
	}
	if consensus.syncCalls != 1 || len(consensus.commits) != 0 {
		t.Fatalf("sync calls=%d commits=%d, want one synchronization and no stale commit", consensus.syncCalls, len(consensus.commits))
	}
	if _, found := repository.Cluster(baseCluster.ResourceID); !found {
		t.Fatal("base cluster disappeared after backlog synchronization")
	}
	if _, found := repository.Cluster(remoteCluster.ResourceID); !found {
		t.Fatal("replicated backlog was not applied")
	}
	for _, cluster := range repository.Clusters() {
		if cluster.DisplayName == "stale-local" {
			t.Fatal("stale local mutation overwrote replicated state")
		}
	}
}

func TestRepositoryConsensusCommitAppliesWithoutHoldingRepositoryLock(t *testing.T) {
	repository := NewMemory()
	consensus := &snapshotConsensusStub{apply: repository.ApplyReplicatedState}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "no-deadlock"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("commit through local Raft apply: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consensus commit deadlocked with the local replicated-state apply")
	}
}

func TestRepositorySerializesMutationsBeforeBuildingConsensusSnapshot(t *testing.T) {
	repository := NewMemory()
	clusterA, endpointsA, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "cluster-a"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster a: %v", err)
	}
	clusterB, endpointsB, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "cluster-b"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3307, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster b: %v", err)
	}
	refresh := func(cluster model.DatabaseCluster, endpoint model.Endpoint, serverUUID string, observedAt time.Time) error {
		instance := mysqlInstance(cluster.ResourceID, endpoint.Hostname, endpoint.IPAddress, endpoint.Port)
		instance.EngineIdentity["server_uuid"] = serverUUID
		instance.Role = model.RolePrimary
		_, refreshErr := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
			ClusterID:           cluster.ResourceID,
			InventoryGeneration: currentInventoryGeneration(t, repository, cluster.ResourceID),
			ObservedAt:          observedAt,
			Observations:        []DiscoveryObservation{{EndpointID: endpoint.ResourceID, Instance: instance}},
			Probes:              []model.ProbeStatus{{EndpointID: endpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		})
		return refreshErr
	}

	firstSynchronizeEntered := make(chan struct{})
	releaseFirstSynchronize := make(chan struct{})
	var blockFirst sync.Once
	consensus := &synchronizingSnapshotConsensusStub{}
	consensus.apply = repository.ApplyReplicatedState
	consensus.synchronize = func() error {
		blockFirst.Do(func() {
			close(firstSynchronizeEntered)
			<-releaseFirstSynchronize
		})
		return nil
	}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}

	errors := make(chan error, 2)
	baseTime := time.Date(2026, time.July, 14, 1, 0, 0, 0, time.UTC)
	go func() {
		errors <- refresh(clusterA, endpointsA[0], "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", baseTime)
	}()
	select {
	case <-firstSynchronizeEntered:
	case <-time.After(time.Second):
		t.Fatal("first discovery refresh did not reach consensus synchronization")
	}
	if repository.mutationMu.TryLock() {
		repository.mutationMu.Unlock()
		t.Fatal("discovery refresh released the mutation gate before its consensus commit completed")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		errors <- refresh(clusterB, endpointsB[0], "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee", baseTime.Add(time.Second))
	}()
	<-secondStarted
	select {
	case err := <-errors:
		t.Fatalf("a discovery refresh completed before the blocked consensus commit was released: %v", err)
	default:
	}
	close(releaseFirstSynchronize)

	for index := 0; index < 2; index++ {
		select {
		case err := <-errors:
			if err != nil {
				t.Fatalf("serialized mutation %d failed: %v", index+1, err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent mutation did not complete")
		}
	}
	for _, cluster := range []model.DatabaseCluster{clusterA, clusterB} {
		topology, found := repository.TopologySnapshot(cluster.ResourceID)
		if !found || topology.ClusterID != cluster.ResourceID || len(topology.Instances) != 1 {
			t.Fatalf("cluster %s topology=%+v found=%t, want one committed discovery instance", cluster.DisplayName, topology, found)
		}
	}
}

func TestRepositoryBlocksMutationUntilSnapshotCASProtocolIsActivated(t *testing.T) {
	repository := NewMemory()
	consensus := &gatedSnapshotConsensusStub{active: false}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}
	if _, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "blocked-during-upgrade"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("protocol-gated mutation error=%v, want repository conflict", err)
	}
	if len(consensus.commits) != 0 {
		t.Fatalf("protocol-gated mutation reached Raft: commits=%d", len(consensus.commits))
	}
}

func TestRepositoryRejectsStaleConsensusSnapshotAfterForeignApply(t *testing.T) {
	repository := NewMemory()
	seed, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "seed"})
	if err != nil {
		t.Fatalf("create seed cluster: %v", err)
	}

	foreign := repository.snapshot
	foreign.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	foreignCluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "foreign"}
	foreign.Clusters[foreignCluster.ResourceID] = foreignCluster
	foreignState, err := encodeConsensusSnapshot(foreign, repository.snapshot, repository.stateRevision+1)
	if err != nil {
		t.Fatalf("encode foreign state: %v", err)
	}

	consensus := &snapshotConsensusStub{apply: repository.ApplyReplicatedState}
	consensus.apply = func(localState []byte) error {
		if err := repository.ApplyReplicatedState(foreignState); err != nil {
			return err
		}
		return repository.ApplyReplicatedState(localState)
	}
	if err := repository.SetSnapshotConsensus(consensus); err != nil {
		t.Fatalf("set snapshot consensus: %v", err)
	}

	_, err = repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "stale-local"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale local snapshot error=%v, want repository conflict", err)
	}
	if _, found := repository.Cluster(seed.ResourceID); !found {
		t.Fatal("foreign state lost the existing cluster")
	}
	if _, found := repository.Cluster(foreignCluster.ResourceID); !found {
		t.Fatal("foreign state was overwritten by the stale local snapshot")
	}
	for _, cluster := range repository.Clusters() {
		if cluster.DisplayName == "stale-local" {
			t.Fatal("stale local mutation was published")
		}
	}
}

func TestRepositoryPublishesMutationOnlyAfterConsensusCommit(t *testing.T) {
	repository := NewMemory()
	consensus := &snapshotConsensusStub{apply: repository.ApplyReplicatedState}
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

func TestRepositoryConsensusSnapshotReplayIsIdempotentAcrossRestart(t *testing.T) {
	value := emptySnapshot()
	cluster := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "restart-safe"}
	value.Clusters[cluster.ResourceID] = cluster
	state, err := encodeConsensusSnapshot(value, emptySnapshot(), 1)
	if err != nil {
		t.Fatalf("encode consensus state: %v", err)
	}

	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repository.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply consensus state: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	if err := reopened.ApplyReplicatedState(state); err != nil {
		t.Fatalf("replay persisted consensus state: %v", err)
	}
	if reopened.stateRevision != 1 {
		t.Fatalf("replayed state revision=%d, want 1", reopened.stateRevision)
	}
}

func TestRepositoryRaftSnapshotRestoreIsAuthoritative(t *testing.T) {
	repository := NewMemory()
	local, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "local-only"})
	if err != nil {
		t.Fatalf("create local state: %v", err)
	}

	authoritative := emptySnapshot()
	remote := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "raft-authoritative"}
	authoritative.Clusters[remote.ResourceID] = remote
	state, err := encodeConsensusSnapshot(authoritative, emptySnapshot(), 9)
	if err != nil {
		t.Fatalf("encode Raft snapshot: %v", err)
	}
	if err := repository.RestoreReplicatedState(state); err != nil {
		t.Fatalf("restore Raft snapshot: %v", err)
	}
	if repository.stateRevision != 9 {
		t.Fatalf("restored state revision=%d, want 9", repository.stateRevision)
	}
	if _, found := repository.Cluster(remote.ResourceID); !found {
		t.Fatal("authoritative Raft state was not restored")
	}
	if _, found := repository.Cluster(local.ResourceID); found {
		t.Fatal("stale local-only state survived authoritative Raft restore")
	}
}

func TestRepositoryConsensusSnapshotAcceptsMatchingContentAcrossRevisionSkew(t *testing.T) {
	base := NewMemory()
	if _, err := base.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "shared"}); err != nil {
		t.Fatalf("create shared state: %v", err)
	}
	baseState, err := base.ReplicatedState()
	if err != nil {
		t.Fatalf("encode shared state: %v", err)
	}

	leader := NewMemory()
	if err := leader.RestoreReplicatedState(baseState); err != nil {
		t.Fatalf("seed leader: %v", err)
	}
	follower := NewMemory()
	if err := follower.RestoreReplicatedState(baseState); err != nil {
		t.Fatalf("seed follower: %v", err)
	}
	leader.stateRevision = 9
	follower.stateRevision = 3

	next := leader.snapshot
	next.Clusters = cloneClusterMap(leader.snapshot.Clusters)
	added := model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, DisplayName: "next"}
	next.Clusters[added.ResourceID] = added
	state, err := encodeConsensusSnapshot(next, leader.snapshot, leader.stateRevision+1)
	if err != nil {
		t.Fatalf("encode consensus mutation: %v", err)
	}
	if err := follower.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply matching-content mutation across revision skew: %v", err)
	}
	if _, found := follower.Cluster(added.ResourceID); !found {
		t.Fatal("matching-content mutation was not applied")
	}
}
