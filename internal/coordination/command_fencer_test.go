package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type fenceCommandRunnerStub struct {
	action  string
	payload []byte
	output  []byte
	err     error
}

func (runner *fenceCommandRunnerStub) Run(_ context.Context, _ string, arguments []string, input []byte) ([]byte, error) {
	if len(arguments) == 1 {
		runner.action = arguments[0]
	}
	runner.payload = append([]byte{}, input...)
	return append([]byte{}, runner.output...), runner.err
}

func TestCommandFencerUsesStructuredContractForFenceAndStatus(t *testing.T) {
	runner := &fenceCommandRunnerStub{output: []byte(`{"status":"ok","fenced":true}`)}
	fencer, err := NewCommandFencer(filepath.Join(t.TempDir(), "fencer"), 10*time.Second, runner)
	if err != nil {
		t.Fatalf("new command fencer: %v", err)
	}
	request := ExternalFenceRequest{
		ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(), LeaseID: model.NewResourceID(),
		Instance: model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306},
	}
	if err := fencer.Fence(context.Background(), request); err != nil {
		t.Fatalf("fence through command provider: %v", err)
	}
	if runner.action != "fence" {
		t.Fatalf("fence command action=%q", runner.action)
	}
	decoded := ExternalFenceRequest{}
	if err := json.Unmarshal(runner.payload, &decoded); err != nil || decoded.Instance.ResourceID != request.Instance.ResourceID || decoded.LeaseID != request.LeaseID {
		t.Fatalf("fence payload=%s decoded=%+v err=%v", runner.payload, decoded, err)
	}
	fenced, err := fencer.Status(context.Background(), request)
	if err != nil || !fenced || runner.action != "status" {
		t.Fatalf("fence status=%t action=%q err=%v", fenced, runner.action, err)
	}
}

func TestCommandFencerRejectsInvalidProviderResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		output []byte
		err    error
	}{
		{name: "process failure", err: errors.New("provider failed")},
		{name: "invalid JSON", output: []byte("not-json")},
		{name: "provider blocked", output: []byte(`{"status":"blocked","fenced":false}`)},
		{name: "trailing response", output: []byte(`{"status":"ok","fenced":true}{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fenceCommandRunnerStub{output: test.output, err: test.err}
			fencer, err := NewCommandFencer(filepath.Join(t.TempDir(), "fencer"), time.Second, runner)
			if err != nil {
				t.Fatalf("new command fencer: %v", err)
			}
			request := ExternalFenceRequest{
				ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(),
				Instance: model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}},
			}
			if _, err := fencer.Status(context.Background(), request); err == nil {
				t.Fatal("unsafe external fence response was accepted")
			}
		})
	}
}

func TestCommandFencerRequiresAbsoluteExecutable(t *testing.T) {
	if _, err := NewCommandFencer("relative-fencer", time.Second, &fenceCommandRunnerStub{}); err == nil {
		t.Fatal("relative external fencer executable was accepted")
	}
}

func TestCommandFencerPreflightsOSExecutable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-fencer")
	if _, err := NewCommandFencer(missing, time.Second, nil); err == nil {
		t.Fatal("missing external fencer executable was accepted")
	}

	notExecutable := filepath.Join(t.TempDir(), "fencer")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatalf("write non-executable fencer: %v", err)
	}
	if _, err := NewCommandFencer(notExecutable, time.Second, nil); err == nil {
		t.Fatal("non-executable external fencer was accepted")
	}
	if err := os.Chmod(notExecutable, 0700); err != nil {
		t.Fatalf("make fencer executable: %v", err)
	}
	if _, err := NewCommandFencer(notExecutable, time.Second, nil); err != nil {
		t.Fatalf("executable external fencer was rejected: %v", err)
	}
}

func TestFenceCommandEnvironmentDoesNotExposeControlPlaneSecrets(t *testing.T) {
	t.Setenv("CG_MYSQL_OPERATION_PASSWORD", "database-secret")
	t.Setenv("CG_CONTROL_TOKEN", "control-secret")
	t.Setenv("CG_FENCER_API_TOKEN", "fencer-secret")
	environment := strings.Join(fenceCommandEnvironment(), "\n")
	if strings.Contains(environment, "database-secret") || strings.Contains(environment, "control-secret") {
		t.Fatalf("fencer inherited control-plane secrets: %s", environment)
	}
	if !strings.Contains(environment, "CG_FENCER_API_TOKEN=fencer-secret") {
		t.Fatalf("dedicated fencer credential missing from environment: %s", environment)
	}
}
