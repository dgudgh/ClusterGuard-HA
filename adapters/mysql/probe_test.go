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
			if instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning || instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds != 2 {
				t.Fatalf("unexpected replication result: %+v", instance.Replication)
			}
			if test.statementRejected && runner.legacyQueryRuns != 1 {
				t.Fatalf("legacy statement was not used after syntax rejection: %v", runner.queries)
			}
		})
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
printf 'server_uuid\thostname\tnote\r\nsource-uuid\tmysql-a\t\r\nsource-uuid-2\tmysql-b\tready\r\n'
`
	if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mysql: %v", err)
	}
	t.Setenv("MYSQL_TEST_ARGS", argsPath)
	t.Setenv("MYSQL_TEST_PASSWORD", passwordPath)
	password := "p@ss word --password=visible"
	rows, err := (CLIQueryRunner{Binary: binaryPath}).Query(context.Background(), adapterRequest().Endpoint, adapter.Credentials{Username: "monitor", Password: password}, "SELECT 1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !reflect.DeepEqual(rows, []Row{
		{"server_uuid": "source-uuid", "hostname": "mysql-a", "note": ""},
		{"server_uuid": "source-uuid-2", "hostname": "mysql-b", "note": "ready"},
	}) {
		t.Fatalf("unexpected TSV rows: %#v", rows)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.Contains(string(args), password) || strings.Contains(string(args), "--password") {
		t.Fatalf("password leaked into command arguments: %q", args)
	}
	for _, flag := range []string{"--batch", "--raw"} {
		if !strings.Contains(string(args), flag) {
			t.Fatalf("missing %s in command arguments: %q", flag, args)
		}
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
