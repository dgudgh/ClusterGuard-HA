package workflow

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type recordingAdapter struct {
	adapter.UnsupportedAdapter
	trace   *[]string
	support bool
}

func newRecordingAdapter(trace *[]string, support bool) *recordingAdapter {
	return &recordingAdapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL), trace: trace, support: support}
}

func (candidate *recordingAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityPrecheck: {Available: candidate.support},
		adapter.CapabilityPlan:     {Available: candidate.support},
		adapter.CapabilityExecute:  {Available: candidate.support, Mutating: true},
		adapter.CapabilityVerify:   {Available: candidate.support},
	}}
}

func (candidate *recordingAdapter) Precheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	*candidate.trace = append(*candidate.trace, "adapter:precheck")
	return []model.Check{{Name: "adapter_ready", Status: model.CheckPass, Message: "ready"}}, nil
}

func (candidate *recordingAdapter) BuildPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	*candidate.trace = append(*candidate.trace, "adapter:plan")
	return model.OperationPlan{Summary: "safe plan", Mutating: true}, nil
}

func (candidate *recordingAdapter) Execute(context.Context, adapter.OperationRequest) (model.Execution, error) {
	*candidate.trace = append(*candidate.trace, "adapter:execute")
	return model.Execution{Status: model.OperationSucceeded, Message: "executed"}, nil
}

func (candidate *recordingAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	*candidate.trace = append(*candidate.trace, "adapter:verify")
	return model.Verification{Passed: true, Checks: []model.Check{{Name: "verified", Status: model.CheckPass}}}, nil
}

type recordingGate struct{ trace *[]string }

func (gate recordingGate) Evaluate(context.Context, model.Operation) error {
	*gate.trace = append(*gate.trace, "gate:safety")
	return nil
}
func (gate recordingGate) Acquire(context.Context, model.Operation) (func(), error) {
	*gate.trace = append(*gate.trace, "gate:lock")
	return func() { *gate.trace = append(*gate.trace, "gate:release") }, nil
}
func (gate recordingGate) Validate(context.Context, model.Operation, string) error {
	*gate.trace = append(*gate.trace, "gate:approval")
	return nil
}

func TestExecuteRunsGuardedWorkflowAndProducesAuditReport(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationSucceeded {
		t.Fatalf("unexpected execution: %+v", execution)
	}
	want := []string{"adapter:precheck", "adapter:plan", "gate:safety", "gate:lock", "gate:approval", "adapter:execute", "adapter:verify", "gate:release"}
	if len(trace) != len(want) {
		t.Fatalf("workflow trace: got %v want %v", trace, want)
	}
	for index := range want {
		if trace[index] != want[index] {
			t.Fatalf("workflow trace[%d]: got %q want %q", index, trace[index], want[index])
		}
	}
	if len(journal.Audits()) < 8 || len(journal.Reports()) != 1 {
		t.Fatalf("workflow must record audit and report, audits=%d reports=%d", len(journal.Audits()), len(journal.Reports()))
	}
	safetyIndex, lockIndex := -1, -1
	for index, event := range journal.Audits() {
		if event.Stage == model.StageSafetyGuard && safetyIndex < 0 {
			safetyIndex = index
		}
		if event.Stage == model.StageLock && lockIndex < 0 {
			lockIndex = index
		}
	}
	if safetyIndex < 0 || lockIndex < 0 || safetyIndex >= lockIndex {
		t.Fatalf("safety guard must be an explicit stage before lock: %+v", journal.Audits())
	}
}

func TestExecuteBlocksUnsupportedAdapterBeforeSafetyOrLock(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, false)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, Kind: model.OperationFailover}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if !errors.Is(err, adapter.ErrUnsupported) || execution.Status != model.OperationUnsupported {
		t.Fatalf("unsupported execution must fail closed, execution=%+v err=%v", execution, err)
	}
	if len(trace) != 0 {
		t.Fatalf("unsupported execution must not acquire safety gates: %v", trace)
	}
	if len(journal.Audits()) == 0 || len(journal.Reports()) != 1 {
		t.Fatalf("unsupported execution still needs audit and report")
	}
}
