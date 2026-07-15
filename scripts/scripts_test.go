package scripts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func fakeInstallBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"clusterguard", "cgctl", "clusterguard-agent"} {
		writeExecutable(t, filepath.Join(root, "bin", name), "#!/usr/bin/env bash\nexit 0\n")
	}
	for _, name := range []string{"clusterguard-node-lifecycle.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh", "clusterguard-preflight.sh", "clusterguard-smoke.sh", "clusterguard-agent-stdio.sh"} {
		writeExecutable(t, filepath.Join(root, "scripts", name), "#!/usr/bin/env bash\nexit 0\n")
	}
	for _, name := range []string{"clusterguard-ha.service", "clusterguard-agent.service", "clusterguard-agent-reconcile.service", "clusterguard-agent-reconcile.timer"} {
		writeFile(t, filepath.Join(root, "packaging", "systemd", name), "[Unit]\nDescription=test\n", 0o644)
	}
	writeFile(t, filepath.Join(root, "packaging", "logrotate", "clusterguard-ha"), "/var/log/clusterguard/*.log {}\n", 0o644)
	return root
}

func installerArguments(bundle, config, environment, agentConfig string) []string {
	return []string{
		"clusterguard-install.sh",
		"--bundle-dir", bundle,
		"--role", "mixed",
		"--node-name", "cg-node-0001",
		"--node-id", "11111111-1111-4111-8111-111111111111",
		"--config", config,
		"--env-file", environment,
		"--agent-config", agentConfig,
	}
}

func TestDeliveryScriptsAreSyntaxValid(t *testing.T) {
	paths := []string{
		"clusterguard-install.sh",
		"clusterguard-agent-stdio.sh",
		"clusterguard-preflight.sh",
		"build-clusterguard-bundle.sh",
		"clusterguard-smoke.sh",
		"clusterguard-ha-matrix.sh",
	}
	for _, path := range paths {
		if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
}

func TestVIPReconcileTimerAttemptsRecoveryWithinThirtySecondWindow(t *testing.T) {
	contents, err := os.ReadFile("../packaging/systemd/clusterguard-agent-reconcile.timer")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{"OnBootSec=5s", "OnUnitActiveSec=5s", "AccuracySec=1s"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("VIP reconcile timer missing %q", expected)
		}
	}
}

func TestAgentStdioWrapperLoadsProtectedEnvironmentBeforeAgent(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-agent-stdio.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"source /etc/clusterguard/agent.env",
		"exec /usr/local/bin/clusterguard-agent \"$@\"",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("agent stdio wrapper is missing %q", expected)
		}
	}
}

