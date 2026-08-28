package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type roleCommandResult struct {
	output string
	err    error
}

type roleCommandRunner struct {
	results map[string]roleCommandResult
	calls   []string
}

func (runner *roleCommandRunner) Run(_ context.Context, name string, arguments ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(arguments, " "))
	runner.calls = append(runner.calls, call)
	result := runner.results[call]
	return []byte(result.output), result.err
}

func TestMySQLRoleStatusReadsBothGlobalReadOnlyFlags(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", t.TempDir())
	policy := ClusterPolicy{MySQLPort: 3306, MySQLDefaultsFile: "/etc/clusterguard/mysql.cnf"}
	readOnly, superReadOnly, err := controller.Status(context.Background(), policy)
	if err == nil {
		t.Fatal("empty role status output was accepted")
	}
	if readOnly || superReadOnly {
		t.Fatalf("empty output invented role flags: read_only=%v super_read_only=%v", readOnly, superReadOnly)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only") {
		t.Fatalf("role status command=%v", runner.calls)
	}

	runner.outputs[runner.calls[0]] = "1\t1\n"
	readOnly, superReadOnly, err = controller.Status(context.Background(), policy)
	if err != nil || !readOnly || !superReadOnly {
		t.Fatalf("role status: read_only=%v super_read_only=%v err=%v", readOnly, superReadOnly, err)
	}
}

func TestMySQLRoleUsesThePerClusterClientBinary(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", t.TempDir())
	policy := ClusterPolicy{
		MySQLBinary:       "/opt/clusterguard/mysql/3384/software/bin/mysql",
		MySQLPort:         3384,
		MySQLDefaultsFile: "/etc/clusterguard/mysql/3384-client.cnf",
	}
	_, _, _ = controller.Status(context.Background(), policy)
	if len(runner.calls) != 1 || !strings.HasPrefix(runner.calls[0], policy.MySQLBinary+" ") {
		t.Fatalf("role command did not use per-cluster client: %v", runner.calls)
	}
}

func TestMySQLPersistReadOnlyRecordsIsolationBeforeRuntimeMutation(t *testing.T) {
	stateDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:         model.NewResourceID(),
		InstanceID:        model.NewResourceID(),
		MySQLPort:         3306,
		MySQLDefaultsFile: "/etc/clusterguard/mysql/3306-client.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	versionCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SELECT SUBSTRING_INDEX(VERSION(), '.', 1)"
	roleCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SET GLOBAL super_read_only = ON; SET GLOBAL read_only = ON"
	runner.results[versionCommand] = roleCommandResult{output: "5\n"}
	runner.results[roleCommand] = roleCommandResult{err: errors.New("database is stopped")}

	if err := controller.PersistReadOnly(context.Background(), policy, true); err == nil {
		t.Fatal("stopped database unexpectedly accepted the runtime role mutation")
	}
	contents, err := os.ReadFile(filepath.Join(stateDirectory, string(policy.ClusterID)+".json"))
	if err != nil {
		t.Fatalf("read durable isolation state: %v", err)
	}
	if !strings.Contains(string(contents), `"read_only":true`) {
		t.Fatalf("durable isolation was not recorded before runtime mutation: %s", contents)
	}
}

func TestMySQLPersistReadOnlyPersistsSafeRestartRoleOnMySQL8(t *testing.T) {
	stateDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:         model.NewResourceID(),
		InstanceID:        model.NewResourceID(),
		MySQLPort:         3384,
		MySQLDefaultsFile: "/etc/clusterguard/mysql/3384-client.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	versionCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SELECT SUBSTRING_INDEX(VERSION(), '.', 1)"
	persistCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SET PERSIST_ONLY super_read_only = ON; SET PERSIST_ONLY read_only = ON"
	runtimeCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SET GLOBAL super_read_only = ON; SET GLOBAL read_only = ON"
	runner.results[versionCommand] = roleCommandResult{output: "8\n"}

	if err := controller.PersistReadOnly(context.Background(), policy, true); err != nil {
		t.Fatalf("persist read-only role: %v", err)
	}
	want := []string{versionCommand, persistCommand, runtimeCommand}
	if len(runner.calls) != len(want) {
		t.Fatalf("role commands=%v want=%v", runner.calls, want)
	}
	for index := range want {
		if runner.calls[index] != want[index] {
			t.Fatalf("role commands=%v want=%v", runner.calls, want)
		}
	}
}

