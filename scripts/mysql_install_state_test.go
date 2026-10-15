package scripts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests execute the real shell preparation and journal functions against
// real directories. Only service/process inspection and privileged ownership
// operations are substituted; they do not represent a field installation.
// Mount inspection keeps the real guard and points its default input at an
// explicit Linux mount-table fixture, since the test host may be macOS.
func mysqlInstallTestMountTable(program string) string {
	return strings.ReplaceAll(program, `${1:-/proc/self/mountinfo}`, `${1:-${CG_TEST_ROOT}/mountinfo}`)
}

func mysqlInstallStateSource(t *testing.T) (string, string) {
	t.Helper()
	b, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "\nquiesce_managed_mysql_service() {")
	functionsEnd := strings.Index(s, "\ninstallation_node_id=")
	end := strings.Index(s, "\ntar --no-same-owner --no-same-permissions -xf")
	guardStart := strings.Index(s, "\n# Reject links before mkdir/chmod")
	guardEnd := strings.Index(s, "\ncommand -v flock")
	if start < 0 || functionsEnd <= start || end <= functionsEnd || guardStart < 0 || guardEnd <= guardStart {
		t.Fatal("cannot locate real MySQL installation journal and preparation")
	}
	return s[start:functionsEnd], s[guardStart:guardEnd] + s[start:end]
}

type mysqlInstallStateFixture struct {
	root, instance, data, config, state, software, marker, installing, log string
	functions, preparation                                                 string
}