func TestInstallerDefaultsToPreflightAndRequiresExplicitExecute(t *testing.T) {
	bundle := fakeInstallBundle(t)
	input := t.TempDir()
	config := filepath.Join(input, "clusterguard.json")
	environment := filepath.Join(input, "clusterguard.env")
	agentConfig := filepath.Join(input, "agent.json")
	writeFile(t, config, `{"metadata_path":"/var/lib/clusterguard/metadata.json"}`, 0o600)
	writeFile(t, environment, "CG_CONTROL_TOKEN=top-secret\n", 0o600)
	writeFile(t, agentConfig, `{"shared_secret_env":"CG_AGENT_SHARED_SECRET"}`, 0o600)
	installRoot := filepath.Join(t.TempDir(), "root")

	command := exec.Command("bash", installerArguments(bundle, config, environment, agentConfig)...)
	command.Env = append(os.Environ(), "CG_INSTALL_ROOT="+installRoot, "CG_PREFLIGHT_SKIP_RUNTIME=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("preflight installer: %v\n%s", err, output)
	}
	text := string(output)
	for _, expected := range []string{"ClusterGuard HA installation plan", "mode: preflight", "--execute"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("preflight output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "top-secret") {
		t.Fatalf("preflight exposed secret: %s", text)
	}
	if _, err := os.Stat(filepath.Join(installRoot, "usr", "local", "bin", "clusterguard")); !os.IsNotExist(err) {
		t.Fatalf("default preflight mutated install root: %v", err)
	}
}

func TestInstallerWritesProtectedEnvironmentAndDefersAgentReconcileByDefault(t *testing.T) {
	bundle := fakeInstallBundle(t)
	input := t.TempDir()
	config := filepath.Join(input, "clusterguard.json")
	environment := filepath.Join(input, "clusterguard.env")
	agentConfig := filepath.Join(input, "agent.json")
	writeFile(t, config, `{"metadata_path":"/var/lib/clusterguard/metadata.json"}`, 0o600)
	writeFile(t, environment, "CG_CONTROL_TOKEN=top-secret\n", 0o600)
	writeFile(t, agentConfig, `{"shared_secret_env":"CG_AGENT_SHARED_SECRET"}`, 0o600)
	installRoot := filepath.Join(t.TempDir(), "root")
	systemctlLog := filepath.Join(t.TempDir(), "systemctl.log")
	fakeSystemctl := filepath.Join(t.TempDir(), "systemctl")
	writeExecutable(t, fakeSystemctl, "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>\"${CG_SYSTEMCTL_LOG}\"\n")

	arguments := append(installerArguments(bundle, config, environment, agentConfig), "--execute")
	command := exec.Command("bash", arguments...)
	command.Env = append(os.Environ(),
		"CG_INSTALL_ROOT="+installRoot,
		"CG_PREFLIGHT_SKIP_RUNTIME=1",
		"CG_SYSTEMCTL="+fakeSystemctl,
		"CG_SYSTEMCTL_LOG="+systemctlLog,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute installer: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "top-secret") {
		t.Fatalf("execute installer exposed secret: %s", output)
	}
	installedEnvironment := filepath.Join(installRoot, "etc", "clusterguard", "clusterguard.env")
	info, err := os.Stat(installedEnvironment)
	if err != nil {
		t.Fatalf("installed environment: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("environment mode=%#o, want 0640", info.Mode().Perm())
	}
	for _, helper := range []string{"clusterguard-node-lifecycle.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh"} {
		info, err := os.Stat(filepath.Join(installRoot, "usr", "local", "libexec", helper))
		if err != nil {
			t.Fatalf("installed lifecycle helper %s: %v", helper, err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("lifecycle helper %s mode=%#o, want 0755 so the unprivileged controller can execute it", helper, info.Mode().Perm())
		}
	}
	logContents, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logContents)
	for _, expected := range []string{
		"daemon-reload",
		"enable clusterguard-ha.service",
		"restart clusterguard-ha.service",
		"enable clusterguard-agent.service",
		"restart clusterguard-agent.service",
		"disable --now clusterguard-agent-reconcile.timer",
	} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("systemd activation missing %q:\n%s", expected, logText)
		}
	}
	if strings.Contains(logText, "enable --now clusterguard-agent-reconcile.timer") {
		t.Fatalf("installer activated reconcile before VIP metadata and leases were ready:\n%s", logText)
	}
}

func TestInstallerActivatesAgentReconcileOnlyWhenExplicitlyRequested(t *testing.T) {
	bundle := fakeInstallBundle(t)
	input := t.TempDir()
	config := filepath.Join(input, "clusterguard.json")
	environment := filepath.Join(input, "clusterguard.env")
	agentConfig := filepath.Join(input, "agent.json")
	writeFile(t, config, `{"metadata_path":"/var/lib/clusterguard/metadata.json"}`, 0o600)
	writeFile(t, environment, "CG_CONTROL_TOKEN=top-secret\n", 0o600)
	writeFile(t, agentConfig, `{"shared_secret_env":"CG_AGENT_SHARED_SECRET"}`, 0o600)
	installRoot := filepath.Join(t.TempDir(), "root")
	systemctlLog := filepath.Join(t.TempDir(), "systemctl.log")
	fakeSystemctl := filepath.Join(t.TempDir(), "systemctl")
	writeExecutable(t, fakeSystemctl, "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>\"${CG_SYSTEMCTL_LOG}\"\n")

	arguments := append(installerArguments(bundle, config, environment, agentConfig), "--activate-agent-reconcile", "--execute")
	command := exec.Command("bash", arguments...)
	command.Env = append(os.Environ(),
		"CG_INSTALL_ROOT="+installRoot,
		"CG_PREFLIGHT_SKIP_RUNTIME=1",
		"CG_SYSTEMCTL="+fakeSystemctl,
		"CG_SYSTEMCTL_LOG="+systemctlLog,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute installer with reconcile activation: %v\n%s", err, output)
	}
	logContents, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logContents), "enable --now clusterguard-agent-reconcile.timer") {
		t.Fatalf("explicit reconcile activation was not honored:\n%s", logContents)
	}
}

