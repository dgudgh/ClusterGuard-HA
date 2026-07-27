package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigReadsSecureControllerReconcileSettings(t *testing.T) {
	t.Setenv("CG_AGENT_TEST_SECRET", "agent-secret")
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{
  "shared_secret_env":"CG_AGENT_TEST_SECRET",
  "controller_urls":["https://192.0.2.10:8088","https://192.0.2.11:8088","https://192.0.2.12:8088"],
  "controller_ca_file":"/etc/clusterguard/tls/ca.crt",
	"controller_server_name":"clusterguard.internal",
	"reconcile_timeout_seconds":4,
	"decision_state_directory":"/var/lib/clusterguard-agent/decisions",
	"mutation_state_directory":"/var/lib/clusterguard-agent/mutations-secure",
  "clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222","vip":"192.0.2.100","interface":"ens160","prefix":24,"mysql_port":3306,"mysql_binary":"/opt/clusterguard/mysql/3306/software/bin/mysql"}]
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.ControllerURLs) != 3 || loaded.ControllerCAFile != "/etc/clusterguard/tls/ca.crt" || loaded.ControllerServerName != "clusterguard.internal" || loaded.ReconcileTimeoutSeconds != 4 || loaded.DecisionStateDirectory != "/var/lib/clusterguard-agent/decisions" || loaded.MutationStateDirectory != "/var/lib/clusterguard-agent/mutations-secure" {
		t.Fatalf("agent controller configuration=%+v err=%v", loaded, err)
	}
	policy := loaded.Clusters["11111111-1111-4111-8111-111111111111"]
	if policy.MySQLBinary != "/opt/clusterguard/mysql/3306/software/bin/mysql" {
		t.Fatalf("per-cluster mysql binary=%q", policy.MySQLBinary)
	}
}

