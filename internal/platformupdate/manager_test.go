package platformupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inspectorStub struct {
	result Package
	err    error
}

type countingInspector struct {
	result Package
	calls  int
}

func (inspector *countingInspector) Inspect(context.Context, string, string) (Package, error) {
	inspector.calls++
	return inspector.result, nil
}

func (stub inspectorStub) Inspect(context.Context, string, string) (Package, error) {
	return stub.result, stub.err
}

type helperStub struct {
	readyErr error
	startErr error
	started  []Mode
}

func (stub *helperStub) Ready(context.Context) error { return stub.readyErr }

func (stub *helperStub) Start(_ context.Context, mode Mode, _ string) error {
	stub.started = append(stub.started, mode)
	return stub.startErr
}

func TestManagerUploadVerifiesAndPersistsSignedUpgradePackage(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	helper := &helperStub{}
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust, MaximumUploadBytes: 1024},
		WithInspector(inspectorStub{result: Package{
			PatchID: "cg-2.2-1-to-2.2-2", SourceVersion: "2.2-1", TargetVersion: "2.2-2",
			Architecture: "x86_64", SignatureVerified: true, RollbackAvailable: true, Rolling: true,
			BootstrapAvailable: true, BootstrapProtocol: 1,
		}}), WithHelper(helper), WithClock(func() time.Time { return time.Unix(100, 0).UTC() }))

	result, err := manager.Upload(context.Background(), "release.cgupgrade", strings.NewReader("signed package"))
	if err != nil {
		t.Fatal(err)
	}
	if result.PatchID != "cg-2.2-1-to-2.2-2" || result.SHA256 == "" || result.SizeBytes != int64(len("signed package")) {
		t.Fatalf("unexpected package: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, result.PatchID, patchFileName)); err != nil {
		t.Fatalf("patch was not persisted: %v", err)
	}
	for _, expectation := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(root, result.PatchID), mode: updateJobDirMode},
		{path: filepath.Join(root, result.PatchID, patchFileName), mode: updateFileMode},
		{path: filepath.Join(root, result.PatchID, packageFileName), mode: updateFileMode},
	} {
		info, statErr := os.Stat(expectation.path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != expectation.mode {
			t.Fatalf("%s mode=%#o want=%#o", expectation.path, info.Mode().Perm(), expectation.mode)
		}
	}
	snapshot := manager.Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Packages) != 1 || snapshot.Packages[0].Package.PatchID != result.PatchID {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestEnsureDirectoryModeDoesNotChmodMatchingPrivilegedDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "root-owned-update")
	if err := os.Mkdir(directory, updateJobDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, updateJobDirMode); err != nil {
		t.Fatal(err)
	}

	chmodCalls := 0
	err := ensureDirectoryModeWithChmod(directory, updateJobDirMode, func(string, os.FileMode) error {
		chmodCalls++
		return os.ErrPermission
	})
	if err != nil {
		t.Fatalf("matching directory mode should not require ownership: %v", err)
	}
	if chmodCalls != 0 {
		t.Fatalf("chmod calls=%d want=0", chmodCalls)
	}
}

func TestEnsureDirectoryModeRepairsMismatchedDirectoryMode(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "update")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}

	chmodCalls := 0
	err := ensureDirectoryModeWithChmod(directory, updateJobDirMode, func(path string, mode os.FileMode) error {
		chmodCalls++
		return os.Chmod(path, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	if chmodCalls != 1 {
		t.Fatalf("chmod calls=%d want=1", chmodCalls)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != updateJobDirMode {
		t.Fatalf("directory mode=%#o want=%#o", info.Mode().Perm(), updateJobDirMode)
	}
}

func TestManagerRequiresBootstrapForPreferredUpgradePackage(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust, MaximumUploadBytes: 1024},
		WithInspector(inspectorStub{result: Package{
			PatchID: "cg-2.2-1-to-2.2-2", SourceVersion: "2.2-1", TargetVersion: "2.2-2",
			SignatureVerified: true, RollbackAvailable: true, Rolling: true,
		}}), WithHelper(&helperStub{}))
	if _, err := manager.Upload(context.Background(), "release.cgupgrade", strings.NewReader("signed package")); !errors.Is(err, ErrBootstrapRequired) {
		t.Fatalf("preferred package without bootstrap err=%v", err)
	}
	if _, err := manager.Upload(context.Background(), "legacy.cgpatch", strings.NewReader("signed package")); err != nil {
		t.Fatalf("legacy package should remain compatible: %v", err)
	}
}

func TestManagerRefusesPreviouslyUploadedUpgradePackageWithoutBootstrap(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	packagePath := filepath.Join(manager.config.RootDirectory, patchID, packageFileName)
	softwarePackage, found := manager.Package(patchID)
	if !found {
		t.Fatal("prepared package not found")
	}
	softwarePackage.FileName = "old-release.cgupgrade"
	softwarePackage.BootstrapAvailable = false
	softwarePackage.BootstrapProtocol = 0
	if err := writeJSONAtomic(packagePath, softwarePackage); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), ModePlan, patchID, ""); !errors.Is(err, ErrBootstrapRequired) {
		t.Fatalf("stored package without bootstrap err=%v", err)
	}
}

