package scripts

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	for _, name := range []string{
		"clusterguard-node-lifecycle.sh", "clusterguard-package-resolve.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh",
		"clusterguard-mysql-probe-cleanup.sh", "clusterguard-mysql-qualification.sh",
		"clusterguard-adapter-runtime-install.sh", "clusterguard-control-join.sh",
		"clusterguard-postgresql-build.sh", "clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh",
		"clusterguard-preflight.sh", "clusterguard-smoke.sh", "clusterguard-ha-matrix.sh", "clusterguard-agent-stdio.sh",
		"clusterguard-cluster-shutdown.sh", "clusterguard-cluster-restore.sh", "clusterguard-cluster-finalize.sh",
	} {
		writeExecutable(t, filepath.Join(root, "scripts", name), "#!/usr/bin/env bash\nexit 0\n")
	}
	for _, name := range []string{"clusterguard-ha.service", "clusterguard-agent.service", "clusterguard-agent-reconcile.service", "clusterguard-agent-reconcile.timer", "clusterguard-cluster-restore.service", "clusterguard-cluster-finalize.service"} {
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
		"install_clusterguard.sh",
		"clusterguard-install.sh",
		"clusterguard-configure.sh",
		"clusterguard-agent-stdio.sh",
		"clusterguard-preflight.sh",
		"build-clusterguard-bundle.sh",
		"build-clusterguard-rpm.sh",
		"build-clusterguard-offline-kit.sh",
		"build-clusterguard-patch.sh",
		"build-clusterguard-postgresql-deps-pack.sh",
		"clusterguard-offline-deps.sh",
		"clusterguard-clock-mesh.sh",
		"clusterguard-postgresql-build.sh",
		"clusterguard-smoke.sh",
		"clusterguard-ha-matrix.sh",
		"clusterguard-upgrade.sh",
		"clusterguard-update-job.sh",
		"clusterguard-update-prune.sh",
		"clusterguard-mysql-qualification.sh",
		"../deploy/docker-swarm/mysql/bootstrap-replication.sh",
		"../deploy/docker-swarm/mysql/install-mysql-client.sh",
		"../deploy/docker-swarm/mysql/prepare-host.sh",
	}
	for _, path := range paths {
		if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
}

func TestUpdatePrunerKeepsThreeNewestVersionsAndProtectsUnfinishedWork(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	updateRoot := filepath.Join(root, "updates")
	historyRoot := filepath.Join(root, "update-history")
	privateRoot := filepath.Join(root, "private-update-state")
	operationLogRoot := filepath.Join(root, "operation-log")
	if err := os.MkdirAll(updateRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(historyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 8; index++ {
		writeFile(t, filepath.Join(operationLogRoot, fmt.Sprintf("operation-%02d.json", index)), "{}\n", 0o640)
	}

	base := time.Unix(1_700_000_000, 0)
	for index := 1; index <= 5; index++ {
		patchID := fmt.Sprintf("cgupgrade-%02d", index)
		updateDirectory := filepath.Join(updateRoot, patchID)
		historyDirectory := filepath.Join(historyRoot, patchID)
		writeFile(t, filepath.Join(updateDirectory, "status.json"), `{"status":"succeeded","maintenance_active":false}`+"\n", 0o640)
		writeFile(t, filepath.Join(privateRoot, "jobs", patchID, "status.json"), `{"status":"succeeded","maintenance_active":false}`+"\n", 0o600)
		writeFile(t, filepath.Join(updateDirectory, "package.cgpatch"), "signed package\n", 0o640)
		writeFile(t, filepath.Join(updateDirectory, "package.json"), "{}\n", 0o640)
		writeFile(t, filepath.Join(updateDirectory, "events.jsonl"), "{\"status\":\"succeeded\"}\n", 0o640)
		writeFile(t, filepath.Join(updateDirectory, "output.log"), "retained upgrade output\n", 0o640)
		writeFile(t, filepath.Join(historyDirectory, "rollback.rpm"), "rpm\n", 0o600)
		writeFile(t, filepath.Join(historyDirectory, "output.log"), "retained node output\n", 0o600)
		modified := base.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(updateDirectory, modified, modified); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(historyDirectory, modified, modified); err != nil {
			t.Fatal(err)
		}
	}

	activeID := "cgupgrade-active"
	activeDirectory := filepath.Join(updateRoot, activeID)
	writeFile(t, filepath.Join(activeDirectory, "status.json"), `{"status":"failed","maintenance_active":true}`+"\n", 0o640)
	writeFile(t, filepath.Join(privateRoot, "jobs", activeID, "status.json"), `{"status":"failed","maintenance_active":true}`+"\n", 0o600)
	if err := os.Chtimes(activeDirectory, base, base); err != nil {
		t.Fatal(err)
	}
	reviewID := "cgupgrade-review"
	reviewDirectory := filepath.Join(updateRoot, reviewID)
	writeFile(t, filepath.Join(reviewDirectory, "status.json"), `{"status":"failed","maintenance_active":false,"verification_required":true}`+"\n", 0o640)
	writeFile(t, filepath.Join(privateRoot, "jobs", reviewID, "status.json"), `{"status":"failed","maintenance_active":false,"verification_required":true}`+"\n", 0o600)
	if err := os.Chtimes(reviewDirectory, base.Add(time.Second), base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, patchID := range []string{activeID, reviewID} {
		writeFile(t, filepath.Join(updateRoot, patchID, "package.cgpatch"), "protected payload\n", 0o640)
		historyDirectory := filepath.Join(historyRoot, patchID)
		writeFile(t, filepath.Join(historyDirectory, "rollback.rpm"), "protected rollback\n", 0o600)
		if err := os.Chtimes(historyDirectory, base, base); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(updateRoot, "cgupgrade-symlink")); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "clusterguard-update-prune.sh",
		"--update-root", updateRoot,
		"--history-root", historyRoot,
		"--private-root", privateRoot,
		"--protect", "cgupgrade-01")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("prune update packages: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "保留最近 3 个版本") {
		t.Fatalf("default retention was not reported:\n%s", output)
	}

	for _, rootPath := range []string{updateRoot, historyRoot} {
		for _, patchID := range []string{"cgupgrade-01", "cgupgrade-03", "cgupgrade-04", "cgupgrade-05"} {
			if _, err := os.Stat(filepath.Join(rootPath, patchID)); err != nil {
				t.Fatalf("%s should be retained in %s: %v", patchID, rootPath, err)
			}
		}
		if _, err := os.Stat(filepath.Join(rootPath, "cgupgrade-02", "output.log")); err != nil {
			t.Fatalf("old upgrade audit was removed in %s: %v", rootPath, err)
		}
	}
	for _, path := range []string{filepath.Join(updateRoot, "cgupgrade-02", "package.cgpatch"), filepath.Join(historyRoot, "cgupgrade-02", "rollback.rpm")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old installation payload remains: %s", path)
		}
	}
	for _, name := range []string{"package.json", "status.json", "events.jsonl", "output.log"} {
		if _, err := os.Stat(filepath.Join(updateRoot, "cgupgrade-02", name)); err != nil {
			t.Fatalf("upgrade audit %s was removed: %v", name, err)
		}
	}
	again := exec.Command("bash", "clusterguard-update-prune.sh", "--update-root", updateRoot, "--history-root", historyRoot, "--private-root", privateRoot, "--protect", "cgupgrade-01")
	if output, err := again.CombinedOutput(); err != nil {
		t.Fatalf("repeat prune: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(updateRoot, "cgupgrade-03", "package.cgpatch")); err != nil {
		t.Fatal("archived audit directories consumed retention slots")
	}
	for _, patchID := range []string{activeID, reviewID} {
		if _, err := os.Stat(filepath.Join(updateRoot, patchID, "package.cgpatch")); err != nil {
			t.Fatalf("unfinished package %s was removed: %v", patchID, err)
		}
		if _, err := os.Stat(filepath.Join(historyRoot, patchID, "rollback.rpm")); err != nil {
			t.Fatalf("unfinished rollback payload %s was removed: %v", patchID, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(updateRoot, "cgupgrade-symlink")); err != nil {
		t.Fatalf("symlink entry outside retention scope was changed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("symlink target outside retention root was changed: %v", err)
	}
	operationLogs, err := os.ReadDir(operationLogRoot)
	if err != nil {
		t.Fatalf("read operation logs after package pruning: %v", err)
	}
	if len(operationLogs) != 8 {
		t.Fatalf("package retention changed operation logs: got %d want 8", len(operationLogs))
	}
}

func TestDockerSwarmMySQLBootstrapPreventsErrantInitializationGTIDs(t *testing.T) {
	stack, err := os.ReadFile("../deploy/docker-swarm/mysql/mysql-stack.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stack), `MYSQL_INITDB_SKIP_TZINFO: "1"`) {
		t.Fatal("Docker Swarm stack does not suppress per-instance time-zone GTIDs")
	}
	bootstrap, err := os.ReadFile("../deploy/docker-swarm/mysql/bootstrap-replication.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(bootstrap)
	for _, required := range []string{
		".clusterguard-replica-gtid-baseline",
		"replication_connection_configuration",
		"RESET BINARY LOGS AND GTIDS",
		"RESET MASTER",
		"refusing destructive GTID reset",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Docker Swarm replica GTID baseline contract missing %q", required)
		}
	}
	reset := strings.Index(text, "RESET BINARY LOGS AND GTIDS")
	attach := strings.LastIndex(text, "CHANGE REPLICATION SOURCE TO")
	if reset < 0 || attach < 0 || reset >= attach {
		t.Fatal("replica GTID baseline must be sanitized before source attachment")
	}
	replicationAccount := strings.Index(text, "CREATE USER IF NOT EXISTS 'cg_replication'@'%'")
	primaryBranch := strings.Index(text, `if [[ "${slot}" == "01" ]]`)
	if replicationAccount < 0 || primaryBranch < 0 || replicationAccount >= primaryBranch || strings.Count(text, "CREATE USER IF NOT EXISTS 'cg_replication'@'%'") != 1 {
		t.Fatal("replication account must be provisioned locally on every promotable node")
	}
}

func generatePatchSigningKey(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is required")
	}
	root := t.TempDir()
	privateKey := filepath.Join(root, "patch-signing.key")
	publicKey := filepath.Join(root, "patch-signing.pub")
	command := exec.Command("openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", privateKey)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate patch key: %v\n%s", err, output)
	}
	command = exec.Command("openssl", "pkey", "-in", privateKey, "-pubout", "-out", publicKey)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("export patch public key: %v\n%s", err, output)
	}
	if err := os.Chmod(privateKey, 0o600); err != nil {
		t.Fatal(err)
	}
	return privateKey, publicKey
}

func TestUpgradePackageBuilderAndInspectorVerifySignedDualRPMBundle(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, publicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)
	patchPath := filepath.Join(root, "clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade")

	command := exec.Command("bash", "build-clusterguard-patch.sh",
		"--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey,
		"--expected-public-key", publicKey, "--output", patchPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build patch: %v\n%s", err, output)
	}
	command = exec.Command("bash", "clusterguard-upgrade.sh", "--package", patchPath, "--trust-key", publicKey, "--inspect")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect patch: %v\n%s", err, output)
	}
	for _, expected := range []string{"signature=verified", "patch_id=cgupgrade-2.2-28-to-2.2-29-x86_64", "source=2.2-28", "target=2.2-29", "rollback=available", "database_mutation=false", "bootstrap=available", "bootstrap_protocol=1"} {
		if !strings.Contains(string(output), expected) {
			t.Fatalf("inspect output missing %q:\n%s", expected, output)
		}
	}
}

func TestUpgradePackageBuilderRejectsSigningKeyOutsideExpectedTrustChain(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, _ := generatePatchSigningKey(t)
	_, unrelatedPublicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	patchPath := filepath.Join(root, "patch.cgupgrade")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)

	output, err := exec.Command("bash", "build-clusterguard-patch.sh",
		"--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey,
		"--expected-public-key", unrelatedPublicKey, "--output", patchPath).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "补丁签名私钥与预期受信公钥不匹配") {
		t.Fatalf("mismatched release key was not rejected: err=%v output=%s", err, output)
	}
	if _, statErr := os.Stat(patchPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mismatched release key produced an artifact: %v", statErr)
	}
}

func TestUpgradePackageHandsOffToVerifiedEmbeddedUpgrader(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, publicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)
	marker := filepath.Join(root, "bootstrap-called")
	bootstrap := filepath.Join(root, "target-upgrader.sh")
	writeFile(t, bootstrap, "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' signed-bootstrap >\"${BOOTSTRAP_MARKER:?}\"\n", 0o755)
	patchPath := filepath.Join(root, "patch.cgupgrade")
	if output, err := exec.Command("bash", "build-clusterguard-patch.sh",
		"--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey,
		"--bootstrap-upgrader", bootstrap, "--output", patchPath).CombinedOutput(); err != nil {
		t.Fatalf("build patch: %v\n%s", err, output)
	}
	command := exec.Command("bash", "clusterguard-upgrade.sh", "--patch", patchPath, "--trust-key", publicKey, "--plan")
	command.Env = append(os.Environ(), "BOOTSTRAP_MARKER="+marker)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap handoff: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(contents)) != "signed-bootstrap" {
		t.Fatalf("signed bootstrap was not executed: contents=%q err=%v", contents, err)
	}
}

func TestUpgradePackageBuilderDoesNotArchiveHostExtendedAttributes(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-patch.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "tar --no-xattrs") {
		t.Fatal("upgrade package builder must exclude host extended attributes")
	}
}

func TestPatchInspectorRejectsTamperedPayload(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, publicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)
	patchPath := filepath.Join(root, "patch.cgpatch")
	if output, err := exec.Command("bash", "build-clusterguard-patch.sh", "--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey, "--output", patchPath).CombinedOutput(); err != nil {
		t.Fatalf("build patch: %v\n%s", err, output)
	}
	extracted := filepath.Join(root, "extracted")
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("tar", "-xzf", patchPath, "-C", extracted).CombinedOutput(); err != nil {
		t.Fatalf("extract patch: %v\n%s", err, output)
	}
	targetPayload := filepath.Join(extracted, "clusterguard-patch", "payload", filepath.Base(toRPM))
	writeFile(t, targetPayload, "tampered-rpm", 0o644)
	tampered := filepath.Join(root, "tampered.cgpatch")
	if output, err := exec.Command("tar", "-C", extracted, "-czf", tampered, "clusterguard-patch").CombinedOutput(); err != nil {
		t.Fatalf("repack patch: %v\n%s", err, output)
	}
	output, err := exec.Command("bash", "clusterguard-upgrade.sh", "--patch", tampered, "--trust-key", publicKey, "--inspect").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "checksum") {
		t.Fatalf("tampered patch was not rejected: err=%v output=%s", err, output)
	}
}

func TestPatchInspectorRejectsTamperedBootstrapUpgrader(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, publicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)
	patchPath := filepath.Join(root, "patch.cgupgrade")
	if output, err := exec.Command("bash", "build-clusterguard-patch.sh", "--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey, "--output", patchPath).CombinedOutput(); err != nil {
		t.Fatalf("build patch: %v\n%s", err, output)
	}
	extracted := filepath.Join(root, "extracted")
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("tar", "-xzf", patchPath, "-C", extracted).CombinedOutput(); err != nil {
		t.Fatalf("extract patch: %v\n%s", err, output)
	}
	writeFile(t, filepath.Join(extracted, "clusterguard-patch", "bootstrap", "clusterguard-upgrade.sh"), "#!/bin/sh\nexit 99\n", 0o755)
	tampered := filepath.Join(root, "tampered.cgupgrade")
	if output, err := exec.Command("tar", "-C", extracted, "-czf", tampered, "clusterguard-patch").CombinedOutput(); err != nil {
		t.Fatalf("repack patch: %v\n%s", err, output)
	}
	output, err := exec.Command("bash", "clusterguard-upgrade.sh", "--patch", tampered, "--trust-key", publicKey, "--inspect").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "bootstrap upgrader checksum mismatch") {
		t.Fatalf("tampered bootstrap was not rejected: err=%v output=%s", err, output)
	}
}

func TestUpgradeScriptNeverInvokesDatabaseClients(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, forbidden := range []string{"mysql -", "mysqld ", "psql ", "pg_ctl ", "sqlplus ", "dgmgrl ", "sqlcmd "} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("software patcher must not invoke database command %q", forbidden)
		}
	}
	for _, required := range []string{"followers", "data-only", "leader", "--oldpackage", "/api/v1/control-plane/status", "active_operations", "active_lifecycle_tasks"} {
		if !strings.Contains(text, required) {
			t.Fatalf("upgrade safety contract missing %q", required)
		}
	}
}

func TestUpgradeScriptEnforcesMaintenanceAndVersionContracts(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"/etc/clusterguard/update-maintenance.json",
		"update_maintenance_active",
		"verify_node_contract",
		"/usr/local/bin/clusterguard --version-json",
		"sshpass -e",
		"--resume",
		"events.jsonl",
		"data_node_members",
		"ip_address",
		"/api/v1/operations",
		"clusterguard-automatic-recovery",
		"stale_operation_threshold_seconds=1800",
		"CG_UPDATE_CLUSTER_IDLE_TIMEOUT_SECONDS",
		"CG_UPDATE_NODE_READY_TIMEOUT_SECONDS",
		"stale_automatic",
		"clusterguard-agent-reconcile.timer",
		"systemctl enable --now 'clusterguard-agent-reconcile.timer'",
		"检测到可安全接管的失败升级维护锁",
		"补偿回锁",
		`[[ -z "${controllers_raw}" ]] || csv_to_array "${controllers_raw}" controllers`,
		`[[ -z "${data_nodes_raw}" ]] || csv_to_array "${data_nodes_raw}" data`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("upgrade execution contract missing %q", required)
		}
	}
	for _, forbidden := range []string{"sshpass -p", `" ${data_nodes[*]} "`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("upgrade script contains unsafe implementation %q", forbidden)
		}
	}
}

