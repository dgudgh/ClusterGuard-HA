package scripts

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mysqlSetupStepFunction(t *testing.T, script string) string {
	t.Helper()
	function, ok := extractShellFunction(script, "run_mysql_setup_step")
	if !ok {
		t.Fatal("missing native setup-step diagnostic function")
	}
	return function
}

func renderMySQLInitializeCnf(t *testing.T, input, output string) {
	t.Helper()
	command := exec.Command("bash", "-c", `set -euo pipefail
source ./clusterguard-mysql-layout.sh
cg_mysql_render_initialize_cnf "$1" "$2"
`, "initialize-cnf", input, output)
	if b, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render initialization options: %v: %s", err, b)
	}
}

func TestMySQLInitializeCnfPreservesProductionRuntime(t *testing.T) {
	for _, name := range []string{"my-master.cnf", "my-slave.cnf"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			template, err := filepath.Abs(filepath.Join("..", "configs", "mysql", name))
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(template)
			if err != nil {
				t.Fatal(err)
			}
			runtimeConfig := filepath.Join(root, "my.cnf")
			initializeConfig := filepath.Join(root, "initialize.cnf")
			command := exec.Command("bash", "-c", `set -euo pipefail
source ./clusterguard-mysql-layout.sh
cg_mysql_render_cnf "$1" "$2" "$3" "$4" 3306 1523306
`, "production-cnf", template, runtimeConfig, filepath.Join(root, "site"), filepath.Join(root, "software"))
			if b, err := command.CombinedOutput(); err != nil {
				t.Fatalf("render runtime configuration: %v: %s", err, b)
			}
			before, err := os.ReadFile(runtimeConfig)
			if err != nil {
				t.Fatal(err)
			}
			renderMySQLInitializeCnf(t, runtimeConfig, initializeConfig)
			got, err := os.ReadFile(initializeConfig)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(string(before), "\nrpl_semi_sync_master_wait_point", "\nloose_rpl_semi_sync_master_wait_point")
			want = strings.ReplaceAll(want, "\nrpl_semi_sync_master_wait_for_slave_count", "\nloose_rpl_semi_sync_master_wait_for_slave_count")
			if string(got) != want {
				t.Fatalf("initialization copy changed more than the two strict plugin options\ngot:\n%s\nwant:\n%s", got, want)
			}
			for label, item := range map[string]struct {
				path string
				want []byte
			}{"runtime": {runtimeConfig, before}, "production original": {template, original}} {
				after, err := os.ReadFile(item.path)
				if err != nil {
					t.Fatal(err)
				}
				if string(after) != string(item.want) {
					t.Fatalf("%s was modified by initialization rendering", label)
				}
			}
		})
	}
}

func TestMySQLInitializeCnfChangesOnlyServerPluginOptions(t *testing.T) {
	for _, section := range []string{"mysqld", "server", "mysqld-8.0", "server-8.0", "MySQLD-8.0"} {
		t.Run(section, func(t *testing.T) {
			root := t.TempDir()
			input, output := filepath.Join(root, "runtime.cnf"), filepath.Join(root, "initialize.cnf")
			fixture := fmt.Sprintf(`# approved comments and whitespace remain intact
[client]
rpl_semi_sync_master_wait_point = CLIENT_UNTOUCHED
[%s]
  RPL-SEMI-SYNC-master-wait-point = AFTER_SYNC  # keep this value
	rpl_semi_sync_master_wait_for_slave_count=2
loose_rpl_semi_sync_master_enabled = 1
loose-rpl-semi-sync-source-enabled=ON
ordinary_unknown_parameter = FAIL_AT_NATIVE_VALIDATION
rpl_semi_sync_=malformed
rpl_semi_syncronize_enabled=not_semisync
prefix_rpl_semi_sync_master_enabled=not_plugin_prefix
# rpl_semi_sync_master_wait_point = COMMENT
; rpl_semi_sync_slave_enabled = COMMENT
plugin_load = "rpl_semi_sync_master=semisync_master.so;rpl_semi_sync_slave=semisync_slave.so"
[mysqldump]
rpl_semi_sync_master_wait_point = DUMP_UNTOUCHED
[mysqld-custom]
rpl_semi_sync_master_wait_point = CUSTOM_UNTOUCHED
`, section)
			writeFile(t, input, fixture, 0600)
			renderMySQLInitializeCnf(t, input, output)
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(fixture, "  RPL-SEMI-SYNC-master-wait-point", "  loose_RPL-SEMI-SYNC-master-wait-point", 1)
			want = strings.Replace(want, "\trpl_semi_sync_master_wait_for_slave_count", "\tloose_rpl_semi_sync_master_wait_for_slave_count", 1)
			if string(got) != want {
				t.Fatalf("server-scoped initialization rendering mismatch\ngot:\n%s\nwant:\n%s", got, want)
			}
			after, err := os.ReadFile(input)
			if err != nil || string(after) != fixture {
				t.Fatalf("runtime input changed: %v", err)
			}
		})
	}
}