func TestManagerReverifiesAndBackfillsBootstrapFromOlderPackageMetadata(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	packagePath := filepath.Join(manager.config.RootDirectory, patchID, packageFileName)
	softwarePackage, found := manager.Package(patchID)
	if !found {
		t.Fatal("prepared package not found")
	}
	softwarePackage.FileName = "uploaded-by-2.2-42.cgupgrade"
	softwarePackage.BootstrapAvailable = false
	softwarePackage.BootstrapProtocol = 0
	if err := writeJSONAtomic(packagePath, softwarePackage); err != nil {
		t.Fatal(err)
	}
	manager.inspector = inspectorStub{result: Package{
		PatchID: patchID, SourceVersion: softwarePackage.SourceVersion, TargetVersion: softwarePackage.TargetVersion,
		Architecture: softwarePackage.Architecture, SignatureVerified: true, RollbackAvailable: true, Rolling: true,
		BootstrapAvailable: true, BootstrapProtocol: 1,
	}}
	snapshot := manager.Snapshot(context.Background())
	if len(snapshot.Packages) != 1 || !snapshot.Packages[0].Package.BootstrapAvailable || snapshot.Packages[0].Package.BootstrapProtocol != 1 {
		t.Fatalf("snapshot did not reconcile older package metadata: %+v", snapshot)
	}
	persisted, found := manager.Package(patchID)
	if !found || !persisted.BootstrapAvailable || persisted.BootstrapProtocol != 1 {
		t.Fatalf("bootstrap contract was not persisted: found=%t package=%+v", found, persisted)
	}
	if _, err := manager.Start(context.Background(), ModePlan, patchID, ""); err != nil {
		t.Fatalf("reconciled package should be plannable: %v", err)
	}
}

func TestManagerCachesVerifiedBootstrapWhenRootHistoryCannotBeRewritten(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	packageDirectory := filepath.Join(manager.config.RootDirectory, patchID)
	packagePath := filepath.Join(packageDirectory, packageFileName)
	softwarePackage, found := manager.Package(patchID)
	if !found {
		t.Fatal("prepared package not found")
	}
	softwarePackage.FileName = "uploaded-by-older-release.cgupgrade"
	softwarePackage.BootstrapAvailable = false
	softwarePackage.BootstrapProtocol = 0
	if err := writeJSONAtomic(packagePath, softwarePackage); err != nil {
		t.Fatal(err)
	}
	inspector := &countingInspector{result: Package{
		PatchID: patchID, SourceVersion: softwarePackage.SourceVersion, TargetVersion: softwarePackage.TargetVersion,
		Architecture: softwarePackage.Architecture, SignatureVerified: true, RollbackAvailable: true, Rolling: true,
		BootstrapAvailable: true, BootstrapProtocol: 1,
	}}
	manager.inspector = inspector
	if err := os.Chmod(packageDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(packageDirectory, 0o750)
	for attempt := 0; attempt < 2; attempt++ {
		snapshot := manager.Snapshot(context.Background())
		if len(snapshot.Packages) != 1 || !snapshot.Packages[0].Package.BootstrapAvailable || snapshot.Packages[0].Package.BootstrapProtocol != 1 {
			t.Fatalf("snapshot %d lost verified bootstrap: %+v", attempt+1, snapshot)
		}
	}
	if inspector.calls != 1 {
		t.Fatalf("verified bootstrap should be cached after one inspection, calls=%d", inspector.calls)
	}
	if err := os.Chmod(packageDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), ModePlan, patchID, ""); err != nil {
		t.Fatalf("cached bootstrap should remain plannable: %v", err)
	}
}

func TestSupportedPackageFileNamePrefersUpgradeAndKeepsLegacyCompatibility(t *testing.T) {
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "release.cgupgrade", want: true},
		{name: "release.CGUPGRADE", want: true},
		{name: "legacy.cgpatch", want: true},
		{name: "clusterguard-ha-offline.tar.gz", want: false},
		{name: "release.cgupgrade.tar.gz", want: false},
	} {
		if got := SupportedPackageFileName(test.name); got != test.want {
			t.Fatalf("SupportedPackageFileName(%q)=%t want=%t", test.name, got, test.want)
		}
	}
}

