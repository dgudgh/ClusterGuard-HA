package platformupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOperationProductVersionsSurviveManagerRestart(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	job := Job{PatchID: id, OperationID: "operation-A", Mode: ModeExecute, Status: StatusSucceeded,
		FromVersion: "3.1.1.7", ToVersion: "3.1.1.8", FromNodeVersions: []NodeVersion{{Node: "one", Version: "3.1.1.7"}, {Node: "two", Version: "3.1.1.7"}, {Node: "three", Version: "3.1.1.7"}}}
	writeJobForTest(t, manager.config.RootDirectory, job)
	dir := filepath.Join(manager.config.RootDirectory, id)
	if err := appendOperation(dir, "operations.jsonl", job); err != nil {
		t.Fatal(err)
	}
	// First migration: an already-running old wrapper publishes a terminal JSON
	// without the new fields. The Runner's same-ID root history remains evidence.
	terminal := job
	terminal.FromVersion, terminal.ToVersion, terminal.FromNodeVersions = "", "", nil
	writeJobForTest(t, manager.config.RootDirectory, terminal)
	if err := appendOperation(dir, "operations.jsonl", terminal); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(dir, jobFileName))
	reopened := NewManager(manager.config, WithHelper(helper), WithInspector(manager.inspector))
	read, ok := reopened.Job(id)
	if !ok || read.FromVersion != "3.1.1.7" || read.ToVersion != "3.1.1.8" || len(read.FromNodeVersions) != 3 {
		t.Fatalf("lost observations: %+v", read)
	}
	operations := reopened.Operations(id)
	if len(operations) != 1 || operations[0].FromVersion != read.FromVersion {
		t.Fatalf("lost history: %+v", operations)
	}
	b, _ := json.Marshal(PackageStatus{Package: Package{PatchID: id, SourceVersion: "2.2-105", PatchVersion: "3.1.1.8"}, Job: &read})
	var roundtrip PackageStatus
	if err := json.Unmarshal(b, &roundtrip); err != nil || roundtrip.Job.FromVersion != "3.1.1.7" || roundtrip.Package.SourceVersion != "2.2-105" {
		t.Fatalf("API conflates eligibility and observations: %s %v", b, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, jobFileName))
	if string(after) != string(original) {
		t.Fatal("read rewrote original evidence")
	}
	// Old status has no observations: never manufacture one from this process or another operation.
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, OperationID: "operation-legacy", Status: StatusSucceeded})
	legacy, _ := reopened.Job(id)
	if legacy.FromVersion != "" || legacy.ToVersion != "" || len(legacy.FromNodeVersions) != 0 {
		t.Fatalf("invented legacy identity: %+v", legacy)
	}
}
