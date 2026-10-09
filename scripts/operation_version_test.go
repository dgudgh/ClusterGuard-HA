package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs the real Runner and wrapper functions against three isolated nodes.
func TestOperationVersionCaptureAndPublication(t *testing.T) {
	runner, _ := os.ReadFile("clusterguard-upgrade.sh")
	wrapper, _ := os.ReadFile("clusterguard-update-job.sh")
	functions := ""
	for _, name := range []string{"capture_operation_versions", "capture_rollback_result_version", "write_journal"} {
		functions += shellFunctionBody(t, string(runner), name) + "\n}\n"
	}
	functions += shellFunctionBody(t, string(wrapper), "write_status") + "\n}\n"
	for _, tc := range []struct{ name, second, expected, rollback string }{
		{"uniform", `{"product_version":"3.1.1.7"}`, "3.1.1.7", "false"},
		{"mixed retry", `{"product_version":"3.1.1.8"}`, "", "false"},
		{"missing", `invalid json`, "", "false"},
		{"legacy actual binary", `{"version":"2.2","release":"105"}`, "", "false"},
		{"rollback does not guess backup version", `{"product_version":"3.1.1.7"}`, "3.1.1.7", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "HOTFIX-MANIFEST.json"), `{"patch_version":"3.1.1.8"}`, 0600)
			probe := `set -euo pipefail
cd "$1"
all_nodes=(one two three); package_kind=hotfix; patch_root="$PWD"
source_version=2.2-105; target_version=2.2-105+hf; rollback_requested="$4"
remote_run() { if [[ $1 == two ]]; then printf '%s' "$second_info"; else printf '{"product_version":"3.1.1.7"}'; fi; }
second_info="$2"
__FUNCTIONS__
capture_operation_versions
[[ "$observed_from_version" == "$3" ]]
[[ $(jq length <<<"$observed_node_versions") == 3 ]]
if $rollback_requested; then [[ -z "$operation_to_version" ]]; else [[ "$operation_to_version" == 3.1.1.8 ]]; fi
operation_id=operation-A; execution_id=operation-A; patch_id=3.1.1.8; update_mode=execute
journal_file="$PWD/journal.json"; journal_events_file="$PWD/events.jsonl"; progress_replication_enabled=true
publish_update_progress() { :; }
write_journal running one 'staging' staging 0 3
jq -e --arg from "$3" '.operation_id=="operation-A" and .from_version==$from and (.from_node_versions|length)==3' status.json
job_dir="$PWD"; status_file="$PWD/status.json"; jq_binary=jq; mode=execute; started_at=2026-10-09T00:00:00Z
publish_public_artifacts() { :; }
write_status succeeded done false 2026-10-09T00:01:00Z
jq -e --arg from "$3" '.from_version==$from and (.from_node_versions|length)==3' status.json
jq -se 'all(.[]; (.from_node_versions|length)==3)' operations.jsonl
# Final verified rollback records the restored version and preserves the original from observations.
write_journal rolled_back one restored rolled_back 3 3
jq -e --arg from "$3" '.from_version==$from and .to_version=="'"$(if [[ "$3" == 3.1.1.7 ]]; then printf 3.1.1.7; fi)"'"' status.json
# A different operation must not inherit the completed attempt's observations.
operation_id=operation-B; mode=plan
write_status running planning false
jq -e '.operation_id=="operation-B" and (has("from_version")|not) and (has("from_node_versions")|not)' status.json
`
			probe = strings.Replace(probe, "__FUNCTIONS__", functions, 1)
			file := filepath.Join(dir, "probe.sh")
			writeExecutable(t, file, probe)
			out, err := exec.Command("bash", file, dir, tc.second, tc.expected, tc.rollback).CombinedOutput()
			if err != nil {
				t.Fatalf("real capture/publication failed: %v\n%s", err, out)
			}
		})
	}
}
