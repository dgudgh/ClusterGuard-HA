package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsConfigurationAndEnvironmentSecret(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "http_address": "127.0.0.1:9090",
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "control_token_env": "CG_TEST_CONTROL",
  "monitoring_token_env": "CG_TEST_MONITORING",
  "approval_token_env": "CG_TEST_APPROVAL",
  "mysql": {
    "enabled": true,
    "semi_sync_required": true,
    "discovery": {"username": "discover", "password_env": "CG_TEST_MYSQL_DISCOVERY"},
    "operation": {"username": "operator", "password_env": "CG_TEST_MYSQL_OPERATION"},
    "replication": {"username": "replicator", "password_env": "CG_TEST_MYSQL_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_APPROVAL", "approve-this")
	t.Setenv("CG_TEST_CONTROL", "control-this")
	t.Setenv("CG_TEST_MONITORING", "monitor-this")
	t.Setenv("CG_TEST_MYSQL_DISCOVERY", "discovery-secret")
	t.Setenv("CG_TEST_MYSQL_OPERATION", "operation-secret")
	t.Setenv("CG_TEST_MYSQL_REPLICATION", "replication-secret")

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if loaded.HTTPAddress != "127.0.0.1:9090" || loaded.ApprovalToken != "approve-this" || loaded.ControlToken != "control-this" || loaded.MonitoringToken != "monitor-this" {
		t.Fatalf("unexpected runtime configuration: %+v", loaded)
	}
	if loaded.MySQL.Discovery.Password != "discovery-secret" || loaded.MySQL.Operation.Password != "operation-secret" || loaded.MySQL.Replication.Password != "replication-secret" || !loaded.MySQL.Enabled {
		t.Fatalf("expected MySQL secret to be resolved: %+v", loaded.MySQL)
	}
	if !loaded.MySQL.SemiSyncRequired {
		t.Fatalf("expected required semi-sync policy: %+v", loaded.MySQL)
	}
	if loaded.MySQL.DiscoveryIntervalSeconds != 1 || loaded.MySQL.DiscoveryTimeoutSeconds != 1 {
		t.Fatalf("unexpected discovery schedule: %+v", loaded.MySQL)
	}
	if loaded.MySQL.AutomaticFailoverEnabled || loaded.MySQL.AutomaticFailoverIntervalSeconds != 1 || loaded.MySQL.AutomaticFailoverRetrySeconds != 30 {
		t.Fatalf("unexpected automatic failover defaults: %+v", loaded.MySQL)
	}
}

func TestLoadResolvesPurposeSpecificMySQLCredentials(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "mysql": {
    "enabled": true,
    "discovery": {"username": "discover", "password_env": "CG_TEST_DISCOVERY"},
    "operation": {"username": "operator", "password_env": "CG_TEST_OPERATION"},
    "replication": {"username": "replicator", "password_env": "CG_TEST_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_DISCOVERY", "discovery-secret")
	t.Setenv("CG_TEST_OPERATION", "operation-secret")
	t.Setenv("CG_TEST_REPLICATION", "replication-secret")

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if loaded.MySQL.Discovery.Username != "discover" || loaded.MySQL.Discovery.Password != "discovery-secret" {
		t.Fatalf("unexpected discovery credentials: %+v", loaded.MySQL.Discovery)
	}
	if loaded.MySQL.Operation.Username != "operator" || loaded.MySQL.Operation.Password != "operation-secret" {
		t.Fatalf("unexpected operation credentials: %+v", loaded.MySQL.Operation)
	}
	if loaded.MySQL.Replication.Username != "replicator" || loaded.MySQL.Replication.Password != "replication-secret" {
		t.Fatalf("unexpected replication credentials: %+v", loaded.MySQL.Replication)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"discovery-secret", "operation-secret", "replication-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized configuration exposed %q: %s", secret, encoded)
		}
	}
}

func TestLoadResolvesPostgreSQLReadOnlyDiscoveryConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "postgresql": {
    "enabled": true,
    "discovery": {
      "username": "cg_monitor",
      "database": "clusterguard",
      "password_env": "CG_TEST_POSTGRESQL_DISCOVERY"
    }
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_POSTGRESQL_DISCOVERY", "postgresql-secret")
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load PostgreSQL configuration: %v", err)
	}
	if !loaded.PostgreSQL.Enabled || loaded.PostgreSQL.DiscoveryIntervalSeconds != 1 || loaded.PostgreSQL.DiscoveryTimeoutSeconds != 1 {
		t.Fatalf("unexpected PostgreSQL schedule: %+v", loaded.PostgreSQL)
	}
	if loaded.PostgreSQL.AutomaticFailoverEnabled || loaded.PostgreSQL.AutomaticFailoverIntervalSeconds != 1 || loaded.PostgreSQL.AutomaticFailoverRetrySeconds != 30 {
		t.Fatalf("unexpected PostgreSQL automatic failover defaults: %+v", loaded.PostgreSQL)
	}
	if loaded.PostgreSQL.Discovery.Username != "cg_monitor" || loaded.PostgreSQL.Discovery.Database != "clusterguard" || loaded.PostgreSQL.Discovery.Password != "postgresql-secret" {
		t.Fatalf("unexpected PostgreSQL credential: %+v", loaded.PostgreSQL.Discovery)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "postgresql-secret") {
		t.Fatalf("serialized configuration exposed PostgreSQL password: %s", encoded)
	}
}

