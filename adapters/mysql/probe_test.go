package mysql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestParseReplicationAcceptsBothTerminologyFamilies(t *testing.T) {
	legacy := legacyReplicationRow()
	modern := modernReplicationRow()
	for _, row := range []Row{legacy, modern} {
		status, err := parseReplication(row)
		if err != nil {
			t.Fatal(err)
		}
		if status.SourceIdentity["server_uuid"] != "source-uuid" || status.IOThread != model.ThreadRunning || status.SQLThread != model.ThreadRunning || status.LagSeconds == nil || *status.LagSeconds != 2 {
			t.Fatalf("unexpected status: %+v", status)
		}
		if status.RetrievedPosition != "source-uuid:1-10" || status.ExecutedPosition != "source-uuid:1-10" {
			t.Fatalf("unexpected positions: %+v", status)
		}
	}
}

func TestParseReplicationPreservesSourceReconnectAndChannelErrors(t *testing.T) {
	status, err := parseReplication(Row{
		"Source_UUID":           "source-uuid",
		"Replica_IO_Running":    "Connecting",
		"Replica_SQL_Running":   "Yes",
		"Seconds_Behind_Source": "NULL",
		"Executed_Gtid_Set":     "source-uuid:1-20",
		"Last_IO_Error":         "Error reconnecting to source",
		"Last_SQL_Error":        "",
	})
	if err != nil {
		t.Fatalf("parse reconnecting replication: %v", err)
	}
	if status.IOThread != model.ThreadConnecting || status.SQLThread != model.ThreadRunning || status.LagSeconds != nil {
		t.Fatalf("source reconnect state was lost: %+v", status)
	}
	if status.LastIOError != "Error reconnecting to source" || status.LastSQLError != "" || status.LastError != "Error reconnecting to source" {
		t.Fatalf("replication channel errors were not preserved: %+v", status)
	}
}

func TestDiscoverSupportsMySQLVersions(t *testing.T) {
	tests := []struct {
		version           string
		replication       Row
		statementRejected bool
	}{
		{version: "5.7.44", replication: legacyReplicationRow(), statementRejected: true},
		{version: "8.0.44", replication: modernReplicationRow()},
		{version: "8.4.10", replication: modernReplicationRow()},
		{version: "9.7.0", replication: modernReplicationRow()},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			runner := healthyReplicaRunner(test.version, test.replication)
			if test.statementRejected {
				runner.replicaError = errors.New("placeholder")
				runner.replicaError = &QueryError{Code: 1064, Output: "syntax rejected", Err: runner.replicaError}
			}
			result, err := New(runner).Discover(context.Background(), adapterRequest())
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			instance := result.Instance
			if instance.Role != model.RoleReplica || instance.Health.State != model.HealthHealthy || instance.EngineMetadata["version"] != test.version {
				t.Fatalf("unexpected engine-neutral result: %+v", instance)
			}
			if !instance.PromotionEligible || instance.EngineMetadata["read_only"] != "true" || instance.EngineMetadata["super_read_only"] != "true" {
				t.Fatalf("healthy read-only replica must be promotion eligible with persisted read-only facts: %+v", instance)
			}
			if instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning || instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds != 2 {
				t.Fatalf("unexpected replication result: %+v", instance.Replication)
			}
			if test.statementRejected && runner.legacyQueryRuns != 1 {
				t.Fatalf("legacy statement was not used after syntax rejection: %v", runner.queries)
			}
		})
	}
}

func TestDiscoverDegradesWritableReplicaAndBlocksPromotionEligibility(t *testing.T) {
	runner := &fakeRunner{
		identity:    identityRow("8.4.10", "0", "0"),
		replication: modernReplicationRow(),
	}
	result, err := New(runner).Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover writable replica: %v", err)
	}
	instance := result.Instance
	if instance.Role != model.RoleReplica || instance.Health.State != model.HealthDegraded || instance.PromotionEligible {
		t.Fatalf("writable replica must be degraded and ineligible: %+v", instance)
	}
	if instance.EngineMetadata["read_only"] != "false" || instance.EngineMetadata["super_read_only"] != "false" {
		t.Fatalf("writable replica read-only facts were not persisted: %+v", instance.EngineMetadata)
	}
}

