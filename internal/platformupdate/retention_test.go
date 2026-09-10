package platformupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrunedPackageRetainsAuditAndAcceptsIdenticalVerifiedReupload(t *testing.T) {
	root := t.TempDir()
	trust := filepath.Join(root, "trust.pem")
	if err := os.WriteFile(trust, []byte("public"), 0640); err != nil {
		t.Fatal(err)
	}
	help := &helperStub{}
	manager := NewManager(Config{RootDirectory: root, TrustKeyPath: trust}, WithHelper(help), WithInspector(inspectorStub{result: Package{PatchID: "cg-retention", SourceVersion: "2.2-78", TargetVersion: "2.2-79", Architecture: "x86_64", SignatureVerified: true, Rolling: true, RollbackAvailable: true, BootstrapAvailable: true, BootstrapProtocol: 1}}))
	p, err := manager.Upload(context.Background(), "release.cgupgrade", strings.NewReader("signed fixture"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, p.PatchID)
	if err := writeJSONAtomic(filepath.Join(dir, jobFileName), Job{PatchID: p.PatchID, Status: StatusSucceeded, Message: "audit survives retention"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, patchFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts-pruned"), []byte("retained audit"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot(context.Background())
	if len(snapshot.Packages) != 1 || !snapshot.Packages[0].Package.ArtifactsPruned || snapshot.Packages[0].Job == nil || snapshot.Packages[0].Job.Status != StatusSucceeded {
		t.Fatalf("lost historical upgrade: %+v", snapshot)
	}
	if _, err := manager.Start(context.Background(), ModePlan, p.PatchID, ""); !errors.Is(err, ErrPackagePruned) {
		t.Fatalf("pruned upgrade was executable: %v", err)
	}
	restored, err := manager.Upload(context.Background(), "release.cgupgrade", strings.NewReader("signed fixture"))
	if err != nil || restored.ArtifactsPruned || !restored.UploadedAt.Equal(p.UploadedAt) {
		t.Fatalf("identical verified reupload failed: %+v %v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(dir, patchFileName)); err != nil {
		t.Fatal(err)
	}
	job, found := manager.Job(p.PatchID)
	if !found || job.Status != StatusSucceeded || job.Message != "audit survives retention" {
		t.Fatalf("reupload changed historical job: %+v", job)
	}
	if len(help.started) != 0 {
		t.Fatal("retention or reupload executed an upgrade")
	}
}
