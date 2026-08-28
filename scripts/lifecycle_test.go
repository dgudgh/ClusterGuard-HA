package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeLifecycleScriptsAreSyntaxValidAndExposeStructuredStages(t *testing.T) {
	paths := []string{
		"clusterguard-node-lifecycle.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh",
		"clusterguard-postgresql-build.sh", "clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh", "clusterguard-mysql-probe-cleanup.sh",
		"clusterguard-package-resolve.sh", "clusterguard-adapter-runtime-install.sh", "clusterguard-control-join.sh",
	}
	for _, path := range paths {
		if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
	contents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatalf("read lifecycle script: %v", err)
	}
	text := string(contents)
	for _, required := range []string{"preflight", "install", "synchronize", "configure_replication", "verify", `\"type\":\"event\"`, `\"type\":\"result\"`, "CG_SSH_PASSWORD", "SSHPASS", "PasswordAuthentication=yes", "BatchMode=no"} {
		if !strings.Contains(text, required) {
			t.Fatalf("lifecycle script missing %q", required)
		}
	}
	for _, forbidden := range []string{"orchestrator", "orchctl", "ProxySQL", "DBProxy", "RouteRepair"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("lifecycle script contains forbidden legacy term %q", forbidden)
		}
	}
	for _, required := range []string{
		`port="$(jq -r '.ssh_port // 22'`,
		`ssh_command+=(-p "${port}")`,
		`scp_command+=(-P "${port}")`,
		"reactivating the enrolled data-node agent",
		"systemctl enable --now clusterguard-agent.service",
		"systemctl is-active --quiet clusterguard-agent.service",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("lifecycle SSH transport does not honor configured ports: missing %q", required)
		}
	}
}

func TestControlJoinIssuesPerNodeTLSIdentityAndKeepsRuntimeSecretsReadable(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-control-join.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, required := range []string{
		"CG_CONTROL_API_ISSUER_CERT", "CG_CONTROL_API_ISSUER_KEY",
		"CG_CONTROL_RAFT_ISSUER_CERT", "CG_CONTROL_RAFT_ISSUER_KEY",
		"issue_control_certificate", "serverAuth,clientAuth", "subjectAltName=",
		"certificate_covers_host", "configured CA bundle", "-set_serial",
		`install -m 0640 "${source_environment}"`,
		`[[ "${relative}" == *.key || "${relative}" == *_ed25519 ]] && mode=0640`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("control join helper missing secure enrollment contract %q", required)
		}
	}
	if strings.Contains(script, "-CAcreateserial") {
		t.Fatal("control join helper must not write CA serial state beside the protected issuer")
	}
}

func TestControllerPairInstallationRollsBackNewControlRolesBeforeMembershipCommit(t *testing.T) {
	lifecycleContents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	controlContents, err := os.ReadFile("clusterguard-control-join.sh")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleScript := string(lifecycleContents)
	controlScript := string(controlContents)
	for _, required := range []string{
		"installed_control_indexes", "rollback_installed_controls", "new_install",
		`"${control_helper}" "${request_file}" "${target_index}" rollback`,
	} {
		if !strings.Contains(lifecycleScript, required) {
			t.Fatalf("node lifecycle executor missing controller rollback contract %q", required)
		}
	}
	for _, required := range []string{
		`operation="${3:-join}"`, `join|rollback`, "control-enrollment-", "systemctl disable --now clusterguard-ha.service",
		`"new_install":true`, `"new_install":false`,
	} {
		if !strings.Contains(controlScript, required) {
			t.Fatalf("control join helper missing rollback contract %q", required)
		}
	}
}

func TestControllerPairExecutionRollsBackBothStagedRolesWhenSecondJoinFails(t *testing.T) {
	root := t.TempDir()
	knownHosts := filepath.Join(root, "known_hosts")
	identityFile := filepath.Join(root, "id_ed25519")
	controlLog := filepath.Join(root, "control.log")
	writeFile(t, knownHosts, "test-host ssh-ed25519 AAAATEST\n", 0o600)
	writeFile(t, identityFile, "test identity\n", 0o600)

	writeExecutable(t, filepath.Join(root, "ssh"), `#!/bin/bash
case "$*" in
  *clusterguard-adapter-runtime-install.sh*)
    printf '%s\n' '{"ready":true,"engine":"mysql","binary":"/usr/bin/mysql"}'
    ;;
esac
exit 0
`)
	writeExecutable(t, filepath.Join(root, "scp"), "#!/bin/bash\nexit 0\n")
	adapterHelper := filepath.Join(root, "adapter-runtime.sh")
	writeExecutable(t, adapterHelper, "#!/bin/bash\nexit 0\n")
	controlHelper := filepath.Join(root, "control-join.sh")
	writeExecutable(t, controlHelper, `#!/bin/bash
index="$2"
operation="$3"
printf '%s:%s\n' "$operation" "$index" >> "$CONTROL_LOG"
if [[ "$operation" == "join" && "$index" == "1" ]]; then
  exit 17
fi
printf '%s\n' '{"ready":true,"new_install":true}'
`)

	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required for the lifecycle integration test")
	}
	request := `{
  "request":{"cluster_id":"11111111-1111-4111-8111-111111111111","engine":"mysql"},
  "plan":{"targets":[
    {"node_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","node_name":"cg-control-0004","kind":"controller","hostname":"control-4","ip_address":"192.0.2.14","ssh_user":"root","ssh_port":22,"mysql_version":"8.4.10"},
    {"node_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","node_name":"cg-control-0005","kind":"controller","hostname":"control-5","ip_address":"192.0.2.15","ssh_user":"root","ssh_port":22,"mysql_version":"8.4.10"}
  ]}
}`
	command := exec.Command("bash", "clusterguard-node-lifecycle.sh", "execute")
	command.Stdin = strings.NewReader(request)
	cleanEnvironment := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PATH=") {
			cleanEnvironment = append(cleanEnvironment, value)
		}
	}
	command.Env = append(cleanEnvironment,
		"PATH="+root+":"+os.Getenv("PATH"),
		"CG_JQ_BINARY="+jqPath,
		"CG_SSH_KNOWN_HOSTS="+knownHosts,
		"CG_SSH_IDENTITY_FILE="+identityFile,
		"CG_ADAPTER_RUNTIME_HELPER="+adapterHelper,
		"CG_CONTROL_JOIN_HELPER="+controlHelper,
		"CONTROL_LOG="+controlLog,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("second controller join unexpectedly succeeded: %s", output)
	}
	contents, readErr := os.ReadFile(controlLog)
	if readErr != nil {
		t.Fatalf("read control helper log: %v; lifecycle output=%s", readErr, output)
	}
	if got, want := string(contents), "join:0\njoin:1\nrollback:1\nrollback:0\n"; got != want {
		t.Fatalf("controller rollback order=%q, want %q; lifecycle output=%s", got, want, output)
	}
}

func TestPackageResolverSelectsCompatibleVerifiedPackage(t *testing.T) {
	repository := t.TempDir()
	packageName := "upsql-2.3.0.tar.gz"
	packageContents := []byte("verified offline package")
	writeFile(t, filepath.Join(repository, packageName), string(packageContents), 0o600)
	digest := sha256.Sum256(packageContents)
	writeFile(t, filepath.Join(repository, "manifest.json"), `{
  "schema_version": 1,
  "packages": [{
    "engine": "mysql",
    "version_prefix": "5.7.23",
    "filename": "`+packageName+`",
    "sha256": "`+hex.EncodeToString(digest[:])+`"
  }]
}`, 0o600)

	command := exec.Command("bash", "clusterguard-package-resolve.sh", "mysql", "5.7.23-upsql-2.3.0-log")
	command.Env = append(os.Environ(), "CG_PACKAGE_REPOSITORY="+repository)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve compatible package: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != filepath.Join(repository, packageName) {
		t.Fatalf("unexpected package path %q", output)
	}
}