func TestManagerUploadFailsClosedOnInspectionAndSize(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust, MaximumUploadBytes: 4},
		WithInspector(inspectorStub{err: errors.New("signature invalid")}), WithHelper(&helperStub{}))
	if _, err := manager.Upload(context.Background(), "release.cgpatch", strings.NewReader("too large")); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("expected upload size failure, got %v", err)
	}
	manager = NewManager(Config{RootDirectory: root, TrustKeyPath: trust, MaximumUploadBytes: 1024},
		WithInspector(inspectorStub{err: errors.New("signature invalid")}), WithHelper(&helperStub{}))
	if _, err := manager.Upload(context.Background(), "release.cgpatch", strings.NewReader("patch")); err == nil || !strings.Contains(err.Error(), "signature invalid") {
		t.Fatalf("expected signature failure, got %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "public.pem" {
			t.Fatalf("failed upload left staging data: %s", entry.Name())
		}
	}
}

func TestManagerUploadIsIdempotentAndRejectsPatchIDContentReplacement(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	existing, found := manager.Package(patchID)
	if !found {
		t.Fatal("prepared patch is missing")
	}
	same, err := manager.Upload(context.Background(), "same.cgpatch", strings.NewReader("patch"))
	if err != nil || same.SHA256 != existing.SHA256 {
		t.Fatalf("same signed patch must be idempotent: result=%+v err=%v", same, err)
	}
	if _, err := manager.Upload(context.Background(), "replacement.cgpatch", strings.NewReader("different patch")); !errors.Is(err, ErrPackageConflict) {
		t.Fatalf("patch ID replacement must fail closed, got %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(manager.config.RootDirectory, patchID, patchFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "patch" {
		t.Fatalf("existing signed patch was replaced: %q", contents)
	}
}

func TestManagerRequiresPlanAndTypedConfirmationBeforeExecute(t *testing.T) {
	manager, helper, patchID := preparedManager(t)
	if _, err := manager.Start(context.Background(), ModeExecute, patchID, patchID); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("execute without plan err=%v", err)
	}
	if _, err := manager.Start(context.Background(), ModePlan, patchID, ""); err != nil {
		t.Fatal(err)
	}
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: patchID, Mode: ModePlan, Status: StatusPlanned})
	if _, err := manager.Start(context.Background(), ModeExecute, patchID, "wrong"); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("wrong confirmation err=%v", err)
	}
	job, err := manager.Start(context.Background(), ModeExecute, patchID, patchID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued || len(helper.started) != 2 || helper.started[1] != ModeExecute {
		t.Fatalf("unexpected job/helper state: %+v %+v", job, helper.started)
	}
}

func TestManagerHelperStartFailurePublishesReadableTerminalStatus(t *testing.T) {
	manager, helper, patchID := preparedManager(t)
	helper.startErr = errors.New("helper launch failed")
	if _, err := manager.Start(context.Background(), ModePlan, patchID, ""); err == nil || !strings.Contains(err.Error(), "helper launch failed") {
		t.Fatalf("unexpected start error: %v", err)
	}
	job, found := manager.Job(patchID)
	if !found || job.Status != StatusFailed || job.Message != "helper launch failed" || job.FinishedAt.IsZero() {
		t.Fatalf("helper failure was not persisted: found=%t job=%+v", found, job)
	}
	for _, expectation := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(manager.config.RootDirectory, patchID), mode: updateJobDirMode},
		{path: filepath.Join(manager.config.RootDirectory, patchID, jobFileName), mode: updateFileMode},
	} {
		info, err := os.Stat(expectation.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != expectation.mode {
			t.Fatalf("%s mode=%#o want=%#o", expectation.path, info.Mode().Perm(), expectation.mode)
		}
	}
}

func TestManagerRejectsUnsafePatchIdentityAndUnavailableHelper(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	helper := &helperStub{}
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
		WithInspector(inspectorStub{result: Package{PatchID: "../escape", SignatureVerified: true}}), WithHelper(helper))
	if _, err := manager.Upload(context.Background(), "bad.cgpatch", strings.NewReader("patch")); !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("unsafe identity err=%v", err)
	}
	helper.readyErr = errors.New("helper unavailable")
	snapshot := manager.Snapshot(context.Background())
	if snapshot.Available || !strings.Contains(snapshot.Reason, "helper unavailable") {
		t.Fatalf("helper readiness was not surfaced: %+v", snapshot)
	}
}

