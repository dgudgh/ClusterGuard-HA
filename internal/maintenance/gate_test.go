package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileGateAllowsAbsentMarkerAndBlocksActiveMaintenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-maintenance.json")
	gate := NewFileGate(path)
	if err := gate.Check(context.Background()); err != nil {
		t.Fatalf("absent marker blocked mutations: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"patch_id":"patch-1"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := gate.Check(context.Background()); !errors.Is(err, ErrActive) {
		t.Fatalf("active marker error=%v, want ErrActive", err)
	}
}

func TestFileGateFailsClosedForUnsafeMarkerAndContextCancellation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("active"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "marker")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := NewFileGate(link).Check(context.Background()); err == nil {
		t.Fatal("symlink marker failed open")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewFileGate(filepath.Join(root, "absent")).Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check error=%v", err)
	}
}
