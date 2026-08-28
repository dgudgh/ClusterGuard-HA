package lifecycle

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

const maximumLifecycleOutputBytes = 2 << 20

type ProcessRunner interface {
	Run(context.Context, []byte, []string, string, ...string) ([]byte, error)
}

type OSLifecycleProcessRunner struct{}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	original := len(contents)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return original, nil
	}
	if len(contents) > remaining {
		contents = contents[:remaining]
		buffer.overflow = true
	}
	_, _ = buffer.buffer.Write(contents)
	return original, nil
}

func (OSLifecycleProcessRunner) Run(ctx context.Context, input []byte, environment []string, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = bytes.NewReader(input)
	command.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}, environment...)
	stdout := &boundedBuffer{limit: maximumLifecycleOutputBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.buffer.String())
		if detail == "" {
			return nil, fmt.Errorf("node lifecycle executor failed")
		}
		if stderr.overflow {
			detail += "\n[stderr truncated]"
		}
		return nil, fmt.Errorf("node lifecycle executor failed: %s", detail)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("node lifecycle output exceeded the allowed size")
	}
	return append([]byte{}, stdout.buffer.Bytes()...), nil
}

type ShellExecutor struct {
	script      string
	runner      ProcessRunner
	environment []string
}

type ShellEnvironment struct {
	PackageRepository       string
	KnownHostsFile          string
	IdentityFile            string
	JQBinary                string
	AdapterRuntimeHelper    string
	ControlJoinHelper       string
	ControlAPIIssuerCert    string
	ControlAPIIssuerKey     string
	ControlRaftIssuerCert   string
	ControlRaftIssuerKey    string
	ControlCertValidityDays int
	CloneHelper             string
	XtraBackupHelper        string
	PostgreSQLInstallHelper string
	PostgreSQLSyncHelper    string
	MySQLRootRemoteHost     string
}

type ShellExecutorOption func(*ShellExecutor) error

func WithShellEnvironment(configuration ShellEnvironment) ShellExecutorOption {
	return func(executor *ShellExecutor) error {
		values := []struct {
			name  string
			value string
		}{
			{"CG_PACKAGE_REPOSITORY", configuration.PackageRepository},
			{"CG_SSH_KNOWN_HOSTS", configuration.KnownHostsFile},
			{"CG_SSH_IDENTITY_FILE", configuration.IdentityFile},
			{"CG_JQ_BINARY", configuration.JQBinary},
			{"CG_ADAPTER_RUNTIME_HELPER", configuration.AdapterRuntimeHelper},
			{"CG_CONTROL_JOIN_HELPER", configuration.ControlJoinHelper},
			{"CG_CONTROL_API_ISSUER_CERT", configuration.ControlAPIIssuerCert},
			{"CG_CONTROL_API_ISSUER_KEY", configuration.ControlAPIIssuerKey},
			{"CG_CONTROL_RAFT_ISSUER_CERT", configuration.ControlRaftIssuerCert},
			{"CG_CONTROL_RAFT_ISSUER_KEY", configuration.ControlRaftIssuerKey},
			{"CG_MYSQL_CLONE_HELPER", configuration.CloneHelper},
			{"CG_MYSQL_XTRABACKUP_HELPER", configuration.XtraBackupHelper},
			{"CG_POSTGRESQL_INSTALL_HELPER", configuration.PostgreSQLInstallHelper},
			{"CG_POSTGRESQL_SYNC_HELPER", configuration.PostgreSQLSyncHelper},
		}
		for _, value := range values {
			path := strings.TrimSpace(value.value)
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				return fmt.Errorf("lifecycle executor path environment must be absolute")
			}
			executor.environment = append(executor.environment, value.name+"="+path)
		}
		if configuration.ControlCertValidityDays > 0 {
			if configuration.ControlCertValidityDays > 3650 {
				return fmt.Errorf("control certificate validity exceeds 3650 days")
			}
			executor.environment = append(executor.environment, "CG_CONTROL_CERT_VALIDITY_DAYS="+strconv.Itoa(configuration.ControlCertValidityDays))
		}
		remoteRootHost := strings.TrimSpace(configuration.MySQLRootRemoteHost)
		if remoteRootHost != "" {
			if !validMySQLAccountHost(remoteRootHost) {
				return fmt.Errorf("lifecycle MySQL root remote host is invalid")
			}
			executor.environment = append(executor.environment, "CG_MYSQL_ROOT_REMOTE_HOST="+remoteRootHost)
		}
		return nil
	}
}

func validMySQLAccountHost(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '.', '_', ':', '%', '-', '/':
			continue
		default:
			return false
		}
	}
	return true
}

