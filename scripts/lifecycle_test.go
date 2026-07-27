package scripts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNodeLifecycleScriptsAreSyntaxValidAndExposeStructuredStages(t *testing.T) {
	paths := []string{
		"clusterguard-node-lifecycle.sh", "clusterguard-mysql-install.sh", "clusterguard-mysql-sync.sh",
		"clusterguard-postgresql-install.sh", "clusterguard-postgresql-sync.sh", "clusterguard-mysql-probe-cleanup.sh",
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
		`result+=(-p "${port}")`,
		`result+=(-P "${port}")`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("lifecycle SSH transport does not honor configured ports: missing %q", required)
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
	for _, required := range []string{
		"clone", "xtrabackup", "logical_dump", "CHANGE MASTER TO", "CHANGE REPLICATION SOURCE TO",
		"MASTER_CONNECT_RETRY=5", "MASTER_RETRY_COUNT=86400", "SOURCE_CONNECT_RETRY=5", "SOURCE_RETRY_COUNT=86400",
		"GET_SOURCE_PUBLIC_KEY=1", "START SLAVE", "START REPLICA", "super_read_only",
	} {
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

func TestLogicalDumpPurgesStaleTargetSchemasBeforeImport(t *testing.T) {
	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	purge := strings.Index(syncText, "target_database_hex")
	dump := strings.Index(syncText, "--set-gtid-purged=ON")
	if purge < 0 || dump < 0 || purge > dump {
		t.Fatal("logical rebuild must purge target-only user schemas before importing the donor dump")
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
	if targetPurge < 0 || donorDump < 0 || targetPurge > donorDump {
		t.Fatal("logical rebuild must purge target schemas before selecting donor dump schemas")
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
			t.Fatalf("logical rebuild must still purge legacy target schema %q before donor filtering", legacySchema)
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

func TestExistingRegisteredMySQLCanBeResynchronizedWithoutManagedInstallPath(t *testing.T) {
	installContents, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	installText := string(installContents)
	for _, required := range []string{"CG_MYSQL_CLIENT", "command -v mysql", "existing MySQL instance accepted for synchronization"} {
		if !strings.Contains(installText, required) {
			t.Fatalf("install helper cannot adopt an existing registered MySQL instance: missing %q", required)
		}
	}

	syncContents, err := os.ReadFile("clusterguard-mysql-sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	syncText := string(syncContents)
	for _, required := range []string{"CG_MYSQL_CLIENT", "command -v mysql", "SELECT @@basedir", `bin/mysqldump`} {
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
