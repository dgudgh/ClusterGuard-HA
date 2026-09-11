package sqlserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type retryVerificationRunner struct {
	calls      int
	cancel     context.CancelFunc
	queryError bool
}

func (*retryVerificationRunner) Executable(context.Context) bool { return true }
func (r *retryVerificationRunner) Exec(ctx context.Context, _ adapter.Endpoint, _ adapter.Credentials, _ string) (string, error) {
	r.calls++
	if r.cancel != nil {
		r.cancel()
	}
	if r.queryError {
		return "", errors.New("query failed")
	}
	if r.calls == 1 {
		return "0|0|0", nil
	}
	return "1|1|1", nil
}

func TestSQLServerVerificationConvergenceFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"converges", "cancelled-before-query", "cancelled-incomplete", "query-error"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &retryVerificationRunner{queryError: name == "query-error"}
			if name == "cancelled-incomplete" || name == "query-error" {
				runner.cancel = cancel
			}
			a := NewWithRunner(runner)
			r := sqlServerOperationRequest(model.OperationSwitchover)
			p, err := a.BuildPlan(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			r.Plan = &p
			if name == "cancelled-before-query" {
				cancel()
			}
			v, err := a.Verify(ctx, r)
			if name == "converges" {
				if err != nil || !v.Passed || runner.calls != 2 {
					t.Fatalf("convergence: %+v %v calls=%d", v, err, runner.calls)
				}
			} else if err == nil || v.Passed {
				t.Fatalf("failure accepted: %+v %v", v, err)
			}
			if name == "cancelled-before-query" && runner.calls != 0 {
				t.Fatal("queried after cancellation")
			}
		})
	}
}

type deadlineRunner struct {
	deadline time.Time
	bounded  bool
}

func (*deadlineRunner) Executable(context.Context) bool { return true }
func (r *deadlineRunner) Exec(ctx context.Context, _ adapter.Endpoint, _ adapter.Credentials, _ string) (string, error) {
	r.deadline, r.bounded = ctx.Deadline()
	return "1|1|1", nil
}

func TestSQLServerVerifyAddsBoundWithoutExtendingCallerDeadline(t *testing.T) {
	for _, short := range []bool{false, true} {
		runner := &deadlineRunner{}
		a := NewWithRunner(runner)
		r := sqlServerOperationRequest(model.OperationSwitchover)
		p, err := a.BuildPlan(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		r.Plan = &p
		ctx := context.Background()
		var deadline time.Time
		if short {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Second)
			defer cancel()
			deadline, _ = ctx.Deadline()
		}
		v, err := a.Verify(ctx, r)
		if err != nil || !v.Passed {
			t.Fatalf("verification: %+v %v", v, err)
		}
		if !runner.bounded || time.Until(runner.deadline) > 30*time.Second {
			t.Fatalf("query has no finite verification budget: %v %v", runner.bounded, runner.deadline)
		}
		if short && runner.deadline.After(deadline) {
			t.Fatal("caller deadline extended")
		}
	}
}