func newMySQLInstallStateFixture(t *testing.T) *mysqlInstallStateFixture {
	t.Helper()
	functions, preparation := mysqlInstallStateSource(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance := filepath.Join(root, "mysql", "3306")
	f := &mysqlInstallStateFixture{
		root: root, instance: instance, data: filepath.Join(instance, "data"),
		config:   filepath.Join(instance, "conf", "my.cnf"),
		state:    filepath.Join(instance, "conf", "installation-state.json"),
		software: filepath.Join(root, "software"), marker: filepath.Join(instance, ".clusterguard-managed"),
		installing: filepath.Join(instance, ".installing"), log: filepath.Join(instance, "log", "error.log"),
		functions: functions, preparation: preparation,
	}
	writeFile(t, f.config, "[mysqld]\ndatadir="+f.data+"\nport=3306\nread_only=ON\nsuper_read_only=ON\n", 0600)
	writeFile(t, f.marker, "managed_by=clusterguard-ha\nport=3306\n", 0600)
	writeFile(t, f.installing, "state=installing\n", 0600)
	writeFile(t, filepath.Join(f.software, ".clusterguard-managed"), "managed_by=clusterguard-ha\nport=3306\ninstance="+instance+"\n", 0600)
	writeFile(t, filepath.Join(f.software, "sentinel"), "old software bytes\n", 0600)
	writeExecutable(t, filepath.Join(f.software, "bin", "mysqld"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(f.data, "mysql", "sentinel"), "partial system table bytes\n", 0600)
	writeFile(t, filepath.Join(instance, "binlog", "mysql-bin.000001"), "original binlog bytes\n", 0600)
	writeFile(t, filepath.Join(instance, "relaylog", "relay.000001"), "original relay bytes\n", 0600)
	writeFile(t, f.log, "native initialization failed with exit 37\n", 0600)
	writeFile(t, filepath.Join(root, "mysql.tar.xz"), "approved original MySQL package bytes\n", 0600)
	writeFile(t, filepath.Join(root, "payload.json"), `{"target":{"node_id":"node-152","mysql_cnf":"approved production template"}}`, 0600)
	writeFile(t, filepath.Join(root, "mountinfo"), "36 25 8:1 / / rw,relatime - ext4 /dev/vda1 rw\n", 0600)
	writeFile(t, filepath.Join(root, "unit.service"), "Description=ClusterGuard managed MySQL 3306\nExecStart="+filepath.Join(f.software, "bin", "mysqld")+" --defaults-file="+f.config+"\n", 0600)
	return f
}

func (f *mysqlInstallStateFixture) run(t *testing.T, body string, mode string) ([]byte, error) {
	t.Helper()
	command := exec.Command("bash", "-c", `set -euo pipefail
port=3306
data_root="$CG_TEST_ROOT"
payload="$CG_TEST_ROOT/payload.json"
package_path="$CG_TEST_ROOT/mysql.tar.xz"
install_parent="$CG_TEST_ROOT/mysql/3306"
install_root="$install_parent"
software_dir="$CG_TEST_ROOT/software"
data_dir="$install_parent/data"
config_dir="$install_parent/conf"
config_file="$config_dir/my.cnf"
installation_state="$config_dir/installation-state.json"
log_dir="$install_parent/log"
run_dir="$install_parent/run"
service=clusterguard-mysql-3306.service
service_file="$CG_TEST_ROOT/unit.service"
managed_marker="$install_parent/.clusterguard-managed"
installing_marker="$install_parent/.installing"
managed_mysqld="$software_dir/bin/mysqld"
rebuild=false
# Real device/inode identity is retained. A test user cannot chown to root, so
# root ownership is modeled only for the explicit ownership checks.
stat() {
 if [[ "$1" == -c && "$2" == %u || "$1" == -f && "$2" == %u ]]; then
  [[ "$CG_TEST_MODE" != nonroot-state || "${@: -1}" != "$installation_state" ]] || { printf '501\n'; return; }
  printf '0\n'; return
 fi
 if [[ "$CG_TEST_MODE" == separate-mount && "$1" == -c && "$2" == %d:%i && "${@: -1}" == "$install_parent" ]]; then printf '999:123\n'; return; fi
 command stat "$@"
}
chown() { :; }
systemctl() {
 printf '%s\n' "$*" >> "$CG_TEST_ROOT/systemctl.log"
 [[ "${*: -1}" == "$service" ]] || return 95
 case "$1" in
  show)
   case "$2" in
    --property=LoadState) printf 'loaded\n' ;;
    --property=FragmentPath) printf '%s\n' "$service_file" ;;
    --property=DropInPaths) [[ "$CG_TEST_MODE" != overridden ]] || printf '/customer/dropin.conf\n' ;;
    --property=ActiveState) [[ "$CG_TEST_MODE" == still-running ]] && printf 'activating\n' || printf 'inactive\n' ;;
    --property=MainPID) [[ "$CG_TEST_MODE" == stale-pid ]] && printf '42\n' || printf '0\n' ;;
    *) return 96 ;;
   esac
   return 0 ;;
  disable)
   [[ "$2" == --now ]] || return 97
   [[ "$CG_TEST_MODE" != stop-failed ]] || return 1
   : > "$CG_TEST_ROOT/disabled"
   return 0 ;;
  *) return 98 ;;
 esac
}
ps() {
 [[ "$CG_TEST_MODE" != ps-failed ]] || return 1
 if [[ "$CG_TEST_MODE" == process-on-recheck ]]; then
  count=0
  [[ ! -f "$CG_TEST_ROOT/ps-observed" ]] || count="$(cat "$CG_TEST_ROOT/ps-observed")"
  count=$((count + 1)); printf '%s\n' "$count" > "$CG_TEST_ROOT/ps-observed"
  if (( count >= 3 )); then printf 'mysqld\n'; else printf 'bash\n'; fi
  return
 fi
 [[ "$CG_TEST_MODE" == process ]] && printf 'mysqld\n' || printf 'bash\n'
}
ss() {
 [[ "$CG_TEST_MODE" != ss-failed ]] || return 1
 [[ "$CG_TEST_MODE" == listener ]] && printf 'LISTEN 0 128 *:3306 *:*\n' || :
}
mv() {
 if [[ "$CG_TEST_MODE" == quarantine-failed && "$1" == "$install_parent" ]]; then return 1; fi
 if [[ "$CG_TEST_MODE" == journal-rename-failed && "$1" == -f && "${*: -1}" == "$installation_state" ]]; then return 1; fi
 if [[ "$CG_TEST_MODE" == success-record-failed && "$1" == -f && "${*: -1}" == "$installation_state" && "$(jq -r '.phase' "$2")" == initialized ]]; then return 1; fi
 command mv "$@"
}
rm() {
 printf '%s\n' "$*" >> "$CG_TEST_ROOT/rm.log"
 command rm "$@"
}
`+mysqlInstallTestMountTable(body))
	command.Env = append(os.Environ(), "CG_TEST_ROOT="+f.root, "CG_TEST_MODE="+mode)
	return command.CombinedOutput()
}

func (f *mysqlInstallStateFixture) seedFailedJournal(t *testing.T) {
	t.Helper()
	b, err := f.run(t, f.functions+`
installation_node_id="$(jq -r '.target.node_id' "$payload")"
installation_package_sha256="$(cg_mysql_sha256 "$package_path")"
installation_template_sha256="$(cg_mysql_template_sha256)"
installation_data_identity="$(cg_mysql_data_identity "$data_dir")"
installation_fresh_empty=true
cg_mysql_write_install_state initialize-failed 37
`, "")
	if err != nil {
		t.Fatalf("write actual failed journal: %v: %s", err, b)
	}
}

func (f *mysqlInstallStateFixture) editJournal(t *testing.T, edit func(map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(f.state)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	edit(record)
	b, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func mysqlDirectorySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		key, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[key] = "link:" + target
		} else if entry.IsDir() {
			result[key] = "directory"
		} else {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[key] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertMySQLSnapshot(t *testing.T, root string, want map[string]string) {
	t.Helper()
	got := mysqlDirectorySnapshot(t, root)
	if len(got) != len(want) {
		t.Fatalf("preserved tree entry count changed: got %d want %d", len(got), len(want))
	}
	for path, bytes := range want {
		if got[path] != bytes {
			t.Fatalf("preserved path %s changed: got %q want %q", path, got[path], bytes)
		}
	}
}

func TestMySQLInstallFailedJournalPreservesWholeInstanceBeforeRetry(t *testing.T) {
	f := newMySQLInstallStateFixture(t)
	f.seedFailedJournal(t)
	before := mysqlDirectorySnapshot(t, f.instance)
	b, err := f.run(t, f.preparation, "")
	if err != nil {
		t.Fatalf("proved failed initialization could not retry: %v: %s", err, b)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(f.instance), ".3306.failed-initialize.*", "instance"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected exactly one preserved original instance, got %v: %v: %s", backups, err, b)
	}
	assertMySQLSnapshot(t, backups[0], before)
	if !strings.Contains(string(b), backups[0]) {
		t.Fatalf("preserved instance location missing from diagnostic: %s", b)
	}
	if _, err := os.Stat(filepath.Join(f.data, "mysql", "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("new data directory still contains failed system tables: %v", err)
	}
	var journal struct {
		Phase string `json:"phase"`
		Fresh bool   `json:"fresh_empty"`
	}
	b, err = os.ReadFile(f.state)
	if err != nil || json.Unmarshal(b, &journal) != nil || journal.Phase != "preparing" || !journal.Fresh {
		t.Fatalf("new attempt did not record its own empty preparation: %s (%v)", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, "rm.log")); strings.Contains(string(b), f.data) || strings.Contains(string(b), f.instance) {
		t.Fatalf("automatic retry deleted preserved database paths: %s", b)
	}
}

func TestMySQLInstallRefusesUnprovedHistoryWithoutChangingBytes(t *testing.T) {
	cases := []struct {
		name, mode string
		mutate     func(*testing.T, *mysqlInstallStateFixture)
	}{
		{name: "legacy-no-journal", mutate: func(t *testing.T, f *mysqlInstallStateFixture) { mustRemoveMySQLTestPath(t, f.state) }},
		{name: "legacy-no-managed-marker", mutate: func(t *testing.T, f *mysqlInstallStateFixture) { mustRemoveMySQLTestPath(t, f.marker) }},
		{name: "missing-installing-marker", mutate: func(t *testing.T, f *mysqlInstallStateFixture) { mustRemoveMySQLTestPath(t, f.installing) }},
		{name: "missing-error-log", mutate: func(t *testing.T, f *mysqlInstallStateFixture) { mustRemoveMySQLTestPath(t, f.log) }},
		{name: "nonroot-state", mode: "nonroot-state"},
		{name: "world-writable-state", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Chmod(f.state, 0666); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "group-writable-config", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Chmod(f.config, 0660); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config-changed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, f.config, "datadir="+f.data+"\nread_only=OFF\n", 0600)
		}},
		{name: "package-changed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.root, "mysql.tar.xz"), "different package bytes", 0600)
		}},
		{name: "template-changed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.root, "payload.json"), `{"target":{"node_id":"node-152","mysql_cnf":"different template"}}`, 0600)
		}},
		{name: "node-changed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.root, "payload.json"), `{"target":{"node_id":"node-153","mysql_cnf":"approved production template"}}`, 0600)
		}},
		{name: "later-start-wrote-log", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, f.log, "ready for connections after successful start\n", 0600)
		}},
		{name: "same-inode-data-file-changed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.data, "mysql", "sentinel"), "data changed after failed journal\n", 0600)
		}},
		{name: "same-inode-data-file-added", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.data, "new-business.ibd"), "new business bytes\n", 0600)
		}},
		{name: "same-inode-data-file-removed", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			mustRemoveMySQLTestPath(t, filepath.Join(f.data, "mysql", "sentinel"))
		}},
		{name: "data-tree-link", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Symlink(f.log, filepath.Join(f.data, "external-data")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "data-path-link", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Rename(f.data, f.data+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.data+".original", f.data); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config-directory-link", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			path := filepath.Dir(f.config)
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".original", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "data-directory-replaced", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Rename(f.data, f.data+".original"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.data, "mysql", "sentinel"), "different database", 0600)
		}},
		{name: "software-directory-replaced", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			if err := os.Rename(f.software, f.software+".original"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.software, ".clusterguard-managed"), "managed_by=clusterguard-ha\nport=3306\ninstance="+f.instance+"\n", 0600)
			writeFile(t, filepath.Join(f.software, "sentinel"), "foreign replacement software", 0600)
		}},
		{name: "invalid-json", mutate: func(t *testing.T, f *mysqlInstallStateFixture) { writeFile(t, f.state, "not json", 0600) }},
		{name: "listener", mode: "listener"}, {name: "leftover-process", mode: "process"},
		{name: "process-query-failed", mode: "ps-failed"}, {name: "listener-query-failed", mode: "ss-failed"},
		{name: "process-started-before-rename", mode: "process-on-recheck"},
		{name: "instance-is-separate-mount", mode: "separate-mount"},
		{name: "cannot-stop-service", mode: "stop-failed"}, {name: "service-still-active", mode: "still-running"},
		{name: "stale-service-pid", mode: "stale-pid"}, {name: "unit-has-dropins", mode: "overridden"},
		{name: "unknown-unit", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			writeFile(t, filepath.Join(f.root, "unit.service"), "Description=Customer database\n", 0600)
		}},
		{name: "quarantine-rename-failed", mode: "quarantine-failed"},
	}
	for _, phase := range []string{"preparing", "initializing", "initialized", "starting", "complete", "unknown"} {
		phase := phase
		cases = append(cases, struct {
			name, mode string
			mutate     func(*testing.T, *mysqlInstallStateFixture)
		}{
			name: "phase-" + phase, mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
				f.editJournal(t, func(m map[string]any) { m["phase"] = phase })
			},
		})
	}
	cases = append(cases, struct {
		name, mode string
		mutate     func(*testing.T, *mysqlInstallStateFixture)
	}{name: "empty-data-with-unknown-interrupted-state", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
		f.editJournal(t, func(m map[string]any) { m["phase"] = "initializing" })
		if err := os.RemoveAll(filepath.Join(f.data, "mysql")); err != nil {
			t.Fatal(err)
		}
	}})
	for field, value := range map[string]any{
		"schema_version": 2, "fresh_empty": false, "port": 3307, "node_id": "foreign-node", "instance": "/another/instance",
		"software_dir": "/another/software", "config_sha256": strings.Repeat("0", 64), "package_sha256": strings.Repeat("0", 64),
		"template_sha256": strings.Repeat("0", 64), "data_identity": "999:999", "error_log_sha256": strings.Repeat("0", 64),
		"data_sha256":       strings.Repeat("0", 64),
		"software_identity": "999:999",
		"native_exit":       0, "unexpected_field": true,
	} {
		field, value := field, value
		cases = append(cases, struct {
			name, mode string
			mutate     func(*testing.T, *mysqlInstallStateFixture)
		}{
			name: "journal-" + field, mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
				f.editJournal(t, func(m map[string]any) { m[field] = value })
			},
		})
	}
	for _, value := range []any{"37", 1.5, 256, -1} {
		value := value
		cases = append(cases, struct {
			name, mode string
			mutate     func(*testing.T, *mysqlInstallStateFixture)
		}{name: "invalid-native-status-" + strings.ReplaceAll(fmt.Sprint(value), ".", "-"), mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
			f.editJournal(t, func(m map[string]any) { m["native_exit"] = value })
		}})
	}
	cases = append(cases, struct {
		name, mode string
		mutate     func(*testing.T, *mysqlInstallStateFixture)
	}{name: "missing-required-field", mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
		f.editJournal(t, func(m map[string]any) { delete(m, "data_identity") })
	}})
	for _, name := range []string{"state", "config", "managed-marker", "installing-marker", "error-log"} {
		name := name
		cases = append(cases, struct {
			name, mode string
			mutate     func(*testing.T, *mysqlInstallStateFixture)
		}{
			name: "symlink-" + name, mutate: func(t *testing.T, f *mysqlInstallStateFixture) {
				path := map[string]string{"state": f.state, "config": f.config, "managed-marker": f.marker, "installing-marker": f.installing, "error-log": f.log}[name]
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			},
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMySQLInstallStateFixture(t)
			f.seedFailedJournal(t)
			if tc.mutate != nil {
				tc.mutate(t, f)
			}
			before := mysqlDirectorySnapshot(t, f.instance)
			softwareBefore := mysqlDirectorySnapshot(t, f.software)
			b, err := f.run(t, f.preparation, tc.mode)
			if err == nil {
				t.Fatalf("unproved history was accepted: %s", b)
			}
			assertMySQLSnapshot(t, f.instance, before)
			assertMySQLSnapshot(t, f.software, softwareBefore)
			if b, _ := os.ReadFile(filepath.Join(f.root, "rm.log")); len(b) > 0 {
				t.Fatalf("rejected history reached removal: %s", b)
			}
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(f.instance), ".3306.failed-initialize.*", "instance"))
			if len(backups) != 0 {
				t.Fatalf("rejected history moved database: %v", backups)
			}
			if tc.mode == "process-on-recheck" {
				if _, err := os.Stat(filepath.Join(f.root, "disabled")); err != nil {
					t.Fatalf("race fixture never reached the quiesced pre-rename proof: %v", err)
				}
			}
		})
	}
}

