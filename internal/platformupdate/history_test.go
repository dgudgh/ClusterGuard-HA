package platformupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentSuccessSurvivesLaterFailedPlanAndRestart(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, OperationID: "successful-operation", Mode: ModeExecute, Status: StatusSucceeded, UpdatedAt: time.Now().UTC()})
	helper.startErr = errors.New("helper unavailable")
	if _, err := manager.Start(context.Background(), ModePlan, id, ""); err == nil {
		t.Fatal("expected helper failure")
	}
	reopened := NewManager(manager.config, WithHelper(helper), WithInspector(manager.inspector))
	deployment, found := reopened.Deployment(id)
	if !found || deployment.State != "installed" || deployment.LastVerifiedOperationID != "successful-operation" {
		t.Fatalf("deployment lost: %+v", deployment)
	}
	operations := reopened.Operations(id)
	if len(operations) != 2 {
		t.Fatalf("expected two independent operations, got %+v", operations)
	}
	for _, operation := range operations {
		if operation.OperationID == "successful-operation" && operation.Status != StatusSucceeded {
			t.Fatalf("success overwritten: %+v", operation)
		}
	}
	job, found := reopened.Job(id)
	if !found || job.Status != StatusFailed || job.DeploymentState != "installed" {
		t.Fatalf("latest attempt and deployment not separated: %+v", job)
	}
}

func TestCorruptHistoryBlocksStartWithoutReplacingSuccess(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, OperationID: "verified", Mode: ModeExecute, Status: StatusSucceeded})
	path := filepath.Join(manager.config.RootDirectory, id, "operations.jsonl")
	if err := os.WriteFile(path, []byte("{corrupt}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), ModePlan, id, ""); err == nil {
		t.Fatal("corrupt history accepted")
	}
	job, _ := manager.Job(id)
	if job.Status != StatusSucceeded || !job.VerificationRequired || len(helper.started) != 0 {
		t.Fatalf("lost success or started: %+v", job)
	}
}

func TestLateRunningSnapshotCannotReplaceTerminalOperation(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	directory := filepath.Join(manager.config.RootDirectory, id)
	job := Job{PatchID: id, OperationID: "same-operation", Status: StatusSucceeded}
	if err := appendOperation(directory, "operations.jsonl", job); err != nil {
		t.Fatal(err)
	}
	job.Status = StatusRunning
	if err := appendOperation(directory, "operations.jsonl", job); err != nil {
		t.Fatal(err)
	}
	if operations := manager.Operations(id); len(operations) != 1 || operations[0].Status != StatusSucceeded {
		t.Fatalf("terminal overwritten: %+v", operations)
	}
}

func TestReleasedBytesCannotChangeUnderSamePackageIdentity(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	path := filepath.Join(manager.config.RootDirectory, id, patchFileName)
	if err := os.WriteFile(path, []byte("different signed bytes with reused identity"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), ModePlan, id, ""); !errors.Is(err, ErrPackageConflict) {
		t.Fatalf("changed bytes accepted: %v", err)
	}
	if len(helper.started) != 0 {
		t.Fatal("mutated artifact reached helper")
	}
}

func TestHelperLaunchFailureHasNewIdentityAndKeepsVerifiedDeployment(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, OperationID: "successful-operation", Mode: ModeExecute, Status: StatusSucceeded})
	root, err := filepath.EvalSymlinks(manager.config.RootDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := openHelperDirectory(filepath.Join(root, id))
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	NewHelperHandler(root, nil).recordLaunchFailure(directory, id, ModePlan, errors.New("launcher failed"), "new-attempt")
	job, _ := manager.Job(id)
	deployment, found := manager.Deployment(id)
	if job.OperationID != "new-attempt" || job.Status != StatusFailed || !found || deployment.State != "installed" {
		t.Fatalf("launch failure lost history: job=%+v deployment=%+v", job, deployment)
	}
}

func TestLostResponseCannotStartTheSameOperationTwiceAfterRestart(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, OperationID: "failed-before", Mode: ModeExecute, Status: StatusFailed})
	first, err := manager.StartWithOperationID(context.Background(), ModeRetry, id, id, "client-operation")
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewManager(manager.config, WithHelper(helper), WithInspector(manager.inspector))
	second, err := reopened.StartWithOperationID(context.Background(), ModeRetry, id, id, "client-operation")
	if err != nil || first.OperationID != second.OperationID || len(helper.started) != 1 {
		t.Fatalf("duplicate execution: first=%+v second=%+v helper=%v err=%v", first, second, helper.started, err)
	}
	if _, err := reopened.StartWithOperationID(context.Background(), ModeRollback, id, id, "client-operation"); !errors.Is(err, ErrPackageConflict) {
		t.Fatalf("operation identity reused for a different action: %v", err)
	}
}

func TestLegacyRejectedResumeMigratesItsEarlierVerifiedSuccess(t *testing.T) {
	manager, helper, id := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, Mode: ModeResume, Status: StatusFailed, UpdatedAt: time.Now().UTC(), Events: []Event{{ExecutionID: "verified-original", Mode: ModeExecute, Status: "succeeded", UpdatedAt: time.Now().UTC()}}})
	event, _ := json.Marshal(Event{ExecutionID: "verified-original", PatchID: id, Mode: ModeExecute, Status: "succeeded", UpdatedAt: time.Now().UTC().Add(-time.Minute)})
	if err := os.WriteFile(filepath.Join(manager.config.RootDirectory, id, eventsFileName), append(event, '\n'), 0o640); err != nil {
		t.Fatal(err)
	}
	helper.startErr = errors.New("plan failed")
	_, _ = manager.Start(context.Background(), ModePlan, id, "")
	deployment, found := manager.Deployment(id)
	if !found || deployment.State != "installed" || deployment.LastVerifiedOperationID != "verified-original" {
		t.Fatalf("legacy incident lost success: %+v", deployment)
	}
}

func TestHistoryCacheDetectsAtomicReplacementWithSameSizeAndTimestamp(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	directory := filepath.Join(manager.config.RootDirectory, id)
	job := Job{PatchID: id, OperationID: "valid-operation", Mode: ModeExecute, Status: StatusSucceeded}
	if err := appendOperation(directory, "operations.jsonl", job); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateHistory(id); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "operations.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(strings.Repeat("x", int(info.Size())-1) + "\n")
	temporary := filepath.Join(directory, "replacement")
	if err := os.WriteFile(temporary, corrupt, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(temporary, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
	if err := manager.validateHistory(id); err == nil {
		t.Fatal("cached validation hid atomic corruption")
	}
}

func TestProgressCannotImportAnotherOperationsEvents(t *testing.T) {
	at := time.Now().UTC()
	job := Job{PatchID: "HF-TEST", OperationID: "new-operation", Mode: ModeRetry, Status: StatusRunning, UpdatedAt: at, Events: []Event{{OperationID: "old-operation", Mode: ModeExecute, Status: "succeeded", Node: "other-node", UpdatedAt: at.Add(time.Minute)}}}
	deriveJobProgress(&job)
	if job.Mode != ModeRetry || job.Status != StatusRunning || job.Node != "" {
		t.Fatalf("foreign operation changed current job: %+v", job)
	}
}