func TestUpgradeScriptRollsFollowersDataOnlyThenLeader(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	privateKey, publicKey := generatePatchSigningKey(t)
	root := t.TempDir()
	fromRPM := filepath.Join(root, "clusterguard-ha-2.2-28.x86_64.rpm")
	toRPM := filepath.Join(root, "clusterguard-ha-2.2-29.x86_64.rpm")
	writeFile(t, fromRPM, "rollback-rpm", 0o644)
	writeFile(t, toRPM, "target-rpm", 0o644)
	patchPath := filepath.Join(root, "patch.cgpatch")
	if output, err := exec.Command("bash", "build-clusterguard-patch.sh", "--from-rpm", fromRPM, "--to-rpm", toRPM, "--signing-key", privateKey, "--output", patchPath).CombinedOutput(); err != nil {
		t.Fatalf("build patch: %v\n%s", err, output)
	}
	writeFile(t, filepath.Join(root, "clusterguard-deployment-state.json"), `{"nodes":[{"address":"stale-controller","role":"mixed"}]}`+"\n", 0o600)

	state := filepath.Join(root, "remote-state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fakeBin, "ssh"), `#!/usr/bin/env bash
set -euo pipefail
host=""
for value in "$@"; do
  case "$value" in root@*) host="${value#root@}" ;; esac
done
command="${!#}"
bash -n <<<"$command"
state="${FAKE_REMOTE_STATE:?}"
version="$(tr -d '\n' <"$state/$host.version")"
case "$host" in
  c1) node_id=11111111-1111-4111-8111-111111111111; node_role=mixed ;;
  c2) node_id=22222222-2222-4222-8222-222222222222; node_role=mixed ;;
  c3) node_id=33333333-3333-4333-8333-333333333333; node_role=mixed ;;
  d1) node_id=44444444-4444-4444-8444-444444444444; node_role=data ;;
  *) exit 90 ;;
esac
if [[ "$command" == *"rpm -q --qf"* ]]; then
  printf '%s\n' "$version"
elif [[ "$command" == *"clusterguard --version-json"* ]]; then
  gate_protocol="${FAKE_UPDATE_GATE_PROTOCOL:-1}"
  if [[ "${FAKE_LEGACY_SOURCE:-}" == true && "$version" == 2.2-28 ]]; then gate_protocol=0; fi
  printf '{"product":"ClusterGuard HA","binary":"clusterguard","version":"%s","release":"%s","rpm_architecture":"x86_64","state_format":1,"update_protocol":1,"update_gate_protocol":%s}\n' "${version%-*}" "${version##*-}" "$gate_protocol"
elif [[ "$command" == *"cat /etc/clusterguard/node.json"* ]]; then
  printf '{"resource_id":"%s","node_name":"node-%s","role":"%s"}\n' "$node_id" "$host" "$node_role"
elif [[ "$command" == *"/api/v1/platform/updates/gate/"* ]]; then
  payload="$(printf '%s\n' "$command" | sed -n "s/^.*--data-binary '\([^']*\)'.*$/\1/p")"
  owner="$(jq -r .execution_id <<<"$payload")"
  [[ "$owner" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || exit 60
  if [[ "$command" == *"/gate/acquire"* ]]; then
    if [[ -f "$state/replicated-gate" ]]; then
      existing="$(cat "$state/replicated-gate")"
      [[ "$existing" == "$owner" || "$existing" == "$(jq -r .previous_execution_id <<<"$payload")" ]] || exit 61
    fi
    printf '%s\n' "$owner" >"$state/replicated-gate"
  else
    [[ "$(cat "$state/replicated-gate")" == "$owner" ]] || exit 62
    for controller in c1 c2 c3; do [[ ! -f "$state/$controller.maintenance" ]] || exit 63; done
    rm -f "$state/replicated-gate"
  fi
elif [[ "$command" == *"/api/v1/operations"* ]]; then
  leader_host="${FAKE_LEADER_HOST:-c3}"
  [[ ! -f "$state/leader-host" ]] || leader_host="$(tr -d '\n' <"$state/leader-host")"
  [[ "$host" == "$leader_host" ]] || exit 54
  printf '%s\n' "$host" >>"$state/operation-query-hosts"
  mode="${FAKE_OPERATION_MODE:-none}"
  leader_version="$(tr -d '\n' <"$state/$leader_host.version")"
  if [[ "$mode" == stale-auto && "$leader_version" == 2.2-29 ]]; then mode=none; fi
  case "$mode" in
    none) printf '{"status":"ok","result":[]}\n' ;;
    unreadable) exit 55 ;;
    malformed) printf '{"status":"ok","result":[{"status":"running","raw_payload":"must-not-leak","token":"must-not-leak"}]}\n' ;;
    *)
      requested_by=clusterguard-automatic-recovery
      stage=plan
      updated_at=2020-01-01T00:00:00Z
      [[ "$mode" != manual ]] || requested_by=manual-secret-operator
      [[ "$mode" != fresh ]] || updated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      case "$mode" in execute|verify|audit|report) stage="$mode" ;; esac
      printf '{"status":"ok","result":[{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","operation":{"cluster_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","kind":"failover","requested_by":"%s"},"stage":"%s","status":"running","updated_at":"%s","raw_payload":"must-not-leak","credentials":"must-not-leak","token":"must-not-leak"}]}\n' "$requested_by" "$stage" "$updated_at"
      ;;
  esac
elif [[ "$command" == *"cgctl"*" status"* ]]; then
  maintenance=false
  [[ ! -f "$state/$host.maintenance" && ! -f "$state/replicated-gate" ]] || maintenance=true
  leader_host="${FAKE_LEADER_HOST:-c3}"
  [[ ! -f "$state/leader-host" ]] || leader_host="$(tr -d '\n' <"$state/leader-host")"
  ready=true
  role=follower
  [[ "$host" != "$leader_host" ]] || role=leader
  voter_count=3
  members='[{"resource_id":"11111111-1111-4111-8111-111111111111"},{"resource_id":"22222222-2222-4222-8222-222222222222"},{"resource_id":"33333333-3333-4333-8333-333333333333"}]'
  data_members='[{"resource_id":"11111111-1111-4111-8111-111111111111","ip_address":"c1"},{"resource_id":"22222222-2222-4222-8222-222222222222","ip_address":"c2"},{"resource_id":"33333333-3333-4333-8333-333333333333","ip_address":"c3"},{"resource_id":"44444444-4444-4444-8444-444444444444","ip_address":"d1"}]'
  quorum=false
  [[ "$role" != leader ]] || quorum=true
	active_operations=0
	if [[ "${FAKE_ACTIVE_UNTIL_LEADER_LOCK:-}" == true && ! -f "$state/active-operation-drained" ]]; then
		active_operations=1
	fi
	operation_mode="${FAKE_OPERATION_MODE:-none}"
	leader_version="$(tr -d '\n' <"$state/$leader_host.version")"
	if [[ "$operation_mode" != none && "$operation_mode" != indeterminate && "$operation_mode" != lifecycle ]]; then
		if [[ "$operation_mode" != stale-auto || "$leader_version" != 2.2-29 ]]; then active_operations=1; fi
	fi
	indeterminate_operations=0
	active_lifecycle_tasks=0
	[[ "$operation_mode" != indeterminate ]] || indeterminate_operations=1
	[[ "$operation_mode" != lifecycle ]] || active_lifecycle_tasks=1
	if [[ "$host" == "${FAKE_TRANSIENT_ACTIVE_HOST:-}" && ! -f "$state/transient-active-injected" ]]; then
		active_operations=1
		: >"$state/transient-active-injected"
	fi
  if [[ "${FAKE_CONTAINER_DATA:-}" == true ]]; then
    data_members='[{"resource_id":"71111111-1111-4111-8111-111111111111","ip_address":"c1"},{"resource_id":"71111111-1111-4111-8111-111111111112","ip_address":"c1"},{"resource_id":"72222222-2222-4222-8222-222222222222","ip_address":"c2"},{"resource_id":"73333333-3333-4333-8333-333333333333","ip_address":"c3"},{"resource_id":"74444444-4444-4444-8444-444444444444","ip_address":"d1"}]'
  fi
  if [[ "${FAKE_EXTRA_VOTERS:-}" == true ]]; then
    voter_count=5
    members='[{"resource_id":"11111111-1111-4111-8111-111111111111"},{"resource_id":"22222222-2222-4222-8222-222222222222"},{"resource_id":"33333333-3333-4333-8333-333333333333"},{"resource_id":"55555555-5555-4555-8555-555555555555"},{"resource_id":"66666666-6666-4666-8666-666666666666"}]'
  fi
  if [[ "${FAKE_EXTRA_DATA_NODE:-}" == true ]]; then
    data_members='[{"resource_id":"11111111-1111-4111-8111-111111111111","ip_address":"c1"},{"resource_id":"22222222-2222-4222-8222-222222222222","ip_address":"c2"},{"resource_id":"33333333-3333-4333-8333-333333333333","ip_address":"c3"},{"resource_id":"44444444-4444-4444-8444-444444444444","ip_address":"d1"},{"resource_id":"77777777-7777-4777-8777-777777777777","ip_address":"d2"}]'
  fi
  if [[ "$host" == "${FAKE_PERSISTENT_STATUS_HOST:-}" && "$(tr -d '\n' <"$state/c1.version")" == 2.2-29 ]]; then
    ready=false
  elif [[ "$host" == "${FAKE_TRANSIENT_STATUS_HOST:-}" && "$(tr -d '\n' <"$state/c1.version")" == 2.2-29 && ! -f "$state/transient-status-injected" ]]; then
    ready=false
    : >"$state/transient-status-injected"
  fi
  if [[ "$host" == "${FAKE_STATUS_FAIL_AFTER_HOST:-}" ]]; then
    status_calls=0
    [[ ! -f "$state/$host.status-calls" ]] || status_calls="$(tr -d '\n' <"$state/$host.status-calls")"
    status_calls=$((status_calls + 1))
    printf '%s\n' "$status_calls" >"$state/$host.status-calls"
    if ((status_calls > ${FAKE_STATUS_FAIL_AFTER_CALLS:-999999})); then ready=false; fi
  fi
  printf '{"status":"ok","result":{"ready":%s,"leader_known":true,"quorum_confirmed":%s,"voter_count":%s,"active_operations":%s,"indeterminate_operations":%s,"active_lifecycle_tasks":%s,"update_maintenance_active":%s,"role":"%s","local_controller_id":"%s","controller_members":%s,"data_node_members":%s}}\n' "$ready" "$quorum" "$voter_count" "$active_operations" "$indeterminate_operations" "$active_lifecycle_tasks" "$maintenance" "$role" "$node_id" "$members" "$data_members"
elif [[ "$command" == *".cluster-update.lock"* && "$command" == *"status.json"* && "$command" == *"maintenance_active"* && "$command" != *"patch-id.tmp"* ]]; then
  [[ -n "${FAKE_FAILED_UPDATE_LOCK:-}" ]] || exit 56
  printf '%s\n' "${FAKE_FAILED_UPDATE_LOCK}"
elif [[ "$command" == *".cluster-update.lock/patch-id"* && "$command" == *"grep -Fq"* ]]; then
  [[ -z "${FAKE_FAILED_UPDATE_LOCK:-}" ]] || exit 56
  test -f "$state/$host.maintenance"
  if [[ -f "$state/$host.execution" ]]; then cat "$state/$host.execution"; else printf 'previous-execution\n'; fi
elif [[ "$command" == *"if test -f"*".cluster-update.lock/execution-id"*"else printf 'legacy"* ]]; then
  if [[ -f "$state/$host.execution" ]]; then cat "$state/$host.execution"; else printf 'legacy\n'; fi
elif [[ "$command" == *"tail -n 1"*"/events.jsonl"* ]]; then
  [[ "${FAKE_PREVIOUS_EXECUTION_RUNNING:-false}" != true ]] || exit 64
elif [[ "$command" == *"execution-id.tmp"* ]]; then
  test -f "$state/$host.maintenance"
  owner="$(printf '%s\n' "$command" | sed -n 's/.*"execution_id":"\([^"]*\)".*/\1/p' | head -n 1)"
  [[ -n "$owner" ]] && printf '%s\n' "$owner" >"$state/$host.execution"
elif [[ "$command" == *"--arg execution"* && "$command" != *"rm -f"* ]]; then
  test -f "$state/$host.maintenance"
  owner="$(printf '%s\n' "$command" | sed -n "s/.*--arg execution '\([^']*\)'.*/\1/p")"
  [[ "$(cat "$state/$host.execution")" == "$owner" ]] || exit 65
elif [[ "$command" == *".cluster-update.lock"* && "$command" == *"mkdir"* && "$command" == *"update-maintenance.json"* && "$command" == *"rolling_update"* ]]; then
  : >"$state/$host.maintenance"
  owner="$(printf '%s\n' "$command" | sed -n 's/.*"execution_id":"\([^"]*\)".*/\1/p' | head -n 1)"
  [[ -n "$owner" ]] && printf '%s\n' "$owner" >"$state/$host.execution"
	if [[ "${FAKE_ACTIVE_UNTIL_LEADER_LOCK:-}" == true && "$host" == "${FAKE_LEADER_HOST:-c3}" ]]; then : >"$state/active-operation-drained"; fi
	if [[ "${FAKE_RECORD_LOCK_ORDER:-}" == true ]]; then printf '%s\n' "$host" >>"$state/lock-order"; fi
elif [[ "$command" == *"package.json."* && "$command" == *"sha256sum -c"* ]]; then
  actual="$(sha256sum "$state/$host.metadata.tmp" | awk '{print $1}')"
  [[ "$actual" == "${FAKE_EXPECTED_METADATA_SHA:?}" ]]
  mv -f "$state/$host.package.tmp" "$state/$host.package.cgpatch"
  mv -f "$state/$host.metadata.tmp" "$state/$host.package.json"
elif [[ "$command" == *"package.cgpatch."* && "$command" == *"sha256sum -c"* ]]; then
  [[ "$host" != "${FAKE_PACKAGE_VERIFY_FAIL_HOST:-}" ]] || exit 45
  actual="$(sha256sum "$state/$host.package.tmp" | awk '{print $1}')"
  [[ "$actual" == "${FAKE_EXPECTED_PACKAGE_SHA:?}" ]]
elif [[ "$command" == *"rm -f '/etc/clusterguard/update-maintenance.json.tmp'"* ]]; then
  if [[ "$host" == "${FAKE_RELEASE_FAIL_HOST:-}" && ! -f "$state/release-failure-injected" ]]; then
    : >"$state/release-failure-injected"
    exit 44
  fi
	if [[ "${FAKE_RECORD_LOCK_ORDER:-}" == true ]]; then printf '%s\n' "$host" >>"$state/release-order"; fi
  rm -f "$state/$host.maintenance" "$state/$host.execution"
  if [[ -n "${FAKE_LEADER_SWITCH_ON_RELEASE:-}" && ! -f "$state/release-election-injected" ]]; then
    test -f "$state/replicated-gate"
    printf '%s\n' "$host" >"$state/leader-host"
    : >"$state/release-election-injected"
  fi
elif [[ "$command" == *"rpm -Uvh"* ]]; then
  [[ "$command" == *"flock -x -w 30 8"* ]] || exit 66
  if [[ "$command" == *"2.2-29.x86_64.rpm"* && "$host" == "${FAKE_FAIL_HOST:-}" && ! -f "$state/failure-injected" ]]; then
    : >"$state/failure-injected"
    exit 42
  fi
  if [[ "$command" == *"2.2-29.x86_64.rpm"* ]]; then
    printf '2.2-29\n' >"$state/$host.version"
    if [[ "$host" == "${FAKE_LEADER_SWITCH_ON_HOST:-}" && -n "${FAKE_LEADER_SWITCH_TO:-}" ]]; then
      printf '%s\n' "${FAKE_LEADER_SWITCH_TO}" >"$state/leader-host"
    fi
  else
    printf '2.2-28\n' >"$state/$host.version"
  fi
  printf '%s\n' "$host" >>"$state/install-order"
  if [[ "$command" == *"systemctl enable --now 'clusterguard-agent-reconcile.timer'"* ]]; then
    : >"$state/$host.timer-enabled"
    printf '%s\n' "$host" >>"$state/timer-enable-order"
  fi
  if [[ "$command" == *"systemctl restart 'clusterguard-update-helper.service'"* ]]; then printf '%s\n' "$host" >>"$state/helper-restart-order"; fi
elif [[ "$command" == *"systemctl enable --now 'clusterguard-agent-reconcile.timer'"* ]]; then
  : >"$state/$host.timer-enabled"
  printf '%s\n' "$host" >>"$state/timer-enable-order"
elif [[ "$command" == *"systemctl is-active --quiet clusterguard-agent.service"* && "$command" == *"systemctl is-active --quiet clusterguard-agent-reconcile.timer"* ]]; then
  if [[ "${FAKE_TIMER_REQUIRES_ENABLE:-}" == true ]]; then test -f "$state/$host.timer-enabled"; fi
elif [[ "$command" == *"systemctl restart 'clusterguard-update-helper.service'"* ]]; then
  printf '%s\n' "$host" >>"$state/helper-restart-order"
elif [[ "$command" == *"systemd-run"*"clusterguard-update-helper.service"* ]]; then
  printf '%s\n' "$host" >>"$state/leader-helper-refresh-order"
fi
`, 0o755)
	writeFile(t, filepath.Join(fakeBin, "scp"), `#!/usr/bin/env bash
set -euo pipefail
source_path="${@: -2:1}"
destination="${@: -1}"
remote="${destination#*@}"
host="${remote%%:*}"
remote_path="${remote#*:}"
state="${FAKE_REMOTE_STATE:?}"
case "$remote_path" in
  *package.cgpatch.*.tmp)
    [[ "$host" != "${FAKE_PACKAGE_COPY_FAIL_HOST:-}" ]] || exit 46
    cp "$source_path" "$state/$host.package.tmp"
    ;;
  *package.json.*.tmp)
    cp "$source_path" "$state/$host.metadata.tmp"
    ;;
