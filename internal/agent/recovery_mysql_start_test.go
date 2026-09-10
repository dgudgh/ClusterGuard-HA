package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryMySQLPersistedFencePreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mysqld-auto.cnf")
	original := []byte(`{"Version":2,"mysql_dynamic_variables":{"read_only":{"Value":"OFF","Metadata":{"Username":"operator"}},"super_read_only":{"Value":"OFF","Metadata":{"Username":"operator"}},"max_connections":{"Value":"200"}},"mysql_server_static_options":{"skip_name_resolve":{"Value":"ON"}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := fenceMySQLPersistedVariables(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]interface{}
	if err = json.Unmarshal(after, &actual); err != nil {
		t.Fatal(err)
	}
	dynamic := actual["mysql_dynamic_variables"].(map[string]interface{})
	for _, name := range []string{"read_only", "super_read_only"} {
		if dynamic[name].(map[string]interface{})["Value"] != "ON" {
			t.Fatal("restart fence not applied")
		}
	}
	if dynamic["max_connections"].(map[string]interface{})["Value"] != "200" || actual["mysql_server_static_options"] == nil {
		t.Fatal("unrelated settings changed")
	}
	backup, err := os.ReadFile(path + ".clusterguard-before-" + recoveryFileDigest(original)[:16])
	if err != nil || string(backup) != string(original) {
		t.Fatal("exact original not retained")
	}
	if err = fenceMySQLPersistedVariables(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryMySQLRestartRejectsUnqualifiedSettings(t *testing.T) {
	valid := "  datadir /var/lib/mysql/\n persisted-globals-load TRUE\n init-file (No default value)\n skip-grant-tables FALSE\n"
	if settings, err := parseMySQLStartupSettings([]byte(valid)); err != nil || settings["datadir"] != "/var/lib/mysql/" {
		t.Fatalf("valid settings rejected: %v", err)
	}
	for _, bad := range []string{strings.Replace(valid, "load TRUE", "load FALSE", 1), strings.Replace(valid, "(No default value)", "/tmp/unreviewed.sql", 1), strings.Replace(valid, "tables FALSE", "tables TRUE", 1)} {
		if _, err := parseMySQLStartupSettings([]byte(bad)); err == nil {
			t.Fatal("unsafe startup accepted")
		}
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "persisted")
	if err := os.WriteFile(target, []byte(`{"Version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "mysqld-auto.cnf")); err != nil {
		t.Fatal(err)
	}
	if err := fenceMySQLPersistedVariables(dir); err == nil {
		t.Fatal("persisted-role symlink accepted")
	}
}