func TestLoadResolvesPurposeSpecificPostgreSQLOperationCredentials(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "postgresql": {
    "enabled": true,
    "discovery": {"username":"cg_monitor","database":"postgres","password_env":"CG_TEST_PG_DISCOVERY"},
    "operation": {"username":"cg_operator","database":"postgres","password_env":"CG_TEST_PG_OPERATION"},
    "replication": {"username":"cg_replication","database":"postgres","password_env":"CG_TEST_PG_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_PG_DISCOVERY", "discovery-secret")
	t.Setenv("CG_TEST_PG_OPERATION", "operation-secret")
	t.Setenv("CG_TEST_PG_REPLICATION", "replication-secret")
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load PostgreSQL operation configuration: %v", err)
	}
	if loaded.PostgreSQL.Operation.Username != "cg_operator" || loaded.PostgreSQL.Operation.Password != "operation-secret" || loaded.PostgreSQL.Replication.Username != "cg_replication" || loaded.PostgreSQL.Replication.Password != "replication-secret" {
		t.Fatalf("PostgreSQL credentials=%+v", loaded.PostgreSQL)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"discovery-secret", "operation-secret", "replication-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized PostgreSQL configuration exposed %q: %s", secret, encoded)
		}
	}
}

func TestLoadRejectsPartialPostgreSQLOperationCredentials(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "postgresql": {
    "enabled": true,
    "discovery": {"username":"cg_monitor","password_env":"CG_TEST_PG_DISCOVERY"},
    "operation": {"username":"cg_operator","password_env":"CG_TEST_PG_OPERATION"}
  }
}`
	t.Setenv("CG_TEST_PG_DISCOVERY", "discovery-secret")
	t.Setenv("CG_TEST_PG_OPERATION", "operation-secret")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "operation and replication") {
		t.Fatalf("partial PostgreSQL mutation credentials error=%v", err)
	}
}

func TestLoadRejectsPostgreSQLAutomaticFailoverWithoutMutationCredentials(t *testing.T) {
	setPostgreSQLAutomaticFailoverSecrets(t)
	path := writePostgreSQLAutomaticFailoverConfig(t, false, true)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "operation and replication credentials") {
		t.Fatalf("PostgreSQL automatic failover credential error=%v", err)
	}
}

func TestLoadRejectsAutomaticFailoverForDisabledEngine(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql"} {
		t.Run(engine, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "control.json")
			contents := `{"metadata_path":` + fmt.Sprintf("%q", filepath.Join(directory, "metadata.json")) + `,` + fmt.Sprintf("%q", engine) + `:{"enabled":false,"automatic_failover_enabled":true}}`
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "must be enabled") {
				t.Fatalf("disabled %s automatic failover error=%v", engine, err)
			}
		})
	}
}

func TestLoadRejectsPostgreSQLAutomaticFailoverWithoutConsensusAndAgent(t *testing.T) {
	setPostgreSQLAutomaticFailoverSecrets(t)
	path := writePostgreSQLAutomaticFailoverConfig(t, true, false)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "automatic failover") {
		t.Fatalf("unsafe PostgreSQL automatic failover configuration error=%v", err)
	}
}

func TestLoadAllowsPostgreSQLAutomaticFailoverWithoutStaticApproval(t *testing.T) {
	setPostgreSQLAutomaticFailoverSecrets(t)
	path := writePostgreSQLAutomaticFailoverConfig(t, true, true)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load PostgreSQL automatic failover configuration: %v", err)
	}
	if !loaded.PostgreSQL.AutomaticFailoverEnabled || loaded.PostgreSQL.AutomaticFailoverIntervalSeconds != 1 || loaded.PostgreSQL.AutomaticFailoverRetrySeconds != 30 || loaded.ApprovalToken != "" {
		t.Fatalf("PostgreSQL automatic failover configuration=%+v approval=%q", loaded.PostgreSQL, loaded.ApprovalToken)
	}
}

func setPostgreSQLAutomaticFailoverSecrets(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"CG_AUTO_PG_DISCOVERY":   "discovery-secret",
		"CG_AUTO_PG_OPERATION":   "operation-secret",
		"CG_AUTO_PG_REPLICATION": "replication-secret",
		"CG_AUTO_PG_AGENT":       "agent-secret",
	} {
		t.Setenv(name, value)
	}
}

func writePostgreSQLAutomaticFailoverConfig(t *testing.T, mutationCredentials, infrastructure bool) string {
	t.Helper()
	directory := t.TempDir()
	postgresql := map[string]any{
		"enabled":                    true,
		"automatic_failover_enabled": true,
		"discovery": map[string]any{
			"username": "discover", "password_env": "CG_AUTO_PG_DISCOVERY",
		},
	}
	if mutationCredentials {
		postgresql["operation"] = map[string]any{"username": "operator", "password_env": "CG_AUTO_PG_OPERATION"}
		postgresql["replication"] = map[string]any{"username": "replicator", "password_env": "CG_AUTO_PG_REPLICATION"}
	}
	configuration := map[string]any{
		"metadata_path": filepath.Join(directory, "metadata.json"),
		"postgresql":    postgresql,
	}
	if infrastructure {
		localID := "11111111-1111-4111-8111-111111111111"
		configuration["consensus"] = map[string]any{
			"enabled": true, "snapshot_cas_enabled": true, "local_id": localID,
			"bind_address": "127.0.0.1:10009", "advertise_address": "127.0.0.1:10009",
			"data_directory": filepath.Join(directory, "raft"), "bootstrap": true,
			"peers": []map[string]any{
				{"resource_id": localID, "address": "127.0.0.1:10009"},
				{"resource_id": "22222222-2222-4222-8222-222222222222", "address": "127.0.0.1:10019"},
				{"resource_id": "33333333-3333-4333-8333-333333333333", "address": "127.0.0.1:10029"},
			},
		}
		configuration["agent"] = map[string]any{
			"enabled": true, "user": "cg-agent",
			"identity_file": "/etc/clusterguard/agent_ed25519", "known_hosts_file": "/etc/clusterguard/agent_known_hosts",
			"shared_secret_env": "CG_AUTO_PG_AGENT",
		}
		configuration["fencing"] = map[string]any{"agent_quorum_enabled": true}
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "control.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsPostgreSQLDatabaseAndRejectsMissingSecret(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "postgresql": {
    "enabled": true,
    "discovery": {"username": "cg_monitor", "password_env": "CG_TEST_POSTGRESQL_MISSING"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "PostgreSQL discovery") {
		t.Fatalf("missing PostgreSQL secret error = %v", err)
	}
	t.Setenv("CG_TEST_POSTGRESQL_MISSING", "secret")
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load PostgreSQL defaults: %v", err)
	}
	if loaded.PostgreSQL.Discovery.Database != "postgres" {
		t.Fatalf("database = %q, want postgres", loaded.PostgreSQL.Discovery.Database)
	}
}

func TestLoadRejectsMissingPurposeSpecificMySQLCredential(t *testing.T) {
	t.Setenv("CG_TEST_DISCOVERY", "discovery-secret")
	t.Setenv("CG_TEST_OPERATION", "operation-secret")
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path": "` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "mysql": {
    "enabled": true,
    "discovery": {"username": "discover", "password_env": "CG_TEST_DISCOVERY"},
    "operation": {"username": "operator", "password_env": "CG_TEST_OPERATION"},
    "replication": {"username": "replicator", "password_env": "CG_TEST_REPLICATION_MISSING"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "replication") {
		t.Fatalf("missing replication secret must be rejected clearly, got %v", err)
	}
}

