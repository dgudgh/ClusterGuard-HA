package endpoint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
)

type processRunnerStub struct {
	input []byte
	name  string
	args  []string
}

func (runner *processRunnerStub) Run(_ context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	runner.input = append([]byte{}, input...)
	runner.name = name
	runner.args = append([]string{}, arguments...)
	return json.Marshal(agent.Response{Status: agent.StatusOK, Message: "VIP status collected"})
}

func TestSSHAgentTransportUsesPinnedNonInteractiveConnection(t *testing.T) {
	runner := &processRunnerStub{}
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{
		SSHBinary: "/usr/bin/ssh", User: "cg-agent", IdentityFile: "/etc/clusterguard/agent_ed25519",
		KnownHostsFile: "/etc/clusterguard/agent_known_hosts", AgentBinary: "/usr/local/bin/clusterguard-agent",
		AgentConfigPath: "/etc/clusterguard/agent.json",
	}, runner)
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	request := agent.Request{Command: agent.CommandVIPStatus, ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(), PlanDigest: "sha256:test", ExpiresAt: time.Now().Add(time.Minute), Signature: "signature"}
	response, err := transport.Send(context.Background(), model.DatabaseInstance{IPAddress: "192.0.2.10"}, request)
	if err != nil || response.Status != agent.StatusOK {
		t.Fatalf("send response=%+v err=%v", response, err)
	}
	joined := strings.Join(runner.args, " ")
	for _, required := range []string{
		"-o BatchMode=yes", "-o StrictHostKeyChecking=yes", "-o UserKnownHostsFile=/etc/clusterguard/agent_known_hosts",
		"-i /etc/clusterguard/agent_ed25519", "cg-agent@192.0.2.10", "/usr/local/bin/clusterguard-agent", "--config /etc/clusterguard/agent.json",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("SSH args missing %q: %s", required, joined)
		}
	}
	if runner.name != "/usr/bin/ssh" || !json.Valid(runner.input) || strings.Contains(joined, "password") {
		t.Fatalf("unsafe transport name=%q args=%s input=%s", runner.name, joined, runner.input)
	}
}

func TestSSHAgentTransportRejectsInstanceWithoutAddress(t *testing.T) {
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{User: "cg-agent", IdentityFile: "/key", KnownHostsFile: "/known"}, &processRunnerStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Send(context.Background(), model.DatabaseInstance{}, agent.Request{}); err == nil {
		t.Fatal("addressless instance must be rejected")
	}
}
