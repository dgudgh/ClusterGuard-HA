package runtimecompat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestVerifiedMetadataRequirements(t *testing.T) {
	for _, scenario := range []string{"disabled-legacy-policy", "explicit-floor-no-clusters", "empty-legacy", "checksum-corruption", "policy-without-checksum", "unknown-floor", "symlink", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			metadata := filepath.Join(root, "state.json")
			cfg := filepath.Join(root, "config.json")
			payload := `{"clusters":{"c":{"failover_policy":{"automatic_enabled":false}}}}`
			if scenario == "explicit-floor-no-clusters" {
				payload = `{"clusters":{},"runtime_compatibility_floor":{"protocol":1,"instance_failover_policy":1}}`
			}
			if scenario == "unknown-floor" {
				payload = `{"clusters":{},"runtime_compatibility_floor":{"protocol":2,"instance_failover_policy":1}}`
			}
			hash := sha256.Sum256([]byte(payload))
			data := []byte(`{"clusterguard_state_revision":1,"clusterguard_state_digest":"` + hex.EncodeToString(hash[:]) + `",` + payload[1:])
			switch scenario {
			case "empty-legacy":
				data = []byte(`{"clusters":{}}`)
			case "checksum-corruption":
				data[len(data)-3] ^= 1
			case "policy-without-checksum":
				data = []byte(payload)
			}
			os.WriteFile(metadata, data, 0600)
			if scenario == "symlink" {
				alias := metadata + ".alias"
				os.Symlink(metadata, alias)
				metadata = alias
			}
			if scenario == "missing" {
				os.Remove(metadata)
			}
			config, _ := json.Marshal(map[string]string{"metadata_path": metadata})
			os.WriteFile(cfg, config, 0600)
			floor, e := ReadFromConfig(cfg)
			if scenario == "empty-legacy" {
				if e != nil || floor.Required() {
					t.Fatalf("empty metadata: %v %v", floor, e)
				}
			} else if scenario == "disabled-legacy-policy" || scenario == "explicit-floor-no-clusters" {
				if e != nil || floor != model.PolicyCompatibilityFloor() {
					t.Fatalf("lost floor: %v %v", floor, e)
				}
			} else if e == nil {
				t.Fatal("unverified metadata accepted", floor)
			}
		})
	}
}
