package postgresql

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
)

type Row map[string]string

type SQLRunner interface {
	Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)
}

type SQLExecutor interface {
	Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error
}

type CLIQueryRunner struct {
	Binary string
}

type commandSpec struct {
	Binary      string
	Arguments   []string
	Environment []string
}

type QueryError struct {
	Output string
	Err    error
}

func (queryError *QueryError) Error() string {
	if queryError.Output == "" {
		return "postgresql query failed"
	}
	return "postgresql query failed: " + queryError.Output
}

func (queryError *QueryError) Unwrap() error { return queryError.Err }

func (runner CLIQueryRunner) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]Row, error) {
	spec, err := queryCommandSpec(ctx, runner.Binary, endpoint, credentials, query)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, spec.Binary, spec.Arguments...)
	command.Env = spec.Environment
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, &QueryError{Output: strings.TrimSpace(string(output)), Err: err}
	}
	return parseJSONRows(output)
}

func (runner CLIQueryRunner) Exec(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) error {
	spec, err := postgresqlCommandSpec(ctx, runner.Binary, endpoint, credentials, query, "clusterguard-control")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, spec.Binary, spec.Arguments...)
	command.Env = spec.Environment
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		return &QueryError{Output: strings.TrimSpace(string(output)), Err: err}
	}
	return nil
}

func queryCommandSpec(ctx context.Context, binary string, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) (commandSpec, error) {
	return postgresqlCommandSpec(ctx, binary, endpoint, credentials, query, "clusterguard-discovery")
}

func postgresqlCommandSpec(ctx context.Context, binary string, endpoint adapter.Endpoint, credentials adapter.Credentials, query, applicationName string) (commandSpec, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = "psql"
	}
	host := strings.TrimSpace(endpoint.Hostname)
	if host == "" {
		host = strings.TrimSpace(endpoint.IPAddress)
	}
	username := strings.TrimSpace(credentials.Username)
	if host == "" || endpoint.Port <= 0 || username == "" {
		return commandSpec{}, fmt.Errorf("database endpoint, port, and username are required")
	}
	database := strings.TrimSpace(credentials.Database)
	if database == "" {
		database = "postgres"
	}
	arguments := []string{
		"--no-password",
		"--no-psqlrc",
		"--quiet",
		"--tuples-only",
		"--no-align",
		"--host", host,
		"--port", strconv.Itoa(endpoint.Port),
		"--username", username,
		"--dbname", database,
		"--command", query,
	}
	environment := append(os.Environ(),
		"PGPASSWORD="+credentials.Password,
		"PGDATABASE="+database,
		"PGAPPNAME="+applicationName,
		"PGCONNECT_TIMEOUT="+strconv.Itoa(postgresqlConnectTimeoutSeconds(ctx)),
		"PGOPTIONS=",
	)
	return commandSpec{Binary: binary, Arguments: arguments, Environment: environment}, nil
}

func postgresqlConnectTimeoutSeconds(ctx context.Context) int {
	const defaultTimeout = 5
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return defaultTimeout
	}
	remaining := time.Until(deadline) - time.Second
	if remaining < time.Second {
		return 1
	}
	seconds := int(remaining / time.Second)
	if seconds < defaultTimeout {
		return seconds
	}
	return defaultTimeout
}

func parseJSONRows(output []byte) ([]Row, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	rows := make([]Row, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("parse PostgreSQL JSON row %d: %w", lineNumber, err)
		}
		if raw == nil {
			return nil, fmt.Errorf("parse PostgreSQL JSON row %d: object is required", lineNumber)
		}
		row := make(Row, len(raw))
		for key, value := range raw {
			normalized, err := normalizeJSONScalar(value)
			if err != nil {
				return nil, fmt.Errorf("parse PostgreSQL JSON row %d column %q: %w", lineNumber, key, err)
			}
			row[key] = normalized
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan PostgreSQL JSON rows: %w", err)
	}
	return rows, nil
}

func normalizeJSONScalar(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var scalar interface{}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&scalar); err != nil {
		return "", err
	}
	switch scalar.(type) {
	case bool, json.Number:
		return string(trimmed), nil
	default:
		return "", fmt.Errorf("scalar value is required")
	}
}
