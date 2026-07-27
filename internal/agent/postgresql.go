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

const (
	defaultPostgreSQLSystemctlBinary = "/usr/bin/systemctl"
	defaultPostgreSQLRunuserBinary   = "/usr/bin/setpriv"
)

// PostgreSQLLocalController exposes only the finite PostgreSQL command set
// accepted by the signed agent protocol. It never invokes a shell.
type PostgreSQLLocalController struct {
	runner          CommandRunner
	systemctlBinary string
	runuserBinary   string
	pollInterval    time.Duration
	pollAttempts    int
}

func NewPostgreSQLController(runner CommandRunner, systemctlBinary, runuserBinary string) (*PostgreSQLLocalController, error) {
	systemctlBinary = strings.TrimSpace(systemctlBinary)
	runuserBinary = strings.TrimSpace(runuserBinary)
	if runner == nil {
		return nil, fmt.Errorf("PostgreSQL command runner is required")
	}
	if !filepath.IsAbs(systemctlBinary) || !filepath.IsAbs(runuserBinary) {
		return nil, fmt.Errorf("PostgreSQL control executables must use absolute paths")
	}
	return &PostgreSQLLocalController{
		runner: runner, systemctlBinary: systemctlBinary, runuserBinary: runuserBinary,
		pollInterval: time.Second, pollAttempts: 60,
	}, nil
}

func NewDefaultPostgreSQLController(runner CommandRunner) (*PostgreSQLLocalController, error) {
	return NewPostgreSQLController(runner, defaultPostgreSQLSystemctlBinary, defaultPostgreSQLRunuserBinary)
}

func (controller *PostgreSQLLocalController) runAsPostgreSQLUser(ctx context.Context, policy ClusterPolicy, command string, arguments ...string) ([]byte, error) {
	prefix := []string{"-u", policy.PostgreSQLUser, "--", command}
	if filepath.Base(controller.runuserBinary) == "setpriv" {
		prefix = []string{"--reuid", policy.PostgreSQLUser, "--regid", policy.PostgreSQLUser, "--init-groups", "--", command}
	}
	return controller.runner.Run(ctx, controller.runuserBinary, append(prefix, arguments...)...)
}

func (controller *PostgreSQLLocalController) serviceState(ctx context.Context, policy ClusterPolicy) (string, error) {
	output, err := controller.runner.Run(ctx, controller.systemctlBinary, "show", "--property=ActiveState", "--value", policy.PostgreSQLService)
	if err != nil {
		return "", fmt.Errorf("inspect PostgreSQL service: %w", err)
	}
	state := strings.ToLower(strings.TrimSpace(string(output)))
	switch state {
	case "active", "inactive", "failed", "dead", "activating", "deactivating", "reloading":
		return state, nil
	default:
		return "", fmt.Errorf("unexpected PostgreSQL service state %q", state)
	}
}

func (controller *PostgreSQLLocalController) psql(ctx context.Context, policy ClusterPolicy, query string) ([]byte, error) {
	arguments := []string{
		"PGPASSFILE=" + policy.PostgreSQLPassfile,
		"PGAPPNAME=clusterguard-agent",
		"PGCONNECT_TIMEOUT=5",
		filepath.Join(policy.PostgreSQLBinaryDirectory, "psql"),
		"--no-password", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align",
		"--set=ON_ERROR_STOP=1",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(policy.PostgreSQLPort),
		"--username", policy.PostgreSQLUser,
		"--dbname", policy.PostgreSQLDatabase,
		"--command", query,
	}
	return controller.runAsPostgreSQLUser(ctx, policy, "/usr/bin/env", arguments...)
}

