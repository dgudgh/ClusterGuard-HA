package workflow

import (
	"context"
	"errors"
	"maps"
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

type testLockLeaseLost struct{}

func (testLockLeaseLost) Error() string        { return "test operation lock lease lost" }
func (testLockLeaseLost) FailureClass() string { return "lock_lease_lost" }

type leaseLosingLock struct {
	executeStarted <-chan struct{}
}

func (lock leaseLosingLock) Acquire(ctx context.Context, _ model.Operation) (context.Context, func(), error) {
	leaseCtx, cancel := context.WithCancelCause(ctx)
	go func() {
		<-lock.executeStarted
		cancel(testLockLeaseLost{})
	}()
	return leaseCtx, func() { cancel(context.Canceled) }, nil
}

type commitCancelingLock struct {
	cancel context.CancelCauseFunc
}

func (lock *commitCancelingLock) Acquire(ctx context.Context, _ model.Operation) (context.Context, func(), error) {
	leaseCtx, cancel := context.WithCancelCause(ctx)
	lock.cancel = cancel
	return leaseCtx, func() { cancel(context.Canceled) }, nil
}

type verifyCancelingAdapter struct {
	*recordingAdapter
	lock *commitCancelingLock
}

func (candidate *verifyCancelingAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	*candidate.trace = append(*candidate.trace, "adapter:verify")
	candidate.lock.cancel(testLockLeaseLost{})
	return model.Verification{Passed: true, Checks: []model.Check{{Name: "verified", Status: model.CheckPass}}}, nil
}

type failingVerificationAdapter struct {
	*recordingAdapter
}

func (candidate *failingVerificationAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	*candidate.trace = append(*candidate.trace, "adapter:verify")
	return model.Verification{Passed: false, Checks: []model.Check{{Name: "verified", Status: model.CheckFail}}}, nil
}

type leaseAwareAdapter struct {
	*recordingAdapter
	executeStarted chan struct{}
}

func (candidate *leaseAwareAdapter) Execute(ctx context.Context, _ adapter.OperationRequest) (model.Execution, error) {
	*candidate.trace = append(*candidate.trace, "adapter:execute")
	close(candidate.executeStarted)
	<-ctx.Done()
	return model.Execution{Status: model.OperationRunning}, ctx.Err()
}

func (gate recordingGate) CaptureObservation(_ context.Context, operation model.Operation) (ObservationToken, error) {
	*gate.trace = append(*gate.trace, "gate:discover")
	return ObservationToken{ClusterID: operation.ClusterID, ObservedAt: workflowTestObservation, Digest: "sha256:workflow-test"}, nil
}

func (gate recordingGate) RevalidateObservation(context.Context, model.Operation, ObservationToken) error {
	*gate.trace = append(*gate.trace, "gate:revalidate")
	return nil
}

func (gate recordingGate) Evaluate(context.Context, model.Operation) error {
	*gate.trace = append(*gate.trace, "gate:safety")
	return nil
}
func (gate recordingGate) Acquire(ctx context.Context, _ model.Operation) (context.Context, func(), error) {
	*gate.trace = append(*gate.trace, "gate:lock")
	return ctx, func() { *gate.trace = append(*gate.trace, "gate:release") }, nil
}
func (gate recordingGate) Consume(_ context.Context, operation model.OperationRecord, _ string) (model.ResourceID, model.OperationRecord, error) {
	*gate.trace = append(*gate.trace, "gate:approval")
	operation.Stage = model.StageApprove
	return model.NewResourceID(), operation, nil
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

type finalReportFailingJournal struct {
	*MemoryJournal
	calls int
}

func (journal *finalReportFailingJournal) RecordReport(report model.Report) error {
	journal.calls++
	if journal.calls == 2 {
		return errors.New("terminal report write failed before commit")
	}
	return journal.MemoryJournal.RecordReport(report)
}

type finalReportPostCommitJournal struct {
	*MemoryJournal
	calls int
}

func TestAutomaticExecutionUsesInternalAuthorizationWithoutHumanGrant(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, NewMemoryJournal())
	request := adapter.OperationRequest{
		Operation: model.Operation{
			ClusterID:   model.NewResourceID(),
			Engine:      model.EngineMySQL,
			Kind:        model.OperationFailover,
			RequestedBy: "untrusted-caller",
		},
		TargetID: model.NewResourceID(),
	}

	execution, err := service.ExecuteAutomatic(context.Background(), request, "incident-20260716")
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("automatic execution=%+v err=%v", execution, err)
	}
	for _, entry := range trace {
		if entry == "gate:approval" {
			t.Fatalf("automatic recovery consumed a human grant: %v", trace)
		}
	}
	if _, err := service.ExecuteAutomatic(context.Background(), adapter.OperationRequest{
		Operation: model.Operation{ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover},
		TargetID:  model.NewResourceID(),
	}, "incident-20260716"); err == nil {
		t.Fatal("automatic switchover was accepted")
	}
	if _, err := service.ExecuteAutomatic(context.Background(), request, ""); err == nil {
		t.Fatal("automatic failover without incident identity was accepted")
	}
}

