package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

var workflowTestObservation = time.Date(2026, time.July, 12, 8, 0, 0, 123, time.UTC)

type recordingAdapter struct {
	adapter.UnsupportedAdapter
	trace   *[]string
	support bool
}

type capabilityMismatchAdapter struct {
	*recordingAdapter
	missing adapter.Capability
}

func (candidate *capabilityMismatchAdapter) Capabilities(context.Context) adapter.Capabilities {
	features := map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityPrecheck: {Available: true},
		adapter.CapabilityPlan:     {Available: true},
		adapter.CapabilityExecute:  {Available: true, Mutating: true},
		adapter.CapabilityVerify:   {Available: true},
	}
	features[candidate.missing] = adapter.CapabilityState{Available: false}
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: features}
}

func newRecordingAdapter(trace *[]string, support bool) *recordingAdapter {
	return &recordingAdapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL), trace: trace, support: support}
}

func (candidate *recordingAdapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityPrecheck:          {Available: candidate.support},
		adapter.CapabilityPlan:              {Available: candidate.support},
		adapter.CapabilityExecute:           {Available: candidate.support, Mutating: true},
		adapter.CapabilityVerify:            {Available: candidate.support},
		adapter.CapabilityMetadataReconcile: {Available: candidate.support},
	}}
}

func (candidate *recordingAdapter) MetadataPrecheck(context.Context, adapter.MetadataRequest) ([]model.Check, error) {
	return []model.Check{{Name: "metadata_ready", Status: model.CheckPass}}, nil
}

func (candidate *recordingAdapter) ReconcileMetadata(context.Context, adapter.MetadataRequest) (adapter.MetadataResult, error) {
	return adapter.MetadataResult{Summary: "metadata ready"}, nil
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

func (gate recordingGate) CaptureObservation(_ context.Context, operation model.Operation) (ObservationToken, error) {
	*gate.trace = append(*gate.trace, "gate:discover")
	return ObservationToken{ClusterID: operation.ClusterID, ObservedAt: workflowTestObservation}, nil
}

func (gate recordingGate) RevalidateObservation(context.Context, model.Operation, ObservationToken) error {
	*gate.trace = append(*gate.trace, "gate:revalidate")
	return nil
}

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

type failingJournal struct {
	err error
}

type committedWarning struct{ message string }

func (warning committedWarning) Error() string { return warning.message }
func (committedWarning) Committed() bool       { return true }

func (journal failingJournal) RecordAudit(model.AuditEvent) error { return journal.err }
func (journal failingJournal) RecordReport(model.Report) error    { return journal.err }

type stageFailingJournal struct {
	*MemoryJournal
	stage model.WorkflowStage
	err   error
}

func (journal stageFailingJournal) RecordAudit(event model.AuditEvent) error {
	if event.Stage == journal.stage {
		return journal.err
	}
	return journal.MemoryJournal.RecordAudit(event)
}

type postCommitReportJournal struct {
	*MemoryJournal
}

func (journal postCommitReportJournal) RecordReport(report model.Report) error {
	if err := journal.MemoryJournal.RecordReport(report); err != nil {
		return err
	}
	return committedWarning{message: "report committed with durability warning"}
}

func TestExecuteStillVerifiesAfterPostCommitJournalFailure(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := stageFailingJournal{MemoryJournal: NewMemoryJournal(), stage: model.StageExecute, err: errors.New("execute audit unavailable")}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if !errors.Is(err, ErrJournalPersistence) {
		t.Fatalf("execute error = %v, want journal persistence failure", err)
	}
	if execution.Status != model.OperationIndeterminate {
		t.Fatalf("execution status = %q, want indeterminate", execution.Status)
	}
	executeIndex, verifyIndex := -1, -1
	for index, entry := range trace {
		if entry == "adapter:execute" {
			executeIndex = index
		}
		if entry == "adapter:verify" {
			verifyIndex = index
		}
	}
	if executeIndex < 0 || verifyIndex <= executeIndex {
		t.Fatalf("verification did not follow committed execution: %v", trace)
	}
	foundVerify := false
	for _, event := range journal.Audits() {
		if event.Stage == model.StageVerify {
			foundVerify = true
		}
	}
	if !foundVerify {
		t.Fatalf("verification outcome was not audited: %+v", journal.Audits())
	}
}

func TestReportPostCommitWarningRewritesDurableOutcomeAsIndeterminate(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := postCommitReportJournal{MemoryJournal: NewMemoryJournal()}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("report warning execution=%+v err=%v", execution, err)
	}
	reports := journal.Reports()
	if len(reports) != 1 || !strings.Contains(reports[0].Summary, "journal persistence failed") {
		t.Fatalf("durable report disagrees with indeterminate response: %+v", reports)
	}
}

