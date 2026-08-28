package lifecycle

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type PlanSafetyGuard struct{}

type MaintenanceChecker interface {
	Check(context.Context) error
}

type MaintenanceSafetyGuard struct {
	Gate MaintenanceChecker
}

func (guard MaintenanceSafetyGuard) EvaluateLifecycle(ctx context.Context, _ Request, _ Plan) error {
	if guard.Gate == nil {
		return fmt.Errorf("software update maintenance guard is not configured")
	}
	if err := guard.Gate.Check(ctx); err != nil {
		return fmt.Errorf("software update maintenance gate blocked lifecycle execution: %w", err)
	}
	return nil
}

type CompositeSafetyGuard struct {
	guards []SafetyGuard
}

func NewCompositeSafetyGuard(guards ...SafetyGuard) CompositeSafetyGuard {
	return CompositeSafetyGuard{guards: append([]SafetyGuard{}, guards...)}
}

func (guard CompositeSafetyGuard) EvaluateLifecycle(ctx context.Context, request Request, plan Plan) error {
	if len(guard.guards) == 0 {
		return fmt.Errorf("no lifecycle safety guards are configured")
	}
	for _, candidate := range guard.guards {
		if candidate == nil {
			return fmt.Errorf("nil lifecycle safety guard is configured")
		}
		if err := candidate.EvaluateLifecycle(ctx, request, plan); err != nil {
			return err
		}
	}
	return nil
}

func sameLifecycleTarget(request Target, planned TargetPlan) bool {
	if strings.TrimSpace(request.NodeName) != strings.TrimSpace(planned.NodeName) || request.Kind != planned.Kind || !strings.EqualFold(strings.TrimSpace(request.Hostname), strings.TrimSpace(planned.Hostname)) || strings.TrimSpace(request.IPAddress) != strings.TrimSpace(planned.IPAddress) || request.MySQLPort != planned.MySQLPort || request.Rebuild != planned.Rebuild {
		return false
	}
	if request.NodeID != "" && request.NodeID != planned.NodeID {
		return false
	}
	return model.ValidResourceID(planned.NodeID)
}

func (PlanSafetyGuard) EvaluateLifecycle(ctx context.Context, request Request, plan Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.Blocked || plan.ClusterID != request.ClusterID || plan.Action != request.Action || len(plan.Targets) != len(request.Targets) {
		return fmt.Errorf("lifecycle plan does not match the prechecked request")
	}
	for _, check := range plan.Checks {
		if check.Status == model.CheckFail {
			return fmt.Errorf("lifecycle plan contains a blocking safety check")
		}
	}
	for index, target := range plan.Targets {
		if !sameLifecycleTarget(request.Targets[index], target) {
			return fmt.Errorf("lifecycle target identity changed after precheck")
		}
		if isLifecycleDataKind(target.Kind) && (target.DatabaseRole != model.RoleReplica || target.SyncMethod == "") {
			return fmt.Errorf("lifecycle data target is not a verified replica synchronization plan")
		}
	}
	if plan.FinalControllerCount != 0 && (plan.FinalControllerCount < 3 || plan.FinalControllerCount%2 == 0) {
		return fmt.Errorf("lifecycle controller membership is not an odd quorum")
	}
	return nil
}

func isLifecycleDataKind(kind model.NodeKind) bool {
	return kind == model.NodeData || kind == model.NodeMixed
}

type TokenApproval struct {
	ExpectedToken string
}

func (approval TokenApproval) ValidateLifecycle(ctx context.Context, _ Request, _ Plan, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if approval.ExpectedToken == "" || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(approval.ExpectedToken)) != 1 {
		return fmt.Errorf("valid lifecycle approval is required")
	}
	return nil
}
