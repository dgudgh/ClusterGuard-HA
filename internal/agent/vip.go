package agent

import (
	"context"
	"fmt"
	"strings"
)

type LinuxVIPController struct {
	runner       CommandRunner
	ipBinary     string
	arpingBinary string
}

func NewLinuxVIPController(runner CommandRunner, ipBinary string, arpingBinary string) *LinuxVIPController {
	return &LinuxVIPController{runner: runner, ipBinary: ipBinary, arpingBinary: arpingBinary}
}

func (controller *LinuxVIPController) Status(ctx context.Context, policy ClusterPolicy) (bool, error) {
	output, err := controller.runner.Run(ctx, controller.ipBinary, "-4", "-o", "addr", "show", "dev", policy.Interface)
	if err != nil {
		return false, err
	}
	needle := " " + policy.VIP + "/"
	return strings.Contains(" "+string(output), needle), nil
}

func (controller *LinuxVIPController) Acquire(ctx context.Context, policy ClusterPolicy) error {
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
