package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type lifecycleProcessRunnerStub struct {
	input []byte
	env   []string
	name  string
	args  []string
	out   []byte
	err   error
}

func (runner *lifecycleProcessRunnerStub) Run(_ context.Context, input []byte, environment []string, name string, arguments ...string) ([]byte, error) {
	runner.input = append([]byte{}, input...)
	runner.env = append([]string{}, environment...)
	runner.name = name
	runner.args = append([]string{}, arguments...)
	return append([]byte{}, runner.out...), runner.err
}

func TestShellExecutorPassesSecretsOnlyThroughEnvironmentAndEmitsStages(t *testing.T) {
	runner := &lifecycleProcessRunnerStub{out: []byte(
		`{"type":"event","stage":"install","status":"running","message":"install started"}` + "\n" +
			`{"type":"event","stage":"verify","status":"succeeded","message":"replication healthy"}` + "\n" +
			`{"type":"result","verified":true,"message":"node synchronized"}` + "\n",
	)}
	executor, err := NewShellExecutor("/usr/local/libexec/clusterguard-node-lifecycle.sh", runner)
	if err != nil {
		t.Fatalf("new shell executor: %v", err)
	}
	request, plan := executableLifecyclePlan()
	secrets := ExecutionSecrets{
		SSHPassword: "ssh-secret", MySQLRootPassword: "mysql-root-secret", ReplicationPassword: "replication-secret",
		PostgreSQLAdminPassword: "pg-admin-secret", PostgreSQLReplicationPassword: "pg-replication-secret",
	}
	events := []Event{}
	result, err := executor.Execute(context.Background(), request, plan, secrets, func(event Event) { events = append(events, event) })
	if err != nil || !result.Verified || len(events) != 2 || events[0].Stage != StageInstall || events[1].Stage != StageVerify {
		t.Fatalf("execute result=%+v events=%+v err=%v", result, events, err)
	}
	unsafe := string(runner.input) + " " + runner.name + " " + strings.Join(runner.args, " ")
	for _, secret := range []string{"ssh-secret", "mysql-root-secret", "replication-secret", "pg-admin-secret", "pg-replication-secret"} {
		if strings.Contains(unsafe, secret) {
			t.Fatalf("secret %q appeared in stdin or command arguments: %s", secret, unsafe)
		}
	}
	joinedEnvironment := strings.Join(runner.env, "\n")
	for _, expected := range []string{
		"CG_SSH_PASSWORD=ssh-secret", "CG_MYSQL_ROOT_PASSWORD=mysql-root-secret", "CG_MYSQL_REPLICATION_PASSWORD=replication-secret",
		"CG_POSTGRESQL_ADMIN_PASSWORD=pg-admin-secret", "CG_POSTGRESQL_REPLICATION_PASSWORD=pg-replication-secret",
	} {
		if !strings.Contains(joinedEnvironment, expected) {
			t.Fatalf("executor environment missing %q: %v", expected, runner.env)
		}
	}
	if runner.name != "/usr/local/libexec/clusterguard-node-lifecycle.sh" || len(runner.args) != 1 || runner.args[0] != "execute" {
		t.Fatalf("executor command=%s %v", runner.name, runner.args)
	}
}