func TestMySQLPersistReadWriteKeepsRestartRoleReadOnlyOnMySQL8(t *testing.T) {
	stateDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:         model.NewResourceID(),
		InstanceID:        model.NewResourceID(),
		MySQLPort:         3306,
		MySQLDefaultsFile: "/etc/clusterguard/mysql/3306-client.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	versionCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SELECT SUBSTRING_INDEX(VERSION(), '.', 1)"
	persistCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SET PERSIST_ONLY super_read_only = ON; SET PERSIST_ONLY read_only = ON"
	runtimeCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SET GLOBAL super_read_only = OFF; SET GLOBAL read_only = OFF"
	runner.results[versionCommand] = roleCommandResult{output: "8\n"}

	if err := controller.PersistReadOnly(context.Background(), policy, false); err != nil {
		t.Fatalf("persist read-write role: %v", err)
	}
	want := []string{versionCommand, persistCommand, runtimeCommand}
	if len(runner.calls) != len(want) {
		t.Fatalf("role commands=%v want=%v", runner.calls, want)
	}
	for index := range want {
		if runner.calls[index] != want[index] {
			t.Fatalf("role commands=%v want=%v", runner.calls, want)
		}
	}
}

func TestMySQLIsolationStatusProvesStoppedServiceHasReadOnlyRestartDefaults(t *testing.T) {
	stateDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:               model.NewResourceID(),
		InstanceID:              model.NewResourceID(),
		MySQLPort:               3384,
		MySQLDefaultsFile:       "/etc/clusterguard/mysql/3384-client.cnf",
		MySQLService:            "mysql-matrix-3384.service",
		MySQLServerBinary:       "/opt/mysql/bin/mysqld",
		MySQLServerDefaultsFile: "/etc/mysql-version-matrix/mysql-3384.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	if err := controller.persistState(policy, true); err != nil {
		t.Fatalf("persist test isolation state: %v", err)
	}
	statusCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"
	runner.results[statusCommand] = roleCommandResult{err: errors.New("database is stopped")}
	runner.results["/usr/bin/systemctl is-active mysql-matrix-3384.service"] = roleCommandResult{output: "inactive\n", err: errors.New("exit status 3")}
	runner.results["/opt/mysql/bin/mysqld --defaults-file=/etc/mysql-version-matrix/mysql-3384.cnf --verbose --help"] = roleCommandResult{
		output: "datadir " + dataDirectory + "\nread-only TRUE\nsuper-read-only TRUE\n",
	}

	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil {
		t.Fatalf("offline durable isolation status: %v", err)
	}
	if status.DatabaseReachable || status.ServiceRunning || !status.RestartReadOnly || !status.PersistedReadOnly || !status.Isolated() {
		t.Fatalf("offline durable isolation status=%+v", status)
	}
}

func TestMySQLIsolationStatusTreatsEmptySystemdAutoRestartAsStopped(t *testing.T) {
	stateDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:               model.NewResourceID(),
		InstanceID:              model.NewResourceID(),
		MySQLPort:               3384,
		MySQLDefaultsFile:       "/etc/clusterguard/mysql/3384-client.cnf",
		MySQLService:            "mysql-matrix-3384.service",
		MySQLServerBinary:       "/opt/mysql/bin/mysqld",
		MySQLServerDefaultsFile: "/etc/mysql-version-matrix/mysql-3384.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	if err := controller.persistState(policy, true); err != nil {
		t.Fatalf("persist test isolation state: %v", err)
	}
	statusCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"
	runner.results[statusCommand] = roleCommandResult{err: errors.New("database is stopped")}
	runner.results["/usr/bin/systemctl is-active mysql-matrix-3384.service"] = roleCommandResult{output: "activating\n", err: errors.New("exit status 3")}
	runner.results["/usr/bin/systemctl show mysql-matrix-3384.service --property=ActiveState --property=SubState --property=MainPID --property=ControlGroup --no-pager"] = roleCommandResult{
		output: "MainPID=0\nControlGroup=\nActiveState=activating\nSubState=auto-restart\n",
	}
	runner.results["/opt/mysql/bin/mysqld --defaults-file=/etc/mysql-version-matrix/mysql-3384.cnf --verbose --help"] = roleCommandResult{
		output: "datadir " + dataDirectory + "\nread-only TRUE\nsuper-read-only TRUE\n",
	}

	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil {
		t.Fatalf("auto-restart isolation status: %v", err)
	}
	if status.DatabaseReachable || status.ServiceRunning || !status.RestartReadOnly || !status.PersistedReadOnly || !status.Isolated() {
		t.Fatalf("auto-restart isolation status=%+v", status)
	}
}