func TestLoadRejectsConfiguredEmptyControlTokenEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{"metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `","control_token_env":"CG_MISSING_CONTROL"}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("configured empty control token must be rejected")
	}
}

func TestLoadRequiresCompleteAbsoluteTLSCertificatePair(t *testing.T) {
	for _, contents := range []string{
		`{"metadata_path":"/tmp/metadata.json","tls_cert_file":"/etc/clusterguard/tls/server.crt"}`,
		`{"metadata_path":"/tmp/metadata.json","tls_key_file":"/etc/clusterguard/tls/server.key"}`,
		`{"metadata_path":"/tmp/metadata.json","tls_cert_file":"relative.crt","tls_key_file":"relative.key"}`,
		`{"metadata_path":"/tmp/metadata.json","tls_ca_file":"relative-ca.crt"}`,
	} {
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("unsafe TLS configuration was accepted: %s", contents)
		}
	}
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{"metadata_path":"/tmp/metadata.json","tls_cert_file":"/etc/clusterguard/tls/server.crt","tls_key_file":"/etc/clusterguard/tls/server.key","tls_ca_file":"/etc/clusterguard/tls/ca.crt"}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.TLSCertFile == "" || loaded.TLSKeyFile == "" || loaded.TLSCAFile == "" {
		t.Fatalf("complete TLS configuration=%+v err=%v", loaded, err)
	}
}

func TestLoadRejectsExternalPlaintextHTTPUnlessExplicitlyAllowed(t *testing.T) {
	for _, address := range []string{"0.0.0.0:3000", ":3000", "192.0.2.10:3000"} {
		path := filepath.Join(t.TempDir(), "control.json")
		contents := `{"http_address":"` + address + `","metadata_path":"/tmp/metadata.json"}`
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "plaintext HTTP") {
			t.Fatalf("external plaintext HTTP %q error=%v", address, err)
		}
	}

	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{"http_address":"0.0.0.0:3000","allow_insecure_http":true,"metadata_path":"/tmp/metadata.json"}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !loaded.AllowInsecureHTTP {
		t.Fatalf("explicit development HTTP configuration=%+v err=%v", loaded, err)
	}
	if len(loaded.DeprecationWarnings) == 0 || !strings.Contains(loaded.DeprecationWarnings[len(loaded.DeprecationWarnings)-1], "plaintext HTTP") {
		t.Fatalf("missing plaintext HTTP warning: %v", loaded.DeprecationWarnings)
	}
}