func TestPackageResolverFailsClosedOnAmbiguityOrDigestMismatch(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name: "ambiguous",
			manifest: `{"schema_version":1,"packages":[
              {"engine":"mysql","version_prefix":"8.0","filename":"mysql-a.tar.gz","sha256":"unused"},
              {"engine":"mysql","version_prefix":"8.0","filename":"mysql-b.tar.gz","sha256":"unused"}
            ]}`,
			want: "exactly one compatible package",
		},
		{
			name:     "digest mismatch",
			manifest: `{"schema_version":1,"packages":[{"engine":"mysql","version":"8.0.44","filename":"mysql-a.tar.gz","sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`,
			want:     "SHA256 verification failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			writeFile(t, filepath.Join(repository, "mysql-a.tar.gz"), "package-a", 0o600)
			writeFile(t, filepath.Join(repository, "mysql-b.tar.gz"), "package-b", 0o600)
			writeFile(t, filepath.Join(repository, "manifest.json"), test.manifest, 0o600)
			command := exec.Command("bash", "clusterguard-package-resolve.sh", "mysql", "8.0.44")
			command.Env = append(os.Environ(), "CG_PACKAGE_REPOSITORY="+repository)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("resolver must fail closed: err=%v output=%s", err, output)
			}
		})
	}
}

func TestPowerRestoreRejectsStaleSnapshotBeforeStartingDatabase(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "cluster.json")
	writeFile(t, snapshot, `{
  "cluster_id":"11111111-1111-4111-8111-111111111111",
  "cluster_name":"stale",
  "engine":"mysql",
  "captured_at":"2026-08-09T00:00:00Z",
  "local_instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "local_service_name":"mysqld-stale.service",
  "primary":{"instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","ip_address":"192.0.2.10","port":3306},
  "replicas":[]
}`, 0o600)
	actionLog := filepath.Join(root, "actions.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/power/status'*) printf '%s\n200' '{"status":"ok","result":{"power_operation":null}}' ;;
  *) exit 9 ;;
esac
`)
	systemctlStub := filepath.Join(root, "systemctl")
	writeExecutable(t, systemctlStub, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$ACTION_LOG\"\n")
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshot)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("SYSTEMCTL_COMMAND", systemctlStub)
	t.Setenv("ACTION_LOG", actionLog)
	t.Setenv("CLUSTER_RESTORE_API_TIMEOUT", "1")
	command := exec.Command("bash", "clusterguard-cluster-restore.sh")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not backed by an active planned recovery") {
		t.Fatalf("stale snapshot must fail closed before service start: err=%v output=%s", err, output)
	}
	if contents, readErr := os.ReadFile(actionLog); readErr == nil && len(contents) != 0 {
		t.Fatalf("stale snapshot executed a local mutation: %s", contents)
	}
}

func TestPowerRestoreProcessesEveryPerClusterSnapshot(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "power-snapshots")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index, clusterID := range []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	} {
		instanceID := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}[index]
		service := []string{"mysqld-3306.service", "mysqld-3384.service"}[index]
		contents := `{"cluster_id":"` + clusterID + `","cluster_name":"cluster-` + string(rune('a'+index)) + `","engine":"mysql","captured_at":"2026-08-09T00:00:00Z","local_instance_id":"` + instanceID + `","local_service_name":"` + service + `","primary":{"instance_id":"` + instanceID + `","hostname":"db-a","ip_address":"192.0.2.10","port":3306},"replicas":[]}`
		writeFile(t, filepath.Join(snapshotDir, clusterID+".json"), contents, 0o600)
	}
	actionLog := filepath.Join(root, "actions.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/power/status'*) printf '%s\n200' '{"status":"ok","result":{"power_operation":{"state":"power_off"}}}' ;;
  *'/power/boot-detected'*|*'/power/recovering'*) printf '200' ;;
  *'/discover'*) printf '200' ;;
  *) exit 9 ;;
esac
`)
	for _, commandName := range []string{"systemctl", "mysql", "mysqladmin"} {
		path := filepath.Join(root, commandName)
		if commandName == "mysql" {
			writeExecutable(t, path, "#!/bin/bash\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$ACTION_LOG\"\n[[ \"$*\" == *\"SELECT VERSION()\"* ]] && printf '8.0.46\\n'\nexit 0\n")
		} else if commandName == "systemctl" {
			writeExecutable(t, path, "#!/bin/bash\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$ACTION_LOG\"\n")
		} else {
			writeExecutable(t, path, "#!/bin/bash\nexit 0\n")
		}
	}
	t.Setenv("CLUSTER_SNAPSHOT_PATH", "")
	t.Setenv("CLUSTER_SNAPSHOT_DIR", snapshotDir)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CG_NODE_MYSQL_ROOT_PASSWORD", "test-password")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("SYSTEMCTL_COMMAND", filepath.Join(root, "systemctl"))
	t.Setenv("MYSQL_COMMAND", filepath.Join(root, "mysql"))
	t.Setenv("MYSQLADMIN_COMMAND", filepath.Join(root, "mysqladmin"))
	t.Setenv("ACTION_LOG", actionLog)
	command := exec.Command("bash", "clusterguard-cluster-restore.sh")
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restore every snapshot: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(actionLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"start mysqld-3306.service", "start mysqld-3384.service"} {
		if !strings.Contains(string(contents), service) {
			t.Fatalf("per-cluster restore did not start %s:\n%s", service, contents)
		}
	}
}

func TestPowerRestoreUsesRuntimeReadOnlyForMySQL57WithoutPersistOnly(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "cluster.json")
	writeFile(t, snapshot, `{
  "cluster_id":"11111111-1111-4111-8111-111111111111",
  "cluster_name":"upsql-57",
  "engine":"mysql",
  "local_instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "local_service_name":"upsql.service",
  "primary":{"instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","ip_address":"192.0.2.10","port":3360},
  "replicas":[]
}`, 0o600)
	actionLog := filepath.Join(root, "actions.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/power/status'*) printf '%s\n200' '{"status":"ok","result":{"power_operation":{"state":"power_off"}}}' ;;
  *'/power/boot-detected'*|*'/power/recovering'*|*'/discover'*) printf '200' ;;
  *) exit 9 ;;
esac
`)
	systemctlStub := filepath.Join(root, "systemctl")
	writeExecutable(t, systemctlStub, "#!/bin/bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$ACTION_LOG\"\n")
	mysqlStub := filepath.Join(root, "mysql")
	writeExecutable(t, mysqlStub, `#!/bin/bash
printf 'mysql %s\n' "$*" >> "$ACTION_LOG"
if [[ "$*" == *"PERSIST_ONLY"* ]]; then
  exit 57
fi
if [[ "$*" == *"SELECT VERSION()"* ]]; then
  printf '%s\n' '5.7.44-upsql'