func TestManagerStatusIncludesOutputAndJournalEventsWithoutSecrets(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	jobDir := filepath.Join(manager.config.RootDirectory, patchID)
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: patchID, Mode: ModeExecute, Status: StatusRunning})
	if err := os.WriteFile(filepath.Join(jobDir, outputFileName), []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, eventsFileName), []byte(
		"{\"status\":\"updating\",\"node\":\"node-2\",\"phase\":\"updating\",\"current\":1,\"total\":3}\n"+
			"{\"status\":\"verified\",\"node\":\"node-2\",\"phase\":\"updating\",\"current\":2,\"total\":3}\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	job, found := manager.Job(patchID)
	if !found || len(job.OutputTail) != 2 || len(job.Events) != 2 || job.Events[0].Node != "node-2" {
		t.Fatalf("unexpected job evidence: found=%t job=%+v", found, job)
	}
	if job.Progress.Phase != "updating" || job.Progress.Current != 2 || job.Progress.Total != 3 || job.Progress.Percent != 61 {
		t.Fatalf("unexpected structured progress: %+v", job.Progress)
	}
	if len(job.Progress.CompletedNodes) != 1 || job.Progress.CompletedNodes[0] != "node-2" {
		t.Fatalf("unexpected completed nodes: %+v", job.Progress.CompletedNodes)
	}
}

func TestManagerSurfacesUnreadableOrCorruptJobInsteadOfUploaded(t *testing.T) {
	manager, _, patchID := preparedManager(t)
	jobPath := filepath.Join(manager.config.RootDirectory, patchID, jobFileName)
	if err := os.WriteFile(jobPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, found := manager.Job(patchID)
	if !found || job.Status != StatusFailed || job.MaintenanceActive || job.AutomaticFailoverAvailable || !job.VerificationRequired ||
		!strings.Contains(job.Message, "操作结果需要验证") {
		t.Fatalf("corrupt job was hidden as uploaded: found=%t job=%+v", found, job)
	}
	snapshot := manager.Snapshot(context.Background())
	if len(snapshot.Packages) != 1 || snapshot.Packages[0].Job == nil || snapshot.Packages[0].Job.Status != StatusFailed {
		t.Fatalf("snapshot hid corrupt job: %+v", snapshot)
	}
}

func TestJobProgressUsesTerminalStateAndLegacyEvents(t *testing.T) {
	job := Job{
		Status: StatusSucceeded,
		Events: []Event{{Status: "verified", Node: "node-1"}, {Status: "verified", Node: "node-2"}},
	}
	deriveJobProgress(&job)
	if job.Progress.Phase != "completed" || job.Progress.Percent != 100 || job.Progress.Current != 2 {
		t.Fatalf("unexpected terminal progress: %+v", job.Progress)
	}
}

func TestJobProgressUsesNewerReplicatedLeaderEvent(t *testing.T) {
	started := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	job := Job{
		Mode: ModePlan, Status: StatusPlanned, UpdatedAt: started,
		Events: []Event{{
			Mode: ModeExecute, Status: "updating", Node: "node-3", Phase: "updating", Current: 1, Total: 3,
			UpdatedAt: started.Add(time.Minute),
		}},
	}
	deriveJobProgress(&job)
	if job.Mode != ModeExecute || job.Status != StatusRunning || job.Node != "node-3" || !job.MaintenanceActive {
		t.Fatalf("newer replicated event did not replace stale local plan: %+v", job)
	}
}

func preparedManager(t *testing.T) (*Manager, *helperStub, string) {
	t.Helper()
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	helper := &helperStub{}
	patchID := "cg-2.2-1-to-2.2-2"
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
		WithInspector(inspectorStub{result: Package{PatchID: patchID, SourceVersion: "2.2-1", TargetVersion: "2.2-2", SignatureVerified: true, RollbackAvailable: true, Rolling: true}}),
		WithHelper(helper))
	if _, err := manager.Upload(context.Background(), "release.cgpatch", strings.NewReader("patch")); err != nil {
		t.Fatal(err)
	}
	return manager, helper, patchID
}

