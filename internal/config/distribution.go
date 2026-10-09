package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ConfigurationField is an explicit allow-list. Identity, credentials, executable
// paths, transport and fencing switches require their own migration procedures.
type ConfigurationField struct {
	Path    string `json:"path"`
	Minimum int    `json:"minimum"`
	Maximum int    `json:"maximum"`
	Scope   string `json:"scope"`
}

func EditableConfigurationFields() []ConfigurationField {
	fields := []ConfigurationField{}
	for _, engine := range []string{"mysql", "postgresql", "oracle", "sqlserver"} {
		fields = append(fields, ConfigurationField{engine + ".discovery_interval_seconds", 1, 3600, "cluster"}, ConfigurationField{engine + ".discovery_timeout_seconds", 1, 600, "cluster"})
	}
	for _, engine := range []string{"mysql", "postgresql"} {
		fields = append(fields, ConfigurationField{engine + ".automatic_failover_interval_seconds", 1, 3600, "cluster"}, ConfigurationField{engine + ".automatic_failover_retry_seconds", 1, 3600, "cluster"}, ConfigurationField{engine + ".automatic_failover_minimum_observations", 2, 100, "cluster"}, ConfigurationField{engine + ".automatic_failover_failure_window_seconds", 1, 3600, "cluster"}, ConfigurationField{engine + ".automatic_failover_operation_timeout_seconds", 30, 3600, "cluster"})
	}
	return append(fields, ConfigurationField{"consensus.apply_timeout_seconds", 1, 60, "node"}, ConfigurationField{"agent.command_timeout_seconds", 1, 600, "cluster"}, ConfigurationField{"agent.mutation_timeout_seconds", 1, 3600, "cluster"}, ConfigurationField{"agent.max_concurrent_sessions", 1, 128, "node"}, ConfigurationField{"fencing.timeout_seconds", 1, 600, "cluster"}, ConfigurationField{"fencing.agent_quorum_grace_seconds", 1, 60, "cluster"}, ConfigurationField{"node_lifecycle.control_certificate_validity_days", 1, 3650, "cluster"})
}

// ConfigurationRequiresAllVoters prevents Leader changes from changing failover,
// discovery, fencing or operation decisions because only a subset was updated.
func ConfigurationRequiresAllVoters(values map[string]int) bool {
	for _, f := range EditableConfigurationFields() {
		if _, changed := values[f.Path]; changed && f.Scope == "cluster" {
			return true
		}
	}
	return false
}

const DefaultConfigurationStepTimeoutSeconds = 180

type ConfigurationDistribution struct {
	StepTimeoutSeconds int `json:"step_timeout_seconds,omitempty"`
}

func (c ConfigurationDistribution) EffectiveStepTimeoutSeconds() int {
	if c.StepTimeoutSeconds == 0 {
		return DefaultConfigurationStepTimeoutSeconds
	}
	return c.StepTimeoutSeconds
}

func ValidateConfigurationChanges(values map[string]int) error {
	if len(values) == 0 || len(values) > 64 {
		return fmt.Errorf("configuration changes must contain 1..64 fields")
	}
	allowed := map[string]ConfigurationField{}
	for _, f := range EditableConfigurationFields() {
		allowed[f.Path] = f
	}
	for key, value := range values {
		f, ok := allowed[key]
		if !ok {
			return fmt.Errorf("configuration field %s is not editable", key)
		}
		if value < f.Minimum || value > f.Maximum {
			return fmt.Errorf("configuration field %s must be between %d and %d", key, f.Minimum, f.Maximum)
		}
	}
	return nil
}

type ConfigurationOverrides struct {
	SchemaVersion int            `json:"schema_version"`
	TaskID        string         `json:"task_id"`
	Values        map[string]int `json:"values"`
	Digest        string         `json:"digest"`
}

