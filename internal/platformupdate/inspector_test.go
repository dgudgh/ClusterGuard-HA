package platformupdate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandInspectorReportsExecutionFailureWithoutCommandOutput(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "missing-upgrader")
	_, err := (CommandInspector{UpgradeBinaryPath: binary}).Inspect(
		context.Background(), "release.cgupgrade", "public.pem",
	)
	if err == nil {
		t.Fatal("expected inspector execution failure")
	}
	message := err.Error()
	if !strings.Contains(message, "signed update package inspection failed:") ||
		!strings.Contains(message, "missing-upgrader") {
		t.Fatalf("execution failure lost its diagnostic detail: %q", message)
	}
}