func (journal *finalReportPostCommitJournal) RecordReport(report model.Report) error {
	journal.calls++
	if err := journal.MemoryJournal.RecordReport(report); err != nil {
		return err
	}
	if journal.calls == 2 {
		return committedWarning{message: "terminal report committed with durability warning"}
	}
	return nil
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

func TestInitialReportPostCommitWarningPreservesDurableIndeterminateFallback(t *testing.T) {
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

func TestFinalReportWriteFailurePreservesDurableIndeterminateFallback(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := &finalReportFailingJournal{MemoryJournal: NewMemoryJournal()}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("terminal report failure execution=%+v err=%v", execution, err)
	}
	reports := journal.Reports()
	if journal.calls != 2 || len(reports) != 1 || !strings.Contains(reports[0].Summary, "journal persistence failed") {
		t.Fatalf("durable fallback was not preserved: calls=%d reports=%+v", journal.calls, reports)
	}
}

func TestFinalReportPostCommitWarningUsesDurableFallbackAndKeepsVerifiedOutcome(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := &finalReportPostCommitJournal{MemoryJournal: NewMemoryJournal()}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	operation := model.Operation{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba"}
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if err != nil || execution.Status != model.OperationSucceeded {
		t.Fatalf("recoverable terminal warning execution=%+v err=%v", execution, err)
	}
	reports := journal.Reports()
	if journal.calls != 2 || len(reports) != 1 || reports[0].Status != model.OperationSucceeded || reports[0].Summary != "executed" {
		t.Fatalf("terminal report did not match verified outcome: calls=%d reports=%+v", journal.calls, reports)
	}
}

func TestUnsupportedReportWarningDoesNotClaimDatabaseOperationCommitted(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, false)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := postCommitReportJournal{MemoryJournal: NewMemoryJournal()}
	service := New(registry, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, recordingGate{&trace}, journal)
	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Engine: model.EngineMySQL, Kind: model.OperationFailover,
	}}, "approved")
	if !errors.Is(err, ErrJournalPersistence) || execution.Status != model.OperationFailed {
		t.Fatalf("unsupported report warning execution=%+v err=%v", execution, err)
	}
	reports := journal.Reports()
	if len(reports) != 1 || strings.Contains(reports[0].Summary, "operation committed") || !strings.Contains(reports[0].Summary, "journal persistence failed") {
		t.Fatalf("unsupported report falsely claims a database commit: %+v", reports)
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

func TestMetadataCommitIsIndeterminateWhenLockLeaseIsLostDuringCommit(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	gate := recordingGate{trace: &trace}
	lock := &commitCancelingLock{}
	service := New(registry, gate, gate, lock, gate, journal)
	request := adapter.MetadataRequest{ClusterID: model.NewResourceID(), Instance: model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	}}
	committed := false
	execution, err := service.ExecuteMetadata(context.Background(), model.Operation{RequestedBy: "dba"}, request, "approved", func() error {
		committed = true
		lock.cancel(testLockLeaseLost{})
		return nil
	})
	var leaseLost testLockLeaseLost
	if !committed || !errors.As(err, &leaseLost) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("lease-lost metadata execution=%+v committed=%t err=%v", execution, committed, err)
	}
	if !strings.Contains(execution.Message, "lock lease") {
		t.Fatalf("indeterminate metadata message=%q", execution.Message)
	}
}

