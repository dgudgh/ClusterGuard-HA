package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

// DockerMySQLRoleController controls only the local Swarm task selected by
// immutable ClusterGuard labels and an allowlisted service name. It never
// accepts a container ID or command from an API request.
type DockerMySQLRoleController struct {
	runner                CommandRunner
	dockerBinary          string
	dockerConfigDirectory string
	state                 *MySQLRoleController
}

func NewDockerMySQLRoleController(runner CommandRunner, dockerBinary, stateDirectory string, dockerConfigDirectory ...string) *DockerMySQLRoleController {
	configurationDirectory := ""
	if len(dockerConfigDirectory) > 0 {
		configurationDirectory = strings.TrimSpace(dockerConfigDirectory[0])
	}
	return &DockerMySQLRoleController{
		runner: runner, dockerBinary: dockerBinary, dockerConfigDirectory: configurationDirectory,
		state: NewMySQLRoleController(runner, "", stateDirectory),
	}
}

func (controller *DockerMySQLRoleController) runDocker(ctx context.Context, arguments ...string) ([]byte, error) {
	if controller.dockerConfigDirectory != "" {
		arguments = append([]string{"--config", controller.dockerConfigDirectory}, arguments...)
	}
	return controller.runner.Run(ctx, controller.dockerBinary, arguments...)
}

func (controller *DockerMySQLRoleController) runningContainer(ctx context.Context, policy ClusterPolicy) (string, bool, error) {
	arguments := []string{
		"ps",
		"--filter", "label=com.docker.swarm.service.name=" + policy.DockerSwarmService,
		"--filter", "label=clusterguard.cluster_id=" + string(policy.ClusterID),
		"--filter", "label=clusterguard.instance_id=" + string(policy.InstanceID),
		"--format", "{{.ID}}",
	}
	output, err := controller.runDocker(ctx, arguments...)
	if err != nil {
		return "", false, fmt.Errorf("inspect local Docker Swarm task: %w", err)
	}
	containers, err := dockerObjectIDs(output)
	if err != nil {
		return "", false, err
	}
	if len(containers) == 0 {
		return "", false, nil
	}
	if len(containers) != 1 {
		return "", false, fmt.Errorf("Docker workload identity is ambiguous: %d matching running containers", len(containers))
	}
	containerID := containers[0]
	if !safeDockerObjectID(containerID) {
		return "", false, fmt.Errorf("Docker returned an invalid container identity")
	}
	if policy.DockerSwarmServiceID != "" {
		output, err = controller.runDocker(ctx, "inspect", "--type", "container", "--format",
			`{{index .Config.Labels "com.docker.swarm.service.id"}}`, containerID)
		if err != nil {
			return "", false, fmt.Errorf("verify Docker Swarm service identity: %w", err)
		}
		if strings.TrimSpace(string(output)) != policy.DockerSwarmServiceID {
			return "", false, fmt.Errorf("Docker Swarm service identity does not match the Agent allowlist")
		}
	}
	return containerID, true, nil
}

