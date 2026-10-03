package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateHistoryImportPreservesEvidenceAndRefusesConflicts(t *testing.T) {
	source, err := os.ReadFile("clusterguard-update-job.sh")
	if err != nil {
		t.Fatal(err)
	}
	function := shellFunctionBody(t, string(source), "import_private_history") + "\n}\n"
	for _, tc := range []struct {
		name, owner, status string
		fail                bool
	}{
		{"same attempt completed on another leader", "same-operation", "running", false},
		{"different attempt requires verification", "different-operation", "succeeded", true},
		{"conflicting terminal states are refused", "same-operation", "rolled_back", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			probe := `set -euo pipefail
private_root="$1"; patch_id=HF-TEST; job_dir="$private_root/jobs/$patch_id"; status_file="$job_dir/status.json"; jq_binary=jq
workspace_helper="$private_root/helper"
die() { printf '%s\n' "$*" >&2; exit 1; }
__FUNCTION__
import_private_history
jq -e '.status=="succeeded" and .operation_id=="same-operation"' "$status_file" >/dev/null
jq -se 'length>=2' "$job_dir/operations.jsonl" >/dev/null
printf 'history imported safely\n'
`
			writeFile(t, filepath.Join(directory, "jobs", "HF-TEST", "status.json"), `{"patch_id":"HF-TEST","operation_id":"`+tc.owner+`","status":"`+tc.status+`"}`, 0o600)
			writeFile(t, filepath.Join(directory, "jobs", "HF-TEST", "operations.jsonl"), `{"patch_id":"HF-TEST","operation_id":"`+tc.owner+`","status":"running"}`+"\n", 0o600)
			writeFile(t, filepath.Join(directory, "history", "HF-TEST", "status.json"), `{"patch_id":"HF-TEST","operation_id":"same-operation","status":"succeeded"}`, 0o600)
			writeFile(t, filepath.Join(directory, "history", "HF-TEST", "deployment.json"), `{"package_id":"HF-TEST","operation_id":"same-operation","state":"installed"}`, 0o600)
			writeFile(t, filepath.Join(directory, "history", "HF-TEST", "operations.jsonl"), `{"patch_id":"HF-TEST","operation_id":"same-operation","status":"succeeded"}`+"\n", 0o600)
			writeExecutable(t, filepath.Join(directory, "helper"), "#!/bin/bash\nset -eu\nif [[ $2 == snapshot ]]; then cp -- \"$3\" \"$4\"; fi\n")
			path := filepath.Join(directory, "probe.sh")
			writeExecutable(t, path, strings.Replace(probe, "__FUNCTION__", function, 1))
			output, err := exec.Command("bash", path, directory).CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("import: err=%v output=%s", err, output)
			}
			if tc.fail && !strings.Contains(string(output), "CG_HISTORY_OVERWRITE_FORBIDDEN") {
				t.Fatalf("missing refusal: %s", output)
			}
		})
	}
}
