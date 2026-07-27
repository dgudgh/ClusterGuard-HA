package discovery

import (
	"context"
	"errors"
	"log"
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

type ScheduledBatchRefresher interface {
	RefreshBatch(context.Context, []model.ResourceID) (map[model.ResourceID]model.TopologySnapshot, error)
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
	onError   func(error)
}

type SchedulerOption func(*Scheduler)

func WithSchedulerErrorHandler(handler func(error)) SchedulerOption {
	return func(scheduler *Scheduler) {
		if handler != nil {
			scheduler.onError = handler
		}
	}
}

func NewScheduler(clusters ScheduledClusterSource, refresher ScheduledRefresher, authority ScheduledMutationAuthority, interval, timeout time.Duration, options ...SchedulerOption) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	scheduler := &Scheduler{
		clusters: clusters, refresher: refresher, authority: authority, interval: interval, timeout: timeout,
		onError: func(err error) { log.Printf("scheduled topology discovery failed: %v", err) },
	}
	for _, option := range options {
		if option != nil {
			option(scheduler)
		}
	}
	return scheduler
}

func (scheduler *Scheduler) reportError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && scheduler != nil && scheduler.onError != nil {
		scheduler.onError(err)
	}
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
	clusterIDs := make([]model.ResourceID, 0, len(clusters))
	for _, cluster := range clusters {
		if model.ValidResourceID(cluster.ResourceID) {
			clusterIDs = append(clusterIDs, cluster.ResourceID)
		}
	}
	if batch, ok := scheduler.refresher.(ScheduledBatchRefresher); ok {
		refreshContext, cancel := context.WithTimeout(ctx, scheduler.timeout)
		defer cancel()
		_, err := batch.RefreshBatch(refreshContext, clusterIDs)
		return err
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
	scheduler.reportError(scheduler.RunOnce(ctx))
	ticker := time.NewTicker(scheduler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scheduler.reportError(scheduler.RunOnce(ctx))
		}
	}
}