func TestLoadRequiresMutualTLSForExternalConsensusTransport(t *testing.T) {
	writeConfig := func(contents string) string {
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	configuration := func(extra string) string {
		return `{
  "metadata_path":"/tmp/metadata.json",
  "tls_cert_file":"/etc/clusterguard/tls/server.crt",
  "tls_key_file":"/etc/clusterguard/tls/server.key",
  "consensus":{
    "enabled":true,
    "local_id":"11111111-1111-4111-8111-111111111111",
    "bind_address":"0.0.0.0:10009",
    "advertise_address":"192.0.2.11:10009",
    "data_directory":"/tmp/raft",
    ` + extra + `
    "peers":[
      {"resource_id":"11111111-1111-4111-8111-111111111111","address":"192.0.2.11:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"192.0.2.12:10009"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"192.0.2.13:10009"}
    ]
  }
}`
	}
	if _, err := Load(writeConfig(configuration(""))); err == nil || !strings.Contains(err.Error(), "Raft TLS") {
		t.Fatalf("external plaintext Raft transport error=%v", err)
	}
	loaded, err := Load(writeConfig(configuration(`
    "tls_cert_file":"/etc/clusterguard/tls/raft.crt",
    "tls_key_file":"/etc/clusterguard/tls/raft.key",
    "tls_ca_file":"/etc/clusterguard/tls/raft-ca.crt",`)))
	if err != nil || loaded.Consensus.TLSCertFile == "" || loaded.Consensus.TLSCAFile == "" {
		t.Fatalf("mutual TLS consensus configuration=%+v err=%v", loaded.Consensus, err)
	}
	loaded, err = Load(writeConfig(configuration(`"allow_insecure_transport":true,`)))
	if err != nil || !loaded.Consensus.AllowInsecureTransport {
		t.Fatalf("explicit insecure consensus configuration=%+v err=%v", loaded.Consensus, err)
	}
	if len(loaded.DeprecationWarnings) == 0 || !strings.Contains(loaded.DeprecationWarnings[len(loaded.DeprecationWarnings)-1], "Raft transport") {
		t.Fatalf("missing insecure Raft warning: %v", loaded.DeprecationWarnings)
	}
	if _, err := Load(writeConfig(configuration(`"tls_cert_file":"/etc/clusterguard/tls/raft.crt",`))); err == nil {
		t.Fatal("partial Raft TLS configuration was accepted")
	}
}

func TestLoadRejectsUnsafeConsensusPeerAPIAddresses(t *testing.T) {
	writeConfig := func(apiAddress string, allowInsecure bool) string {
		path := filepath.Join(t.TempDir(), "control.json")
		contents := fmt.Sprintf(`{
  "http_address":"127.0.0.1:8088",
  "allow_insecure_http":%t,
  "metadata_path":"/tmp/metadata.json",
  "consensus":{
    "enabled":true,
    "snapshot_cas_enabled":true,
    "local_id":"11111111-1111-4111-8111-111111111111",
    "bind_address":"0.0.0.0:10009",
    "advertise_address":"192.0.2.11:10009",
    "data_directory":"/tmp/raft",
    "tls_cert_file":"/etc/clusterguard/tls/raft.crt",
    "tls_key_file":"/etc/clusterguard/tls/raft.key",
    "tls_ca_file":"/etc/clusterguard/tls/raft-ca.crt",
    "peers":[
      {"resource_id":"11111111-1111-4111-8111-111111111111","address":"192.0.2.11:10009","api_address":"https://controller-a.example:8088"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"192.0.2.12:10009","api_address":%q},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"192.0.2.13:10009","api_address":"https://controller-c.example:8088"}
    ]
  }
}`, allowInsecure, apiAddress)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if _, err := Load(writeConfig("http://192.0.2.12:8088", false)); err == nil || !strings.Contains(err.Error(), "peer API") {
		t.Fatalf("external plaintext peer API error=%v", err)
	}
	if _, err := Load(writeConfig("https://controller-a.example:8088", false)); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate peer API error=%v", err)
	}
	loaded, err := Load(writeConfig("http://192.0.2.12:8088", true))
	if err != nil || len(loaded.DeprecationWarnings) == 0 {
		t.Fatalf("explicit insecure peer API configuration=%+v err=%v", loaded.Consensus, err)
	}
}

func TestLoadValidatesExternalFencingProvider(t *testing.T) {
	writeConfig := func(contents string) string {
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := Load(writeConfig(`{"metadata_path":"/tmp/metadata.json","fencing":{"enabled":true,"executable_path":"relative-fencer"}}`)); err == nil {
		t.Fatal("relative external fencer path was accepted")
	}
	configuration := `{
  "metadata_path":"/tmp/metadata.json",
  "consensus":{
    "enabled":true,
    "local_id":"11111111-1111-4111-8111-111111111111",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"/tmp/raft",
    "peers":[
      {"resource_id":"11111111-1111-4111-8111-111111111111","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10010"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10011"}
    ]
  },
  "fencing":{"enabled":true,"executable_path":"/usr/local/libexec/clusterguard-fencer","timeout_seconds":20}
}`
	loaded, err := Load(writeConfig(configuration))
	if err != nil || !loaded.Fencing.Enabled || loaded.Fencing.TimeoutSeconds != 20 {
		t.Fatalf("external fencing configuration=%+v err=%v", loaded.Fencing, err)
	}
}

func TestLoadAllowsLoopbackPlaintextHTTP(t *testing.T) {
	for _, address := range []string{"127.0.0.1:3000", "[::1]:3000", "localhost:3000"} {
		path := filepath.Join(t.TempDir(), "control.json")
		contents := `{"http_address":"` + address + `","metadata_path":"/tmp/metadata.json"}`
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err != nil {
			t.Fatalf("loopback HTTP %q rejected: %v", address, err)
		}
	}
}

func TestLoadTreatsLegacyApprovalTokenAsDeprecatedWithoutRequiringEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{"metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `","approval_token_env":"CG_MISSING_TOKEN"}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("deprecated approval environment should not block startup: %v", err)
	}
	if loaded.ApprovalToken != "" {
		t.Fatal("missing deprecated approval environment unexpectedly produced a token")
	}
	if len(loaded.DeprecationWarnings) != 1 || !strings.Contains(loaded.DeprecationWarnings[0], "CG_APPROVAL_TOKEN") {
		t.Fatalf("deprecation warnings=%v", loaded.DeprecationWarnings)
	}
}

