package agent

import (
	"context"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type LinuxVIPController struct {
	runner       CommandRunner
	ipBinary     string
	arpingBinary string
}

func NewLinuxVIPController(runner CommandRunner, ipBinary string, arpingBinary string) *LinuxVIPController {
	return &LinuxVIPController{runner: runner, ipBinary: ipBinary, arpingBinary: arpingBinary}
}

// gatewayDatabaseEndpoint accepts only the explicit database-side no-VIP policy.
// The gateway owns its frontend address; database fencing still runs separately.
func gatewayDatabaseEndpoint(policy ClusterPolicy) (bool, error) {
	if policy.EndpointProvider != model.EndpointProviderGateway {
		return false, nil
	}
	if (policy.Engine != model.EngineMySQL && policy.Engine != model.EnginePostgreSQL) ||
		policy.VIP != "" || policy.Interface != "" || policy.Prefix != 0 {
		return true, fmt.Errorf("gateway database policy must use a supported engine without a host VIP")
	}
	return true, nil
}

func (controller *LinuxVIPController) Status(ctx context.Context, policy ClusterPolicy) (bool, error) {
	if gateway, err := gatewayDatabaseEndpoint(policy); gateway {
		return false, err
	}
	output, err := controller.runner.Run(ctx, controller.ipBinary, "-4", "-o", "addr", "show", "dev", policy.Interface)
	if err != nil {
		return false, err
	}
	needle := " " + policy.VIP + "/"
	return strings.Contains(" "+string(output), needle), nil
}

func (controller *LinuxVIPController) Acquire(ctx context.Context, policy ClusterPolicy) error {
	if gateway, _ := gatewayDatabaseEndpoint(policy); gateway {
		return fmt.Errorf("gateway database nodes cannot acquire the frontend VIP")
	}
	owns, err := controller.Status(ctx, policy)
	if err != nil {
		return err
	}
	if !owns {
		address := fmt.Sprintf("%s/%d", policy.VIP, policy.Prefix)
		if _, err := controller.runner.Run(ctx, controller.ipBinary, "addr", "add", address, "dev", policy.Interface); err != nil {
			return err
		}
	}
	_, err = controller.runner.Run(ctx, controller.arpingBinary, "-U", "-c", "1", "-I", policy.Interface, policy.VIP)
	return err
}

func (controller *LinuxVIPController) Release(ctx context.Context, policy ClusterPolicy) error {
	if gateway, err := gatewayDatabaseEndpoint(policy); gateway {
		return err
	}
	owns, err := controller.Status(ctx, policy)
	if err != nil {
		return err
	}
	if !owns {
		return nil
	}
	address := fmt.Sprintf("%s/%d", policy.VIP, policy.Prefix)
	_, err = controller.runner.Run(ctx, controller.ipBinary, "addr", "del", address, "dev", policy.Interface)
	return err
}
