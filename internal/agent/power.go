package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

const defaultPowerSnapshotDirectory = "/etc/clusterguard/power-snapshots"

// PowerController drives the host and database service lifecycle that the
// power shutdown workflow needs: stopping and starting the MySQL service and
// powering off the host. It is deliberately thin — every operation is a
// systemctl call — so the control plane's PowerShutdownAdapter can rely on
// predictable, inspectable side effects.
type PowerController interface {
	// PrepareRecoverySnapshot durably stores the frozen topology used by the
	// boot-time restore units. It must complete before any database service is
	// stopped.
	PrepareRecoverySnapshot(ctx context.Context, policy ClusterPolicy, snapshot model.PowerSnapshot) error
	// StopService stops the database service (systemctl stop).
	StopService(ctx context.Context, policy ClusterPolicy) error
	// StartService starts the database service (systemctl start).
	StartService(ctx context.Context, policy ClusterPolicy) error
	// ServiceStatus reports whether the database service is active and the
	// database itself answers a probe.
	ServiceStatus(ctx context.Context, policy ClusterPolicy) (serviceRunning bool, databaseReachable bool, err error)
	// PowerOff shuts down the host (systemctl poweroff).
	PowerOff(ctx context.Context, policy ClusterPolicy) error
}

// LinuxPowerController implements PowerController with systemctl on the
// service name from the cluster policy (MySQLService). The database reachable
// probe reuses MySQLClientArguments so it always targets the same local
// instance the role controller talks to.
type LinuxPowerController struct {
	runner            CommandRunner
	snapshotPath      string
	snapshotDirectory bool
}

func NewLinuxPowerController(runner CommandRunner) *LinuxPowerController {
	controller, _ := NewLinuxPowerControllerWithSnapshotPath(runner, defaultPowerSnapshotDirectory)
	return controller
}

// NewLinuxPowerControllerWithSnapshotPath exists so tests and packaged
// installations can verify the durable-write contract without redirecting a
// signed request to an arbitrary path. The path is process configuration, not
// request data, and must be absolute.
func NewLinuxPowerControllerWithSnapshotPath(runner CommandRunner, snapshotPath string) (*LinuxPowerController, error) {
	cleaned := filepath.Clean(strings.TrimSpace(snapshotPath))
	if runner == nil {
		return nil, fmt.Errorf("power command runner is required")
	}
	if cleaned == "." || !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("power recovery snapshot path must be absolute")
	}
	return &LinuxPowerController{
		runner: runner, snapshotPath: cleaned,
		snapshotDirectory: !strings.HasSuffix(strings.ToLower(cleaned), ".json"),
	}, nil
}

func (controller *LinuxPowerController) PrepareRecoverySnapshot(_ context.Context, policy ClusterPolicy, snapshot model.PowerSnapshot) error {
	if err := validatePowerRecoverySnapshot(policy, snapshot); err != nil {
		return err
	}
	snapshot.LocalInstanceID = policy.InstanceID
	snapshot.LocalServiceName = dockerPowerServiceName(policy)
	contents, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode power recovery snapshot: %w", err)
	}
	contents = append(contents, '\n')
	targetPath := controller.snapshotPath
	if controller.snapshotDirectory {
		targetPath = filepath.Join(controller.snapshotPath, string(snapshot.ClusterID)+".json")
	}
	directory := filepath.Dir(targetPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create power recovery snapshot directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".cluster-topology.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary power recovery snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary power recovery snapshot: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write power recovery snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync power recovery snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close power recovery snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return fmt.Errorf("commit power recovery snapshot: %w", err)
	}
	committed = true
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func powerServiceName(policy ClusterPolicy) string {
	if policy.Engine == model.EnginePostgreSQL {
		return strings.TrimSpace(policy.PostgreSQLService)
	}
	return strings.TrimSpace(policy.MySQLService)
}