esac
`, 0o755)
	knownHosts := filepath.Join(root, "known_hosts")
	writeFile(t, knownHosts, "test host keys\n", 0o600)

	upgradeScript, err := filepath.Abs("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_TIMER_REQUIRES_ENABLE=true")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute rolling patch: %v\n%s", err, output)
	}
	order, err := os.ReadFile(filepath.Join(state, "install-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(order)), "c1\nc2\nd1\nc3"; got != want {
		t.Fatalf("rolling order=%q want %q", got, want)
	}
	helperRestartOrder, err := os.ReadFile(filepath.Join(state, "helper-restart-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(helperRestartOrder)), "c1\nc2"; got != want {
		t.Fatalf("non-leader helper restart order=%q want %q", got, want)
	}
	leaderHelperRefreshOrder, err := os.ReadFile(filepath.Join(state, "leader-helper-refresh-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(leaderHelperRefreshOrder)), "c3"; got != want {
		t.Fatalf("Leader helper refresh order=%q want %q", got, want)
	}
	timerEnableOrder, err := os.ReadFile(filepath.Join(state, "timer-enable-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(timerEnableOrder)), "c1\nc2\nd1\nc3"; got != want {
		t.Fatalf("reconcile timer enable order=%q want %q", got, want)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, err := os.ReadFile(filepath.Join(state, host+".version"))
		if err != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s final version=%q err=%v", host, version, err)
		}
		if _, err := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(err) {
			t.Fatalf("%s maintenance marker was not released: %v", host, err)
		}
	}

	if err := os.Remove(filepath.Join(state, "install-order")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_ACTIVE_UNTIL_LEADER_LOCK=true", "FAKE_RECORD_LOCK_ORDER=true")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("active operation did not drain behind maintenance gate: %v\n%s", err, output)
	}
	lockOrder, err := os.ReadFile(filepath.Join(state, "lock-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(lockOrder)), "c3\nc1\nc2"; got != want {
		t.Fatalf("maintenance lock order=%q want %q", got, want)
	}
	releaseOrder, err := os.ReadFile(filepath.Join(state, "release-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(releaseOrder)), "c1\nc2\nc3"; got != want {
		t.Fatalf("maintenance release order=%q want %q", got, want)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s gated-drain version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s gated-drain maintenance marker was not released: %v", host, statErr)
		}
	}

	resetFakeCluster := func(t *testing.T) {
		t.Helper()
		for _, name := range []string{"install-order", "operation-query-hosts", "timer-enable-order", "lock-order", "release-order", "helper-restart-order", "leader-helper-refresh-order", "leader-host", "replicated-gate", "release-election-injected"} {
			_ = os.Remove(filepath.Join(state, name))
		}
		for _, host := range []string{"c1", "c2", "c3", "d1"} {
			writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
			_ = os.Remove(filepath.Join(state, host+".maintenance"))
			_ = os.Remove(filepath.Join(state, host+".execution"))
			_ = os.Remove(filepath.Join(state, host+".timer-enabled"))
			_ = os.Remove(filepath.Join(state, host+".status-calls"))
		}
	}
	runOperationGate := func(t *testing.T, mode string) ([]byte, error) {
		t.Helper()
		command := exec.Command("bash", upgradeScript,
			"--patch", patchPath, "--trust-key", publicKey,
			"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
			"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
		command.Dir = root
		command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
			"FAKE_OPERATION_MODE="+mode, "CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
		return command.CombinedOutput()
	}
	for _, scenario := range []struct {
		name  string
		env   []string
		fails bool
	}{
		{name: "Leader changes during marker release", env: []string{"FAKE_LEADER_SWITCH_ON_RELEASE=true"}},
		{name: "legacy source adopts replicated gate after all nodes upgrade", env: []string{"FAKE_LEGACY_SOURCE=true"}},
		{name: "legacy rollback keeps local maintenance", env: []string{"FAKE_LEGACY_SOURCE=true", "FAKE_FAIL_HOST=c1"}, fails: true},
		{name: "resume rejected while previous execution is rolling back", env: []string{"FAKE_PREVIOUS_EXECUTION_RUNNING=true"}, fails: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			resetFakeCluster(t)
			_ = os.Remove(filepath.Join(state, "failure-injected"))
			args := []string{upgradeScript, "--patch", patchPath, "--trust-key", publicKey, "--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1", "--known-hosts", knownHosts, "-u", "root", "--execute", "--yes"}
			if strings.Contains(scenario.name, "resume") {
				args = append(args, "--resume")
				for _, host := range []string{"c1", "c2", "c3"} {
					writeFile(t, filepath.Join(state, host+".maintenance"), "previous execution\n", 0o600)
				}
			}
			command := exec.Command("bash", args...)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1")
			command.Env = append(command.Env, scenario.env...)
			output, err := command.CombinedOutput()
			if (err != nil) != scenario.fails {
				t.Fatalf("err=%v\n%s", err, output)
			}
			for _, host := range []string{"c1", "c2", "c3"} {
				_, statErr := os.Stat(filepath.Join(state, host+".maintenance"))
				if scenario.fails && statErr != nil {
					t.Fatalf("%s failure removed maintenance: %v", host, statErr)
				}
				if !scenario.fails && !os.IsNotExist(statErr) {
					t.Fatalf("%s success retained maintenance: %v", host, statErr)
				}
			}
			if strings.Contains(scenario.name, "resume") {
				if _, err := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(err) {
					t.Fatal("concurrent resume mutated a node")
				}
			}
		})
	}
	resetFakeCluster(t)
	_ = os.Remove(filepath.Join(state, "failure-injected"))
	runPlanGate := func(t *testing.T, mode string) ([]byte, error) {
		t.Helper()
		command := exec.Command("bash", upgradeScript,
			"--patch", patchPath, "--trust-key", publicKey,
			"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
			"--known-hosts", knownHosts, "-u", "root", "--plan")
		command.Dir = root
		command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
			"FAKE_OPERATION_MODE="+mode, "CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
		return command.CombinedOutput()
	}
	assertLeaderOnlyOperationQueries := func(t *testing.T) {
		t.Helper()
		queries, readErr := os.ReadFile(filepath.Join(state, "operation-query-hosts"))
		if readErr != nil {
			t.Fatalf("operation inventory was not queried: %v", readErr)
		}
		for _, host := range strings.Fields(string(queries)) {
			if host != "c3" {
				t.Fatalf("operation inventory queried non-Leader host %q: %s", host, queries)
			}
		}
	}

	resetFakeCluster(t)
	output, err := runPlanGate(t, "stale-auto")
	if err != nil || !strings.Contains(string(output), "只读计划陈旧自动恢复豁免") || !strings.Contains(string(output), "计划完成，未修改任何节点") {
		t.Fatalf("read-only plan did not break stale-operation bootstrap cycle: err=%v\n%s", err, output)
	}
	assertLeaderOnlyOperationQueries(t)
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("read-only stale-operation plan installed an RPM: %v", statErr)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("read-only stale-operation plan entered maintenance on %s: %v", host, statErr)
		}
	}

	resetFakeCluster(t)
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--resume", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "续跑只允许复用全部控制节点上的同一升级包完整维护锁") {
		t.Fatalf("resume without a complete current-patch lock did not fail closed: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("resume without a lock allowed an RPM mutation: %v", statErr)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--resume", "--rollback", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "--rollback 与 --resume 不能同时使用") {
		t.Fatalf("mutually exclusive resume and rollback flags were accepted: err=%v\n%s", err, output)
	}

	resetFakeCluster(t)
	writeFile(t, filepath.Join(state, "c1.maintenance"), "partial current patch maintenance\n", 0o600)
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--rollback", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "当前升级包维护锁仅在 1/3 个控制节点匹配") ||
		!strings.Contains(string(output), "当前升级包维护锁不完整") {
		t.Fatalf("partial same-patch maintenance lock did not fail closed: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("partial same-patch maintenance lock allowed an RPM mutation: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(state, "c1.maintenance")); statErr != nil {
		t.Fatalf("partial same-patch maintenance lock was altered during rejection: %v", statErr)
	}
	for _, host := range []string{"c2", "c3"} {
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("partial same-patch rejection created a maintenance marker on %s: %v", host, statErr)
		}
	}

	resetFakeCluster(t)
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "current patch maintenance\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--rollback", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_STATUS_FAIL_AFTER_HOST=c2", "FAKE_STATUS_FAIL_AFTER_CALLS=1",
		"CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "ready_not_true") {
		t.Fatalf("post-adoption control-plane failure did not stop rollback: err=%v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); statErr != nil {
			t.Fatalf("adopted maintenance lock on %s was released after a pre-mutation failure: %v", host, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("post-adoption control-plane failure allowed an RPM mutation: %v", statErr)
	}

	resetFakeCluster(t)
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "current patch maintenance\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--rollback", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_OPERATION_MODE=stale-auto")
	output, err = command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "维护恢复   : 复用当前升级包的现有门禁") ||
		!strings.Contains(string(output), "受控回退释放后陈旧自动恢复豁免") {
		t.Fatalf("same-patch rollback did not reuse its maintenance lock: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("same-patch rollback reinstalled an RPM despite every node already matching the source: %v", statErr)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-28" {
			t.Fatalf("%s same-patch rollback final version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s same-patch rollback maintenance marker was not released: %v", host, statErr)
		}
	}

	resetFakeCluster(t)
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_LEADER_SWITCH_ON_HOST=c3", "FAKE_LEADER_SWITCH_TO=c2", "FAKE_RECORD_LOCK_ORDER=true")
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("rolling update did not tolerate a final-node Leader change: err=%v\n%s", err, output)
	}
	releaseOrder, err = os.ReadFile(filepath.Join(state, "release-order"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(releaseOrder)), "c3\nc1\nc2"; got != want {
		t.Fatalf("post-election maintenance release order=%q want %q", got, want)
	}

	resetFakeCluster(t)
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "failed update maintenance\n", 0o600)
	}
	previousPatchID := "cgupgrade-2.2-27-to-2.2-28-x86_64"
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_FAILED_UPDATE_LOCK="+previousPatchID, "FAKE_OPERATION_MODE=stale-auto",
		"CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=1", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
	output, err = command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "维护恢复   : 接管已失败升级 "+previousPatchID+" 的现有门禁") ||
		!strings.Contains(string(output), "计划完成，未修改任何节点") {
		t.Fatalf("failed-update maintenance takeover plan was not read-only and actionable: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("failed-update takeover plan installed an RPM: %v", statErr)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_FAILED_UPDATE_LOCK="+previousPatchID, "FAKE_OPERATION_MODE=stale-auto", "FAKE_TIMER_REQUIRES_ENABLE=true")
	output, err = command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "已接管失败升级维护锁 previous_patch_id="+previousPatchID) {
		t.Fatalf("failed-update maintenance takeover execution failed: err=%v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s takeover final version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s takeover maintenance marker was not released: %v", host, statErr)
		}
	}

	resetFakeCluster(t)
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "failed update maintenance\n", 0o600)
	}
	writeFile(t, filepath.Join(state, "c1.version"), "2.2-29\n", 0o600)
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_FAILED_UPDATE_LOCK="+previousPatchID, "FAKE_OPERATION_MODE=stale-auto")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "所有节点均已回到源版本 2.2-28") {
		t.Fatalf("mixed-version failed-update maintenance was incorrectly adopted: err=%v\n%s", err, output)
	}

	resetFakeCluster(t)
	output, err = runOperationGate(t, "stale-auto")
	if err != nil {
		t.Fatalf("stale pre-mutation automatic recovery did not bootstrap under maintenance: %v\n%s", err, output)
	}
	for _, expected := range []string{
		"维护期陈旧自动恢复豁免", "stale_threshold_seconds=1800",
		`"operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`,
		`"cluster_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"`, `"kind":"failover"`,
		`"stage":"plan"`, `"status":"running"`, `"updated_at":"2020-01-01T00:00:00Z"`,
	} {
		if !strings.Contains(string(output), expected) {
			t.Fatalf("stale-operation bootstrap output missing %q:\n%s", expected, output)
		}
	}
	for _, forbidden := range []string{"must-not-leak", "manual-secret-operator", "raw_payload", "credentials", "token"} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("operation diagnostics leaked forbidden field/value %q:\n%s", forbidden, output)
		}
	}
	assertLeaderOnlyOperationQueries(t)
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s stale-bootstrap final version=%q err=%v", host, version, readErr)
		}
	}

	blockedOperations := []struct {
		name      string
		mode      string
		violation string
	}{
		{name: "manual", mode: "manual", violation: "requested_by_not_automatic_recovery"},
		{name: "fresh", mode: "fresh", violation: "operation_not_stale"},
		{name: "execute", mode: "execute", violation: "stage_not_pre_mutation"},
		{name: "verify", mode: "verify", violation: "stage_not_pre_mutation"},
		{name: "audit", mode: "audit", violation: "stage_not_pre_mutation"},
		{name: "report", mode: "report", violation: "stage_not_pre_mutation"},
		{name: "malformed", mode: "malformed", violation: "operation_id_missing"},
		{name: "unreadable", mode: "unreadable", violation: "authenticated_operation_inventory_unreadable"},
	}
	for _, test := range blockedOperations {
		t.Run("operation gate rejects "+test.name, func(t *testing.T) {
			resetFakeCluster(t)
			output, err := runOperationGate(t, test.mode)
			if err == nil || !strings.Contains(string(output), test.violation) {
				t.Fatalf("%s operation bypassed gate or lacked diagnostics: err=%v\n%s", test.name, err, output)
			}
			for _, host := range []string{"c1", "c2", "c3"} {
				if !strings.Contains(string(output), "host="+host+" facts=") || !strings.Contains(string(output), "active_operations_not_exempt") {
					t.Fatalf("%s failure lacked per-host activity diagnostics for %s:\n%s", test.name, host, output)
				}
			}
			for _, forbidden := range []string{"must-not-leak", "manual-secret-operator", "raw_payload", "credentials", "token"} {
				if strings.Contains(string(output), forbidden) {
					t.Fatalf("%s diagnostics leaked forbidden field/value %q:\n%s", test.name, forbidden, output)
				}
			}
			assertLeaderOnlyOperationQueries(t)
			if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
				t.Fatalf("%s operation allowed an RPM mutation: %v", test.name, statErr)
			}
		})
	}

	for _, test := range []struct {
		mode      string
		violation string
	}{{mode: "indeterminate", violation: "indeterminate_operations_not_zero"}, {mode: "lifecycle", violation: "active_lifecycle_tasks_not_zero"}} {
		t.Run("activity gate rejects "+test.mode, func(t *testing.T) {
			resetFakeCluster(t)
			output, err := runOperationGate(t, test.mode)
			if err == nil || !strings.Contains(string(output), test.violation) {
				t.Fatalf("%s work bypassed gate or lacked diagnostics: err=%v\n%s", test.mode, err, output)
			}
			for _, host := range []string{"c1", "c2", "c3"} {
				if !strings.Contains(string(output), "host="+host+" facts=") {
					t.Fatalf("%s failure lacked per-host diagnostics for %s:\n%s", test.mode, host, output)
				}
			}
			if _, statErr := os.Stat(filepath.Join(state, "operation-query-hosts")); !os.IsNotExist(statErr) {
				t.Fatalf("%s-only failure unexpectedly queried operation inventory: %v", test.mode, statErr)
			}
		})
	}

	resetFakeCluster(t)
	output, err = runOperationGate(t, "stale-auto-persistent")
	if err == nil || !strings.Contains(string(output), "active_operations_not_zero") || !strings.Contains(string(output), "已完成自动回退") {
		t.Fatalf("persistent stale operation bypassed strict post-Leader convergence: err=%v\n%s", err, output)
	}
	assertLeaderOnlyOperationQueries(t)
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-28" {
			t.Fatalf("%s persistent-operation rollback version=%q err=%v", host, version, readErr)
		}
	}

	if err := os.Remove(filepath.Join(state, "install-order")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_TRANSIENT_STATUS_HOST=c2")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("transient control-plane convergence should retry: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(state, "transient-status-injected")); err != nil {
		t.Fatalf("transient status failure was not exercised: %v", err)
	}
	if err := os.Remove(filepath.Join(state, "transient-status-injected")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s transient-retry version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s transient-retry maintenance marker was not released: %v", host, statErr)
		}
	}

	if err := os.Remove(filepath.Join(state, "install-order")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_PERSISTENT_STATUS_HOST=c2", "CG_UPDATE_CLUSTER_IDLE_ATTEMPTS=2", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "已完成自动回退") {
		t.Fatalf("persistent convergence failure bypassed rollback: err=%v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-28" {
			t.Fatalf("%s convergence rollback version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s convergence rollback maintenance marker was not released: %v", host, statErr)
		}
	}

	if err := os.Remove(filepath.Join(state, "install-order")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_FAIL_HOST=c2")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "已完成自动回退") {
		t.Fatalf("failed rolling patch did not report rollback: err=%v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-28" {
			t.Fatalf("%s rollback version=%q err=%v", host, version, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s rollback maintenance marker was not released: %v", host, statErr)
		}
	}

	if err := os.Remove(filepath.Join(state, "failure-injected")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "interrupted\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--resume", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("resume interrupted rolling patch: %v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s resumed version=%q err=%v", host, version, readErr)
		}
	}

	if err := os.Remove(filepath.Join(state, "install-order")); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--execute", "--yes")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_RELEASE_FAIL_HOST=c2")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "维护门禁") {
		t.Fatalf("partial maintenance release did not fail closed: err=%v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); statErr != nil {
			t.Fatalf("%s was not compensation-locked after release failure: %v", host, statErr)
		}
		if removeErr := os.Remove(filepath.Join(state, host+".maintenance")); removeErr != nil {
			t.Fatal(removeErr)
		}
	}
	if err := os.Remove(filepath.Join(state, "release-failure-injected")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(state, "replicated-gate")); err != nil {
		t.Fatalf("partial release did not retain replicated maintenance: %v", err)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state,
		"FAKE_TRANSIENT_ACTIVE_HOST=c2", "CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS=0")
	output, err = command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "等待控制面收敛") {
		t.Fatalf("read-only plan did not wait for a transient operation: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "transient-active-injected")); statErr != nil {
		t.Fatalf("transient plan activity was not exercised: %v", statErr)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_EXTRA_DATA_NODE=true")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "静态数据节点地址与实时活动节点宿主机映射不一致") {
		t.Fatalf("stale data-node inventory was not rejected: err=%v\n%s", err, output)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_CONTAINER_DATA=true")
	output, err = command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "检测到容器数据节点独立逻辑身份") {
		t.Fatalf("container logical identities were not mapped by host: err=%v\n%s", err, output)
	}

	command = exec.Command("bash", upgradeScript,
		"--patch", patchPath, "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "-u", "root", "--plan")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"), "FAKE_REMOTE_STATE="+state, "FAKE_EXTRA_VOTERS=true")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "静态控制节点清单与实时 Raft 成员不一致") {
		t.Fatalf("stale controller inventory was not rejected: err=%v\n%s", err, output)
	}

	patchID := "cgupgrade-2.2-28-to-2.2-29-x86_64"
	updateRoot := filepath.Join(root, "updates")
	jobDirectory := filepath.Join(updateRoot, patchID)
	packageBytes, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	packageSHA := fmt.Sprintf("%x", sha256.Sum256(packageBytes))
	packageMetadata := fmt.Sprintf(`{
  "patch_id": %q,
  "file_name": "clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade",
  "size_bytes": %d,
  "sha256": %q,
  "source_version": "2.2-28",
  "target_version": "2.2-29",
  "architecture": "x86_64",
  "signature_verified": true,
  "rollback_available": true,
  "rolling": true,
  "database_mutation": false,
  "bootstrap_available": true,
  "bootstrap_protocol": 1
}
`, patchID, len(packageBytes), packageSHA)
	writeFile(t, filepath.Join(jobDirectory, "package.cgpatch"), string(packageBytes), 0o600)
	writeFile(t, filepath.Join(jobDirectory, "package.json"), packageMetadata, 0o600)
	metadataSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(packageMetadata)))
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
		_ = os.Remove(filepath.Join(state, host+".maintenance"))
	}
	_ = os.Remove(filepath.Join(state, "install-order"))
	managedEnvironment := []string{
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		"FAKE_REMOTE_STATE=" + state,
		"FAKE_EXPECTED_PACKAGE_SHA=" + packageSHA,
		"FAKE_EXPECTED_METADATA_SHA=" + metadataSHA,
	}
	command = exec.Command("bash", upgradeScript,
		"--patch", filepath.Join(jobDirectory, "package.cgpatch"), "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "--update-root", updateRoot, "--managed-job-dir", jobDirectory, "-u", "root", "--execute", "--yes")
	command.Dir = jobDirectory
	command.Env = append(os.Environ(), managedEnvironment...)
	if output, err = command.CombinedOutput(); err != nil {
		t.Fatalf("managed rolling update with package replication: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "升级包已在全部控制节点完成 SHA-256 校验和原子发布") {
		t.Fatalf("managed update did not enter artifact replication:\n%s", output)
	}
	events, err := os.ReadFile(filepath.Join(jobDirectory, "clusterguard-update-"+patchID+".events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	eventLines := strings.Split(strings.TrimSpace(string(events)), "\n")
	if len(eventLines) < 2 {
		t.Fatalf("managed update emitted %d progress events, want multiple events", len(eventLines))
	}
	for index, line := range eventLines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("managed update event line %d is not compact JSON: %q", index+1, line)
		}
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		replicatedPackage, readErr := os.ReadFile(filepath.Join(state, host+".package.cgpatch"))
		if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(replicatedPackage)) != packageSHA {
			t.Fatalf("%s did not receive the exact signed package: err=%v", host, readErr)
		}
		replicatedMetadata, readErr := os.ReadFile(filepath.Join(state, host+".package.json"))
		if readErr != nil || fmt.Sprintf("%x", sha256.Sum256(replicatedMetadata)) != metadataSHA {
			t.Fatalf("%s did not receive the exact package metadata: err=%v", host, readErr)
		}
	}

	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		writeFile(t, filepath.Join(state, host+".version"), "2.2-28\n", 0o600)
		_ = os.Remove(filepath.Join(state, host+".maintenance"))
	}
	_ = os.Remove(filepath.Join(state, "install-order"))
	command = exec.Command("bash", upgradeScript,
		"--patch", filepath.Join(jobDirectory, "package.cgpatch"), "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "--update-root", updateRoot, "--managed-job-dir", jobDirectory, "-u", "root", "--execute", "--yes")
	command.Dir = jobDirectory
	command.Env = append(os.Environ(), append(managedEnvironment, "FAKE_PACKAGE_COPY_FAIL_HOST=c2")...)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "尚未建立维护门禁，也未修改任何 RPM") {
		t.Fatalf("package distribution failure did not fail before mutation: err=%v\n%s", err, output)
	}
	if _, statErr := os.Stat(filepath.Join(state, "install-order")); !os.IsNotExist(statErr) {
		t.Fatalf("an RPM was installed after package distribution failure: %v", statErr)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		if _, statErr := os.Stat(filepath.Join(state, host+".maintenance")); !os.IsNotExist(statErr) {
			t.Fatalf("%s entered maintenance after package distribution failure: %v", host, statErr)
		}
	}

	// Model an interrupted run resuming after leadership moves from c3 to c2.
	// The new Leader restores both local artifacts from its replicated copy.
	newLeaderPackage, err := os.ReadFile(filepath.Join(state, "c2.package.cgpatch"))
	if err != nil {
		t.Fatal(err)
	}
	newLeaderMetadata, err := os.ReadFile(filepath.Join(state, "c2.package.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(jobDirectory, "package.cgpatch"), string(newLeaderPackage), 0o600)
	writeFile(t, filepath.Join(jobDirectory, "package.json"), string(newLeaderMetadata), 0o600)
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version := "2.2-28\n"
		if host == "c1" {
			version = "2.2-29\n"
		}
		writeFile(t, filepath.Join(state, host+".version"), version, 0o600)
	}
	for _, host := range []string{"c1", "c2", "c3"} {
		writeFile(t, filepath.Join(state, host+".maintenance"), "interrupted\n", 0o600)
	}
	_ = os.Remove(filepath.Join(state, "install-order"))
	command = exec.Command("bash", upgradeScript,
		"--patch", filepath.Join(jobDirectory, "package.cgpatch"), "--trust-key", publicKey,
		"--controllers", "c1,c2,c3", "--data-nodes", "c1,c2,c3,d1",
		"--known-hosts", knownHosts, "--update-root", updateRoot, "--managed-job-dir", jobDirectory, "-u", "root", "--resume", "--execute", "--yes")
	command.Dir = jobDirectory
	command.Env = append(os.Environ(), append(managedEnvironment, "FAKE_LEADER_HOST=c2")...)
	if output, err = command.CombinedOutput(); err != nil {
		t.Fatalf("resume from replicated package after Leader change: %v\n%s", err, output)
	}
	for _, host := range []string{"c1", "c2", "c3", "d1"} {
		version, readErr := os.ReadFile(filepath.Join(state, host+".version"))
		if readErr != nil || strings.TrimSpace(string(version)) != "2.2-29" {
			t.Fatalf("%s Leader-change resume version=%q err=%v", host, version, readErr)
		}
	}
}

