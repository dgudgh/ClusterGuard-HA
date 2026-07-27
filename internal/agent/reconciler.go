package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"clusterguard.io/ha/pkg/model"
)

var ErrSelfIsolated = errors.New("local database instance was self-isolated")

type ReconcileDecisionClient interface {
	Decision(context.Context, ClusterPolicy) (ReconcileResponse, error)
}

type ReconcileResult struct {
	ClusterID  model.ResourceID `json:"cluster_id"`
	InstanceID model.ResourceID `json:"instance_id"`
	Action     ReconcileAction  `json:"action"`
	Message    string           `json:"message"`
}

type Reconciler struct {
	vip        VIPController
	roles      RoleController
	postgresql PostgreSQLController
	decisions  ReconcileDecisionClient
}

type ReconcilerOption func(*Reconciler)

func WithPostgreSQLReconcileController(controller PostgreSQLController) ReconcilerOption {
	return func(reconciler *Reconciler) { reconciler.postgresql = controller }
}

func NewReconciler(vip VIPController, roles RoleController, decisions ReconcileDecisionClient, options ...ReconcilerOption) *Reconciler {
	reconciler := &Reconciler{vip: vip, roles: roles, decisions: decisions}
	for _, option := range options {
		if option != nil {
			option(reconciler)
		}
	}
	return reconciler
}

func (reconciler *Reconciler) convergeSelfIsolation(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	releaseErr := reconciler.vip.Release(ctx, policy)
	if policy.Engine == model.EnginePostgreSQL {
		if reconciler.postgresql == nil {
			return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: "VIP released; PostgreSQL controller is unavailable"}, errors.Join(releaseErr, fmt.Errorf("PostgreSQL reconciler controller is not configured"))
		}
		running, inRecovery, statusErr := reconciler.postgresql.Status(ctx, policy)
		if statusErr != nil {
			return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: "VIP released; PostgreSQL status is unknown"}, errors.Join(releaseErr, statusErr)
		}
		if statusErr == nil && running && inRecovery {
			return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: "VIP released; PostgreSQL streaming standby remains active"}, releaseErr
		}
		if statusErr == nil && !running {
			return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: "VIP released; PostgreSQL is not currently eligible for ownership"}, releaseErr
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: "VIP released; PostgreSQL writer left running without VIP pending controller authorization"}, releaseErr
	}
	roleErr := reconciler.roles.PersistReadOnly(ctx, policy, true)
	message := "VIP released and MySQL persisted read-only"
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: message}, errors.Join(releaseErr, roleErr)
}

func (reconciler *Reconciler) selfIsolate(ctx context.Context, policy ClusterPolicy, cause error) (ReconcileResult, error) {
	result, isolationErr := reconciler.convergeSelfIsolation(ctx, policy)
	return result, errors.Join(ErrSelfIsolated, cause, isolationErr)
}

func (reconciler *Reconciler) reconcile(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	if !model.ValidResourceID(policy.ClusterID) || !model.ValidResourceID(policy.InstanceID) {
		return ReconcileResult{}, fmt.Errorf("agent reconcile policy identity is invalid")
	}
	decision, err := reconciler.decisions.Decision(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if decision.ClusterID != policy.ClusterID || decision.InstanceID != policy.InstanceID {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("controller did not authorize local VIP ownership"))
	}
	if decision.Action == ReconcileSelfIsolate {
		return reconciler.convergeSelfIsolation(ctx, policy)
	}
	if !model.ValidResourceID(decision.LeaseID) || (decision.Action != ReconcileKeepVIP && decision.Action != ReconcileTransitionTarget && decision.Action != ReconcileTransitionSource && decision.Action != ReconcileBootstrapPrimary) {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("controller did not authorize local VIP ownership"))
	}
	if policy.Engine == model.EnginePostgreSQL {
		return reconciler.reconcilePostgreSQL(ctx, policy, decision.Action)
	}
	if decision.Action == ReconcileTransitionSource {
		return reconciler.reconcileTransitionSource(ctx, policy)
	}
	if decision.Action == ReconcileTransitionTarget {
		return reconciler.reconcileTransitionTarget(ctx, policy)
	}
	if decision.Action == ReconcileBootstrapPrimary {
		return reconciler.bootstrapPrimary(ctx, policy)
	}
	readOnly, superReadOnly, err := reconciler.roles.Status(ctx, policy)
	if err != nil || readOnly || superReadOnly {
		if err == nil {
			err = fmt.Errorf("MySQL is not proven writable")
		}
		return reconciler.selfIsolate(ctx, policy, err)
	}
	ownsVIP, err := reconciler.vip.Status(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if !ownsVIP {
		if err := reconciler.vip.Acquire(ctx, policy); err != nil {
			return reconciler.selfIsolate(ctx, policy, fmt.Errorf("acquire authorized VIP: %w", err))
		}
	}
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP, Message: "active majority lease authorizes local VIP ownership"}, nil
}

