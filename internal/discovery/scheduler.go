package discovery

import (
	"context"
	"errors"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const maximumScheduledRefreshes = 4

type ScheduledClusterSource interface {
	Clusters() []model.DatabaseCluster
}

type ScheduledRefresher interface {
	Refresh(context.Context, model.ResourceID) (model.TopologySnapshot, error)
}

type ScheduledMutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type Scheduler struct {
	clusters  ScheduledClusterSource
	refresher ScheduledRefresher
	authority ScheduledMutationAuthority
	interval  time.Duration
	timeout   time.Duration
}

func NewScheduler(clusters ScheduledClusterSource, refresher ScheduledRefresher, authority ScheduledMutationAuthority, interval, timeout time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	return &Scheduler{clusters: clusters, refresher: refresher, authority: authority, interval: interval, timeout: timeout}
}

func (scheduler *Scheduler) RunOnce(ctx context.Context) error {
	if scheduler == nil || scheduler.clusters == nil || scheduler.refresher == nil {
		return errors.New("discovery scheduler is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if scheduler.authority != nil {
		if err := scheduler.authority.RequireMutationAuthority(ctx); err != nil {
			return nil
		}
	}
	clusters := scheduler.clusters.Clusters()
	if len(clusters) == 0 {
		return nil
	}
	parallel := maximumScheduledRefreshes
	if len(clusters) < parallel {
		parallel = len(clusters)
	}
	gate := make(chan struct{}, parallel)
	errorsFound := make(chan error, len(clusters))
	var wait sync.WaitGroup
	for _, cluster := range clusters {
		clusterID := cluster.ResourceID
		if !model.ValidResourceID(clusterID) {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-ctx.Done():
				errorsFound <- ctx.Err()
				return
			}
			refreshContext, cancel := context.WithTimeout(ctx, scheduler.timeout)
			defer cancel()
			if _, err := scheduler.refresher.Refresh(refreshContext, clusterID); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	var refreshErrors []error
	for err := range errorsFound {
		refreshErrors = append(refreshErrors, err)
	}
	return errors.Join(refreshErrors...)
}

func (scheduler *Scheduler) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	_ = scheduler.RunOnce(ctx)
	ticker := time.NewTicker(scheduler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = scheduler.RunOnce(ctx)
		}
	}
}
