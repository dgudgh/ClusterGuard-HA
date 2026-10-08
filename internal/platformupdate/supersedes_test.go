package platformupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The manager reads a job's events from events.jsonl, not from status.json, so a
// fixture that only writes the status file describes a record the runner never
// produces.
func writeEventsForTest(t *testing.T, root, patchID string, statuses ...string) {
	t.Helper()
	var contents []byte
	for index, status := range statuses {
		line, err := json.Marshal(Event{ExecutionID: "execution-" + patchID, PatchID: patchID, Mode: ModeExecute, Status: status, UpdatedAt: time.Unix(int64(index), 0).UTC()})
		if err != nil {
			t.Fatal(err)
		}
		contents = append(append(contents, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(root, patchID, eventsFileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The record's own kind decides whether its operation counts as evidence: only a
// hotfix manifest can name a predecessor.
func writePackageForTest(t *testing.T, root, patchID, kind string) {
	t.Helper()
	contents, err := marshalJSON(Package{PatchID: patchID, Kind: kind, SignatureVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, patchID, packageFileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSignedInstalledSuccessorBlocksOnlyDeclaredPredecessor(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	successor := "HF-TEST-SUCCESSOR"
	directory := filepath.Join(manager.config.RootDirectory, successor)
	if err := ensureDirectoryMode(directory, updateJobDirMode); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(directory, "deployment.json"), Deployment{PackageID: successor, State: "installed"}); err != nil {
		t.Fatal(err)
	}
	manager.inspector = inspectorStub{result: Package{PatchID: successor, SignatureVerified: true, Supersedes: []string{id}}}
	if err := manager.checkSuperseded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("declared predecessor not blocked: %v", err)
	}
	if err := manager.checkSuperseded(context.Background(), "HF-UNRELATED"); err != nil {
		t.Fatalf("unrelated package blocked: %v", err)
	}
	if err := writeJSONAtomic(filepath.Join(directory, "deployment.json"), Deployment{PackageID: successor, State: "rolled_back"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.checkSuperseded(context.Background(), id); err != nil {
		t.Fatalf("rolled back successor blocked recovery: %v", err)
	}
}

// The guard also has to fire when there is no deployment record at all, which is
// the shape a site is left in when the patch was applied by an update runner that
// predated deployment.json: HF-2026-1008-01 was applied and verified at 16:57 on
// 2026-10-08 by exactly such a runner, so no node holds a deployment record for
// it. Reading only Deployment() made this guard a no-op there, and the first
// request it failed to stop was a rollback of HF-2026-0929-05 - which
// HF-2026-1008-01's signed manifest declares as superseded - reverting one file
// to its pre-HF-05 contents while the other four kept the newer ones.
func TestAppliedSuccessorBlocksPredecessorWithoutAnyDeploymentRecord(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	successor := "HF-TEST-APPLIED-SUCCESSOR"
	directory := filepath.Join(manager.config.RootDirectory, successor)
	if err := ensureDirectoryMode(directory, updateJobDirMode); err != nil {
		t.Fatal(err)
	}
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: successor, OperationID: "applied", Mode: ModeExecute, Status: StatusSucceeded})
	writePackageForTest(t, manager.config.RootDirectory, successor, PackageKindHotfix)
	if _, found := manager.Deployment(successor); found {
		t.Fatal("this test is only meaningful without a deployment record")
	}
	manager.inspector = inspectorStub{result: Package{PatchID: successor, SignatureVerified: true, Supersedes: []string{id}}}
	if err := manager.checkSuperseded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("applied successor did not block its declared predecessor: %v", err)
	}
	if err := manager.checkSuperseded(context.Background(), "HF-UNRELATED"); err != nil {
		t.Fatalf("unrelated package blocked: %v", err)
	}
}

// The same site, one step earlier: HF-2026-0929-05's own record says failed,
// because a rejected resume was written over the run that applied it. Treating
// that record as merely failed would have let HF-2026-1008-01 be rolled back as
// if nothing on disk had replaced it.
func TestRejectedAttemptOverVerifiedSuccessCountsAsApplied(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	successor := "HF-TEST-REFUSED-RESUME"
	directory := filepath.Join(manager.config.RootDirectory, successor)
	if err := ensureDirectoryMode(directory, updateJobDirMode); err != nil {
		t.Fatal(err)
	}
	writeJobForTest(t, manager.config.RootDirectory, Job{
		PatchID: successor, OperationID: "refused", Mode: ModeResume, Status: StatusFailed,
	})
	writePackageForTest(t, manager.config.RootDirectory, successor, PackageKindHotfix)
	// The site's real shape: the refused attempt wrote no event at all, so the
	// last completed event in the chain is still the run that applied the patch.
	writeEventsForTest(t, manager.config.RootDirectory, successor, "running", "verified", "succeeded")
	manager.inspector = inspectorStub{result: Package{PatchID: successor, SignatureVerified: true, Supersedes: []string{id}}}
	if err := manager.checkSuperseded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("refused attempt over a verified success did not block its predecessor: %v", err)
	}
	// A failure that never reached success is not evidence the payload is there,
	// and must keep the predecessor recoverable.
	writeJobForTest(t, manager.config.RootDirectory, Job{
		PatchID: successor, OperationID: "never-applied", Mode: ModeExecute, Status: StatusFailed,
	})
	writeEventsForTest(t, manager.config.RootDirectory, successor, "running", "failed")
	if err := manager.checkSuperseded(context.Background(), id); err != nil {
		t.Fatalf("a successor that never applied blocked recovery: %v", err)
	}
}

// A rolling package cannot declare what it supersedes, so its operation is never
// treated as a blocking successor - otherwise this guard would re-inspect the
// site's whole RPM history before every hotfix, including release lines the
// installed script no longer accepts.
func TestSucceededRollingPackageIsNotABlockingSuccessor(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	rolling := "cgupgrade-2.2-103-to-2.2-104-x86_64"
	if err := ensureDirectoryMode(filepath.Join(manager.config.RootDirectory, rolling), updateJobDirMode); err != nil {
		t.Fatal(err)
	}
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: rolling, OperationID: "upgraded", Mode: ModeExecute, Status: StatusSucceeded})
	writePackageForTest(t, manager.config.RootDirectory, rolling, PackageKindUpgrade)
	manager.inspector = inspectorStub{result: Package{PatchID: rolling, SignatureVerified: true, Supersedes: []string{id}}}
	if err := manager.checkSuperseded(context.Background(), id); err != nil {
		t.Fatalf("a rolling package blocked a hotfix: %v", err)
	}
}
