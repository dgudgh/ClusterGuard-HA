package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresOneCompleteJSONDocument(t *testing.T) {
	t.Setenv("CG_AGENT_INPUT_TEST_SECRET", "fixture-secret")
	const configuration = `{"shared_secret_env":"CG_AGENT_INPUT_TEST_SECRET","clusters":[{"cluster_id":"11111111-1111-4111-8111-111111111111","instance_id":"22222222-2222-4222-8222-222222222222"}]}`
	for _, test := range []struct {
		name, suffix string
		valid        bool
	}{
		{"eof", "", true}, {"whitespace", " \t\r\n", true},
		{"second-object", `{}`, false}, {"second-null", ` null`, false},
		{"garbage", ` secret-must-not-be-echoed`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			if err := os.WriteFile(path, []byte(configuration+test.suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if test.valid {
				if err != nil || len(loaded.Clusters) != 1 {
					t.Fatalf("valid document rejected: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("trailing content was accepted")
				}
				if strings.Contains(err.Error(), "secret-must-not-be-echoed") {
					t.Fatal("error exposed configuration contents")
				}
			}
		})
	}
}
