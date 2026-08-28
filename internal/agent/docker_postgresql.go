package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

// DockerPostgreSQLController controls one fixed Docker Swarm service. Workload
// identity comes only from the signed Agent configuration and immutable labels;
// API callers cannot supply a container, image, service, path, or command.
type DockerPostgreSQLController struct {
	runner                CommandRunner
	dockerBinary          string
	dockerConfigDirectory string
	pollInterval          time.Duration
	pollAttempts          int
}

func NewDockerPostgreSQLController(runner CommandRunner, dockerBinary string, dockerConfigDirectory ...string) (*DockerPostgreSQLController, error) {
	dockerBinary = strings.TrimSpace(dockerBinary)
	if dockerBinary == "" {
		dockerBinary = "/usr/bin/docker"
	}
	if runner == nil {
		return nil, fmt.Errorf("Docker PostgreSQL command runner is required")
	}
	if !filepath.IsAbs(dockerBinary) || filepath.Base(dockerBinary) != "docker" {
		return nil, fmt.Errorf("Docker PostgreSQL controller requires an absolute docker path")
	}
	configurationDirectory := ""
	if len(dockerConfigDirectory) > 0 {
		configurationDirectory = strings.TrimSpace(dockerConfigDirectory[0])
		if configurationDirectory != "" && !filepath.IsAbs(configurationDirectory) {
			return nil, fmt.Errorf("Docker CLI configuration directory must be absolute")
		}
	}
	return &DockerPostgreSQLController{
		runner: runner, dockerBinary: dockerBinary, dockerConfigDirectory: configurationDirectory,
		pollInterval: time.Second, pollAttempts: 120,
	}, nil
}

func (controller *DockerPostgreSQLController) runDocker(ctx context.Context, arguments ...string) ([]byte, error) {
	if controller.dockerConfigDirectory != "" {
		arguments = append([]string{"--config", controller.dockerConfigDirectory}, arguments...)
	}
	return controller.runner.Run(ctx, controller.dockerBinary, arguments...)
}

func safeDockerServiceID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'z')) {
			return false
		}
	}
	return true
}

func (controller *DockerPostgreSQLController) verifyService(ctx context.Context, policy ClusterPolicy) (string, error) {
	output, err := controller.runDocker(ctx, "service", "inspect", "--format", "{{.ID}}", policy.DockerSwarmService)
	if err != nil {
		return "", fmt.Errorf("inspect allowlisted Docker Swarm service: %w", err)
	}
	serviceID := strings.TrimSpace(string(output))
	if strings.ContainsAny(serviceID, " \t\r\n") || !safeDockerServiceID(serviceID) {
		return "", fmt.Errorf("Docker returned an invalid Swarm service identity")
	}
	if policy.DockerSwarmServiceID != "" && serviceID != policy.DockerSwarmServiceID {
		return "", fmt.Errorf("Docker Swarm service identity does not match the Agent allowlist")
	}
	return serviceID, nil
}

func (controller *DockerPostgreSQLController) runningContainer(ctx context.Context, policy ClusterPolicy) (string, bool, error) {
	serviceID, err := controller.verifyService(ctx, policy)
	if err != nil {
		return "", false, err
	}
	output, err := controller.runDocker(ctx,
		"ps",
		"--filter", "label=com.docker.swarm.service.name="+policy.DockerSwarmService,
		"--filter", "label=clusterguard.cluster_id="+string(policy.ClusterID),
		"--filter", "label=clusterguard.instance_id="+string(policy.InstanceID),
		"--format", "{{.ID}}",
	)
	if err != nil {
		return "", false, fmt.Errorf("inspect local Docker PostgreSQL task: %w", err)
	}
	containers, err := dockerObjectIDs(output)
	if err != nil {
		return "", false, err
	}
	if len(containers) == 0 {
		return "", false, nil
	}
	if len(containers) != 1 {
		return "", false, fmt.Errorf("Docker PostgreSQL workload identity is ambiguous: %d matching running containers", len(containers))
	}
	containerID := containers[0]
	output, err = controller.runDocker(ctx, "inspect", "--type", "container", "--format",
		`{{index .Config.Labels "com.docker.swarm.service.id"}}`, containerID)
	if err != nil {
		return "", false, fmt.Errorf("verify Docker PostgreSQL service identity: %w", err)
	}
	if strings.TrimSpace(string(output)) != serviceID {
		return "", false, fmt.Errorf("Docker PostgreSQL container is outside the allowlisted Swarm service")
	}
	return containerID, true, nil
}