func TestExecuteIsIndeterminateWhenLockLeaseIsLostDuringVerification(t *testing.T) {
	trace := []string{}
	lock := &commitCancelingLock{}
	registry := adapter.NewRegistry()
	candidate := &verifyCancelingAdapter{recordingAdapter: newRecordingAdapter(&trace, true), lock: lock}
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	gate := recordingGate{trace: &trace}
	service := New(registry, gate, gate, lock, gate, journal)
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
		Engine:       model.EngineMySQL,
		Kind:         model.OperationSwitchover,
		RequestedBy:  "dba",
	}

	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	var leaseLost testLockLeaseLost
	if !errors.As(err, &leaseLost) || execution.Status != model.OperationIndeterminate {
		t.Fatalf("lease-lost verification execution=%+v err=%v", execution, err)
	}
	if !strings.Contains(execution.Message, "lock lease") {
		t.Fatalf("indeterminate verification message=%q", execution.Message)
	}
}

func TestExecuteReturnsIndeterminateErrorWhenPostCommitVerificationFails(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	candidate := &failingVerificationAdapter{recordingAdapter: newRecordingAdapter(&trace, true)}
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register: %v", err)
	}
	gate := recordingGate{trace: &trace}
	service := New(registry, gate, gate, gate, gate, NewMemoryJournal())
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(),
		Engine:       model.EngineMySQL,
		Kind:         model.OperationSwitchover,
		RequestedBy:  "dba",
	}

	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if err == nil || execution.Status != model.OperationIndeterminate {
		t.Fatalf("failed verification execution=%+v err=%v", execution, err)
	}
	if !strings.Contains(execution.Message, "verification failed") {
		t.Fatalf("failed verification message=%q", execution.Message)
	}
}

func TestPlatformRoleMetadataAuthorizationPreservesGatesWithoutExternalApproval(t *testing.T) {
	trace := []string{}
	registry := adapter.NewRegistry()
	if err := registry.Register(newRecordingAdapter(&trace, true)); err != nil {
		t.Fatalf("register: %v", err)
	}
	journal := NewMemoryJournal()
	gate := recordingGate{trace: &trace}
	service := New(registry, gate, gate, gate, gate, journal)
	request := adapter.MetadataRequest{ClusterID: model.NewResourceID(), Instance: model.DatabaseInstance{
		Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
	}}
	committed := false
	execution, err := service.ExecuteMetadataAuthorized(
		context.Background(),
		model.Operation{RequestedBy: "caller-supplied"},
		request,
		"admin",
		func() error {
			committed = true
			return nil
		},
	)
	if err != nil || !committed || execution.Status != model.OperationSucceeded {
		t.Fatalf("authorized metadata execution=%+v committed=%t err=%v", execution, committed, err)
	}
	for _, entry := range trace {
		if entry == "gate:approval" {
			t.Fatalf("platform-authorized metadata called external approval: %+v", trace)
		}
	}
	foundApprovalAudit := false
	for _, event := range journal.Audits() {
		if event.Stage == model.StageApprove {
			foundApprovalAudit = event.Actor == "admin" && strings.Contains(event.Message, "platform")
		}
	}
	if !foundApprovalAudit {
		t.Fatalf("platform metadata authorization audit missing: %+v", journal.Audits())
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
	instanceID := model.NewResourceID()
	snapshot := model.TopologySnapshot{
		ClusterID: clusterID,
		Instances: []model.DatabaseInstance{{
			ResourceMeta: model.ResourceMeta{ResourceID: instanceID, MetadataRevision: 7},
			ClusterID:    clusterID, Engine: model.EngineMySQL, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306,
			Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy, ObservedAt: workflowTestObservation},
			Replication:    model.ReplicationStatus{SourceIdentity: model.EngineIdentity{"server_uuid": "source-a"}, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning},
			EngineMetadata: map[string]string{"version": "8.0.44", "gtid_executed": "source-a:1-10"},
		}},
		Anomalies:  []model.MetadataAnomaly{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1}, ClusterID: clusterID, Engine: model.EngineMySQL, Kind: "test_warning", Severity: "warning", Message: "stable warning"}},
		ObservedAt: workflowTestObservation,
	}
	gate := TopologyDiscovery{Reader: topologyReaderStub{found: true, snapshot: snapshot}}
	token, err := gate.CaptureObservation(context.Background(), operation)
	if err != nil || token.ClusterID != clusterID || !token.ObservedAt.Equal(workflowTestObservation) || token.Digest == "" || token.Snapshot.ClusterID != clusterID {
		t.Fatalf("captured token = %+v err=%v", token, err)
	}
	refreshed := snapshot
	refreshed.ObservedAt = workflowTestObservation.Add(time.Second)
	refreshed.Instances = append([]model.DatabaseInstance{}, snapshot.Instances...)
	refreshed.Instances[0].MetadataRevision++
	refreshed.Instances[0].Health.ObservedAt = refreshed.ObservedAt
	refreshed.Instances[0].EngineMetadata = map[string]string{"version": "8.0.44", "gtid_executed": "source-a:1-20"}
	refreshed.Anomalies = append([]model.MetadataAnomaly{}, snapshot.Anomalies...)
	refreshed.Anomalies[0].ResourceID = model.NewResourceID()
	refreshed.Anomalies[0].MetadataRevision++
	gate.Reader = topologyReaderStub{found: true, snapshot: refreshed}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err != nil {
		t.Fatalf("equivalent topology heartbeat failed revalidation: %v", err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*model.DatabaseInstance)
	}{
		{name: "role", mutate: func(value *model.DatabaseInstance) { value.Role = model.RoleReplica }},
		{name: "endpoint", mutate: func(value *model.DatabaseInstance) { value.IPAddress = "192.0.2.11" }},
		{name: "replication source", mutate: func(value *model.DatabaseInstance) {
			value.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "source-b"}
		}},
	} {
		t.Run("rejects "+change.name+" change", func(t *testing.T) {
			changed := refreshed
			changed.Instances = append([]model.DatabaseInstance{}, refreshed.Instances...)
			change.mutate(&changed.Instances[0])
			gate.Reader = topologyReaderStub{found: true, snapshot: changed}
			if err := gate.RevalidateObservation(context.Background(), operation, token); err == nil {
				t.Fatalf("changed topology %s passed revalidation", change.name)
			}
		})
	}
}

