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

func dockerRoleTestPolicy(t *testing.T) ClusterPolicy {
	t.Helper()
	return ClusterPolicy{
		ClusterID: "11111111-1111-4111-8111-111111111111", InstanceID: "22222222-2222-4222-8222-222222222222",
		Engine: model.EngineMySQL, RuntimeKind: model.RuntimeDocker, MySQLPort: 3306,
		MySQLDefaultsFile: "/run/secrets/clusterguard-operation.cnf", DockerMySQLBinary: "/usr/bin/mysql",
		DockerSwarmService: "cg-mysql-01", DockerFenceFile: filepath.Join(t.TempDir(), "99-clusterguard-fence.cnf"),
	}
}

func dockerPSCall(policy ClusterPolicy) string {
	return "/usr/bin/docker ps --filter label=com.docker.swarm.service.name=" + policy.DockerSwarmService +
		" --filter label=clusterguard.cluster_id=" + string(policy.ClusterID) +
		" --filter label=clusterguard.instance_id=" + string(policy.InstanceID) + " --format {{.ID}}"
}

func dockerMySQLCall(policy ClusterPolicy, containerID, statement string) string {
	return "/usr/bin/docker exec " + containerID + " " + policy.DockerMySQLBinary +
		" --defaults-file=" + policy.MySQLDefaultsFile + " --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names --execute " + statement
}

func TestDockerRolePersistsRestartFenceBeforeMutatingContainer(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	containerID := "0123456789abcdef"
	runner := &roleCommandRunner{results: map[string]roleCommandResult{
		dockerPSCall(policy): {output: containerID + "\n"},
		dockerMySQLCall(policy, containerID, "SELECT SUBSTRING_INDEX(VERSION(), '.', 1)"):                              {output: "8\n"},
		dockerMySQLCall(policy, containerID, "SET PERSIST_ONLY super_read_only = ON; SET PERSIST_ONLY read_only = ON"): {},
		dockerMySQLCall(policy, containerID, "SET GLOBAL super_read_only = ON; SET GLOBAL read_only = ON"):             {},
		dockerMySQLCall(policy, containerID, "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"):                    {output: "1\t1\n"},
	}}
	controller := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir())
	if err := controller.PersistReadOnly(context.Background(), policy, true); err != nil {
		t.Fatalf("persist Docker read-only role: %v", err)
	}
	contents, err := os.ReadFile(policy.DockerFenceFile)
	if err != nil {
		t.Fatalf("read Docker restart fence: %v", err)
	}
	if !strings.Contains(string(contents), "read_only=ON") || !strings.Contains(string(contents), "super_read_only=ON") {
		t.Fatalf("restart fence=%s", contents)
	}
	directoryInfo, err := os.Stat(filepath.Dir(policy.DockerFenceFile))
	if err != nil {
		t.Fatalf("stat restart fence directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o755 {
		t.Fatalf("restart fence directory mode=%v", directoryInfo.Mode().Perm())
	}
	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil || !status.Isolated() || !status.ServiceRunning || !status.DatabaseReachable {
		t.Fatalf("Docker isolation status=%+v err=%v", status, err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl") || strings.ContainsAny(call, ";|&") && !strings.Contains(call, "--execute") {
			t.Fatalf("Docker controller escaped the allowlisted command path: %q", call)
		}
	}
}

func TestDockerRoleProvesStoppedTaskIsRestartFenced(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	runner := &roleCommandRunner{results: map[string]roleCommandResult{dockerPSCall(policy): {}}}
	controller := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir())
	if err := writeDockerRestartFence(policy.DockerFenceFile, true); err != nil {
		t.Fatal(err)
	}
	if err := controller.state.persistState(policy, true); err != nil {
		t.Fatal(err)
	}
	status, err := controller.IsolationStatus(context.Background(), policy)
	if err != nil || !status.Isolated() || status.ServiceRunning || status.DatabaseReachable {
		t.Fatalf("stopped Docker isolation status=%+v err=%v", status, err)
	}
}

func TestDockerRoleFailsClosedForAmbiguousOrUnreachableRuntime(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	for name, result := range map[string]roleCommandResult{
		"ambiguous":   {output: "0123456789abcdef\nfedcba9876543210\n"},
		"unreachable": {err: errors.New("Docker daemon unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &roleCommandRunner{results: map[string]roleCommandResult{dockerPSCall(policy): result}}
			controller := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir())
			if _, _, err := controller.Status(context.Background(), policy); err == nil {
				t.Fatalf("%s Docker runtime was accepted", name)
			}
		})
	}
}

func TestDockerRoleRejectsWarningTextAsWorkloadIdentity(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	runner := &roleCommandRunner{results: map[string]roleCommandResult{
		dockerPSCall(policy): {output: "WARNING: cannot read Docker configuration\n0123456789abcdef\n"},
	}}
	controller := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir())
	if _, _, err := controller.Status(context.Background(), policy); err == nil || !strings.Contains(err.Error(), "unexpected workload identity output") {
		t.Fatalf("Docker warning output was not rejected safely: %v", err)
	}
}

func TestDockerRoleUsesManagedCLIConfigurationDirectory(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	containerID := "0123456789abcdef"
	directory := "/var/lib/clusterguard-agent/docker-cli"
	psCall := strings.Replace(dockerPSCall(policy), "/usr/bin/docker ", "/usr/bin/docker --config "+directory+" ", 1)
	mysqlCall := strings.Replace(
		dockerMySQLCall(policy, containerID, "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only"),
		"/usr/bin/docker ", "/usr/bin/docker --config "+directory+" ", 1,
	)
	runner := &roleCommandRunner{results: map[string]roleCommandResult{
		psCall: {output: containerID + "\n"}, mysqlCall: {output: "1\t1\n"},
	}}
	controller := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir(), directory)
	if readOnly, superReadOnly, err := controller.Status(context.Background(), policy); err != nil || !readOnly || !superReadOnly {
		t.Fatalf("managed Docker CLI status readOnly=%t superReadOnly=%t err=%v", readOnly, superReadOnly, err)
	}
}
