package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributedOverridesAreTypedValidatedAndBoundToLoadedProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := map[string]any{"http_address": "127.0.0.1:3000", "allow_insecure_http": true, "metadata_path": filepath.Join(dir, "metadata.json"), "mysql": map[string]any{"discovery_interval_seconds": 2}}
	b, _ := json.Marshal(raw)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	before, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	values := map[string]int{"mysql.discovery_interval_seconds": 8}
	v := ConfigurationOverrides{SchemaVersion: 1, TaskID: "task-test", Values: values, Digest: ConfigurationDigest(values)}
	override := ConfigurationOverridePath(before.MetadataPath)
	if e = WriteConfigurationOverrides(override, v); e != nil {
		t.Fatal(e)
	}
	if before.AppliedConfiguration.TaskID != "" || before.MySQL.DiscoveryIntervalSeconds != 2 {
		t.Fatal("old process changed on write")
	}
	after, e := Load(path)
	if e != nil || after.MySQL.DiscoveryIntervalSeconds != 8 || after.AppliedConfiguration.TaskID != v.TaskID {
		t.Fatalf("load actual overlay: %v", e)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != string(b) {
		t.Fatal("original file modified")
	}
	encoded, _ := json.Marshal(after)
	if strings.Contains(string(encoded), "task-test") {
		t.Fatal("internal receipt entered config JSON")
	}
	for _, changes := range []map[string]int{{"fencing.enabled": 0}, {"http_address": 3}, {"agent.command_timeout_seconds": 0}, {"mysql.discovery.password": 5}} {
		if _, e = ValidateConfigurationCandidate(path, changes); e == nil {
			t.Fatal("unsafe candidate accepted")
		}
	}
	v.Digest = "bad"
	if e = WriteConfigurationOverrides(override, v); e == nil {
		t.Fatal("bad digest accepted")
	}
	_ = os.WriteFile(override, []byte(`{"schema_version":1,"task_id":"test","values":{},"digest":"bad","unknown":1}`), 0600)
	if _, e = Load(path); e == nil {
		t.Fatal("invalid override did not stop startup")
	}
}
func TestDistributedOverrideRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "target")
	_ = os.WriteFile(dest, []byte("untouched"), 0600)
	link := filepath.Join(dir, "runtime-configuration.json")
	if e := os.Symlink(dest, link); e != nil {
		t.Fatal(e)
	}
	values := map[string]int{"agent.command_timeout_seconds": 10}
	v := ConfigurationOverrides{SchemaVersion: 1, TaskID: "test", Values: values, Digest: ConfigurationDigest(values)}
	if e := WriteConfigurationOverrides(link, v); e == nil {
		t.Fatal("wrote symlink")
	}
	if _, e := ReadConfigurationOverrides(link); e == nil {
		t.Fatal("read symlink")
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "untouched" {
		t.Fatal("symlink target changed")
	}
}

func TestCandidateAllowsNullDisabledSectionWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	raw := `{"http_address":"127.0.0.1:3000","allow_insecure_http":true,"metadata_path":"` + filepath.Join(dir, "metadata.json") + `","mysql":null}`
	_ = os.WriteFile(p, []byte(raw), 0600)
	candidate, e := ValidateConfigurationCandidate(p, map[string]int{"mysql.discovery_interval_seconds": 4})
	if e != nil || candidate.MySQL.DiscoveryIntervalSeconds != 4 {
		t.Fatalf("null section candidate: %v", e)
	}
}