func safeDockerImageReference(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._/@:-", character)) {
			return false
		}
	}
	return true
}

func (controller *DockerPostgreSQLController) serviceImage(ctx context.Context, policy ClusterPolicy) (string, error) {
	if _, err := controller.verifyService(ctx, policy); err != nil {
		return "", err
	}
	output, err := controller.runDocker(ctx, "service", "inspect", "--format", "{{.Spec.TaskTemplate.ContainerSpec.Image}}", policy.DockerSwarmService)
	if err != nil {
		return "", fmt.Errorf("inspect Docker PostgreSQL service image: %w", err)
	}
	image := strings.TrimSpace(string(output))
	if !safeDockerImageReference(image) {
		return "", fmt.Errorf("Docker PostgreSQL service returned an unsafe image reference")
	}
	return image, nil
}

func (controller *DockerPostgreSQLController) psql(ctx context.Context, policy ClusterPolicy, query string) ([]byte, error) {
	containerID, running, err := controller.runningContainer(ctx, policy)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, fmt.Errorf("Docker PostgreSQL workload is not running on this host")
	}
	arguments := []string{
		"exec", "--user", policy.PostgreSQLUser,
		"--env", "PGPASSFILE=" + policy.PostgreSQLPassfile,
		"--env", "PGAPPNAME=clusterguard-agent",
		"--env", "PGCONNECT_TIMEOUT=5",
		containerID,
		filepath.Join(policy.PostgreSQLBinaryDirectory, "psql"),
		"--no-password", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align",
		"--set=ON_ERROR_STOP=1", "--host", "127.0.0.1",
		"--port", strconv.Itoa(policy.DockerPostgreSQLPort),
		"--username", policy.PostgreSQLUser, "--dbname", policy.PostgreSQLDatabase,
		"--command", query,
	}
	output, err := controller.runDocker(ctx, arguments...)
	if err != nil {
		return nil, fmt.Errorf("execute allowlisted Docker PostgreSQL command: %w", err)
	}
	return output, nil
}

func (controller *DockerPostgreSQLController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	_, running, err := controller.runningContainer(ctx, policy)
	if err != nil || !running {
		return running, false, err
	}
	output, err := controller.psql(ctx, policy, "SELECT pg_is_in_recovery()")
	if err != nil {
		return true, false, fmt.Errorf("inspect Docker PostgreSQL recovery state: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(string(output))) {
	case "t", "true", "on", "1":
		return true, true, nil
	case "f", "false", "off", "0":
		return true, false, nil
	default:
		return true, false, fmt.Errorf("unexpected Docker pg_is_in_recovery output")
	}
}

func (controller *DockerPostgreSQLController) StandbyIntent(policy ClusterPolicy) (bool, error) {
	dataDirectory, err := safePostgreSQLDataDirectory(policy.PostgreSQLDataDirectory)
	if err != nil {
		return false, err
	}
	marker := filepath.Join(dataDirectory, "standby.signal")
	info, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Docker PostgreSQL standby signal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("Docker PostgreSQL standby signal must be a regular file")
	}
	return true, nil
}

func (controller *DockerPostgreSQLController) waitFor(ctx context.Context, timeoutMessage string, ready func() (bool, error)) error {
	attempts := controller.pollAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if ok, err := ready(); ok {
			return nil
		} else if err != nil {
			lastErr = err
		}
		if attempt+1 == attempts || controller.pollInterval <= 0 {
			continue
		}
		timer := time.NewTimer(controller.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("%s: %w", timeoutMessage, ctx.Err())
		case <-timer.C:
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%s: %w", timeoutMessage, lastErr)
	}
	return fmt.Errorf("%s", timeoutMessage)
}

