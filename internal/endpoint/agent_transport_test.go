package endpoint

import (
	"context"
	"encoding/json"
	"errors"
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

type blockingProcessRunner struct{}

func (blockingProcessRunner) Run(ctx context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type contextCaptureRunner struct {
	deadline time.Time
	ok       bool
}

func (runner *contextCaptureRunner) Run(ctx context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
	runner.deadline, runner.ok = ctx.Deadline()
	return json.Marshal(agent.Response{Status: agent.StatusOK, ClusterID: model.ResourceID("11111111-1111-4111-8111-111111111111"), InstanceID: model.ResourceID("22222222-2222-4222-8222-222222222222")})
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
		"-o ConnectTimeout=5", "-o ConnectionAttempts=1", "-o ServerAliveInterval=5", "-o ServerAliveCountMax=6",
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

func TestSSHAgentTransportBoundsRemoteCommandDuration(t *testing.T) {
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{
		User: "root", IdentityFile: "/key", KnownHostsFile: "/known", CommandTimeout: 20 * time.Millisecond,
	}, blockingProcessRunner{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = transport.Send(context.Background(), model.DatabaseInstance{IPAddress: "192.0.2.10"}, agent.Request{})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded send err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("agent command exceeded timeout: %s", elapsed)
	}
}

func TestSSHAgentTransportUsesLongTimeoutForPostgreSQLMutations(t *testing.T) {
	runner := &contextCaptureRunner{}
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{
		User: "root", IdentityFile: "/key", KnownHostsFile: "/known",
		CommandTimeout:  5 * time.Second,
		MutationTimeout: 12 * time.Minute,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	clusterID := model.ResourceID("11111111-1111-4111-8111-111111111111")
	instanceID := model.ResourceID("22222222-2222-4222-8222-222222222222")
	started := time.Now()
	_, err = transport.Send(context.Background(), model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: instanceID}, ClusterID: clusterID, IPAddress: "192.0.2.10"}, agent.Request{
		Command: agent.CommandPostgreSQLRepoint, Engine: model.EnginePostgreSQL,
		ClusterID: clusterID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.ok {
		t.Fatal("mutation transport context has no deadline")
	}
	remaining := time.Until(runner.deadline)
	if remaining < 11*time.Minute || runner.deadline.Before(started.Add(11*time.Minute)) {
		t.Fatalf("PostgreSQL mutation used short transport timeout, remaining=%s deadline=%s", remaining, runner.deadline)
	}
}

func TestSSHAgentTransportAllowsOracleBrokerStatusToConverge(t *testing.T) {
	runner := &contextCaptureRunner{}
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{
		User: "root", IdentityFile: "/key", KnownHostsFile: "/known",
		CommandTimeout:  5 * time.Second,
		MutationTimeout: 12 * time.Minute,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	clusterID := model.ResourceID("11111111-1111-4111-8111-111111111111")
	instanceID := model.ResourceID("22222222-2222-4222-8222-222222222222")
	started := time.Now()
	_, err = transport.Send(context.Background(), model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: instanceID},
		ClusterID:    clusterID,
		IPAddress:    "192.0.2.10",
	}, agent.Request{
		Command:   agent.CommandOracleBrokerStatus,
		Engine:    model.EngineOracle,
		ClusterID: clusterID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.ok {
		t.Fatal("Oracle Broker status transport context has no deadline")
	}
	remaining := time.Until(runner.deadline)
	if remaining < 89*time.Second || runner.deadline.Before(started.Add(89*time.Second)) {
		t.Fatalf("Oracle Broker status used short transport timeout, remaining=%s deadline=%s", remaining, runner.deadline)
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

func TestSSHAgentTransportDefaultsToProtectedEnvironmentWrapper(t *testing.T) {
	runner := &processRunnerStub{}
	transport, err := NewSSHAgentTransport(SSHAgentTransportConfig{
		User: "root", IdentityFile: "/key", KnownHostsFile: "/known",
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{Command: agent.CommandVIPStatus, ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(), PlanDigest: "sha256:test", ExpiresAt: time.Now().Add(time.Minute), Signature: "signature"}
	if _, err := transport.Send(context.Background(), model.DatabaseInstance{IPAddress: "192.0.2.10"}, request); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.args, " ")
	if !strings.Contains(joined, "/usr/local/libexec/clusterguard-agent-stdio --config /etc/clusterguard/agent.json") {
		t.Fatalf("default remote command bypasses protected agent environment: %s", joined)
	}
}