func TestMetadataStillVerifiesAfterPostCommitJournalFailure(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := stageFailingJournal{MemoryJournal: NewMemoryJournal(), stage: model.StageExecute, err: errors.New("execute audit unavailable")}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	committed := false
	request := adapter.MetadataRequest{ClusterID: model.NewResourceID(), Instance: model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	}}
	execution, err := service.ExecuteMetadata(context.Background(), model.Operation{RequestedBy: "dba"}, request, "approved", func() error {
		committed = true
		return nil
	})
	if !committed || !errors.Is(err, ErrJournalPersistence) {
		t.Fatalf("metadata result committed=%t err=%v", committed, err)
	}
	if execution.Status != model.OperationIndeterminate {
		t.Fatalf("metadata execution status = %q, want indeterminate", execution.Status)
	}
	foundVerify := false
	for _, event := range journal.Audits() {
		if event.Stage == model.StageVerify {
			foundVerify = true
		}
	}
	if !foundVerify {
		t.Fatalf("metadata verification outcome was not audited: %+v", journal.Audits())
	}
}

func TestMetadataStillVerifiesAfterCommittedPersistenceWarning(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	warning := committedWarning{message: "metadata committed with durability warning"}
	request := adapter.MetadataRequest{ClusterID: model.NewResourceID(), Instance: model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	}}
	execution, err := service.ExecuteMetadata(context.Background(), model.Operation{RequestedBy: "dba"}, request, "approved", func() error {
		return warning
	})
	if !errors.Is(err, warning) {
		t.Fatalf("metadata warning = %v, want committed persistence warning", err)
	}
	if execution.Status != model.OperationIndeterminate {
		t.Fatalf("metadata execution status = %q, want indeterminate", execution.Status)
	}
	foundVerify := false
	for _, event := range journal.Audits() {
		if event.Stage == model.StageVerify {
			foundVerify = true
		}
	}
	if !foundVerify {
		t.Fatalf("committed metadata warning skipped verification: %+v", journal.Audits())
	}
}

func TestExecuteStopsBeforeMutationWhenAuditCannotPersist(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	wantErr := errors.New("journal unavailable")
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, failingJournal{err: wantErr})
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if !errors.Is(err, wantErr) {
		t.Fatalf("execute error = %v, want journal failure", err)
	}
	if execution.Status != model.OperationFailed {
		t.Fatalf("execution status = %q, want failed", execution.Status)
	}
	if len(trace) != 1 || trace[0] != "gate:discover" {
		t.Fatalf("workflow continued after audit failure: %v", trace)
	}
}

type topologyReaderStub struct {
	snapshot model.TopologySnapshot
	found    bool
}

func (reader topologyReaderStub) TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool) {
	return reader.snapshot, reader.found
}

func TestTopologyDiscoveryRequiresCurrentClusterObservation(t *testing.T) {
	clusterID := model.NewResourceID()
	operation := model.Operation{ClusterID: clusterID}
	for _, test := range []struct {
		name    string
		reader  topologyReaderStub
		wantErr bool
	}{
		{name: "missing snapshot", reader: topologyReaderStub{}, wantErr: true},
		{name: "zero observation", reader: topologyReaderStub{found: true, snapshot: model.TopologySnapshot{ClusterID: clusterID}}, wantErr: true},
		{name: "current observation", reader: topologyReaderStub{found: true, snapshot: model.TopologySnapshot{ClusterID: clusterID, ObservedAt: time.Now().UTC()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (TopologyDiscovery{Reader: test.reader}).CaptureObservation(context.Background(), operation)
			if (err != nil) != test.wantErr {
				t.Fatalf("CaptureObservation() error = %v, wantErr=%t", err, test.wantErr)
			}
		})
	}
	gate := TopologyDiscovery{Reader: topologyReaderStub{found: true, snapshot: model.TopologySnapshot{ClusterID: clusterID, ObservedAt: workflowTestObservation}}}
	token, err := gate.CaptureObservation(context.Background(), operation)
	if err != nil || token.ClusterID != clusterID || !token.ObservedAt.Equal(workflowTestObservation) {
		t.Fatalf("captured token = %+v err=%v", token, err)
	}
	gate.Reader = topologyReaderStub{found: true, snapshot: model.TopologySnapshot{ClusterID: clusterID, ObservedAt: workflowTestObservation.Add(time.Second)}}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err == nil {
		t.Fatal("changed topology observation passed revalidation")
	}
}

type changingDiscoveryGate struct {
	trace *[]string
	err   error
}

func (gate changingDiscoveryGate) CaptureObservation(_ context.Context, operation model.Operation) (ObservationToken, error) {
	*gate.trace = append(*gate.trace, "gate:discover")
	return ObservationToken{ClusterID: operation.ClusterID, ObservedAt: workflowTestObservation}, nil
}

func (gate changingDiscoveryGate) RevalidateObservation(context.Context, model.Operation, ObservationToken) error {
	*gate.trace = append(*gate.trace, "gate:revalidate")
	return gate.err
}

func TestExecuteBlocksWhenPinnedObservationChangesBeforeMutation(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	wantErr := errors.New("topology observation changed")
	journal := NewMemoryJournal()
	service := New(registry, changingDiscoveryGate{trace: &trace, err: wantErr}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover,
	}}, "approved")
	if !errors.Is(err, wantErr) || execution.Status != model.OperationBlocked {
		t.Fatalf("changed observation execution=%+v err=%v", execution, err)
	}
	for _, entry := range trace {
		if entry == "gate:approval" || entry == "adapter:execute" || entry == "adapter:verify" {
			t.Fatalf("changed observation reached unsafe stage: %v", trace)
		}
	}
}