func ConfigurationDigest(values map[string]int) string {
	b, _ := json.Marshal(values)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func ConfigurationOverridePath(metadataPath string) string {
	return filepath.Join(filepath.Dir(metadataPath), "runtime-configuration.json")
}
func ReadConfigurationOverrides(path string) (ConfigurationOverrides, error) {
	var v ConfigurationOverrides
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return v, fmt.Errorf("configuration override must be a small regular file")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return v, e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil {
		return v, e
	}
	if e = d.Decode(&struct{}{}); e != io.EOF {
		return v, fmt.Errorf("configuration override has trailing data")
	}
	if v.SchemaVersion != 1 || v.TaskID == "" || v.Digest != ConfigurationDigest(v.Values) {
		return v, fmt.Errorf("configuration override identity/digest mismatch")
	}
	if len(v.Values) > 0 {
		if e = ValidateConfigurationChanges(v.Values); e != nil {
			return v, e
		}
	}
	return v, nil
}
func mergeConfigurationValues(contents []byte, values map[string]int) ([]byte, error) {
	var top map[string]json.RawMessage
	if e := json.Unmarshal(contents, &top); e != nil {
		return nil, e
	}
	for path, value := range values {
		section, key, ok := strings.Cut(path, ".")
		if !ok {
			return nil, fmt.Errorf("invalid configuration path")
		}
		nested := map[string]json.RawMessage{}
		if raw, found := top[section]; found {
			if e := json.Unmarshal(raw, &nested); e != nil {
				return nil, e
			}
		}
		if nested == nil {
			nested = map[string]json.RawMessage{}
		}
		nested[key], _ = json.Marshal(value)
		top[section], _ = json.Marshal(nested)
	}
	return json.Marshal(top)
}
func applyConfigurationOverrides(contents []byte) ([]byte, error) {
	b, _, e := configurationContentsWithOverrides(contents)
	return b, e
}
func ConfigurationFileDigest(contents []byte) string {
	s := sha256.Sum256(contents)
	return hex.EncodeToString(s[:])
}
func configurationContentsWithOverrides(contents []byte) ([]byte, ConfigurationOverrides, error) {
	var head struct {
		MetadataPath string `json:"metadata_path"`
	}
	if e := json.Unmarshal(contents, &head); e != nil {
		return nil, ConfigurationOverrides{}, e
	}
	if strings.TrimSpace(head.MetadataPath) == "" {
		return contents, ConfigurationOverrides{}, nil
	}
	v, e := ReadConfigurationOverrides(ConfigurationOverridePath(head.MetadataPath))
	if e != nil {
		return nil, ConfigurationOverrides{}, e
	}
	if v.TaskID == "" {
		return contents, ConfigurationOverrides{}, nil
	}
	b, e := mergeConfigurationValues(contents, v.Values)
	return b, v, e
}

// ValidateCandidate runs the real config decoder, defaults and validation with
// a candidate merged into this node's original config. It does not publish files.
func ValidateConfigurationCandidate(path string, changes map[string]int) (File, error) {
	if e := ValidateConfigurationChanges(changes); e != nil {
		return File{}, e
	}
	contents, e := os.ReadFile(path)
	if e != nil {
		return File{}, e
	}
	contents, e = applyConfigurationOverrides(contents)
	if e != nil {
		return File{}, e
	}
	contents, e = mergeConfigurationValues(contents, changes)
	if e != nil {
		return File{}, e
	}
	return loadConfigurationContents(contents)
}
func WriteConfigurationOverrides(path string, v ConfigurationOverrides) error {
	if v.SchemaVersion != 1 || v.TaskID == "" || v.Digest != ConfigurationDigest(v.Values) {
		return fmt.Errorf("invalid configuration override identity")
	}
	if len(v.Values) > 0 {
		if e := ValidateConfigurationChanges(v.Values); e != nil {
			return e
		}
	}
	if i, e := os.Lstat(path); e == nil && !i.Mode().IsRegular() {
		return fmt.Errorf("refuse nonregular configuration override")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	dir := filepath.Dir(path)
	i, e := os.Lstat(dir)
	if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("configuration state directory is unavailable")
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(dir, ".configuration-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(append(b, '\n')); e == nil {
		e = tmp.Sync()
	}
	ce := tmp.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func ConfigurationNumbers(file File) map[string]int {
	raw, _ := json.Marshal(file)
	result := map[string]int{}
	// Decode sections independently: top also contains scalar bootstrap fields.
	var all map[string]json.RawMessage
	_ = json.Unmarshal(raw, &all)
	for _, f := range EditableConfigurationFields() {
		s, k, _ := strings.Cut(f.Path, ".")
		var section map[string]json.RawMessage
		_ = json.Unmarshal(all[s], &section)
		var n int
		_ = json.Unmarshal(section[k], &n)
		result[f.Path] = n
	}
	return result
}