func mustRemoveMySQLTestPath(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLRepeatedFailedInitializationKeepsEveryAttempt(t *testing.T) {
	f := newMySQLInstallStateFixture(t)
	f.seedFailedJournal(t)
	before := mysqlDirectorySnapshot(t, f.instance)
	if b, err := f.run(t, f.preparation, ""); err != nil {
		t.Fatalf("first retry: %v: %s", err, b)
	}
	writeFile(t, f.config, "[mysqld]\ndatadir="+f.data+"\nport=3306\nread_only=ON\nsuper_read_only=ON\n", 0600)
	writeFile(t, f.log, "second initialization failed\n", 0600)
	writeFile(t, filepath.Join(f.data, "mysql", "sentinel"), "second partial bytes\n", 0600)
	writeFile(t, filepath.Join(f.software, ".clusterguard-managed"), "managed_by=clusterguard-ha\nport=3306\ninstance="+f.instance+"\n", 0600)
	f.seedFailedJournal(t)
	second := mysqlDirectorySnapshot(t, f.instance)
	if b, err := f.run(t, f.preparation, ""); err != nil {
		t.Fatalf("second retry: %v: %s", err, b)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(f.instance), ".3306.failed-initialize.*", "instance"))
	if len(backups) != 2 {
		t.Fatalf("expected both attempts preserved, got %v", backups)
	}
	matched := map[string]bool{}
	for _, backup := range backups {
		b, err := os.ReadFile(filepath.Join(backup, "data", "mysql", "sentinel"))
		if err != nil {
			t.Fatal(err)
		}
		switch string(b) {
		case "partial system table bytes\n":
			assertMySQLSnapshot(t, backup, before)
			matched["first"] = true
		case "second partial bytes\n":
			assertMySQLSnapshot(t, backup, second)
			matched["second"] = true
		default:
			t.Fatalf("unexpected preserved attempt bytes: %q", b)
		}
	}
	if len(matched) != 2 {
		t.Fatalf("a preserved attempt was overwritten: %v", matched)
	}
}