func TestLoadConfigDefaultsAndValidatesDecisionStateDirectory(t *testing.T) {
	t.Setenv("CG_AGENT_TEST_SECRET", "agent-secret")
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.json")
	contents := `{"shared_secret_env":"CG_AGENT_TEST_SECRET","controller_urls":["https://192.0.2.10:8088"],"clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222"}]}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.DecisionStateDirectory != "/var/lib/clusterguard-agent/decisions" || loaded.MutationStateDirectory != "/var/lib/clusterguard-agent/mutations" {
		t.Fatalf("default state directories decisions=%q mutations=%q err=%v", loaded.DecisionStateDirectory, loaded.MutationStateDirectory, err)
	}
	contents = `{"shared_secret_env":"CG_AGENT_TEST_SECRET","controller_urls":["https://192.0.2.10:8088"],"decision_state_directory":"relative","clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222"}]}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("relative decision state directory was accepted")
	}
	contents = `{"shared_secret_env":"CG_AGENT_TEST_SECRET","controller_urls":["https://192.0.2.10:8088"],"mutation_state_directory":"relative","clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222"}]}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("relative mutation state directory was accepted")
	}
}

func TestLoadConfigRejectsPlaintextControllerByDefault(t *testing.T) {
	t.Setenv("CG_AGENT_TEST_SECRET", "agent-secret")
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{"shared_secret_env":"CG_AGENT_TEST_SECRET","controller_urls":["http://192.0.2.10:8088"],"clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222"}]}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("plaintext controller URL was accepted")
	}
}

func TestLoadConfigValidatesRestrictedPostgreSQLPolicy(t *testing.T) {
	t.Setenv("CG_AGENT_TEST_SECRET", "agent-secret")
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{
  "shared_secret_env":"CG_AGENT_TEST_SECRET",
	  "clusters":[{
	    "cluster_id":"11111111-1111-4111-8111-111111111111",
	    "instance_id":"22222222-2222-4222-8222-222222222222",
	    "engine":"postgresql",
	    "postgresql_node_id":"44444444-4444-4444-8444-444444444444",
	    "postgresql_port":5432,
    "postgresql_service":" postgresql-16 ",
    "postgresql_user":" postgres ",
    "postgresql_data_directory":"/var/lib/postgresql/16/main",
    "postgresql_binary_directory":"/usr/lib/postgresql/16/bin",
    "postgresql_passfile":"/etc/clusterguard/pgpass",
    "postgresql_database":" clusterguard ",
    "postgresql_replication_user":" replicator ",
	    "postgresql_peers":[{"instance_id":"33333333-3333-4333-8333-333333333333","node_id":"55555555-5555-4555-8555-555555555555","hostname":" pg-01 ","ip_address":" 192.0.2.10 ","port":5432}]
	  }]
	}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load PostgreSQL policy: %v", err)
	}
	policy := loaded.Clusters["11111111-1111-4111-8111-111111111111"]
	if policy.Engine != "postgresql" || string(policy.PostgreSQLNodeID) != "44444444-4444-4444-8444-444444444444" || policy.PostgreSQLService != "postgresql-16" || policy.PostgreSQLUser != "postgres" || policy.PostgreSQLDatabase != "clusterguard" || policy.PostgreSQLReplicationUser != "replicator" || len(policy.PostgreSQLPeers) != 1 || string(policy.PostgreSQLPeers[0].NodeID) != "55555555-5555-4555-8555-555555555555" || policy.PostgreSQLPeers[0].Hostname != "pg-01" || policy.PostgreSQLPeers[0].IPAddress != "192.0.2.10" {
		t.Fatalf("PostgreSQL policy=%+v", policy)
	}

	for name, replacement := range map[string]string{
		"relative data directory":  `"postgresql_data_directory":"relative"`,
		"unsafe service":           `"postgresql_service":"postgresql;shutdown"`,
		"invalid peer port":        `"port":0`,
		"missing database":         `"postgresql_database":""`,
		"missing replication user": `"postgresql_replication_user":""`,
	} {
		t.Run(name, func(t *testing.T) {
			invalid := contents
			switch name {
			case "relative data directory":
				invalid = strings.Replace(invalid, `"postgresql_data_directory":"/var/lib/postgresql/16/main"`, replacement, 1)
			case "unsafe service":
				invalid = strings.Replace(invalid, `"postgresql_service":" postgresql-16 "`, replacement, 1)
			case "invalid peer port":
				invalid = strings.Replace(invalid, `"port":5432`, replacement, 1)
			case "missing database":
				invalid = strings.Replace(invalid, `"postgresql_database":" clusterguard "`, replacement, 1)
			case "missing replication user":
				invalid = strings.Replace(invalid, `"postgresql_replication_user":" replicator "`, replacement, 1)
			}
			invalidPath := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(invalidPath, []byte(invalid), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(invalidPath); err == nil {
				t.Fatalf("invalid PostgreSQL policy was accepted: %s", invalid)
			}
		})
	}
}

func TestLoadConfigValidatesRestrictedOraclePolicy(t *testing.T) {
	t.Setenv("CG_AGENT_TEST_SECRET", "agent-secret")
	t.Setenv("CG_ORACLE_TEST_PASSWORD", "oracle-secret")
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := `{
  "shared_secret_env":"CG_AGENT_TEST_SECRET",
  "clusters":[{
    "cluster_id":"11111111-1111-4111-8111-111111111111",
    "instance_id":"22222222-2222-4222-8222-222222222222",
    "engine":"oracle",
    "oracle_home":"/u01/app/oracle/product/19.3.0/db",
    "oracle_sid":"mesdb",
    "oracle_os_user":"oracle",
    "oracle_dgmgrl_binary":"/u01/app/oracle/product/19.3.0/db/bin/dgmgrl",
    "oracle_username":"clusterguard_dg",
    "oracle_authentication_role":"sysdg",
    "oracle_password_env":"CG_ORACLE_TEST_PASSWORD",
    "oracle_connect_identifier":"mesdb",
    "oracle_database_unique_name":"mesdb",
    "oracle_broker_configuration":"MESDB_DG",
    "oracle_members":["mesdb","reportdb"]
  }]
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load Oracle policy: %v", err)
	}
	policy := loaded.Clusters["11111111-1111-4111-8111-111111111111"]
	if policy.Engine != "oracle" || policy.OraclePassword != "oracle-secret" ||
		policy.OracleAuthenticationRole != "sysdg" ||
		policy.OracleDatabaseUniqueName != "mesdb" || len(policy.OracleMembers) != 2 {
		t.Fatalf("Oracle policy=%+v", policy)
	}

	for name, values := range map[string][2]string{
		"binary": {`"oracle_dgmgrl_binary":"/u01/app/oracle/product/19.3.0/db/bin/dgmgrl"`, `"oracle_dgmgrl_binary":"/bin/sh"`},
		"member": {`"oracle_members":["mesdb","reportdb"]`, `"oracle_members":["mesdb","reportdb;shutdown"]`},
		"user":   {`"oracle_os_user":"oracle"`, `"oracle_os_user":"oracle;root"`},
		"role":   {`"oracle_authentication_role":"sysdg"`, `"oracle_authentication_role":"normal"`},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := strings.Replace(contents, values[0], values[1], 1)
			invalidPath := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(invalidPath, []byte(invalid), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(invalidPath); err == nil {
				t.Fatalf("invalid Oracle policy was accepted: %s", invalid)
			}
		})
	}
}
