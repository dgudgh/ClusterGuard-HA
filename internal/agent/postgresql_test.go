package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type postgresqlBaseBackupRunner struct {
	t               *testing.T
	stage           string
	source          string
	sourceResponses []string
	commands        []string
}

func (runner *postgresqlBaseBackupRunner) Run(_ context.Context, name string, arguments ...string) ([]byte, error) {
	runner.t.Helper()
	command := strings.TrimSpace(name + " " + strings.Join(arguments, " "))
	runner.commands = append(runner.commands, command)
	switch len(runner.commands) {
	case 1:
		if !strings.Contains(command, "systemctl stop postgresql-16") {
			runner.t.Fatalf("stop command=%s", command)
		}
	case 2:
		return []byte("inactive\n"), nil
	case 3:
		if !strings.Contains(command, "/pg_basebackup --pgdata "+runner.stage) || strings.Contains(strings.ToLower(command), "password=") {
			runner.t.Fatalf("unsafe base backup command=%s", command)
		}
		if err := os.MkdirAll(runner.stage, 0o700); err != nil {
			runner.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runner.stage, "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
			runner.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runner.stage, "postgresql.auto.conf"), []byte("# donor settings\n"), 0o600); err != nil {
			runner.t.Fatal(err)
		}
	case 4:
		if !strings.Contains(command, "systemctl start postgresql-16") {
			runner.t.Fatalf("start command=%s", command)
		}
	case 5, 7, 10:
		if !strings.Contains(command, "systemctl show") {
			runner.t.Fatalf("service verification command=%s", command)
		}
		return []byte("active\n"), nil
	case 6, 8, 11:
		if !strings.Contains(command, "SELECT pg_is_in_recovery()") {
			runner.t.Fatalf("recovery verification command=%s", command)
		}
		return []byte("t\n"), nil
	case 9, 12:
		if !strings.Contains(command, "current_setting('clusterguard.primary_node_id', true)") {
			runner.t.Fatalf("source verification command=%s", command)
		}
		responses := runner.sourceResponses
		if len(responses) == 0 {
			responses = []string{runner.source}
		}
		responseIndex := 0
		if len(runner.commands) == 12 {
			responseIndex = 1
		}
		if responseIndex >= len(responses) {
			runner.t.Fatalf("unexpected source verification attempt %d", responseIndex+1)
		}
		return []byte(responses[responseIndex] + "\n"), nil
	default:
		runner.t.Fatalf("unexpected command=%s", command)
	}
	return nil, nil
}

type commandExpectation struct {
	contains string
	output   string
	err      error
}

type orderedCommandRunner struct {
	t        *testing.T
	expected []commandExpectation
	commands []string
	next     int
}

func (runner *orderedCommandRunner) Run(_ context.Context, name string, arguments ...string) ([]byte, error) {
	runner.t.Helper()
	command := strings.TrimSpace(name + " " + strings.Join(arguments, " "))
	runner.commands = append(runner.commands, command)
	if runner.next >= len(runner.expected) {
		runner.t.Fatalf("unexpected command: %s", command)
	}
	expectation := runner.expected[runner.next]
	runner.next++
	if !strings.Contains(command, expectation.contains) {
		runner.t.Fatalf("command %d = %q, want substring %q", runner.next, command, expectation.contains)
	}
	return []byte(expectation.output), expectation.err
}

func (runner *orderedCommandRunner) assertComplete() {
	runner.t.Helper()
	if runner.next != len(runner.expected) {
		runner.t.Fatalf("executed %d commands, want %d", runner.next, len(runner.expected))
	}
}

