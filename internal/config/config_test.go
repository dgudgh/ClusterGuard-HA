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
	if loaded.MySQL.DiscoveryIntervalSeconds != 5 || loaded.MySQL.DiscoveryTimeoutSeconds != 4 {
		t.Fatalf("unexpected discovery schedule: %+v", loaded.MySQL)
	}
	if loaded.MySQL.AutomaticFailoverEnabled || loaded.MySQL.AutomaticFailoverIntervalSeconds != 5 || loaded.MySQL.AutomaticFailoverRetrySeconds != 30 {
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
	contents := `{"metadata_path":"/tmp/metadata.json","tls_cert_file":"/etc/clusterguard/tls/server.crt","tls_key_file":"/etc/clusterguard/tls/server.key"}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.TLSCertFile == "" || loaded.TLSKeyFile == "" {
		t.Fatalf("complete TLS configuration=%+v err=%v", loaded, err)
	}
}

func TestLoadRejectsMissingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.json")
	if err := os.WriteFile(path, []byte(`{"approval_token_env":"CG_MISSING_TOKEN"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing approval secret to be rejected")
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

func TestLoadRejectsAutomaticFailoverWithoutConsensusAgentAndApproval(t *testing.T) {
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
	if !loaded.Agent.Enabled || loaded.Agent.SharedSecret != "agent-secret" || loaded.Agent.User != "cg-agent" {
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
	localID := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(t.TempDir(), "control.json")
	contents := `{
  "metadata_path":"` + filepath.Join(t.TempDir(), "metadata.json") + `",
  "consensus": {
    "enabled": true,
    "snapshot_cas_enabled": true,
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
    "ssh_password_env":"CG_TEST_SSH",
    "mysql_root_password_env":"CG_TEST_INSTALL_ROOT",
    "replication_password_env":"CG_TEST_INSTALL_REPLICATION",
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
	if !loaded.Consensus.Enabled || !loaded.Consensus.SnapshotCASEnabled || string(loaded.Consensus.LocalID) != localID || len(loaded.Consensus.Peers) != 3 || !loaded.NodeLifecycle.Enabled || loaded.NodeLifecycle.SSHPassword != "ssh-secret" || loaded.NodeLifecycle.MySQLRootPassword != "install-root-secret" || loaded.NodeLifecycle.ReplicationPassword != "install-replication-secret" {
		t.Fatalf("loaded Raft lifecycle configuration=%+v", loaded)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ssh-secret", "install-root-secret", "install-replication-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized lifecycle configuration exposed %q: %s", secret, encoded)
		}
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
	if configuration.MySQL.AutomaticFailoverEnabled || configuration.MySQL.AutomaticFailoverIntervalSeconds != 5 || configuration.MySQL.AutomaticFailoverRetrySeconds != 30 {
		t.Fatalf("distribution automatic failover defaults=%+v", configuration.MySQL)
	}

	servicePath := filepath.Join("..", "..", "packaging", "systemd", "clusterguard-ha.service")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read systemd service: %v", err)
	}
	for _, contract := range []string{
		"ExecStart=/usr/local/bin/clusterguard --config /etc/clusterguard/clusterguard.json",
		"EnvironmentFile=-/etc/clusterguard/clusterguard.env",
		"Environment=PATH=/usr/local/mysql/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin",
		"ConfigurationDirectoryMode=0750",
		"StateDirectory=clusterguard",
		"StateDirectoryMode=0750",
		"LogsDirectory=clusterguard",
		"LogsDirectoryMode=0750",
	} {
		if !strings.Contains(string(service), contract) {
			t.Fatalf("systemd service missing %q", contract)
		}
	}
}