func fakeDatabaseArchive(t *testing.T, engine string, omit string) string {
	t.Helper()
	root := t.TempDir()
	archiveRoot := filepath.Join(root, engine)
	var binaries []string
	switch engine {
	case "mysql":
		binaries = []string{"mysqld", "mysql", "mysqldump"}
	case "postgresql":
		binaries = []string{"initdb", "postgres", "psql", "pg_basebackup", "pg_rewind", "pg_controldata"}
	default:
		t.Fatalf("unsupported fixture engine %q", engine)
	}
	for _, binary := range binaries {
		if binary == omit {
			continue
		}
		writeExecutable(t, filepath.Join(archiveRoot, "bin", binary), "#!/usr/bin/env bash\nexit 0\n")
	}
	archive := filepath.Join(root, engine+".tar.gz")
	command := exec.Command("tar", "-C", root, "-czf", archive, engine)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s fixture: %v\n%s", engine, err, output)
	}
	return archive
}

func fakePostgreSQLSourceArchive(t *testing.T, omit string) string {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "postgresql-16.4")
	files := map[string]string{
		"configure":                  "#!/usr/bin/env bash\nexit 0\n",
		"src/backend/Makefile":       "all:\n\t@true\n",
		"src/bin/initdb/Makefile":    "all:\n\t@true\n",
		"src/include/pg_config.h.in": "#define PG_VERSION \"16.4\"\n",
		"contrib/Makefile":           "all:\n\t@true\n",
	}
	for name, contents := range files {
		if name == omit {
			continue
		}
		mode := os.FileMode(0o644)
		if name == "configure" {
			mode = 0o755
		}
		writeFile(t, filepath.Join(sourceRoot, name), contents, mode)
	}
	archive := filepath.Join(root, "postgresql-16.4.tar.bz2")
	command := exec.Command("tar", "-C", root, "-cjf", archive, filepath.Base(sourceRoot))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build PostgreSQL source fixture: %v\n%s", err, output)
	}
	return archive
}

func fakeBuildablePostgreSQLSourceArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "postgresql-16.4")
	writeExecutable(t, filepath.Join(sourceRoot, "configure"), "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	makefile := "all:\n\t@true\n" +
		"install:\n" +
		"\t@mkdir -p \"$(DESTDIR)/opt/clusterguard/postgresql/5432/software/bin\"\n" +
		"\t@for binary in initdb postgres psql pg_basebackup pg_rewind pg_controldata pg_config; do " +
		"printf '%s\\n' '#!/usr/bin/env bash' 'if [[ \"$$(basename \"$$0\")\" == pg_config ]]; then echo \"PostgreSQL 16.4\"; fi' 'exit 0' " +
		">\"$(DESTDIR)/opt/clusterguard/postgresql/5432/software/bin/$$binary\"; " +
		"chmod 0755 \"$(DESTDIR)/opt/clusterguard/postgresql/5432/software/bin/$$binary\"; done\n"
	writeFile(t, filepath.Join(sourceRoot, "Makefile"), makefile, 0o644)
	writeFile(t, filepath.Join(sourceRoot, "src", "backend", "Makefile"), "all:\n\t@true\n", 0o644)
	writeFile(t, filepath.Join(sourceRoot, "src", "bin", "initdb", "Makefile"), "all:\n\t@true\n", 0o644)
	writeFile(t, filepath.Join(sourceRoot, "src", "include", "pg_config.h.in"), "#define PG_VERSION \"16.4\"\n", 0o644)
	writeFile(t, filepath.Join(sourceRoot, "contrib", "Makefile"), "all:\n\t@true\ninstall:\n\t@true\n", 0o644)
	archive := filepath.Join(root, "postgresql-16.4.tar.bz2")
	command := exec.Command("tar", "-C", root, "-cjf", archive, filepath.Base(sourceRoot))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build executable PostgreSQL source fixture: %v\n%s", err, output)
	}
	return archive
}

func fakeMultiRootMySQLArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, binary := range []string{"mysqld", "mysql", "mysqldump"} {
		writeExecutable(t, filepath.Join(root, "mysql", "bin", binary), "#!/usr/bin/env bash\nexit 0\n")
	}
	writeFile(t, filepath.Join(root, "unexpected", "README"), "unexpected archive root\n", 0o644)
	archive := filepath.Join(root, "mysql-multi-root.tar.gz")
	command := exec.Command("tar", "-C", root, "-czf", archive, "mysql", "unexpected")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build multi-root fixture: %v\n%s", err, output)
	}
	return archive
}

func runMultiNodeInstallerPlan(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	command := exec.Command("bash", append([]string{"install_clusterguard.sh"}, arguments...)...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestMultiNodeInstallerSupportsControlSplitMixedAndPostgreSQLPlans(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-1.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	mysqlArchive := fakeDatabaseArchive(t, "mysql", "")
	postgresqlArchive := fakeDatabaseArchive(t, "postgresql", "")
	fencer := filepath.Join(root, "site-fencer")
	writeExecutable(t, fencer, "#!/usr/bin/env bash\nexit 0\n")

	tests := []struct {
		name      string
		arguments []string
		required  []string
	}{
		{
			name:      "control only",
			arguments: []string{"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-g", applicationRPM, "--control-only", "--plan"},
			required:  []string{"数据库引擎     : none", "10.0.0.1", "controller", "只读计划完成"},
		},
		{
			name:      "split mysql",
			arguments: []string{"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.1.1,10.0.1.2", "-g", applicationRPM, "-r", mysqlArchive, "--engine", "mysql", "--cluster-name", "mysql-prod", "--vip", "10.0.1.100", "--fencer", fencer, "--plan"},
			required:  []string{"数据库引擎     : mysql", "10.0.0.1", "controller", "10.0.1.1", "data"},
		},
		{
			name:      "mixed mysql",
			arguments: []string{"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "--data-on-arbitrators", "-g", applicationRPM, "-r", mysqlArchive, "--engine", "mysql", "--cluster-name", "mysql-mixed", "--plan"},
			required:  []string{"mysql-mixed", "10.0.0.1", "mixed"},
		},
		{
			name:      "split postgresql",
			arguments: []string{"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.2.1,10.0.2.2,10.0.2.3", "-g", applicationRPM, "-r", postgresqlArchive, "--engine", "postgresql", "--database-version", "16.4", "--cluster-name", "pg-prod", "--plan"},
			required:  []string{"数据库引擎     : postgresql", "10.0.2.1", "data"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runMultiNodeInstallerPlan(t, test.arguments...)
			if err != nil {
				t.Fatalf("plan failed: %v\n%s", err, output)
			}
			for _, required := range test.required {
				if !strings.Contains(output, required) {
					t.Fatalf("plan missing %q:\n%s", required, output)
				}
			}
		})
	}
}

func TestMultiNodeInstallerAcceptsPostgreSQLSourceAndBuildsOnce(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-46.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	sourceArchive := fakePostgreSQLSourceArchive(t, "")
	arguments := []string{
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"-n", "10.0.2.1,10.0.2.2,10.0.2.3",
		"-g", applicationRPM,
		"-r", sourceArchive,
		"--engine", "postgresql",
		"--database-version", "16.4",
		"--cluster-name", "pg-source-prod",
		"--plan",
	}
	output, err := runMultiNodeInstallerPlan(t, arguments...)
	if err != nil {
		t.Fatalf("PostgreSQL source plan failed: %v\n%s", err, output)
	}
	for _, required := range []string{
		"数据库介质类型 : PostgreSQL 官方源码",
		"源码构建节点   : 10.0.2.1（仅编译一次）",
		"统一制品分发   : 启用（SHA256 校验）",
		"PG 编译依赖    : 默认联网安装到隔离构建根",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("source plan missing %q:\n%s", required, output)
		}
	}

	output, err = runMultiNodeInstallerPlan(t, append(arguments[:len(arguments)-1], "--postgresql-build-node", "10.0.2.3", "--plan")...)
	if err != nil || !strings.Contains(output, "源码构建节点   : 10.0.2.3（仅编译一次）") {
		t.Fatalf("explicit source build node was not honored: err=%v\n%s", err, output)
	}

	output, err = runMultiNodeInstallerPlan(t, append(arguments[:len(arguments)-1], "--postgresql-build-node", "10.0.0.1", "--plan")...)
	if err == nil || !strings.Contains(output, "源码构建节点必须属于数据节点清单") {
		t.Fatalf("non-data source build node was not blocked: err=%v\n%s", err, output)
	}
}

func TestMultiNodeInstallerRejectsIncompletePostgreSQLSource(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-46.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	sourceArchive := fakePostgreSQLSourceArchive(t, "src/bin/initdb/Makefile")
	output, err := runMultiNodeInstallerPlan(t,
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"-n", "10.0.2.1",
		"-g", applicationRPM,
		"-r", sourceArchive,
		"--engine", "postgresql",
		"--database-version", "16.4",
		"--plan",
	)
	if err == nil || !strings.Contains(output, "既不是完整二进制包，也不是完整官方源码包") {
		t.Fatalf("incomplete PostgreSQL source was not blocked: err=%v\n%s", err, output)
	}
}

func TestPostgreSQLSourceBuilderProducesOneChecksummedRelocatableArtifact(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-build.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"--source-package", "--output-dir", "--install-prefix", "DESTDIR=", "make -j", "make -C contrib",
		"pg_config --version", "BUILD-MANIFEST", "sha256sum", "archive_has_single_root", "reuse_cached_artifact",
		"--online-dependencies", "--dependency-repository", "creating online isolated PostgreSQL build root",
		"--postgresql-dependencies /path/to/dependencies", "--installroot=",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("PostgreSQL source builder is missing %q", required)
		}
	}
	for _, forbidden := range []string{"yum install", "apt-get install", "curl ", "wget "} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("PostgreSQL source builder contains online or mutable dependency shortcut %q", forbidden)
		}
	}
}

func TestPostgreSQLSourceBuilderBuildsAndReusesVerifiedArtifact(t *testing.T) {
	sourceArchive := fakeBuildablePostgreSQLSourceArchive(t)
	root := t.TempDir()
	outputDir := filepath.Join(root, "output")
	buildRoot := filepath.Join(root, "build", "workspace")
	fakeBin := filepath.Join(root, "bin")
	writeExecutable(t, filepath.Join(fakeBin, "uname"), "#!/usr/bin/env bash\n[[ \"${1:-}\" == -m ]] && { echo aarch64; exit 0; }\nexec /usr/bin/uname \"$@\"\n")
	run := func() (string, string) {
		t.Helper()
		command := exec.Command(
			"bash", "clusterguard-postgresql-build.sh",
			"--source-package", sourceArchive,
			"--output-dir", outputDir,
			"--install-prefix", "/opt/clusterguard/postgresql/5432/software",
			"--version", "16.4",
			"--jobs", "1",
			"--build-root", buildRoot,
		)
		var stderr strings.Builder
		command.Stderr = &stderr
		command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))
		stdout, err := command.Output()
		if err != nil {
			t.Fatalf("build PostgreSQL source artifact: %v\n%s", err, stderr.String())
		}
		return strings.TrimSpace(string(stdout)), stderr.String()
	}

	artifact, _ := run()
	if info, err := os.Stat(artifact); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("compiled artifact mode: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(artifact + ".sha256"); err != nil {
		t.Fatalf("compiled artifact checksum: %v", err)
	}
	listing, err := exec.Command("tar", "-tf", artifact).CombinedOutput()
	if err != nil {
		t.Fatalf("list compiled artifact: %v\n%s", err, listing)
	}
	for _, required := range []string{"/bin/postgres", "/bin/pg_rewind", "/BUILD-MANIFEST.json"} {
		if !strings.Contains(string(listing), required) {
			t.Fatalf("compiled artifact is missing %q:\n%s", required, listing)
		}
	}

	reusedArtifact, stderr := run()
	if reusedArtifact != artifact {
		t.Fatalf("cache returned %q, want %q", reusedArtifact, artifact)
	}
	if !strings.Contains(stderr, "reusing verified artifact") {
		t.Fatalf("second source build did not reuse the verified artifact:\n%s", stderr)
	}
}

func TestMultiNodeInstallerDefaultsVIPMySQLToAutomaticFailoverWithAgentQuorumFencing(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-22.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	mysqlArchive := fakeDatabaseArchive(t, "mysql", "")
	fencer := filepath.Join(root, "site-fencer")
	writeExecutable(t, fencer, "#!/usr/bin/env bash\nexit 0\n")

	base := []string{
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"-n", "10.0.1.1,10.0.1.2,10.0.1.3",
		"-g", applicationRPM, "-r", mysqlArchive,
		"--engine", "mysql", "--cluster-name", "mysql-prod",
		"--vip", "10.0.1.100", "--plan",
	}

	output, err := runMultiNodeInstallerPlan(t, base...)
	if err != nil {
		t.Fatalf("automatic failover plan failed: %v\n%s", err, output)
	}
	for _, required := range []string{
		"自动故障切换   : 启用（数据库探测 + Raft 多数派 + Agent 本地隔离）",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("automatic failover plan missing %q:\n%s", required, output)
		}
	}

	output, err = runMultiNodeInstallerPlan(t, append(base, "--fencer", fencer)...)
	if err != nil || !strings.Contains(output, "外部隔离增强   : "+fencer) {
		t.Fatalf("optional external fencing plan failed: err=%v\n%s", err, output)
	}

	output, err = runMultiNodeInstallerPlan(t, append(base, "--manual-failover-only")...)
	if err != nil || !strings.Contains(output, "自动故障切换   : 关闭（仅人工受控切换）") {
		t.Fatalf("explicit manual-only plan failed: err=%v\n%s", err, output)
	}
}