func writeJobForTest(t *testing.T, root string, job Job) {
	t.Helper()
	contents, err := marshalJSON(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, job.PatchID, jobFileName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func preparedHotfixManager(t *testing.T) (*Manager, *helperStub, string) {
	t.Helper()
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	helper := &helperStub{}
	patchID := "HF-2026-0929-05"
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
		WithInspector(inspectorStub{result: Package{
			PatchID: patchID, Kind: PackageKindHotfix, SourceVersion: "2.2-105", TargetVersion: "2.2-105+hf-2026-0929-05",
			SignatureVerified: true, RollbackAvailable: true, Rolling: true,
		}}),
		WithHelper(helper))
	if _, err := manager.Upload(context.Background(), "hotfix.cgpatch", strings.NewReader("patch")); err != nil {
		t.Fatal(err)
	}
	return manager, helper, patchID
}

// The updater refuses --resume for a hotfix on the first line of run_hotfix_update, because
// applying one is idempotent and the supported way forward is to re-run the same patch. The
// manager used to accept the request anyway, hand it to the helper and let the refusal be
// recorded as the patch's job. On 2026-09-30 that is exactly what happened on a live site:
// HF-2026-0929-05 had been applied and verified on all three nodes, a resume was submitted
// for it, and the refusal wrote `failed` over the record - after which the console reported
// an applied, verified patch as a failed one, and the only place left that knew the payload
// was in place was the event list nobody reads on the summary table.
//
// Two things have to hold, and the second is the one that matters: the request is refused,
// and the existing record survives it. A refusal that still rewrites the outcome has the
// same cost as the failure it prevented.
func TestManagerRefusesToResumeAHotfixAndKeepsItsRecord(t *testing.T) {
	manager, helper, patchID := preparedHotfixManager(t)
	applied := time.Date(2026, 9, 30, 1, 30, 59, 0, time.UTC)
	writeJobForTest(t, manager.config.RootDirectory, Job{
		PatchID: patchID, Mode: ModeExecute, Status: StatusSucceeded,
		StartedAt: applied.Add(-time.Minute), UpdatedAt: applied, FinishedAt: applied,
	})

	if _, err := manager.Start(context.Background(), ModeResume, patchID, patchID); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("resuming a hotfix must be refused with ErrResumeUnsupported, got %v", err)
	}
	if len(helper.started) != 0 {
		t.Fatalf("a refused resume must never reach the helper, started=%v", helper.started)
	}
	job, found := manager.Job(patchID)
	if !found {
		t.Fatal("the applied hotfix lost its record")
	}
	if job.Status != StatusSucceeded || job.Mode != ModeExecute || !job.FinishedAt.Equal(applied) {
		t.Fatalf("a refused resume overwrote the record of the run that applied the patch: %+v", job)
	}
}

func TestManagerRetriesOnlyFailedHotfixWithItsOwnMode(t *testing.T) {
	manager, helper, patchID := preparedHotfixManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{
		PatchID: patchID, Mode: ModeExecute, Status: StatusFailed,
		StartedAt: time.Date(2026, 9, 29, 8, 22, 35, 0, time.UTC),
	})
	if _, err := manager.Start(context.Background(), ModeRetry, patchID, "wrong"); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("retry must require the same package ID confirmation: %v", err)
	}
	job, err := manager.Start(context.Background(), ModeRetry, patchID, patchID)
	if err != nil || job.Mode != ModeRetry || job.Status != StatusQueued || len(helper.started) != 1 || helper.started[0] != ModeRetry {
		t.Fatalf("retry must reach helper as retry of the same package: job=%+v helper=%+v err=%v", job, helper.started, err)
	}
	writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: patchID, Mode: ModeRetry, Status: StatusSucceeded})
	if _, err := manager.Start(context.Background(), ModeRetry, patchID, patchID); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("an installed hotfix must not be retried: %v", err)
	}
	rolling, _, rollingID := preparedManager(t)
	writeJobForTest(t, rolling.config.RootDirectory, Job{PatchID: rollingID, Mode: ModeExecute, Status: StatusFailed})
	if _, err := rolling.Start(context.Background(), ModeRetry, rollingID, rollingID); !errors.Is(err, ErrInvalidPatch) {
		t.Fatalf("rolling upgrade retry must be rejected: %v", err)
	}
}

// The refusal is specific to hotfixes. A rolling upgrade is applied node by node and a
// failure part-way leaves nodes on two different versions, so resume is the only supported
// way forward and must keep working - including the fact that it still reaches the helper.
func TestManagerStillResumesARollingUpgrade(t *testing.T) {
	manager, helper, patchID := preparedManager(t)
	writeJobForTest(t, manager.config.RootDirectory, Job{
		PatchID: patchID, Mode: ModeExecute, Status: StatusFailed,
		StartedAt: time.Date(2026, 9, 29, 8, 22, 35, 0, time.UTC),
	})

	job, err := manager.Start(context.Background(), ModeResume, patchID, patchID)
	if err != nil {
		t.Fatalf("a rolling upgrade must still be resumable: %v", err)
	}
	if job.Status != StatusQueued || len(helper.started) != 1 || helper.started[0] != ModeResume {
		t.Fatalf("unexpected job/helper state: %+v %+v", job, helper.started)
	}
}