func TestLoadRequiresCompleteCredentialsWhenMySQLIsEnabled(t *testing.T) {
	tests := []struct {
		name        string
		mysqlJSON   string
		environment map[string]string
	}{
		{name: "missing username", mysqlJSON: `{"enabled":true,"password_env":"CG_MYSQL_PASSWORD"}`, environment: map[string]string{"CG_MYSQL_PASSWORD": "secret"}},
		{name: "blank username", mysqlJSON: `{"enabled":true,"username":"   ","password_env":"CG_MYSQL_PASSWORD"}`, environment: map[string]string{"CG_MYSQL_PASSWORD": "secret"}},
		{name: "missing password environment name", mysqlJSON: `{"enabled":true,"username":"discover"}`},
		{name: "blank password environment name", mysqlJSON: `{"enabled":true,"username":"discover","password_env":"   "}`},
		{name: "unresolved password", mysqlJSON: `{"enabled":true,"username":"discover","password_env":"CG_MYSQL_MISSING"}`},
		{name: "blank resolved password", mysqlJSON: `{"enabled":true,"username":"discover","password_env":"CG_MYSQL_PASSWORD"}`, environment: map[string]string{"CG_MYSQL_PASSWORD": "   "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for name, value := range test.environment {
				t.Setenv(name, value)
			}
			path := filepath.Join(t.TempDir(), "control.json")
			contents := `{"metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `","mysql":` + test.mysqlJSON + `}`
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("enabled MySQL with incomplete credentials must fail")
			}
		})
	}
}

func TestLoadAllowsDisabledMySQLWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{"metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `","mysql":{"enabled":false}}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("disabled MySQL credentials should be optional: %v", err)
	}
}

