// Package runtimecompat reads compatibility requirements without importing
// execution, Agent, or coordination implementations into the privileged Helper.
package runtimecompat

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ReadFromConfig(configPath string) (model.RuntimeCompatibilityFloor, error) {
	regular := func(path string) ([]byte, error) {
		s, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		if !s.Mode().IsRegular() || s.Size() > 64<<20 {
			return nil, fmt.Errorf("runtime configuration/metadata must be a bounded regular file")
		}
		return os.ReadFile(path)
	}
	b, e := regular(configPath)
	if e != nil {
		return model.RuntimeCompatibilityFloor{}, e
	}
	var cfg struct {
		Path string `json:"metadata_path"`
	}
	if e = json.Unmarshal(b, &cfg); e != nil || !filepath.IsAbs(cfg.Path) {
		return model.RuntimeCompatibilityFloor{}, fmt.Errorf("trusted metadata path unavailable")
	}
	b, e = regular(cfg.Path)
	if e != nil {
		return model.RuntimeCompatibilityFloor{}, e
	}
	var data struct {
		Revision *uint64                              `json:"clusterguard_state_revision"`
		Digest   string                               `json:"clusterguard_state_digest"`
		Clusters map[model.ResourceID]json.RawMessage `json:"clusters"`
		Floor    *model.RuntimeCompatibilityFloor     `json:"runtime_compatibility_floor"`
	}
	if e = json.Unmarshal(b, &data); e != nil || data.Clusters == nil {
		return model.RuntimeCompatibilityFloor{}, fmt.Errorf("runtime metadata invalid: %v", e)
	}
	if data.Digest != "" {
		index := bytes.Index(b, []byte(`"clusters":`))
		if index < 0 {
			return model.RuntimeCompatibilityFloor{}, fmt.Errorf("runtime metadata payload unavailable")
		}
		previous := strings.TrimSpace(string(b[:index]))
		if previous == "" || (previous[len(previous)-1] != '{' && previous[len(previous)-1] != ',') {
			return model.RuntimeCompatibilityFloor{}, fmt.Errorf("invalid metadata payload boundary")
		}
		payload := append([]byte{'{'}, b[index:]...)
		hash := sha256.Sum256(payload)
		if hex.EncodeToString(hash[:]) != data.Digest {
			return model.RuntimeCompatibilityFloor{}, fmt.Errorf("runtime metadata checksum mismatch")
		}
	} else if data.Revision != nil {
		return model.RuntimeCompatibilityFloor{}, fmt.Errorf("runtime metadata digest missing")
	}
	floor := model.RuntimeCompatibilityFloor{}
	if data.Floor != nil {
		floor = *data.Floor
		if !floor.Required() || !floor.Valid() {
			return floor, fmt.Errorf("unknown persisted runtime compatibility floor")
		}
	}
	for _, raw := range data.Clusters {
		var c struct {
			Policy json.RawMessage `json:"failover_policy"`
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return floor, e
		}
		if len(c.Policy) > 0 && string(c.Policy) != "null" {
			if !json.Valid(c.Policy) {
				return floor, fmt.Errorf("invalid persisted policy")
			}
			floor = model.PolicyCompatibilityFloor()
		}
	}
	if floor.Required() && data.Digest == "" {
		return floor, fmt.Errorf("persisted policy requires verified metadata digest")
	}
	return floor, nil
}