// uploaded_at is written from the node clock, so a record created while that clock ran ahead
// carries a timestamp no later clock agrees with. The 2026-09-29 site incident is exactly this:
// the RTC was read as UTC and then localised a second time, HF-2026-0928-06 was recorded at
// 2026-09-29T11:01:17Z while the real time was about 05:22Z, and its package.json mtime landed
// at 19:02 +0800 - six hours in the future. Such a record is reported, not silently reordered:
// uploaded_at is both the ordering key and the time the console shows, so an operator choosing
// an upgrade package has to be told the time on that row is untrustworthy.
func TestSnapshotReportsRecordsWrittenByAFutureClock(t *testing.T) {
	now := time.Date(2026, 9, 29, 5, 24, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{name: "a record written on an agreeing clock is trusted", offset: 0},
		{name: "a record inside the tolerance is trusted", offset: clockSkewTolerance},
		{name: "a record past the tolerance is reported", offset: clockSkewTolerance + time.Second, want: true},
		{name: "a record from the past is trusted", offset: -7 * time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			trust := filepath.Join(root, "public.pem")
			if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
				t.Fatal(err)
			}
			patchID := "cgupgrade-2.2-104-to-2.2-105-x86_64"
			writer := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
				WithInspector(inspectorStub{result: Package{
					PatchID: patchID, SourceVersion: "2.2-104", TargetVersion: "2.2-105",
					Architecture: "x86_64", SignatureVerified: true, RollbackAvailable: true, Rolling: true,
					BootstrapAvailable: true, BootstrapProtocol: 1,
				}}),
				WithHelper(&helperStub{}),
				WithClock(func() time.Time { return now.Add(testCase.offset) }))
			if _, err := writer.Upload(context.Background(), "release.cgupgrade", strings.NewReader("signed package")); err != nil {
				t.Fatal(err)
			}

			// The clock is corrected afterwards, which is what the site did on 2026-09-29.
			reader := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
				WithInspector(inspectorStub{result: Package{PatchID: patchID, BootstrapAvailable: true, BootstrapProtocol: 1}}),
				WithHelper(&helperStub{}),
				WithClock(func() time.Time { return now }))
			snapshot := reader.Snapshot(context.Background())
			if len(snapshot.Packages) != 1 {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			if snapshot.Packages[0].ClockSkew != testCase.want {
				t.Fatalf("offset %s reported clock_skew=%v, want %v",
					testCase.offset, snapshot.Packages[0].ClockSkew, testCase.want)
			}
		})
	}
}

// A future-stamped finished record keeps its position at the head of the list, because the
// ordering key is the very timestamp that is wrong. The site shape is reproduced here: the
// hotfix record sorts above the upgrade package that was really uploaded later, and its job has
// already succeeded. Reading packages[0] therefore selects a package that cannot run, which is
// why the console resolves the newest actionable record instead; this test pins the server side
// of that contract so the console assertion cannot quietly become vacuous.
func TestSnapshotOrdersAFutureStampedRecordAboveALaterUpload(t *testing.T) {
	futureStamped := time.Date(2026, 9, 29, 11, 1, 17, 712051173, time.UTC)
	realUpload := time.Date(2026, 9, 29, 5, 16, 35, 838450034, time.UTC)
	now := time.Date(2026, 9, 29, 5, 22, 0, 0, time.UTC)
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}

	for _, seeded := range []struct {
		patchID    string
		target     string
		uploadedAt time.Time
		jobStatus  Status
	}{
		{patchID: "HF-2026-0928-06", target: "2.2-104+hf-2026-0928-06", uploadedAt: futureStamped, jobStatus: StatusSucceeded},
		{patchID: "cgupgrade-2.2-104-to-2.2-105-x86_64", target: "2.2-105", uploadedAt: realUpload},
	} {
		manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
			WithInspector(inspectorStub{result: Package{
				PatchID: seeded.patchID, SourceVersion: "2.2-104", TargetVersion: seeded.target,
				Architecture: "x86_64", SignatureVerified: true, RollbackAvailable: true, Rolling: true,
				BootstrapAvailable: true, BootstrapProtocol: 1,
			}}),
			WithHelper(&helperStub{}),
			WithClock(func() time.Time { return seeded.uploadedAt }))
		if _, err := manager.Upload(context.Background(), seeded.patchID+".cgupgrade", strings.NewReader("signed package")); err != nil {
			t.Fatal(err)
		}
		if seeded.jobStatus != "" {
			writeJobForTest(t, root, Job{
				PatchID: seeded.patchID, Mode: ModeExecute, Status: seeded.jobStatus,
				UpdatedAt: seeded.uploadedAt, FinishedAt: seeded.uploadedAt,
			})
		}
	}

	reader := NewManager(Config{RootDirectory: root, TrustKeyPath: trust}, WithHelper(&helperStub{}),
		WithClock(func() time.Time { return now }))
	snapshot := reader.Snapshot(context.Background())
	if len(snapshot.Packages) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	head := snapshot.Packages[0]
	if head.Package.PatchID != "HF-2026-0928-06" || !head.ClockSkew {
		t.Fatalf("a future-stamped record must stay at the head and be flagged: %+v", head)
	}
	if head.Job == nil || head.Job.Status != StatusSucceeded {
		t.Fatalf("the head record must carry its finished job: %+v", head.Job)
	}
	if snapshot.Packages[1].Package.PatchID != "cgupgrade-2.2-104-to-2.2-105-x86_64" || snapshot.Packages[1].ClockSkew {
		t.Fatalf("the freshly uploaded package must be second and untouched: %+v", snapshot.Packages[1])
	}
}