func TestMultiNodeInstallerDefaultsVIPPostgreSQLToAutomaticFailoverWithAgentQuorumFencing(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.2-2.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	postgresqlArchive := fakeDatabaseArchive(t, "postgresql", "")
	base := []string{
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"-n", "10.0.2.1,10.0.2.2,10.0.2.3",
		"-g", applicationRPM, "-r", postgresqlArchive,
		"--engine", "postgresql", "--database-version", "16.4", "--cluster-name", "pg-prod",
		"--vip", "10.0.2.100", "--plan",
	}

	output, err := runMultiNodeInstallerPlan(t, base...)
	if err != nil {
		t.Fatalf("PostgreSQL automatic failover plan failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "自动故障切换   : 启用（数据库探测 + Raft 多数派 + Agent 本地隔离）") {
		t.Fatalf("PostgreSQL automatic failover plan is not enabled:\n%s", output)
	}

	output, err = runMultiNodeInstallerPlan(t, append(base, "--manual-failover-only")...)
	if err != nil || !strings.Contains(output, "自动故障切换   : 关闭（仅人工受控切换）") {
		t.Fatalf("PostgreSQL manual-only plan failed: err=%v\n%s", err, output)
	}
}

func TestMultiNodeInstallerRemoteRootIsExplicitOptIn(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-24.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	mysqlArchive := fakeDatabaseArchive(t, "mysql", "")
	base := []string{
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"-n", "10.0.1.1",
		"-g", applicationRPM, "-r", mysqlArchive,
		"--engine", "mysql", "--cluster-name", "mysql-root-policy", "--plan",
	}

	output, err := runMultiNodeInstallerPlan(t, base...)
	if err != nil || !strings.Contains(output, "MySQL 远程 root : 关闭（默认）") {
		t.Fatalf("default remote-root plan is not fail closed: err=%v\n%s", err, output)
	}
	output, err = runMultiNodeInstallerPlan(t, append(base, "--mysql-root-remote-host", "%")...)
	if err != nil || !strings.Contains(output, "MySQL 远程 root : root@%（显式启用）") {
		t.Fatalf("explicit remote-root plan was not honored: err=%v\n%s", err, output)
	}
	output, err = runMultiNodeInstallerPlan(t, append(base, "--mysql-root-remote-host", "bad\nhost")...)
	if err == nil || !strings.Contains(output, "MySQL 远程 root Host 格式无效") {
		t.Fatalf("malformed remote-root host was not blocked: err=%v\n%s", err, output)
	}
}

func TestMultiNodeInstallerRejectsUnsafeTopologyAndIncompleteMedia(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-1.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	output, err := runMultiNodeInstallerPlan(t, "-l", "10.0.0.1,10.0.0.2", "-g", applicationRPM, "--control-only", "--plan")
	if err == nil || !strings.Contains(output, "至少 3 个的奇数") {
		t.Fatalf("even controller topology was not blocked: err=%v\n%s", err, output)
	}
	incomplete := fakeDatabaseArchive(t, "mysql", "mysqldump")
	output, err = runMultiNodeInstallerPlan(t, "-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.1.1", "-g", applicationRPM, "-r", incomplete, "--engine", "mysql", "--plan")
	if err == nil || !strings.Contains(output, "缺少 bin/mysqldump") {
		t.Fatalf("incomplete database media was not blocked: err=%v\n%s", err, output)
	}
	multiRoot := fakeMultiRootMySQLArchive(t)
	output, err = runMultiNodeInstallerPlan(t, "-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.1.1", "-g", applicationRPM, "-r", multiRoot, "--engine", "mysql", "--plan")
	if err == nil || !strings.Contains(output, "一个顶层目录") {
		t.Fatalf("multi-root database media was not blocked: err=%v\n%s", err, output)
	}
	postgresqlArchive := fakeDatabaseArchive(t, "postgresql", "")
	output, err = runMultiNodeInstallerPlan(t, "-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.1.1,10.0.2.1", "-g", applicationRPM, "-r", postgresqlArchive, "--engine", "postgresql", "--plan")
	if err == nil || !strings.Contains(output, "跨网段") {
		t.Fatalf("implicit cross-subnet PostgreSQL access was not blocked: err=%v\n%s", err, output)
	}
}

func TestMultiNodeInstallerAutoSelectsOnlyOneDatabasePackageFromUnifiedDirectory(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-2.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	media := fakeDatabaseArchive(t, "mysql", "")
	selected := filepath.Join(root, "mysql-8.0.44-linux-glibc2.17-x86_64.tar.xz")
	if err := os.Rename(media, selected); err != nil {
		t.Fatalf("stage database archive: %v", err)
	}
	arguments := []string{
		"install_clusterguard.sh", "-l", "10.0.0.1,10.0.0.2,10.0.0.3", "-n", "10.0.1.1",
		"-g", applicationRPM, "--engine", "mysql", "--database-version", "8.0.44", "--plan",
	}
	command := exec.Command("bash", arguments...)
	command.Env = append(os.Environ(), "CG_DATABASE_PACKAGE_DIR="+root)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "自动选择数据库介质："+selected) {
		t.Fatalf("automatic package selection failed: err=%v output=%s", err, output)
	}
	second := filepath.Join(root, "mysql-8.0.44-duplicate.tar.xz")
	contents, err := os.ReadFile(selected)
	if err != nil {
		t.Fatalf("read selected archive: %v", err)
	}
	writeFile(t, second, string(contents), 0o600)
	command = exec.Command("bash", arguments...)
	command.Env = append(os.Environ(), "CG_DATABASE_PACKAGE_DIR="+root)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "发现多个匹配的数据库介质") {
		t.Fatalf("ambiguous automatic package selection was not blocked: err=%v output=%s", err, output)
	}
}

func TestMultiNodeInstallerAutoSelectsOnlyOneBundledApplicationRPM(t *testing.T) {
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	if err := os.MkdirAll(packages, 0o755); err != nil {
		t.Fatal(err)
	}
	rpm := filepath.Join(packages, "clusterguard-ha-2.1-6.x86_64.rpm")
	writeFile(t, rpm, "test-rpm", 0o644)
	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
script_dir="$CG_TEST_KIT"
resolve_application_rpm
printf '%s\n' "$application_rpm"
`)
	command.Env = append(os.Environ(), "CG_TEST_KIT="+root)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), rpm) {
		t.Fatalf("automatic RPM selection failed: err=%v output=%s", err, output)
	}

	writeFile(t, filepath.Join(packages, "clusterguard-ha-2.1-5.x86_64.rpm"), "test-rpm", 0o644)
	command = exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
script_dir="$CG_TEST_KIT"
resolve_application_rpm
`)
	command.Env = append(os.Environ(), "CG_TEST_KIT="+root)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "发现多个 ClusterGuard RPM") {
		t.Fatalf("ambiguous RPM selection was not blocked: err=%v output=%s", err, output)
	}
}

func TestMultiNodeInstallerFailsClosedOnSSHAndPackageSafety(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"StrictHostKeyChecking=yes", "--known-hosts", "--accept-host-keys", "verify_sidecar",
		"assert_node_identity_unique", "remote_preflight", "scp -q -P", "--data-on-arbitrators",
		"archive_has_single_root", "RPM architecture mismatch", "password:[ ]*$", "log_user 0", "tr -d '\\r'",
		"permission denied, please try again", "proc finish_child", "prompt_for_ssh_password",
		"SSH 用户 ${ssh_user} 的密码（仅输入一次）", "CG_SSH_PASSWORD",
		"resolve_database_package", "--database-package-dir", "发现多个匹配的数据库介质，拒绝自动选择",
		"CG_INSTALL_VIP_LEASE_TIMEOUT_SECONDS:-300", "next_discovery=$((SECONDS + 10))",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("multi-node installer is missing %q", required)
		}
	}
	for _, forbidden := range []string{"StrictHostKeyChecking=no", "rpm --nodeps", "--fail-with-body", "exp_continue"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("multi-node installer contains unsafe compatibility shortcut %q", forbidden)
		}
	}
}

func TestMultiNodeInstallerPostgreSQLSourceRemotePreflightExecutes(t *testing.T) {
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-test.x86_64.rpm")
	databasePackage := filepath.Join(root, "postgresql-16.4.tar.bz2")
	writeFile(t, applicationRPM, "rpm", 0o644)
	writeFile(t, databasePackage, "postgresql source", 0o644)
	for _, name := range []string{"rpm", "ss", "systemctl"} {
		writeExecutable(t, filepath.Join(root, name), "#!/usr/bin/env bash\nexit 0\n")
	}
	writeExecutable(t, filepath.Join(root, "uname"), "#!/usr/bin/env bash\nprintf '%s\\n' x86_64\n")

	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
all_nodes=(local-test-node)
controller_nodes=(local-test-node)
data_nodes=(local-test-node)
database_engine=postgresql
database_package_kind=postgresql-source
postgresql_build_node=local-test-node
application_rpm="$CG_TEST_APPLICATION_RPM"
database_package="$CG_TEST_DATABASE_PACKAGE"
controller_data_root="$CG_TEST_ROOT/controller"
database_data_root="$CG_TEST_ROOT/database"
controller_min_free_gb=0
database_min_free_gb=0
postgresql_build_min_free_gb=0
api_port=49151
raft_port=49152
node_role_for() { printf '%s\n' mixed; }
remote_exec() { bash -c "$2"; }
remote_preflight
`)
	command.Env = append(os.Environ(),
		"PATH="+root+":"+os.Getenv("PATH"),
		"CG_TEST_APPLICATION_RPM="+applicationRPM,
		"CG_TEST_DATABASE_PACKAGE="+databasePackage,
		"CG_TEST_ROOT="+root,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PostgreSQL source preflight must execute its remote program: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "执行远端前置检查") {
		t.Fatalf("PostgreSQL source preflight trace missing: %s", output)
	}
}

func TestPostgreSQLInstallerNormalizesExtractedSoftwarePermissionsBeforeInitDB(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	extract := strings.Index(text, `tar --no-same-owner --no-same-permissions -xf`)
	normalize := strings.Index(text, `chmod -R a+rX "${software_root}"`)
	initialize := strings.Index(text, `runuser -u postgres -- "${software_root}/bin/initdb"`)
	if extract < 0 || normalize < 0 || initialize < 0 {
		t.Fatalf("PostgreSQL installer is missing extraction, permission normalization, or initdb")
	}
	if !(extract < normalize && normalize < initialize) {
		t.Fatal("PostgreSQL software permissions must be normalized after extraction and before initdb")
	}
}

func TestPostgreSQLInstallerBoundsWALReceiverFailureDetection(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"wal_receiver_timeout = '5s'",
		"wal_retrieve_retry_interval = '1s'",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("PostgreSQL automatic failover timing invariant is missing %q", required)
		}
	}
}

func TestMultiNodeInstallerRendersOneSecondPostgreSQLRecoveryCadence(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"postgresql:{enabled:$postgresql_enabled,discovery_interval_seconds:1,discovery_timeout_seconds:1",
		"automatic_failover_interval_seconds:1",
	} {
		if !strings.Contains(string(contents), required) {
			t.Fatalf("PostgreSQL generated control-plane schedule is missing %q", required)
		}
	}
}

func TestPostgreSQLInstallerMakesInitPasswordReadableByServiceAccount(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	create := strings.Index(text, `init_secret_file="$(mktemp /tmp/clusterguard-postgresql-init.XXXXXX)"`)
	write := strings.Index(text, `printf '%s\n' "${admin_secret}" >"${init_secret_file}"`)
	handoff := strings.Index(text, `chown postgres:postgres "${init_secret_file}"`)
	initialize := strings.Index(text, `runuser -u postgres -- "${software_root}/bin/initdb"`)
	if create < 0 || write < 0 || handoff < 0 || initialize < 0 {
		t.Fatalf("PostgreSQL installer is missing secure init password creation, ownership handoff, or initdb")
	}
	if !(create < write && write < handoff && handoff < initialize) {
		t.Fatal("PostgreSQL init password must be written and handed to the service account before initdb")
	}
}

func TestPostgreSQLInstallerPersistsReplicationCredentialOnEveryNode(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`.secrets.postgresql_replication_password`,
		`replication_secret="$(jq -r '.secrets.postgresql_replication_password' "${payload}")"`,
		`replication_user="clusterguard_repl"`,
		`printf '*:*:*:%s:%s\n' "${replication_user}" "$(pgpass_escape "${replication_secret}")" >>"${pass_file}"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("PostgreSQL installer must persist the replication credential before a primary can become a standby; missing %q", required)
		}
	}
}

func TestMultiNodePostgreSQLInstallPayloadCarriesReplicationCredential(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	start := strings.Index(text, "postgresql_payload()")
	end := strings.Index(text[start:], "\n}\n\nremote_json_helper()")
	if start < 0 || end < 0 {
		t.Fatal("PostgreSQL install payload function is missing")
	}
	payload := text[start : start+end]
	for _, required := range []string{
		`read_env_value CG_POSTGRESQL_REPLICATION_PASSWORD`,
		`postgresql_replication_password:$replication`,
	} {
		if !strings.Contains(payload, required) {
			t.Fatalf("PostgreSQL install payload must carry the managed replication credential; missing %q", required)
		}
	}
}

func TestPostgreSQLInstallerAllowsServiceAccountToReplaceManagedDataAtomically(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	parentOwnership := strings.Index(text, `chown postgres:postgres "$(dirname "${data_directory}")"`)
	parentMode := strings.Index(text, `chmod 0750 "$(dirname "${data_directory}")"`)
	initialize := strings.Index(text, `runuser -u postgres -- "${software_root}/bin/initdb"`)
	if parentOwnership < 0 || parentMode < 0 || initialize < 0 {
		t.Fatal("PostgreSQL installer must let the service account create atomic base-backup staging directories beside PGDATA")
	}
	if !(parentOwnership < initialize && parentMode < initialize) {
		t.Fatal("PostgreSQL managed data parent ownership must be established before initdb")
	}
}

func TestPostgreSQLInstallerAllowsServiceAccountToTraverseProductLogParent(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	parentMode := strings.Index(text, `chmod 0751 /var/log/clusterguard "$(dirname "${log_directory}")"`)
	serviceStart := strings.Index(text, `systemctl enable --now "${service}"`)
	if parentMode < 0 || serviceStart < 0 {
		t.Fatal("PostgreSQL installer must grant execute-only traversal through the product log parents")
	}
	if parentMode >= serviceStart {
		t.Fatal("PostgreSQL log parent traversal must be established before service startup")
	}
}

func TestMultiNodeInstallerAllowsIdempotentRepairOfManagedPostgreSQLInitialization(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	start := strings.Index(text, "postgresql_payload()")
	end := strings.Index(text[start:], "\n}\n\nremote_json_helper()")
	if start < 0 || end < 0 {
		t.Fatal("PostgreSQL install payload function is missing")
	}
	payload := text[start : start+end]
	if !strings.Contains(payload, "rebuild:true") {
		t.Fatal("managed PostgreSQL initialization must be safely resumable after an interrupted install")
	}
}

func TestPostgreSQLInstallerRecreatesRuntimeDirectoryAfterStoppingManagedService(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	stop := strings.Index(text, `systemctl stop "${service}" || true`)
	recreate := strings.LastIndex(text, `install -d -m 0750 -o postgres -g postgres "${run_directory}"`)
	serviceStart := strings.Index(text, `systemctl enable --now "${service}"`)
	if stop < 0 || recreate < 0 || serviceStart < 0 {
		t.Fatal("PostgreSQL installer must stop the managed service, recreate its runtime directory, and restart it")
	}
	if !(stop < recreate && recreate < serviceStart) {
		t.Fatal("PostgreSQL runtime directory must be recreated after service stop and before service startup")
	}
}

func TestPostgreSQLServiceCanTraverseControllerRuntimeDirectory(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-postgresql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	groupMembership := strings.Index(text, `usermod -a -G clusterguard postgres`)
	supplementaryGroup := strings.Index(text, `SupplementaryGroups=clusterguard`)
	serviceStart := strings.Index(text, `systemctl enable --now "${service}"`)
	if groupMembership < 0 || supplementaryGroup < 0 || serviceStart < 0 {
		t.Fatal("PostgreSQL must retain traversal access when the controller recreates /run/clusterguard")
	}
	if groupMembership >= serviceStart || supplementaryGroup >= serviceStart {
		t.Fatal("PostgreSQL runtime traversal must be configured before service startup")
	}
}

func TestPostgreSQLCredentialPathIsTraversableByServiceAccount(t *testing.T) {
	for _, path := range []string{"clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		parentMode := strings.Index(text, `chmod 0751 /etc/clusterguard`)
		passVariable := "pass_file"
		if strings.Contains(path, "-sync.sh") {
			passVariable = "target_pass"
		}
		readabilityCheck := strings.Index(text, `runuser -u postgres -- test -r "${`+passVariable+`}"`)
		serviceStart := strings.Index(text, `systemctl start "${service}"`)
		if serviceStart < 0 {
			serviceStart = strings.Index(text, `systemctl enable --now "${service}"`)
		}
		if parentMode < 0 || readabilityCheck < 0 || serviceStart < 0 {
			t.Fatalf("%s must expose execute-only credential-parent traversal and verify service-account readability before startup", path)
		}
		if !(parentMode < readabilityCheck && readabilityCheck < serviceStart) {
			t.Fatalf("%s must establish and verify PostgreSQL credential access before service startup", path)
		}
	}
}

func TestClusterGuardConfigRootRemainsTraversableByDatabaseServiceAccounts(t *testing.T) {
	paths := []string{
		"clusterguard-install.sh",
		"clusterguard-configure.sh",
		"clusterguard-control-join.sh",
		filepath.Join("..", "packaging", "rpm", "postinstall.sh"),
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		if !strings.Contains(text, "0751") || !strings.Contains(text, "/etc/clusterguard") {
			t.Fatalf("%s must preserve execute-only traversal of /etc/clusterguard for database service accounts", path)
		}
		if strings.Contains(text, "-m 0750 \"$(target_path /etc/clusterguard)\"") ||
			strings.Contains(text, "-m 0750 -o root -g clusterguard /etc/clusterguard") ||
			strings.Contains(text, "chmod 0750 /etc/clusterguard") {
			t.Fatalf("%s can make protected PostgreSQL passfiles unreachable after installation", path)
		}
	}
}

func TestClusterGuardLogRootRemainsTraversableByDatabaseServiceAccounts(t *testing.T) {
	paths := map[string]string{
		"clusterguard-install.sh":                                 `install -d -m 0751 "$(target_path /var/log/clusterguard)"`,
		"clusterguard-configure.sh":                               `install -d -m 0751 "$(target_path /var/log/clusterguard)"`,
		"clusterguard-control-join.sh":                            `chmod 0751 /var/log/clusterguard`,
		filepath.Join("..", "packaging", "rpm", "postinstall.sh"): `install -d -m 0751 -o clusterguard -g clusterguard /var/log/clusterguard`,
	}
	for path, required := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		if !strings.Contains(text, required) {
			t.Fatalf("%s must preserve execute-only traversal of /var/log/clusterguard for database service accounts", path)
		}
		if strings.Contains(text, `-m 0750 "$(target_path /var/log/clusterguard)"`) ||
			strings.Contains(text, "-m 0750 -o clusterguard -g clusterguard /var/log/clusterguard") ||
			strings.Contains(text, "chmod 0750 /var/log/clusterguard") {
			t.Fatalf("%s can make PostgreSQL log files unreachable after a managed restart", path)
		}
	}
}

