package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func dockerPostgreSQLTestPolicy(t *testing.T) ClusterPolicy {
	t.Helper()
	return ClusterPolicy{
		ClusterID:                     "11111111-1111-4111-8111-111111111111",
		InstanceID:                    "22222222-2222-4222-8222-222222222222",
		Engine:                        model.EnginePostgreSQL,
		RuntimeKind:                   model.RuntimeDocker,
		DockerSwarmService:            "cgpg16_postgresql01",
		DockerSwarmServiceID:          "abcdefghijklmnopqrstuvwxy",
		PostgreSQLNodeID:              "33333333-3333-4333-8333-333333333333",
		PostgreSQLHostname:            "orch-pg01",
		PostgreSQLPort:                55432,
		DockerPostgreSQLPort:          5432,
		PostgreSQLUser:                "postgres",
		PostgreSQLDataDirectory:       filepath.Join(t.TempDir(), "postgresql", "55432"),
		DockerPostgreSQLDataDirectory: "/var/lib/postgresql/data",
		PostgreSQLBinaryDirectory:     "/usr/lib/postgresql/16/bin",
		PostgreSQLPassfile:            filepath.Join(t.TempDir(), "55432.pass"),
		PostgreSQLDatabase:            "postgres",
		PostgreSQLReplicationUser:     "cg_replication",
	}
}

func TestDockerPostgreSQLStatusUsesUniqueLabeledContainerAndContainerPort(t *testing.T) {
	policy := dockerPostgreSQLTestPolicy(t)
	containerID := "0123456789abcdef"
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "docker service inspect --format {{.ID}} " + policy.DockerSwarmService, output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker ps --filter label=com.docker.swarm.service.name=" + policy.DockerSwarmService, output: containerID + "\n"},
		{contains: `docker inspect --type container --format {{index .Config.Labels "com.docker.swarm.service.id"}} ` + containerID, output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker service inspect --format {{.ID}} " + policy.DockerSwarmService, output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker ps --filter label=com.docker.swarm.service.name=" + policy.DockerSwarmService, output: containerID + "\n"},
		{contains: `docker inspect --type container --format {{index .Config.Labels "com.docker.swarm.service.id"}} ` + containerID, output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker exec --user postgres --env PGPASSFILE=" + policy.PostgreSQLPassfile, output: "t\n"},
	}}
	controller, err := NewDockerPostgreSQLController(runner, "/usr/bin/docker")
	if err != nil {
		t.Fatal(err)
	}
	running, inRecovery, err := controller.Status(context.Background(), policy)
	if err != nil || !running || !inRecovery {
		t.Fatalf("Docker PostgreSQL status running=%t recovery=%t err=%v", running, inRecovery, err)
	}
	runner.assertComplete()
	command := runner.commands[len(runner.commands)-1]
	if !strings.Contains(command, "--port 5432") || strings.Contains(command, "--port 55432") {
		t.Fatalf("Docker PostgreSQL status did not use the container port: %s", command)
	}
}

func TestDockerPostgreSQLFailsClosedForAmbiguousWorkload(t *testing.T) {
	policy := dockerPostgreSQLTestPolicy(t)
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "docker service inspect", output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker ps", output: "0123456789abcdef\nfedcba9876543210\n"},
	}}
	controller, err := NewDockerPostgreSQLController(runner, "/usr/bin/docker")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.Status(context.Background(), policy); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous Docker PostgreSQL workload was accepted: %v", err)
	}
	runner.assertComplete()
}

func TestDockerPostgreSQLStopChangesSwarmDesiredState(t *testing.T) {
	policy := dockerPostgreSQLTestPolicy(t)
	runner := &orderedCommandRunner{t: t, expected: []commandExpectation{
		{contains: "docker service inspect", output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker service scale --detach=true " + policy.DockerSwarmService + "=0"},
		{contains: "docker service inspect", output: policy.DockerSwarmServiceID + "\n"},
		{contains: "docker ps --filter label=com.docker.swarm.service.name=" + policy.DockerSwarmService},
	}}
	controller, err := NewDockerPostgreSQLController(runner, "/usr/bin/docker")
	if err != nil {
		t.Fatal(err)
	}
	controller.pollInterval = 0
	if err := controller.Stop(context.Background(), policy); err != nil {
		t.Fatalf("stop Docker PostgreSQL service: %v", err)
	}
	runner.assertComplete()
	for _, command := range runner.commands {
		if strings.Contains(command, "docker stop") {
			t.Fatalf("plain docker stop escaped the Swarm controller: %s", command)
		}
		if strings.Contains(command, "--detach=false") {
			t.Fatalf("Swarm CLI convergence wait duplicated the Agent postcondition check: %s", command)
		}
	}
}

func TestDockerPostgreSQLRecoveryToolRequiresPrivateRegularPassfile(t *testing.T) {
	policy := dockerPostgreSQLTestPolicy(t)
	if err := os.WriteFile(policy.PostgreSQLPassfile, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	controller, err := NewDockerPostgreSQLController(&orderedCommandRunner{t: t}, "/usr/bin/docker")
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.runRecoveryTool(context.Background(), policy, "postgres:16.4", "pg_rewind", "/host/data", "/container/data")
	if err == nil || !strings.Contains(err.Error(), "must not be accessible") {
		t.Fatalf("unsafe Docker PostgreSQL passfile was accepted: %v", err)
	}
}

func TestRuntimePostgreSQLControllerRejectsUnknownRuntime(t *testing.T) {
	controller := NewRuntimePostgreSQLController(nil, nil)
	policy := dockerPostgreSQLTestPolicy(t)
	policy.RuntimeKind = model.RuntimeKubernetes
	if _, _, err := controller.Status(context.Background(), policy); err == nil {
		t.Fatal("Kubernetes PostgreSQL policy escaped the host runtime controller")
	}
}