func (controller *DockerPostgreSQLController) scale(ctx context.Context, policy ClusterPolicy, replicas int) error {
	if _, err := controller.verifyService(ctx, policy); err != nil {
		return err
	}
	if _, err := controller.runDocker(ctx, "service", "scale", "--detach=true", fmt.Sprintf("%s=%d", policy.DockerSwarmService, replicas)); err != nil {
		return fmt.Errorf("scale allowlisted Docker PostgreSQL service to %d: %w", replicas, err)
	}
	return nil
}

func (controller *DockerPostgreSQLController) Stop(ctx context.Context, policy ClusterPolicy) error {
	if err := controller.scale(ctx, policy, 0); err != nil {
		return err
	}
	return controller.waitFor(ctx, "Docker PostgreSQL service did not stop within the allowed interval", func() (bool, error) {
		_, running, err := controller.runningContainer(ctx, policy)
		return err == nil && !running, err
	})
}

func (controller *DockerPostgreSQLController) Start(ctx context.Context, policy ClusterPolicy) error {
	if err := controller.scale(ctx, policy, 1); err != nil {
		return err
	}
	return controller.waitFor(ctx, "Docker PostgreSQL service did not become ready within the allowed interval", func() (bool, error) {
		running, _, err := controller.Status(ctx, policy)
		return err == nil && running, err
	})
}

func (controller *DockerPostgreSQLController) Promote(ctx context.Context, policy ClusterPolicy) error {
	running, inRecovery, err := controller.Status(ctx, policy)
	if err != nil {
		return err
	}
	if !running || !inRecovery {
		return fmt.Errorf("Docker PostgreSQL promotion requires an active standby")
	}
	containerID, _, err := controller.runningContainer(ctx, policy)
	if err != nil {
		return err
	}
	if _, err := controller.runDocker(ctx, "exec", "--user", policy.PostgreSQLUser, containerID,
		filepath.Join(policy.PostgreSQLBinaryDirectory, "pg_ctl"), "-D", policy.DockerPostgreSQLDataDirectory, "-w", "-t", "60", "promote"); err != nil {
		return fmt.Errorf("promote Docker PostgreSQL standby: %w", err)
	}
	if err := controller.waitFor(ctx, "Docker PostgreSQL standby did not become an active primary", func() (bool, error) {
		running, inRecovery, statusErr := controller.Status(ctx, policy)
		return statusErr == nil && running && !inRecovery, statusErr
	}); err != nil {
		return err
	}
	for _, statement := range []string{
		"ALTER SYSTEM RESET primary_conninfo",
		"ALTER SYSTEM RESET primary_slot_name",
		"ALTER SYSTEM RESET clusterguard.primary_node_id",
	} {
		if _, err := controller.psql(ctx, policy, statement); err != nil {
			return fmt.Errorf("clear Docker PostgreSQL upstream configuration after promotion: %w", err)
		}
	}
	if _, err := controller.psql(ctx, policy, "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reload Docker PostgreSQL configuration after promotion: %w", err)
	}
	settings, err := controller.psql(ctx, policy,
		"SELECT COALESCE(current_setting('primary_conninfo', true), '') || E'\\t' || "+
			"COALESCE(current_setting('primary_slot_name', true), '') || E'\\t' || "+
			"COALESCE(current_setting('clusterguard.primary_node_id', true), '')",
	)
	if err != nil {
		return fmt.Errorf("verify Docker PostgreSQL upstream configuration after promotion: %w", err)
	}
	parts := strings.Split(strings.TrimRight(string(settings), "\r\n"), "\t")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" || strings.TrimSpace(parts[1]) != "" || strings.TrimSpace(parts[2]) != "" {
		return fmt.Errorf("Docker PostgreSQL promotion left stale upstream configuration")
	}
	return nil
}