func TestInstallerMakesControllerConfigReadableByServiceGroup(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"chown root:clusterguard /etc/clusterguard\n",
		"chown root:clusterguard /etc/clusterguard/clusterguard.json",
		"find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*_ed25519' -exec chown clusterguard:clusterguard {} +",
		"find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*_ed25519' -exec chmod 0600 {} +",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("installer is missing service-readable configuration ownership: %q", expected)
		}
	}
}

func TestInstallerCopiesOnlySupportedRuntimeAssetsWithProtectedModes(t *testing.T) {
	bundle := fakeInstallBundle(t)
	input := t.TempDir()
	config := filepath.Join(input, "clusterguard.json")
	environment := filepath.Join(input, "clusterguard.env")
	agentConfig := filepath.Join(input, "agent.json")
	assets := filepath.Join(input, "assets")
	writeFile(t, config, `{"metadata_path":"/var/lib/clusterguard/metadata.json"}`, 0o600)
	writeFile(t, environment, "CG_CONTROL_TOKEN=top-secret\n", 0o600)
	writeFile(t, agentConfig, `{"shared_secret_env":"CG_AGENT_SHARED_SECRET"}`, 0o600)
	writeFile(t, filepath.Join(assets, "tls", "ca.crt"), "ca", 0o600)
	writeFile(t, filepath.Join(assets, "tls", "server.crt"), "certificate", 0o600)
	writeFile(t, filepath.Join(assets, "tls", "server.key"), "private-key", 0o600)
	writeFile(t, filepath.Join(assets, "ssh", "controller_ed25519"), "private-key", 0o600)
	writeFile(t, filepath.Join(assets, "ssh", "known_hosts"), "host-key", 0o600)
	writeFile(t, filepath.Join(assets, "mysql", "3306-client.cnf"), "[client]\npassword=secret\n", 0o600)
	writeFile(t, filepath.Join(assets, "._tls"), "apple-double", 0o600)
	writeFile(t, filepath.Join(assets, "tls", "._server.crt"), "apple-double", 0o600)
	writeFile(t, filepath.Join(assets, ".DS_Store"), "finder-metadata", 0o600)
	installRoot := filepath.Join(t.TempDir(), "root")
	fakeSystemctl := filepath.Join(t.TempDir(), "systemctl")
	writeExecutable(t, fakeSystemctl, "#!/usr/bin/env bash\nexit 0\n")

	arguments := append(installerArguments(bundle, config, environment, agentConfig), "--assets-dir", assets, "--execute")
	command := exec.Command("bash", arguments...)
	command.Env = append(os.Environ(),
		"CG_INSTALL_ROOT="+installRoot,
		"CG_PREFLIGHT_SKIP_RUNTIME=1",
		"CG_SYSTEMCTL="+fakeSystemctl,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install runtime assets: %v\n%s", err, output)
	}
	for path, mode := range map[string]os.FileMode{
		"tls/ca.crt": 0o644, "tls/server.crt": 0o644, "tls/server.key": 0o640,
		"ssh/controller_ed25519": 0o600, "ssh/known_hosts": 0o644,
		"mysql/3306-client.cnf": 0o600,
	} {
		info, err := os.Stat(filepath.Join(installRoot, "etc", "clusterguard", path))
		if err != nil {
			t.Fatalf("installed asset %s: %v", path, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("asset %s mode=%#o, want %#o", path, info.Mode().Perm(), mode)
		}
	}
	for _, path := range []string{"._tls", "tls/._server.crt", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(installRoot, "etc", "clusterguard", path)); !os.IsNotExist(err) {
			t.Fatalf("packaging metadata %s was installed: %v", path, err)
		}
	}
}

