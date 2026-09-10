package disaster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type recoveryHarness struct {
	task     model.RecoveryTask
	evidence []model.RecoveryEvidence
	calls    []string
	fail     string
}

func (h *recoveryHarness) step(name string) error {
	h.calls = append(h.calls, name)
	if h.fail == name {
		return errors.New("injected " + name)
	}
	return nil
}
func (h *recoveryHarness) RequireMutationAuthority(context.Context) error { return nil }
func (h *recoveryHarness) Check(context.Context) error                    { return nil }
func (h *recoveryHarness) Acquire(ctx context.Context, _ model.Operation) (context.Context, func(), error) {
	return adapter.WithOperationLeaseID(ctx, model.NewResourceID()), func() {}, nil
}
func (h *recoveryHarness) RecoveryTask(model.ResourceID) (model.RecoveryTask, bool) {
	return h.task, true
}
func (h *recoveryHarness) BeginRecovery(_ context.Context, _ model.ResourceID, _ uint64, lease model.ResourceID) (model.RecoveryTask, error) {
	h.task.LeaseID = lease
	h.task.Stage = model.RecoveryFencing
	h.task.MetadataRevision++
	return h.task, h.step("begin")
}
func (h *recoveryHarness) AdvanceRecovery(_ context.Context, task model.RecoveryTask, event model.RecoveryEvent) (model.RecoveryTask, error) {
	task.Stage = event.Stage
	task.MetadataRevision++
	h.task = task
	return task, nil
}
func (h *recoveryHarness) CommitRecovery(_ context.Context, task model.RecoveryTask, _ model.TopologySnapshot) (model.RecoveryTask, error) {
	if err := h.step("commit"); err != nil {
		return model.RecoveryTask{}, err
	}
	task.Stage = model.RecoveryCommitted
	task.CommittedAt = time.Now()
	task.MetadataRevision++
	h.task = task
	return task, nil
}
func (h *recoveryHarness) CompleteRecovery(_ context.Context, task model.RecoveryTask) (model.RecoveryTask, error) {
	if err := h.step("complete"); err != nil {
		return model.RecoveryTask{}, err
	}
	task.Stage = model.RecoverySucceeded
	task.MetadataRevision++
	h.task = task
	return task, nil
}
func (h *recoveryHarness) Preflight(context.Context, model.RecoveryTask) error {
	return h.step("preflight")
}
func (h *recoveryHarness) Fence(_ context.Context, task model.RecoveryTask, m model.DatabaseInstance) error {
	if task.PrimaryID == m.ResourceID {
		return h.step("refence")
	}
	return h.step("fence")
}
func (h *recoveryHarness) Inspect(_ context.Context, _ model.RecoveryTask, m model.DatabaseInstance) (model.RecoveryEvidence, error) {
	if err := h.step("inspect"); err != nil {
		return model.RecoveryEvidence{}, err
	}
	for _, e := range h.evidence {
		if e.InstanceID == m.ResourceID {
			return e, nil
		}
	}
	return model.RecoveryEvidence{}, errors.New("missing")
}
func (h *recoveryHarness) Compare(context.Context, model.RecoveryTask, []model.RecoveryEvidence) ([]model.RecoveryProof, error) {
	if err := h.step("compare"); err != nil {
		return nil, err
	}
	var p []model.RecoveryProof
	for _, other := range h.evidence[1:] {
		p = append(p, model.RecoveryProof{CandidateID: h.evidence[0].InstanceID, OtherID: other.InstanceID, CandidateFingerprint: h.evidence[0].Fingerprint, OtherFingerprint: other.Fingerprint, CommonWALVerified: true})
	}
	return p, nil
}
func (h *recoveryHarness) StartPrimary(context.Context, model.RecoveryTask) error {
	return h.step("start")
}
func (h *recoveryHarness) Rebuild(context.Context, model.RecoveryTask, model.DatabaseInstance) error {
	return h.step("rebuild")
}
func (h *recoveryHarness) Verify(context.Context, model.RecoveryTask) (model.TopologySnapshot, error) {
	return model.TopologySnapshot{}, h.step("verify")
}
func (h *recoveryHarness) Activate(context.Context, model.RecoveryTask) error {
	return h.step("activate")
}
func (h *recoveryHarness) Complete(ctx context.Context, task model.RecoveryTask) (model.RecoveryTask, error) {
	if err := h.step("verify-activation"); err != nil {
		return task, err
	}
	return h.CompleteRecovery(ctx, task)
}

func TestRecoveryManagerFencesBeforeEvidenceAndCommitsBeforeActivation(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		for _, fail := range []string{"", "preflight", "fence", "inspect", "compare", "start", "rebuild", "verify", "commit", "activate", "verify-activation", "complete"} {
			t.Run(string(engine)+"/"+fail, func(t *testing.T) {
				members, evidence := selectionFixture(engine)
				evidence[0].GTIDExecuted = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-20"
				evidence[0].Position = "0/500"
				fingerprintAll(evidence)
				h := &recoveryHarness{task: model.RecoveryTask{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1}, ClusterID: model.NewResourceID(), Engine: engine, Stage: model.RecoveryPlanned, Members: members}, evidence: evidence, fail: fail}
				m := Manager{Store: h, Authority: h, Locks: h, Maintenance: h, Driver: h, DrainInterval: time.Nanosecond}
				result, err := m.Execute(context.Background(), h.task.ResourceID, 1)
				joined := strings.Join(h.calls, ",")
				if fail == "" {
					if err != nil || result.Stage != model.RecoverySucceeded {
						t.Fatalf("happy path: %v %+v %s", err, result, joined)
					}
					if !strings.Contains(joined, "fence,fence,fence,inspect,inspect,inspect,compare,start,rebuild,rebuild,verify,commit,activate,verify-activation,complete") {
						t.Fatalf("unsafe order: %s", joined)
					}
				} else {
					if err == nil || result.Stage == model.RecoverySucceeded {
						t.Fatalf("failure became success: %+v %s", result, joined)
					}
					if fail == "preflight" && joined != "preflight" {
						t.Fatalf("preflight failure changed runtime: %s", joined)
					}
					if fail == "commit" && strings.Contains(joined, "activate") {
						t.Fatalf("failed commit activated writer: %s", joined)
					}
					if (fail == "commit" || fail == "complete" || fail == "verify-activation") && !strings.Contains(joined, "refence") {
						t.Fatalf("lost task identity prevented failure fencing: %s", joined)
					}
				}
			})
		}
	}
}