func (controller *DockerPostgreSQLController) Repoint(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	connection, err := postgresqlSourceURI(policy, source)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		"ALTER SYSTEM SET primary_conninfo = " + postgresqlSQLLiteral(connection),
		"ALTER SYSTEM RESET primary_slot_name",
		"ALTER SYSTEM SET clusterguard.primary_node_id = " + postgresqlSQLLiteral(string(source.NodeID)),
	} {
		if _, err := controller.psql(ctx, policy, statement); err != nil {
			return fmt.Errorf("update Docker PostgreSQL primary connection: %w", err)
		}
	}
	if _, err := controller.psql(ctx, policy, "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reload Docker PostgreSQL primary connection: %w", err)
	}
	return controller.waitForPostgreSQLSource(ctx, policy, source, connection)
}

func (controller *DockerPostgreSQLController) waitForPostgreSQLSource(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer, connection string) error {
	return controller.waitFor(ctx, "Docker PostgreSQL standby did not converge on the approved replication source", func() (bool, error) {
		running, inRecovery, err := controller.Status(ctx, policy)
		if err != nil {
			return false, err
		}
		if !running || !inRecovery {
			return false, fmt.Errorf("Docker PostgreSQL target is not an active standby")
		}
		output, err := controller.psql(ctx, policy,
			"SELECT COALESCE(current_setting('clusterguard.primary_node_id', true), '') || E'\\t' || "+
				"COALESCE(current_setting('primary_conninfo', true), '') || E'\\t' || "+
				"COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '') || E'\\t' || "+
				"COALESCE((SELECT sender_host FROM pg_stat_wal_receiver LIMIT 1), '') || E'\\t' || "+
				"COALESCE((SELECT sender_port::text FROM pg_stat_wal_receiver LIMIT 1), '')",
		)
		if err != nil {
			return false, fmt.Errorf("verify Docker PostgreSQL replication source: %w", err)
		}
		parts := strings.Split(strings.TrimRight(string(output), "\r\n"), "\t")
		sourceHost := strings.TrimSpace(source.IPAddress)
		if sourceHost == "" {
			sourceHost = strings.TrimSpace(source.Hostname)
		}
		if len(parts) != 5 || parts[0] != string(source.NodeID) || parts[1] != connection ||
			!strings.EqualFold(parts[2], "streaming") || parts[3] != sourceHost || parts[4] != strconv.Itoa(source.Port) {
			return false, fmt.Errorf("Docker PostgreSQL replication source is not streaming from the approved source")
		}
		return true, nil
	})
}

func safeDockerPostgreSQLPassfile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Docker PostgreSQL passfile: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Docker PostgreSQL passfile must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("Docker PostgreSQL passfile must not be accessible by group or others")
	}
	return nil
}

func safeDockerPostgreSQLContainerDataDirectory(path string) (string, error) {
	directory := filepath.Clean(strings.TrimSpace(path))
	if directory == "." || directory == string(filepath.Separator) || !filepath.IsAbs(directory) || filepath.Dir(directory) == string(filepath.Separator) {
		return "", fmt.Errorf("Docker PostgreSQL container data directory must be a safe absolute path")
	}
	return directory, nil
}

func (controller *DockerPostgreSQLController) runRecoveryTool(ctx context.Context, policy ClusterPolicy, image, tool string, mountSource, mountTarget string, arguments ...string) ([]byte, error) {
	if err := safeDockerPostgreSQLPassfile(policy.PostgreSQLPassfile); err != nil {
		return nil, err
	}
	dockerArguments := []string{
		"run", "--rm", "--network", "host", "--user", policy.PostgreSQLUser,
		"--env", "PGPASSFILE=" + policy.PostgreSQLPassfile,
		"--volume", mountSource + ":" + mountTarget,
		"--volume", policy.PostgreSQLPassfile + ":" + policy.PostgreSQLPassfile + ":ro",
		"--entrypoint", filepath.Join(policy.PostgreSQLBinaryDirectory, tool),
		image,
	}
	output, err := controller.runDocker(ctx, append(dockerArguments, arguments...)...)
	if err != nil {
		return output, fmt.Errorf("run allowlisted Docker PostgreSQL %s: %w", tool, err)
	}
	return output, nil
}

