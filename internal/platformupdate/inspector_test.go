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

func TestCommandInspectorParsesHotfixKind(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "upgrader")
	contents := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' 'signature=verified' 'kind=hotfix' 'patch_id=HF-2026-0928-05' 'source=2.2-104' 'target=2.2-104+hf-2026-0928-05' 'architecture=x86_64' 'rollback=available' 'rolling=true' 'database_mutation=false' 'bootstrap=unavailable' 'bootstrap_protocol=0'\n"
	if err := os.WriteFile(binary, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := (CommandInspector{UpgradeBinaryPath: binary}).Inspect(context.Background(), "hotfix.cgpatch", "public.pem")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != PackageKindHotfix {
		t.Fatalf("hotfix kind was not parsed: %+v", result)
	}
	if result.TargetVersion != "2.2-104+hf-2026-0928-05" || !result.Rolling || !result.RollbackAvailable {
		t.Fatalf("hotfix contract was not parsed: %+v", result)
	}
}

// A controller that has not yet received the fix still runs an upgrader that
// never prints a kind. Those releases only ever produced rolling packages, so an
// absent value must not make an otherwise valid package look unknown.
func TestCommandInspectorTreatsAnAbsentKindAsARollingUpgrade(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "upgrader")
	contents := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' 'signature=verified' 'patch_id=cg-2.2-103-to-2.2-104' 'source=2.2-103' 'target=2.2-104' 'architecture=x86_64' 'rollback=available' 'rolling=true' 'database_mutation=false'\n"
	if err := os.WriteFile(binary, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := (CommandInspector{UpgradeBinaryPath: binary}).Inspect(context.Background(), "release.cgupgrade", "public.pem")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != PackageKindUpgrade {
		t.Fatalf("an inspection without a kind must read as a rolling upgrade: %+v", result)
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

func TestCommandInspectorReadsOnlyValidFourPartHotfixVersion(t *testing.T) {
	for _, value := range []string{"3.1.1.1", "", "3.1.1.0", "3.1.1", "3.1.1.1-extra"} {
		t.Run(value, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "upgrader")
			contents := "#!/usr/bin/env bash\nprintf '%s\\n' 'signature=verified' 'kind=hotfix' 'patch_id=HF-version-test' 'source=2.2-105' 'target=2.2-105+hf' 'patch_version=" + value + "'\n"
			if err := os.WriteFile(binary, []byte(contents), 0o755); err != nil {
				t.Fatal(err)
			}
			result, err := (CommandInspector{UpgradeBinaryPath: binary}).Inspect(context.Background(), "hotfix.cgpatch", "public.pem")
			if value != "" && value != "3.1.1.1" {
				if err == nil {
					t.Fatalf("invalid version accepted: %+v", result)
				}
				return
			}
			if err != nil || result.PatchVersion != value || result.SourceVersion != "2.2-105" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