func TestMySQLInstallJournalIsPrivateAtomicAndRecordsNativeFailure(t *testing.T) {
	f := newMySQLInstallStateFixture(t)
	f.seedFailedJournal(t)
	var record map[string]any
	b, err := os.ReadFile(f.state)
	if err != nil || json.Unmarshal(b, &record) != nil {
		t.Fatalf("journal: %v: %s", err, b)
	}
	if record["phase"] != "initialize-failed" || record["native_exit"] != float64(37) || record["fresh_empty"] != true {
		t.Fatalf("native proof missing: %v", record)
	}
	info, err := os.Stat(f.state)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal is not private: %v (%v)", info, err)
	}
	remaining, _ := filepath.Glob(filepath.Join(filepath.Dir(f.state), ".installation-state.*"))
	if len(remaining) != 0 {
		t.Fatalf("journal temporary files leaked: %v", remaining)
	}
	old, err := os.ReadFile(f.state)
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.run(t, f.functions+`
installation_node_id=node-152
installation_package_sha256="$(cg_mysql_sha256 "$package_path")"
installation_template_sha256="$(cg_mysql_template_sha256)"
installation_data_identity="$(cg_mysql_data_identity "$data_dir")"
installation_fresh_empty=true
cg_mysql_write_install_state invalid-phase
`, "")
	if err == nil {
		t.Fatalf("unknown journal phase accepted: %s", b)
	}
	got, err := os.ReadFile(f.state)
	if err != nil || string(got) != string(old) {
		t.Fatalf("rejected phase changed journal: %v", err)
	}
	b, err = f.run(t, f.functions+`
installation_node_id=node-152
installation_package_sha256="$(cg_mysql_sha256 "$package_path")"
installation_template_sha256="$(cg_mysql_template_sha256)"
installation_data_identity="$(cg_mysql_data_identity "$data_dir")"
installation_fresh_empty=true
cg_mysql_write_install_state initializing
`, "journal-rename-failed")
	if err == nil {
		t.Fatalf("failed atomic journal replacement was accepted: %s", b)
	}
	got, err = os.ReadFile(f.state)
	if err != nil || string(got) != string(old) {
		t.Fatalf("failed atomic replacement changed original journal: %v", err)
	}
	remaining, _ = filepath.Glob(filepath.Join(filepath.Dir(f.state), ".installation-state.*"))
	if len(remaining) != 0 {
		t.Fatalf("failed atomic write leaked temporary files: %v", remaining)
	}
}