func TestSmokeChecksOneWriterOneVIPAndReplicaThreads(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"writer_count", "vip_owner_count", "vip_owner_is_primary", "replica_threads_healthy",
		"topology_fresh", "/api/v1/clusters/${cluster_id}/topology", "/api/v1/clusters/${cluster_id}/ha-endpoints",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("smoke script missing invariant %q", expected)
		}
	}
}

func TestHAMatrixRunsSeededCoveredConcurrentSwitchesWithSmoke(t *testing.T) {
	clusterIDs := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	}
	type execution struct {
		ClusterID string
		TargetID  string
		Approval  string
	}
	var (
		active, maximumActive int32
		executionMu           sync.Mutex
		executions            []execution
		operationOrdinal      int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer matrix-control" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			clusterID := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/clusters/"), "/")[0]
			_, _ = fmt.Fprintf(writer, `{"status":"ok","result":[{"instance_id":"%s","eligible":true,"rank":1},{"instance_id":"%s","eligible":true,"rank":2}]}`,
				clusterID[:8]+"-aaaa-4aaa-8aaa-aaaaaaaaaaa1", clusterID[:8]+"-bbbb-4bbb-8bbb-bbbbbbbbbbb2")
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			current := atomic.AddInt32(&active, 1)
			for {
				observed := atomic.LoadInt32(&maximumActive)
				if current <= observed || atomic.CompareAndSwapInt32(&maximumActive, observed, current) {
					break
				}
			}
			defer atomic.AddInt32(&active, -1)
			payload := struct {
				Operation struct {
					ClusterID string `json:"cluster_id"`
				} `json:"operation"`
				TargetID      string `json:"target_id"`
				ApprovalToken string `json:"approval_token"`
			}{}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			executionMu.Lock()
			executions = append(executions, execution{ClusterID: payload.Operation.ClusterID, TargetID: payload.TargetID, Approval: payload.ApprovalToken})
			executionMu.Unlock()
			time.Sleep(100 * time.Millisecond)
			ordinal := atomic.AddInt32(&operationOrdinal, 1)
			_, _ = fmt.Fprintf(writer, `{"status":"ok","result":{"resource_id":"operation-%d","status":"succeeded"}}`, ordinal)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	smokeLog := filepath.Join(t.TempDir(), "smoke.log")
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  case "$1" in
    --cluster) cluster="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "${cluster}" >>"${CG_MATRIX_SMOKE_LOG}"
`)
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", strings.Join(clusterIDs, ","),
		"--round-robin", "2",
		"--random", "2",
		"--seed", "11",
		"--parallel", "2",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"CG_MATRIX_SMOKE_LOG="+smokeLog,
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "matrix_summary total=4 passed=4 failed=0 seed=11") {
		t.Fatalf("matrix summary missing:\n%s", output)
	}
	if atomic.LoadInt32(&maximumActive) < 2 {
		t.Fatalf("matrix did not execute independent clusters concurrently: maximum=%d", maximumActive)
	}
	if len(executions) != 4 {
		t.Fatalf("executions=%d, want 4", len(executions))
	}
	byCluster := map[string][]string{}
	for _, execution := range executions {
		if execution.Approval != "matrix-approval" {
			t.Fatalf("approval token was not forwarded: %+v", execution)
		}
		byCluster[execution.ClusterID] = append(byCluster[execution.ClusterID], execution.TargetID)
	}
	for _, clusterID := range clusterIDs {
		targets := byCluster[clusterID]
		if len(targets) != 2 || targets[0] == targets[1] {
			t.Fatalf("cluster %s targets=%v, want two rotated candidates", clusterID, targets)
		}
	}
	smokeContents, err := os.ReadFile(smokeLog)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(smokeContents)); len(lines) != 4 {
		t.Fatalf("smoke checks=%d, want 4: %s", len(lines), smokeContents)
	}
}

func TestHAMatrixRetriesTransientCandidateRead(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	var candidateReads int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			if atomic.AddInt32(&candidateReads, 1) == 1 {
				http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-1","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"CG_MATRIX_READ_ATTEMPTS=2",
		"CG_MATRIX_READ_RETRY_INTERVAL=0",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix transient candidate read: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&candidateReads) != 2 {
		t.Fatalf("candidate reads=%d, want 2\n%s", candidateReads, output)
	}
	if !strings.Contains(string(output), "api_read_retry attempt=1") {
		t.Fatalf("candidate read retry diagnostic missing:\n%s", output)
	}
	if !strings.Contains(string(output), "matrix_summary total=1 passed=1 failed=0") {
		t.Fatalf("matrix summary missing after transient candidate read:\n%s", output)
	}
}

func TestHAMatrixWaitsForSmokeConvergence(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-1","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	attemptFile := filepath.Join(t.TempDir(), "smoke-attempts")
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), `#!/usr/bin/env bash
set -euo pipefail
attempt=0
if [[ -f "${CG_MATRIX_SMOKE_ATTEMPT_FILE}" ]]; then
  attempt="$(cat "${CG_MATRIX_SMOKE_ATTEMPT_FILE}")"