func TestMySQLInitializeCnfRejectsAliasedOutputWithoutChangingRuntime(t *testing.T) {
	for _, mode := range []string{"same-path", "hard-link", "symbolic-link", "symbolic-input", "missing-input"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			runtime := filepath.Join(root, "my.cnf")
			const contents = "[mysqld]\nrpl_semi_sync_master_wait_point=AFTER_SYNC\n"
			writeFile(t, runtime, contents, 0600)
			input, output := runtime, filepath.Join(root, "initialize.cnf")
			switch mode {
			case "same-path":
				output = runtime
			case "hard-link":
				if err := os.Link(runtime, output); err != nil {
					t.Fatal(err)
				}
			case "symbolic-link":
				if err := os.Symlink(runtime, output); err != nil {
					t.Fatal(err)
				}
			case "symbolic-input":
				input = filepath.Join(root, "input-link")
				if err := os.Symlink(runtime, input); err != nil {
					t.Fatal(err)
				}
			case "missing-input":
				input = filepath.Join(root, "missing")
			}
			command := exec.Command("bash", "-c", `set -euo pipefail
source ./clusterguard-mysql-layout.sh
cg_mysql_render_initialize_cnf "$1" "$2"
`, "unsafe-initialize-cnf", input, output)
			b, runErr := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("unsafe configuration was accepted: %v: %s", runErr, b)
			}
			after, err := os.ReadFile(runtime)
			if err != nil || sha256.Sum256(after) != sha256.Sum256([]byte(contents)) {
				t.Fatalf("runtime configuration digest changed after rejection: %v: %s", err, after)
			}
		})
	}
}

func TestMySQLSetupStepPreservesStatusAndRedactsBoundedDiagnostics(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	function := mysqlSetupStepFunction(t, string(script))
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "native-failure", status: 37},
		{name: "successful-output", status: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			var log strings.Builder
			for i := 1; i <= 100; i++ {
				fmt.Fprintf(&log, "native-log-line-%03d\n", i)
			}
			log.WriteString("root=r.[a]*$ discovery=d'quoted operation=o/$[] replication=r\\literal\n")
			writeFile(t, filepath.Join(root, "log/error.log"), log.String(), 0600)
			command := exec.Command("bash", "-c", `set -euo pipefail
log_dir="$1/log"
root_password='r.[a]*$'
discovery_password="d'quoted"
operation_password='o/$[]'
replication_password='r\literal'
native_fixture() {
  printf 'direct native output; %s %s %s %s; retained rX[a]*$\n' "$root_password" "$discovery_password" "$operation_password" "$replication_password"
  return "$2"
}
`+function+`
run_mysql_setup_step initialization native_fixture unused "$2"
`, "native-diagnostics", root, fmt.Sprint(tc.status))
			output, runErr := command.CombinedOutput()
			if tc.status == 0 {
				if runErr != nil {
					t.Fatalf("successful native step failed: %v: %s", runErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != tc.status {
					t.Fatalf("native status lost: %v, expected %d: %s", runErr, tc.status, output)
				}
				for _, want := range []string{"initialization", "37", "direct native output", "native-log-line-100"} {
					if !strings.Contains(string(output), want) {
						t.Fatalf("missing failure diagnostic %q: %s", want, output)
					}
				}
				if strings.Contains(string(output), "native-log-line-001") {
					t.Fatal("native error log was printed without the tail bound")
				}
				if strings.Count(string(output), "native-log-line-") > 80 {
					t.Fatal("native error-log diagnostic exceeded the 80-line bound")
				}
			}
			for _, secret := range []string{"r.[a]*$", "d'quoted", "o/$[]", `r\literal`} {
				if strings.Contains(string(output), secret) {
					t.Fatalf("native step exposed a fixture password: %s", output)
				}
			}
			if !strings.Contains(string(output), "retained rX[a]*$") {
				t.Fatalf("diagnostic redaction treated a password as a pattern, or lost direct output: %s", output)
			}
		})
	}
}

