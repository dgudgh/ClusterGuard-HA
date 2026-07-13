package agent

import (
	"os"
	"path/filepath"
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
  "clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222","vip":"192.0.2.100","interface":"ens160","prefix":24,"mysql_port":3306}]
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.ControllerURLs) != 3 || loaded.ControllerCAFile != "/etc/clusterguard/tls/ca.crt" || loaded.ControllerServerName != "clusterguard.internal" || loaded.ReconcileTimeoutSeconds != 4 {
		t.Fatalf("agent controller configuration=%+v err=%v", loaded, err)
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