fi
attempt=$((attempt + 1))
printf '%s\n' "${attempt}" >"${CG_MATRIX_SMOKE_ATTEMPT_FILE}"
if ((attempt < 3)); then
  printf '{"checks":[{"name":"topology_fresh","status":"fail"}]}\n'
  exit 1
fi
`)
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
		"--smoke-attempts", "3",
		"--smoke-interval", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"CG_MATRIX_SMOKE_ATTEMPT_FILE="+attemptFile,
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix convergence retry: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(attemptFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != "3" {
		t.Fatalf("smoke attempts=%q, want 3", contents)
	}
}

func TestHAMatrixRetriesFreshOperationAfterStalePlan(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			if atomic.AddInt32(&attempts, 1) == 1 {
				writer.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprint(writer, `{"status":"error","message":"topology observation is unavailable or changed","result":{"resource_id":"stale-operation","stage":"lock","failure_class":"stale_plan"}}`)
				return
			}
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-2","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix stale-plan retry: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&attempts) != 2 || !strings.Contains(string(output), "switch_retry ordinal=0") || !strings.Contains(string(output), "reason=stale_plan") {
		t.Fatalf("stale plan was not retried once with a fresh operation: attempts=%d\n%s", attempts, output)
	}
	if !strings.Contains(string(output), "matrix_summary total=1 passed=1 failed=0") {
		t.Fatalf("matrix summary missing after stale-plan retry:\n%s", output)
	}
}

func TestHAMatrixFollowsRaftLeaderForMutation(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	var followerPosts, leaderPosts int32
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			atomic.AddInt32(&leaderPosts, 1)
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-leader","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer leader.Close()
	follower := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			atomic.AddInt32(&followerPosts, 1)
			writer.Header().Set("X-ClusterGuard-Leader-Address", "127.0.0.1:10009")
			writer.Header().Set("X-ClusterGuard-Leader-API-Address", leader.URL)
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(writer, `{"status":"blocked","message":"mutation requires the current Raft leader with controller quorum"}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer follower.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", follower.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix leader follow: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&followerPosts) != 1 || atomic.LoadInt32(&leaderPosts) != 1 {
		t.Fatalf("leader routing posts follower=%d leader=%d\n%s", followerPosts, leaderPosts, output)
	}
	if !strings.Contains(string(output), "api_leader_retry") || !strings.Contains(string(output), "to="+leader.URL) {
		t.Fatalf("leader routing diagnostic missing:\n%s", output)
	}
}

