package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type schedulerClusterSource struct{ clusters []model.DatabaseCluster }

func (source schedulerClusterSource) Clusters() []model.DatabaseCluster {
	return append([]model.DatabaseCluster{}, source.clusters...)
}

type schedulerRefresher struct {
	mu    sync.Mutex
	calls []model.ResourceID
	wake  chan struct{}
}

func (refresher *schedulerRefresher) Refresh(_ context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	refresher.mu.Lock()
	refresher.calls = append(refresher.calls, clusterID)
	refresher.mu.Unlock()
	if refresher.wake != nil {
		select {
		case refresher.wake <- struct{}{}:
		default:
		}
	}
	return model.TopologySnapshot{ClusterID: clusterID}, nil
}

func (refresher *schedulerRefresher) count() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return len(refresher.calls)
}

type schedulerAuthority struct{ err error }

func (authority schedulerAuthority) RequireMutationAuthority(context.Context) error {
	return authority.err
}

func TestSchedulerRefreshesEveryClusterOnlyWithLeaderQuorum(t *testing.T) {
	clusters := []model.DatabaseCluster{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}}, {ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}}}
	refresher := &schedulerRefresher{}
	scheduler := NewScheduler(schedulerClusterSource{clusters: clusters}, refresher, schedulerAuthority{}, time.Second, time.Second)
	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("leader refresh: %v", err)
	}
	if refresher.count() != len(clusters) {
		t.Fatalf("refresh calls=%v", refresher.calls)
	}

	standbyRefresher := &schedulerRefresher{}
	standby := NewScheduler(schedulerClusterSource{clusters: clusters}, standbyRefresher, schedulerAuthority{err: errors.New("not leader")}, time.Second, time.Second)
	if err := standby.RunOnce(context.Background()); err != nil || standbyRefresher.count() != 0 {
		t.Fatalf("standby scheduler err=%v calls=%v", err, standbyRefresher.calls)
	}
}

func TestSchedulerRunsImmediatelyAndStopsWithContext(t *testing.T) {
	refresher := &schedulerRefresher{wake: make(chan struct{}, 4)}
	scheduler := NewScheduler(
		schedulerClusterSource{clusters: []model.DatabaseCluster{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}}}},
		refresher, nil, 10*time.Millisecond, time.Second,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()
	select {
	case <-refresher.wake:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not refresh immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
