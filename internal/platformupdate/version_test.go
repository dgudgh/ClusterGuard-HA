package platformupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHotfixHistoryReadsItsOwnSignedVersionWithoutChangingBaselineOrRecords(t *testing.T) {
	manager, _, id := preparedHotfixManager(t)
	original, _ := manager.Package(id)
	inspector := &countingInspector{result: original}
	inspector.result.PatchVersion = "3.1.1.1"
	manager.inspector = inspector
	path := filepath.Join(manager.config.RootDirectory, id, packageFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		snapshot := manager.Snapshot(context.Background())
		if len(snapshot.Packages) != 1 {
			t.Fatalf("snapshot=%+v", snapshot)
		}
		got := snapshot.Packages[0].Package
		if got.PatchVersion != "3.1.1.1" || got.SourceVersion != original.SourceVersion || got.TargetVersion != original.TargetVersion || snapshot.Packages[0].Incompatible {
			t.Fatalf("version projection changed baseline: %+v", snapshot)
		}
	}
	if inspector.calls != 1 {
		t.Fatalf("verified archive inspected %d times", inspector.calls)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("history metadata was overwritten")
	}
	// A new process reads the same signed archive, never a directory name or upload order.
	reopened := NewManager(manager.config, WithInspector(inspector), WithHelper(&helperStub{}))
	if got := reopened.Snapshot(context.Background()).Packages[0].Package.PatchVersion; got != "3.1.1.1" {
		t.Fatalf("after restart=%q", got)
	}
	if inspector.calls != 2 {
		t.Fatal("restart trusted a stale in-memory verdict")
	}
	// An archive replacement must invalidate the cached version and preserve history.
	if err := os.WriteFile(filepath.Join(manager.config.RootDirectory, id, patchFileName), []byte("replaced archive"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot(context.Background()).Packages[0].Package.PatchVersion; got != "" {
		t.Fatalf("unverified version=%q", got)
	}
}

func TestHotfixHistoryRejectsUntrustedOrMismatchedVersionSources(t *testing.T) {
	for _, kind := range []string{"legacy", "unsigned", "identity", "baseline", "invalid", "inspector-error", "trust-replaced"} {
		t.Run(kind, func(t *testing.T) {
			manager, _, id := preparedHotfixManager(t)
			original, _ := manager.Package(id)
			inspected := original
			inspected.PatchVersion = "3.1.1.1"
			var inspectErr error
			switch kind {
			case "legacy":
				inspected.PatchVersion = ""
			case "unsigned":
				inspected.SignatureVerified = false
			case "identity":
				inspected.PatchID = "different"
			case "baseline":
				inspected.SourceVersion = "2.2-103"
			case "invalid":
				inspected.PatchVersion = "3.1.1.0"
			case "inspector-error":
				inspectErr = errors.New("signature verification failed")
			case "trust-replaced":
				manager.inspector = inspectorStub{result: inspected}
				if manager.Snapshot(context.Background()).Packages[0].Package.PatchVersion != "3.1.1.1" {
					t.Fatal("initial verification failed")
				}
				if err := os.WriteFile(manager.config.TrustKeyPath, []byte("different trust"), 0o640); err != nil {
					t.Fatal(err)
				}
				inspectErr = errors.New("new key rejects signature")
			}
			manager.inspector = inspectorStub{result: inspected, err: inspectErr}
			got := manager.Snapshot(context.Background()).Packages[0].Package
			if got.PatchVersion != "" || got.PatchID != id {
				t.Fatalf("untrusted version or lost history: %+v", got)
			}
		})
	}
}