// The baseline rule itself was the one part of the 2026-09-29 field incident that
// already worked: the updater refused a 2.2-105 hotfix on a 2.2-104 cluster and
// said exactly why. What failed was that nothing upstream knew the rule, so the
// refused package still occupied the console's only action slot. This pins the
// shared predicate to the updater's own two cases (scripts/clusterguard-upgrade.sh,
// run_hotfix_update and run_rolling_update) using the values the site really held.
func TestPackageBaselineFaultMirrorsTheUpdaterGuard(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		kind      string
		source    string
		target    string
		current   string
		wantFault bool
	}{
		{
			name: "the field incident: a 2.2-105 hotfix on a 2.2-104 cluster is refused",
			kind: PackageKindHotfix, source: "2.2-105", target: "2.2-105+hf-2026-0929-04", current: "2.2-104",
			wantFault: true,
		},
		{
			name: "the same hotfix is accepted once the cluster reaches 2.2-105",
			kind: PackageKindHotfix, source: "2.2-105", target: "2.2-105+hf-2026-0929-04", current: "2.2-105",
		},
		{
			name: "the hotfix the site did apply matched the line it was installed on",
			kind: PackageKindHotfix, source: "2.2-104", target: "2.2-104+hf-2026-0928-06", current: "2.2-104",
		},
		{
			name: "a rolling upgrade is accepted on the line it upgrades from",
			kind: PackageKindUpgrade, source: "2.2-104", target: "2.2-105", current: "2.2-104",
		},
		{
			name: "a rolling upgrade stays acceptable once a node reached the target",
			kind: PackageKindUpgrade, source: "2.2-104", target: "2.2-105", current: "2.2-105",
		},
		{
			name: "a rolling upgrade two lines behind is refused",
			kind: PackageKindUpgrade, source: "2.2-103", target: "2.2-104", current: "2.2-105",
			wantFault: true,
		},
		{
			name: "a hotfix from the line below is refused as well",
			kind: PackageKindHotfix, source: "2.2-104", target: "2.2-104+hf", current: "2.2-105",
			wantFault: true,
		},
		{
			name: "an unknown running release abstains rather than blocking legitimate uploads",
			kind: PackageKindHotfix, source: "2.2-105", target: "2.2-105+hf", current: "",
		},
		{
			name: "a package that reports no baseline abstains",
			kind: PackageKindHotfix, source: "", target: "2.2-105+hf", current: "2.2-104",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fault := packageBaselineFault(testCase.kind, testCase.source, testCase.target, testCase.current)
			if (fault != "") != testCase.wantFault {
				t.Fatalf("fault=%q wantFault=%v", fault, testCase.wantFault)
			}
			if !testCase.wantFault {
				return
			}
			// An operator cannot act on "incompatible": the refusal has to name the
			// line the package belongs to and the line the cluster is actually on,
			// otherwise it repeats the fault the message text was meant to fix.
			if !strings.Contains(fault, testCase.source) {
				t.Fatalf("refusal must name the package line: %q", fault)
			}
			if testCase.current != "" && !strings.Contains(fault, testCase.current) {
				t.Fatalf("refusal must name the running line: %q", fault)
			}
		})
	}
}

