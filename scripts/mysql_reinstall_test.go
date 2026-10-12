package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the actual pre-extraction program, using a fake systemd service that
// still owns RuntimeDirectory until disabled. Any rm before quiescence fails.
func TestMySQLReinstallRetiresOnlyItsStoppedManagedUnit(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "\nquiesce_managed_mysql_service() {")
	if start < 0 {
		start = strings.Index(text, "\nmanaged_evidence=false\n")
	}
	end := strings.Index(text, "\ntar --no-same-owner --no-same-permissions -xf")
	if start < 0 || end <= start {
		t.Fatal("cannot locate reinstall preparation program")
	}
	program := text[start:end]
	for _, tc := range []struct {
		name         string
		mode         string
		initialized  bool
		partial      bool
		rebuild      bool
		wantSuccess  bool
		wantDisabled bool
	}{
		{name: "fresh", mode: "absent", wantSuccess: true},
		{name: "interrupted-empty", mode: "managed", wantSuccess: true, wantDisabled: true},
		{name: "interrupted-partial", mode: "managed", partial: true, wantSuccess: true, wantDisabled: true},
		{name: "stop-failed", mode: "stop-failed", partial: true},
		{name: "still-running", mode: "still-running", partial: true, wantDisabled: true},
		{name: "leftover-process", mode: "leftover-process", partial: true, wantDisabled: true},
		{name: "unrecognized-unit", mode: "unrecognized", partial: true},
		{name: "overridden-unit", mode: "overridden", partial: true},
		{name: "different-data-root", mode: "different-data-root", partial: true},
		{name: "symlink-unit", mode: "symlink", partial: true},
		{name: "initialized-data-preserved", mode: "managed", initialized: true},
		{name: "explicit-managed-rebuild", mode: "managed", initialized: true, rebuild: true, wantSuccess: true, wantDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "state", "mysql", "3306", "data")
			writeFile(t, filepath.Join(root, "software", "sentinel"), "old binary", 0600)
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.partial || tc.initialized {
				writeFile(t, filepath.Join(data, "sentinel"), "database bytes", 0600)
			}
			if tc.initialized {
				if err := os.Mkdir(filepath.Join(data, "mysql"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("bash", "-c", `
set -euo pipefail
port=3306
install_root="$CG_TEST_ROOT"
install_parent="$CG_TEST_ROOT/state/mysql/3306"
data_dir="$install_parent/data"
log_dir="$CG_TEST_ROOT/log"
run_dir="$CG_TEST_ROOT/run"
service=clusterguard-mysql-3306.service
service_file="$CG_TEST_ROOT/unit.service"
config_file="$CG_TEST_ROOT/3306.cnf"
managed_marker="$install_parent/.clusterguard-managed"
installing_marker="$install_parent/.installing"
managed_mysqld="$CG_TEST_ROOT/software/bin/mysqld"
rebuild="$CG_TEST_REBUILD"
if [[ "$CG_TEST_MODE" != absent ]]; then
 printf 'Description=ClusterGuard managed MySQL 3306\nExecStart=%s --defaults-file=%s\n' "$managed_mysqld" "$config_file" > "$service_file"
 printf 'datadir=%s\n' "$data_dir" > "$config_file"
 case "$CG_TEST_MODE" in
  unrecognized) printf 'Description=Customer database\n' > "$service_file" ;;
  different-data-root) printf 'datadir=/customer/database\n' > "$config_file" ;;
  symlink) mv "$service_file" "$service_file.real"; ln -s "$service_file.real" "$service_file" ;;
 esac
fi
systemctl() {
 printf '%s\n' "$*" >> "$CG_TEST_ROOT/systemctl.log"
 [[ "${*: -1}" == "$service" ]] || exit 95
 case "$1" in
 show)
  case "$2" in
   --property=LoadState) [[ "$CG_TEST_MODE" == absent ]] && printf 'not-found\n' || printf 'loaded\n' ;;
   --property=FragmentPath) printf '%s\n' "$service_file" ;;
   --property=DropInPaths) [[ "$CG_TEST_MODE" != overridden ]] || printf '/etc/systemd/system/custom.conf\n' ;;
   --property=ActiveState) [[ "$CG_TEST_MODE" == still-running ]] && printf 'activating\n' || printf 'inactive\n' ;;
   --property=MainPID) [[ "$CG_TEST_MODE" == still-running || "$CG_TEST_MODE" == leftover-process ]] && printf '42\n' || printf '0\n' ;;
   *) exit 96 ;;
  esac
  return 0 ;;
 disable)
  [[ "$2" == --now ]] || exit 97
  [[ "$CG_TEST_MODE" != stop-failed ]] || return 1
  printf 'disabled\n' > "$CG_TEST_ROOT/disabled"
  /bin/rm -rf "$run_dir"
  return 0 ;;
 stop) return 1 ;;
 *) exit 98 ;;
 esac
}
rm() {
 printf 'rm attempted\n' >> "$CG_TEST_ROOT/rm-attempted"
 if [[ "$CG_TEST_MODE" != absent && ! -f "$CG_TEST_ROOT/disabled" ]]; then
  echo 'directory removal raced an active restart job' >&2
  exit 92
 fi
 /bin/rm "$@"
}
`+program+`
[[ -d "$run_dir" && -d "$install_root/software" ]]
`)
			rebuild := "false"
			if tc.rebuild {
				rebuild = "true"
			}
			command.Env = append(os.Environ(), "CG_TEST_ROOT="+root, "CG_TEST_MODE="+tc.mode, "CG_TEST_REBUILD="+rebuild)
			output, err := command.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%v want=%v: %v\n%s", err == nil, tc.wantSuccess, err, output)
			}
			_, disabledErr := os.Stat(filepath.Join(root, "disabled"))
			if (disabledErr == nil) != tc.wantDisabled {
				t.Fatalf("disabled=%v want=%v: %s", disabledErr == nil, tc.wantDisabled, output)
			}
			if !tc.wantSuccess {
				if _, e := os.Stat(filepath.Join(root, "rm-attempted")); e == nil {
					t.Fatal("rejected preparation attempted to remove directories")
				}
				oldSoftware, e := os.ReadFile(filepath.Join(root, "software", "sentinel"))
				if e != nil || string(oldSoftware) != "old binary" {
					t.Fatalf("software changed despite rejection: %v", e)
				}
				if tc.partial || tc.initialized {
					bytes, e := os.ReadFile(filepath.Join(data, "sentinel"))
					if e != nil || string(bytes) != "database bytes" {
						t.Fatalf("data changed despite rejection: %v", e)
					}
				}
			}
			if tc.wantDisabled {
				log, e := os.ReadFile(filepath.Join(root, "systemctl.log"))
				if e != nil || !strings.Contains(string(log), "disable --now clusterguard-mysql-3306.service") || strings.Contains(string(log), "3307") {
					t.Fatalf("incorrect retirement scope: %s (%v)", log, e)
				}
			}
		})
	}
}
