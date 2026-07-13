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
	vip       VIPController
	roles     RoleController
	decisions ReconcileDecisionClient
}

func NewReconciler(vip VIPController, roles RoleController, decisions ReconcileDecisionClient) *Reconciler {
	return &Reconciler{vip: vip, roles: roles, decisions: decisions}
}

func (reconciler *Reconciler) convergeSelfIsolation(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	releaseErr := reconciler.vip.Release(ctx, policy)
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
	if !model.ValidResourceID(decision.LeaseID) || (decision.Action != ReconcileKeepVIP && decision.Action != ReconcileBootstrapPrimary) {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("controller did not authorize local VIP ownership"))
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

func (reconciler *Reconciler) bootstrapPrimary(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	readOnly, superReadOnly, err := reconciler.roles.Status(ctx, policy)
	if err != nil || !readOnly || !superReadOnly {
		if err == nil {
			err = fmt.Errorf("reboot bootstrap requires MySQL to be fully read-only")
		}
		return reconciler.selfIsolate(ctx, policy, err)
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