func TestMySQLInitializeFlowUsesTemporaryCnfAndCleansIt(t *testing.T) {
	scriptBytes, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	marker := strings.Index(script, "# Initialization uses a disposable option file")
	if marker < 0 {
		t.Fatal("missing isolated initialization program")
	}
	start := strings.LastIndex(script[:marker], "\nif (( release_major >= 8 ))")
	end := strings.Index(script[marker:], "\n# Keep the system registration")
	if start < 0 || end < 0 {
		t.Fatal("cannot locate validation and initialization program")
	}
	program := script[start : marker+end]
	identityFunction, ok := extractShellFunction(script, "cg_mysql_data_identity")
	if !ok {
		t.Fatal("missing data identity function")
	}
	var exitTrap string
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "trap ") && strings.HasSuffix(line, " EXIT") {
			exitTrap = line
			break
		}
	}
	if exitTrap == "" {
		t.Fatal("missing installer cleanup trap")
	}
	for _, tc := range []struct {
		name       string
		failAt     string
		wantStatus int
	}{
		{name: "success"},
		{name: "initialization-failure", failAt: "initialization", wantStatus: 37},
		{name: "strict-validation-failure", failAt: "validation", wantStatus: 38},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "my.cnf")
			const contents = "[mysqld]\nrpl_semi_sync_master_wait_point = AFTER_SYNC\nrpl_semi_sync_master_wait_for_slave_count = 1\nordinary_unknown_parameter=must_remain_strict\n"
			writeFile(t, config, contents, 0600)
			writeExecutable(t, filepath.Join(root, "mysqld"), `#!/usr/bin/env bash
set -euo pipefail
option_file="${1#--defaults-file=}"
if [[ "$2" == --validate-config ]]; then
  [[ "$option_file" == "$CG_TEST_RUNTIME" ]] || exit 91
  grep -q '^rpl_semi_sync_master_wait_point = AFTER_SYNC$' "$option_file" || exit 92
  printf 'validated\n' >>"$CG_TEST_ROOT/calls"
  [[ "$CG_TEST_FAIL_AT" != validation ]] || { echo 'ordinary_unknown_parameter rejected'; exit 38; }
elif [[ "$2" == --initialize-insecure ]]; then
  [[ "$option_file" != "$CG_TEST_RUNTIME" && -f "$option_file" && "$3" == --user=mysql ]] || exit 93
  grep -q '^loose_rpl_semi_sync_master_wait_point = AFTER_SYNC$' "$option_file" || exit 94
  grep -q '^ordinary_unknown_parameter=must_remain_strict$' "$option_file" || exit 95
  printf '%s\n' "$option_file" >"$CG_TEST_ROOT/initialize-path"
  printf 'initialized\n' >>"$CG_TEST_ROOT/calls"
  [[ "$CG_TEST_FAIL_AT" != initialization ]] || { echo 'native initialization failed'; exit 37; }
else
  exit 96
fi
`)
			command := exec.Command("bash", "-c", `set -euo pipefail
source ./clusterguard-mysql-layout.sh
log_dir="$1/log"; mkdir -p "$log_dir"
config_file="$1/my.cnf"; mysqld="$1/mysqld"; release_major=8
payload="$1/disposable-payload"; production_template=''; initialize_config=''
root_password=''; discovery_password=''; operation_password=''; replication_password=''
data_dir="$1/data"; mkdir -p "$data_dir"
# Journal transitions are exercised with real files in mysql_install_state_test.
# This fixture isolates the disposable option-file and native-status contract.
cg_mysql_write_install_state() { :; }
`+identityFunction+`
installation_data_identity="$(cg_mysql_data_identity "$data_dir")"
`+mysqlSetupStepFunction(t, script)+"\n"+exitTrap+"\n"+program, "initialize-flow", root)
			command.Env = append(os.Environ(), "CG_TEST_ROOT="+root, "CG_TEST_RUNTIME="+config, "CG_TEST_FAIL_AT="+tc.failAt)
			output, runErr := command.CombinedOutput()
			if tc.wantStatus == 0 {
				if runErr != nil {
					t.Fatalf("initialization program failed: %v: %s", runErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != tc.wantStatus {
					t.Fatalf("native failure changed: %v, expected %d: %s", runErr, tc.wantStatus, output)
				}
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.failAt == "validation" {
				if string(calls) != "validated\n" {
					t.Fatalf("initialization ran after strict validation failed: %s", calls)
				}
			} else {
				if string(calls) != "validated\ninitialized\n" {
					t.Fatalf("wrong setup order: %s", calls)
				}
				path, err := os.ReadFile(filepath.Join(root, "initialize-path"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(strings.TrimSpace(string(path))); !os.IsNotExist(err) {
					t.Fatalf("temporary initialization configuration was not cleaned: %v", err)
				}
			}
			if got, err := os.ReadFile(config); err != nil || string(got) != contents {
				t.Fatalf("runtime configuration changed: %v: %s", err, got)
			}
		})
	}
}

func TestMySQLSetupStepDoesNotFollowErrorLogSymlinks(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "private"), "PRIVATE FILE MUST NOT BE READ\n", 0600)
	if err := os.Mkdir(filepath.Join(root, "log"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private"), filepath.Join(root, "log/error.log")); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "-c", `set -euo pipefail
log_dir="$1/log"
root_password=''; discovery_password=''; operation_password=''; replication_password=''
`+mysqlSetupStepFunction(t, string(script))+`
run_mysql_setup_step initialization bash -c 'echo native-safe-error >&2; exit 41'
`, "symlink-diagnostics", root)
	output, runErr := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 41 {
		t.Fatalf("native status changed: %v: %s", runErr, output)
	}
	if !strings.Contains(string(output), "native-safe-error") || strings.Contains(string(output), "PRIVATE FILE MUST NOT BE READ") {
		t.Fatalf("unexpected symlink diagnostics: %s", output)
	}
}

func TestMySQLSetupStepKeepsNativeStatusWhenLogReadFails(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "log/error.log"), "log may disappear during read\n", 0600)
	command := exec.Command("bash", "-c", `set -euo pipefail
log_dir="$1/log"
root_password=''; discovery_password=''; operation_password=''; replication_password=''
tail() { return 9; }
`+mysqlSetupStepFunction(t, string(script))+`
run_mysql_setup_step initialization bash -c 'echo direct-native-error >&2; exit 37'
`, "log-read-failure", root)
	output, runErr := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("error-log read replaced the native exit status: %v: %s", runErr, output)
	}
	if !strings.Contains(string(output), "direct-native-error") {
		t.Fatalf("error-log read failure lost direct native output: %s", output)
	}
}