func TestMySQLNativeInitializeRecordsOnlyObservedOutcome(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	start := strings.Index(script, `[[ "$(cg_mysql_data_identity "${data_dir}")" == "${installation_data_identity}"`)
	end := strings.Index(script, "\n# Keep the system registration readable")
	if start < 0 || end <= start {
		t.Fatal("cannot locate actual MySQL initialization state transition")
	}
	flow := script[start:end]
	for _, tc := range []struct {
		name, mode, phase string
		nativeExit, exit  int
	}{
		{name: "native-failure", nativeExit: 37, exit: 37, phase: "initialize-failed"},
		{name: "native-success", phase: "initialized"},
		{name: "success-before-durable-record", mode: "success-record-failed", exit: 3, phase: "initializing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMySQLInstallStateFixture(t)
			payload, err := json.Marshal(map[string]any{"target": map[string]any{
				"node_id": "node-152", "mysql_cnf": "[mysqld]\nbasedir=/production/mysql\ndatadir=/production/data\nport=3306\nserver_id=1\nread_only=ON\nsuper_read_only=ON\n",
			}})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.root, "payload.json"), string(payload), 0600)
			if err := os.RemoveAll(f.data); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.data, 0700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.root, "initialize.cnf"), "disposable initialization config", 0600)
			body := f.functions + mysqlSetupStepFunction(t, script) + `
installation_node_id=node-152
installation_package_sha256="$(cg_mysql_sha256 "$package_path")"
installation_template_sha256="$(cg_mysql_template_sha256)"
installation_data_identity="$(cg_mysql_data_identity "$data_dir")"
installation_fresh_empty=true
initialize_config="$CG_TEST_ROOT/initialize.cnf"
root_password=root-fixture-secret
discovery_password=discovery-fixture-secret
operation_password=operation-fixture-secret
replication_password=replication-fixture-secret
mysql_native_fixture() {
 mkdir -p "$data_dir/mysql"
 printf 'native initialization data\n' > "$data_dir/mysql/sentinel"
 printf 'native initializer exit %s\n' "` + fmt.Sprint(tc.nativeExit) + `" > "$log_dir/error.log"
 return ` + fmt.Sprint(tc.nativeExit) + `
}
mysqld=mysql_native_fixture
` + flow
			b, runErr := f.run(t, body, tc.mode)
			if tc.exit == 0 {
				if runErr != nil {
					t.Fatalf("initialization failed: %v: %s", runErr, b)
				}
			} else if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != tc.exit {
				t.Fatalf("native/record failure status changed: got %v want %d: %s", runErr, tc.exit, b)
			}
			var record map[string]any
			b, err = os.ReadFile(f.state)
			if err != nil || json.Unmarshal(b, &record) != nil || record["phase"] != tc.phase {
				t.Fatalf("unproved native outcome was recorded as success/failure: %s (%v)", b, err)
			}
			if tc.nativeExit != 0 && record["native_exit"] != float64(tc.nativeExit) {
				t.Fatalf("native initialization status not bound to journal: %v", record)
			}
			before := mysqlDirectorySnapshot(t, f.instance)
			b, retryErr := f.run(t, f.preparation, "")
			if tc.nativeExit == 0 {
				if retryErr == nil {
					t.Fatalf("initialized or unrecorded success was automatically reset: %s", b)
				}
				assertMySQLSnapshot(t, f.instance, before)
			} else if retryErr != nil {
				t.Fatalf("observed failed initialization could not be preserved/retried: %v: %s", retryErr, b)
			}
		})
	}
}