func (controller *PostgreSQLLocalController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	state, err := controller.serviceState(ctx, policy)
	if err != nil {
		return false, false, err
	}
	if state != "active" {
		return false, false, nil
	}
	output, err := controller.psql(ctx, policy, "SELECT pg_is_in_recovery()")
	if err != nil {
		return true, false, fmt.Errorf("inspect PostgreSQL recovery state: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(string(output))) {
	case "t", "true", "on", "1":
		return true, true, nil
	case "f", "false", "off", "0":
		return true, false, nil
	default:
		return true, false, fmt.Errorf("unexpected pg_is_in_recovery output")
	}
}

func (controller *PostgreSQLLocalController) Stop(ctx context.Context, policy ClusterPolicy) error {
	if _, err := controller.runner.Run(ctx, controller.systemctlBinary, "stop", policy.PostgreSQLService); err != nil {
		return fmt.Errorf("stop PostgreSQL service: %w", err)
	}
	return controller.waitFor(ctx, "PostgreSQL service did not stop within the allowed interval", func() (bool, error) {
		state, err := controller.serviceState(ctx, policy)
		return err == nil && state != "active", err
	})
}

func (controller *PostgreSQLLocalController) Start(ctx context.Context, policy ClusterPolicy) error {
	if _, err := controller.runner.Run(ctx, controller.systemctlBinary, "start", policy.PostgreSQLService); err != nil {
		return fmt.Errorf("start PostgreSQL service: %w", err)
	}
	return controller.waitFor(ctx, "PostgreSQL service did not become ready within the allowed interval", func() (bool, error) {
		running, _, err := controller.Status(ctx, policy)
		return err == nil && running, err
	})
}

func (controller *PostgreSQLLocalController) waitFor(ctx context.Context, timeoutMessage string, ready func() (bool, error)) error {
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

func (controller *PostgreSQLLocalController) Promote(ctx context.Context, policy ClusterPolicy) error {
	running, inRecovery, err := controller.Status(ctx, policy)
	if err != nil {
		return err
	}
	if !running || !inRecovery {
		return fmt.Errorf("PostgreSQL promotion requires an active standby")
	}
	if _, err := controller.runAsPostgreSQLUser(ctx, policy, filepath.Join(policy.PostgreSQLBinaryDirectory, "pg_ctl"),
		"-D", policy.PostgreSQLDataDirectory, "-w", "-t", "60", "promote",
	); err != nil {
		return fmt.Errorf("promote PostgreSQL standby: %w", err)
	}
	if err := controller.waitFor(ctx, "PostgreSQL standby did not become an active primary", func() (bool, error) {
		running, inRecovery, statusErr := controller.Status(ctx, policy)
		return statusErr == nil && running && !inRecovery, statusErr
	}); err != nil {
		return err
	}
	if _, err := controller.psql(ctx, policy, "ALTER SYSTEM RESET primary_conninfo"); err != nil {
		return fmt.Errorf("clear PostgreSQL upstream connection after promotion: %w", err)
	}
	if _, err := controller.psql(ctx, policy, "ALTER SYSTEM RESET clusterguard.primary_node_id"); err != nil {
		return fmt.Errorf("clear PostgreSQL upstream identity after promotion: %w", err)
	}
	if _, err := controller.psql(ctx, policy, "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reload PostgreSQL configuration after promotion: %w", err)
	}
	settings, err := controller.psql(ctx, policy,
		"SELECT COALESCE(current_setting('primary_conninfo', true), '') || E'\\t' || COALESCE(current_setting('clusterguard.primary_node_id', true), '')",
	)
	if err != nil {
		return fmt.Errorf("verify PostgreSQL upstream configuration after promotion: %w", err)
	}
	parts := strings.SplitN(strings.TrimRight(string(settings), "\r\n"), "\t", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) != "" || strings.TrimSpace(parts[1]) != "" {
		return fmt.Errorf("PostgreSQL promotion left stale upstream configuration")
	}
	return nil
}

func postgresqlSourceURI(policy ClusterPolicy, source PostgreSQLPeer) (string, error) {
	return postgresqlSourceConninfoForUser(policy, source, policy.PostgreSQLReplicationUser)
}

func postgresqlSourceURIForUser(policy ClusterPolicy, source PostgreSQLPeer, username string) (string, error) {
	return postgresqlSourceConninfoForUser(policy, source, username)
}

func postgresqlSourceConninfoForUser(policy ClusterPolicy, source PostgreSQLPeer, username string) (string, error) {
	host := strings.TrimSpace(source.IPAddress)
	if host == "" {
		host = strings.TrimSpace(source.Hostname)
	}
	username = strings.TrimSpace(username)
	if host == "" || source.Port < 1 || source.Port > 65535 ||
		!model.ValidResourceID(policy.InstanceID) || !model.ValidResourceID(source.InstanceID) || source.InstanceID == policy.InstanceID ||
		!model.ValidResourceID(policy.PostgreSQLNodeID) || !model.ValidResourceID(source.NodeID) || source.NodeID == policy.PostgreSQLNodeID ||
		username == "" {
		return "", fmt.Errorf("PostgreSQL source connection is incomplete")
	}
	database := strings.TrimSpace(policy.PostgreSQLDatabase)
	if database == "" {
		database = "postgres"
	}
	values := []struct {
		key   string
		value string
	}{
		{key: "host", value: host},
		{key: "port", value: strconv.Itoa(source.Port)},
		{key: "user", value: username},
		{key: "dbname", value: database},
		{key: "passfile", value: policy.PostgreSQLPassfile},
		{key: "application_name", value: string(policy.PostgreSQLNodeID)},
		{key: "connect_timeout", value: "5"},
	}
	parts := make([]string, 0, len(values))
	for _, item := range values {
		parts = append(parts, item.key+"="+postgresqlConninfoValue(item.value))
	}
	return strings.Join(parts, " "), nil
}

func postgresqlConninfoValue(value string) string {
	value = strings.TrimSpace(value)
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

func postgresqlSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (controller *PostgreSQLLocalController) Repoint(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	connection, err := postgresqlSourceURI(policy, source)
	if err != nil {
		return err
	}
	if _, err := controller.psql(ctx, policy, "ALTER SYSTEM SET primary_conninfo = "+postgresqlSQLLiteral(connection)); err != nil {
		return fmt.Errorf("update PostgreSQL primary connection: %w", err)
	}
	if _, err := controller.psql(ctx, policy, "ALTER SYSTEM SET clusterguard.primary_node_id = "+postgresqlSQLLiteral(string(source.NodeID))); err != nil {
		return fmt.Errorf("update PostgreSQL primary node identity: %w", err)
	}
	if _, err := controller.psql(ctx, policy, "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reload PostgreSQL primary connection: %w", err)
	}
	return controller.waitForPostgreSQLSource(ctx, policy, source, connection)
}

func (controller *PostgreSQLLocalController) Rewind(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
	dataDirectory, err := safePostgreSQLDataDirectory(policy.PostgreSQLDataDirectory)
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
	if _, err := controller.runAsPostgreSQLUser(ctx, policy, filepath.Join(policy.PostgreSQLBinaryDirectory, "pg_rewind"),
		"--target-pgdata", dataDirectory,
		"--source-server", rewindConnection,
		"--write-recovery-conf", "--progress",
	); err != nil {
		return fmt.Errorf("rewind PostgreSQL former primary: %w", err)
	}
	if err := appendPostgreSQLRecoveryIdentity(filepath.Join(dataDirectory, "postgresql.auto.conf"), connection, policy, source); err != nil {
		return err
	}
	if err := controller.Start(ctx, policy); err != nil {
		return err
	}
	return controller.waitForPostgreSQLSource(ctx, policy, source, connection)
}

func (controller *PostgreSQLLocalController) waitForPostgreSQLSource(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer, connection string) error {
	return controller.waitFor(ctx, "PostgreSQL standby did not converge on the approved replication source", func() (bool, error) {
		running, inRecovery, err := controller.Status(ctx, policy)
		if err != nil {
			return false, err
		}
		if !running || !inRecovery {
			return false, fmt.Errorf("PostgreSQL target is not an active standby")
		}
		if err := controller.verifyPostgreSQLSource(ctx, policy, source, connection); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (controller *PostgreSQLLocalController) verifyPostgreSQLSource(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer, connection string) error {
	output, err := controller.psql(ctx, policy,
		"SELECT COALESCE(current_setting('clusterguard.primary_node_id', true), '') || E'\\t' || "+
			"COALESCE(current_setting('primary_conninfo', true), '') || E'\\t' || "+
			"COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '') || E'\\t' || "+
			"COALESCE((SELECT sender_host FROM pg_stat_wal_receiver LIMIT 1), '') || E'\\t' || "+
			"COALESCE((SELECT sender_port::text FROM pg_stat_wal_receiver LIMIT 1), '')",
	)
	if err != nil {
		return fmt.Errorf("verify PostgreSQL replication source: %w", err)
	}
	parts := strings.Split(strings.TrimRight(string(output), "\r\n"), "\t")
	sourceHost := strings.TrimSpace(source.IPAddress)
	if sourceHost == "" {
		sourceHost = strings.TrimSpace(source.Hostname)
	}
	if len(parts) != 5 ||
		parts[0] != string(source.NodeID) ||
		parts[1] != connection ||
		!strings.EqualFold(parts[2], "streaming") ||
		parts[3] != sourceHost ||
		parts[4] != strconv.Itoa(source.Port) {
		return fmt.Errorf("PostgreSQL replication source is not streaming from the approved source")
	}
	return nil
}

func safePostgreSQLDataDirectory(value string) (string, error) {
	directory := filepath.Clean(strings.TrimSpace(value))
	if directory == "." || directory == string(filepath.Separator) || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("PostgreSQL data directory must be a safe absolute path")
	}
	if filepath.Dir(directory) == string(filepath.Separator) {
		return "", fmt.Errorf("PostgreSQL data directory cannot be a filesystem top-level directory")
	}
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("PostgreSQL data directory cannot be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect PostgreSQL data directory: %w", err)
	}
	return directory, nil
}

func appendPostgreSQLRecoveryIdentity(path, connection string, policy ClusterPolicy, source PostgreSQLPeer) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("PostgreSQL recovery configuration must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect PostgreSQL recovery configuration: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open PostgreSQL recovery configuration: %w", err)
	}
	configuration := "\n# Managed by ClusterGuard HA.\n" +
		"clusterguard.node_id = " + postgresqlSQLLiteral(string(policy.PostgreSQLNodeID)) + "\n" +
		"clusterguard.primary_node_id = " + postgresqlSQLLiteral(string(source.NodeID)) + "\n" +
		"primary_conninfo = " + postgresqlSQLLiteral(connection) + "\n"
	if _, err := file.WriteString(configuration); err != nil {
		_ = file.Close()
		return fmt.Errorf("write PostgreSQL recovery configuration: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync PostgreSQL recovery configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close PostgreSQL recovery configuration: %w", err)
	}
	return nil
}

func postgreSQLPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func restorePostgreSQLDataDirectory(dataDirectory, stageDirectory, backupDirectory string, hadOriginal bool) error {
	failedDirectory := stageDirectory + ".failed"
	if exists, err := postgreSQLPathExists(failedDirectory); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("failed PostgreSQL data quarantine already exists at %s", failedDirectory)
	}
	if exists, err := postgreSQLPathExists(dataDirectory); err != nil {
		return err
	} else if exists {
		if err := os.Rename(dataDirectory, failedDirectory); err != nil {
			return fmt.Errorf("quarantine failed PostgreSQL data directory: %w", err)
		}
	}
	if hadOriginal {
		if err := os.Rename(backupDirectory, dataDirectory); err != nil {
			return fmt.Errorf("restore original PostgreSQL data directory: %w", err)
		}
	}
	return nil
}

func (controller *PostgreSQLLocalController) BaseBackup(ctx context.Context, policy ClusterPolicy, source PostgreSQLPeer) error {
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
		return fmt.Errorf("inspect PostgreSQL backup quarantine: %w", err)
	} else if exists {
		return fmt.Errorf("PostgreSQL backup quarantine already exists at %s", backupDirectory)
	}
	if err := os.RemoveAll(stageDirectory); err != nil {
		return fmt.Errorf("remove stale PostgreSQL staging directory: %w", err)
	}
	if err := controller.Stop(ctx, policy); err != nil {
		return err
	}
	if _, err := controller.runAsPostgreSQLUser(ctx, policy, filepath.Join(policy.PostgreSQLBinaryDirectory, "pg_basebackup"),
		"--pgdata", stageDirectory,
		"--dbname", connection,
		"--write-recovery-conf", "--checkpoint", "fast", "--wal-method", "stream", "--progress", "--no-password",
	); err != nil {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("take PostgreSQL base backup: %w", err)
	}
	if info, err := os.Stat(filepath.Join(stageDirectory, "PG_VERSION")); err != nil || info.IsDir() {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("PostgreSQL base backup did not produce a valid PG_VERSION file")
	}
	if err := appendPostgreSQLRecoveryIdentity(filepath.Join(stageDirectory, "postgresql.auto.conf"), connection, policy, source); err != nil {
		_ = os.RemoveAll(stageDirectory)
		return err
	}

	hadOriginal, err := postgreSQLPathExists(dataDirectory)
	if err != nil {
		_ = os.RemoveAll(stageDirectory)
		return fmt.Errorf("inspect original PostgreSQL data directory: %w", err)
	}
	if hadOriginal {
		if err := os.Rename(dataDirectory, backupDirectory); err != nil {
			_ = os.RemoveAll(stageDirectory)
			return fmt.Errorf("quarantine original PostgreSQL data directory: %w", err)
		}
	}
	if err := os.Rename(stageDirectory, dataDirectory); err != nil {
		if hadOriginal {
			_ = os.Rename(backupDirectory, dataDirectory)
		}
		return fmt.Errorf("activate synchronized PostgreSQL data directory: %w", err)
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
			return fmt.Errorf("PostgreSQL base backup verification failed and rollback failed: start=%v source=%v rollback=%v", startErr, sourceErr, restoreErr)
		}
		return fmt.Errorf("PostgreSQL base backup verification failed; original data restored and service left stopped: start=%v source=%v", startErr, sourceErr)
	}
	if hadOriginal {
		if err := os.RemoveAll(backupDirectory); err != nil {
			return fmt.Errorf("remove verified PostgreSQL backup quarantine: %w", err)
		}
	}
	return nil
}

var _ PostgreSQLController = (*PostgreSQLLocalController)(nil)