func postgresqlControllerPolicy() ClusterPolicy {
	return ClusterPolicy{
		ClusterID:                 model.NewResourceID(),
		InstanceID:                model.NewResourceID(),
		Engine:                    model.EnginePostgreSQL,
		PostgreSQLNodeID:          model.NewResourceID(),
		PostgreSQLPort:            5432,
		PostgreSQLService:         "postgresql-16",
		PostgreSQLUser:            "postgres",
		PostgreSQLDataDirectory:   "/var/lib/pgsql/16/data",
		PostgreSQLBinaryDirectory: "/usr/pgsql-16/bin",
		PostgreSQLPassfile:        "/etc/clusterguard/postgresql.pass",
		PostgreSQLDatabase:        "postgres",
		PostgreSQLReplicationUser: "clusterguard_repl",
	}
}

func TestPostgreSQLControllerStatusRequiresActiveServiceAndNativeRole(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "/usr/bin/systemctl show --property=ActiveState --value postgresql-16", output: "active\n"},
		{contains: "/usr/sbin/runuser -u postgres -- /usr/bin/env PGPASSFILE=/etc/clusterguard/postgresql.pass", output: "t\n"},
	}}
	controller, err := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err != nil {
		t.Fatal(err)
	}
	running, inRecovery, err := controller.Status(context.Background(), postgresqlControllerPolicy())
	if err != nil || !running || !inRecovery {
		t.Fatalf("status running=%v in_recovery=%v err=%v", running, inRecovery, err)
	}
	runner.assertComplete()
	command := runner.commands[1]
	for _, required := range []string{
		"PGCONNECT_TIMEOUT=5", "/usr/pgsql-16/bin/psql", "--host 127.0.0.1", "--port 5432",
		"--username postgres", "--dbname postgres", "SELECT pg_is_in_recovery()",
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("status command missing %q: %s", required, command)
		}
	}
}

