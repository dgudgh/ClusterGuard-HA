package platformupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveIdentityInspector map[string]Package

func (inspector archiveIdentityInspector) Inspect(_ context.Context, archive, _ string) (Package, error) {
	result, ok := inspector[filepath.Base(filepath.Dir(archive))]
	if !ok {
		return Package{}, errors.New("signature unavailable")
	}
	return result, nil
}

func TestSnapshotRetiresFailureOnlyWithVerifiedInstalledDeclaredSuccessor(t *testing.T) {
	for _, mode := range []string{"installed", "not-installed", "rolled-back", "unsigned", "archive-replaced", "undeclared", "active-predecessor"} {
		t.Run(mode, func(t *testing.T) {
			manager, _, id := preparedHotfixManager(t)
			old, _ := manager.Package(id)
			writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, Status: StatusFailed, Mode: ModeExecute})
			next := old
			next.PatchID, next.PatchVersion, next.Supersedes = "3.1.1.4", "3.1.1.4", []string{id}
			manager.inspector = inspectorStub{result: next}
			uploaded, err := manager.Upload(context.Background(), "clusterguard-3.1.1.4.x86_64.cgpatch", strings.NewReader("signed new identity"))
			if err != nil {
				t.Fatal(err)
			}
			writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: next.PatchID, Status: StatusSucceeded, Mode: ModeExecute})
			switch mode {
			case "not-installed":
				writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: next.PatchID, Status: StatusFailed})
			case "rolled-back":
				writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: next.PatchID, Status: StatusRolledBack, Mode: ModeRollback})
			case "unsigned":
				next.SignatureVerified = false
			case "archive-replaced":
				if err := os.WriteFile(filepath.Join(manager.config.RootDirectory, next.PatchID, patchFileName), []byte("replacement"), 0o640); err != nil {
					t.Fatal(err)
				}
			case "undeclared":
				next.Supersedes = nil
			case "active-predecessor":
				writeJobForTest(t, manager.config.RootDirectory, Job{PatchID: id, Status: StatusRunning, Mode: ModeExecute})
			}
			manager.inspector = archiveIdentityInspector{id: old, next.PatchID: next}
			for _, status := range manager.Snapshot(context.Background()).Packages {
				if status.Package.PatchID != id {
					continue
				}
				want := ""
				if mode == "installed" {
					want = uploaded.PatchID
				}
				if status.SupersededBy != want {
					t.Fatalf("%s: %+v", mode, status)
				}
				if mode != "active-predecessor" && status.Job.Status != StatusFailed {
					t.Fatal("historical failure overwritten")
				}
			}
		})
	}
}