func TestShellExecutorPassesOnlyTypedStaticLifecyclePaths(t *testing.T) {
	runner := &lifecycleProcessRunnerStub{out: []byte(`{"type":"result","verified":true}` + "\n")}
	executor, err := NewShellExecutor("/usr/local/libexec/clusterguard-node-lifecycle.sh", runner, WithShellEnvironment(ShellEnvironment{
		PackageRepository:       "/opt/clusterguard/packages",
		KnownHostsFile:          "/etc/clusterguard/known_hosts",
		IdentityFile:            "/etc/clusterguard/lifecycle_ed25519",
		JQBinary:                "/usr/local/libexec/jq-linux-amd64",
		ControlJoinHelper:       "/usr/local/libexec/clusterguard-control-join",
		CloneHelper:             "/usr/local/libexec/clusterguard-mysql-clone",
		XtraBackupHelper:        "/usr/local/libexec/clusterguard-mysql-xtrabackup",
		PostgreSQLInstallHelper: "/usr/local/libexec/clusterguard-postgresql-install.sh",
		PostgreSQLSyncHelper:    "/usr/local/libexec/clusterguard-postgresql-sync.sh",
	}))
	if err != nil {
		t.Fatalf("new configured shell executor: %v", err)
	}
	request, plan := executableLifecyclePlan()
	if _, err := executor.Execute(context.Background(), request, plan, ExecutionSecrets{}, nil); err != nil {
		t.Fatalf("execute configured shell lifecycle: %v", err)
	}
	joined := strings.Join(runner.env, "\n")
	for _, expected := range []string{
		"CG_PACKAGE_REPOSITORY=/opt/clusterguard/packages",
		"CG_SSH_KNOWN_HOSTS=/etc/clusterguard/known_hosts",
		"CG_SSH_IDENTITY_FILE=/etc/clusterguard/lifecycle_ed25519",
		"CG_JQ_BINARY=/usr/local/libexec/jq-linux-amd64",
		"CG_CONTROL_JOIN_HELPER=/usr/local/libexec/clusterguard-control-join",
		"CG_MYSQL_CLONE_HELPER=/usr/local/libexec/clusterguard-mysql-clone",
		"CG_MYSQL_XTRABACKUP_HELPER=/usr/local/libexec/clusterguard-mysql-xtrabackup",
		"CG_POSTGRESQL_INSTALL_HELPER=/usr/local/libexec/clusterguard-postgresql-install.sh",
		"CG_POSTGRESQL_SYNC_HELPER=/usr/local/libexec/clusterguard-postgresql-sync.sh",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("static lifecycle environment missing %q: %v", expected, runner.env)
		}
	}
}

func TestShellExecutorRejectsMalformedOrUnverifiedResult(t *testing.T) {
	request, plan := executableLifecyclePlan()
	for _, testCase := range []struct {
		name string
		out  string
		err  error
	}{
		{name: "malformed", out: "not-json\n"},
		{name: "missing result", out: `{"type":"event","stage":"install","status":"succeeded"}` + "\n"},
		{name: "unverified", out: `{"type":"result","verified":false,"message":"verification failed"}` + "\n"},
		{name: "process failure", err: errors.New("remote command failed with mysql-root-secret")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &lifecycleProcessRunnerStub{out: []byte(testCase.out), err: testCase.err}
			executor, err := NewShellExecutor("/usr/local/libexec/clusterguard-node-lifecycle.sh", runner)
			if err != nil {
				t.Fatalf("new shell executor: %v", err)
			}
			_, err = executor.Execute(context.Background(), request, plan, ExecutionSecrets{MySQLRootPassword: "mysql-root-secret"}, func(Event) {})
			if err == nil || strings.Contains(err.Error(), "mysql-root-secret") {
				t.Fatalf("unsafe executor error=%v", err)
			}
		})
	}
}

func TestOSLifecycleProcessRunnerReturnsBoundedStderrDetail(t *testing.T) {
	_, err := (OSLifecycleProcessRunner{}).Run(
		context.Background(), nil, nil, "/bin/sh", "-c", "printf 'logical dump failed on target\\n' >&2; exit 7",
	)
	if err == nil || !strings.Contains(err.Error(), "logical dump failed on target") {
		t.Fatalf("process error did not preserve stderr detail: %v", err)
	}
}

func TestShellExecutorRedactsSecretsButPreservesProcessFailureDetail(t *testing.T) {
	runner := &lifecycleProcessRunnerStub{err: errors.New("mysql rejected mysql-root-secret while resetting GTID")}
	executor, err := NewShellExecutor("/usr/local/libexec/clusterguard-node-lifecycle.sh", runner)
	if err != nil {
		t.Fatalf("new shell executor: %v", err)
	}
	request, plan := executableLifecyclePlan()
	_, err = executor.Execute(context.Background(), request, plan, ExecutionSecrets{MySQLRootPassword: "mysql-root-secret"}, nil)
	if err == nil || !strings.Contains(err.Error(), "while resetting GTID") || strings.Contains(err.Error(), "mysql-root-secret") {
		t.Fatalf("process error was not safely propagated: %v", err)
	}
}
