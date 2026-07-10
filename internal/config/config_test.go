package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsConfigurationAndEnvironmentSecret(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	contents := `{
  "http_address": "127.0.0.1:9090",
  "metadata_path": "` + filepath.Join(directory, "metadata.json") + `",
  "approval_token_env": "CG_TEST_APPROVAL",
  "mysql": {"enabled": true, "username": "discover", "password_env": "CG_TEST_MYSQL_PASSWORD"}
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CG_TEST_APPROVAL", "approve-this")
	t.Setenv("CG_TEST_MYSQL_PASSWORD", "secret")

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if loaded.HTTPAddress != "127.0.0.1:9090" || loaded.ApprovalToken != "approve-this" {
		t.Fatalf("unexpected runtime configuration: %+v", loaded)
	}
	if loaded.MySQL.Password != "secret" || !loaded.MySQL.Enabled {
		t.Fatalf("expected MySQL secret to be resolved: %+v", loaded.MySQL)
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
