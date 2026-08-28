package platformupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandInspectorParsesVerifiedBootstrapContract(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "upgrader")
	contents := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' 'signature=verified' 'patch_id=cg-2.2-1-to-2.2-2' 'source=2.2-1' 'target=2.2-2' 'architecture=x86_64' 'rollback=available' 'rolling=true' 'database_mutation=false' 'bootstrap=available' 'bootstrap_protocol=1'\n"
	if err := os.WriteFile(binary, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := (CommandInspector{UpgradeBinaryPath: binary}).Inspect(context.Background(), "release.cgupgrade", "public.pem")
	if err != nil {
		t.Fatal(err)
	}
	if !result.BootstrapAvailable || result.BootstrapProtocol != 1 {
		t.Fatalf("bootstrap contract was not parsed: %+v", result)
	}
}

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
