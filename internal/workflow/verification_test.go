package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type verificationContractAdapter struct {
	*recordingAdapter
	verification model.Verification
}

func (candidate *verificationContractAdapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	return candidate.verification, nil
}

func TestVerificationContractAcrossExecutionPaths(t *testing.T) {
	for _, engine := range model.SupportedEngines() {
		for _, mode := range []string{"legacy", "durable", "manual"} {
			for _, test := range []struct {
				name   string
				passed bool
				checks []model.Check
				want   bool
			}{
				{"pass", true, []model.Check{{Name: "verified", Status: model.CheckPass}}, true},
				{"warning", true, []model.Check{{Name: "verified", Status: model.CheckPass}, {Name: "advisory", Status: model.CheckWarn}}, true},
				{"failure", true, []model.Check{{Name: "writer_unique", Status: model.CheckFail}}, false},
				{"unknown", true, []model.Check{{Name: "writer_unique", Status: "future-status"}}, false},
				{"missing status", true, []model.Check{{Name: "writer_unique"}}, false},
				{"no checks", true, nil, false},
				{"explicit false", false, []model.Check{{Name: "verified", Status: model.CheckPass}}, false},
			} {
				t.Run(string(engine)+"/"+mode+"/"+test.name, func(t *testing.T) {
					verification := model.Verification{Passed: test.passed, Checks: test.checks}
					request, resolved := durableRequestFixture()
					request.Operation.Engine = engine
					resolved.Cluster.Engine, resolved.Primary.Engine, resolved.Target.Engine = engine, engine, engine
					if mode == "legacy" {
						trace := []string{}
						base := newRecordingAdapter(&trace, true)
						base.UnsupportedAdapter = adapter.NewUnsupported(engine)
						registry := adapter.NewRegistry()
						if err := registry.Register(&verificationContractAdapter{base, verification}); err != nil {
							t.Fatal(err)
						}
						gate := recordingGate{&trace}
						journal := NewMemoryJournal()
						service := New(registry, gate, gate, gate, gate, journal)
						execution, err := service.Execute(context.Background(), request, "approved")
						if (err == nil) != test.want || (execution.Status == model.OperationSucceeded) != test.want {
							t.Fatalf("execution=%s err=%v want success=%t", execution.Status, err, test.want)
						}
						if !test.want && execution.Status != model.OperationIndeterminate {
							t.Fatalf("unverified mutation must remain indeterminate: %s", execution.Status)
						}
						for _, report := range journal.Reports() {
							if !test.want && report.Status == model.OperationSucceeded {
								t.Fatal("unverified mutation received a successful report")
							}
						}
						return
					}
					path := filepath.Join(t.TempDir(), "metadata.json")
					repository, err := store.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					candidate := newDurableAdapter()
					candidate.UnsupportedAdapter = adapter.NewUnsupported(engine)
					candidate.verificationResult = &verification
					service := newDurableWorkflowService(t, repository, candidate, request, resolved)
					if mode == "manual" {
						candidate.verificationResult = &model.Verification{Passed: false, Checks: []model.Check{{Name: "unverified", Status: model.CheckFail}}}
						execution, err := service.Execute(context.Background(), request, "approved")
						if err == nil || execution.Status != model.OperationIndeterminate {
							t.Fatalf("manual fixture not indeterminate: %s %v", execution.Status, err)
						}
						candidate.verificationResult = &verification
						got, err := service.Verify(context.Background(), request)
						if got.Passed != test.want || (test.want && err != nil) {
							t.Fatalf("manual verification=%+v err=%v want success=%t", got, err, test.want)
						}
					} else {
						execution, err := service.Execute(context.Background(), request, "approved")
						if (err == nil) != test.want || (execution.Status == model.OperationSucceeded) != test.want {
							t.Fatalf("durable execution=%s err=%v want success=%t", execution.Status, err, test.want)
						}
					}
					reopened, err := store.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					record, found := reopened.OperationByIdempotencyKey(request.IdempotencyKey)
					if !found || (record.Status == model.OperationSucceeded) != test.want || record.Verification.Passed != test.want {
						t.Fatalf("persisted verification inconsistent: found=%t status=%s verification=%+v", found, record.Status, record.Verification)
					}
					if len(record.Verification.Checks) != len(test.checks) {
						t.Fatal("verification evidence was discarded")
					}
					_, _ = service.Execute(context.Background(), request, "approved")
					if candidate.executeCalls != 1 {
						t.Fatalf("operation was submitted again: %d", candidate.executeCalls)
					}
				})
			}
		}
	}
}

type legacyVerificationRepository struct {
	*store.Repository
	record model.OperationRecord
}

