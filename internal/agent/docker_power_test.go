package agent

import (
	"context"
	"strings"
	"testing"
)

func TestDockerPowerUsesSwarmDesiredStateInsteadOfContainerStop(t *testing.T) {
	policy := dockerRoleTestPolicy(t)
	runner := &roleCommandRunner{results: map[string]roleCommandResult{}}
	roles := NewDockerMySQLRoleController(runner, "/usr/bin/docker", t.TempDir())
	controller := NewDockerPowerController(runner, "/usr/bin/docker", roles)
	if err := controller.StopService(context.Background(), policy); err != nil {
		t.Fatalf("stop Docker service: %v", err)
	}
	if err := controller.StartService(context.Background(), policy); err != nil {
		t.Fatalf("start Docker service: %v", err)
	}
	want := []string{
		"/usr/bin/docker service scale --detach=false cg-mysql-01=0",
		"/usr/bin/docker service scale --detach=false cg-mysql-01=1",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Docker power calls=%v want=%v", runner.calls, want)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, " docker stop ") || strings.Contains(call, "systemctl") {
			t.Fatalf("unsafe Docker service lifecycle call: %q", call)
		}
	}
}