func TestControllerSystemdKeepsSharedLogRootTraversable(t *testing.T) {
	path := filepath.Join("..", "packaging", "systemd", "clusterguard-ha.service")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "LogsDirectory=clusterguard") ||
		!strings.Contains(text, "LogsDirectoryMode=0751") {
		t.Fatal("controller systemd unit must keep the shared log root traversable by database service accounts")
	}
	if strings.Contains(text, "LogsDirectoryMode=0750") {
		t.Fatal("systemd must not reset /var/log/clusterguard to 0750 on every controller start")
	}
}

func TestMultiNodeInstallerWiresPostgreSQLLifecycleHelpersAndCredentials(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`postgresql_install_helper:"/usr/local/libexec/clusterguard-postgresql-install.sh"`,
		`postgresql_sync_helper:"/usr/local/libexec/clusterguard-postgresql-sync.sh"`,
		`postgresql_admin_password_env:"CG_POSTGRESQL_ADMIN_PASSWORD"`,
		`postgresql_replication_password_env:"CG_POSTGRESQL_REPLICATION_PASSWORD"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("generated controller configuration is missing PostgreSQL lifecycle field %q", required)
		}
	}
}

func TestExpectSSHDoesNotTreatRemoteMySQLPasswordErrorAsAuthenticationPrompt(t *testing.T) {
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("expect is required")
	}
	root := t.TempDir()
	fakeSSH := filepath.Join(root, "ssh")
	writeExecutable(t, fakeSSH, `#!/usr/bin/env bash
printf '\rroot@test password: ' >&2
IFS= read -r supplied
if [[ "${supplied}" != "ssh-secret" ]]; then
  printf 'unexpected authentication input\n' >&2
  exit 90
fi
printf 'ERROR 1045 (28000): Access denied for user root (using password: YES)\n' >&2
exit 17
`)
	knownHosts := filepath.Join(root, "known_hosts")
	writeFile(t, knownHosts, "test ssh-ed25519 AAAATEST\n", 0o600)
	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
ssh_user=root
ssh_port=22
ssh_password=ssh-secret
known_hosts_file="$CG_TEST_KNOWN_HOSTS"
expect_ssh test-host 'mysql --execute SELECT_1'
`)
	command.Env = append(os.Environ(),
		"PATH="+root+":"+os.Getenv("PATH"),
		"CG_TEST_KNOWN_HOSTS="+knownHosts,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("remote MySQL failure unexpectedly succeeded: %s", output)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 17 {
		t.Fatalf("exit code was not preserved: err=%v output=%s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "using password: YES") {
		t.Fatalf("remote MySQL diagnostic was swallowed: %s", output)
	}
	if strings.Contains(text, "ssh-secret") {
		t.Fatalf("SSH password leaked into remote output: %s", output)
	}
}

func TestMySQLQualificationWriterCreatesAtomicIntegrityReport(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	statePath := filepath.Join(root, "mysql-state")
	mysqlPath := filepath.Join(root, "mysql")
	timeoutPath := filepath.Join(root, "timeout")
	writeExecutable(t, timeoutPath, "#!/usr/bin/env bash\nshift\nexec \"$@\"\n")
	writeExecutable(t, mysqlPath, `#!/usr/bin/env bash
set -euo pipefail
sql=""
while (($#)); do
  case "$1" in
    --execute)
      sql="${2:-}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
count=0
if [[ -f "${CG_FAKE_MYSQL_STATE}" ]]; then
  read -r count <"${CG_FAKE_MYSQL_STATE}"
fi
case "${sql}" in
  *"INSERT INTO cg_prodqual.cg_switch_writes"*)
    count=$((count + 1))
    printf '%s\n' "${count}" >"${CG_FAKE_MYSQL_STATE}"
    ;;
  *"CREATE DATABASE IF NOT EXISTS cg_prodqual"*)
    ;;
  *)
    printf '%s:1:%s:123\n' "${count}" "${count}"
    ;;
esac
`)
	environmentPath := filepath.Join(root, "clusterguard.env")
	writeFile(t, environmentPath, "CG_NODE_MYSQL_ROOT_PASSWORD=test-password\n", 0o600)
	logRoot := filepath.Join(root, "logs")
	command := exec.Command(
		"bash", "clusterguard-mysql-qualification.sh",
		"mysql80", "127.0.0.1", "3306", "2", "qualification-run", logRoot,
	)
	command.Env = append(os.Environ(),
		"CG_ENV_FILE="+environmentPath,
		"CG_MYSQL_CLIENT="+mysqlPath,
		"CG_TIMEOUT_BIN="+timeoutPath,
		"CG_FAKE_MYSQL_STATE="+statePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("qualification writer failed: %v\n%s", err, output)
	}
	summaryPath := filepath.Join(logRoot, "mysql80-qualification-run.json")
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		IntegrityStatus string `json:"integrity_status"`
		Acknowledged    int    `json:"acknowledged"`
		Observed        struct {
			Count int `json:"count"`
			Min   int `json:"min_sequence"`
			Max   int `json:"max_sequence"`
		} `json:"observed"`
	}
	if err := json.Unmarshal(summaryBytes, &summary); err != nil {
		t.Fatalf("invalid qualification report: %v\n%s", err, summaryBytes)
	}
	if summary.IntegrityStatus != "pass" {
		t.Fatalf("unexpected integrity status: %+v", summary)
	}
	if summary.Acknowledged == 0 || summary.Observed.Count != summary.Acknowledged {
		t.Fatalf("observed rows do not match acknowledgements: %+v", summary)
	}
	if summary.Observed.Min != 1 || summary.Observed.Max != summary.Acknowledged {
		t.Fatalf("sequence is not contiguous: %+v", summary)
	}
	info, err := os.Stat(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected report mode: %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(summaryPath + ".tmp.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary reports were not cleaned up: %v", matches)
	}
}

func TestMySQLQualificationWriterIsDelivered(t *testing.T) {
	for _, path := range []string{
		"clusterguard-install.sh",
		"build-clusterguard-bundle.sh",
		"build-clusterguard-rpm.sh",
		"../packaging/rpm/nfpm.yaml",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "clusterguard-mysql-qualification.sh") {
			t.Fatalf("%s does not deliver the MySQL qualification writer", path)
		}
	}
}

func TestPostgreSQLLifecycleHelpersAreInstalledBundledAndPreflighted(t *testing.T) {
	for _, path := range []string{"clusterguard-install.sh", "build-clusterguard-bundle.sh", "clusterguard-preflight.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, helper := range []string{"clusterguard-postgresql-build.sh", "clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh"} {
			if !strings.Contains(string(contents), helper) {
				t.Fatalf("%s does not deliver required PostgreSQL helper %s", path, helper)
			}
		}
	}
}

func TestAdapterRuntimeHelperIsInstalledBundledAndPackaged(t *testing.T) {
	for _, path := range []string{"clusterguard-install.sh", "build-clusterguard-bundle.sh", "../packaging/rpm/nfpm.yaml"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "clusterguard-adapter-runtime-install.sh") {
			t.Fatalf("%s does not deliver the adapter runtime helper", path)
		}
	}
}

func TestControlJoinHelperIsInstalledBundledAndPackaged(t *testing.T) {
	for _, path := range []string{"clusterguard-install.sh", "build-clusterguard-bundle.sh", "build-clusterguard-rpm.sh", "../packaging/rpm/nfpm.yaml"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "clusterguard-control-join.sh") {
			t.Fatalf("%s does not deliver the control-node join helper", path)
		}
	}
}

func TestPowerLifecycleHelpersAreInstalledBundledAndPreflighted(t *testing.T) {
	for _, path := range []string{"clusterguard-install.sh", "build-clusterguard-bundle.sh", "clusterguard-preflight.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, helper := range []string{
			"clusterguard-cluster-shutdown.sh",
			"clusterguard-cluster-restore.sh",
			"clusterguard-cluster-finalize.sh",
		} {
			if !strings.Contains(string(contents), helper) {
				t.Fatalf("%s does not deliver required power lifecycle helper %s", path, helper)
			}
		}
	}
	for _, unit := range []string{
		"clusterguard-cluster-restore.service",
		"clusterguard-cluster-finalize.service",
	} {
		contents, err := os.ReadFile("clusterguard-preflight.sh")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), unit) {
			t.Fatalf("preflight does not require power lifecycle unit %s", unit)
		}
	}
}

func TestInstallerKeepsControllerAndAgentEnvironmentFilesSeparated(t *testing.T) {
	for _, path := range []string{"clusterguard-install.sh", "clusterguard-preflight.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "--agent-env-file") {
			t.Fatalf("%s does not support a least-privilege agent environment file", path)
		}
	}
	contents, err := os.ReadFile("clusterguard-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `install_file 0600 "${agent_environment_file}" /etc/clusterguard/agent.env`) {
		t.Fatal("installer does not install the dedicated agent environment file")
	}
}

func TestHAMatrixUsesFreshOneTimeApprovalPerSwitch(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-ha-matrix.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"/api/v1/approvals",
		"approval_token",
		"approval_issue_failed",
		"requested_by:\"cg-ha-matrix\"",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("HA matrix missing one-time approval contract %q", expected)
		}
	}
	for _, forbidden := range []string{"CG_APPROVAL_TOKEN", "--approval-env", "approval_token_environment"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("HA matrix still depends on reusable approval material %q", forbidden)
		}
	}
}

func TestHAMatrixSupportsAuthenticatedPlatformSessionExecution(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-ha-matrix.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"--platform-session",
		"CG_PLATFORM_USERNAME",
		"CG_PLATFORM_PASSWORD",
		"CG_PLATFORM_NEW_PASSWORD",
		"/api/v1/auth/login",
		"/api/v1/auth/password",
		"clusterguard_csrf",
		"X-CSRF-Token",
		"authorization_mode",
		"session",
		"requested_by:$requested_by",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("HA matrix missing platform session contract %q", expected)
		}
	}
}

func TestAuthenticationDeliveryDocumentationCoversBootstrapAndRecovery(t *testing.T) {
	// The literal bootstrap password used to be the proxy for "the delivery
	// documentation explains how to obtain the first administrator credential".
	// The runtime no longer falls back to that literal: unless the deployment
	// sets bootstrap_admin_password_env, the Leader generates the password into a
	// root-only file. The contract therefore moves with the behaviour and now
	// requires the documentation to name the mechanism. What this test protects
	// is unchanged: an operator must be able to bootstrap and recover the
	// administrator account from the shipped documentation alone.
	for _, path := range []string{"../README.md", "../docs/operations.md", "../docs/architecture.md", "../docs/mysql-feature-parity-acceptance.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(contents)
		for _, expected := range []string{"admin", "bootstrap_admin_password_env", "bootstrap-admin-password", "MustChangePassword", "eight-hour", "logout", "first password change"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("%s missing authentication documentation %q", path, expected)
			}
		}
	}
	environment, err := os.ReadFile("../packaging/systemd/clusterguard.env.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(environment), "Platform administrator passwords are not configured in this file") {
		t.Fatal("systemd environment sample does not separate platform passwords from service secrets")
	}
}

