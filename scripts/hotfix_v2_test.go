package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHotfixRollbackAndRetryCompleteTheirRuntimeChecks(t *testing.T) {
	source, err := os.ReadFile("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	flow := shellFunctionBody(t, string(source), "run_hotfix_update") + "\n}\n"
	probe := `set -eo pipefail
source_version=2.2-105; target_version=2.2-105+hf-test; patch_id=HF-TEST
patch_file=/tmp/unused; patch_root=/tmp; work_dir=/tmp; remote_stage=/tmp/stage
execute=true; assume_yes=true; resume_requested=false; retry_requested=true; rollback_requested="$1"; release_ok="$2"; ready_ok="$3"
current_patch_maintenance_active=false; retain_update_locks=false; update_locks_acquired=false; replicated_gate_active=false
release_calls=0; restart_calls=0; ready_calls=0; journal_statuses=''
controllers=(node1 node2 node3); data_nodes=(); all_nodes=(node1 node2 node3); leader_host=node3
load_nodes() { :; }; configure_passwords() { :; }; configure_known_hosts() { :; }
load_runtime_data_members() { :; }; log_all_node_service_facts() { :; }
detect_current_update_lock() { :; }; detect_foreign_update_lock() { :; }
remote_package_version() { printf '2.2-105\n'; }
capture_operation_versions() { :; } # Version observations have their own real-shell regression.
write_hotfix_state_probe() { :; }; publish_update_artifacts() { :; }; sha256_file() { printf 'deadbeef\n'; }
jq() { printf 'clusterguard-ha.service\n'; }
verify_cluster_idle() { :; }; wait_cluster_idle() { :; }; resolve_leader_host() { :; }
log() { :; }; die() { printf 'DIE %s journal=%s gate=%s\n' "$*" "$journal_statuses" "$replicated_gate_active"; exit 13; }
write_journal() { journal_statuses="${journal_statuses},$1"; }
acquire_update_locks() { retain_update_locks=true; update_locks_acquired=true; }
all_controllers_support_replicated_gate() { return 0; }
acquire_replicated_update_gate() { replicated_gate_active=true; }
assert_update_lock_ownership() { :; }
finish_update_maintenance() {
  [[ "$release_ok" == true ]] || return 1
  release_calls=$((release_calls+1)); replicated_gate_active=false; retain_update_locks=false
}
rollback_hotfix_nodes() { return 0; }; stage_hotfix_on_node() { return 0; }
hotfix_state_on() { printf 'matched=1 total=1 mismatched=\n'; }
apply_hotfix_on_node() { return 0; }; assert_hotfix_digests() { :; }
restart_hotfix_units() { restart_calls=$((restart_calls+1)); }
wait_node_ready() { ready_calls=$((ready_calls+1)); [[ "$ready_ok" == true ]]; }
hotfix_field() { sed -n "s/.*$2=\\([0-9][0-9]*\\).*/\\1/p" <<<"$1" | head -n 1; }
__FLOW__
run_hotfix_update
printf 'journal=%s gate=%s retain=%s releases=%s restarts=%s ready=%s\n' "$journal_statuses" "$replicated_gate_active" "$retain_update_locks" "$release_calls" "$restart_calls" "$ready_calls"
`
	path := filepath.Join(t.TempDir(), "flow.sh")
	if err := os.WriteFile(path, []byte(strings.Replace(probe, "__FLOW__", flow, 1)), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rollback, release, ready, expected string
		fail                                     bool
	}{
		{"controlled rollback releases both gates", "true", "true", "true", "journal=,running,rolled_back gate=false retain=false releases=1", false},
		{"release failure keeps the gate", "true", "false", "true", "rollback_lock_release_failed gate=true", true},
		{"digest match still restarts and checks readiness", "false", "true", "true", "restarts=3 ready=3", false},
		{"failed retry readiness keeps the gate", "false", "true", "false", "failed gate=true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := exec.Command("bash", path, tc.rollback, tc.release, tc.ready).CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(output), tc.expected) {
				t.Fatalf("exit=%v output=%s; want %q fail=%v", err, output, tc.expected, tc.fail)
			}
		})
	}
}

func TestGeneratedHotfixRollbackFailsClosedOnIncompleteBackup(t *testing.T) {
	node := requireNode(t)
	source, err := os.ReadFile("build-hotfix-patch.sh")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(source), "cat >\"${render_js}\" <<'RENDER_JS'\n", 2)
	if len(parts) != 2 {
		t.Fatal("render-scripts heredoc missing")
	}
	renderer := strings.SplitN(parts[1], "\nRENDER_JS", 2)[0]
	dir := t.TempDir()
	renderPath, manifestPath := filepath.Join(dir, "render.cjs"), filepath.Join(dir, "manifest.json")
	for path, data := range map[string]string{
		renderPath:   renderer,
		manifestPath: `{"hotfix_id":"HF-AUDIT","build_commit":"1234567890abcdef","fix_commits":[],"source":{"version":"2.2","release":"105"},"files":[{"artifact":"payload/bin/example","install_path":"/tmp/example","mode":"0755","owner":"root","group":"root"}],"verification":[]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rollbackPath := filepath.Join(dir, "rollback.sh")
	if output, err := exec.Command(node, renderPath, manifestPath, "", filepath.Join(dir, "apply.sh"), rollbackPath).CombinedOutput(); err != nil {
		t.Fatalf("render: %v: %s", err, output)
	}
	generated, err := os.ReadFile(rollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	restore := shellFunctionBody(t, string(generated), "restore_backup") + "\n}\n"
	probe := `set -eo pipefail
dir="$(mktemp -d)"; trap 'rm -rf "$dir"' EXIT
backup_list="$dir/backup-1.txt"; backup_lists=( "$backup_list" )
__RESTORE__
printf 'original\n' >"$dir/live"
printf '# package_id=HF-AUDIT\n' >"$backup_list"
if restore_backup "$dir/live"; then exit 21; fi
[[ "$(cat "$dir/live")" == original ]] || exit 22
printf '%s\t%s\n' "$dir/live" "$dir/missing" >>"$backup_list"
if restore_backup "$dir/live"; then exit 23; fi
[[ "$(cat "$dir/live")" == original ]] || exit 24
printf 'original\n' >"$dir/original-backup"
printf '# package_id=HF-AUDIT\n%s\t%s\n' "$dir/live" "$dir/original-backup" >"$backup_list"
printf 'patched\n' >"$dir/live"; printf 'patched\n' >"$dir/retry-backup"
printf '# package_id=HF-AUDIT\n%s\t%s\n' "$dir/live" "$dir/retry-backup" >"$dir/backup-2.txt"
backup_lists=( "$backup_list" "$dir/backup-2.txt" )
restore_backup "$dir/live"
[[ "$(cat "$dir/live")" == original ]] || exit 25
printf 'rollback safety probe passed\n'
`
	probePath := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probePath, []byte(strings.Replace(probe, "__RESTORE__", restore, 1)), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("bash", probePath).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "rollback safety probe passed") {
		t.Fatalf("rollback probe: %v: %s", err, output)
	}
}
