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
	mu         sync.Mutex
	calls      []model.ResourceID
	batchCalls [][]model.ResourceID
	wake       chan struct{}
	err        error
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
	return model.TopologySnapshot{ClusterID: clusterID}, refresher.err
}

func (refresher *schedulerRefresher) count() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return len(refresher.calls)
}

func (refresher *schedulerRefresher) RefreshBatch(_ context.Context, clusterIDs []model.ResourceID) (map[model.ResourceID]model.TopologySnapshot, error) {
	refresher.mu.Lock()
	refresher.batchCalls = append(refresher.batchCalls, append([]model.ResourceID{}, clusterIDs...))
	refresher.mu.Unlock()
	if refresher.wake != nil {
		select {
		case refresher.wake <- struct{}{}:
		default:
		}
	}
	result := make(map[model.ResourceID]model.TopologySnapshot, len(clusterIDs))
	for _, clusterID := range clusterIDs {
		result[clusterID] = model.TopologySnapshot{ClusterID: clusterID}
	}
	return result, refresher.err
}

func (refresher *schedulerRefresher) totalCount() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	total := len(refresher.calls)
	for _, batch := range refresher.batchCalls {
		total += len(batch)
	}
	return total
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
	if refresher.totalCount() != len(clusters) {
		t.Fatalf("refresh calls=%v", refresher.calls)
	}

	standbyRefresher := &schedulerRefresher{}
	standby := NewScheduler(schedulerClusterSource{clusters: clusters}, standbyRefresher, schedulerAuthority{err: errors.New("not leader")}, time.Second, time.Second)
	if err := standby.RunOnce(context.Background()); err != nil || standbyRefresher.totalCount() != 0 {
		t.Fatalf("standby scheduler err=%v calls=%v", err, standbyRefresher.calls)
	}
}

func TestSchedulerPublishesOneBatchForAllClusters(t *testing.T) {
	clusters := []model.DatabaseCluster{
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
	}
	refresher := &schedulerRefresher{}
	scheduler := NewScheduler(schedulerClusterSource{clusters: clusters}, refresher, schedulerAuthority{}, time.Second, time.Second)
	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatalf("batch refresh: %v", err)
	}
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	if len(refresher.calls) != 0 {
		t.Fatalf("individual refresh calls=%v, want none when batching is available", refresher.calls)
	}
	if len(refresher.batchCalls) != 1 || len(refresher.batchCalls[0]) != len(clusters) {
		t.Fatalf("batch calls=%v, want one call containing every cluster", refresher.batchCalls)
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

func TestSchedulerRunReportsRefreshFailures(t *testing.T) {
	expected := errors.New("discovery backend unavailable")
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := NewScheduler(
		schedulerClusterSource{clusters: []model.DatabaseCluster{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}}}},
		&schedulerRefresher{err: expected}, nil, time.Hour, time.Second,
		WithSchedulerErrorHandler(func(err error) {
			reported <- err
			cancel()
		}),
	)
	done := make(chan struct{})
	go func() {
		scheduler.Run(ctx)
		close(done)
	}()
	select {
	case err := <-reported:
		if !errors.Is(err, expected) {
			t.Fatalf("reported discovery error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler swallowed refresh failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after error test cancellation")
	}
}
