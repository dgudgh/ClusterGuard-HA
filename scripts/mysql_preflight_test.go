package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual remote shell, before the installer's first RPM/clock/
// firewall mutation. This preflight only rejects legacy ambiguity; it cannot
// authorize initialization or replace the helper's detailed journal checks.
func TestMySQLPreflightRejectsLegacyAmbiguityBeforeMutation(t *testing.T) {
	source, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	function, ok := extractShellFunction(string(source), "remote_preflight")
	if !ok {
		t.Fatal("missing remote_preflight")
	}
	for _, tc := range []struct {
		name, engine           string
		legacy, journal, alive bool
		wantStatus             int
	}{
		{"legacy failed initialize", "mysql", true, false, false, 27},
		{"fresh instance", "mysql", false, false, false, 0},
		{"journal deferred to helper", "mysql", true, true, false, 0},
		{"running instance deferred to authentication", "mysql", true, false, true, 0},
		{"other engine", "postgresql", true, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			instance := filepath.Join(root, "mysql", "3306")
			if err := os.MkdirAll(filepath.Join(instance, "data", "mysql"), 0700); err != nil {
				t.Fatal(err)
			}
			dataFile := filepath.Join(instance, "data", "mysql", "preserved")
			if err := os.WriteFile(dataFile, []byte("original database bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.legacy {
				if err := os.WriteFile(filepath.Join(instance, ".installing"), []byte("state=installing\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.journal {
				if err := os.MkdirAll(filepath.Join(instance, "conf"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(instance, "conf", "installation-state.json"), []byte(`{"phase":"initialize-failed"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			media := filepath.Join(root, "media")
			if err := os.WriteFile(media, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			script := `set -euo pipefail
all_nodes=(fixture)
node_role_for() { echo data; }
log() { :; }
warn() { :; }
die() { echo "$*" >&2; exit 99; }
remote_exec() { bash -c "$2"; }
systemctl() { :; }
rpm() { :; }
ss() { if [[ "$TEST_ALIVE" == true ]]; then echo 'LISTEN 3306'; fi; }
flock() { :; }
df() { printf 'Filesystem 1024-blocks Used Available Capacity Mounted\nfixture 999999999 0 999999999 0 /\n'; }
uname() { echo x86_64; }
getconf() { echo 'glibc 2.28'; }
export -f systemctl rpm ss flock df uname getconf
controller_min_free_gb=1
database_min_free_gb=1
postgresql_build_min_free_gb=1
application_rpm="$TEST_MEDIA"
database_package="$TEST_MEDIA"
database_engine="$TEST_ENGINE"
database_package_kind=mysql-binary
postgresql_build_node=fixture
controller_data_root="$TEST_ROOT"
database_data_root="$TEST_ROOT"
database_port=3306
api_port=3000
raft_port=10009
` + function + "\nremote_preflight\nprintf mutation-reached > \"$TEST_ROOT/after-preflight\"\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "TEST_ROOT="+root, "TEST_MEDIA="+media, "TEST_ENGINE="+tc.engine, fmt.Sprintf("TEST_ALIVE=%t", tc.alive))
			output, err := cmd.CombinedOutput()
			status := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					status = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if status != tc.wantStatus {
				t.Fatalf("status=%d want=%d: %s", status, tc.wantStatus, output)
			}
			if status != 0 {
				if !strings.Contains(string(output), "no verified initialization journal") || !strings.Contains(string(output), "Do not delete data") {
					t.Fatalf("missing accurate data-protection diagnostic: %s", output)
				}
				if _, err := os.Stat(filepath.Join(root, "after-preflight")); !os.IsNotExist(err) {
					t.Fatal("installer reached a mutation after rejected preflight")
				}
			}
			got, err := os.ReadFile(dataFile)
			if err != nil || string(got) != "original database bytes" {
				t.Fatalf("preflight changed the original database: %q, %v", got, err)
			}
		})
	}
}