func TestHAMatrixReconcilesTimedOutMutationWithoutSubmittingAgain(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	operationID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var posts, lookups int32
	var keyMu sync.Mutex
	operationKey := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			atomic.AddInt32(&posts, 1)
			payload := struct {
				IdempotencyKey string `json:"idempotency_key"`
			}{}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			keyMu.Lock()
			operationKey = payload.IdempotencyKey
			keyMu.Unlock()
			time.Sleep(1500 * time.Millisecond)
			_, _ = fmt.Fprintf(writer, `{"status":"ok","result":{"resource_id":"%s","idempotency_key":"%s","status":"succeeded"}}`, operationID, payload.IdempotencyKey)
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/api/v1/operations" {
			atomic.AddInt32(&lookups, 1)
			keyMu.Lock()
			persistedKey := operationKey
			keyMu.Unlock()
			if persistedKey == "" || request.URL.Query().Get("idempotency_key") != persistedKey {
				http.Error(writer, "operation not found", http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprintf(writer, `{"status":"ok","result":{"resource_id":"%s","idempotency_key":"%s","status":"succeeded"}}`, operationID, persistedKey)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
		"--api-timeout", "1",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_APPROVAL_TOKEN=matrix-approval",
		"CG_MATRIX_RECONCILE_ATTEMPTS=2",
		"CG_MATRIX_RECONCILE_INTERVAL=0",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix timeout reconciliation: %v posts=%d lookups=%d\n%s", err, posts, lookups, output)
	}
	if atomic.LoadInt32(&posts) != 1 {
		t.Fatalf("timed out mutation was submitted %d times, want exactly once\n%s", posts, output)
	}
	if atomic.LoadInt32(&lookups) == 0 || !strings.Contains(string(output), "api_transport_reconciled operation="+operationID+" status=succeeded") {
		t.Fatalf("timed out mutation was not reconciled by idempotency key: lookups=%d\n%s", lookups, output)
	}
	if !strings.Contains(string(output), "matrix_summary total=1 passed=1 failed=0") {
		t.Fatalf("matrix summary missing after timeout reconciliation:\n%s", output)
	}
}

func TestHAMatrixRejectsDuplicateClusterInventory(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	command := exec.Command("bash", "clusterguard-ha-matrix.sh", "--clusters", clusterID+","+clusterID, "--round-robin", "0", "--random", "0")
	command.Env = append(os.Environ(), "CG_CONTROL_TOKEN=matrix-control", "CG_APPROVAL_TOKEN=matrix-approval")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cluster UUIDs must be unique") {
		t.Fatalf("duplicate inventory was not rejected: err=%v output=%s", err, output)
	}
}

func TestHAMatrixReportsSafeOperationEvidenceForHTTPFailure(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	operationID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(writer, `{"status":"indeterminate","message":"operation requires operator review","debug":"never-print-matrix-approval","result":{"resource_id":"%s","stage":"VERIFY","failure_class":"promoted_unverified","verification":{"checks":[{"name":"writer_endpoint_owner","status":"fail"},{"name":"target_writable","status":"fail"}]}}}`, operationID)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(), "CG_CONTROL_TOKEN=matrix-control", "CG_APPROVAL_TOKEN=matrix-approval")
	output, err := command.CombinedOutput()
	text := string(output)
	if err == nil {
		t.Fatalf("HTTP 500 matrix execution unexpectedly passed:\n%s", text)
	}
	for _, expected := range []string{
		"api_error http=500 status=indeterminate",
		"operation=" + operationID,
		"stage=VERIFY",
		"failure_class=promoted_unverified",
		"failed_checks=target_writable,writer_endpoint_owner",
		"switch_failed ordinal=0 cluster=" + clusterID,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("matrix diagnostics missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "never-print-matrix-approval") || strings.Contains(text, "matrix-approval") {
		t.Fatalf("matrix diagnostics leaked hidden response or approval data:\n%s", text)
	}
}

func TestBundleBuildContainsInstallableRuntimeAndChecksums(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"cmd/clusterguard", "cmd/cgctl", "cmd/clusterguard-agent", "SHA256SUMS", "clusterguard-install.sh", "clusterguard-agent-stdio.sh", "COPYFILE_DISABLE=1", "--no-xattrs",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("bundle builder missing %q", expected)
		}
	}
}
