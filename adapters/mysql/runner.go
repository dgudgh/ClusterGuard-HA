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

var ErrStatementUnsupported = errors.New("mysql statement unsupported")

var mysqlErrorCodePattern = regexp.MustCompile(`(?m)ERROR\s+([0-9]+)\b`)

type QueryError struct {
	Code   int
	Output string
	Err    error
}

func (queryError *QueryError) Error() string {
	if queryError.Code > 0 {
		return fmt.Sprintf("mysql query failed with error code %d", queryError.Code)
	}
	return "mysql query failed"
}

func (queryError *QueryError) Unwrap() error { return queryError.Err }

func (queryError *QueryError) Is(target error) bool {
	return target == ErrStatementUnsupported && queryError.Code == 1064
}

type CLIQueryRunner struct {
	Binary       string
	QueryTimeout time.Duration
}

const defaultMySQLQueryTimeout = 30 * time.Second

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
	if _, bounded := ctx.Deadline(); !bounded {
		timeout := runner.QueryTimeout
		if timeout <= 0 {
			timeout = defaultMySQLQueryTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	binary := runner.Binary
	if binary == "" {
		binary = "mysql"
	}
	// The resource registry owns the database endpoint. Prefer its concrete IP so
	// hostname changes or incomplete DNS cannot turn a healthy registered node
	// into an unreachable one. Hostname remains a fallback for DNS-only assets.
	host := endpoint.IPAddress
	if host == "" {
		host = endpoint.Hostname
	}
	if host == "" || endpoint.Port <= 0 || credentials.Username == "" {
		return nil, fmt.Errorf("database endpoint, port, and username are required")
	}
	credentialFile, err := mysqlCredentialOptionFile(credentials.Password)
	if err != nil {
		return nil, err
	}
	defer os.Remove(credentialFile)
	connectTimeout := mysqlConnectTimeoutSeconds(ctx)
	command := exec.CommandContext(ctx, binary, "--defaults-file="+credentialFile, "--batch", "--protocol=TCP", "--connect-timeout="+strconv.Itoa(connectTimeout), "-h", host, "-P", strconv.Itoa(endpoint.Port), "-u", credentials.Username)
	command.Stdin = strings.NewReader(query + "\n")
	command.Env = environmentWithout("MYSQL_PWD")
	output, err := command.CombinedOutput()
	if err != nil {
		cause := err
		if contextErr := ctx.Err(); contextErr != nil {
			cause = contextErr
		}
		return nil, &QueryError{
			Code:   mysqlErrorCode(output),
			Output: strings.TrimSpace(string(output)),
			Err:    cause,
		}
	}
	return output, nil
}

func mysqlCredentialOptionFile(password string) (string, error) {
	file, err := os.CreateTemp("", ".clusterguard-mysql-*.cnf")
	if err != nil {
		return "", fmt.Errorf("create MySQL credential file: %w", err)
	}
	path := file.Name()
	cleanup := func(cause error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", cause
	}
	if err := file.Chmod(0o600); err != nil {
		return cleanup(fmt.Errorf("secure MySQL credential file: %w", err))
	}
	escapedPassword := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	).Replace(password)
	if _, err := fmt.Fprintf(file, "[client]\npassword=\"%s\"\n", escapedPassword); err != nil {
		return cleanup(fmt.Errorf("write MySQL credential file: %w", err))
	}
	if err := file.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync MySQL credential file: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close MySQL credential file: %w", err)
	}
	return path, nil
}

func environmentWithout(name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func mysqlConnectTimeoutSeconds(ctx context.Context) int {
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
	if len(headers) == 1 {
		return parseSingleColumnTSVRows(output, headers[0])
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
			row[header] = decodeMySQLBatchValue(values[index])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseSingleColumnTSVRows(output []byte, header string) ([]Row, error) {
	headerEnd := bytes.IndexByte(output, '\n')
	if headerEnd < 0 || headerEnd+1 == len(output) {
		return nil, nil
	}
	data := output[headerEnd+1:]
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	records := bytes.Split(data, []byte{'\n'})
	rows := make([]Row, 0, len(records))
	for _, record := range records {
		record = bytes.TrimSuffix(record, []byte{'\r'})
		value := ""
		if len(record) > 0 {
			reader := csv.NewReader(bytes.NewReader(record))
			reader.Comma = '\t'
			reader.FieldsPerRecord = 1
			reader.LazyQuotes = true
			values, err := reader.Read()
			if err != nil {
				return nil, fmt.Errorf("parse MySQL row: %w", err)
			}
			value = values[0]
		}
		rows = append(rows, Row{header: decodeMySQLBatchValue(value)})
	}
	return rows, nil
}

func decodeMySQLBatchValue(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 == len(value) {
			decoded.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case '0':
			decoded.WriteByte(0)
		case 'b':
			decoded.WriteByte('\b')
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		case 'Z':
			decoded.WriteByte(0x1a)
		case '\\':
			decoded.WriteByte('\\')
		default:
			decoded.WriteByte('\\')
			decoded.WriteByte(value[index])
		}
	}
	return decoded.String()
}
