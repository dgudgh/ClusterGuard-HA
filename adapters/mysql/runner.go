package mysql

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
)

type Row map[string]string

type SQLRunner interface {
	Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error)
}

type SQLExecutor interface {
	Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error
}

var ErrStatementUnsupported = errors.New("mysql statement unsupported")

var mysqlErrorCodePattern = regexp.MustCompile(`(?m)ERROR\s+([0-9]+)\b`)

type QueryError struct {
	Code   int
	Output string
	Err    error
}

func (queryError *QueryError) Error() string {
	if queryError.Output != "" {
		return fmt.Sprintf("mysql query failed: %s", queryError.Output)
	}
	return fmt.Sprintf("mysql query failed: %v", queryError.Err)
}

func (queryError *QueryError) Unwrap() error { return queryError.Err }

func (queryError *QueryError) Is(target error) bool {
	return target == ErrStatementUnsupported && queryError.Code == 1064
}

type CLIQueryRunner struct {
	Binary string
}

func (runner CLIQueryRunner) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]Row, error) {
	output, err := runner.execute(ctx, endpoint, credentials, query)
	if err != nil {
		return nil, err
	}
	return parseTSV(output)
}

func (runner CLIQueryRunner) Exec(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, statement string) error {
	_, err := runner.execute(ctx, endpoint, credentials, statement)
	return err
}

func (runner CLIQueryRunner) execute(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) ([]byte, error) {
	binary := runner.Binary
	if binary == "" {
		binary = "mysql"
	}
	host := endpoint.Hostname
	if host == "" {
		host = endpoint.IPAddress
	}
	if host == "" || endpoint.Port <= 0 || credentials.Username == "" {
		return nil, fmt.Errorf("database endpoint, port, and username are required")
	}
	command := exec.CommandContext(ctx, binary, "--batch", "--raw", "--protocol=TCP", "--connect-timeout=5", "-h", host, "-P", strconv.Itoa(endpoint.Port), "-u", credentials.Username, "-e", query)
	command.Env = append(os.Environ(), "MYSQL_PWD="+credentials.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, &QueryError{
			Code:   mysqlErrorCode(output),
			Output: strings.TrimSpace(string(output)),
			Err:    err,
		}
	}
	return output, nil
}

func mysqlErrorCode(output []byte) int {
	match := mysqlErrorCodePattern.FindSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	code, _ := strconv.Atoi(string(match[1]))
	return code
}

func parseTSV(output []byte) ([]Row, error) {
	reader := csv.NewReader(bytes.NewReader(output))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse MySQL headers: %w", err)
	}
	for index := range headers {
		headers[index] = strings.TrimSpace(headers[index])
		if headers[index] == "" {
			return nil, fmt.Errorf("parse MySQL headers: column %d is empty", index+1)
		}
	}

	var rows []Row
	for {
		values, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("parse MySQL row: %w", readErr)
		}
		if len(values) != len(headers) {
			return nil, fmt.Errorf("parse MySQL row: got %d columns, want %d", len(values), len(headers))
		}
		row := make(Row, len(headers))
		for index, header := range headers {
			row[header] = values[index]
		}
		rows = append(rows, row)
	}
	return rows, nil
}
