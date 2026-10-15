package scripts

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runInstallerProgressShell(t *testing.T, body string, environment ...string) (string, error) {
	t.Helper()
	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
`+body)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestInstallerMySQLProgressRelaysOnlyFixedMetadata(t *testing.T) {
	output, err := runInstallerProgressShell(t, `
remote_exec() {
  printf '%s\n' \
    'MySQL 3306: checking database archive' \
    'raw helper output with credential=do-not-print' \
    'MySQL 3306: archive verified (39 seconds)' \
    'MySQL 3306: extracting database software' \
    'MySQL 3306: software extracted (42 seconds)' \
    'MySQL 3306: validation started' \
    'MySQL 3306: validation finished (exit=0, 1 seconds)' \
    'MySQL 3306: initialization started' \
    'MySQL 3306: initialization finished (exit=0, 18 seconds)' \
    'MySQL 3306: starting database service' \
    'MySQL 3306: database service ready (2 seconds)' \
    'MySQL unknown: checking database archive' \
    'MySQL 3306: archive verified (39 seconds) trailing-noise'
}
remote_exec_checked node-a mysql-install 'ignored command' mysql-install
printf 'DONE\n'
`)
	if err != nil {
		t.Fatalf("MySQL progress relay failed: %v\n%s", err, output)
	}
	for _, expected := range []string{
		"MySQL 3306: checking database archive",
		"MySQL 3306: archive verified (39 seconds)",
		"MySQL 3306: extracting database software",
		"MySQL 3306: software extracted (42 seconds)",
		"MySQL 3306: validation started",
		"MySQL 3306: validation finished (exit=0, 1 seconds)",
		"MySQL 3306: initialization started",
		"MySQL 3306: initialization finished (exit=0, 18 seconds)",
		"MySQL 3306: starting database service",
		"MySQL 3306: database service ready (2 seconds)",
		"DONE",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("allowed progress line %q was not relayed:\n%s", expected, output)
		}
	}
	for _, forbidden := range []string{"do-not-print", "raw helper output", "MySQL unknown", "trailing-noise"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("untrusted helper output %q leaked on success:\n%s", forbidden, output)
		}
	}
}

func TestInstallerMySQLProgressRelaysBeforeRemoteCompletes(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release-remote")
	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
remote_exec() {
  printf '%s\n' 'raw credential=do-not-stream'
  printf '%s\n' 'MySQL 3306: checking database archive'
  while [[ ! -e "$CG_TEST_RELEASE" ]]; do sleep 0.05; done
  printf '%s\n' 'raw helper completion'
}
remote_exec_checked node-a mysql-install 'ignored command' mysql-install
printf 'REMOTE_FINISHED\n'
`)
	command.Env = append(os.Environ(), "CG_TEST_RELEASE="+release)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	lineResult := make(chan string, 1)
	readError := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			readError <- err
			return
		}
		lineResult <- line
	}()
	select {
	case line := <-lineResult:
		if strings.TrimSpace(line) != "MySQL 3306: checking database archive" {
			_ = os.WriteFile(release, nil, 0o600)
			_ = command.Wait()
			t.Fatalf("first live output was not constrained progress metadata: %q", line)
		}
	case err := <-readError:
		_ = os.WriteFile(release, nil, 0o600)
		_ = command.Wait()
		t.Fatalf("read live progress: %v\nstderr=%s", err, stderr.String())
	case <-time.After(2 * time.Second):
		_ = os.WriteFile(release, nil, 0o600)
		_ = command.Wait()
		t.Fatalf("progress was not relayed before remote completion\nstderr=%s", stderr.String())
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	remainder, err := reader.ReadString(0)
	if err != nil && len(remainder) == 0 {
		_ = command.Wait()
		t.Fatalf("read relay completion: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("progress relay command failed: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(remainder, "REMOTE_FINISHED") || strings.Contains(remainder, "do-not-stream") || strings.Contains(remainder, "raw helper") {
		t.Fatalf("unexpected output after live progress: %q", remainder)
	}
}

func TestInstallerMySQLProgressFailurePreservesExitAndRedactsCapturedOutput(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, "deployment-secrets.env")
	if err := os.WriteFile(secrets, []byte("CG_NODE_MYSQL_ROOT_PASSWORD=top-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runInstallerProgressShell(t, `
secrets_file="$CG_TEST_SECRETS"
ssh_password='ssh-secret-value'
remote_exec() {
  printf '%s\n' \
    'MySQL 3306: initialization started' \
    'raw diagnostic top-secret-value ssh-secret-value'
  return 37
}
if remote_exec_checked node-a mysql-install 'ignored command' mysql-install; then
  exit 99
else
  status=$?
fi
printf 'PRESERVED_STATUS=%s\n' "$status"
exit "$status"
`, "CG_TEST_SECRETS="+secrets)
	if err == nil {
		t.Fatalf("failing remote helper unexpectedly succeeded:\n%s", output)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 37 {
		t.Fatalf("remote exit code was not preserved: err=%v\n%s", err, output)
	}
	for _, expected := range []string{
		"MySQL 3306: initialization started",
		"远端步骤失败：mysql-install [node-a] (exit=37)",
		"raw diagnostic <redacted> <redacted>",
		"PRESERVED_STATUS=37",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("failure evidence %q is missing:\n%s", expected, output)
		}
	}
	for _, secret := range []string{"top-secret-value", "ssh-secret-value"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q leaked in failure output:\n%s", secret, output)
		}
	}
}

func TestInstallerRemoteChecksDoNotRelayProgressUnlessExplicitlyEnabled(t *testing.T) {
	output, err := runInstallerProgressShell(t, `
remote_exec() {
  printf '%s\n' \
    'MySQL 3306: checking database archive' \
    'PostgreSQL raw helper output'
}
remote_exec_checked node-a postgresql-install 'ignored command'
printf 'NON_MYSQL_DONE\n'
`)
	if err != nil {
		t.Fatalf("ordinary remote check failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(output) != "NON_MYSQL_DONE" {
		t.Fatalf("non-MySQL output was relayed: %q", output)
	}
}

func TestInstallerEnablesProgressOnlyForMySQLInstallHelper(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, `remote_json_helper "${host}" /usr/local/libexec/clusterguard-mysql-install.sh "" "${payload}" "" mysql-install`) {
		t.Fatal("MySQL install helper does not enable the constrained progress relay")
	}
	for _, helper := range []string{
		"clusterguard-mysql-sync.sh",
		"clusterguard-postgresql-install.sh",
		"clusterguard-postgresql-sync.sh",
	} {
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, helper) && strings.Contains(line, "mysql-install") {
				t.Fatalf("%s unexpectedly enables MySQL progress relay: %s", helper, line)
			}
		}
	}
}