func TestTopologyDiscoveryIgnoresVolatilePostgreSQLWALPositions(t *testing.T) {
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	operation := model.Operation{ClusterID: clusterID}
	snapshot := model.TopologySnapshot{
		ClusterID: clusterID,
		Instances: []model.DatabaseInstance{{
			ResourceMeta: model.ResourceMeta{ResourceID: instanceID, MetadataRevision: 7},
			ClusterID:    clusterID,
			Engine:       model.EnginePostgreSQL,
			Hostname:     "postgres-a",
			IPAddress:    "192.0.2.20",
			Port:         5432,
			Role:         model.RolePrimary,
			Health:       model.Health{State: model.HealthHealthy, Replication: "primary", ObservedAt: workflowTestObservation},
			EngineMetadata: map[string]string{
				"version": "16.14", "timeline_id": "1", "in_recovery": "false",
				"current_lsn": "0/405D728", "receive_lsn": "", "replay_lsn": "",
			},
		}},
		ObservedAt: workflowTestObservation,
	}
	gate := TopologyDiscovery{Reader: topologyReaderStub{found: true, snapshot: snapshot}}
	token, err := gate.CaptureObservation(context.Background(), operation)
	if err != nil {
		t.Fatalf("capture PostgreSQL observation: %v", err)
	}

	refreshed := snapshot
	refreshed.ObservedAt = snapshot.ObservedAt.Add(time.Second)
	refreshed.Instances = append([]model.DatabaseInstance{}, snapshot.Instances...)
	refreshed.Instances[0].MetadataRevision++
	refreshed.Instances[0].Health.ObservedAt = refreshed.ObservedAt
	refreshed.Instances[0].EngineMetadata = map[string]string{
		"version": "16.14", "timeline_id": "1", "in_recovery": "false",
		"current_lsn": "0/405E000", "receive_lsn": "0/405E000", "replay_lsn": "0/405DFF8",
	}
	gate.Reader = topologyReaderStub{found: true, snapshot: refreshed}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err != nil {
		t.Fatalf("PostgreSQL WAL progress invalidated equivalent topology: %v", err)
	}

	changed := refreshed
	changed.Instances = append([]model.DatabaseInstance{}, refreshed.Instances...)
	changed.Instances[0].EngineMetadata = map[string]string{
		"version": "16.14", "timeline_id": "2", "in_recovery": "false",
		"current_lsn": "0/405E000", "receive_lsn": "0/405E000", "replay_lsn": "0/405DFF8",
	}
	gate.Reader = topologyReaderStub{found: true, snapshot: changed}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err == nil {
		t.Fatal("PostgreSQL timeline change passed observation revalidation")
	}
}