fi
`)
	mysqladminStub := filepath.Join(root, "mysqladmin")
	writeExecutable(t, mysqladminStub, "#!/bin/bash\nexit 0\n")
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshot)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CG_NODE_MYSQL_ROOT_PASSWORD", "test-password")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("SYSTEMCTL_COMMAND", systemctlStub)
	t.Setenv("MYSQL_COMMAND", mysqlStub)
	t.Setenv("MYSQLADMIN_COMMAND", mysqladminStub)
	t.Setenv("ACTION_LOG", actionLog)
	t.Setenv("CLUSTER_RESTORE_API_TIMEOUT", "1")
	t.Setenv("CLUSTER_RESTORE_MYSQL_TIMEOUT", "1")
	command := exec.Command("bash", "clusterguard-cluster-restore.sh")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("MySQL 5.7-compatible restore failed: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(actionLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(contents)
	if !strings.Contains(logText, "SET GLOBAL super_read_only = OFF; SET GLOBAL read_only = OFF") {
		t.Fatalf("restore did not clear runtime read-only state: %s", logText)
	}
	if strings.Contains(logText, "PERSIST_ONLY") {
		t.Fatalf("MySQL 5.7 restore attempted unsupported PERSIST_ONLY syntax: %s", logText)
	}
}

func TestPowerRestoreRetriesTransientLifecycleReportWithinSameBoot(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "cluster.json")
	writeFile(t, snapshot, `{
  "cluster_id":"11111111-1111-4111-8111-111111111111",
  "cluster_name":"retry-after-election",
  "engine":"mysql",
  "captured_at":"2026-08-09T00:00:00Z",
  "local_instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "local_service_name":"mysqld-retry.service",
  "primary":{"instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","ip_address":"192.0.2.10","port":3306},
  "replicas":[]
}`, 0o600)
	curlLog := filepath.Join(root, "curl.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
touch "$CURL_LOG"
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/power/status'*) printf '%s\n200' '{"status":"ok","result":{"power_operation":{"state":"power_off"}}}' ;;
  *'/power/boot-detected'*)
    count="$(grep -c '^boot-detected$' "$CURL_LOG" 2>/dev/null || true)"
    printf '%s\n' 'boot-detected' >> "$CURL_LOG"
    if [[ "$count" == "0" ]]; then printf '500'; else printf '200'; fi
    ;;
  *'/power/recovering'*) printf '200' ;;
  *'/discover'*)
    count="$(grep -c '^discover$' "$CURL_LOG" 2>/dev/null || true)"
    printf '%s\n' 'discover' >> "$CURL_LOG"
    if [[ "$count" == "0" ]]; then printf '503'; exit 22; else printf '200'; fi
    ;;
  *) exit 9 ;;
esac
`)
	for _, commandName := range []string{"systemctl", "mysql", "mysqladmin"} {
		path := filepath.Join(root, commandName)
		if commandName == "mysqladmin" {
			writeExecutable(t, path, "#!/bin/bash\nexit 0\n")
		} else if commandName == "mysql" {
			writeExecutable(t, path, "#!/bin/bash\n[[ \"$*\" == *\"SELECT VERSION()\"* ]] && printf '8.0.46\\n'\nexit 0\n")
		} else {
			writeExecutable(t, path, "#!/bin/bash\nexit 0\n")
		}
	}
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshot)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CG_NODE_MYSQL_ROOT_PASSWORD", "test-password")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("SYSTEMCTL_COMMAND", filepath.Join(root, "systemctl"))
	t.Setenv("MYSQL_COMMAND", filepath.Join(root, "mysql"))
	t.Setenv("MYSQLADMIN_COMMAND", filepath.Join(root, "mysqladmin"))
	t.Setenv("CURL_LOG", curlLog)
	t.Setenv("CLUSTER_RESTORE_REPORT_ATTEMPTS", "3")
	t.Setenv("CLUSTER_RESTORE_REPORT_RETRY_INTERVAL", "0")
	command := exec.Command("bash", "clusterguard-cluster-restore.sh")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transient lifecycle report must converge without a systemd restart: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(contents), "boot-detected\n"); got != 2 {
		t.Fatalf("boot-detected attempts=%d, want 2; output=%s", got, output)
	}
	if got := strings.Count(string(contents), "discover\n"); got != 2 {
		t.Fatalf("discovery attempts=%d, want 2; output=%s", got, output)
	}
	if !strings.Contains(string(output), "retrying power/boot-detected") {
		t.Fatalf("transient lifecycle retry was not observable: %s", output)
	}
	if !strings.Contains(string(output), "retrying topology discovery") {
		t.Fatalf("transient discovery retry was not observable: %s", output)
	}
}

func TestPowerRestoreWaitsForReplicatedPowerOffStateBeforeRoleChanges(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "cluster.json")
	writeFile(t, snapshot, `{
  "cluster_id":"11111111-1111-4111-8111-111111111111",
  "cluster_name":"wait-for-raft-catchup",
  "engine":"mysql",
  "local_instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "local_service_name":"mysqld-catchup.service",
  "primary":{"instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","ip_address":"192.0.2.10","port":3306},
  "replicas":[]
}`, 0o600)
	actionLog := filepath.Join(root, "actions.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/power/status'*)
    touch "$ACTION_LOG"
    count="$(grep -c '^power-status$' "$ACTION_LOG" 2>/dev/null || true)"
    printf '%s\n' 'power-status' >> "$ACTION_LOG"
    if [[ "$count" == "0" ]]; then
      printf '%s\n200' '{"status":"ok","result":{"power_operation":{"state":"shutting_down"}}}'
    else
      printf '%s\n200' '{"status":"ok","result":{"power_operation":{"state":"power_off"}}}'
    fi
    ;;
  *'/power/boot-detected'*|*'/power/recovering'*) printf '200' ;;
  *'/discover'*) printf '200' ;;
  *) exit 9 ;;
esac
`)
	for _, commandName := range []string{"systemctl", "mysql", "mysqladmin"} {
		path := filepath.Join(root, commandName)
		if commandName == "mysqladmin" {
			writeExecutable(t, path, "#!/bin/bash\nexit 0\n")
		} else if commandName == "mysql" {
			writeExecutable(t, path, "#!/bin/bash\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$ACTION_LOG\"\n[[ \"$*\" == *\"SELECT VERSION()\"* ]] && printf '8.0.46\\n'\nexit 0\n")
		} else {
			writeExecutable(t, path, "#!/bin/bash\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$ACTION_LOG\"\n")
		}
	}
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshot)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CG_NODE_MYSQL_ROOT_PASSWORD", "test-password")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("SYSTEMCTL_COMMAND", filepath.Join(root, "systemctl"))
	t.Setenv("MYSQL_COMMAND", filepath.Join(root, "mysql"))
	t.Setenv("MYSQLADMIN_COMMAND", filepath.Join(root, "mysqladmin"))
	t.Setenv("ACTION_LOG", actionLog)
	t.Setenv("CLUSTER_RESTORE_STATE_TIMEOUT", "2")
	t.Setenv("CLUSTER_RESTORE_STATE_POLL_INTERVAL", "0")
	command := exec.Command("bash", "clusterguard-cluster-restore.sh")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("replicated lifecycle state must converge in the same boot attempt: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(actionLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(contents)
	if got := strings.Count(logText, "power-status\n"); got != 2 {
		t.Fatalf("power status attempts=%d, want 2; log=%s output=%s", got, logText, output)
	}
	if startAt := strings.Index(logText, "systemctl start mysqld-catchup.service"); startAt < strings.LastIndex(logText, "power-status\n") {
		t.Fatalf("database mutation ran before the replicated power_off state arrived: %s", logText)
	}
	if !strings.Contains(string(output), "waiting for replicated lifecycle state") {
		t.Fatalf("state catch-up wait was not observable: %s", output)
	}
}

func TestPowerFinalizeCompletesEveryPerClusterSnapshot(t *testing.T) {
	root := t.TempDir()
	snapshotDir := filepath.Join(root, "power-snapshots")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	for index, clusterID := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		writeFile(t, filepath.Join(snapshotDir, clusterID+".json"), `{"cluster_id":"`+clusterID+`","engine":"mysql","primary":{"instance_id":"`+ids[index]+`"},"replicas":[]}`, 0o600)
	}
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/topology'*) printf '%s' '{"status":"ok","result":{"instances":[{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","role":"primary","health":{"state":"healthy"}},{"resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","hostname":"db-b","role":"primary","health":{"state":"healthy"}}]}}' ;;
  *'/power/verify'*|*'/power/complete'*) printf '200' ;;
  *) exit 9 ;;
esac
`)
	t.Setenv("CLUSTER_SNAPSHOT_PATH", "")
	t.Setenv("CLUSTER_SNAPSHOT_DIR", snapshotDir)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("CLUSTER_FINALIZE_API_TIMEOUT", "1")
	t.Setenv("CLUSTER_FINALIZE_POLL_INTERVAL", "0")
	t.Setenv("CLUSTER_FINALIZE_POLL_TIMEOUT", "1")
	command := exec.Command("bash", "clusterguard-cluster-finalize.sh")
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("finalize every snapshot: %v\n%s", err, output)
	}
	for _, clusterID := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		contents, err := os.ReadFile(filepath.Join(snapshotDir, clusterID+".json"))
		if err != nil || !strings.Contains(string(contents), `"recovered_at"`) {
			t.Fatalf("snapshot %s was not finalized: err=%v contents=%s", clusterID, err, contents)
		}
	}
}