func TestHAMatrixPlatformSessionNeedsNoControlTokenOrApprovalField(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	var executions int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/auth/login":
			http.SetCookie(writer, &http.Cookie{Name: "clusterguard_session", Value: "session-secret", Path: "/", HttpOnly: true})
			http.SetCookie(writer, &http.Cookie{Name: "clusterguard_csrf", Value: "csrf-secret", Path: "/"})
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"user":{"username":"admin","must_change_password":false}}}`)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates"):
			if request.Header.Get("Authorization") != "" {
				http.Error(writer, "platform reads must not use the control bearer", http.StatusBadRequest)
				return
			}
			if cookie, err := request.Cookie("clusterguard_session"); err != nil || cookie.Value != "session-secret" {
				http.Error(writer, "missing platform session", http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute":
			if request.Header.Get("Authorization") != "" {
				http.Error(writer, "platform execution must not use the control bearer", http.StatusBadRequest)
				return
			}
			if request.Header.Get("X-CSRF-Token") != "csrf-secret" {
				http.Error(writer, "missing CSRF token", http.StatusForbidden)
				return
			}
			payload := map[string]any{}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			if _, found := payload["approval_token"]; found {
				http.Error(writer, "platform execution exposed an approval token", http.StatusBadRequest)
				return
			}
			atomic.AddInt32(&executions, 1)
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-1","status":"succeeded"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
		"--platform-session",
	)
	command.Env = append(os.Environ(),
		"CG_PLATFORM_USERNAME=admin",
		"CG_PLATFORM_PASSWORD=changed-password",
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("platform-session HA matrix: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&executions) != 1 {
		t.Fatalf("platform-session executions=%d, want 1\n%s", executions, output)
	}
	if !strings.Contains(string(output), "matrix_summary total=1 passed=1 failed=0") {
		t.Fatalf("platform-session matrix summary missing:\n%s", output)
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

func TestVIPReconcileServiceCanDropPrivilegesForPostgreSQL(t *testing.T) {
	contents, err := os.ReadFile("../packaging/systemd/clusterguard-agent-reconcile.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"CAP_SETUID",
		"CAP_SETGID",
		"ProtectSystem=strict",
		"ReadWritePaths=/var/lib/clusterguard-agent -/etc/clusterguard/docker",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("VIP reconcile service is missing required runtime access %q", required)
		}
	}
}

func TestDockerSwarmAgentExampleUsesUnifiedControlPlane(t *testing.T) {
	contents, err := os.ReadFile("../configs/clusterguard-agent.docker-swarm.example.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, ":3100") || strings.Contains(text, "clusterguard-agent-swarm") {
		t.Fatalf("Docker Swarm example still provisions an isolated control plane or Agent state: %s", text)
	}
	if strings.Count(text, ":3000") != 3 || !strings.Contains(text, `"role_state_directory": "/var/lib/clusterguard-agent/roles"`) {
		t.Fatalf("Docker Swarm example does not target the shared three-node control plane: %s", text)
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
	for _, helper := range []string{
		"clusterguard-node-lifecycle.sh", "clusterguard-package-resolve.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh",
		"clusterguard-postgresql-build.sh", "clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh",
	} {
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
		"find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*known_hosts' -exec chown root:clusterguard {} +",
		"find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*known_hosts' -exec chmod 0644 {} +",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("installer is missing service-readable configuration ownership: %q", expected)
		}
	}
}

func TestControllerSetupPreservesDatabaseEngineLogOwnership(t *testing.T) {
	paths := []string{
		"clusterguard-install.sh",
		"clusterguard-configure.sh",
		"clusterguard-control-join.sh",
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(contents)
		if strings.Contains(text, "chown -R clusterguard:clusterguard /var/lib/clusterguard /var/log/clusterguard") {
			t.Fatalf("%s recursively takes ownership of database engine log directories", path)
		}
		for _, required := range []string{
			"chown -R clusterguard:clusterguard /var/lib/clusterguard",
			"chown clusterguard:clusterguard /var/log/clusterguard",
			"find /var/log/clusterguard -maxdepth 1 -type f -exec chown clusterguard:clusterguard {} +",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s is missing ownership boundary %q", path, required)
			}
		}
	}
}

func TestRPMConfiguratorRepairsControllerKnownHostsPermissions(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{"-name '*known_hosts'", "-exec chown root:clusterguard {} +", "-exec chmod 0644 {} +"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("RPM configurator is missing known_hosts permission repair: %q", expected)
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
	writeFile(t, filepath.Join(assets, "pki", "api-issuer.crt"), "api-issuer", 0o600)
	writeFile(t, filepath.Join(assets, "pki", "api-issuer.key"), "api-issuer-key", 0o600)
	writeFile(t, filepath.Join(assets, "pki", "raft-issuer.crt"), "raft-issuer", 0o600)
	writeFile(t, filepath.Join(assets, "pki", "raft-issuer.key"), "raft-issuer-key", 0o600)
	writeFile(t, filepath.Join(assets, "ssh", "controller_ed25519"), "private-key", 0o600)
	writeFile(t, filepath.Join(assets, "ssh", "known_hosts"), "host-key", 0o600)
	writeFile(t, filepath.Join(assets, "mysql", "3306-client.cnf"), "[client]\npassword=secret\n", 0o600)
	writeExecutable(t, filepath.Join(assets, "fencing", "clusterguard-fencer"), "#!/usr/bin/env bash\nexit 0\n")
	writeFile(t, filepath.Join(assets, "fencing", "site.json"), `{"provider":"test"}`, 0o600)
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
		"pki/api-issuer.crt": 0o644, "pki/api-issuer.key": 0o640,
		"pki/raft-issuer.crt": 0o644, "pki/raft-issuer.key": 0o640,
		"ssh/controller_ed25519": 0o600, "ssh/known_hosts": 0o644,
		"mysql/3306-client.cnf": 0o600,
		"fencing/site.json":     0o640,
	} {
		info, err := os.Stat(filepath.Join(installRoot, "etc", "clusterguard", path))
		if err != nil {
			t.Fatalf("installed asset %s: %v", path, err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("asset %s mode=%#o, want %#o", path, info.Mode().Perm(), mode)
		}
	}
	fencerInfo, err := os.Stat(filepath.Join(installRoot, "usr", "local", "libexec", "clusterguard-fencer"))
	if err != nil {
		t.Fatalf("installed external fencer: %v", err)
	}
	if fencerInfo.Mode().Perm() != 0o750 {
		t.Fatalf("external fencer mode=%#o, want %#o", fencerInfo.Mode().Perm(), os.FileMode(0o750))
	}
	for _, path := range []string{"._tls", "tls/._server.crt", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(installRoot, "etc", "clusterguard", path)); !os.IsNotExist(err) {
			t.Fatalf("packaging metadata %s was installed: %v", path, err)
		}
	}
}

func TestOfflineConfiguratorAcceptsProtectedControllerEnrollmentAssets(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-configure.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"pki/*.crt|pki/*.key",
		"tls/*.crt|pki/*.crt|ssh/*known_hosts",
		"tls/*.key|pki/*.key",
		"/etc/clusterguard/pki",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("offline configurator omits controller enrollment asset contract %q", required)
		}
	}
}

func TestSmokeChecksOneWriterOneVIPReplicaThreadsAndSemiSync(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"writer_count", "vip_owner_count", "vip_owner_is_primary", "replica_threads_healthy",
		"semi_sync_primary_ready", "semi_sync_replicas_ready", "semi_sync_required",
		"topology_fresh", "/api/v1/clusters/${cluster_id}/topology", "/api/v1/clusters/${cluster_id}/ha-endpoints",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("smoke script missing invariant %q", expected)
		}
	}
}

func TestInstallerDefersVIPReconcileUntilInitialOwnershipLeaseIsStable(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"wait_for_initial_vip_ownership_lease()",
		"/api/v1/clusters/${cluster_id}/ha-ownership",
		"activate_vip_reconciliation \"${cluster_id}\" \"${primary_id}\"",
		"systemctl enable --now clusterguard-agent-reconcile.timer",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("installer is missing initial VIP ownership sequencing %q", expected)
		}
	}
	configureStart := strings.Index(text, "configure_data_agents()")
	waitStart := strings.Index(text, "wait_for_initial_vip_ownership_lease()")
	if configureStart < 0 || waitStart < 0 || waitStart <= configureStart {
		t.Fatal("installer VIP ownership sequencing functions are not ordered")
	}
	configureSection := text[configureStart:waitStart]
	if strings.Contains(configureSection, "clusterguard-agent-reconcile.timer") {
		t.Fatal("installer enables VIP reconciliation before the initial ownership lease is stable")
	}
}

func TestInstallerVerifiesNodeReadinessBeforeWaitingForVIPLease(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"verify_managed_mysql_node()",
		"verify_managed_mysql_node \"${host}\" \"安装后\"",
		"verify_managed_mysql_node \"${host}\" \"副本同步后\"",
		"verify_data_agent_readiness()",
		"验证数据节点 Agent 就绪",
		"data_agents_remain_ready()",
		"等待初始所有权租约期间发现数据节点服务或 Agent 不完整",
		"diagnose_vip_lease_readiness()",
		"VIP 租约启动诊断",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("installer is missing post-install VIP readiness guard %q", required)
		}
	}
	configureStart := strings.Index(text, "configure_data_agents\n  verify_data_agent_readiness\n  wait_control_plane")
	if configureStart < 0 {
		t.Fatal("installer must verify every data Agent before proceeding to control-plane and VIP lease checks")
	}
}

func TestInstallerRegistersImmutableNodeInventoryBeforeClusterDiscovery(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"register_node_inventory()",
		"api_request GET /api/v1/nodes",
		`api_request PUT "/api/v1/nodes/${resource_id}"`,
		"api_request POST /api/v1/nodes",
		"resource_id:$resource_id",
		"kind:$kind",
		"active:true",
		"configure_controllers_first_pass\n  wait_control_plane\n  register_node_inventory\n  register_and_discover_cluster",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("installer is missing immutable node inventory registration %q", required)
		}
	}
}

func TestInstallerEnforcesSafeCrossNodeClockSkewAfterInternalClockBootstrap(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"verify_cluster_clock_skew()",
		"date -u +%s",
		"skew > 30",
		"超过 30 秒安全上限",
		"configure_fixed_cluster_clock_source()",
		"clusterguard-clock-mesh.sh",
		"--server-address",
		"discover_node_identities\n  remote_preflight\n  configure_fixed_cluster_clock_source\n  verify_cluster_clock_skew",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("installer is missing clock-skew execution gate: %q", required)
		}
	}
}

func TestClockMeshUsesOnlyFixedInternalSource(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-clock-mesh.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"local stratum 10",
		"server ${server_address} iburst prefer",
		"--server-address",
		"wait_for_fixed_source()",
		"Chrony did not select fixed internal source",
		"chronyc makestep",
		"timedatectl set-local-rtc 0",
		"hwclock --systohc --utc",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("clock mesh is missing %q", expected)
		}
	}
	if strings.Contains(text, "pool ") {
		t.Fatal("clock mesh must not depend on an external NTP pool")
	}
}

func serveMatrixApproval(writer http.ResponseWriter, request *http.Request, issued *int32) bool {
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/approvals" {
		return false
	}
	if request.Header.Get("Authorization") != "Bearer matrix-control" {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return true
	}
	payload := struct {
		ClusterID      string `json:"cluster_id"`
		TargetID       string `json:"target_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}{}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return true
	}
	ordinal := int32(1)
	if issued != nil {
		ordinal = atomic.AddInt32(issued, 1)
	}
	_, _ = fmt.Fprintf(writer, `{"status":"ok","result":{"operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","idempotency_key":%q},"grant":{"resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","cluster_id":%q,"target_id":%q,"status":"active"},"approval_token":"cgag_bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.token-%d"}}`,
		payload.IdempotencyKey, payload.ClusterID, payload.TargetID, ordinal)
	return true
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
		issuedApprovals       int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if serveMatrixApproval(writer, request, &issuedApprovals) {
			return
		}
		if request.URL.Path != "/api/v1/operations/execute" && request.Header.Get("Authorization") != "Bearer matrix-control" {
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
			if request.Header.Get("Authorization") != "Bearer matrix-control" {
				http.Error(writer, "service execution must retain administrator authorization", http.StatusUnauthorized)
				return
			}
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
		"CG_MATRIX_SMOKE_LOG="+smokeLog,
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
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
	if atomic.LoadInt32(&issuedApprovals) != 4 {
		t.Fatalf("issued approvals=%d, want one per execution", issuedApprovals)
	}
	byCluster := map[string][]string{}
	for _, execution := range executions {
		if !strings.HasPrefix(execution.Approval, "cgag_") {
			t.Fatalf("one-time approval token was not forwarded: %+v", execution)
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
		if serveMatrixApproval(writer, request, nil) {
			return
		}
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
		"CG_MATRIX_READ_ATTEMPTS=2",
		"CG_MATRIX_READ_RETRY_INTERVAL=0",
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
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
		if serveMatrixApproval(writer, request, nil) {
			return
		}
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
		"CG_MATRIX_SMOKE_ATTEMPT_FILE="+attemptFile,
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
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
	var attempts, issuedApprovals int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if serveMatrixApproval(writer, request, &issuedApprovals) {
			return
		}
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
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("HA matrix stale-plan retry: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&attempts) != 2 || !strings.Contains(string(output), "switch_retry ordinal=0") || !strings.Contains(string(output), "reason=stale_plan") {
		t.Fatalf("stale plan was not retried once with a fresh operation: attempts=%d\n%s", attempts, output)
	}
	if atomic.LoadInt32(&issuedApprovals) != 2 {
		t.Fatalf("stale plan retry reused an approval: issued=%d", issuedApprovals)
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
		if serveMatrixApproval(writer, request, nil) {
			return
		}
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
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
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
		if serveMatrixApproval(writer, request, nil) {
			return
		}
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
		"CG_MATRIX_RECONCILE_ATTEMPTS=2",
		"CG_MATRIX_RECONCILE_INTERVAL=0",
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
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
	command.Env = append(os.Environ(), "CG_CONTROL_TOKEN=matrix-control", "CG_MATRIX_STABLE_OBSERVATIONS=1")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cluster UUIDs must be unique") {
		t.Fatalf("duplicate inventory was not rejected: err=%v output=%s", err, output)
	}
}

func TestHAMatrixReportsSafeOperationEvidenceForHTTPFailure(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	operationID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if serveMatrixApproval(writer, request, nil) {
			return
		}
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":[{"instance_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","eligible":true,"rank":1}]}`)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(writer, `{"status":"indeterminate","message":"operation requires operator review","debug":"never-print-matrix-approval","result":{"resource_id":"%s","stage":"VERIFY","failure_class":"promoted_unverified","precheck":[{"name":"replication_lag","status":"fail"}],"plan":{"checks":[{"name":"probe_coverage","status":"blocked"}]},"verification":{"checks":[{"name":"writer_endpoint_owner","status":"fail"},{"name":"target_writable","status":"fail"}]}}}`, operationID)
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
	command.Env = append(os.Environ(), "CG_CONTROL_TOKEN=matrix-control", "CG_MATRIX_STABLE_OBSERVATIONS=1")
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
		"failed_checks=probe_coverage,replication_lag,target_writable,writer_endpoint_owner",
		"switch_failed ordinal=0 cluster=" + clusterID,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("matrix diagnostics missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "never-print-matrix-approval") || strings.Contains(text, "cgag_") {
		t.Fatalf("matrix diagnostics leaked hidden response or approval data:\n%s", text)
	}
}

func TestHAMatrixWaitsForDistinctStableCandidateObservations(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	targetA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	targetB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	var candidateReads int32
	executedTarget := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if serveMatrixApproval(writer, request, nil) {
			return
		}
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			read := atomic.AddInt32(&candidateReads, 1)
			target := targetA
			if read >= 2 {
				target = targetB
			}
			_, _ = fmt.Fprintf(
				writer,
				`{"status":"ok","observation_id":"2026-07-28T10:00:%02dZ","result":[{"instance_id":%q,"eligible":true,"rank":1}]}`,
				read,
				target,
			)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			payload := struct {
				TargetID string `json:"target_id"`
			}{}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			executedTarget = payload.TargetID
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-stable","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command(
		"bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_MATRIX_STABLE_OBSERVATIONS=3",
		"CG_MATRIX_CANDIDATE_ATTEMPTS=5",
		"CG_MATRIX_CANDIDATE_INTERVAL=0",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stable candidate matrix: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&candidateReads) != 4 {
		t.Fatalf("candidate reads=%d, want 4 distinct observations\n%s", candidateReads, output)
	}
	if executedTarget != targetB {
		t.Fatalf("executed target=%s, want stable target %s\n%s", executedTarget, targetB, output)
	}
	if !strings.Contains(string(output), "candidate_stable") {
		t.Fatalf("stable candidate diagnostic missing:\n%s", output)
	}
}

func TestHAMatrixHandlesEmptyCandidateSetWithoutJQError(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprint(writer, `{"status":"ok","observation_id":"2026-07-28T10:01:00Z","result":[]}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	command := exec.Command(
		"bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
		"CG_MATRIX_CANDIDATE_ATTEMPTS=2",
		"CG_MATRIX_CANDIDATE_INTERVAL=0",
	)
	output, err := command.CombinedOutput()
	text := string(output)
	if err == nil {
		t.Fatalf("empty candidate matrix unexpectedly passed:\n%s", text)
	}
	if !strings.Contains(text, "reason=no_eligible_candidate") {
		t.Fatalf("empty candidate diagnostic missing:\n%s", text)
	}
	if strings.Contains(text, "cannot be divided") || strings.Contains(text, "jq: error") {
		t.Fatalf("empty candidate set triggered an internal jq error:\n%s", text)
	}
}

func TestHAMatrixRetriesApprovalAfterTransientBlockingPlan(t *testing.T) {
	clusterID := "11111111-1111-4111-8111-111111111111"
	targetID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	var approvals, executions int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/candidates") {
			_, _ = fmt.Fprintf(
				writer,
				`{"status":"ok","observation_id":"2026-07-28T10:01:%02dZ","result":[{"instance_id":%q,"eligible":true,"rank":1}]}`,
				atomic.LoadInt32(&approvals)+1,
				targetID,
			)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/approvals" {
			if atomic.AddInt32(&approvals, 1) == 1 {
				writer.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprint(writer, `{"status":"blocked","message":"operation precheck contains blocking checks","result":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","status":"planned","stage":"plan","precheck":[{"name":"replication_lag","status":"fail","message":"check failed"}]}}`)
				return
			}
			serveMatrixApproval(writer, request, nil)
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/execute" {
			atomic.AddInt32(&executions, 1)
			_, _ = fmt.Fprint(writer, `{"status":"ok","result":{"resource_id":"operation-retried","status":"succeeded"}}`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	fakeScripts := t.TempDir()
	writeExecutable(t, filepath.Join(fakeScripts, "clusterguard-smoke.sh"), "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command(
		"bash", "clusterguard-ha-matrix.sh",
		"--api", server.URL,
		"--clusters", clusterID,
		"--round-robin", "1",
		"--random", "0",
	)
	command.Env = append(os.Environ(),
		"CG_CONTROL_TOKEN=matrix-control",
		"CG_MATRIX_STABLE_OBSERVATIONS=1",
		"CG_MATRIX_CANDIDATE_INTERVAL=0",
		"script_dir="+fakeScripts,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("blocking approval retry matrix: %v\n%s", err, output)
	}
	if atomic.LoadInt32(&approvals) != 2 || atomic.LoadInt32(&executions) != 1 {
		t.Fatalf("approvals=%d executions=%d, want 2 and 1\n%s", approvals, executions, output)
	}
	if !strings.Contains(string(output), "reason=blocking_precheck") {
		t.Fatalf("blocking approval retry diagnostic missing:\n%s", output)
	}
}

func TestBundleBuildContainsInstallableRuntimeAndChecksums(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"cmd/clusterguard", "cmd/cgctl", "cmd/clusterguard-agent", "SHA256SUMS",
		"install_clusterguard.sh", "clusterguard-install.sh", "clusterguard-configure.sh", "clusterguard-agent-stdio.sh",
		"clusterguard-offline-deps.sh",
		"clusterguard-postgresql-build.sh",
		"clusterguard-clock-mesh.sh",
		"clusterguard-mysql-probe-cleanup.sh", "docs/offline-install.md", "docs/zh-CN/*.md",
		"clusterguard-cluster-shutdown.sh", "clusterguard-cluster-restore.sh", "clusterguard-cluster-finalize.sh",
		"OFFLINE-INSTALL.md", "COPYFILE_DISABLE=1", "--no-xattrs",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("bundle builder missing %q", expected)
		}
	}
}

func TestTarInstallerInstallsOperationalQualificationTools(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"clusterguard-smoke.sh",
		"clusterguard-ha-matrix.sh",
		"clusterguard-mysql-qualification.sh",
	} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("tar installer is missing %q", expected)
		}
	}
}

func TestOfflineKitBuilderIncludesBothInstallFormatsAndChineseManuals(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-offline-kit.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"build-clusterguard-bundle.sh",
		"build-clusterguard-rpm.sh",
		"install_clusterguard.sh",
		"jq-linux-amd64",
		"packages/database",
		"dependencies",
		"dependency_dir",
		"--dependencies",
		"--database-package",
		"database_package_count",
		"packages/database/${database_package_name}.sha256",
		"*.rpm",
		"*-linux-\"${goarch}\".tar.gz",
		"ClusterGuard-HA-离线安装与部署手册.md",
		"版本升级与回退手册.md",
		"ClusterGuard-HA-${version}.${release}-发布说明.md",
		"PostgreSQL-高可用手册.md",
		"PostgreSQL-生产验收报告.md",
		"examples/docker-swarm/postgresql",
		"postgresql-stack.yml",
		"clusterguard-postgres-entrypoint.sh",
		"verify-replication.sh",
		"clusterguard-offline-deps.sh",
		"SHA256SUMS",
		"RELEASE-INFO",
		"--git-common-dir",
		"release/${bundle_version}",
		`"${output}/docs"`,
		`"${output}/RELEASE-INFO"`,
		"packaging/offline-dependencies/rocky-8-${media_arch}-runtime",
		"自动使用内置 Rocky 8 基础运行依赖",
		"ncurses-compat-libs",
		"Rocky GPG 公钥",
		"编译PostgreSQL源码.sh",
		"构建PostgreSQL依赖包.sh",
		"postgresql_build_dependencies=separate-online-first",
		"release_channel=",
		"source_tree_dirty=",
		"source_tracked_diff_sha256=",
		"source_untracked_count=",
		"canonical_executable",
		"--patch-trust-key",
		"trust/patch-signing-public.pem",
		"update.json.example",
		"clusterguard-update-prune.sh",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("offline kit builder is missing %q", expected)
		}
	}
}

func TestMultiNodeInstallerAutoUsesBundledDependenciesAndReportsRemoteFailures(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"discover_bundled_dependencies",
		`"${script_dir}/dependencies"`,
		"remote_exec_checked",
		"configure_cluster_firewalls",
		"firewall-ports.managed",
		"reconcile_database_installation_phase",
		"拒绝整集群覆盖，请使用节点恢复流程",
		"verify_remote_tcp_reachability",
		"redact_remote_output",
		"远端步骤失败",
		"rpm --checksig",
		"COPYFILE_DISABLE=1",
		"--exclude='._*'",
		"/etc/pki/rpm-gpg/RPM-GPG-KEY-*",
		"rpm --import",
		"controller_ca_file:\"/etc/clusterguard/tls/ca.crt\"",
		"--ssh-credentials-file FILE",
		"节点级 SSH 凭据文件权限必须为 0600",
		"credential_password_for_host",
		"SSH 凭据       : 节点级凭据文件",
		"复用 ClusterGuard 管理的 MySQL 客户端",
		"/opt/clusterguard/mysql/${database_port}/software/bin/mysql",
		"--postgresql-dependencies DIR",
		"--online-dependencies",
		"默认从构建节点已配置的软件源联网获取",
		"discover_bundled_patch_trust_key",
		"configure_platform_updates",
		"/etc/clusterguard/update.json",
		"retained_versions",
		"systemctl enable --now clusterguard-update-helper.service",
		"图形化补丁升级",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("multi-node installer dependency/error handling is missing %q", expected)
		}
	}
	if strings.Contains(text, `remote_json_helper "${host}" /usr/local/libexec/clusterguard-mysql-install.sh "" "${payload}" >/dev/null`) {
		t.Fatal("MySQL install helper failures are still silently discarded")
	}
}

func TestPostgreSQLDependencyPackIsSeparateAndVersionMatched(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-postgresql-deps-pack.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"postgresql_source_build=true",
		"signature_verification=passed",
		"PACKAGE-MANIFEST.txt",
		"repodata/repomd.xml",
		"--postgresql-dependencies",
		"postgresql-build-deps-${platform}-${architecture}",
		"release/${bundle_version}",
		"README-中文.txt",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("PostgreSQL dependency pack builder is missing %q", expected)
		}
	}
}

