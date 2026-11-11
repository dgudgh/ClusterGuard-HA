package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClockMeshChecksPersistentChronyBeforeMutations(t *testing.T) {
	for _, tc := range []struct {
		name, state, absent string
		checkOnly, pass     bool
	}{
		{name: "missing-daemon", state: "loaded", absent: "chronyd", checkOnly: true},
		{name: "missing-client", state: "loaded", absent: "chronyc", checkOnly: true},
		{name: "missing-unit", state: "not-found", checkOnly: true},
		{name: "masked-unit", state: "masked", checkOnly: true},
		{name: "masked-unit-state", state: "loaded-masked", checkOnly: true},
		{name: "static-unit", state: "static", checkOnly: true},
		{name: "missing-fragment", state: "loaded", absent: "fragment", checkOnly: true},
		{name: "systemd-query-failure", state: "query-failed", checkOnly: true},
		{name: "installed-enabled", state: "loaded", checkOnly: true, pass: true},
		{name: "installed-disabled", state: "loaded-disabled", checkOnly: true, pass: true},
		{name: "normal-apply-missing-unit", state: "not-found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"chronyd", "chronyc"} {
				if tc.absent != name {
					writeExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nexit 0\n")
				}
			}
			for _, name := range []string{"awk", "date", "cat"} {
				utility, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(utility, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			fragment := filepath.Join(root, "chronyd.service")
			if tc.absent != "fragment" {
				writeFile(t, fragment, "[Service]\nExecStart=/usr/sbin/chronyd\n", 0644)
			}
			writeExecutable(t, filepath.Join(bin, "timedatectl"), `#!/bin/sh
if [ "$1" = show ]; then printf 'Asia/Shanghai\n'; exit 0; fi
printf 'timezone mutation attempted\n' >> "$TEST_CLOCK_ROOT/mutations"
exit 98
`)
			writeExecutable(t, filepath.Join(bin, "systemctl"), `#!/bin/sh
if [ "$1" != show ]; then
 printf 'service mutation attempted\n' >> "$TEST_CLOCK_ROOT/mutations"
 exit 98
fi
[ "$TEST_CLOCK_STATE" != query-failed ] || exit 1
state="$TEST_CLOCK_STATE"
unit_state=enabled
if [ "$state" = loaded-disabled ]; then state=loaded; unit_state=disabled; fi
if [ "$state" = loaded-masked ]; then state=loaded; unit_state=masked; fi
if [ "$state" = static ]; then state=loaded; unit_state=static; fi
printf 'LoadState=%s\nFragmentPath=%s\nUnitFileState=%s\n' "$state" "$TEST_CLOCK_FRAGMENT" "$unit_state"
`)
			args := []string{"clusterguard-clock-mesh.sh"}
			if tc.checkOnly {
				args = append(args, "--check")
			} else {
				args = append(args, "--server", "--accept-current-time", "--timezone", "Asia/Shanghai")
			}
			bash, err := exec.LookPath("bash")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bash, args...)
			cmd.Env = append(os.Environ(), "PATH="+bin, "TEST_CLOCK_ROOT="+root, "TEST_CLOCK_STATE="+tc.state, "TEST_CLOCK_FRAGMENT="+fragment)
			out, runErr := cmd.CombinedOutput()
			if (runErr == nil) != tc.pass {
				t.Fatalf("pass=%v want=%v: %v: %s", runErr == nil, tc.pass, runErr, out)
			}
			if _, err := os.Stat(filepath.Join(root, "mutations")); !os.IsNotExist(err) {
				t.Fatalf("Chrony validation reached a mutation: %s", out)
			}
			if !tc.pass && !strings.Contains(string(out), "Chrony prerequisites") {
				t.Fatalf("missing actionable dependency diagnostic: %s", out)
			}
			if tc.pass && !strings.Contains(string(out), "Chrony prerequisites ready") {
				t.Fatalf("read-only validation did not confirm readiness: %s", out)
			}
		})
	}
}

func TestInstallerChecksEveryClockNodeBeforeApplyingSource(t *testing.T) {
	source, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	function, ok := extractShellFunction(string(source), "configure_fixed_cluster_clock_source")
	if !ok {
		t.Fatal("missing actual clock bootstrap function")
	}
	for _, failedNode := range []string{"", "n1", "n2", "n3"} {
		t.Run("failed-"+failedNode, func(t *testing.T) {
			root := t.TempDir()
			program := `set -euo pipefail
controller_nodes=(n1 n2 n3)
all_nodes=(n1 n2 n3)
valid_ipv4() { return 0; }
clock_mesh_tool() { printf '/fixture/clock-tool\n'; }
cluster_timezone_zone() { printf 'Asia/Shanghai\n'; }
log() { :; }
warn() { :; }
die() { exit 97; }
remote_copy() { :; }
remote_exec_checked() {
 case "$3" in
  *' --check')
   printf 'check:%s\n' "$1" >> "$TEST_CLOCK_ROOT/sequence"
   [[ "$1" != "$TEST_FAILED_NODE" ]] || return 1 ;;
  *' --server'*|*' --client'*)
   printf 'apply:%s\n' "$1" >> "$TEST_CLOCK_ROOT/sequence" ;;
 esac
}
` + function + "\nconfigure_fixed_cluster_clock_source\n"
			cmd := exec.Command("bash", "-c", program)
			cmd.Env = append(os.Environ(), "TEST_CLOCK_ROOT="+root, "TEST_FAILED_NODE="+failedNode)
			out, err := cmd.CombinedOutput()
			if (err == nil) != (failedNode == "") {
				t.Fatalf("wrong bootstrap result for %q: %v: %s", failedNode, err, out)
			}
			sequence, _ := os.ReadFile(filepath.Join(root, "sequence"))
			if failedNode != "" {
				if strings.Contains(string(sequence), "apply:") {
					t.Fatalf("changed a clock before all nodes were checked: %s", sequence)
				}
			} else if string(sequence) != "check:n1\ncheck:n2\ncheck:n3\napply:n1\napply:n2\napply:n3\n" {
				t.Fatalf("checks must precede every apply: %s", sequence)
			}
		})
	}
}
