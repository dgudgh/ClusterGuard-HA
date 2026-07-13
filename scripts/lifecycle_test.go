package scripts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNodeLifecycleScriptsAreSyntaxValidAndExposeStructuredStages(t *testing.T) {
	paths := []string{"clusterguard-node-lifecycle.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh"}
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
		if !strings.Contains(text, "--defaults-extra-file") || !strings.Contains(text, "0600") {
			t.Fatalf("%s does not use protected MySQL option files", path)
		}
	}
}

func TestSyncScriptSupportsVersionAwareReplicationAndSelectedCopyMethods(t *testing.T) {
	contents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatalf("read sync script: %v", err)
	}
	text := string(contents)
	for _, required := range []string{"clone", "xtrabackup", "logical_dump", "CHANGE MASTER TO", "CHANGE REPLICATION SOURCE TO", "START SLAVE", "START REPLICA", "super_read_only"} {
		if !strings.Contains(text, required) {
			t.Fatalf("sync script missing %q", required)
		}
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

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	reset := strings.Index(syncText, "RESET BINARY LOGS AND GTIDS")
	dump := strings.Index(syncText, "--set-gtid-purged=ON")
	if reset < 0 || dump < 0 || reset > dump || !strings.Contains(syncText, "RESET MASTER") {
		t.Fatalf("logical sync must clear target GTIDs before importing donor GTIDs")
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
