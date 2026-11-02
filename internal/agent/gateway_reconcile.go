package agent

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"fmt"
)

// Gateway writer authority is signed and explicitly bound to the local
// policy. It never adds an IP address or weakens loss-of-quorum SQL fencing.
func (r *Reconciler) reconcileGateway(ctx context.Context, policy ClusterPolicy, decision ReconcileResponse) (ReconcileResult, error) {
	if policy.EndpointProvider != model.EndpointProviderGateway || decision.EndpointProvider != model.EndpointProviderGateway || (policy.Engine != model.EngineMySQL && policy.Engine != model.EnginePostgreSQL) || policy.VIP != "" || policy.Interface != "" || policy.Prefix != 0 || !model.ValidResourceID(decision.LeaseID) {
		return r.selfIsolate(ctx, policy, fmt.Errorf("gateway authorization does not match the local writer policy"))
	}
	if decision.Action != ReconcileKeepVIP && decision.Action != ReconcileTransitionSource && decision.Action != ReconcileTransitionTarget && decision.Action != ReconcileBootstrapPrimary {
		return r.selfIsolate(ctx, policy, fmt.Errorf("unsupported gateway writer authorization"))
	}
	if policy.Engine == model.EnginePostgreSQL {
		return r.reconcileGatewayPostgreSQL(ctx, policy, decision)
	}
	ro, sro, err := r.roles.Status(ctx, policy)
	if err != nil {
		return r.selfIsolate(ctx, policy, err)
	}
	if decision.Action == ReconcileTransitionSource || decision.Action == ReconcileTransitionTarget {
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: decision.Action, Message: "majority lease preserves controller-managed database transition; no host VIP"}, nil
	}
	if decision.Action == ReconcileBootstrapPrimary && (ro != sro) {
		return r.selfIsolate(ctx, policy, fmt.Errorf("gateway reboot bootstrap requires consistent read-only state"))
	}
	if decision.Action == ReconcileBootstrapPrimary && ro && sro {
		if err = r.roles.PersistReadOnly(ctx, policy, false); err != nil {
			return r.selfIsolate(ctx, policy, err)
		}
	} else if err = r.convergeWritableRestartState(ctx, policy); err != nil {
		return r.selfIsolate(ctx, policy, err)
	}
	ro, sro, err = r.roles.Status(ctx, policy)
	if err != nil || ro || sro {
		return r.selfIsolate(ctx, policy, fmt.Errorf("gateway writer is not proven writable: %v", err))
	}
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: decision.Action, Message: "majority lease authorizes database writer without a host VIP"}, nil
}

type postgreSQLWritableInspector interface {
	Writable(context.Context, ClusterPolicy) (bool, error)
}

func (r *Reconciler) reconcileGatewayPostgreSQL(ctx context.Context, policy ClusterPolicy, decision ReconcileResponse) (ReconcileResult, error) {
	if r.postgresql == nil {
		return r.selfIsolate(ctx, policy, fmt.Errorf("PostgreSQL gateway controller is unavailable"))
	}
	running, recovery, err := r.postgresql.Status(ctx, policy)
	if err != nil {
		return r.selfIsolate(ctx, policy, err)
	}
	if decision.Action == ReconcileTransitionSource || decision.Action == ReconcileTransitionTarget {
		if decision.Action == ReconcileTransitionTarget && !running {
			return r.selfIsolate(ctx, policy, fmt.Errorf("PostgreSQL gateway transition target is not running"))
		}
		return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: decision.Action, Message: "majority lease preserves controller-managed PostgreSQL transition without a host VIP"}, nil
	}
	inspector, ok := r.postgresql.(postgreSQLWritableInspector)
	if !running || recovery || !ok {
		return r.selfIsolate(ctx, policy, fmt.Errorf("PostgreSQL gateway requires a running native writer and SQL write-state inspection"))
	}
	writable, err := inspector.Writable(ctx, policy)
	if err != nil || !writable {
		return r.selfIsolate(ctx, policy, fmt.Errorf("PostgreSQL gateway writer is not proven writable: %v", err))
	}
	return ReconcileResult{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, Action: decision.Action, Message: "majority lease authorizes native PostgreSQL writer without a host VIP"}, nil
}
