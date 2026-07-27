package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/pkg/model"
)

const oracleBrokerQueryTimeout = 90 * time.Second

type ProcessRunner interface {
	Run(context.Context, []byte, string, ...string) ([]byte, error)
}

type OSProcessRunner struct{}

func (OSProcessRunner) Run(ctx context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = bytes.NewReader(input)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("agent transport command failed: %w", err)
	}
	return output, nil
}

type SSHAgentTransportConfig struct {
	SSHBinary       string
	User            string
	IdentityFile    string
	KnownHostsFile  string
	AgentBinary     string
	AgentConfigPath string
	CommandTimeout  time.Duration
	MutationTimeout time.Duration
}

type SSHAgentTransport struct {
	configuration SSHAgentTransportConfig
	runner        ProcessRunner
}

func NewSSHAgentTransport(configuration SSHAgentTransportConfig, runner ProcessRunner) (*SSHAgentTransport, error) {
	configuration.SSHBinary = strings.TrimSpace(configuration.SSHBinary)
	configuration.User = strings.TrimSpace(configuration.User)
	configuration.IdentityFile = strings.TrimSpace(configuration.IdentityFile)
	configuration.KnownHostsFile = strings.TrimSpace(configuration.KnownHostsFile)
	configuration.AgentBinary = strings.TrimSpace(configuration.AgentBinary)
	configuration.AgentConfigPath = strings.TrimSpace(configuration.AgentConfigPath)
	if configuration.SSHBinary == "" {
		configuration.SSHBinary = "/usr/bin/ssh"
	}
	if configuration.AgentBinary == "" {
		configuration.AgentBinary = "/usr/local/libexec/clusterguard-agent-stdio"
	}
	if configuration.AgentConfigPath == "" {
		configuration.AgentConfigPath = "/etc/clusterguard/agent.json"
	}
	if configuration.CommandTimeout <= 0 {
		configuration.CommandTimeout = 5 * time.Second
	}
	if configuration.MutationTimeout <= 0 {
		configuration.MutationTimeout = 30 * time.Minute
	}
	if configuration.User == "" || configuration.IdentityFile == "" || configuration.KnownHostsFile == "" || runner == nil {
		return nil, fmt.Errorf("SSH agent transport configuration is incomplete")
	}
	return &SSHAgentTransport{configuration: configuration, runner: runner}, nil
}

func (transport *SSHAgentTransport) Send(ctx context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	timeout := transport.configuration.CommandTimeout
	switch {
	case postgresqlAgentMutationCommand(request.Command) || request.Command == agent.CommandOracleBrokerSwitchover:
		timeout = transport.configuration.MutationTimeout
	case request.Command == agent.CommandOracleBrokerDiscover || request.Command == agent.CommandOracleBrokerStatus:
		if timeout < oracleBrokerQueryTimeout {
			timeout = oracleBrokerQueryTimeout
		}
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host := strings.TrimSpace(instance.IPAddress)
	if host == "" {
		host = strings.TrimSpace(instance.Hostname)
	}
	if host == "" {
		return agent.Response{}, fmt.Errorf("agent instance has no network address")
	}
	contents, err := json.Marshal(request)
	if err != nil {
		return agent.Response{}, fmt.Errorf("encode agent request: %w", err)
	}
	contents = append(contents, '\n')
	arguments := []string{
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ConnectTimeout=5",
		"-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=6",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + transport.configuration.KnownHostsFile,
		"-i", transport.configuration.IdentityFile,
		transport.configuration.User + "@" + host,
		transport.configuration.AgentBinary, "--config", transport.configuration.AgentConfigPath,
	}
	output, err := transport.runner.Run(commandContext, contents, transport.configuration.SSHBinary, arguments...)
	if err != nil {
		return agent.Response{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var response agent.Response
	if err := decoder.Decode(&response); err != nil {
		return agent.Response{}, fmt.Errorf("decode agent response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return agent.Response{}, fmt.Errorf("agent response contains multiple JSON values")
	}
	return response, nil
}

func postgresqlAgentMutationCommand(command string) bool {
	switch command {
	case agent.CommandPostgreSQLStop, agent.CommandPostgreSQLStart, agent.CommandPostgreSQLPromote,
		agent.CommandPostgreSQLRepoint, agent.CommandPostgreSQLRewind, agent.CommandPostgreSQLBaseBackup:
		return true
	default:
		return false
	}
}