func TestReplicationProbeFallsBackOnlyForStatementRejection(t *testing.T) {
	t.Run("statement unsupported", func(t *testing.T) {
		runner := healthyReplicaRunner("5.7.44", legacyReplicationRow())
		runner.replicaError = &QueryError{Code: 1064, Output: "syntax rejected", Err: errors.New("exit status 1")}
		status, configured, err := probeReplication(context.Background(), runner, adapterRequest().Endpoint, adapterRequest().Credentials)
		if err != nil || !configured || status.SourceIdentity["server_uuid"] != "source-uuid" {
			t.Fatalf("fallback probe: configured=%v status=%+v err=%v", configured, status, err)
		}
		if !reflect.DeepEqual(runner.queries, []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"}) {
			t.Fatalf("unexpected fallback queries: %v", runner.queries)
		}
	})

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "authentication", err: errors.New("access denied")},
		{name: "network", err: errors.New("connection refused")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "server", err: errors.New("server unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := healthyReplicaRunner("8.0.44", modernReplicationRow())
			runner.replicaError = test.err
			_, _, err := probeReplication(context.Background(), runner, adapterRequest().Endpoint, adapterRequest().Credentials)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected original error %v, got %v", test.err, err)
			}
			if !reflect.DeepEqual(runner.queries, []string{"SHOW REPLICA STATUS"}) || runner.legacyQueryRuns != 0 {
				t.Fatalf("unsafe fallback after %s error: %v", test.name, runner.queries)
			}
		})
	}
}

func TestDiscoverDegradesReplicaWithStoppedOrMissingThread(t *testing.T) {
	for _, test := range []struct {
		name string
		row  Row
	}{
		{name: "stopped", row: Row{"Source_UUID": "source-uuid", "Replica_IO_Running": "No", "Replica_SQL_Running": "Yes"}},
		{name: "missing", row: Row{"Source_UUID": "source-uuid", "Replica_IO_Running": "Yes"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := New(healthyReplicaRunner("8.4.10", test.row)).Discover(context.Background(), adapterRequest())
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if result.Instance.Role != model.RoleReplica || result.Instance.Health.State != model.HealthDegraded {
				t.Fatalf("stopped or missing thread must degrade replica: %+v", result.Instance)
			}
		})
	}
}

func TestDiscoverRejectsMultipleReplicationChannels(t *testing.T) {
	runner := healthyReplicaRunner("8.4.10", modernReplicationRow())
	second := modernReplicationRow()
	second["Channel_Name"] = "analytics"
	second["Source_UUID"] = "other-source-uuid"
	second["Replica_SQL_Running"] = "No"
	runner.replicationRows = []Row{modernReplicationRow(), second}

	_, err := New(runner).Discover(context.Background(), adapterRequest())
	if err == nil || !strings.Contains(err.Error(), "multiple MySQL replication channels are unsupported") {
		t.Fatalf("multi-channel replication did not fail closed: %v", err)
	}
}

func TestDiscoverIdentifiesPrimaryOnlyWhenWritableAndUnreplicated(t *testing.T) {
	for _, test := range []struct {
		name          string
		readOnly      string
		superReadOnly string
		wantRole      model.InstanceRole
		wantHealth    model.HealthState
	}{
		{name: "writable", readOnly: "0", superReadOnly: "0", wantRole: model.RolePrimary, wantHealth: model.HealthHealthy},
		{name: "read only", readOnly: "1", superReadOnly: "1", wantRole: model.RoleUnknown, wantHealth: model.HealthDegraded},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{identity: identityRow("9.7.0", test.readOnly, test.superReadOnly)}
			result, err := New(runner).Discover(context.Background(), adapterRequest())
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if result.Instance.Role != test.wantRole || result.Instance.Health.State != test.wantHealth {
				t.Fatalf("unexpected role/health: %+v", result.Instance)
			}
		})
	}
}