func dockerObjectIDs(output []byte) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	identities := make([]string, 0, len(lines))
	for _, line := range lines {
		identity := strings.TrimSpace(line)
		if identity == "" {
			continue
		}
		if strings.ContainsAny(identity, " \t") || !safeDockerObjectID(identity) {
			return nil, fmt.Errorf("Docker returned unexpected workload identity output")
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

func safeDockerObjectID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func dockerMySQLClientArguments(policy ClusterPolicy) []string {
	arguments := []string{"--defaults-file=" + policy.MySQLDefaultsFile}
	return append(arguments, "--protocol=tcp", "--host=127.0.0.1", fmt.Sprintf("--port=%d", policy.MySQLPort), "--batch", "--skip-column-names")
}

func (controller *DockerMySQLRoleController) mysql(ctx context.Context, policy ClusterPolicy, statement string) ([]byte, error) {
	containerID, running, err := controller.runningContainer(ctx, policy)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, fmt.Errorf("Docker MySQL workload is not running on this host")
	}
	arguments := []string{"exec", containerID, policy.DockerMySQLBinary}
	arguments = append(arguments, dockerMySQLClientArguments(policy)...)
	arguments = append(arguments, "--execute", statement)
	output, err := controller.runDocker(ctx, arguments...)
	if err != nil {
		return nil, fmt.Errorf("execute allowlisted Docker MySQL command: %w", err)
	}
	return output, nil
}

func (controller *DockerMySQLRoleController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	output, err := controller.mysql(ctx, policy, "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only")
	if err != nil {
		return false, false, err
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return false, false, fmt.Errorf("unexpected Docker MySQL role status output")
	}
	readOnly, err := mysqlBoolean(fields[0])
	if err != nil {
		return false, false, err
	}
	superReadOnly, err := mysqlBoolean(fields[1])
	if err != nil {
		return false, false, err
	}
	return readOnly, superReadOnly, nil
}

func (controller *DockerMySQLRoleController) majorVersion(ctx context.Context, policy ClusterPolicy) (int, error) {
	output, err := controller.mysql(ctx, policy, "SELECT SUBSTRING_INDEX(VERSION(), '.', 1)")
	if err != nil {
		return 0, fmt.Errorf("inspect Docker MySQL major version: %w", err)
	}
	value := strings.TrimSpace(string(output))
	major, err := strconv.Atoi(value)
	if err != nil || major < 1 {
		return 0, fmt.Errorf("unexpected Docker MySQL major version %q", value)
	}
	return major, nil
}

func (controller *DockerMySQLRoleController) PersistReadOnly(ctx context.Context, policy ClusterPolicy, readOnly bool) error {
	if readOnly {
		if err := writeDockerRestartFence(policy.DockerFenceFile, true); err != nil {
			return err
		}
		if err := controller.state.persistState(policy, true); err != nil {
			return err
		}
	}
	major, err := controller.majorVersion(ctx, policy)
	if err != nil {
		return err
	}
	if major < 8 {
		return fmt.Errorf("Docker runtime role persistence requires MySQL 8 or newer")
	}
	persisted := "OFF"
	if readOnly {
		persisted = "ON"
	}
	if _, err := controller.mysql(ctx, policy, "SET PERSIST_ONLY super_read_only = "+persisted+"; SET PERSIST_ONLY read_only = "+persisted); err != nil {
		return fmt.Errorf("persist Docker MySQL restart role: %w", err)
	}
	if _, err := controller.mysql(ctx, policy, "SET GLOBAL super_read_only = "+persisted+"; SET GLOBAL read_only = "+persisted); err != nil {
		return fmt.Errorf("set Docker MySQL runtime role: %w", err)
	}
	if readOnly {
		return nil
	}
	if err := writeDockerRestartFence(policy.DockerFenceFile, false); err != nil {
		return err
	}
	return controller.state.persistState(policy, false)
}

func (controller *DockerMySQLRoleController) IsolationStatus(ctx context.Context, policy ClusterPolicy) (MySQLIsolationStatus, error) {
	status := MySQLIsolationStatus{}
	persisted, err := controller.state.persistedReadOnly(policy)
	if err != nil {
		return status, err
	}
	status.PersistedReadOnly = persisted
	readOnly, superReadOnly, err := readDockerRestartFence(policy.DockerFenceFile)
	if err != nil {
		return status, err
	}
	status.RestartReadOnly = readOnly && superReadOnly

	_, running, err := controller.runningContainer(ctx, policy)
	if err != nil {
		return status, err
	}
	status.ServiceRunning = running
	if !running {
		return status, nil
	}
	readOnly, superReadOnly, err = controller.Status(ctx, policy)
	if err != nil {
		return status, fmt.Errorf("running Docker MySQL role state is unavailable: %w", err)
	}
	status.DatabaseReachable = true
	status.ReadOnly = readOnly
	status.SuperReadOnly = superReadOnly
	return status, nil
}

func writeDockerRestartFence(path string, readOnly bool) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create Docker restart fence directory: %w", err)
	}
	// The directory is mounted read-only into the database container. mysqld
	// drops root before parsing option files, so it must be able to traverse the
	// host directory even though the files remain root-owned and immutable from
	// inside the container.
	if err := os.Chmod(directory, 0o755); err != nil {
		return fmt.Errorf("set Docker restart fence directory permissions: %w", err)
	}
	value := "OFF"
	if readOnly {
		value = "ON"
	}
	contents := []byte("# Managed by ClusterGuard HA.\n[mysqld]\nread_only=" + value + "\nsuper_read_only=" + value + "\n")
	temporary, err := os.CreateTemp(directory, ".docker-fence-*.tmp")
	if err != nil {
		return fmt.Errorf("create Docker restart fence: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit Docker restart fence: %w", err)
	}
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

func readDockerRestartFence(path string) (bool, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, false, fmt.Errorf("read Docker restart fence: %w", err)
	}
	defer file.Close()
	var readOnly, superReadOnly bool
	var readOnlyFound, superReadOnlyFound bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found {
			continue
		}
		parsed, parseErr := mysqlBoolean(value)
		if parseErr != nil {
			return false, false, fmt.Errorf("Docker restart fence has an invalid role value")
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "read_only", "read-only":
			readOnly, readOnlyFound = parsed, true
		case "super_read_only", "super-read-only":
			superReadOnly, superReadOnlyFound = parsed, true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, false, fmt.Errorf("scan Docker restart fence: %w", err)
	}
	if !readOnlyFound || !superReadOnlyFound {
		return false, false, fmt.Errorf("Docker restart fence does not define both MySQL read-only controls")
	}
	return readOnly, superReadOnly, nil
}

// RuntimeRoleController prevents runtime-specific behavior from leaking into
// the workflow. The policy chooses a preconfigured implementation; requests
// cannot choose a runtime or arbitrary command.
type RuntimeRoleController struct {
	linux  runtimeDurableRoleController
	docker runtimeDurableRoleController
}

type runtimeDurableRoleController interface {
	RoleController
	DurableRoleController
}

func NewRuntimeRoleController(linux, docker runtimeDurableRoleController) *RuntimeRoleController {
	return &RuntimeRoleController{linux: linux, docker: docker}
}

func (controller *RuntimeRoleController) selected(policy ClusterPolicy) (runtimeDurableRoleController, error) {
	switch policy.RuntimeKind {
	case "", model.RuntimeLinux:
		if controller.linux == nil {
			return nil, fmt.Errorf("Linux MySQL role controller is unavailable")
		}
		return controller.linux, nil
	case model.RuntimeDocker:
		if controller.docker == nil {
			return nil, fmt.Errorf("Docker MySQL role controller is unavailable")
		}
		return controller.docker, nil
	default:
		return nil, fmt.Errorf("runtime %q is unsupported by the host Agent", policy.RuntimeKind)
	}
}

func (controller *RuntimeRoleController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	selected, err := controller.selected(policy)
	if err != nil {
		return false, false, err
	}
	return selected.Status(ctx, policy)
}

func (controller *RuntimeRoleController) PersistReadOnly(ctx context.Context, policy ClusterPolicy, readOnly bool) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.PersistReadOnly(ctx, policy, readOnly)
}

func (controller *RuntimeRoleController) IsolationStatus(ctx context.Context, policy ClusterPolicy) (MySQLIsolationStatus, error) {
	selected, err := controller.selected(policy)
	if err != nil {
		return MySQLIsolationStatus{}, err
	}
	return selected.IsolationStatus(ctx, policy)
}