func (controller *DockerPostgreSQLController) Rewind(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	hostDataDirectory, err := safePostgreSQLDataDirectory(policy.PostgreSQLDataDirectory)
	if err != nil {
		return err
	}
	containerDataDirectory, err := safeDockerPostgreSQLContainerDataDirectory(policy.DockerPostgreSQLDataDirectory)
	if err != nil {
		return err
	}
	connection, err := postgresqlSourceURI(policy, source)
	if err != nil {
		return err
	}
	rewindConnection, err := postgresqlSourceURIForUser(policy, source, policy.PostgreSQLUser)
	if err != nil {
		return err
	}
	if err := controller.Stop(ctx, policy); err != nil {
		return err
	}
	image, err := controller.serviceImage(ctx, policy)
	if err != nil {
		return err
	}
	if _, err := controller.runRecoveryTool(ctx, policy, image, "pg_rewind", hostDataDirectory, containerDataDirectory,
		"--target-pgdata", containerDataDirectory, "--source-server", rewindConnection,
		"--write-recovery-conf", "--progress"); err != nil {
		return fmt.Errorf("rewind Docker PostgreSQL former primary: %w", err)
	}
	if err := appendPostgreSQLRecoveryIdentity(filepath.Join(hostDataDirectory, "postgresql.auto.conf"), connection, policy, source); err != nil {
		return err
	}
	if err := controller.Start(ctx, policy); err != nil {
		return err
	}
	return controller.waitForPostgreSQLSource(ctx, policy, source, connection)
}

func (controller *DockerPostgreSQLController) BaseBackup(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	dataDirectory, err := safePostgreSQLDataDirectory(policy.PostgreSQLDataDirectory)
	if err != nil {
		return err
	}
	connection, err := postgresqlSourceURI(policy, source)
	if err != nil {
		return err
	}
	stageDirectory := dataDirectory + ".clusterguard-stage"
	backupDirectory := dataDirectory + ".clusterguard-backup"
	if exists, err := postgreSQLPathExists(backupDirectory); err != nil {
		return fmt.Errorf("inspect Docker PostgreSQL backup quarantine: %w", err)
	} else if exists {
		return fmt.Errorf("Docker PostgreSQL backup quarantine already exists at %s", backupDirectory)
	}
	if err := os.RemoveAll(stageDirectory); err != nil {
		return fmt.Errorf("remove stale Docker PostgreSQL staging directory: %w", err)
	}
	if err := controller.Stop(ctx, policy); err != nil {
		return err
	}
	image, err := controller.serviceImage(ctx, policy)
	if err != nil {
		return err
	}
	parent := filepath.Dir(dataDirectory)
	if _, err := controller.runRecoveryTool(ctx, policy, image, "pg_basebackup", parent, parent,
		"--pgdata", stageDirectory, "--dbname", connection, "--write-recovery-conf",
		"--checkpoint", "fast", "--wal-method", "stream", "--progress", "--no-password"); err != nil {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("take Docker PostgreSQL base backup: %w", err)
	}
	if info, err := os.Stat(filepath.Join(stageDirectory, "PG_VERSION")); err != nil || info.IsDir() {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("Docker PostgreSQL base backup did not produce a valid PG_VERSION file")
	}
	if err := appendPostgreSQLRecoveryIdentity(filepath.Join(stageDirectory, "postgresql.auto.conf"), connection, policy, source); err != nil {
		_ = os.RemoveAll(stageDirectory)
		return err
	}

	hadOriginal, err := postgreSQLPathExists(dataDirectory)
	if err != nil {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("inspect original Docker PostgreSQL data directory: %w", err)
	}
	if hadOriginal {
		if err := os.Rename(dataDirectory, backupDirectory); err != nil {
			_ = os.RemoveAll(stageDirectory)
			return fmt.Errorf("quarantine original Docker PostgreSQL data directory: %w", err)
		}
	}
	if err := os.Rename(stageDirectory, dataDirectory); err != nil {
		if hadOriginal {
			_ = os.Rename(backupDirectory, dataDirectory)
		}
		return fmt.Errorf("activate synchronized Docker PostgreSQL data directory: %w", err)
	}

	startErr := controller.Start(ctx, policy)
	var sourceErr error
	if startErr == nil {
		sourceErr = controller.waitForPostgreSQLSource(ctx, policy, source, connection)
	}
	if startErr != nil || sourceErr != nil {
		_ = controller.Stop(ctx, policy)
		restoreErr := restorePostgreSQLDataDirectory(dataDirectory, stageDirectory, backupDirectory, hadOriginal)
		if restoreErr != nil {
			return fmt.Errorf("Docker PostgreSQL base backup verification failed and rollback failed: start=%v source=%v rollback=%v", startErr, sourceErr, restoreErr)
		}
		return fmt.Errorf("Docker PostgreSQL base backup verification failed; original data restored and service left stopped: start=%v source=%v", startErr, sourceErr)
	}
	if hadOriginal {
		if err := os.RemoveAll(backupDirectory); err != nil {
			return fmt.Errorf("remove verified Docker PostgreSQL backup quarantine: %w", err)
		}
	}
	return nil
}