func NewShellExecutor(script string, runner ProcessRunner, options ...ShellExecutorOption) (*ShellExecutor, error) {
	script = strings.TrimSpace(script)
	if !filepath.IsAbs(script) || runner == nil {
		return nil, fmt.Errorf("absolute lifecycle executor path and process runner are required")
	}
	executor := &ShellExecutor{script: script, runner: runner}
	for _, option := range options {
		if option != nil {
			if err := option(executor); err != nil {
				return nil, err
			}
		}
	}
	return executor, nil
}

type executorRecord struct {
	Type      string                   `json:"type"`
	Stage     Stage                    `json:"stage,omitempty"`
	Status    StageStatus              `json:"status,omitempty"`
	Message   string                   `json:"message,omitempty"`
	Verified  bool                     `json:"verified,omitempty"`
	Instances []model.DatabaseInstance `json:"instances,omitempty"`
	Checks    []model.Check            `json:"checks,omitempty"`
}

type executorInput struct {
	Request Request `json:"request"`
	Plan    Plan    `json:"plan"`
}

func (executor *ShellExecutor) Execute(ctx context.Context, request Request, plan Plan, secrets ExecutionSecrets, emit func(Event)) (ExecutionResult, error) {
	contents, err := json.Marshal(executorInput{Request: request, Plan: plan})
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("encode lifecycle request: %w", err)
	}
	contents = append(contents, '\n')
	environment := append([]string{}, executor.environment...)
	environment = append(environment,
		"CG_SSH_PASSWORD="+secrets.SSHPassword,
		"CG_MYSQL_ROOT_PASSWORD="+secrets.MySQLRootPassword,
		"CG_MYSQL_DISCOVERY_USERNAME="+secrets.MySQLDiscoveryUsername,
		"CG_MYSQL_DISCOVERY_PASSWORD="+secrets.MySQLDiscoveryPassword,
		"CG_MYSQL_OPERATION_USERNAME="+secrets.MySQLOperationUsername,
		"CG_MYSQL_OPERATION_PASSWORD="+secrets.MySQLOperationPassword,
		"CG_MYSQL_REPLICATION_USERNAME="+secrets.MySQLReplicationUsername,
		"CG_MYSQL_REPLICATION_PASSWORD="+secrets.ReplicationPassword,
		"CG_POSTGRESQL_ADMIN_PASSWORD="+secrets.PostgreSQLAdminPassword,
		"CG_POSTGRESQL_REPLICATION_PASSWORD="+secrets.PostgreSQLReplicationPassword,
	)
	output, err := executor.runner.Run(ctx, contents, environment, executor.script, "execute")
	if err != nil {
		detail := redactLifecycleMessage(err.Error(), secrets)
		detail = strings.TrimSpace(strings.TrimPrefix(detail, "node lifecycle executor failed:"))
		if detail == "" || detail == "node lifecycle executor failed" {
			return ExecutionResult{}, fmt.Errorf("node lifecycle executor failed")
		}
		return ExecutionResult{}, fmt.Errorf("node lifecycle executor failed: %s", detail)
	}
	return decodeExecutorOutput(output, emit)
}

func decodeExecutorOutput(output []byte, emit func(Event)) (ExecutionResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var result ExecutionResult
	resultFound := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var record executorRecord
		if err := decoder.Decode(&record); err != nil {
			return ExecutionResult{}, fmt.Errorf("decode lifecycle executor record")
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return ExecutionResult{}, fmt.Errorf("decode lifecycle executor record")
		}
		switch record.Type {
		case "event":
			if !record.Stage.Valid() || !record.Status.Valid() || resultFound {
				return ExecutionResult{}, fmt.Errorf("invalid lifecycle stage record")
			}
			if emit != nil {
				emit(Event{Stage: record.Stage, Status: record.Status, Message: record.Message})
			}
		case "result":
			if resultFound {
				return ExecutionResult{}, fmt.Errorf("multiple lifecycle results returned")
			}
			result = ExecutionResult{Verified: record.Verified, Instances: record.Instances, Checks: record.Checks, Message: record.Message}
			resultFound = true
		default:
			return ExecutionResult{}, fmt.Errorf("unsupported lifecycle executor record")
		}
	}
	if err := scanner.Err(); err != nil {
		return ExecutionResult{}, fmt.Errorf("read lifecycle executor output")
	}
	if !resultFound {
		return ExecutionResult{}, fmt.Errorf("lifecycle executor did not return a result")
	}
	if !result.Verified {
		return result, fmt.Errorf("lifecycle executor verification failed")
	}
	return result, nil
}