func TestDefaultPostgreSQLControllerUsesPAMFreePrivilegeDrop(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "/usr/bin/systemctl show --property=ActiveState --value postgresql-16", output: "active\n"},
		{contains: "/usr/bin/setpriv --reuid postgres --regid postgres --init-groups -- /usr/bin/env", output: "t\n"},
	}}
	controller, err := NewDefaultPostgreSQLController(runner)
	if err != nil {
		t.Fatal(err)
	}
	running, inRecovery, err := controller.Status(context.Background(), postgresqlControllerPolicy())
	if err != nil || !running || !inRecovery {
		t.Fatalf("default status running=%v in_recovery=%v err=%v", running, inRecovery, err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerStopProvesServiceInactive(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "/usr/bin/systemctl stop postgresql-16"},
		{contains: "/usr/bin/systemctl show --property=ActiveState --value postgresql-16", output: "inactive\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.Stop(context.Background(), postgresqlControllerPolicy()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerStartWaitsThroughTransientReadinessFailure(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "/usr/bin/systemctl start postgresql-16"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", err: fmt.Errorf("database is starting")},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Start(context.Background(), postgresqlControllerPolicy()); err != nil {
		t.Fatalf("start through transient readiness failure: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerStopWaitsUntilSystemdReportsInactive(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "/usr/bin/systemctl stop postgresql-16"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "systemctl show", output: "inactive\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Stop(context.Background(), postgresqlControllerPolicy()); err != nil {
		t.Fatalf("stop through transient active state: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerPromoteVerifiesRecoveryEnded(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "/usr/sbin/runuser -u postgres -- /usr/pgsql-16/bin/pg_ctl -D /var/lib/pgsql/16/data -w -t 60 promote"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "f\n"},
		{contains: "ALTER SYSTEM RESET primary_conninfo"},
		{contains: "ALTER SYSTEM RESET clusterguard.primary_node_id"},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "current_setting('primary_conninfo', true)", output: "\t\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.Promote(context.Background(), postgresqlControllerPolicy()); err != nil {
		t.Fatalf("promote: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerPromoteWaitsForRecoveryToEnd(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "pg_ctl", output: "server promoting\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "f\n"},
		{contains: "ALTER SYSTEM RESET primary_conninfo"},
		{contains: "ALTER SYSTEM RESET clusterguard.primary_node_id"},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "current_setting('primary_conninfo', true)", output: "\t\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Promote(context.Background(), postgresqlControllerPolicy()); err != nil {
		t.Fatalf("promote through transient recovery state: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerPromoteRejectsStaleUpstreamConfiguration(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "pg_ctl", output: "server promoted\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "f\n"},
		{contains: "ALTER SYSTEM RESET primary_conninfo"},
		{contains: "ALTER SYSTEM RESET clusterguard.primary_node_id"},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "current_setting('primary_conninfo', true)", output: "postgresql://stale-primary\t\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	err := controller.Promote(context.Background(), postgresqlControllerPolicy())
	if err == nil || !strings.Contains(err.Error(), "stale upstream configuration") {
		t.Fatalf("expected stale upstream configuration rejection, got %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerRepointUsesAllowlistedPasswordlessSourceURI(t *testing.T) {
	policy := postgresqlControllerPolicy()
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), Hostname: "pg-primary.internal", IPAddress: "192.0.2.20", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "ALTER SYSTEM SET primary_conninfo =", output: ""},
		{contains: "ALTER SYSTEM SET clusterguard.primary_node_id =", output: ""},
		{contains: "SELECT pg_reload_conf()", output: ""},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "current_setting('clusterguard.primary_node_id', true)", output: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.20\t5432\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.Repoint(context.Background(), policy, peer); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	runner.assertComplete()
	commands := strings.Join(runner.commands[:2], "\n")
	for _, required := range []string{
		"host=''192.0.2.20''",
		"passfile=''/etc/clusterguard/postgresql.pass''",
		"application_name=''" + string(policy.PostgreSQLNodeID) + "''",
		"ALTER SYSTEM SET clusterguard.primary_node_id = '" + string(peer.NodeID) + "'",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("repoint commands missing %q: %s", required, commands)
		}
	}
	if strings.Contains(strings.ToLower(commands), "password=") {
		t.Fatalf("repoint exposed a password: %s", commands)
	}
}

func TestPostgreSQLControllerKeepsPlatformAndNativeNodeIdentitiesSeparate(t *testing.T) {
	policy := postgresqlControllerPolicy()
	policy.PostgreSQLNodeID = model.NewResourceID()
	peer := PostgreSQLPeer{
		InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(),
		Hostname: "pg-primary.internal", IPAddress: "192.0.2.20", Port: 5432,
	}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "ALTER SYSTEM SET primary_conninfo ="},
		{contains: "ALTER SYSTEM SET clusterguard.primary_node_id ="},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "current_setting('clusterguard.primary_node_id', true)", output: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.20\t5432\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Repoint(context.Background(), policy, peer); err != nil {
		t.Fatalf("repoint with distinct identities: %v", err)
	}
	runner.assertComplete()
	commands := strings.Join(runner.commands[:2], "\n")
	for _, required := range []string{
		"application_name=''" + string(policy.PostgreSQLNodeID) + "''",
		"ALTER SYSTEM SET clusterguard.primary_node_id = '" + string(peer.NodeID) + "'",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("native PostgreSQL identity missing %q: %s", required, commands)
		}
	}
	for _, forbidden := range []string{string(policy.InstanceID), string(peer.InstanceID)} {
		if strings.Contains(commands, forbidden) {
			t.Fatalf("platform identity leaked into PostgreSQL configuration %q: %s", forbidden, commands)
		}
	}
}

func TestPostgreSQLControllerRepointWaitsForApprovedSourceIdentity(t *testing.T) {
	policy := postgresqlControllerPolicy()
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.23", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "ALTER SYSTEM SET primary_conninfo ="},
		{contains: "ALTER SYSTEM SET clusterguard.primary_node_id ="},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "current_setting('clusterguard.primary_node_id', true)", output: "stale-source\tpostgresql://stale\tstreaming\t192.0.2.23\t5432\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "current_setting('clusterguard.primary_node_id', true)", output: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.23\t5432\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Repoint(context.Background(), policy, peer); err != nil {
		t.Fatalf("repoint through transient source state: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerRepointWaitsForApprovedSourceToStream(t *testing.T) {
	policy := postgresqlControllerPolicy()
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.23", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "ALTER SYSTEM SET primary_conninfo ="},
		{contains: "ALTER SYSTEM SET clusterguard.primary_node_id ="},
		{contains: "SELECT pg_reload_conf()"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "pg_stat_wal_receiver", output: string(peer.NodeID) + "\t" + connection + "\tstarting\t192.0.2.23\t5432\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "pg_stat_wal_receiver", output: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.23\t5432\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.Repoint(context.Background(), policy, peer); err != nil {
		t.Fatalf("repoint before approved source streams: %v", err)
	}
	runner.assertComplete()
}

func TestPostgreSQLControllerRewindStopsBeforeRewindAndRestartsAsStandby(t *testing.T) {
	policy := postgresqlControllerPolicy()
	policy.PostgreSQLDataDirectory = filepath.Join(t.TempDir(), "postgresql", "data")
	if err := os.MkdirAll(policy.PostgreSQLDataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	autoConfig := filepath.Join(policy.PostgreSQLDataDirectory, "postgresql.auto.conf")
	if err := os.WriteFile(autoConfig, []byte("# rewind output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.21", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "systemctl stop postgresql-16"},
		{contains: "systemctl show", output: "inactive\n"},
		{contains: "/usr/pgsql-16/bin/pg_rewind --target-pgdata " + policy.PostgreSQLDataDirectory + " --source-server host='192.0.2.21'", output: "servers diverged at WAL location"},
		{contains: "systemctl start postgresql-16"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "t\n"},
		{contains: "current_setting('clusterguard.primary_node_id', true)", output: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.21\t5432\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.Rewind(context.Background(), policy, peer); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	runner.assertComplete()
	contents, err := os.ReadFile(autoConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{string(policy.PostgreSQLNodeID), string(peer.NodeID), "primary_conninfo"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("rewind identity override missing %q: %s", expected, contents)
		}
	}
}

func TestPostgreSQLControllerBaseBackupAtomicallyReplacesDataAndPreservesTargetIdentity(t *testing.T) {
	root := t.TempDir()
	dataDirectory := filepath.Join(root, "postgresql", "data")
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, "old-primary.marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := postgresqlControllerPolicy()
	policy.PostgreSQLDataDirectory = dataDirectory
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.22", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	runner := &postgresqlBaseBackupRunner{t: t, stage: dataDirectory + ".clusterguard-stage", source: string(peer.NodeID) + "\t" + connection + "\tstreaming\t192.0.2.22\t5432"}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.BaseBackup(context.Background(), policy, peer); err != nil {
		t.Fatalf("base backup: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDirectory, "postgresql.auto.conf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(contents)
	for _, expected := range []string{string(policy.PostgreSQLNodeID), string(peer.NodeID), "primary_conninfo"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("identity override missing %q: %s", expected, configuration)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDirectory, "old-primary.marker")); !os.IsNotExist(err) {
		t.Fatalf("old data survived verified atomic replacement: %v", err)
	}
	if _, err := os.Stat(dataDirectory + ".clusterguard-backup"); !os.IsNotExist(err) {
		t.Fatalf("verified backup quarantine was not cleaned: %v", err)
	}
	if len(runner.commands) != 9 {
		t.Fatalf("base backup executed %d commands, want source verification command", len(runner.commands))
	}
}

func TestPostgreSQLControllerBaseBackupWaitsForApprovedSourceToStream(t *testing.T) {
	root := t.TempDir()
	dataDirectory := filepath.Join(root, "postgresql", "data")
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := postgresqlControllerPolicy()
	policy.PostgreSQLDataDirectory = dataDirectory
	peer := PostgreSQLPeer{InstanceID: model.NewResourceID(), NodeID: model.NewResourceID(), IPAddress: "192.0.2.22", Port: 5432}
	connection, err := postgresqlSourceURI(policy, peer)
	if err != nil {
		t.Fatal(err)
	}
	prefix := string(peer.NodeID) + "\t" + connection
	runner := &postgresqlBaseBackupRunner{
		t: t, stage: dataDirectory + ".clusterguard-stage",
		sourceResponses: []string{
			prefix + "\tstarting\t192.0.2.22\t5432",
			prefix + "\tstreaming\t192.0.2.22\t5432",
		},
	}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	controller.pollInterval = 0
	if err := controller.BaseBackup(context.Background(), policy, peer); err != nil {
		t.Fatalf("base backup before approved source streams: %v", err)
	}
	if len(runner.commands) != 12 {
		t.Fatalf("base backup executed %d commands, want two source convergence probes", len(runner.commands))
	}
}

func TestPostgreSQLControllerBaseBackupRejectsDangerousDataDirectory(t *testing.T) {
	policy := postgresqlControllerPolicy()
	policy.PostgreSQLDataDirectory = "/"
	controller, _ := NewPostgreSQLController(&orderedCommandRunner{t: t}, "/usr/bin/systemctl", "/usr/sbin/runuser")
	if err := controller.BaseBackup(context.Background(), policy, PostgreSQLPeer{InstanceID: model.NewResourceID(), IPAddress: "192.0.2.22", Port: 5432}); err == nil {
		t.Fatal("dangerous PostgreSQL data directory was accepted")
	}
}

func TestNewPostgreSQLControllerRejectsRelativeExecutables(t *testing.T) {
	if _, err := NewPostgreSQLController(&orderedCommandRunner{t: t}, "systemctl", "/usr/sbin/runuser"); err == nil {
		t.Fatal("relative systemctl binary was accepted")
	}
	if _, err := NewPostgreSQLController(&orderedCommandRunner{t: t}, "/usr/bin/systemctl", "runuser"); err == nil {
		t.Fatal("relative runuser binary was accepted")
	}
	if _, err := NewPostgreSQLController(nil, "/usr/bin/systemctl", "/usr/sbin/runuser"); err == nil {
		t.Fatal("nil command runner was accepted")
	}
}

func TestPostgreSQLControllerTreatsActivatingServiceAsTransient(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{{contains: "systemctl show", output: "activating\n"}}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	running, inRecovery, err := controller.Status(context.Background(), postgresqlControllerPolicy())
	if err != nil || running || inRecovery {
		t.Fatalf("activating state running=%v in_recovery=%v error=%v", running, inRecovery, err)
	}
}

func TestPostgreSQLControllerRejectsUnknownServiceState(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{{contains: "systemctl show", output: "surprising\n"}}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	_, _, err := controller.Status(context.Background(), postgresqlControllerPolicy())
	if err == nil || !strings.Contains(err.Error(), "unexpected PostgreSQL service state") {
		t.Fatalf("unexpected state error=%v", err)
	}
}

func TestPostgreSQLControllerRejectsUnexpectedRecoveryOutput(t *testing.T) {
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "systemctl show", output: "active\n"},
		{contains: "SELECT pg_is_in_recovery()", output: "maybe\n"},
	}}
	controller, _ := NewPostgreSQLController(runner, "/usr/bin/systemctl", "/usr/sbin/runuser")
	_, _, err := controller.Status(context.Background(), postgresqlControllerPolicy())
	if err == nil || !strings.Contains(err.Error(), "unexpected pg_is_in_recovery") {
		t.Fatalf("unexpected recovery error=%v", err)
	}
}

func ExamplePostgreSQLController() {
	_, _ = NewPostgreSQLController(OSCommandRunner{}, "/usr/bin/systemctl", "/usr/sbin/runuser")
	fmt.Println("restricted")
	// Output: restricted
}
