package recovery

import (
	"context"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

// The cluster policy is replicated metadata, so the controller may not cache
// it: a change made on the Leader has to be honoured by the very next cycle,
// or the console is advertising an "immediate" effect it does not deliver.

func TestControllerReadsOperationBudgetFromPolicyEveryRound(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	state := recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}}
	evidence := recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}}
	selector := recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}}

	budget := 120 * time.Second
	executor := &deadlineRecoveryExecutor{}
	controller := NewController(state, evidence, selector, executor, recoveryAuthorityStub{}, 30*time.Second,
		func() time.Time { return now },
		WithOperationTimeout(90*time.Second),
		WithOperationTimeoutProvider(func() time.Duration { return budget }),
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	if remaining := time.Until(executor.deadline); remaining <= 119*time.Second || remaining > 120*time.Second {
		t.Fatalf("the policy budget did not win over the start-up value: remaining=%s", remaining)
	}

	// Zero means "no override", so clearing the policy must fall back to the
	// budget the node was started with rather than to an unbounded operation.
	budget = 0
	fallback := &deadlineRecoveryExecutor{}
	controller = NewController(state, evidence, selector, fallback, recoveryAuthorityStub{}, 30*time.Second,
		func() time.Time { return now },
		WithOperationTimeout(90*time.Second),
		WithOperationTimeoutProvider(func() time.Duration { return budget }),
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	if remaining := time.Until(fallback.deadline); remaining <= 89*time.Second || remaining > 90*time.Second {
		t.Fatalf("a cleared policy must restore the configured budget: remaining=%s", remaining)
	}
}

func TestControllerPausesAutomaticFailoverWhileSuppressed(t *testing.T) {
	now := time.Date(2026, time.July, 13, 22, 30, 30, 0, time.UTC)
	cluster, snapshot, targetID := recoveryFixture(now)
	incident := now.Add(-30 * time.Second)
	executor := &recoveryExecutorStub{}
	suppressed := true
	controller := NewController(
		recoveryStateStub{clusters: []model.DatabaseCluster{cluster}, snapshots: map[model.ResourceID]model.TopologySnapshot{cluster.ResourceID: snapshot}},
		recoveryFailureEvidenceStub{incidents: map[model.ResourceID]time.Time{cluster.ResourceID: incident}},
		recoverySelectorStub{targets: map[model.ResourceID]model.ResourceID{cluster.ResourceID: targetID}},
		executor, recoveryAuthorityStub{}, 30*time.Second, func() time.Time { return now },
		WithSuppression(func() bool { return suppressed }),
	)
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("suppression must not be reported as a failure: %v", err)
	}
	if requests, _ := executor.calls(); len(requests) != 0 {
		t.Fatalf("automatic failover ran during planned maintenance: %d", len(requests))
	}

	// Lifting the suppression on the next round must take effect immediately.
	suppressed = false
	if err := controller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run automatic failover: %v", err)
	}
	if requests, _ := executor.calls(); len(requests) != 1 {
		t.Fatalf("lifting suppression did not resume failover: %d", len(requests))
	}
}
