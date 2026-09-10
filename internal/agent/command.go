package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"clusterguard.io/ha/pkg/redact"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type OSCommandRunner struct{}

// Recovery evidence must not depend on scheduling between stdout and stderr.
// In particular, pg_waldump can report its valid zero tail before stdout flushes.
type orderedOutputRunner struct{ CommandRunner }

func (runner orderedOutputRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	if ordered, ok := runner.CommandRunner.(interface {
		RunOrdered(context.Context, string, ...string) ([]byte, error)
	}); ok {
		return ordered.RunOrdered(ctx, name, arguments...)
	}
	return runner.CommandRunner.Run(ctx, name, arguments...)
}

func (OSCommandRunner) RunOrdered(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = []string{"PATH=/usr/local/mysql/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	output := append(stdout.Bytes(), stderr.Bytes()...)
	if err != nil {
		return output, fmt.Errorf("local command failed: %s", redact.Bounded(strings.TrimSpace(string(output)), 512))
	}
	return output, nil
}

func (OSCommandRunner) Run(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = []string{"PATH=/usr/local/mysql/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	output, err := command.CombinedOutput()
	if err != nil {
		message := redact.Bounded(strings.TrimSpace(string(output)), 512)
		return output, fmt.Errorf("local command failed: %s", message)
	}
	return output, nil
}