// Refusing at the door only helps if it leaves nothing behind. A stored record
// owns the console's single action slot, is ordered by upload time and is treated
// as retryable after a failure, so a refused package that still got stored would
// reproduce the very deadlock this guard exists to prevent.
func TestUploadRefusesAPackageFromAnotherReleaseLine(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	patchID := "HF-2026-0929-04"
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust, CurrentRelease: "2.2-104"},
		WithInspector(inspectorStub{result: Package{
			PatchID: patchID, Kind: PackageKindHotfix, SourceVersion: "2.2-105",
			TargetVersion: "2.2-105+hf-2026-0929-04", Architecture: "x86_64",
			SignatureVerified: true, RollbackAvailable: true, Rolling: true,
		}}),
		WithHelper(&helperStub{}))
	_, err := manager.Upload(context.Background(), patchID+".cgpatch", strings.NewReader("signed package"))
	if !errors.Is(err, ErrPackageBaselineMismatch) {
		t.Fatalf("a package from another release line must be refused, got %v", err)
	}
	for _, want := range []string{"2.2-105", "2.2-104"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q so the operator learns which upgrade comes first: %v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, patchID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused package must leave no record behind: %v", statErr)
	}
	if snapshot := manager.Snapshot(context.Background()); len(snapshot.Packages) != 0 {
		t.Fatalf("a refused package must not appear in the snapshot: %+v", snapshot.Packages)
	}
}

// Records that already carry a foreign baseline - uploaded before this guard, or
// accepted while the cluster sat on another line - must be annotated rather than
// hidden: the operator has to see why the package is listed but will not run.
func TestSnapshotMarksAPackageFromAnotherReleaseLine(t *testing.T) {
	now := time.Date(2026, 9, 29, 6, 2, 27, 0, time.UTC)
	root := t.TempDir()
	trust := filepath.Join(root, "public.pem")
	if err := os.WriteFile(trust, []byte("public"), 0o640); err != nil {
		t.Fatal(err)
	}
	foreign := "HF-2026-0929-04"
	legitimate := "cgupgrade-2.2-104-to-2.2-105-x86_64"
	for _, seeded := range []struct {
		patchID    string
		kind       string
		source     string
		target     string
		uploadedAt time.Time
	}{
		{foreign, PackageKindHotfix, "2.2-105", "2.2-105+hf-2026-0929-04", now},
		{legitimate, PackageKindUpgrade, "2.2-104", "2.2-105", now.Add(-46 * time.Minute)},
	} {
		patchID, kind, source, target := seeded.patchID, seeded.kind, seeded.source, seeded.target
		uploadedAt := seeded.uploadedAt
		writer := NewManager(Config{RootDirectory: root, TrustKeyPath: trust},
			WithInspector(inspectorStub{result: Package{
				PatchID: patchID, Kind: kind, SourceVersion: source, TargetVersion: target,
				Architecture: "x86_64", SignatureVerified: true, RollbackAvailable: true, Rolling: true,
				BootstrapAvailable: true, BootstrapProtocol: 1,
			}}),
			WithHelper(&helperStub{}),
			WithClock(func() time.Time { return uploadedAt }))
		if _, err := writer.Upload(context.Background(), patchID+".cgpatch", strings.NewReader("signed package")); err != nil {
			t.Fatal(err)
		}
	}

	reader := NewManager(Config{RootDirectory: root, TrustKeyPath: trust, CurrentRelease: "2.2-104"},
		WithHelper(&helperStub{}), WithClock(func() time.Time { return now }))
	snapshot := reader.Snapshot(context.Background())
	if len(snapshot.Packages) != 2 {
		t.Fatalf("the foreign record must stay visible, not be hidden: %+v", snapshot.Packages)
	}
	head := snapshot.Packages[0]
	if head.Package.PatchID != foreign {
		t.Fatalf("the newest upload still sorts first: %+v", head.Package)
	}
	if !head.Incompatible {
		t.Fatalf("the foreign record must be marked incompatible: %+v", head)
	}
	for _, want := range []string{"2.2-105", "2.2-104"} {
		if !strings.Contains(head.IncompatibleReason, want) {
			t.Fatalf("the reason must name %q: %q", want, head.IncompatibleReason)
		}
	}
	if snapshot.Packages[1].Incompatible || snapshot.Packages[1].IncompatibleReason != "" {
		t.Fatalf("a rolling package that matches this line must not be marked: %+v", snapshot.Packages[1])
	}
}

// A guard only tests exercise reads as present while doing nothing in production.
// The control plane has exactly one construction site and it must stamp the
// release this binary was built for; without it the guard silently abstains.
func TestProductionWiresTheRunningReleaseIntoTheSoftwareUpdateManager(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "runtime", "runtime.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "platformupdate.Config{CurrentRelease: platformupdate.CurrentRelease()}") {
		t.Fatal("internal/runtime must construct the software update manager with the release this binary runs, or the upload baseline guard silently abstains in production")
	}
}