func TestMySQLPreparationFailureCanRetryAfterExternalCauseIsFixed(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	start := strings.Index(script, "\nquiesce_managed_mysql_service() {")
	end := strings.Index(script, "\n# Keep the system registration readable")
	if start < 0 || end <= start {
		t.Fatal("cannot locate actual preparation through initialization flow")
	}
	flow := script[start:end]
	for _, tc := range []struct {
		name, mode string
		exit       int
	}{
		{name: "partial-tar-extraction", mode: "tar-failed", exit: 23},
		{name: "missing-runtime-library", mode: "runtime-failed", exit: 3},
		{name: "strict-config-validation", mode: "validation-failed", exit: 46},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMySQLInstallStateFixture(t)
			payload, err := json.Marshal(map[string]any{"target": map[string]any{
				"node_id": "node-152", "mysql_cnf": "[mysqld]\nbasedir=/production/mysql\ndatadir=/production/data\nport=3306\nserver_id=1\nread_only=ON\nsuper_read_only=ON\n",
			}})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.root, "payload.json"), string(payload), 0600)
			if err := os.RemoveAll(f.data); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.data, 0700); err != nil {
				t.Fatal(err)
			}
			commands := `
source ./clusterguard-mysql-layout.sh
production_template=""
initialize_config=""
trap '[[ -z "$production_template" ]] || command rm -f "$production_template"; [[ -z "$initialize_config" ]] || command rm -f "$initialize_config"' EXIT
server_id=1523306
socket="$run_dir/mysql.sock"
root_password=root-fixture-secret
discovery_password=discovery-fixture-secret
operation_password=operation-fixture-secret
replication_password=replication-fixture-secret
tar() {
 mkdir -p "$software_dir/bin"
 printf 'partially extracted native payload\n' > "$software_dir/partial-native-file"
 [[ "$CG_TEST_MODE" != tar-failed ]] || return 23
 cat > "$software_dir/bin/mysqld" <<'NATIVE'
#!/usr/bin/env bash
for option in "$@"; do
 case "$option" in
  --version) printf 'mysqld Ver 8.0.44 for Linux on x86_64\n'; exit 0 ;;
  --validate-config) [[ "$CG_TEST_MODE" != validation-failed ]] || exit 46 ;;
  --initialize-insecure)
   mkdir -p "$CG_TEST_ROOT/mysql/3306/data/mysql"
   printf 'initialized native data\n' > "$CG_TEST_ROOT/mysql/3306/data/mysql/sentinel" ;;
 esac
done
NATIVE
 chmod 0755 "$software_dir/bin/mysqld"
 for executable in mysql mysqladmin mysqldump; do
  printf '#!/bin/sh\nexit 0\n' > "$software_dir/bin/$executable"
  chmod 0755 "$software_dir/bin/$executable"
 done
}
ldd() {
 [[ "$CG_TEST_MODE" != runtime-failed ]] || printf 'libfixture.so => not found\n'
 return 0
}
`
			body := commands + flow
			b, runErr := f.run(t, body, tc.mode)
			if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != tc.exit {
				t.Fatalf("wrong native preparation failure: got %v want %d: %s", runErr, tc.exit, b)
			}
			var journal map[string]any
			b, err = os.ReadFile(f.state)
			if err != nil || json.Unmarshal(b, &journal) != nil || journal["phase"] != "preparing" {
				t.Fatalf("pre-initialization failure lost its preparation state: %s (%v)", b, err)
			}
			if entries, err := os.ReadDir(f.data); err != nil || len(entries) != 0 {
				t.Fatalf("test failure unexpectedly initialized a database: %v (%v)", entries, err)
			}
			before := mysqlDirectorySnapshot(t, f.instance)
			b, runErr = f.run(t, body, "")
			if runErr != nil {
				t.Fatalf("external cause fixed but empty preparation cannot retry: %v: %s", runErr, b)
			}
			b, err = os.ReadFile(f.state)
			if err != nil || json.Unmarshal(b, &journal) != nil || journal["phase"] != "initialized" {
				t.Fatalf("safe preparation retry did not reach native initialization: %s (%v)", b, err)
			}
			b, err = os.ReadFile(filepath.Join(f.data, "mysql", "sentinel"))
			if err != nil || string(b) != "initialized native data\n" {
				t.Fatalf("native initialization was not executed after retry: %s (%v)", b, err)
			}
			// The retry must retain the prior instance, including its config/logs
			// and preparation journal, even though data was still empty.
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(f.instance), ".3306.*", "instance"))
			if len(backups) != 1 {
				t.Fatalf("preparation evidence was discarded: %v", backups)
			}
			assertMySQLSnapshot(t, backups[0], before)
		})
	}
}