func validatePowerRecoverySnapshot(policy ClusterPolicy, snapshot model.PowerSnapshot) error {
	if !model.ValidResourceID(policy.ClusterID) || snapshot.ClusterID != policy.ClusterID {
		return fmt.Errorf("power recovery snapshot cluster identity does not match the Agent allowlist")
	}
	engine := policy.Engine
	if engine == "" {
		engine = model.EngineMySQL
	}
	if snapshot.Engine != engine {
		return fmt.Errorf("power recovery snapshot engine does not match the Agent allowlist")
	}
	if strings.TrimSpace(snapshot.ClusterName) == "" || snapshot.CapturedAt.IsZero() {
		return fmt.Errorf("power recovery snapshot metadata is incomplete")
	}
	instances := append(append([]model.PowerInstanceRef{}, snapshot.Replicas...), snapshot.Primary)
	seen := make(map[model.ResourceID]struct{}, len(instances))
	localFound := false
	for _, instance := range instances {
		if !model.ValidResourceID(instance.InstanceID) || instance.Port < 1 || instance.Port > 65535 ||
			(strings.TrimSpace(instance.Hostname) == "" && strings.TrimSpace(instance.IPAddress) == "") {
			return fmt.Errorf("power recovery snapshot contains an invalid instance")
		}
		if _, duplicate := seen[instance.InstanceID]; duplicate {
			return fmt.Errorf("power recovery snapshot contains duplicate instance identity")
		}
		seen[instance.InstanceID] = struct{}{}
		if instance.InstanceID == policy.InstanceID {
			localFound = true
		}
	}
	if !model.ValidResourceID(snapshot.Primary.InstanceID) {
		return fmt.Errorf("power recovery snapshot has no valid primary")
	}
	if !localFound {
		return fmt.Errorf("power recovery snapshot does not contain the local Agent instance")
	}
	return nil
}

func (controller *LinuxPowerController) mysqlService(policy ClusterPolicy) (string, error) {
	service := strings.TrimSpace(policy.MySQLService)
	if service == "" {
		return "", fmt.Errorf("MySQL service name is not configured")
	}
	return service, nil
}

func (controller *LinuxPowerController) StopService(ctx context.Context, policy ClusterPolicy) error {
	service, err := controller.mysqlService(policy)
	if err != nil {
		return err
	}
	if _, err := controller.runner.Run(ctx, "/usr/bin/systemctl", "stop", service); err != nil {
		return fmt.Errorf("stop MySQL service: %w", err)
	}
	return nil
}

func (controller *LinuxPowerController) StartService(ctx context.Context, policy ClusterPolicy) error {
	service, err := controller.mysqlService(policy)
	if err != nil {
		return err
	}
	if _, err := controller.runner.Run(ctx, "/usr/bin/systemctl", "start", service); err != nil {
		return fmt.Errorf("start MySQL service: %w", err)
	}
	return nil
}

func (controller *LinuxPowerController) ServiceStatus(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	service, err := controller.mysqlService(policy)
	if err != nil {
		return false, false, err
	}
	// systemctl is-active exits 0 only for active services.
	_, isActiveErr := controller.runner.Run(ctx, "/usr/bin/systemctl", "is-active", service)
	serviceRunning := isActiveErr == nil

	databaseReachable := false
	if binary := controller.mysqlBinary(policy); binary != "" && policy.MySQLDefaultsFile != "" {
		arguments := MySQLClientArguments(policy)
		arguments = append(arguments, "--execute", "SELECT 1")
		if _, pingErr := controller.runner.Run(ctx, binary, arguments...); pingErr == nil {
			databaseReachable = true
		}
	}
	return serviceRunning, databaseReachable, nil
}

func (controller *LinuxPowerController) mysqlBinary(policy ClusterPolicy) string {
	return strings.TrimSpace(policy.MySQLBinary)
}

func (controller *LinuxPowerController) PowerOff(ctx context.Context, _ ClusterPolicy) error {
	// Delay the actual poweroff long enough for the control plane to schedule
	// every peer and durably finish verify/audit/report. This matters when the
	// current Raft leader is itself one of the hosts being shut down.
	if _, err := controller.runner.Run(ctx, "/usr/bin/systemd-run",
		"--unit=clusterguard-node-poweroff", "--collect", "--on-active=10s",
		"/usr/bin/systemctl", "poweroff"); err != nil {
		return fmt.Errorf("power off host: %w", err)
	}
	return nil
}
