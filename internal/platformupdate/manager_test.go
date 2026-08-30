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
	if !found || job.Status != StatusFailed || !job.MaintenanceActive || job.AutomaticFailoverAvailable ||
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
