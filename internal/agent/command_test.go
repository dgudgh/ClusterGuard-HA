package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestOrderedRecoveryCommandKeepsOutputDeterministic(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		out, err := (orderedOutputRunner{OSCommandRunner{}}).Run(context.Background(), sh, "-c", "printf 'valid zero tail\\n' >&2; printf 'record 1\\nrecord 2\\n'; exit 1")
		if err == nil || string(out) != "record 1\nrecord 2\nvalid zero tail\n" {
			t.Fatalf("ordered recovery output=%q err=%v", out, err)
		}
	}
}

func TestOrderedRecoveryCommandRedactsFailure(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (OSCommandRunner{}).RunOrdered(context.Background(), sh, "-c", "printf 'password=fixture-secret token=fixture-token\\n' >&2; exit 1")
	if err == nil || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "fixture-token") {
		t.Fatalf("ordered command exposed a secret: %v", err)
	}
}