func TestCLIQueryRunnerUsesPasswordEnvironmentAndParsesTSV(t *testing.T) {
	tempDir := t.TempDir()
	argsPath := filepath.Join(tempDir, "args")
	passwordPath := filepath.Join(tempDir, "password")
	binaryPath := filepath.Join(tempDir, "mysql")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$MYSQL_TEST_ARGS"
printf '%s' "$MYSQL_PWD" > "$MYSQL_TEST_PASSWORD"
printf '%s\n' 'server_uuid	hostname	note' 'source-uuid	mysql-a	' 'source-uuid-2	mysql-b	line1\nline2'
`
	if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mysql: %v", err)
	}
	t.Setenv("MYSQL_TEST_ARGS", argsPath)
	t.Setenv("MYSQL_TEST_PASSWORD", passwordPath)
	password := "p@ss word --password=visible"
	endpoint := adapter.Endpoint{Hostname: "localhost", Port: 4407}
	rows, err := (CLIQueryRunner{Binary: binaryPath}).Query(context.Background(), endpoint, adapter.Credentials{Username: "monitor", Password: password}, "SELECT 1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !reflect.DeepEqual(rows, []Row{
		{"server_uuid": "source-uuid", "hostname": "mysql-a", "note": ""},
		{"server_uuid": "source-uuid-2", "hostname": "mysql-b", "note": "line1\nline2"},
	}) {
		t.Fatalf("unexpected TSV rows: %#v", rows)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	argumentLines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(argumentLines) == 0 || argumentLines[0] != "--no-defaults" {
		t.Fatalf("mysql option files were not disabled before all other arguments: %q", args)
	}
	if strings.Contains(string(args), password) || strings.Contains(string(args), "--password") {
		t.Fatalf("password leaked into command arguments: %q", args)
	}
	for _, flag := range []string{"--batch", "--protocol=TCP", "4407"} {
		if !strings.Contains(string(args), flag) {
			t.Fatalf("missing %s in command arguments: %q", flag, args)
		}
	}
	if strings.Contains(string(args), "--raw") {
		t.Fatalf("raw batch output can split multiline fields into invalid TSV records: %q", args)
	}
	if strings.Contains(string(args), "--skip-column-names") {
		t.Fatalf("headers were disabled: %q", args)
	}
	storedPassword, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatalf("read password: %v", err)
	}
	if string(storedPassword) != password {
		t.Fatalf("MYSQL_PWD mismatch: %q", storedPassword)
	}
}

func TestCLIQueryRunnerSendsSQLOnStdinInsteadOfProcessArguments(t *testing.T) {
	tempDir := t.TempDir()
	argsPath := filepath.Join(tempDir, "args")
	stdinPath := filepath.Join(tempDir, "stdin")
	binaryPath := filepath.Join(tempDir, "mysql")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$MYSQL_TEST_ARGS"
cat > "$MYSQL_TEST_STDIN"
printf 'result\n1\n'
`
	if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYSQL_TEST_ARGS", argsPath)
	t.Setenv("MYSQL_TEST_STDIN", stdinPath)
	statement := "CHANGE REPLICATION SOURCE TO SOURCE_PASSWORD='top-secret'"
	if err := (CLIQueryRunner{Binary: binaryPath}).Exec(context.Background(), adapter.Endpoint{Hostname: "localhost", Port: 3306}, adapter.Credentials{Username: "operator", Password: "login-secret"}, statement); err != nil {
		t.Fatalf("execute SQL: %v", err)
	}
	arguments, _ := os.ReadFile(argsPath)
	stdin, _ := os.ReadFile(stdinPath)
	if strings.Contains(string(arguments), statement) || strings.Contains(string(arguments), "top-secret") {
		t.Fatalf("SQL secret leaked into process arguments: %s", arguments)
	}
	if strings.TrimSpace(string(stdin)) != statement {
		t.Fatalf("stdin=%q want %q", stdin, statement)
	}
}

func TestCLIQueryRunnerClassifiesOnlySyntaxRejectionAsUnsupported(t *testing.T) {
	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "mysql")
	script := `#!/bin/sh
if [ "$MYSQL_TEST_ERROR" = "syntax" ]; then
  printf 'ERROR 1064 (42000): syntax rejected\n' >&2
else
  printf 'ERROR 1045 (28000): access denied\n' >&2
fi
exit 1
`
	if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mysql: %v", err)
	}
	runner := CLIQueryRunner{Binary: binaryPath}
	for _, test := range []struct {
		name            string
		mode            string
		wantUnsupported bool
	}{
		{name: "syntax", mode: "syntax", wantUnsupported: true},
		{name: "authentication", mode: "auth", wantUnsupported: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MYSQL_TEST_ERROR", test.mode)
			_, err := runner.Query(context.Background(), adapterRequest().Endpoint, adapterRequest().Credentials, "SHOW REPLICA STATUS")
			if err == nil {
				t.Fatal("expected query error")
			}
			var queryError *QueryError
			if !errors.As(err, &queryError) {
				t.Fatalf("expected typed QueryError, got %T: %v", err, err)
			}
			if errors.Is(err, ErrStatementUnsupported) != test.wantUnsupported {
				t.Fatalf("unsupported classification mismatch: %v", err)
			}
		})
	}
}
