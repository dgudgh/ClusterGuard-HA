package platformupdate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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
