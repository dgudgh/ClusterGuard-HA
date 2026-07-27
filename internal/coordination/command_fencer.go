package coordination

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const maximumFenceCommandOutputBytes = 64 << 10

var errFenceCommandOutputTooLarge = errors.New("external fencer output exceeds maximum size")

type FenceCommandRunner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}

type boundedCommandOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (output *boundedCommandOutput) Write(contents []byte) (int, error) {
	originalLength := len(contents)
	if originalLength > output.remaining {
		if output.remaining > 0 {
			_, _ = output.buffer.Write(contents[:output.remaining])
			output.remaining = 0
		}
		return originalLength, errFenceCommandOutputTooLarge
	}
	output.remaining -= originalLength
	_, _ = output.buffer.Write(contents)
	return originalLength, nil
}

type OSFenceCommandRunner struct{}

func fenceCommandEnvironment() []string {
	environment := make([]string, 0, 8)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		upperName := strings.ToUpper(strings.TrimSpace(name))
		if strings.HasPrefix(upperName, "CG_FENCER_") || strings.HasPrefix(upperName, "LC_") {
			environment = append(environment, entry)
			continue
		}
		switch upperName {
		case "PATH", "LANG", "TZ":
			environment = append(environment, entry)
		}
	}
	return environment
}

func (OSFenceCommandRunner) Run(ctx context.Context, executable string, arguments []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdin = bytes.NewReader(input)
	command.Env = fenceCommandEnvironment()
	stdout := &boundedCommandOutput{remaining: maximumFenceCommandOutputBytes}
	stderr := &boundedCommandOutput{remaining: maximumFenceCommandOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	return append([]byte{}, stdout.buffer.Bytes()...), nil
}

type CommandFencer struct {
	executable string
	timeout    time.Duration
	runner     FenceCommandRunner
}

func NewCommandFencer(executable string, timeout time.Duration, runner FenceCommandRunner) (*CommandFencer, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" || !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("external fencer executable must be an absolute path")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if runner == nil {
		info, err := os.Stat(executable)
		if err != nil {
			return nil, fmt.Errorf("inspect external fencer executable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, fmt.Errorf("external fencer executable is not an executable regular file")
		}
		runner = OSFenceCommandRunner{}
	}
	return &CommandFencer{executable: executable, timeout: timeout, runner: runner}, nil
}

type commandFenceResponse struct {
	Status  string `json:"status"`
	Fenced  bool   `json:"fenced"`
	Message string `json:"message,omitempty"`
}

func validateExternalFenceRequest(request ExternalFenceRequest) error {
	if !model.ValidResourceID(request.ClusterID) || !model.ValidResourceID(request.OperationID) || !model.ValidResourceID(request.Instance.ResourceID) {
		return fmt.Errorf("external fencing requires valid cluster, operation, and instance identities")
	}
	if request.LeaseID != "" && !model.ValidResourceID(request.LeaseID) {
		return fmt.Errorf("external fencing lease identity is invalid")
	}
	return nil
}

func (fencer *CommandFencer) run(ctx context.Context, action string, request ExternalFenceRequest) (commandFenceResponse, error) {
	if fencer == nil || fencer.runner == nil || fencer.executable == "" {
		return commandFenceResponse{}, fmt.Errorf("external command fencer is not configured")
	}
	if err := validateExternalFenceRequest(request); err != nil {
		return commandFenceResponse{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return commandFenceResponse{}, fmt.Errorf("encode external fence request: %w", err)
	}
	commandContext, cancel := context.WithTimeout(ctx, fencer.timeout)
	defer cancel()
	output, err := fencer.runner.Run(commandContext, fencer.executable, []string{action}, payload)
	if err != nil {
		return commandFenceResponse{}, fmt.Errorf("external fencer %s failed: %w", action, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	response := commandFenceResponse{}
	if err := decoder.Decode(&response); err != nil {
		return commandFenceResponse{}, fmt.Errorf("decode external fencer %s response: %w", action, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return commandFenceResponse{}, fmt.Errorf("external fencer %s returned multiple JSON values", action)
	}
	if !strings.EqualFold(strings.TrimSpace(response.Status), "ok") {
		return commandFenceResponse{}, fmt.Errorf("external fencer %s blocked the request", action)
	}
	return response, nil
}

func (fencer *CommandFencer) Fence(ctx context.Context, request ExternalFenceRequest) error {
	response, err := fencer.run(ctx, "fence", request)
	if err != nil {
		return err
	}
	if !response.Fenced {
		return fmt.Errorf("external fencer did not confirm isolation")
	}
	return nil
}

func (fencer *CommandFencer) Status(ctx context.Context, request ExternalFenceRequest) (bool, error) {
	response, err := fencer.run(ctx, "status", request)
	if err != nil {
		return false, err
	}
	return response.Fenced, nil
}