func TestPowerFinalizeRetriesTransientLifecycleMutationWithinSameBoot(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "cluster.json")
	writeFile(t, snapshot, `{
  "cluster_id":"11111111-1111-4111-8111-111111111111",
  "engine":"mysql",
  "primary":{"instance_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
  "replicas":[]
}`, 0o600)
	curlLog := filepath.Join(root, "curl.log")
	curlStub := filepath.Join(root, "curl")
	writeExecutable(t, curlStub, `#!/bin/bash
touch "$CURL_LOG"
case "$*" in
  *'/healthz'*) printf '200' ;;
  *'/topology'*) printf '%s' '{"status":"ok","result":{"instances":[{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","hostname":"db-a","role":"primary","health":{"state":"healthy"}}]}}' ;;
  *'/power/verify'*)
    count="$(grep -c '^verify$' "$CURL_LOG" 2>/dev/null || true)"
    printf '%s\n' 'verify' >> "$CURL_LOG"
    if [[ "$count" == "0" ]]; then printf '503'; exit 22; else printf '200'; fi
    ;;
  *'/power/complete'*)
    count="$(grep -c '^complete$' "$CURL_LOG" 2>/dev/null || true)"
    printf '%s\n' 'complete' >> "$CURL_LOG"
    if [[ "$count" == "0" ]]; then printf '503'; exit 22; else printf '200'; fi
    ;;
  *) exit 9 ;;
esac
`)
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshot)
	t.Setenv("CG_CONTROL_TOKEN", "test-control-token")
	t.Setenv("CURL_COMMAND", curlStub)
	t.Setenv("CURL_LOG", curlLog)
	t.Setenv("CLUSTER_FINALIZE_API_TIMEOUT", "1")
	t.Setenv("CLUSTER_FINALIZE_POLL_INTERVAL", "0")
	t.Setenv("CLUSTER_FINALIZE_POLL_TIMEOUT", "1")
	t.Setenv("CLUSTER_FINALIZE_REPORT_ATTEMPTS", "3")
	t.Setenv("CLUSTER_FINALIZE_REPORT_RETRY_INTERVAL", "0")
	command := exec.Command("bash", "clusterguard-cluster-finalize.sh")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transient finalize mutations must converge without a systemd restart: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"verify", "complete"} {
		if got := strings.Count(string(contents), action+"\n"); got != 2 {
			t.Fatalf("%s attempts=%d, want 2; output=%s", action, got, output)
		}
		if !strings.Contains(string(output), "retrying power/"+action) {
			t.Fatalf("transient %s retry was not observable: %s", action, output)
		}
	}
	if snapshotContents, readErr := os.ReadFile(snapshot); readErr != nil || !strings.Contains(string(snapshotContents), `"recovered_at"`) {
		t.Fatalf("snapshot was not finalized: err=%v contents=%s", readErr, snapshotContents)
	}
}

func TestPowerRestoreUsesPerClusterSnapshotsAndFailClosedLifecycleGate(t *testing.T) {
	restoreContents, err := os.ReadFile("clusterguard-cluster-restore.sh")
	if err != nil {
		t.Fatal(err)
	}
	restore := string(restoreContents)
	for _, required := range []string{
		"CLUSTER_SNAPSHOT_DIR",
		"/etc/clusterguard/power-snapshots",
		"power/status",
		"power_off|boot_detected|recovering|verifying",
		`.primary.hostname // .cluster.primary.host`,
		`.primary.ip_address // .cluster.primary.ip`,
	} {
		if !strings.Contains(restore, required) {
			t.Fatalf("power restore safety contract missing %q", required)
		}
	}
	finalizeContents, err := os.ReadFile("clusterguard-cluster-finalize.sh")
	if err != nil {
		t.Fatal(err)
	}
	finalize := string(finalizeContents)
	for _, required := range []string{
		"CLUSTER_SNAPSHOT_DIR",
		"/etc/clusterguard/power-snapshots",
		`.primary.instance_id // .cluster.primary.instance_id`,
		`(.replicas // .cluster.replicas // [])`,
	} {
		if !strings.Contains(finalize, required) {
			t.Fatalf("power finalize snapshot contract missing %q", required)
		}
	}
}

func TestMySQLProbeCleanupConvergesLegacySchemasToSingleCanonicalSchema(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-probe-cleanup.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, `--password="${password}"`) || !strings.Contains(text, `MYSQL_PWD="${password}"`) {
		t.Fatal("probe cleanup helper must keep MySQL credentials out of process arguments")
	}
	for _, required := range []string{
		"canonical_probe_schema",
		"clusterguard_probe",
		"cg_rc28_test",
		"clusterguard_ha_test",
		"clusterguard_matrix_probe",
		"clusterguard_validation",
		"DROP DATABASE IF EXISTS",
		"CREATE DATABASE IF NOT EXISTS",
		"probe_heartbeat",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("MySQL probe cleanup script missing %q", required)
		}
	}
	for _, forbidden := range []string{"DROP DATABASE IF EXISTS `mysql`", "DROP DATABASE IF EXISTS `sys`", "DROP DATABASE IF EXISTS `performance_schema`"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("MySQL probe cleanup script must not target system schema %q", forbidden)
		}
	}
}

func TestPostgreSQLLifecycleUsesProtectedCredentialsAtomicSyncAndNativeVerification(t *testing.T) {
	lifecycleContents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{".request.engine", "clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh", "CG_POSTGRESQL_ADMIN_PASSWORD", "CG_POSTGRESQL_REPLICATION_PASSWORD"} {
		if !strings.Contains(string(lifecycleContents), required) {
			t.Fatalf("lifecycle PostgreSQL dispatch missing %q", required)
		}
	}
	for _, path := range []string{"clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		for _, required := range []string{"PGPASSFILE", "chmod 0600"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s does not protect PostgreSQL credentials: missing %q", path, required)
			}
		}
		for _, forbidden := range []string{"password=", "PGPASSWORD="} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s exposes PostgreSQL credentials through %q", path, forbidden)
			}
		}
	}
	syncContents, err := os.ReadFile("clusterguard-postgresql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"pg_basebackup", "pg_rewind", "pg_is_in_recovery", "system_identifier", ".clusterguard-backup", "unexpectedly owns the cluster VIP"} {
		if !strings.Contains(string(syncContents), required) {
			t.Fatalf("PostgreSQL synchronization safety invariant missing %q", required)
		}
	}
	installContents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		`chown root:postgres "${config_directory}"`,
		`chown postgres:postgres "${pass_file}"`,
		"BEGIN ClusterGuard managed PostgreSQL settings",
		`allowed_cidr="${target_ipv4%.*}.0/24"`,
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("PostgreSQL installer is missing enterprise invariant %q", required)
		}
	}
	syncText := string(syncContents)
	for _, required := range []string{
		`replication_pass="${target_pass}"`,
		`chown postgres:postgres "${target_pass}"`,
		`printf '*:*:*:postgres:%s\n'`,
		`printf '*:*:*:%s:%s\n' "${replication_user}"`,
	} {
		if !strings.Contains(syncText, required) {
			t.Fatalf("PostgreSQL synchronization passfile contract is missing %q", required)
		}
	}
	if !strings.Contains(installText, `printf '*:*:*:postgres:%s\n'`) {
		t.Fatal("PostgreSQL installer does not persist cluster-wide admin passfile access for pg_rewind")
	}
}

