package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the real CLI entry point in a child process; no valid mutation is signed.
func TestAgentStdinChild(t *testing.T) {
	if os.Getenv("CG_AGENT_STDIN_CHILD") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("clusterguard-agent", flag.ExitOnError)
	os.Args = []string{"clusterguard-agent", "--config", os.Getenv("CG_AGENT_STDIN_CONFIG")}
	main()
	os.Exit(0)
}

func TestAgentStdinRejectsTrailingContentBeforeService(t *testing.T) {
	root := t.TempDir()
	config := map[string]any{
		"shared_secret_env": "CG_AGENT_STDIN_SECRET", "role_state_directory": filepath.Join(root, "roles"),
		"mutation_state_directory": filepath.Join(root, "mutations"),
		"clusters":                 []map[string]string{{"cluster_id": "11111111-1111-4111-8111-111111111111", "instance_id": "22222222-2222-4222-8222-222222222222"}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "agent.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"single", `{}`, true}, {"whitespace", "{} \t\r\n", true},
		{"second-object", `{} {}`, false}, {"second-null", `{} null`, false},
		{"garbage", `{} secret-must-not-be-echoed`, false}, {"unknown-field", `{"unknown":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentStdinChild$")
			command.Env = append(os.Environ(), "CG_AGENT_STDIN_CHILD=1", "CG_AGENT_STDIN_CONFIG="+configPath, "CG_AGENT_STDIN_SECRET=fixture-secret")
			command.Stdin = strings.NewReader(test.body)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if test.valid {
				if err != nil || !json.Valid(stdout.Bytes()) {
					t.Fatalf("valid framing did not reach signature validation: err=%v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
				}
				return
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || stdout.Len() != 0 || strings.TrimSpace(stderr.String()) != "invalid agent request" {
				t.Fatalf("invalid framing reached service or wrong rejection: err=%v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
			}
		})
	}
}
