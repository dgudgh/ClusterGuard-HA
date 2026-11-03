package platformupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryProjectionUsesCausalRequestsAndNativeTerminalTime(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		manager, _, id := preparedHotfixManager(t)
		dir := filepath.Join(manager.config.RootDirectory, id)
		now := time.Now().UTC().Truncate(time.Second)
		// The second operation started after the first in the durable request log,
		// even if a backwards wall clock and stale node-local status suggest otherwise.
		old := Job{PatchID: id, OperationID: "old-rollback", Mode: ModeRollback, Status: StatusRolledBack, StartedAt: now, FinishedAt: now.Add(10 * time.Second), UpdatedAt: now.Add(10 * time.Second)}
		current := Job{PatchID: id, OperationID: "next-attempt", Mode: ModeExecute, Status: StatusFailed, StartedAt: now.Add(-time.Second), FinishedAt: now.Add(20 * time.Second), UpdatedAt: now.Add(20 * time.Second)}
		for _, j := range []Job{old, current} {
			if err := appendOperation(dir, "requests.jsonl", j); err != nil {
				t.Fatal(err)
			}
		}
		// Root history has a causal prefix; only late snapshots may be in a
		// different order. Local request files need not be complete on peers.
		for _, j := range []Job{old, current} {
			if err := appendOperation(dir, "operations.jsonl", j); err != nil {
				t.Fatal(err)
			}
		}
		jobs := []Job{current, old}
		if reverse {
			jobs = []Job{old, current}
		}
		for _, j := range jobs {
			if err := appendOperation(dir, "operations.jsonl", j); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeJSONAtomic(filepath.Join(dir, jobFileName), old); err != nil {
			t.Fatal(err)
		}
		event := Event{PatchID: id, OperationID: current.OperationID, Mode: ModeExecute, Status: "failed", UpdatedAt: now.Add(15 * time.Second)}
		data, _ := json.Marshal(event)
		if err := os.WriteFile(filepath.Join(dir, eventsFileName), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := manager.Job(id)
		ops := manager.Operations(id)
		if !ok || got.OperationID != current.OperationID || got.Status != StatusFailed || !got.FinishedAt.Equal(event.UpdatedAt) || len(ops) != 2 || ops[1].OperationID != current.OperationID || !ops[1].FinishedAt.Equal(got.FinishedAt) {
			t.Fatalf("node-local/file-order projection differs: got=%+v ops=%+v", got, ops)
		}
		raw := Job{}
		if err := readJSONFile(filepath.Join(dir, jobFileName), &raw); err != nil || raw.OperationID != old.OperationID {
			t.Fatal("projection rewrote original status")
		}
	}
}
func TestHistoryProjectionRejectsConflictingTerminalIdentity(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	dir := filepath.Join(manager.config.RootDirectory, id)
	j := Job{PatchID: id, OperationID: "conflicting-attempt", Mode: ModeExecute, Status: StatusSucceeded}
	if err := appendOperation(dir, "requests.jsonl", j); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, jobFileName), j); err != nil {
		t.Fatal(err)
	}
	j.Status = StatusFailed
	if err := appendOperation(dir, "operations.jsonl", j); err != nil {
		t.Fatal(err)
	}
	got, _ := manager.Job(id)
	if !got.VerificationRequired {
		t.Fatalf("conflicting terminal identity selected by file order: %+v", got)
	}
}

func TestHistoryProjectionDoesNotLetIncompletePeerRequestsReorderRootHistory(t *testing.T) {
	for _, requestID := range []string{"earlier", "later"} {
		m, _, id := preparedHotfixManager(t)
		dir := filepath.Join(m.config.RootDirectory, id)
		old := Job{PatchID: id, OperationID: "earlier", Mode: ModeRollback, Status: StatusRolledBack}
		next := Job{PatchID: id, OperationID: "later", Mode: ModeExecute, Status: StatusFailed}
		for _, j := range []Job{old, next} {
			if err := appendOperation(dir, "operations.jsonl", j); err != nil {
				t.Fatal(err)
			}
		}
		request := old
		if requestID == "later" {
			request = next
		}
		if err := appendOperation(dir, "requests.jsonl", request); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(filepath.Join(dir, jobFileName), old); err != nil {
			t.Fatal(err)
		}
		got, _ := m.Job(id)
		if got.OperationID != "later" || got.Status != StatusFailed {
			t.Fatalf("incomplete peer requests reversed root causal order: %+v", got)
		}
	}
}

func TestHistoryProjectionDoesNotUseWallClockAsReplacementAuthority(t *testing.T) {
	m, _, id := preparedHotfixManager(t)
	dir := filepath.Join(m.config.RootDirectory, id)
	current := Job{PatchID: id, OperationID: "current-subject", Mode: ModeExecute, Status: StatusSucceeded, StartedAt: time.Now().UTC()}
	unrelated := Job{PatchID: id, OperationID: "legacy-unrelated", Mode: ModeRollback, Status: StatusRolledBack, StartedAt: current.StartedAt.Add(time.Hour)}
	if err := appendOperation(dir, "operations.jsonl", current); err != nil {
		t.Fatal(err)
	}
	if err := appendOperation(dir, "requests.jsonl", unrelated); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, jobFileName), current); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Job(id)
	if got.OperationID != current.OperationID {
		t.Fatalf("unrelated clock replaced current subject: %+v", got)
	}
}
func TestConflictingCausalHistoryBlocksActionAndPreservesOriginals(t *testing.T) {
	m, helper, id := preparedHotfixManager(t)
	dir := filepath.Join(m.config.RootDirectory, id)
	a := Job{PatchID: id, OperationID: "attempt-a", Mode: ModePlan, Status: StatusPlanned}
	b := Job{PatchID: id, OperationID: "attempt-b", Mode: ModeExecute, Status: StatusFailed}
	for _, j := range []Job{a, b} {
		if err := appendOperation(dir, "operations.jsonl", j); err != nil {
			t.Fatal(err)
		}
	}
	for _, j := range []Job{b, a} {
		if err := appendOperation(dir, "requests.jsonl", j); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONAtomic(filepath.Join(dir, jobFileName), b); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, jobFileName))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m.Job(id)
	if !got.VerificationRequired {
		t.Fatal("causal conflict not flagged")
	}
	if _, err := m.Start(context.Background(), ModePlan, id, ""); err == nil || len(helper.started) != 0 {
		t.Fatal("conflicting history reached Helper")
	}
	after, _ := os.ReadFile(filepath.Join(dir, jobFileName))
	if string(before) != string(after) {
		t.Fatal("conflict replaced original status")
	}
}