func TestPostgreSQLLifecycleRequiresCanonicalDistinctPlatformIdentities(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncContents, err := os.ReadFile("clusterguard-postgresql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	canonicalPattern := `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}`
	if !strings.Contains(string(installContents), canonicalPattern) {
		t.Fatal("PostgreSQL installer does not require a canonical platform UUID")
	}
	syncText := string(syncContents)
	if !strings.Contains(syncText, canonicalPattern) ||
		!strings.Contains(syncText, `valid_platform_uuid "${node_id}"`) ||
		!strings.Contains(syncText, `valid_platform_uuid "${source_node_id}"`) {
		t.Fatal("PostgreSQL synchronization does not validate both platform UUIDs canonically")
	}
	if !strings.Contains(syncText, `[[ "${source_node_id}" != "${node_id}" ]]`) {
		t.Fatal("PostgreSQL synchronization does not reject a donor that is also the target resource")
	}
	for _, required := range []string{
		`valid_platform_uuid "${cluster_id}"`,
		`[[ "${node_name}" =~ ^[A-Za-z0-9._-]+$ ]]`,
		`[[ "${target_host}" =~ ^[A-Za-z0-9._-]+$ ]]`,
		`PostgreSQL donor endpoint cannot equal the target endpoint`,
		`"$(pgpass_escape "${source_host}")"`,
	} {
		if !strings.Contains(syncText, required) {
			t.Fatalf("PostgreSQL synchronization input boundary is missing %q", required)
		}
	}
}

func TestMySQLInstallAndSyncScriptsKeepCredentialsOffCommandArguments(t *testing.T) {
	for _, path := range []string{"clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		for _, forbidden := range []string{"-p${MYSQL", "--password=${", "IDENTIFIED BY '${", "MASTER_PASSWORD='${", "SOURCE_PASSWORD='${"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s exposes a credential in command arguments or generated SQL: %q", path, forbidden)
			}
		}
		if !strings.Contains(text, "--defaults-file") || !strings.Contains(text, "0600") {
			t.Fatalf("%s does not use protected MySQL option files", path)
		}
		if strings.Contains(text, "--defaults-extra-file") {
			t.Fatalf("%s allows user option files to override ClusterGuard credentials", path)
		}
	}
}

func TestMySQLLifecycleReconcilesConfiguredControlPlaneAccounts(t *testing.T) {
	lifecycleContents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleText := string(lifecycleContents)
	for _, required := range []string{
		"CG_MYSQL_DISCOVERY_USERNAME", "CG_MYSQL_DISCOVERY_PASSWORD",
		"CG_MYSQL_OPERATION_USERNAME", "CG_MYSQL_OPERATION_PASSWORD",
		"CG_MYSQL_REPLICATION_USERNAME", "CG_MYSQL_ROOT_REMOTE_HOST",
	} {
		if !strings.Contains(lifecycleText, required) {
			t.Fatalf("lifecycle payload omits configured MySQL account field %q", required)
		}
	}
	installText := string(installContents)
	for _, required := range []string{
		"mysql_discovery_username", "mysql_discovery_password",
		"mysql_operation_username", "mysql_operation_password",
		"mysql_replication_username",
		"REPLICATION CLIENT", "WITH GRANT OPTION",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("MySQL install does not reconcile platform account contract: missing %q", required)
		}
	}
	syncText := string(syncContents)
	if !strings.Contains(syncText, `.secrets.mysql_replication_username`) {
		t.Fatal("MySQL sync does not use the configured replication account")
	}
	if strings.Contains(syncText, `replication_user="clusterguard_repl"`) {
		t.Fatal("MySQL sync still hard-codes a replication identity")
	}
}

func TestMySQLSyncAuthenticatesReplicationAccountWithoutMutatingReadOnlyDonor(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`replication_defaults="$(mktemp`,
		`chmod 0600 "${payload}" "${donor_defaults}" "${replication_defaults}"`,
		`mysql_replication_donor`,
		`--defaults-file="${replication_defaults}"`,
		`managed MySQL replication account cannot authenticate to donor`,
		`-e 'SELECT 1'`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("MySQL synchronization is missing read-only donor account validation %q", required)
		}
	}
	for _, forbidden := range []string{
		`CREATE USER IF NOT EXISTS '%s'@'%%'`,
		`ALTER USER '%s'@'%%'`,
		`GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '%s'@'%%'`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("MySQL synchronization still mutates the read-only donor: %q", forbidden)
		}
	}
	authentication := strings.Index(text, `replication_auth_result="$(mysql_replication_donor`)
	targetMutation := strings.Index(text, "SET GLOBAL super_read_only=OFF;")
	if authentication < 0 || targetMutation < 0 || authentication > targetMutation {
		t.Fatal("replication account validation must complete before destructive target synchronization")
	}
}

func TestMultiNodeInstallerUsesOneMySQLReplicationSecretWithinChannelLimit(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`mysql_replication_secret() { openssl rand -hex 16; }`,
		`mysql_managed_replication_password="$(mysql_replication_secret)"`,
		`CG_MYSQL_REPLICATION_PASSWORD=${mysql_managed_replication_password}`,
		`CG_NODE_MYSQL_REPLICATION_PASSWORD=${mysql_managed_replication_password}`,
		`reconcile_mysql_replication_secret_policy`,
		`[[ "$(state_phase)" == "pending" ]]`,
		`拒绝自动轮换`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("multi-node installer is missing MySQL replication credential invariant %q", required)
		}
	}
	phaseReconcile := strings.LastIndex(text, "  reconcile_database_installation_phase\n")
	credentialReconcile := strings.LastIndex(text, "  reconcile_mysql_replication_secret_policy\n")
	packageInstall := strings.LastIndex(text, `  for host in "${all_nodes[@]}"; do log "安装 ClusterGuard RPM`)
	if phaseReconcile < 0 || credentialReconcile < 0 || packageInstall < 0 || !(phaseReconcile < credentialReconcile && credentialReconcile < packageInstall) {
		t.Fatal("snapshot state and replication credentials must be reconciled before any package is installed")
	}

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(syncContents), "managed MySQL replication password exceeds the 32-character channel limit") {
		t.Fatal("MySQL synchronization does not reject an incompatible external replication credential")
	}
}

func TestMultiNodeInstallerWaitsForStableCompleteTopology(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`api_controller_host="${host}"`,
		`CG_INSTALL_API_RETRY_ATTEMPTS:-4`,
		`x-clusterguard-leader-api-address`,
		`[[ "${http_status}" == "503" ]]`,
		`wait_control_plane >/dev/null`,
		`.result.role == "leader"`,
		`.result.voter_count == $voters`,
		`.result.quorum_confirmed == true`,
		`.result.mutation_authority == true`,
		`.result.commit_index == .result.applied_index`,
		`stable >= 2`,
		`wait_for_stable_database_topology()`,
		`CG_INSTALL_VERIFY_TIMEOUT_SECONDS:-180`,
		`CG_INSTALL_VERIFY_STABLE_OBSERVATIONS:-3`,
		`(.result.instances | length) == $count`,
		`([.result.instances[] | select(.health.state == "healthy")] | length) == $count`,
		`([.result.instances[] | select(.role == "primary")] | length) == 1`,
		`(.result.health.state == "healthy")`,
		`stable=$((stable + 1))`,
		`topology="$(wait_for_stable_database_topology`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("installer stable topology verification is missing %q", required)
		}
	}
	configureAgents := strings.LastIndex(text, "  configure_data_agents\n")
	reconfirmLeader := strings.LastIndex(text, "  wait_control_plane\n")
	finalDiscover := strings.LastIndex(text, `  if [[ "${database_engine}" != "none" ]]; then api_request POST`)
	if configureAgents < 0 || reconfirmLeader < 0 || finalDiscover < 0 || !(configureAgents < reconfirmLeader && reconfirmLeader < finalDiscover) {
		t.Fatal("installer must reacquire a stable Raft leader after mixed-node agent configuration and before final discovery")
	}
}