func TestMySQLIsolationStatusRejectsSystemdAutoRestartWithLiveMainPID(t *testing.T) {
	stateDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:               model.NewResourceID(),
		InstanceID:              model.NewResourceID(),
		MySQLPort:               3384,
		MySQLDefaultsFile:       "/etc/clusterguard/mysql/3384-client.cnf",
		MySQLService:            "mysql-matrix-3384.service",
		MySQLServerBinary:       "/opt/mysql/bin/mysqld",
		MySQLServerDefaultsFile: "/etc/mysql-version-matrix/mysql-3384.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	if err := controller.persistState(policy, true); err != nil {
		t.Fatalf("persist test isolation state: %v", err)
	}
	statusCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3384-client.cnf --protocol=tcp --host=127.0.0.1 --port=3384 --batch --skip-column-names --execute SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"
	runner.results[statusCommand] = roleCommandResult{err: errors.New("database is not accepting connections")}
	runner.results["/usr/bin/systemctl is-active mysql-matrix-3384.service"] = roleCommandResult{output: "activating\n", err: errors.New("exit status 3")}
	runner.results["/usr/bin/systemctl show mysql-matrix-3384.service --property=ActiveState --property=SubState --property=MainPID --property=ControlGroup --no-pager"] = roleCommandResult{
		output: "MainPID=1234\nControlGroup=/system.slice/mysql-matrix-3384.service\nActiveState=activating\nSubState=start\n",
	}
	runner.results["/opt/mysql/bin/mysqld --defaults-file=/etc/mysql-version-matrix/mysql-3384.cnf --verbose --help"] = roleCommandResult{
		output: "datadir " + dataDirectory + "\nread-only TRUE\nsuper-read-only TRUE\n",
	}

	status, err := controller.IsolationStatus(context.Background(), policy)
	if err == nil || !status.ServiceRunning {
		t.Fatalf("live transitional service was accepted: status=%+v err=%v", status, err)
	}
}

func TestMySQLIsolationStatusRejectsWritableRestartDefaults(t *testing.T) {
	stateDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:               model.NewResourceID(),
		InstanceID:              model.NewResourceID(),
		MySQLPort:               3306,
		MySQLDefaultsFile:       "/etc/clusterguard/mysql/3306-client.cnf",
		MySQLService:            "mysqld.service",
		MySQLServerBinary:       "/usr/local/mysql/bin/mysqld",
		MySQLServerDefaultsFile: "/data/mysql8/conf/my.cnf",
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	if err := controller.persistState(policy, true); err != nil {
		t.Fatalf("persist test isolation state: %v", err)
	}
	statusCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"
	runner.results[statusCommand] = roleCommandResult{err: errors.New("database is stopped")}
	runner.results["/usr/bin/systemctl is-active mysqld.service"] = roleCommandResult{output: "inactive\n", err: errors.New("exit status 3")}
	runner.results["/usr/local/mysql/bin/mysqld --defaults-file=/data/mysql8/conf/my.cnf --verbose --help"] = roleCommandResult{
		output: "datadir " + dataDirectory + "\nread-only FALSE\nsuper-read-only FALSE\n",
	}

	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil {
		t.Fatalf("inspect unsafe restart defaults: %v", err)
	}
	if status.RestartReadOnly || status.Isolated() {
		t.Fatalf("writable restart defaults were accepted: %+v", status)
	}
}

func TestMySQLIsolationStatusRejectsPersistedWritableOverrides(t *testing.T) {
	stateDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	policy := ClusterPolicy{
		ClusterID:               model.NewResourceID(),
		InstanceID:              model.NewResourceID(),
		MySQLPort:               3306,
		MySQLDefaultsFile:       "/etc/clusterguard/mysql/3306-client.cnf",
		MySQLService:            "mysqld.service",
		MySQLServerBinary:       "/usr/local/mysql/bin/mysqld",
		MySQLServerDefaultsFile: "/data/mysql8/conf/my.cnf",
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, "mysqld-auto.cnf"), []byte(`{
		"Version": 2,
		"mysql_dynamic_variables": {
			"read_only": {"Value": "OFF"},
			"super_read_only": {"Value": "OFF"}
		}
	}`), 0600); err != nil {
		t.Fatalf("write persisted globals: %v", err)
	}
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	controller := NewMySQLRoleController(runner, "/usr/local/mysql/bin/mysql", stateDirectory)
	if err := controller.persistState(policy, true); err != nil {
		t.Fatalf("persist test isolation state: %v", err)
	}
	statusCommand := "/usr/local/mysql/bin/mysql --defaults-file=/etc/clusterguard/mysql/3306-client.cnf --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"
	runner.results[statusCommand] = roleCommandResult{err: errors.New("database is stopped")}
	runner.results["/usr/bin/systemctl is-active mysqld.service"] = roleCommandResult{output: "inactive\n", err: errors.New("exit status 3")}
	runner.results["/usr/local/mysql/bin/mysqld --defaults-file=/data/mysql8/conf/my.cnf --verbose --help"] = roleCommandResult{
		output: "datadir " + dataDirectory + "\nread-only TRUE\nsuper-read-only TRUE\n",
	}

	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil {
		t.Fatalf("inspect persisted writable overrides: %v", err)
	}
	if status.RestartReadOnly || status.Isolated() {
		t.Fatalf("persisted writable restart overrides were accepted: %+v", status)
	}
}