func TestMultiNodeInstallerSupportsPerNodeSSHCredentialsWithoutCommandLinePasswords(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-3.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	databaseArchive := fakeDatabaseArchive(t, "mysql", "")
	credentials := filepath.Join(root, "node-ssh.credentials")
	writeFile(t, credentials, "10.0.0.1=first-password\n10.0.0.2=second-password\n10.0.0.3=third-password\n", 0o600)

	output, err := runMultiNodeInstallerPlan(t,
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"--data-on-arbitrators",
		"-g", applicationRPM,
		"-r", databaseArchive,
		"--engine", "mysql",
		"--ssh-credentials-file", credentials,
		"--plan")
	if err != nil {
		t.Fatalf("per-node credentials plan failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "SSH 凭据       : 节点级凭据文件") {
		t.Fatalf("plan did not disclose node-level credential mode:\n%s", output)
	}
	if strings.Contains(output, "first-password") || strings.Contains(output, "second-password") || strings.Contains(output, "third-password") {
		t.Fatalf("plan leaked a node credential:\n%s", output)
	}

	if err := os.Chmod(credentials, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = runMultiNodeInstallerPlan(t,
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "--data-on-arbitrators",
		"-g", applicationRPM, "-r", databaseArchive, "--engine", "mysql",
		"--ssh-credentials-file", credentials, "--plan")
	if err == nil || !strings.Contains(output, "权限必须为 0600") {
		t.Fatalf("insecure credential permissions were not blocked: %v\n%s", err, output)
	}
}

func TestMultiNodeInstallerSupportsOrderedSSHPasswords(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	applicationRPM := filepath.Join(root, "clusterguard-ha-2.1-5.x86_64.rpm")
	writeFile(t, applicationRPM, "test-rpm", 0o644)
	databaseArchive := fakeDatabaseArchive(t, "mysql", "")

	output, err := runMultiNodeInstallerPlan(t,
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3",
		"--data-on-arbitrators",
		"-g", applicationRPM, "-r", databaseArchive, "--engine", "mysql",
		"-u", "root", "-p", "first,second,third", "--plan")
	if err != nil {
		t.Fatalf("ordered password plan failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "SSH 凭据       : 节点顺序密码列表") {
		t.Fatalf("plan did not disclose ordered credential mode:\n%s", output)
	}
	if strings.Contains(output, "first") || strings.Contains(output, "second") || strings.Contains(output, "third") {
		t.Fatalf("plan leaked an ordered credential:\n%s", output)
	}

	output, err = runMultiNodeInstallerPlan(t,
		"-l", "10.0.0.1,10.0.0.2,10.0.0.3", "--data-on-arbitrators",
		"-g", applicationRPM, "-r", databaseArchive, "--engine", "mysql",
		"-p", "first,second", "--plan")
	if err == nil || !strings.Contains(output, "需要 3 个密码") {
		t.Fatalf("wrong ordered password count was not blocked: %v\n%s", err, output)
	}
}

func TestRPMDeliveryUsesVersionReleaseFilename(t *testing.T) {
	rpmBuilder, err := os.ReadFile("build-clusterguard-rpm.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`version="2.2"`,
		`release="1"`,
		`clusterguard-ha-${version}-${release}.${rpm_arch}.rpm`,
	} {
		if !strings.Contains(string(rpmBuilder), expected) {
			t.Fatalf("RPM builder is missing version-release naming %q", expected)
		}
	}

	offlineBuilder, err := os.ReadFile("build-clusterguard-offline-kit.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(offlineBuilder), `bundle_version="${version}-${release}"`) {
		t.Fatal("offline kit must default to the RPM version-release identifier")
	}
}

func TestOfflineDependencyCollectorIsVersionMatchedAndChecksummed(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-offline-deps.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{
		"dnf download",
		"--resolve",
		"--alldeps",
		"--archlist=\"${target_arch},noarch\"",
		"rpm --checksig",
		"signatures OK",
		"PACKAGE-MANIFEST.txt",
		"signature_verification=passed",
		"libaio",
		"ncurses-compat-libs",
		"numactl-libs",
		"openssh-clients",
		"iproute",
		"iputils",
		"SHA256SUMS",
		"/etc/redhat-release",
		"--runtime-minimal",
		"runtime_minimal=%s",
		"--postgresql-source-build",
		"readline-devel",
		"openssl-devel",
		"libicu-devel",
		"systemd-devel",
		"openldap-devel",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("offline dependency collector is missing %q", expected)
		}
	}
}

func TestRPMConfigurationRollsBackWhenServiceActivationFails(t *testing.T) {
	input := t.TempDir()
	config := filepath.Join(input, "clusterguard.json")
	environment := filepath.Join(input, "clusterguard.env")
	writeFile(t, config, `{"mysql":{"enabled":false}}`, 0o600)
	writeFile(t, environment, "CG_CONTROL_TOKEN=new-secret\n", 0o600)

	installRoot := filepath.Join(t.TempDir(), "root")
	existingConfig := filepath.Join(installRoot, "etc", "clusterguard", "clusterguard.json")
	existingEnvironment := filepath.Join(installRoot, "etc", "clusterguard", "clusterguard.env")
	writeFile(t, existingConfig, `{"mysql":{"enabled":true},"marker":"old"}`, 0o640)
	writeFile(t, existingEnvironment, "CG_CONTROL_TOKEN=old-secret\n", 0o640)

	fakeSystemctl := filepath.Join(t.TempDir(), "systemctl")
	writeExecutable(t, fakeSystemctl, `#!/usr/bin/env bash
if [[ "$*" == "restart clusterguard-ha.service" ]]; then
  exit 1
fi
exit 0
`)
	fakeJQ := filepath.Join(t.TempDir(), "jq")
	writeExecutable(t, fakeJQ, "#!/usr/bin/env bash\nexit 0\n")
	command := exec.Command("bash", "clusterguard-configure.sh",
		"--role", "controller",
		"--node-name", "cg-node-0001",
		"--node-id", "11111111-1111-4111-8111-111111111111",
		"--config", config,
		"--env-file", environment,
		"--execute",
	)
	command.Env = append(os.Environ(),
		"CG_INSTALL_ROOT="+installRoot,
		"CG_CONFIGURE_SKIP_RUNTIME=1",
		"CG_SYSTEMCTL="+fakeSystemctl,
		"CG_JQ_BINARY="+fakeJQ,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("configuration unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "已自动恢复原配置") {
		t.Fatalf("rollback diagnostic missing:\n%s", output)
	}
	restoredConfig, readErr := os.ReadFile(existingConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(restoredConfig), `"marker":"old"`) {
		t.Fatalf("configuration was not restored: %s", restoredConfig)
	}
	restoredEnvironment, readErr := os.ReadFile(existingEnvironment)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restoredEnvironment) != "CG_CONTROL_TOKEN=old-secret\n" {
		t.Fatalf("environment was not restored: %s", restoredEnvironment)
	}
	if _, statErr := os.Stat(filepath.Join(installRoot, "etc", "clusterguard", "node.json")); !os.IsNotExist(statErr) {
		t.Fatalf("new node identity remained after rollback: %v", statErr)
	}
}

func TestRPMDeliveryIsCompleteAndDoesNotStartUnconfiguredServices(t *testing.T) {
	requiredFiles := []string{
		"../packaging/rpm/nfpm.yaml",
		"../packaging/rpm/preinstall.sh",
		"../packaging/rpm/postinstall.sh",
		"../packaging/rpm/preremove.sh",
		"../packaging/rpm/postremove.sh",
		"build-clusterguard-rpm.sh",
		"clusterguard-configure.sh",
	}
	for _, path := range requiredFiles {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required RPM delivery file %s: %v", path, err)
		}
	}

	configuration, err := os.ReadFile("../packaging/rpm/nfpm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(configuration)
	for _, expected := range []string{
		"name: clusterguard-ha",
		"version_schema: none",
		"/usr/local/bin/clusterguard",
		"/usr/local/bin/cgctl",
		"/usr/local/bin/clusterguard-agent",
		"/usr/local/libexec/clusterguard-k8s-fence-guard",
		"/usr/local/libexec/clusterguard-update-helper",
		"/usr/local/libexec/clusterguard-update-job.sh",
		"/usr/local/libexec/clusterguard-update-prune.sh",
		"/usr/local/sbin/clusterguard-upgrade",
		"/usr/local/sbin/clusterguard-configure",
		"/usr/lib/systemd/system/clusterguard-ha.service",
		"/usr/lib/systemd/system/clusterguard-cluster-restore.service",
		"/usr/lib/systemd/system/clusterguard-cluster-finalize.service",
		"/usr/lib/systemd/system/clusterguard-update-helper.service",
		"/usr/local/libexec/clusterguard-cluster-restore.sh",
		"/usr/local/libexec/clusterguard-cluster-finalize.sh",
		"/etc/clusterguard/clusterguard.json.example",
		"/etc/clusterguard/update.json.example",
		"type: config|noreplace",
		"/usr/share/doc/clusterguard-ha/离线安装手册.md",
		"/usr/share/doc/clusterguard-ha/数据库接入手册.md",
		"/usr/share/doc/clusterguard-ha/运维操作手册.md",
		"/usr/share/doc/clusterguard-ha/版本升级与回退手册.md",
		"/usr/share/doc/clusterguard-ha/Kubernetes-MySQL.md",
		"/usr/share/doc/clusterguard-ha/PostgreSQL-高可用手册.md",
		"/usr/share/doc/clusterguard-ha/PostgreSQL-生产验收报告.md",
		"/usr/share/clusterguard/docker-swarm/mysql/mysql-stack.yml",
		"/usr/share/clusterguard/docker-swarm/postgresql/postgresql-stack.yml",
		"/usr/share/clusterguard/docker-swarm/postgresql/verify-replication.sh",
		"/usr/share/clusterguard/kubernetes/clusterguard-rbac.yaml",
		"/usr/share/clusterguard/kubernetes/mysql/statefulset-fence-guard-patch.yaml",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("RPM configuration is missing %q", expected)
		}
	}

	postinstall, err := os.ReadFile("../packaging/rpm/postinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	postinstallText := string(postinstall)
	for _, forbidden := range []string{
		"systemctl enable --now clusterguard-ha",
		"systemctl start clusterguard-ha",
		"systemctl restart clusterguard-ha",
		"systemctl try-restart clusterguard-ha",
		"systemctl try-restart clusterguard-agent",
	} {
		if strings.Contains(postinstallText, forbidden) {
			t.Fatalf("RPM postinstall starts an unconfigured service: %q", forbidden)
		}
	}
	for _, expected := range []string{
		"chown root:clusterguard /usr/local/sbin/clusterguard-upgrade",
		"chmod 0750 /usr/local/sbin/clusterguard-upgrade",
		"/var/lib/clusterguard",
		"/var/log/clusterguard",
		"/etc/clusterguard/trust",
		"/var/lib/clusterguard/updates",
		"systemctl daemon-reload",
		"clusterguard-configure",
	} {
		if !strings.Contains(postinstallText, expected) {
			t.Fatalf("RPM postinstall is missing %q", expected)
		}
	}

	preinstall, err := os.ReadFile("../packaging/rpm/preinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	preinstallText := string(preinstall)
	for _, expected := range []string{"groupadd --system clusterguard", "useradd --system", "--gid clusterguard"} {
		if !strings.Contains(preinstallText, expected) {
			t.Fatalf("RPM preinstall is missing %q", expected)
		}
	}
	if !strings.Contains(text, "preinstall: ./preinstall.sh") ||
		!strings.Contains(text, "group: clusterguard") {
		t.Fatal("RPM metadata must create and assign the clusterguard execution group before payload installation")
	}
}

func TestRPMBuildEmbedsVersionContractAndPackagesUpdater(t *testing.T) {
	contents, err := os.ReadFile("build-clusterguard-rpm.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"clusterguard.io/ha/internal/buildinfo.Version",
		"clusterguard.io/ha/internal/buildinfo.Release",
		"clusterguard.io/ha/internal/buildinfo.Commit",
		"clusterguard.io/ha/internal/buildinfo.BuiltAt",
		"clusterguard-upgrade.sh",
		"clusterguard-update-helper:./cmd/clusterguard-update-helper",
		"clusterguard-k8s-fence-guard:./cmd/clusterguard-k8s-fence-guard",
		"clusterguard-update-job.sh",
		"clusterguard-update-prune.sh",
		"clusterguard-update.example.json",
		"deploy/docker-swarm/postgresql/postgresql-stack.yml",
		"deploy/docker-swarm/postgresql/verify-replication.sh",
		"deploy/kubernetes/mysql/bootstrap-writer-endpoint.sh",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("RPM build contract missing %q", required)
		}
	}
}

func TestSoftwareUpdateHelperSystemdUnitAllowsControlledPrivilegedUpdate(t *testing.T) {
	contents, err := os.ReadFile("../packaging/systemd/clusterguard-update-helper.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"User=root",
		"Group=clusterguard",
		"RuntimeDirectory=clusterguard",
		"RuntimeDirectoryMode=0750",
		"ProtectSystem=false",
		"/usr/local/libexec/clusterguard-update-helper",
		"/usr/local/libexec/clusterguard-update-job.sh",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("software update helper unit is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"ProtectSystem=strict",
		"CapabilityBoundingSet=",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("software update helper unit contains incompatible restriction %q", forbidden)
		}
	}
}

func TestSoftwareUpdateArtifactsRemainReadableByConsoleService(t *testing.T) {
	job, err := os.ReadFile("clusterguard-update-job.sh")
	if err != nil {
		t.Fatal(err)
	}
	jobText := string(job)
	for _, required := range []string{
		"workspace snapshot",
		"private_root=\"${CG_UPDATE_PRIVATE_ROOT:-/var/lib/clusterguard-update-private}\"",
		"runuser -u clusterguard",
		"publish_public_file",
		"chmod 0640",
		"output.log",
		"clusterguard-update-*.events.jsonl",
		"schedule_helper_refresh",
		"--on-active=3s",
		"restart clusterguard-update-helper.service",
	} {
		if !strings.Contains(jobText, required) {
			t.Fatalf("update job does not publish %q for the console service", required)
		}
	}
	for _, forbidden := range []string{"chown root:clusterguard", "chgrp clusterguard", "chmod 0770 \"${job_dir}\""} {
		if strings.Contains(jobText, forbidden) {
			t.Fatalf("root update job still mutates the public projection directly: %q", forbidden)
		}
	}

	upgrader, err := os.ReadFile("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	upgraderText := string(upgrader)
	for _, required := range []string{
		`services="${services} clusterguard-update-helper.service"`,
		`--on-active=5s /usr/bin/systemctl restart clusterguard-update-helper.service`,
	} {
		if !strings.Contains(upgraderText, required) {
			t.Fatalf("rolling updater is missing Helper refresh contract %q", required)
		}
	}
	for _, forbidden := range []string{
		`chmod 0600 "${journal_events_file}"`,
		`chmod 0600 "${journal_file}.tmp"`,
	} {
		if strings.Contains(upgraderText, forbidden) {
			t.Fatalf("update journal remains private to the helper: %q", forbidden)
		}
	}
}

func TestRollingUpdaterPublishesStructuredProgressAcrossControllers(t *testing.T) {
	upgrader, err := os.ReadFile("clusterguard-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(upgrader)
	for _, required := range []string{
		"publish_update_artifacts",
		"publish_update_progress",
		"validate_root_input_patch",
		"root 升级必须从可信私有目录读取输入包",
		`runuser -u clusterguard -- install -d -m 0770 '${remote_dir}'`,
		`private_root}/inbox/${patch_id}`,
		`private_root}/history/${patch_id}`,
		`runuser -u clusterguard -- bash -c`,
		`input_sha="$(sha256_file "${input_patch_file}")"`,
		`[[ "${input_sha}" == "${package_sha}" ]]`,
		`printf '%s  %s\\n' '${package_sha}' '${package_temporary}' | sha256sum -c -`,
		`无法向控制节点 ${host} 分发并验证升级包；尚未建立维护门禁，也未修改任何 RPM`,
		`--arg phase "${phase}"`,
		`--argjson current "${current}"`,
		`--argjson total "${total}"`,
		`--argjson percent "${percent}"`,
		`percent=$((current * 100 / total))`,
		`percent:$percent`,
		`private_root}/history/${patch_id}/events.jsonl`,
		`write_journal updating`,
		`write_journal verified`,
		`write_journal finalizing`,
		`prune_update_artifacts`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("rolling updater is missing structured progress contract %q", required)
		}
	}
	if publish := strings.Index(text, "publish_update_artifacts\n"); publish < 0 {
		t.Fatal("rolling updater does not publish the signed package before execution")
	} else if locks := strings.LastIndex(text, "acquire_update_locks\n"); locks < 0 || publish >= locks {
		t.Fatal("rolling updater must distribute and verify the package before acquiring maintenance locks")
	}
	if succeeded := strings.LastIndex(text, `write_journal succeeded`); succeeded < 0 {
		t.Fatal("rolling updater does not publish a durable success state")
	} else if prune := strings.LastIndex(text, `prune_update_artifacts ||`); prune <= succeeded {
		t.Fatal("rolling updater must prune old packages only after durable success")
	}

	job, err := os.ReadFile("clusterguard-update-job.sh")
	if err != nil {
		t.Fatal(err)
	}
	jobText := string(job)
	for _, required := range []string{
		`--update-root "${root}"`,
		`--private-root "${private_root}"`,
		`--managed-job-dir "${public_dir}"`,
		`--retain-versions "${retained_versions}"`,
		`.retained_versions // 3`,
		`last_event_status`,
		`write_status rolled_back`,
	} {
		if !strings.Contains(jobText, required) {
			t.Fatalf("update job is missing progress continuity contract %q", required)
		}
	}

	pruner, err := os.ReadFile("clusterguard-update-prune.sh")
	if err != nil {
		t.Fatal(err)
	}
	prunerText := string(pruner)
	for _, required := range []string{
		`retained_versions="${CG_UPDATE_RETAINED_VERSIONS:-3}"`,
		`--private-root DIR`,
		`private_root}/jobs/${patch_id}/status.json`,
		`maintenance_active`,
		`verification_required`,
		`--protect`,
	} {
		if !strings.Contains(prunerText, required) {
			t.Fatalf("update pruner is missing retention safety contract %q", required)
		}
	}
}

func TestChineseDeliveryManualsCoverInstallDatabasePreparationAndOperations(t *testing.T) {
	expectations := map[string][]string{
		"../docs/zh-CN/offline-rpm-install.md": {
			"离线安装",
			"rpm -K",
			"rpm -qpl",
			"dnf install",
			"clusterguard-configure",
			"--execute",
			"卸载与回滚",
		},
		"../docs/zh-CN/database-preparation.md": {
			"MySQL",
			"PostgreSQL",
			"Oracle Data Guard Broker",
			"SQL Server Always On",
			"数据库侧",
			"最小权限",
			"验证",
		},
		"../docs/zh-CN/operations-manual.md": {
			"运维操作手册",
			"登录",
			"集群接入",
			"计划切换",
			"故障切换",
			"旧主恢复",
			"节点扩容",
			"操作日志",
			"备份与恢复",
			"应急处理",
		},
		"../docs/zh-CN/update-and-patch.md": {
			"版本升级与回退手册",
			".cgupgrade",
			".cgpatch",
			"签名",
			"滚动升级",
			"--resume",
			"--rollback",
			"维护门禁",
			"升级包驻留合同",
			"retained_versions",
			"系统升级期间无法进行自动切换，请注意关注。",
		},
	}
	for path, required := range expectations {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, expected := range required {
			if !strings.Contains(string(contents), expected) {
				t.Fatalf("%s is missing %q", path, expected)
			}
		}
	}
}