func (reconciler *Reconciler) reconcilePostgreSQL(ctx context.Context, policy ClusterPolicy, action ReconcileAction) (ReconcileResult, error) {
	if reconciler.postgresql == nil {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("PostgreSQL reconciler controller is not configured"))
	}
	switch action {
	case ReconcileTransitionSource:
		if _, _, err := reconciler.postgresql.Status(ctx, policy); err != nil {
			return reconciler.selfIsolate(ctx, policy, err)
		}
		if _, err := reconciler.vip.Status(ctx, policy); err != nil {
			return reconciler.selfIsolate(ctx, policy, err)
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: action, Message: "majority lease holds the PostgreSQL source for controlled transition"}, nil
	case ReconcileTransitionTarget:
		running, inRecovery, err := reconciler.postgresql.Status(ctx, policy)
		if err != nil || !running {
			if err == nil {
				err = fmt.Errorf("PostgreSQL transition target is not running")
			}
			return reconciler.selfIsolate(ctx, policy, err)
		}
		ownsVIP, err := reconciler.vip.Status(ctx, policy)
		if err != nil {
			return reconciler.selfIsolate(ctx, policy, err)
		}
		if inRecovery && ownsVIP {
			if err := reconciler.vip.Release(ctx, policy); err != nil {
				return reconciler.selfIsolate(ctx, policy, fmt.Errorf("release VIP from PostgreSQL standby transition target: %w", err))
			}
		}
		message := "majority lease preserves the promoted PostgreSQL target while the controller transfers the VIP"
		if inRecovery {
			message = "majority lease holds the PostgreSQL standby without VIP for controlled promotion"
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: action, Message: message}, nil
	case ReconcileBootstrapPrimary, ReconcileKeepVIP:
		running, inRecovery, err := reconciler.postgresql.Status(ctx, policy)
		if err != nil || !running || inRecovery {
			if err == nil {
				err = fmt.Errorf("PostgreSQL VIP ownership requires a running primary outside recovery")
			}
			return reconciler.selfIsolate(ctx, policy, err)
		}
		ownsVIP, err := reconciler.vip.Status(ctx, policy)
		if err != nil {
			return reconciler.selfIsolate(ctx, policy, err)
		}
		if !ownsVIP {
			if err := reconciler.vip.Acquire(ctx, policy); err != nil {
				return reconciler.selfIsolate(ctx, policy, fmt.Errorf("acquire authorized PostgreSQL VIP: %w", err))
			}
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: action, Message: "active majority lease authorizes the running PostgreSQL primary and VIP"}, nil
	default:
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("unsupported PostgreSQL reconcile action %q", action))
	}
}

func (reconciler *Reconciler) reconcileTransitionSource(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	if _, _, err := reconciler.roles.Status(ctx, policy); err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if _, err := reconciler.vip.Status(ctx, policy); err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	return ReconcileResult{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionSource,
		Message: "majority lease holds the source for the controlled transition",
	}, nil
}

func (reconciler *Reconciler) reconcileTransitionTarget(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	readOnly, superReadOnly, err := reconciler.roles.Status(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	ownsVIP, err := reconciler.vip.Status(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if readOnly || superReadOnly {
		if ownsVIP {
			if err := reconciler.vip.Release(ctx, policy); err != nil {
				return reconciler.selfIsolate(ctx, policy, fmt.Errorf("release VIP while transition target is read-only: %w", err))
			}
		}
		if readOnly != superReadOnly {
			return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, Message: "majority lease preserves the bounded read-only transition while the controller completes promotion"}, nil
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, Message: "majority lease holds the read-only target for controlled promotion"}, nil
	}
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileTransitionTarget, Message: "majority lease preserves the promoted target while the controller transfers the VIP"}, nil
}

func (reconciler *Reconciler) bootstrapPrimary(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	readOnly, superReadOnly, err := reconciler.roles.Status(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if !readOnly && !superReadOnly {
		ownsVIP, statusErr := reconciler.vip.Status(ctx, policy)
		if statusErr != nil || !ownsVIP {
			if statusErr == nil {
				statusErr = fmt.Errorf("writable reboot bootstrap target does not own the authorized VIP")
			}
			return reconciler.selfIsolate(ctx, policy, statusErr)
		}
		return ReconcileResult{
			ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileKeepVIP,
			Message: "majority lease bootstrap is already converged",
		}, nil
	}
	if !readOnly || !superReadOnly {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("reboot bootstrap requires MySQL to be fully read-only"))
	}
	ownsVIP, err := reconciler.vip.Status(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if !ownsVIP {
		if err := reconciler.vip.Acquire(ctx, policy); err != nil {
			return reconciler.selfIsolate(ctx, policy, fmt.Errorf("acquire reboot bootstrap VIP: %w", err))
		}
	}
	if err := reconciler.roles.PersistReadOnly(ctx, policy, false); err != nil {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("activate rebooted primary: %w", err))
	}
	readOnly, superReadOnly, err = reconciler.roles.Status(ctx, policy)
	if err != nil || readOnly || superReadOnly {
		if err == nil {
			err = fmt.Errorf("rebooted primary did not become fully writable")
		}
		return reconciler.selfIsolate(ctx, policy, err)
	}
	ownsVIP, err = reconciler.vip.Status(ctx, policy)
	if err != nil || !ownsVIP {
		if err == nil {
			err = fmt.Errorf("rebooted primary does not own the authorized VIP")
		}
		return reconciler.selfIsolate(ctx, policy, err)
	}
	return ReconcileResult{
		ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileBootstrapPrimary,
		Message: "majority lease restored the rebooted primary and its VIP",
	}, nil
}

func (reconciler *Reconciler) ReconcileAll(ctx context.Context, policies map[model.ResourceID]ClusterPolicy) ([]ReconcileResult, error) {
	if reconciler == nil || reconciler.vip == nil || reconciler.roles == nil || reconciler.decisions == nil {
		return nil, fmt.Errorf("agent reconciler is not configured")
	}
	clusterIDs := make([]model.ResourceID, 0, len(policies))
	for clusterID := range policies {
		clusterIDs = append(clusterIDs, clusterID)
	}
	sort.Slice(clusterIDs, func(i, j int) bool { return clusterIDs[i] < clusterIDs[j] })
	results := make([]ReconcileResult, 0, len(clusterIDs))
	var failures []error
	for _, clusterID := range clusterIDs {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		result, err := reconciler.reconcile(ctx, policies[clusterID])
		if result.ClusterID != "" {
			results = append(results, result)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("cluster %s: %w", clusterID, err))
		}
	}
	return results, errors.Join(failures...)
}