func TestMySQLLegacyInitializedRecordDoesNotGetAutomaticRebuild(t *testing.T) {
	f := newMySQLInstallStateFixture(t)
	before := mysqlDirectorySnapshot(t, f.instance)
	b, err := f.run(t, f.preparation, "")
	if err == nil {
		t.Fatalf("legacy initialized data silently rebuilt: %s", b)
	}
	assertMySQLSnapshot(t, f.instance, before)
	for _, word := range []string{"preserved", "state", "log"} {
		if !strings.Contains(string(b), word) {
			t.Fatalf("missing actionable %s diagnostic: %s", word, b)
		}
	}
	if strings.Contains(string(b), "unmanaged MySQL data directory") {
		t.Fatalf("managed interrupted data mislabeled unmanaged: %s", b)
	}
}

func TestMySQLInstanceMountBoundaryGuard(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	function, ok := extractShellFunction(string(script), "cg_mysql_assert_instance_unmounted")
	if !ok {
		t.Fatal("missing actual MySQL instance mount-boundary guard")
	}
	base := "36 25 8:1 / / rw,relatime - ext4 /dev/vda1 rw\n"
	for _, tc := range []struct {
		name, contents string
		missing, pass  bool
	}{
		{name: "data-ancestor-mount-allowed", contents: base + "37 36 8:2 / /data rw - xfs /dev/vdb1 rw\n", pass: true},
		{name: "instance-mount-refused", contents: base + "38 36 8:2 / /data/mysql/3306 rw - xfs /dev/vdb1 rw\n"},
		{name: "nested-bind-and-tmpfs-mounts-refused", contents: base + "39 36 8:1 /database-data /data/mysql/3306/data rw - ext4 /dev/vda1 rw\n40 36 0:42 / /data/mysql/3306/tmp rw - tmpfs tmpfs rw\n"},
		{name: "unreadable-mount-table-refused", missing: true},
		{name: "malformed-mount-table-refused", contents: base + "malformed kernel mount table\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mountinfo")
			if !tc.missing {
				writeFile(t, path, tc.contents, 0600)
			}
			// Pass the fixture through the production function's public argument;
			// no ownership, mount parsing or path-prefix logic is replaced here.
			command := exec.Command("bash", "-c", "set -euo pipefail\ninstall_parent=/data/mysql/3306\n"+function+"\ncg_mysql_assert_instance_unmounted \"$1\"", "mount-boundary", path)
			output, runErr := command.CombinedOutput()
			if (runErr == nil) != tc.pass {
				t.Fatalf("mount boundary accepted=%v want=%v: %v: %s", runErr == nil, tc.pass, runErr, output)
			}
			if !tc.pass {
				if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
					t.Fatalf("unsafe or unknown mount state lost fail-closed status: %v: %s", runErr, output)
				}
				if !strings.Contains(string(output), "mount") {
					t.Fatalf("mount refusal lacks operator diagnostic: %s", output)
				}
			}
		})
	}
}