func TestLoadRejectsAutomaticFailoverWithoutConsensusAndAgent(t *testing.T) {
	for name, value := range map[string]string{
		"CG_AUTO_DISCOVERY":   "discovery-secret",
		"CG_AUTO_OPERATION":   "operation-secret",
		"CG_AUTO_REPLICATION": "replication-secret",
	} {
		t.Setenv(name, value)
	}
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "mysql": {
    "enabled":true,
    "automatic_failover_enabled":true,
    "discovery":{"username":"discover","password_env":"CG_AUTO_DISCOVERY"},
    "operation":{"username":"operator","password_env":"CG_AUTO_OPERATION"},
    "replication":{"username":"replicator","password_env":"CG_AUTO_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "automatic failover") {
		t.Fatalf("unsafe automatic failover configuration error=%v", err)
	}
}

func TestLoadKubernetesExecutionRequiresConsensusAndAppliesSafeTimeouts(t *testing.T) {
	write := func(contents string) string {
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := Load(write(`{"metadata_path":"/tmp/metadata.json","kubernetes":{"enabled":true}}`)); err == nil || !strings.Contains(err.Error(), "Raft consensus") {
		t.Fatalf("Kubernetes execution without consensus error=%v", err)
	}
	localID := "11111111-1111-4111-8111-111111111111"
	contents := `{
  "metadata_path":"/tmp/metadata.json",
  "kubernetes":{"enabled":true},
  "consensus":{
    "enabled":true,
    "local_id":"` + localID + `",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"` + filepath.Join(t.TempDir(), "raft") + `",
    "peers":[
      {"resource_id":"` + localID + `","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029"}
    ]
  }
}`
	loaded, err := Load(write(contents))
	if err != nil {
		t.Fatalf("load Kubernetes execution configuration: %v", err)
	}
	if !loaded.Kubernetes.Enabled || loaded.Kubernetes.RequestTimeoutSeconds != 10 || loaded.Kubernetes.FenceTimeoutSeconds != 60 {
		t.Fatalf("Kubernetes execution defaults=%+v", loaded.Kubernetes)
	}
}

func TestLoadAllowsAutomaticFailoverWithoutStaticApproval(t *testing.T) {
	for name, value := range map[string]string{
		"CG_AUTO_DISCOVERY":   "discovery-secret",
		"CG_AUTO_OPERATION":   "operation-secret",
		"CG_AUTO_REPLICATION": "replication-secret",
		"CG_AUTO_AGENT":       "agent-secret",
	} {
		t.Setenv(name, value)
	}
	localID := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "consensus": {
    "enabled":true,
    "snapshot_cas_enabled":true,
    "local_id":"` + localID + `",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"` + filepath.Join(t.TempDir(), "raft") + `",
    "bootstrap":true,
    "peers":[
      {"resource_id":"` + localID + `","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029"}
    ]
  },
  "agent": {
    "enabled":true,
    "user":"cg-agent",
    "identity_file":"/etc/clusterguard/agent_ed25519",
    "known_hosts_file":"/etc/clusterguard/agent_known_hosts",
    "shared_secret_env":"CG_AUTO_AGENT"
  },
  "fencing": {
    "agent_quorum_enabled":true
  },
  "mysql": {
    "enabled":true,
    "automatic_failover_enabled":true,
    "discovery":{"username":"discover","password_env":"CG_AUTO_DISCOVERY"},
    "operation":{"username":"operator","password_env":"CG_AUTO_OPERATION"},
    "replication":{"username":"replicator","password_env":"CG_AUTO_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("automatic failover should not depend on a static approval token: %v", err)
	}
	if !loaded.MySQL.AutomaticFailoverEnabled || !loaded.Fencing.AgentQuorumEnabled || loaded.Fencing.AgentQuorumGraceSeconds != 15 || loaded.ApprovalToken != "" {
		t.Fatalf("automatic failover configuration=%+v approval=%q", loaded.MySQL, loaded.ApprovalToken)
	}
}

func TestLoadRequiresPostgreSQLAutomaticFailoverFencing(t *testing.T) {
	for name, value := range map[string]string{
		"CG_PG_AUTO_DISCOVERY":   "discovery-secret",
		"CG_PG_AUTO_OPERATION":   "operation-secret",
		"CG_PG_AUTO_REPLICATION": "replication-secret",
		"CG_PG_AUTO_AGENT":       "agent-secret",
	} {
		t.Setenv(name, value)
	}
	localID := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "consensus": {
    "enabled":true,
    "snapshot_cas_enabled":true,
    "local_id":"` + localID + `",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"` + filepath.Join(t.TempDir(), "raft") + `",
    "bootstrap":true,
    "peers":[
      {"resource_id":"` + localID + `","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029"}
    ]
  },
  "agent": {
    "enabled":true,
    "user":"cg-agent",
    "identity_file":"/etc/clusterguard/agent_ed25519",
    "known_hosts_file":"/etc/clusterguard/agent_known_hosts",
    "shared_secret_env":"CG_PG_AUTO_AGENT"
  },
  "postgresql": {
    "enabled":true,
    "automatic_failover_enabled":true,
    "discovery":{"username":"discover","password_env":"CG_PG_AUTO_DISCOVERY"},
    "operation":{"username":"operator","password_env":"CG_PG_AUTO_OPERATION"},
    "replication":{"username":"replicator","password_env":"CG_PG_AUTO_REPLICATION"}
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "PostgreSQL automatic failover requires agent quorum fencing or an external fencer") {
		t.Fatalf("unsafe PostgreSQL automatic failover configuration error=%v", err)
	}

	contents = strings.Replace(contents, `"postgresql": {`, `"fencing":{"agent_quorum_enabled":true},"postgresql": {`, 1)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("PostgreSQL Agent quorum failover configuration: %v", err)
	}
	if !loaded.PostgreSQL.AutomaticFailoverEnabled || !loaded.Fencing.AgentQuorumEnabled {
		t.Fatalf("PostgreSQL automatic failover configuration=%+v fencing=%+v", loaded.PostgreSQL, loaded.Fencing)
	}
}

func TestLoadResolvesAgentTransportSecret(t *testing.T) {
	t.Setenv("CG_TEST_AGENT_SECRET", "agent-secret")
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "agent": {
    "enabled": true,
    "user": "cg-agent",
    "identity_file": "/etc/clusterguard/agent_ed25519",
    "known_hosts_file": "/etc/clusterguard/agent_known_hosts",
    "command_timeout_seconds": 37,
    "mutation_timeout_seconds": 901,
	"max_concurrent_sessions": 5,
    "shared_secret_env": "CG_TEST_AGENT_SECRET"
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load agent transport: %v", err)
	}
	if !loaded.Agent.Enabled || loaded.Agent.SharedSecret != "agent-secret" || loaded.Agent.User != "cg-agent" ||
		loaded.Agent.CommandTimeoutSeconds != 37 || loaded.Agent.MutationTimeoutSeconds != 901 || loaded.Agent.MaxConcurrentSessions != 5 {
		t.Fatalf("unexpected agent configuration: %+v", loaded.Agent)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "agent-secret") {
		t.Fatalf("serialized configuration exposed agent secret: %s", encoded)
	}
}

func TestLoadResolvesRaftAndWriteOnlyNodeLifecycleConfiguration(t *testing.T) {
	t.Setenv("CG_TEST_SSH", "ssh-secret")
	t.Setenv("CG_TEST_INSTALL_ROOT", "install-root-secret")
	t.Setenv("CG_TEST_INSTALL_REPLICATION", "install-replication-secret")
	t.Setenv("CG_TEST_POSTGRESQL_ADMIN", "postgresql-admin-secret")
	t.Setenv("CG_TEST_POSTGRESQL_REPLICATION", "postgresql-replication-secret")
	localID := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "consensus": {
    "enabled": true,
    "snapshot_cas_enabled": true,
    "replicated_log_compression_enabled": true,
    "local_id":"` + localID + `",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"` + filepath.Join(t.TempDir(), "raft") + `",
    "bootstrap":true,
    "peers":[
      {"resource_id":"` + localID + `","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029"}
    ]
  },
  "node_lifecycle": {
    "enabled":true,
    "executor_path":"/usr/local/libexec/clusterguard-node-lifecycle.sh",
    "package_repository":"/opt/clusterguard/packages",
    "known_hosts_file":"/etc/clusterguard/known_hosts",
    "jq_binary":"/usr/local/libexec/jq-linux-amd64",
    "control_api_issuer_cert_file":"/etc/clusterguard/pki/api-issuer.crt",
    "control_api_issuer_key_file":"/etc/clusterguard/pki/api-issuer.key",
    "control_raft_issuer_cert_file":"/etc/clusterguard/pki/raft-issuer.crt",
    "control_raft_issuer_key_file":"/etc/clusterguard/pki/raft-issuer.key",
    "control_certificate_validity_days":397,
    "ssh_password_env":"CG_TEST_SSH",
    "mysql_root_password_env":"CG_TEST_INSTALL_ROOT",
    "mysql_root_remote_host":"%",
    "replication_password_env":"CG_TEST_INSTALL_REPLICATION",
    "postgresql_install_helper":"/usr/local/libexec/clusterguard-postgresql-install.sh",
    "postgresql_sync_helper":"/usr/local/libexec/clusterguard-postgresql-sync.sh",
    "postgresql_admin_password_env":"CG_TEST_POSTGRESQL_ADMIN",
    "postgresql_replication_password_env":"CG_TEST_POSTGRESQL_REPLICATION",
    "postgresql_basebackup_available":true,
    "postgresql_rewind_available":true,
    "clone_available":true,
    "xtrabackup_versions":{"8.0":true},
    "logical_dump_allowed":true
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load Raft lifecycle configuration: %v", err)
	}
	if !loaded.Consensus.Enabled || !loaded.Consensus.SnapshotCASEnabled || !loaded.Consensus.ReplicatedLogCompressionEnabled || string(loaded.Consensus.LocalID) != localID || len(loaded.Consensus.Peers) != 3 || !loaded.NodeLifecycle.Enabled || loaded.NodeLifecycle.SSHPassword != "ssh-secret" || loaded.NodeLifecycle.MySQLRootPassword != "install-root-secret" || loaded.NodeLifecycle.MySQLRootRemoteHost != "%" || loaded.NodeLifecycle.ReplicationPassword != "install-replication-secret" || loaded.NodeLifecycle.PostgreSQLAdminPassword != "postgresql-admin-secret" || loaded.NodeLifecycle.PostgreSQLReplicationPassword != "postgresql-replication-secret" || loaded.NodeLifecycle.AdapterRuntimeHelper != "/usr/local/libexec/clusterguard-adapter-runtime-install.sh" || loaded.NodeLifecycle.ControlJoinHelper != "/usr/local/libexec/clusterguard-control-join.sh" || loaded.NodeLifecycle.ControlAPIIssuerKeyFile != "/etc/clusterguard/pki/api-issuer.key" || loaded.NodeLifecycle.ControlRaftIssuerKeyFile != "/etc/clusterguard/pki/raft-issuer.key" || loaded.NodeLifecycle.ControlCertificateValidityDays != 397 || !loaded.NodeLifecycle.PostgreSQLBaseBackupAvailable || !loaded.NodeLifecycle.PostgreSQLRewindAvailable {
		t.Fatalf("loaded Raft lifecycle configuration=%+v", loaded)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ssh-secret", "install-root-secret", "install-replication-secret", "postgresql-admin-secret", "postgresql-replication-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized lifecycle configuration exposed %q: %s", secret, encoded)
		}
	}
}

func TestResolveNodeLifecycleRequiresCompleteControlCertificateIssuers(t *testing.T) {
	configuration := NodeLifecycle{
		ExecutorPath:             "/usr/local/libexec/clusterguard-node-lifecycle.sh",
		PackageRepository:        "/opt/clusterguard/packages",
		KnownHostsFile:           "/etc/clusterguard/known_hosts",
		JQBinary:                 "/usr/local/libexec/jq-linux-amd64",
		ControlAPIIssuerCertFile: "/etc/clusterguard/pki/api-issuer.crt",
	}
	if err := resolveNodeLifecycle(&configuration); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("partial controller issuer configuration error=%v", err)
	}
	configuration.ControlAPIIssuerKeyFile = "/etc/clusterguard/pki/api-issuer.key"
	configuration.ControlRaftIssuerCertFile = "/etc/clusterguard/pki/raft-issuer.crt"
	configuration.ControlRaftIssuerKeyFile = "/etc/clusterguard/pki/raft-issuer.key"
	configuration.ControlCertificateValidityDays = 3651
	if err := resolveNodeLifecycle(&configuration); err == nil || !strings.Contains(err.Error(), "validity") {
		t.Fatalf("unsafe controller certificate validity error=%v", err)
	}
}

