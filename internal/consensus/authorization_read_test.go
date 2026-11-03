package consensus

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type blockedAuthorizationFSM struct {
	stateRecorder
	entered chan struct{}
	release <-chan struct{}
}

func (s *blockedAuthorizationFSM) ApplyReplicatedState(b []byte) error {
	if bytes.Contains(b, []byte("inherited-lease")) {
		s.entered <- struct{}{}
		<-s.release
	}
	return s.stateRecorder.ApplyReplicatedState(b)
}
func TestAuthorizationReadWaitsForRealRaftFSMAndCallerDeadline(t *testing.T) {
	peers := make([]Peer, 3)
	for i := range peers {
		peers[i] = Peer{ResourceID: model.NewResourceID(), Address: freeTCPAddress(t)}
	}
	release := make(chan struct{})
	released := false
	nodes := make([]*Node, 3)
	defer func() {
		if !released {
			close(release)
		}
		for _, node := range nodes {
			if node != nil {
				node.Close()
			}
		}
	}()
	entered := make(chan struct{}, 3)
	for _, i := range []int{1, 2, 0} {
		n, err := Open(Config{LocalID: peers[i].ResourceID, BindAddress: peers[i].Address, AdvertiseAddress: peers[i].Address, DataDirectory: filepath.Join(t.TempDir(), "raft"), Peers: peers, Bootstrap: i == 0, ApplyTimeout: 3 * time.Second}, &blockedAuthorizationFSM{entered: entered, release: release})
		if err != nil {
			t.Fatal(err)
		}
		nodes[i] = n
	}
	leader := waitForRaftLeader(t, nodes)
	committed := make(chan error, 1)
	go func() { committed <- leader.Commit([]byte(`{"state":"inherited-lease"}`)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("FSM did not start applying committed state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := leader.RequireMutationAuthority(ctx); err != nil {
		t.Fatalf("quorum should still be available while FSM is delayed: %v", err)
	}
	if err := leader.SynchronizeRead(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("signed read did not wait for application/deadline: %v", err)
	}
	close(release)
	released = true
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := leader.SynchronizeRead(ctx2); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node != leader {
			node.Close()
		}
	}
	ctx3, cancel3 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel3()
	if err := leader.SynchronizeRead(ctx3); err == nil {
		t.Fatal("lost majority still authorized a read")
	}
}
