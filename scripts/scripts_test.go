package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	logContents, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logContents)
	for _, expected := range []string{
		"daemon-reload",
		"enable --now clusterguard-ha.service",
		"enable --now clusterguard-agent.service",
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
		"ssh/controller_ed25519": 0o640, "ssh/known_hosts": 0o644,
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

func TestBundleBuildContainsInstallableRuntimeAndChecksums(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"cmd/clusterguard", "cmd/cgctl", "cmd/clusterguard-agent", "SHA256SUMS", "clusterguard-install.sh", "clusterguard-agent-stdio.sh",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("bundle builder missing %q", expected)
		}
	}
}