func TestMySQLRootRemoteHostValidation(t *testing.T) {
	for _, valid := range []string{"%", "192.168.102.%", "db-admin.example.com", "10.0.0.0/255.255.255.0"} {
		if !validMySQLAccountHost(valid) {
			t.Fatalf("valid MySQL account host %q was rejected", valid)
		}
	}
	for _, invalid := range []string{"", "bad host", "bad\nhost", "root'@'%'", strings.Repeat("a", 256)} {
		if validMySQLAccountHost(invalid) {
			t.Fatalf("invalid MySQL account host %q was accepted", invalid)
		}
	}
}

func TestLoadRejectsIncompletePostgreSQLLifecycleCapability(t *testing.T) {
	t.Setenv("CG_TEST_LIFECYCLE_SSH", "ssh-secret")
	t.Setenv("CG_TEST_LIFECYCLE_MYSQL", "mysql-secret")
	t.Setenv("CG_TEST_LIFECYCLE_REPLICATION", "replication-secret")
	configuration := NodeLifecycle{
		PostgreSQLBaseBackupAvailable: true,
		PostgreSQLInstallHelper:       "/usr/local/libexec/clusterguard-postgresql-install.sh",
		ExecutorPath:                  "/usr/local/libexec/clusterguard-node-lifecycle.sh",
		PackageRepository:             "/opt/clusterguard/packages",
		KnownHostsFile:                "/etc/clusterguard/known_hosts",
		JQBinary:                      "/usr/local/libexec/jq-linux-amd64",
		SSHPasswordEnv:                "CG_TEST_LIFECYCLE_SSH",
		MySQLRootPasswordEnv:          "CG_TEST_LIFECYCLE_MYSQL",
		ReplicationPasswordEnv:        "CG_TEST_LIFECYCLE_REPLICATION",
	}
	if err := resolveNodeLifecycle(&configuration); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("incomplete PostgreSQL lifecycle configuration error=%v", err)
	}
}

