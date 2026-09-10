package runtime

import (
	"context"
	"log"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/model"
)

const (
	abandonedOperationStaleAfter        = 30 * time.Minute
	abandonedOperationReconcileInterval = 5 * time.Second
)

func reconcileAbandonedOperations(
	ctx context.Context,
	repository *store.Repository,
	service *workflow.Service,
	authority coordination.MutationAuthority,
) ([]model.OperationRecord, error) {
	if repository == nil || service == nil {
		return nil, nil
	}
	if authority != nil {
		authorityCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := authority.RequireMutationAuthority(authorityCtx)
		cancel()
		if err != nil {
			// Followers observe the same records but must never race the Leader's
			// replicated terminal commit.
			return nil, nil
		}
	}
	if authority != nil {
		if err := repository.BlockAbandonedRecoveries(ctx); err != nil {
			return nil, err
		}
	}
	return service.ReconcileAbandonedOperations(ctx, abandonedOperationStaleAfter)
}

func runAbandonedOperationReconciler(
	ctx context.Context,
	repository *store.Repository,
	service *workflow.Service,
	authority coordination.MutationAuthority,
) {
	reconcile := func() {
		records, err := reconcileAbandonedOperations(ctx, repository, service, authority)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("abandoned operation reconciliation failed: %v", err)
			}
			return
		}
		for _, record := range records {
			log.Printf("abandoned operation reconciled: operation_id=%s stage=%s status=%s failure_class=%s",
				record.ResourceID, record.Stage, record.Status, record.FailureClass)
		}
	}

	reconcile()
	ticker := time.NewTicker(abandonedOperationReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