func (repository *legacyVerificationRepository) OperationByIdempotencyKey(string) (model.OperationRecord, bool) {
	return repository.record, true
}

func (repository *legacyVerificationRepository) CreateOperation(model.OperationRecord) (model.OperationRecord, bool, error) {
	return repository.record, true, nil
}

func TestLegacySuccessfulRecordCannotBypassVerificationContract(t *testing.T) {
	for index, checks := range [][]model.Check{nil, {{Status: model.CheckFail}}, {{Status: "unknown"}}, {{Status: model.CheckPass}}} {
		t.Run(fmt.Sprintf("evidence-%d", index), func(t *testing.T) {
			request, resolved := durableRequestFixture()
			request.Operation.ResourceID = model.NewResourceID()
			repository := store.NewMemory()
			candidate := newDurableAdapter()
			service := newDurableWorkflowService(t, repository, candidate, request, resolved)
			record := model.OperationRecord{ResourceMeta: model.ResourceMeta{ResourceID: request.Operation.ResourceID}, Operation: request.Operation, Status: model.OperationSucceeded,
				Execution: model.Execution{Status: model.OperationSucceeded}, Verification: model.Verification{OperationID: request.Operation.ResourceID, Passed: true, Checks: checks}}
			legacy := &legacyVerificationRepository{Repository: repository, record: record}
			service.operations = legacy
			want := record.Verification.Successful()
			execution, err := service.Execute(context.Background(), request, "approved")
			if (err == nil) != want || (execution.Status == model.OperationSucceeded) != want {
				t.Fatalf("cached execution=%+v err=%v", execution, err)
			}
			verification, err := service.Verify(context.Background(), request)
			if (err == nil) != want || verification.Passed != want {
				t.Fatalf("cached verification=%+v err=%v", verification, err)
			}
			if candidate.executeCalls != 0 || candidate.verifyCalls != 0 || !reflect.DeepEqual(record, legacy.record) {
				t.Fatal("historical evidence was rewritten or operation was re-executed")
			}
		})
	}
}

func TestVerificationRejectsForeignOperationAndProbeError(t *testing.T) {
	request, _ := durableRequestFixture()
	trace := []string{}
	candidate := &verificationContractAdapter{newRecordingAdapter(&trace, true), model.Verification{OperationID: model.NewResourceID(), Passed: true, Checks: []model.Check{{Status: model.CheckPass}}}}
	verification, err := verifyOperation(context.Background(), candidate, request)
	if err == nil || verification.Passed {
		t.Fatalf("foreign evidence accepted: %+v %v", verification, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	verification, err = verifyOperation(cancelled, newDurableAdapter(), request)
	if !errors.Is(err, context.Canceled) || verification.Passed {
		t.Fatalf("failed probe accepted: %+v %v", verification, err)
	}
}

func TestAutomaticResumeRejectsContradictoryVerification(t *testing.T) {
	for index, checks := range [][]model.Check{nil, {{Status: model.CheckFail}}, {{Status: "unknown"}}} {
		t.Run(fmt.Sprintf("invalid-evidence-%d", index), func(t *testing.T) {
			request, resolved := durableRequestFixture()
			request.Operation.Kind = model.OperationFailover
			resolved.Primary.Health.State = model.HealthUnhealthy
			request.IdempotencyKey = "automatic-failover:" + string(request.Operation.ClusterID) + ":" + string(resolved.Primary.ResourceID) + ":1786471200000000000:1"
			repository := store.NewMemory()
			candidate := newDurableAdapter()
			candidate.executeError = durableCommittedFailure{}
			candidate.verificationResult = &model.Verification{Checks: []model.Check{{Status: model.CheckFail}}}
			service := newDurableWorkflowService(t, repository, candidate, request, resolved)
			if execution, err := service.ExecuteAutomatic(context.Background(), request, "incident-1"); err == nil || execution.Status != model.OperationIndeterminate {
				t.Fatalf("initial promotion=%+v err=%v", execution, err)
			}
			candidate.executeError = nil
			candidate.verificationResult = &model.Verification{Passed: true, Checks: checks}
			execution, err := service.ExecuteAutomatic(context.Background(), request, "incident-1")
			if err == nil || execution.Status != model.OperationIndeterminate {
				t.Fatalf("unverified resume=%+v err=%v", execution, err)
			}
			record, _ := repository.OperationByIdempotencyKey(request.IdempotencyKey)
			if record.Status != model.OperationIndeterminate || record.Verification.Passed || candidate.executeCalls != 2 || candidate.verifyCalls != 2 {
				t.Fatalf("unsafe resume result: %+v execute=%d verify=%d", record, candidate.executeCalls, candidate.verifyCalls)
			}
		})
	}
}
