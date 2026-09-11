package endpoint

import (
	"context"
	"fmt"
	"sync"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func authorizeTransitionLease(ctx context.Context, leases LeaseStore, resolved adapter.ResolvedOperation, haEndpointID model.ResourceID, ttl, interval time.Duration) (adapter.TransitionAuthorization, error) {
	if leases == nil {
		return adapter.TransitionAuthorization{}, fmt.Errorf("endpoint lease store is not configured")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	request := LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: haEndpointID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: ttl,
	}
	lease, err := leases.Acquire(ctx, request)
	if err != nil {
		return adapter.TransitionAuthorization{}, fmt.Errorf("acquire endpoint transition lease: %w", err)
	}
	request.RenewOnly = true
	guarded, cancelCause := context.WithCancelCause(ctx)
	var leaseMu sync.Mutex
	current := lease
	stopRenewal := make(chan struct{})
	renewalStopped := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(renewalStopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-guarded.Done():
				return
			case <-stopRenewal:
				return
			case <-ticker.C:
				leaseMu.Lock()
				renewed, renewErr := leases.Acquire(guarded, request)
				if renewErr == nil && !SameLeaseIdentity(current, renewed) {
					renewErr = fmt.Errorf("endpoint lease identity changed during renewal")
				}
				if renewErr == nil {
					current = renewed
				}
				leaseMu.Unlock()
				if renewErr != nil {
					cancelCause(fmt.Errorf("renew endpoint transition lease: %w", renewErr))
					return
				}
			}
		}
	}()
	stop := func() {
		stopOnce.Do(func() { close(stopRenewal) })
		<-renewalStopped
	}
	var terminalMu sync.Mutex
	terminalAction := ""
	var terminalErr error
	finalize := func(finalizeCtx context.Context) error {
		terminalMu.Lock()
		defer terminalMu.Unlock()
		if terminalAction != "" {
			if terminalAction == "finalize" {
				return terminalErr
			}
			return fmt.Errorf("endpoint transition lease was already aborted")
		}
		if guarded.Err() != nil {
			return context.Cause(guarded)
		}
		terminalAction = "finalize"
		leaseMu.Lock()
		stable, finalizeErr := leases.FinalizeTransition(finalizeCtx, current, ttl)
		if finalizeErr == nil {
			current = stable
			request = LeaseRequest{
				ClusterID: stable.ClusterID, HAEndpointID: stable.HAEndpointID,
				OperationID: stable.HAEndpointID, OwnerID: stable.OwnerID,
				TTL: ttl, RenewOnly: true,
			}
		}
		terminalErr = finalizeErr
		leaseMu.Unlock()
		if terminalErr != nil {
			cancelCause(fmt.Errorf("finalize endpoint transition lease: %w", terminalErr))
		}
		return terminalErr
	}
	abort := func(abortCtx context.Context) error {
		terminalMu.Lock()
		defer terminalMu.Unlock()
		if terminalAction != "" {
			if terminalAction == "abort" {
				return terminalErr
			}
			return fmt.Errorf("endpoint transition lease was already finalized")
		}
		stop()
		terminalAction = "abort"
		leaseMu.Lock()
		_, terminalErr = leases.RollbackTransition(abortCtx, current, ttl)
		leaseMu.Unlock()
		if terminalErr != nil {
			cancelCause(fmt.Errorf("rollback endpoint transition lease: %w", terminalErr))
		}
		return terminalErr
	}
	return adapter.TransitionAuthorization{
		Context: guarded, LeaseID: lease.ResourceID, Abort: abort, Finalize: finalize,
		Cancel: func() { stop(); cancelCause(context.Canceled) },
	}, nil
}

func authorizeStableLease(ctx context.Context, leases LeaseStore, resolved adapter.ResolvedOperation, haEndpointID model.ResourceID, ttl, interval time.Duration) (adapter.StableOwnershipAuthorization, error) {
	reader, ok := leases.(CurrentLeaseReader)
	if !ok {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("current stable endpoint lease cannot be inspected")
	}
	current, err := reader.Current(ctx, resolved.Cluster.ResourceID, haEndpointID)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("inspect current stable endpoint lease: %w", err)
	}
	if current.OperationID != haEndpointID || current.OwnerID != resolved.Primary.ResourceID || current.PreviousOwnerID != "" {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("current endpoint lease is not stable for the current primary")
	}
	if err := leases.Validate(ctx, current); err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("validate current stable endpoint lease: %w", err)
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	request := LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: haEndpointID,
		OperationID: haEndpointID, OwnerID: resolved.Primary.ResourceID,
		TTL: ttl, RenewOnly: true,
	}
	renewed, err := leases.Acquire(ctx, request)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("renew current stable endpoint lease: %w", err)
	}
	if !SameLeaseIdentity(current, renewed) {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("stable endpoint lease identity changed during renewal")
	}
	guarded, cancelCause := context.WithCancelCause(ctx)
	stopRenewal := make(chan struct{})
	renewalStopped := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(renewalStopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-guarded.Done():
				return
			case <-stopRenewal:
				return
			case <-ticker.C:
				lease, renewErr := leases.Acquire(guarded, request)
				if renewErr != nil {
					cancelCause(fmt.Errorf("renew stable endpoint lease: %w", renewErr))
					return
				}
				if !SameLeaseIdentity(current, lease) {
					cancelCause(fmt.Errorf("stable endpoint lease identity changed"))
					return
				}
			}
		}
	}()
	stop := func() {
		stopOnce.Do(func() { close(stopRenewal) })
		<-renewalStopped
	}
	return adapter.StableOwnershipAuthorization{
		Context: guarded, LeaseID: renewed.ResourceID,
		Cancel: func() { stop(); cancelCause(context.Canceled) },
	}, nil
}