// RuntimePostgreSQLController keeps runtime selection in signed Agent policy.
type RuntimePostgreSQLController struct {
	linux  PostgreSQLController
	docker PostgreSQLController
}

func NewRuntimePostgreSQLController(linux, docker PostgreSQLController) *RuntimePostgreSQLController {
	return &RuntimePostgreSQLController{linux: linux, docker: docker}
}

func (controller *RuntimePostgreSQLController) selected(policy ClusterPolicy) (PostgreSQLController, error) {
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
	return nil, fmt.Errorf("runtime %q has no PostgreSQL controller", policy.RuntimeKind)
}

func (controller *RuntimePostgreSQLController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	selected, err := controller.selected(policy)
	if err != nil {
		return false, false, err
	}
	return selected.Status(ctx, policy)
}

func (controller *RuntimePostgreSQLController) Stop(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.Stop(ctx, policy)
}

func (controller *RuntimePostgreSQLController) Start(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.Start(ctx, policy)
}

func (controller *RuntimePostgreSQLController) Promote(ctx context.Context, policy ClusterPolicy) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.Promote(ctx, policy)
}

func (controller *RuntimePostgreSQLController) Repoint(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.Repoint(ctx, policy, source)
}

func (controller *RuntimePostgreSQLController) Rewind(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.Rewind(ctx, policy, source)
}

func (controller *RuntimePostgreSQLController) BaseBackup(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	selected, err := controller.selected(policy)
	if err != nil {
		return err
	}
	return selected.BaseBackup(ctx, policy, source)
}

func (controller *RuntimePostgreSQLController) StandbyIntent(policy ClusterPolicy) (bool, error) {
	selected, err := controller.selected(policy)
	if err != nil {
		return false, err
	}
	inspector, ok := selected.(PostgreSQLStandbyIntentInspector)
	if !ok {
		return false, fmt.Errorf("runtime %q cannot inspect PostgreSQL standby intent", policy.RuntimeKind)
	}
	return inspector.StandbyIntent(policy)
}

var _ PostgreSQLController = (*DockerPostgreSQLController)(nil)
var _ PostgreSQLStandbyIntentInspector = (*DockerPostgreSQLController)(nil)
var _ PostgreSQLController = (*RuntimePostgreSQLController)(nil)
var _ PostgreSQLStandbyIntentInspector = (*RuntimePostgreSQLController)(nil)
