package disaster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

type Store interface {
	RecoveryTask(model.ResourceID) (model.RecoveryTask, bool)
	BeginRecovery(context.Context, model.ResourceID, uint64, model.ResourceID) (model.RecoveryTask, error)
	AdvanceRecovery(context.Context, model.RecoveryTask, model.RecoveryEvent) (model.RecoveryTask, error)
	CommitRecovery(context.Context, model.RecoveryTask, model.TopologySnapshot) (model.RecoveryTask, error)
}

type Authority interface{ RequireMutationAuthority(context.Context) error }
type Locker interface {
	Acquire(context.Context, model.Operation) (context.Context, func(), error)
}
type Maintenance interface{ Check(context.Context) error }

// Driver must verify physical state, not just command completion. It never
// receives a user-supplied winner, evidence, shell command or filesystem path.
type Driver interface {
	Preflight(context.Context, model.RecoveryTask) error
	Fence(context.Context, model.RecoveryTask, model.DatabaseInstance) error
	Inspect(context.Context, model.RecoveryTask, model.DatabaseInstance) (model.RecoveryEvidence, error)
	Compare(context.Context, model.RecoveryTask, []model.RecoveryEvidence) ([]model.RecoveryProof, error)
	StartPrimary(context.Context, model.RecoveryTask) error
	Rebuild(context.Context, model.RecoveryTask, model.DatabaseInstance) error
	Verify(context.Context, model.RecoveryTask) (model.TopologySnapshot, error)
	Activate(context.Context, model.RecoveryTask) error
	Complete(context.Context, model.RecoveryTask) (model.RecoveryTask, error)
}

type Manager struct {
	Store         Store
	Authority     Authority
	Locks         Locker
	Maintenance   Maintenance
	Driver        Driver
	DrainInterval time.Duration
}

func (m *Manager) configured() bool {
	return m != nil && m.Store != nil && m.Authority != nil && m.Locks != nil && m.Maintenance != nil && m.Driver != nil
}

func (m *Manager) Preflight(ctx context.Context, task model.RecoveryTask) error {
	if !m.configured() {
		return fmt.Errorf("disaster recovery executor is not configured")
	}
	if err := m.Authority.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	if err := m.Maintenance.Check(ctx); err != nil {
		return err
	}
	return m.Driver.Preflight(ctx, task)
}

