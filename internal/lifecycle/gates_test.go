package lifecycle

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type lifecycleMaintenanceStub struct{ err error }

func (stub lifecycleMaintenanceStub) Check(context.Context) error { return stub.err }

func TestPlanSafetyGuardRejectsBlockedFailedOrIdentityChangingPlans(t *testing.T) {
	request, plan := executableLifecyclePlan()
	guard := PlanSafetyGuard{}
	if err := guard.EvaluateLifecycle(context.Background(), request, plan); err != nil {
		t.Fatalf("valid plan was blocked: %v", err)
	}

	blocked := plan
	blocked.Blocked = true
	if err := guard.EvaluateLifecycle(context.Background(), request, blocked); err == nil {
		t.Fatal("blocked plan passed safety guard")
	}
	failed := plan
	failed.Checks = append(failed.Checks, model.Check{Name: "unsafe", Status: model.CheckFail})
	if err := guard.EvaluateLifecycle(context.Background(), request, failed); err == nil {
		t.Fatal("failed check passed safety guard")
	}
	changed := plan
	changed.Targets = append([]TargetPlan{}, plan.Targets...)
	changed.Targets[0].NodeName = "different-fixed-name"
	if err := guard.EvaluateLifecycle(context.Background(), request, changed); err == nil {
		t.Fatal("plan target identity changed after precheck")
	}
}

func TestTokenApprovalFailsClosedAndUsesExactToken(t *testing.T) {
	request, plan := executableLifecyclePlan()
	approval := TokenApproval{ExpectedToken: "approved-secret"}
	for _, token := range []string{"", "approved", " approved-secret"} {
		if err := approval.ValidateLifecycle(context.Background(), request, plan, token); err == nil {
			t.Fatalf("token %q was accepted", token)
		}
	}
	if err := approval.ValidateLifecycle(context.Background(), request, plan, "approved-secret"); err != nil {
		t.Fatalf("valid approval token was rejected: %v", err)
	}
}

func TestCompositeSafetyGuardBlocksLifecycleDuringSoftwareUpdate(t *testing.T) {
	request, plan := executableLifecyclePlan()
	guard := NewCompositeSafetyGuard(
		PlanSafetyGuard{},
		MaintenanceSafetyGuard{Gate: lifecycleMaintenanceStub{err: errors.New("update active")}},
	)
	if err := guard.EvaluateLifecycle(context.Background(), request, plan); err == nil {
		t.Fatal("lifecycle execution bypassed software update maintenance")
	}
}
