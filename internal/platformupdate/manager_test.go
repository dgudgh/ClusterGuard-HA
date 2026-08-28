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

func (stub inspectorStub) Inspect(context.Context, string, string) (Package, error) {
	return stub.result, stub.err
}

type helperStub struct {
	readyErr error
	started  []Mode
}

func (stub *helperStub) Ready(context.Context) error { return stub.readyErr }

func (stub *helperStub) Start(_ context.Context, mode Mode, _ string) error {
	stub.started = append(stub.started, mode)
	return nil
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
	snapshot := manager.Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Packages) != 1 || snapshot.Packages[0].Package.PatchID != result.PatchID {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
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
	if err := os.WriteFile(filepath.Join(jobDir, eventsFileName), []byte("{\"status\":\"updating\",\"node\":\"node-2\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, found := manager.Job(patchID)
	if !found || len(job.OutputTail) != 2 || len(job.Events) != 1 || job.Events[0].Node != "node-2" {
		t.Fatalf("unexpected job evidence: found=%t job=%+v", found, job)
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
