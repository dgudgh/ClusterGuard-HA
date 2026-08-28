package agent

import (
	"context"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

// DockerPowerController changes Swarm desired state through a manager. A
// plain docker stop is intentionally never used because Swarm would recreate
// the task and defeat a planned database shutdown.
type DockerPowerController struct {
	runner       CommandRunner
	dockerBinary string
	roles        *DockerMySQLRoleController
	host         *LinuxPowerController
}

func NewDockerPowerController(runner CommandRunner, dockerBinary string, roles *DockerMySQLRoleController) *DockerPowerController {
	return &DockerPowerController{
		runner: runner, dockerBinary: dockerBinary, roles: roles,
		host: NewLinuxPowerController(runner),
	}
}

func (controller *DockerPowerController) PrepareRecoverySnapshot(ctx context.Context, policy ClusterPolicy, snapshot model.PowerSnapshot) error {
	return controller.host.PrepareRecoverySnapshot(ctx, policy, snapshot)
}

func (controller *DockerPowerController) scale(ctx context.Context, policy ClusterPolicy, replicas int) error {
	if policy.DockerSwarmService == "" {
		return fmt.Errorf("Docker Swarm service is not configured")
	}
	if controller.roles == nil {
		return fmt.Errorf("Docker role controller is not configured")
	}
	if _, err := controller.roles.runDocker(ctx, "service", "scale", "--detach=false",
		fmt.Sprintf("%s=%d", policy.DockerSwarmService, replicas)); err != nil {
		return fmt.Errorf("scale allowlisted Docker Swarm service to %d: %w", replicas, err)
	}
	return nil
}

func (controller *DockerPowerController) StopService(ctx context.Context, policy ClusterPolicy) error {
	return controller.scale(ctx, policy, 0)
}

func (controller *DockerPowerController) StartService(ctx context.Context, policy ClusterPolicy) error {
	return controller.scale(ctx, policy, 1)
}

func (controller *DockerPowerController) ServiceStatus(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	_, running, err := controller.roles.runningContainer(ctx, policy)
	if err != nil || !running {
		return running, false, err
	}
	if _, _, err := controller.roles.Status(ctx, policy); err != nil {
		return true, false, nil
	}
	return true, true, nil
}

func (controller *DockerPowerController) PowerOff(ctx context.Context, policy ClusterPolicy) error {
	return controller.host.PowerOff(ctx, policy)
}

type RuntimePowerController struct {
	linux  PowerController
	docker PowerController
}

func NewRuntimePowerController(linux, docker PowerController) *RuntimePowerController {
	return &RuntimePowerController{linux: linux, docker: docker}
}

func (controller *RuntimePowerController) selected(policy ClusterPolicy) (PowerController, error) {
	switch policy.RuntimeKind {
	case "", model.RuntimeLinux:
		if controller.linux != nil {
			return controller.linux, nil
		}
	case model.RuntimeDocker:
		if controller.docker != nil {
			return controller.docker, nil
		}
	}
	return nil, fmt.Errorf("runtime %q has no database power controller", policy.RuntimeKind)
}

func (controller *RuntimePowerController) PrepareRecoverySnapshot(ctx context.Context, policy ClusterPolicy, snapshot model.PowerSnapshot) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.PrepareRecoverySnapshot(ctx, policy, snapshot)
}

func (controller *RuntimePowerController) StopService(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.StopService(ctx, policy)
}

func (controller *RuntimePowerController) StartService(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.StartService(ctx, policy)
}

func (controller *RuntimePowerController) ServiceStatus(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	selected, err := controller.selected(policy)
	if err != nil {
		return false, false, err
	}
	return selected.ServiceStatus(ctx, policy)
}

func (controller *RuntimePowerController) PowerOff(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.PowerOff(ctx, policy)
}

func dockerPowerServiceName(policy ClusterPolicy) string {
	if policy.RuntimeKind == model.RuntimeDocker {
		return strings.TrimSpace(policy.DockerSwarmService)
	}
	return powerServiceName(policy)
}
