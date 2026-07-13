package consensus

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type stateRecorder struct {
	mu     sync.Mutex
	states [][]byte
}

func (recorder *stateRecorder) ValidateReplicatedState(state []byte) error {
	if len(state) == 0 {
		return context.Canceled
	}
	return nil
}

func (recorder *stateRecorder) ApplyReplicatedState(state []byte) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.states = append(recorder.states, append([]byte{}, state...))
	return nil
}

func (recorder *stateRecorder) contains(want string) bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, state := range recorder.states {
		if string(state) == want {
			return true
		}
	}
	return false
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate TCP address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP address: %v", err)
	}
	return address
}

func waitForRaftLeader(t *testing.T, nodes []*Node) *Node {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		leaders := make([]*Node, 0, 1)
		for _, node := range nodes {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			err := node.RequireMutationAuthority(ctx)
			cancel()
			if err == nil {
				leaders = append(leaders, node)
			}
		}
		if len(leaders) == 1 {
			return leaders[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("three-node Raft cluster did not elect one authoritative leader")
	return nil
}

func TestThreeNodeRaftCommitsToFollowersAndLosesAuthorityWithoutQuorum(t *testing.T) {
	addresses := []string{freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t)}
	peers := make([]Peer, 3)
	for index := range peers {
		peers[index] = Peer{ResourceID: model.NewResourceID(), Address: addresses[index]}
	}
	recorders := []*stateRecorder{{}, {}, {}}
	nodes := make([]*Node, 3)
	for _, index := range []int{1, 2, 0} {
		node, err := Open(Config{
			LocalID: peers[index].ResourceID, BindAddress: addresses[index], AdvertiseAddress: addresses[index],
			DataDirectory: filepath.Join(t.TempDir(), "raft"), Peers: peers, Bootstrap: index == 0,
			ApplyTimeout: 3 * time.Second,
		}, recorders[index])
		if err != nil {
			t.Fatalf("open Raft node %d: %v", index, err)
		}
		nodes[index] = node
		defer node.Close()
	}
	leader := waitForRaftLeader(t, nodes)
	if err := leader.Commit([]byte(`{"revision":1}`)); err != nil {
		t.Fatalf("commit replicated state: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		applied := 0
		for index, node := range nodes {
			if node != leader && recorders[index].contains(`{"revision":1}`) {
				applied++
			}
		}
		if applied == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for index, node := range nodes {
		if node != leader && !recorders[index].contains(`{"revision":1}`) {
			t.Fatalf("follower %d did not apply committed state", index)
		}
	}

	stopped := 0
	for _, node := range nodes {
		if node == leader || stopped >= 2 {
			continue
		}
		if err := node.Close(); err != nil {
			t.Fatalf("close follower: %v", err)
		}
		stopped++
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := leader.RequireMutationAuthority(ctx)
		cancel()
		if err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated leader retained mutation authority without a majority")
}

func TestRaftConfigurationRequiresOddUniqueThreeNodeMembership(t *testing.T) {
	peer := Peer{ResourceID: model.NewResourceID(), Address: freeTCPAddress(t)}
	for _, peers := range [][]Peer{{peer}, {peer, peer}} {
		if _, err := Open(Config{LocalID: peer.ResourceID, BindAddress: peer.Address, AdvertiseAddress: peer.Address, DataDirectory: t.TempDir(), Peers: peers}, &stateRecorder{}); err == nil {
			t.Fatalf("unsafe membership was accepted: %+v", peers)
		}
	}
}

func TestRaftRuntimeConfigurationCompactsFullStateLogsAggressively(t *testing.T) {
	localID := model.NewResourceID()
	configuration := newRaftRuntimeConfiguration(localID)

	if string(configuration.LocalID) != string(localID) {
		t.Fatalf("local id=%q, want %q", configuration.LocalID, localID)
	}
	if configuration.SnapshotThreshold != 64 {
		t.Fatalf("snapshot threshold=%d, want 64", configuration.SnapshotThreshold)
	}
	if configuration.TrailingLogs != 32 {
		t.Fatalf("trailing logs=%d, want 32", configuration.TrailingLogs)
	}
	if configuration.SnapshotInterval != 30*time.Second {
		t.Fatalf("snapshot interval=%s, want 30s", configuration.SnapshotInterval)
	}
}
