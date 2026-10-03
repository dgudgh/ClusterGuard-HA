package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute scripts with a real compiled contract consumer, not a stub that
// always passes. The standalone runner must refuse missing/old consumers.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cg-contract-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	helper := filepath.Join(dir, "clusterguard-update-helper")
	command := exec.Command("go", "build", "-o", helper, "../cmd/clusterguard-update-helper")
	if output, err := command.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, string(output), err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	os.Setenv("CG_UPDATE_CONTRACT_HELPER", helper)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRunnerRefusesMissingContractBeforeInspect(t *testing.T) {
	command := exec.Command("bash", "clusterguard-upgrade.sh", "--patch", "missing.cgpatch", "--trust-key", "missing.pem", "--inspect")
	command.Env = append(os.Environ(), "CG_UPDATE_CONTRACT_HELPER=/missing/consumer")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "CG_CONTRACT_UNAVAILABLE") {
		t.Fatalf("missing contract not refused: %s, %v", output, err)
	}
}