func TestLoadDefaultsSnapshotCASProtocolGateToDisabledForUpgrade(t *testing.T) {
	localID := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "consensus": {
    "enabled":true,
    "local_id":"` + localID + `",
    "bind_address":"127.0.0.1:10009",
    "advertise_address":"127.0.0.1:10009",
    "data_directory":"` + filepath.Join(t.TempDir(), "raft") + `",
    "peers":[
      {"resource_id":"` + localID + `","address":"127.0.0.1:10009"},
      {"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"},
      {"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029"}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load legacy consensus configuration: %v", err)
	}
	if loaded.Consensus.SnapshotCASEnabled {
		t.Fatal("legacy configuration silently activated the snapshot CAS protocol")
	}
}

func TestLoadRejectsUnsafeRaftMembershipAndLifecycleWithoutConsensus(t *testing.T) {
	write := func(t *testing.T, contents string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := `{"metadata_path":"/tmp/metadata.json","consensus":{"enabled":true,"local_id":"11111111-1111-4111-8111-111111111111","bind_address":"127.0.0.1:10009","advertise_address":"127.0.0.1:10009","data_directory":"/tmp/raft","peers":[%s]}}`
	twoPeers := `{"resource_id":"11111111-1111-4111-8111-111111111111","address":"127.0.0.1:10009"},{"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019"}`
	if _, err := Load(write(t, fmt.Sprintf(base, twoPeers))); err == nil {
		t.Fatal("even two-controller Raft membership was accepted")
	}
	withoutConsensus := `{"metadata_path":"/tmp/metadata.json","node_lifecycle":{"enabled":true,"executor_path":"/x","package_repository":"/x","known_hosts_file":"/x","jq_binary":"/x","ssh_password_env":"X","mysql_root_password_env":"Y","replication_password_env":"Z"}}`
	if _, err := Load(write(t, withoutConsensus)); err == nil {
		t.Fatal("real node lifecycle was enabled without Raft consensus")
	}
}

func TestLoadAcceptsOnlyAbsoluteHTTPControllerAPIAddresses(t *testing.T) {
	localID := "11111111-1111-4111-8111-111111111111"
	configuration := func(apiAddress string) string {
		return `{"metadata_path":"/tmp/metadata.json","consensus":{"enabled":true,"local_id":"` + localID + `","bind_address":"127.0.0.1:10009","advertise_address":"127.0.0.1:10009","data_directory":"/tmp/raft","peers":[` +
			`{"resource_id":"` + localID + `","address":"127.0.0.1:10009","api_address":"` + apiAddress + `"},` +
			`{"resource_id":"22222222-2222-4222-8222-222222222222","address":"127.0.0.1:10019","api_address":"https://127.0.0.1:3019"},` +
			`{"resource_id":"33333333-3333-4333-8333-333333333333","address":"127.0.0.1:10029","api_address":"https://127.0.0.1:3029"}]}}`
	}
	write := func(contents string) string {
		path := filepath.Join(t.TempDir(), "control.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	loaded, err := Load(write(configuration("https://127.0.0.1:3009")))
	if err != nil || loaded.Consensus.Peers[0].APIAddress != "https://127.0.0.1:3009" {
		t.Fatalf("valid controller API address configuration=%+v err=%v", loaded.Consensus.Peers, err)
	}
	for _, invalid := range []string{"127.0.0.1:3009", "ftp://127.0.0.1:3009", "https://user:pass@127.0.0.1:3009/path"} {
		if _, err := Load(write(configuration(invalid))); err == nil {
			t.Fatalf("invalid controller API address %q was accepted", invalid)
		}
	}
}

func TestOfficialDistributionUsesClusterGuardPathsAndServiceName(t *testing.T) {
	examplePath := filepath.Join("..", "..", "configs", "clusterguard.example.json")
	contents, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read example configuration: %v", err)
	}
	configuration := File{}
	if err := json.Unmarshal(contents, &configuration); err != nil {
		t.Fatalf("decode example configuration: %v", err)
	}
	if configuration.MetadataPath != "/var/lib/clusterguard/metadata.json" {
		t.Fatalf("metadata path = %q", configuration.MetadataPath)
	}
	if configuration.HTTPAddress != "0.0.0.0:8088" {
		t.Fatalf("clustered example HTTP address = %q, want a peer-reachable TLS listener", configuration.HTTPAddress)
	}
	if configuration.MySQL.AutomaticFailoverEnabled || configuration.MySQL.AutomaticFailoverIntervalSeconds != 1 || configuration.MySQL.AutomaticFailoverRetrySeconds != 30 {
		t.Fatalf("distribution automatic failover defaults=%+v", configuration.MySQL)
	}

	servicePath := filepath.Join("..", "..", "packaging", "systemd", "clusterguard-ha.service")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read systemd service: %v", err)
	}
	for _, contract := range []string{
		"ExecStartPre=/usr/local/bin/clusterguard --config /etc/clusterguard/clusterguard.json --check-config",
		"ExecStart=/usr/local/bin/clusterguard --config /etc/clusterguard/clusterguard.json",
		"EnvironmentFile=-/etc/clusterguard/clusterguard.env",
		"Environment=PATH=/usr/local/mysql/bin:/usr/pgsql-16/bin:/usr/lib/postgresql/16/bin:/opt/mssql-tools18/bin:/opt/mssql-tools/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin",
		"ConfigurationDirectoryMode=0750",
		"StateDirectory=clusterguard",
		"StateDirectoryMode=0750",
		"LogsDirectory=clusterguard",
		"LogsDirectoryMode=0751",
		"Restart=always",
	} {
		if !strings.Contains(string(service), contract) {
			t.Fatalf("systemd service missing %q", contract)
		}
	}
	for _, forbidden := range []string{
		"Requires=mysqld.service", "Requires=mysql.service", "Requires=postgresql.service",
		"Wants=mysqld.service", "Wants=mysql.service", "Wants=postgresql.service",
		"After=mysqld.service", "After=mysql.service", "After=postgresql.service",
	} {
		if strings.Contains(string(service), forbidden) {
			t.Fatalf("control plane must start independently of database services, found %q", forbidden)
		}
	}
}