func TestExecuteRunsGuardedWorkflowAndProducesAuditReport(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	clusterID := model.NewResourceID()
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execution.Status != model.OperationSucceeded {
		t.Fatalf("unexpected execution: %+v", execution)
	}
	want := []string{"gate:discover", "adapter:precheck", "adapter:plan", "gate:safety", "gate:lock", "gate:revalidate", "gate:approval", "adapter:execute", "adapter:verify", "gate:release"}
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
	discoverIndex, precheckIndex := -1, -1
	for index, event := range journal.Audits() {
		if event.Stage == model.StageDiscover && discoverIndex < 0 {
			discoverIndex = index
		}
		if event.Stage == model.StagePrecheck && precheckIndex < 0 {
			precheckIndex = index
		}
		if event.Stage == model.StageSafetyGuard && safetyIndex < 0 {
			safetyIndex = index
		}
		if event.Stage == model.StageLock && lockIndex < 0 {
			lockIndex = index
		}
	}
	observationLabel := string(clusterID) + "@" + workflowTestObservation.Format(time.RFC3339Nano)
	if len(journal.Audits()) == 0 || !strings.Contains(journal.Audits()[0].Message, observationLabel) {
		t.Fatalf("discover audit does not identify the pinned observation: %+v", journal.Audits())
	}
	if safetyIndex < 0 || lockIndex < 0 || safetyIndex >= lockIndex {
		t.Fatalf("safety guard must be an explicit stage before lock: %+v", journal.Audits())
	}
	if discoverIndex < 0 || precheckIndex < 0 || discoverIndex >= precheckIndex {
		t.Fatalf("discover must be an explicit stage before precheck: %+v", journal.Audits())
	}
}

func TestExecuteBlocksUnsupportedAdapterBeforeSafetyOrLock(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, false)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
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

func TestExecuteRequiresCompleteMutationCapabilitySetBeforeDiscovery(t *testing.T) {
	for _, missing := range []adapter.Capability{
		adapter.CapabilityPrecheck,
		adapter.CapabilityPlan,
		adapter.CapabilityExecute,
		adapter.CapabilityVerify,
	} {
		t.Run(string(missing), func(t *testing.T) {
			trace := []string{}
			registry := adapter.NewRegistry()
			candidate := &capabilityMismatchAdapter{recordingAdapter: newRecordingAdapter(&trace, true), missing: missing}
			if err := registry.Register(candidate); err != nil {
				t.Fatalf("register: %v", err)
			}
			journal := NewMemoryJournal()
			service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
			execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: model.Operation{
				ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineMySQL, Kind: model.OperationFailover,
			}}, "approved")
			if !errors.Is(err, adapter.ErrUnsupported) || execution.Status != model.OperationUnsupported {
				t.Fatalf("missing %s execution=%+v err=%v", missing, execution, err)
			}
			if len(trace) != 0 {
				t.Fatalf("missing %s reached discovery, gates, or adapter methods: %v", missing, trace)
			}
			if len(journal.Audits()) == 0 || len(journal.Reports()) != 1 {
				t.Fatalf("missing %s was not audited and reported", missing)
			}
		})
	}
}
