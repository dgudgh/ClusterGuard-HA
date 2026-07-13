package config

import (
	"encoding/json"
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
  "approval_token_env": "CG_TEST_APPROVAL",
  "mysql": {"enabled": true, "username": "discover", "password_env": "CG_TEST_MYSQL_PASSWORD"}
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_APPROVAL", "approve-this")
	t.Setenv("CG_TEST_CONTROL", "control-this")
	t.Setenv("CG_TEST_MYSQL_PASSWORD", "secret")

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if loaded.HTTPAddress != "127.0.0.1:9090" || loaded.ApprovalToken != "approve-this" || loaded.ControlToken != "control-this" {
		t.Fatalf("unexpected runtime configuration: %+v", loaded)
	}
	if loaded.MySQL.Password != "secret" || !loaded.MySQL.Enabled {
		t.Fatalf("expected MySQL secret to be resolved: %+v", loaded.MySQL)
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

	servicePath := filepath.Join("..", "..", "packaging", "systemd", "clusterguard-ha.service")
	service, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read systemd service: %v", err)
	}
	for _, contract := range []string{
		"ExecStart=/usr/local/bin/clusterguard --config /etc/clusterguard/clusterguard.json",
		"EnvironmentFile=-/etc/clusterguard/clusterguard.env",
		"Environment=PATH=/usr/local/mysql/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin",
		"StateDirectory=clusterguard",
		"LogsDirectory=clusterguard",
	} {
		if !strings.Contains(string(service), contract) {
			t.Fatalf("systemd service missing %q", contract)
		}
	}
}