func TestSyncScriptSupportsVersionAwareReplicationAndSelectedCopyMethods(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatalf("read sync script: %v", err)
	}
	text := string(contents)
	for _, required := range []string{
		"clone", "xtrabackup", "logical_dump", "CHANGE MASTER TO", "CHANGE REPLICATION SOURCE TO",
		"MASTER_CONNECT_RETRY=5", "MASTER_RETRY_COUNT=86400", "SOURCE_CONNECT_RETRY=5", "SOURCE_RETRY_COUNT=86400",
		"GET_SOURCE_PUBLIC_KEY=1", "START SLAVE", "START REPLICA", "super_read_only",
		"CG_MYSQL_REPLICATION_VERIFY_TIMEOUT", "${CG_MYSQL_REPLICATION_VERIFY_TIMEOUT:-180}",
		`--vertical -e "${status_command}"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("sync script missing %q", required)
		}
	}
	if strings.Contains(text, `STATUS\G`) {
		t.Fatal("replication verification still relies on the MySQL client-specific \\G batch terminator")
	}
}

func TestLifecycleBundlesRemoteJQAndLogicalDumpExcludesSystemSchemas(t *testing.T) {
	lifecycleContents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatalf("read lifecycle script: %v", err)
	}
	lifecycleText := string(lifecycleContents)
	for _, required := range []string{"CG_JQ_BINARY", "/var/lib/clusterguard/stage/jq", "PATH=/var/lib/clusterguard/stage:"} {
		if !strings.Contains(lifecycleText, required) {
			t.Fatalf("lifecycle script does not bundle remote jq: missing %q", required)
		}
	}
	if strings.Contains(lifecycleText, `command -v bash >/dev/null && command -v jq >/dev/null`) {
		t.Fatal("fresh target still requires jq before lifecycle bootstrap")
	}
	if strings.Contains(lifecycleText, `if ! remote_exec "${host}" "${user}" "${port}" "command -v jq`) {
		t.Fatal("lifecycle conditionally omits the pinned remote jq that later built-in helpers require")
	}

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatalf("read sync script: %v", err)
	}
	syncText := string(syncContents)
	if strings.Contains(syncText, "--all-databases") {
		t.Fatal("logical synchronization still overwrites MySQL system schemas")
	}
	for _, required := range []string{"information_schema", "performance_schema", "mysql", "sys", "--databases"} {
		if !strings.Contains(syncText, required) {
			t.Fatalf("logical synchronization system-schema filter missing %q", required)
		}
	}
}

func TestNewNodeBootstrapDoesNotCreateErrantGTIDsBeforeSynchronization(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installContents), "SET sql_log_bin=0;") {
		t.Fatal("local root bootstrap is still written to the new node binlog")
	}
	installText := string(installContents)
	for _, required := range []string{
		"SET GLOBAL read_only=ON;",
		"SET GLOBAL super_read_only=OFF;",
		`managed_authentication_clause="IDENTIFIED WITH mysql_native_password BY"`,
		`CREATE USER IF NOT EXISTS 'root'@'127.0.0.1' ${managed_authentication_clause}`,
		`ALTER USER 'root'@'127.0.0.1' ${managed_authentication_clause}`,
		`GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION`,
		"bootstrap_status=$?",
		"restore_read_only",
		"SET GLOBAL read_only=ON; SET GLOBAL super_read_only=ON;",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("new-node credential bootstrap is not fail-safe: missing %q", required)
		}
	}
	if strings.Contains(installText, "SET GLOBAL read_only=OFF;") {
		t.Fatal("node installation creates a transient writable database while reconciling local accounts")
	}

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	dump := strings.Index(syncText, `> "${donor_dump_file}"`)
	reset := strings.Index(syncText, "RESET BINARY LOGS AND GTIDS")
	load := strings.Index(syncText, `mysql_target <"${donor_dump_file}"`)
	if dump < 0 || reset < 0 || load < 0 || !(dump < reset && reset < load) || !strings.Contains(syncText, "RESET MASTER") {
		t.Fatalf("logical sync must stage a complete donor dump before clearing target GTIDs and then import it")
	}
}

func TestMySQLInstallerAutoTunesMemoryWithProductionDefaults(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"detect_total_memory_mb", "calculate_memory_tuning", "/proc/meminfo", "MemTotal",
		"seventy_percent_mb=$(((total_memory_mb * 70 + 99) / 100))",
		"eighty_percent_mb=$((total_memory_mb * 80 / 100))",
		"rounded_up_mb=$((((seventy_percent_mb + 4095) / 4096) * 4096))",
		"max_safe_multiple_mb=$(((eighty_percent_mb / 4096) * 4096))",
		"innodb_buffer_pool_mb > max_safe_multiple_mb",
		"requires at least 5 GiB of physical memory",
		"mysql_max_connections=1000",
		"innodb_buffer_pool_size=${innodb_buffer_pool_mb}M",
		"max_connections=${mysql_max_connections}",
		"table_open_cache=${mysql_table_open_cache}",
		"thread_cache_size=${mysql_thread_cache_size}",
		"tmp_table_size=${mysql_tmp_table_size_mb}M",
		"max_heap_table_size=${mysql_tmp_table_size_mb}M",
		"sort_buffer_size=2M", "join_buffer_size=2M",
		"memory_tuning total_mb=",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("MySQL installer is missing bounded memory tuning contract %q", required)
		}
	}
	if strings.Contains(text, "sort_buffer_size=32M") || strings.Contains(text, "join_buffer_size=128M") {
		t.Fatal("MySQL installer copied unsafe per-connection buffers from the legacy script")
	}
	if strings.Contains(text, "innodb_buffer_pool_mb > 131072") {
		t.Fatal("MySQL installer still caps the buffer pool at 128 GiB")
	}

	functionStart := strings.Index(text, "calculate_memory_tuning() {")
	if functionStart < 0 {
		t.Fatal("cannot isolate MySQL memory tuning function for behavior verification")
	}
	functionEnd := strings.Index(text[functionStart:], "\n}\n\ntotal_memory_mb=")
	if functionEnd < 0 {
		t.Fatal("cannot isolate MySQL memory tuning function for behavior verification")
	}
	functionBody := text[functionStart : functionStart+functionEnd+3]
	command := exec.Command("bash", "-c", functionBody+"\ncalculate_memory_tuning \"$1\" >/dev/null\nprintf '%s %s\\n' \"$innodb_buffer_pool_mb\" \"$mysql_max_connections\"", "memory-test", "262144")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run MySQL memory tuning function: %v\n%s", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "184320 1000"; got != want {
		t.Fatalf("256 GiB tuning=%q, want %q (180 GiB buffer pool and 1000 connections)", got, want)
	}

	command = exec.Command("bash", "-c", functionBody+"\ncalculate_memory_tuning \"$1\" >/dev/null\nprintf '%s\\n' \"$innodb_buffer_pool_mb\"", "memory-test", "12288")
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("run 80%% capped MySQL memory tuning function: %v\n%s", err, output)
	}
	if got, want := strings.TrimSpace(string(output)), "8192"; got != want {
		t.Fatalf("12 GiB tuning=%q, want %q (largest 4 GiB multiple below 80%%)", got, want)
	}

	command = exec.Command("bash", "-c", functionBody+"\ncalculate_memory_tuning \"$1\"", "memory-test", "4096")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "requires at least 5 GiB") {
		t.Fatalf("4 GiB node must fail because no 4 GiB multiple fits below 80%%: err=%v output=%s", err, output)
	}
}

