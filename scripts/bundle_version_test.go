package scripts

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBundleVersionRejectsIncompleteOrUnsafeRPMIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"--rpm-version", "2.2"},
		{"--rpm-release", "99"},
		{"--rpm-version", "2.2 -X unsafe", "--rpm-release", "99"},
		{"--rpm-version", "2.2", "--rpm-release", "99;false"},
	} {
		command := exec.Command("bash", append([]string{"build-clusterguard-bundle.sh", "--version", "2.2-99"}, args...)...)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "both valid --rpm-version and --rpm-release are required") {
			t.Fatalf("arguments %q did not fail before build: %v\n%s", args, err, output)
		}
	}
}
