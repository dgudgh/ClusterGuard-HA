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

func (reconciler *Reconciler) selfIsolate(ctx context.Context, policy ClusterPolicy, cause error) (ReconcileResult, error) {
	releaseErr := reconciler.vip.Release(ctx, policy)
	roleErr := reconciler.roles.PersistReadOnly(ctx, policy, true)
	message := "VIP released and MySQL persisted read-only"
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: ReconcileSelfIsolate, Message: message}, errors.Join(ErrSelfIsolated, cause, releaseErr, roleErr)
}

func (reconciler *Reconciler) reconcile(ctx context.Context, policy ClusterPolicy) (ReconcileResult, error) {
	if !model.ValidResourceID(policy.ClusterID) || !model.ValidResourceID(policy.InstanceID) {
		return ReconcileResult{}, fmt.Errorf("agent reconcile policy identity is invalid")
	}
	decision, err := reconciler.decisions.Decision(ctx, policy)
	if err != nil {
		return reconciler.selfIsolate(ctx, policy, err)
	}
	if decision.ClusterID != policy.ClusterID || decision.InstanceID != policy.InstanceID || decision.Action != ReconcileKeepVIP || !model.ValidResourceID(decision.LeaseID) {
		return reconciler.selfIsolate(ctx, policy, fmt.Errorf("controller did not authorize local VIP ownership"))
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