func TestMySQLInstallerUsesVersionAwareNativePasswordCompatibility(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`authentication_options="default_authentication_plugin=mysql_native_password"`,
		`if [[ "${release_family}" == "8.4" ]]`,
		`authentication_options="mysql_native_password=ON"`,
		`managed_authentication_clause="IDENTIFIED WITH mysql_native_password BY"`,
		"IDENTIFIED WITH mysql_native_password BY",
		"information_schema.plugins",
		"bind_address=0.0.0.0",
		`--protocol=tcp --host=127.0.0.1 --port="${port}"`,
		`mysql_root_remote_host="$(jq -r '.target.mysql_root_remote_host // ""' "${payload}")"`,
		`if [[ -n "${mysql_root_remote_host}" ]]`,
		`CREATE USER IF NOT EXISTS 'root'@'${mysql_root_remote_host_sql}'`,
		`GRANT ALL PRIVILEGES ON *.* TO 'root'@'${mysql_root_remote_host_sql}' WITH GRANT OPTION`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("MySQL installer is missing authentication compatibility contract %q", required)
		}
	}
	if strings.Contains(text, `CREATE USER IF NOT EXISTS 'root'@'%'`) {
		t.Fatal("MySQL installer must not unconditionally expose root@'%'")
	}
}

func TestMySQLRebuildHasCancellationSafeReseedBoundaryAndVerifiedFastRejoin(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`.last-verified-sync.json`,
		`GTID_SUBSET`,
		`sync_strategy="incremental_rejoin"`,
		`sync_strategy="full_reseed"`,
		`rm -f "${verified_sync_marker}"`,
		`mv -f "${verified_sync_marker_tmp}" "${verified_sync_marker}"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("MySQL rebuild safety boundary is missing %q", required)
		}
	}
	if strings.Count(text, `--batch --raw --skip-column-names -e 'SELECT @@GLOBAL.gtid_executed'`) < 3 {
		t.Fatal("verified fast rejoin must read multiline GTID sets without mysql batch escaping")
	}
	dump := strings.Index(text, `> "${donor_dump_file}"`)
	targetMutation := strings.Index(text, "SET GLOBAL super_read_only=OFF;")
	if dump < 0 || targetMutation < 0 || dump > targetMutation {
		t.Fatal("logical reseed mutates the target before the complete donor dump is staged")
	}
	if strings.Contains(text, "SET GLOBAL read_only=OFF;") {
		t.Fatal("node synchronization exposes the target as writable while replication is being rebuilt")
	}
	mutationSequence := "SET GLOBAL super_read_only=OFF;\nSET GLOBAL read_only=ON;"
	if !strings.Contains(text, mutationSequence) {
		t.Fatal("node synchronization must disable super_read_only before entering the read-only mutation window")
	}
	restoreSequence := "SET GLOBAL read_only=ON;\nSET GLOBAL super_read_only=ON;"
	if strings.Count(text, restoreSequence) < 3 {
		t.Fatal("node synchronization does not restore read_only before super_read_only on every exit boundary")
	}
}

func TestLogicalDumpPurgesStaleTargetSchemasBeforeImport(t *testing.T) {
	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	purge := strings.Index(syncText, "target_database_hex")
	dump := strings.Index(syncText, `> "${donor_dump_file}"`)
	load := strings.Index(syncText, `mysql_target <"${donor_dump_file}"`)
	if purge < 0 || dump < 0 || load < 0 || !(dump < purge && purge < load) {
		t.Fatal("logical rebuild must stage the donor dump, purge target-only user schemas, and then import")
	}
	for _, required := range []string{
		"SELECT HEX(schema_name) FROM information_schema.schemata",
		"DROP DATABASE IF EXISTS",
		"PREPARE cg_drop_schema",
	} {
		if !strings.Contains(syncText, required) {
			t.Fatalf("logical rebuild cannot safely purge stale target schemas: missing %q", required)
		}
	}
}

func TestLogicalDumpExcludesLegacyProbeSchemas(t *testing.T) {
	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	targetPurge := strings.Index(syncText, "target_database_hex")
	donorDump := strings.Index(syncText, "user_databases")
	if targetPurge < 0 || donorDump < 0 || donorDump > targetPurge {
		t.Fatal("logical rebuild must select and stage filtered donor schemas before mutating target schemas")
	}
	for _, legacySchema := range []string{
		"cg_rc28_test",
		"clusterguard_ha_test",
		"clusterguard_matrix_probe",
		"clusterguard_validation",
	} {
		legacyPosition := strings.Index(syncText, legacySchema)
		if legacyPosition < 0 {
			t.Fatalf("logical rebuild must exclude legacy probe schema %q from donor dump", legacySchema)
		}
		if legacyPosition < donorDump {
			t.Fatalf("logical rebuild must filter legacy donor schema %q", legacySchema)
		}
	}
}

func TestMySQLInstallRaisesHostErrorToleranceForControllerProbes(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installContents), "max_connect_errors=10000") {
		t.Fatal("managed MySQL instances can block healthy controllers after a transient probe storm")
	}
}

func TestMySQLInstallFailsBeforeInitializationWhenRuntimeLibrariesAreMissing(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		"validate_runtime_linkage",
		"ldd",
		"not found",
		"MySQL package runtime dependencies are missing",
		"--dependencies",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL installation does not fail closed on missing runtime library %q", required)
		}
	}
	linkageCheck := strings.Index(installText, `validate_runtime_linkage "${mysqld}" "${mysql}"`)
	initialize := strings.Index(installText, `"${mysqld}" --defaults-file="${config_file}" --initialize-insecure --user=mysql`)
	if linkageCheck < 0 || initialize < 0 || linkageCheck > initialize {
		t.Fatal("managed MySQL runtime linkage must be checked before the data directory is initialized")
	}
}

func TestMySQLInstallPublishesStableLocalClientDefaults(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		`system_client_file="${config_dir}/default-client.cnf"`,
		`socket=${socket}`,
		`port=${port}`,
		`!include ${system_client_file}`,
		`ensure_option_file_include /etc/my.cnf 0644`,
		`ensure_option_file_include /root/.my.cnf 0600`,
		`/etc/tmpfiles.d/clusterguard-mysql-${port}.conf`,
		`L+ /tmp/mysql.sock - - - - ${socket}`,
		`ln -sfn "${managed_mysql}" /usr/local/bin/mysql`,
		`ln -sfn "${managed_mysqladmin}" /usr/local/bin/mysqladmin`,
		`ln -sfn "${managed_mysqldump}" /usr/local/bin/mysqldump`,
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL local client compatibility is missing %q", required)
		}
	}
	privateStart := strings.Index(installText, "write_managed_private_client() {")
	privateEnd := strings.Index(installText, "\n}\n\nensure_option_file_include()")
	if privateStart < 0 || privateEnd < privateStart ||
		!strings.Contains(installText[privateStart:privateEnd], "password=${root_password}") ||
		!strings.Contains(installText[privateStart:privateEnd], "socket=${socket}\nport=${port}") {
		t.Fatal("private managed client defaults must retain credentials, socket, and port")
	}
	localStart := strings.Index(installText, "install_local_client_defaults() {")
	localEnd := strings.Index(installText, "\n}\n\nreconcile_control_accounts()")
	if localStart < 0 || localEnd < localStart {
		t.Fatal("managed local client defaults function is not structurally identifiable")
	}
	if strings.Contains(installText[localStart:localEnd], "password=") ||
		strings.Contains(installText[localStart:localEnd], "root_password") {
		t.Fatal("world-readable local client defaults must not contain MySQL credentials")
	}
	fastPathStart := strings.Index(installText, `if [[ -n "${mysql_client}" && -f "${client_file}" ]]`)
	fastPathEnd := strings.Index(installText, "existing MySQL instance accepted for synchronization")
	if fastPathStart < 0 || fastPathEnd < fastPathStart ||
		!strings.Contains(installText[fastPathStart:fastPathEnd], "install_local_client_defaults") {
		t.Fatal("an idempotent installer retry does not repair local MySQL client defaults")
	}
}

func TestMySQLInstallEnablesCrashSafeReplicationDurability(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		"relay_log_recovery=ON",
		"sync_binlog=1",
		"innodb_flush_log_at_trx_commit=1",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL durability configuration is missing %q", required)
		}
	}
}

func TestMySQLInstallUsesManagedTransactionAndOwnsTheDataParent(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		`data_root="$(jq -r '.target.data_root // "/var/lib/clusterguard"' "${payload}")"`,
		`install_parent="${data_root}/mysql/${port}"`,
		`state_root="/var/lib/clusterguard"`,
		`log_root="/var/log/clusterguard"`,
		`config_root="/etc/clusterguard"`,
		`managed_marker="${install_parent}/.clusterguard-managed"`,
		`installing_marker="${install_parent}/.installing"`,
		`ensure_mysql_traverse "${state_root}"`,
		`ensure_mysql_traverse "${log_root}"`,
		`ensure_mysql_traverse "${config_root}"`,
		`ensure_mysql_traverse "${config_dir}"`,
		`setfacl -m u:mysql:--x "${directory}"`,
		`runuser -u mysql -- test -x "${directory}"`,
		`chown -R mysql:mysql "${install_parent}" "${log_dir}" "${run_dir}"`,
		`chown root:mysql "${config_file}"`,
		`rm -f "${installing_marker}"`,
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL installation transaction is missing %q", required)
		}
	}
	if !strings.Contains(installText, `refusing to replace an unmanaged partial MySQL data directory`) {
		t.Fatal("partial MySQL data can be replaced without durable ClusterGuard ownership evidence")
	}
	if strings.Contains(installText, `chown -R mysql:mysql "${data_dir}" "${log_dir}" "${run_dir}"`) {
		t.Fatal("MySQL install still leaves the protected data parent owned by root")
	}
	stop := strings.Index(installText, `systemctl stop "${service}"`)
	recreate := strings.LastIndex(installText, `mkdir -p "${install_root}/software" "${data_dir}" "${log_dir}" "${run_dir}"`)
	ownership := strings.Index(installText, `chown -R mysql:mysql "${install_parent}" "${log_dir}" "${run_dir}"`)
	if stop < 0 || recreate < 0 || ownership < 0 || recreate < stop || ownership < recreate {
		t.Fatal("managed MySQL retry does not recreate systemd RuntimeDirectory after stopping the prior attempt")
	}
}

func TestMySQLInstallEnablesSemiSyncDurabilityForModernAndLegacyServers(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		"semisync_source.so",
		"semisync_replica.so",
		"rpl_semi_sync_source_enabled=ON",
		"rpl_semi_sync_replica_enabled=ON",
		"rpl_semi_sync_source_wait_for_replica_count=1",
		"rpl_semi_sync_source_wait_point=AFTER_SYNC",
		"semisync_master.so",
		"semisync_slave.so",
		"rpl_semi_sync_master_enabled=ON",
		"rpl_semi_sync_slave_enabled=ON",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL semi-sync configuration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"\nrpl_semi_sync_source_enabled=", "\nrpl_semi_sync_replica_enabled=",
		"\nrpl_semi_sync_master_enabled=", "\nrpl_semi_sync_slave_enabled=",
	} {
		if strings.Contains(installText, forbidden) {
			t.Fatalf("managed MySQL initialization still parses plugin variable before plugin load: %q", forbidden)
		}
	}
	for _, required := range []string{
		"loose-rpl_semi_sync_source_enabled=ON",
		"loose-rpl_semi_sync_replica_enabled=ON",
		"loose-rpl_semi_sync_master_enabled=ON",
		"loose-rpl_semi_sync_slave_enabled=ON",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("managed MySQL semi-sync option is not initialization-safe: missing %q", required)
		}
	}
}

func TestExistingRegisteredMySQLCanBeResynchronizedWithoutManagedInstallPath(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		"CG_MYSQL_CLIENT",
		"command -v mysql",
		"discover_running_mysql_client",
		`ss -H -ltnp "sport = :${port}"`,
		`/proc/${pid}/exe`,
		"existing MySQL instance accepted for synchronization",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("install helper cannot adopt an existing registered MySQL instance: missing %q", required)
		}
	}

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	for _, required := range []string{
		"CG_MYSQL_CLIENT",
		"command -v mysql",
		"discover_running_mysql_client",
		`ss -H -ltnp "sport = :${target_port}"`,
		`/proc/${pid}/exe`,
		"SELECT @@basedir",
		`bin/mysqldump`,
	} {
		if !strings.Contains(syncText, required) {
			t.Fatalf("sync helper cannot use the registered instance's native client tools: missing %q", required)
		}
	}
}

func TestExistingRegisteredPostgreSQLCanBeRebuiltWithSystemToolchain(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{
		"existing PostgreSQL toolchain accepted for rebuild synchronization",
		"command -v pg_basebackup",
		"command -v pg_rewind",
		"command -v pg_controldata",
		"systemctl cat",
		"ln -sfn",
	} {
		if !strings.Contains(installText, required) {
			t.Fatalf("install helper cannot adopt an existing registered PostgreSQL toolchain: missing %q", required)
		}
	}
}

func TestMixedNodeDatabaseRebuildPreservesHealthyControllerRole(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"reuses_node_slot", "clusterguard-ha.service", "existing controller role preserved",
		"existing controller role reactivated after adapter readiness", "CG_ADAPTER_RUNTIME_HELPER",
		"controller adapter runtime is ready",
		`emit_event install succeeded "${engine} adapter runtime is ready on ${node_name}"`,
		`emit_event install succeeded "existing controller role reactivated on ${node_name}"`,
		`emit_event install succeeded "controller role installed on ${node_name}"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("mixed-node rebuild does not preserve an active controller role: missing %q", required)
		}
	}
	preserve := strings.Index(text, "existing controller role preserved")
	join := strings.Index(text, `"${control_helper}" "${request_file}" "${index}"`)
	if preserve < 0 || join < 0 || preserve > join {
		t.Fatal("controller preservation must be checked before invoking the join helper")
	}
}

func TestLifecyclePayloadObjectMergeIsValidForJQ16(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-node-lifecycle.sh")
	if err != nil {
		t.Fatal(err)
	}
	expression := `target:((env.TARGET_JSON|fromjson)+{package_path:env.REMOTE_PACKAGE})`
	if !strings.Contains(string(contents), expression) {
		t.Fatalf("lifecycle payload must parenthesize the complete jq object merge: missing %q", expression)
	}
	command := exec.Command("jq", "-nc", `{target:((env.TARGET_JSON|fromjson)+{package_path:env.REMOTE_PACKAGE})}`)
	command.Env = append(os.Environ(), `TARGET_JSON={"node_name":"cg-node-0001"}`, "REMOTE_PACKAGE=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("jq lifecycle payload expression is not portable: %v\n%s", err, output)
	}
}