func TestTopologyDiscoveryIgnoresVolatileOracleBrokerLag(t *testing.T) {
	clusterID := model.NewResourceID()
	instanceID := model.NewResourceID()
	operation := model.Operation{ClusterID: clusterID}
	snapshot := model.TopologySnapshot{
		ClusterID: clusterID,
		Instances: []model.DatabaseInstance{{
			ResourceMeta: model.ResourceMeta{ResourceID: instanceID, MetadataRevision: 7},
			ClusterID:    clusterID,
			Engine:       model.EngineOracle,
			EngineIdentity: model.EngineIdentity{
				"dbid": "3248481464", "db_unique_name": "mesdb",
			},
			Hostname: "mesdb", IPAddress: "192.0.2.20", Port: 1521,
			Role: model.RoleStandby,
			Health: model.Health{
				State: model.HealthHealthy, Replication: "success", ObservedAt: workflowTestObservation,
			},
			Replication: model.ReplicationStatus{
				IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning,
			},
			PromotionEligible: true,
			EngineMetadata: map[string]string{
				"data_guard_broker": "enabled", "database_role": "PHYSICAL STANDBY",
				"database_status": "SUCCESS", "configuration_status": "SUCCESS",
				"transport_lag_seconds": "0", "apply_lag_seconds": "0",
			},
		}},
		ObservedAt: workflowTestObservation,
	}
	gate := TopologyDiscovery{Reader: topologyReaderStub{found: true, snapshot: snapshot}}
	token, err := gate.CaptureObservation(context.Background(), operation)
	if err != nil {
		t.Fatalf("capture Oracle observation: %v", err)
	}

	refreshed := snapshot
	refreshed.ObservedAt = snapshot.ObservedAt.Add(time.Second)
	refreshed.Instances = append([]model.DatabaseInstance{}, snapshot.Instances...)
	refreshed.Instances[0].MetadataRevision++
	refreshed.Instances[0].Health.ObservedAt = refreshed.ObservedAt
	refreshed.Instances[0].PromotionEligible = false
	refreshed.Instances[0].EngineMetadata = map[string]string{
		"data_guard_broker": "enabled", "database_role": "PHYSICAL STANDBY",
		"database_status": "SUCCESS", "configuration_status": "SUCCESS",
		"transport_lag_seconds": "1", "apply_lag_seconds": "1",
	}
	gate.Reader = topologyReaderStub{found: true, snapshot: refreshed}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err != nil {
		t.Fatalf("Oracle Broker lag heartbeat invalidated equivalent topology: %v", err)
	}

	changed := refreshed
	changed.Instances = append([]model.DatabaseInstance{}, refreshed.Instances...)
	changed.Instances[0].Role = model.RolePrimary
	changed.Instances[0].EngineMetadata = maps.Clone(refreshed.Instances[0].EngineMetadata)
	changed.Instances[0].EngineMetadata["database_role"] = "PRIMARY"
	gate.Reader = topologyReaderStub{found: true, snapshot: changed}
	if err := gate.RevalidateObservation(context.Background(), operation, token); err == nil {
		t.Fatal("Oracle role change passed observation revalidation")
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
	observationLabel := string(clusterID) + "@sha256:workflow-test"
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

func TestExecuteMarksOperationIndeterminateWhenLockLeaseIsLostDuringMutation(t *testing.T) {
	trace := []string{}
	started := make(chan struct{})
	candidate := &leaseAwareAdapter{recordingAdapter: newRecordingAdapter(&trace, true), executeStarted: started}
	registry := adapter.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register: %v", err)
	}
	gate := recordingGate{trace: &trace}
	service := New(registry, gate, gate, leaseLosingLock{executeStarted: started}, gate, NewMemoryJournal())
	operation := model.Operation{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    model.NewResourceID(), Engine: model.EngineMySQL,
		Kind: model.OperationSwitchover, RequestedBy: "dba",
	}

	execution, err := service.Execute(context.Background(), adapter.OperationRequest{Operation: operation}, "approved")
	if err == nil || execution.Status != model.OperationIndeterminate || !strings.Contains(execution.Message, "lock lease was lost") {
		t.Fatalf("lease-loss execution=%+v err=%v", execution, err)
	}
	for _, entry := range trace {
		if entry == "adapter:verify" {
			t.Fatalf("legacy workflow reported normal verification after losing the mutation lock: %v", trace)
		}
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
