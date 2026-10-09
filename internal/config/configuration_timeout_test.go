package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationDistributionTimeoutIsExplicitAndValidated(t *testing.T) {
	for _, tt := range []struct {
		seconds, want int
		valid         bool
	}{{0, 180, true}, {30, 30, true}, {900, 900, true}, {3600, 3600, true}, {-1, 0, false}, {29, 0, false}, {3601, 0, false}} {
		t.Run(fmt.Sprint(tt.seconds), func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			b := fmt.Sprintf(`{"metadata_path":%q,"configuration_distribution":{"step_timeout_seconds":%d}}`, filepath.Join(dir, "metadata.json"), tt.seconds)
			if err := os.WriteFile(p, []byte(b), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if (err == nil) != tt.valid {
				t.Fatalf("Load: %v", err)
			}
			if tt.valid && c.ConfigurationDistribution.StepTimeoutSeconds != tt.want {
				t.Fatal("wrong effective timeout")
			}
		})
	}
}
