package configuration

import (
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Actual three-node Raft and HTTP peers. Process configuration is reloaded in
// the harness; privileged systemd restarts remain a separate field obligation.
func TestConfigurationRolloutUsesReplicatedCASAcrossRealRaftLeaderElection(t *testing.T) {
	c := newTestCluster(t)
	peers := []consensus.Peer{}
	for _, id := range c.IDs {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		peers = append(peers, consensus.Peer{ResourceID: model.ResourceID(id), Address: address, APIAddress: c.URLs[id]})
	}
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	nodes := make([]*consensus.Node, 3)
	open := func(index int, bootstrap bool) {
		id := c.IDs[index]
		m := c.Managers[id]
		repo, e := store.Open(m.Config.MetadataPath)
		if e != nil {
			t.Fatal(e)
		}
		node, e := consensus.Open(consensus.Config{LocalID: peers[index].ResourceID, BindAddress: peers[index].Address, AdvertiseAddress: peers[index].Address, DataDirectory: filepath.Join(dirs[index], "raft"), Peers: peers, Bootstrap: bootstrap, ApplyTimeout: 3 * time.Second, SnapshotCASEnabled: true}, repo)
		if e != nil {
			t.Fatal(e)
		}
		if e = repo.SetSnapshotConsensus(node); e != nil {
			t.Fatal(e)
		}
		nodes[index] = node
		m.Repository = repo
		m.Authority = node
	}
	for _, i := range []int{1, 2, 0} {
		open(i, i == 0)
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			if n != nil {
				_ = n.Close()
			}
		}
	})
	elect := func(exclude int) {
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			for i, n := range nodes {
				if i == exclude || n == nil {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				e := n.RequireMutationAuthority(ctx)
				cancel()
				if e == nil {
					c.Leader = c.IDs[i]
					c.Repository = c.Managers[c.Leader].Repository
					return
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("no actual Raft majority Leader")
	}
	elect(-1)
	var taskID string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		m := c.Managers[c.Leader]
		r := Request{RequestID: string(model.NewResourceID()), NodeIDs: c.IDs, Changes: map[string]int{"mysql.discovery_interval_seconds": 9}}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		plan, e := m.Plan(ctx, r)
		if e == nil {
			r.PlanHash = plan.Hash
			task, e := m.Dispatch(ctx, r, "raft-test-admin")
			if e == nil {
				taskID = task.TaskID
				cancel()
				break
			}
		}
		cancel()
		time.Sleep(40 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("Raft dispatch could not pass preflight")
	}
	previous := c.Leader
	leaderIndex := 0
	for i, id := range c.IDs {
		if id == previous {
			leaderIndex = i
		}
	}
	_ = nodes[leaderIndex].Close()
	nodes[leaderIndex] = nil
	elect(leaderIndex)
	if c.Leader == previous {
		t.Fatal("Leader did not change")
	}
	open(leaderIndex, false)
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, id := range c.IDs {
			m := c.Managers[id]
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			m.Tick(ctx)
			cancel()
			if c.Restarts[id].Calls > 0 && m.StartedOverride.TaskID != taskID {
				c.reload(t, id)
			}
		}
		task, ok := c.Managers[c.Leader].Repository.ConfigurationTask(taskID)
		if ok && !task.Active {
			if task.Status != "succeeded" {
				t.Fatalf("unexpected completion %+v", task)
			}
			for _, id := range c.IDs {
				record, found := c.Managers[id].Repository.ConfigurationTask(taskID)
				if !found || record.PlanHash != task.PlanHash {
					t.Fatal("replicated task subject diverged")
				}
				if c.Managers[id].Config.MySQL.DiscoveryIntervalSeconds != 9 {
					t.Fatal("node value not applied")
				}
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	task, _ := c.Managers[c.Leader].Repository.ConfigurationTask(taskID)
	t.Fatalf("real Raft configuration rollout incomplete: %+v", task)
}