func (m *Manager) Execute(ctx context.Context, id model.ResourceID, revision uint64) (task model.RecoveryTask, err error) {
	if !m.configured() {
		return task, fmt.Errorf("disaster recovery executor is not configured")
	}
	task, found := m.Store.RecoveryTask(id)
	if !found || task.MetadataRevision != revision {
		return task, fmt.Errorf("recovery confirmation is stale")
	}
	if err = m.Preflight(ctx, task); err != nil {
		return task, err
	}
	leaseCtx, release, err := m.Locks.Acquire(ctx, model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: id}, ClusterID: task.ClusterID})
	if err != nil {
		return task, err
	}
	defer release()
	leaseID := adapter.OperationLeaseID(leaseCtx)
	task, err = m.Store.BeginRecovery(leaseCtx, id, revision, leaseID)
	if err != nil {
		return task, err
	}
	defer func() {
		if err == nil {
			return
		}
		// Failed activation must not leave a writer serving behind a blocked
		// task. With lost authority the Agent's expiring permit self-fences.
		if task.PrimaryID != "" && m.Authority.RequireMutationAuthority(leaseCtx) == nil {
			for _, member := range task.Members {
				if member.ResourceID == task.PrimaryID {
					err = errors.Join(err, m.Driver.Fence(leaseCtx, task, member))
				}
			}
		}
		blocked, persistErr := m.Store.AdvanceRecovery(leaseCtx, task, model.RecoveryEvent{Stage: model.RecoveryBlocked, Message: redact.Bounded(err.Error(), 2048)})
		if persistErr == nil {
			task = blocked
		}
		err = errors.Join(err, persistErr)
	}()
	advance := func(stage model.RecoveryStage, member model.ResourceID, message string) error {
		if cause := context.Cause(leaseCtx); cause != nil {
			return cause
		}
		if e := m.Authority.RequireMutationAuthority(leaseCtx); e != nil {
			return e
		}
		updated, e := m.Store.AdvanceRecovery(leaseCtx, task, model.RecoveryEvent{Stage: stage, InstanceID: member, Message: message})
		if e == nil {
			task = updated
		}
		return e
	}
	if task.CommittedAt.IsZero() {
		for _, member := range task.Members {
			if err = advance(model.RecoveryFencing, member.ResourceID, "isolating member and verifying persistent write fence"); err != nil {
				return task, err
			}
			if err = m.Driver.Fence(leaseCtx, task, member); err != nil {
				return task, err
			}
			if err = advance(model.RecoveryFencing, member.ResourceID, "member write fence verified"); err != nil {
				return task, err
			}
		}
		drain := m.DrainInterval
		if drain <= 0 {
			drain = 11 * time.Second
		}
		timer := time.NewTimer(drain)
		select {
		case <-leaseCtx.Done():
			timer.Stop()
			return task, context.Cause(leaseCtx)
		case <-timer.C:
		}
		if err = advance(model.RecoveryInspecting, "", "all members fenced; collecting recoverable history"); err != nil {
			return task, err
		}
		evidence := make([]model.RecoveryEvidence, 0, len(task.Members))
		for _, member := range task.Members {
			if err = advance(model.RecoveryInspecting, member.ResourceID, "collecting native identity and recoverable transaction history"); err != nil {
				return task, err
			}
			e, eErr := m.Driver.Inspect(leaseCtx, task, member)
			if eErr != nil {
				return task, eErr
			}
			evidence = append(evidence, e)
		}
		if err = ValidateEvidence(task.Members, evidence); err != nil {
			return task, err
		}
		if err = advance(model.RecoverySelecting, "", "comparing committed history of every member"); err != nil {
			return task, err
		}
		proofs, proofErr := m.Driver.Compare(leaseCtx, task, evidence)
		if proofErr != nil {
			return task, proofErr
		}
		primary, selectionErr := Select(task.Members, evidence, proofs)
		if selectionErr != nil {
			return task, selectionErr
		}
		task.Evidence = evidence
		task.Proofs = proofs
		task.PrimaryID = primary
		if err = advance(model.RecoveryStarting, primary, "authoritative primary selected from complete committed history"); err != nil {
			return task, err
		}
		if err = m.Driver.StartPrimary(leaseCtx, task); err != nil {
			return task, err
		}
		if err = advance(model.RecoveryRebuilding, primary, "selected primary started under recovery-only authorization"); err != nil {
			return task, err
		}
		for _, member := range task.Members {
			if member.ResourceID == primary {
				continue
			}
			if err = advance(model.RecoveryRebuilding, member.ResourceID, "reconstructing replica from the selected authoritative primary"); err != nil {
				return task, err
			}
			if err = m.Driver.Rebuild(leaseCtx, task, member); err != nil {
				return task, err
			}
			if err = advance(model.RecoveryRebuilding, member.ResourceID, "replica reconstruction and upstream identity verified"); err != nil {
				return task, err
			}
		}
		if err = advance(model.RecoveryVerifying, "", "verifying every role, replication link and catch-up position"); err != nil {
			return task, err
		}
		topology, verifyErr := m.Driver.Verify(leaseCtx, task)
		if verifyErr != nil {
			return task, verifyErr
		}
		committed, commitErr := m.Store.CommitRecovery(leaseCtx, task, topology)
		err = commitErr
		if err != nil {
			return task, err
		}
		task = committed
	}
	if err = m.Authority.RequireMutationAuthority(leaseCtx); err != nil {
		return task, err
	}
	if err = m.Driver.Activate(leaseCtx, task); err != nil {
		return task, err
	}
	completed, completeErr := m.Driver.Complete(leaseCtx, task)
	if completeErr != nil {
		return task, completeErr
	}
	return completed, nil
}
