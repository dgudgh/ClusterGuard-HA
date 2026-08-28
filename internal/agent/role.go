package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type MySQLRoleController struct {
	runner         CommandRunner
	mysqlBinary    string
	stateDirectory string
}

type MySQLIsolationStatus struct {
	DatabaseReachable bool
	ServiceRunning    bool
	ReadOnly          bool
	SuperReadOnly     bool
	RestartReadOnly   bool
	PersistedReadOnly bool
}

func (status MySQLIsolationStatus) Isolated() bool {
	if !status.RestartReadOnly || !status.PersistedReadOnly {
		return false
	}
	if status.DatabaseReachable {
		return status.ReadOnly && status.SuperReadOnly
	}
	return !status.ServiceRunning
}

func (status MySQLIsolationStatus) PathReady() bool {
	if !status.RestartReadOnly {
		return false
	}
	return status.DatabaseReachable || !status.ServiceRunning
}

func NewMySQLRoleController(runner CommandRunner, mysqlBinary string, stateDirectory string) *MySQLRoleController {
	return &MySQLRoleController{runner: runner, mysqlBinary: mysqlBinary, stateDirectory: stateDirectory}
}

func (controller *MySQLRoleController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	arguments := MySQLClientArguments(policy)
	arguments = append(arguments, "--execute", "SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only")
	output, err := controller.runner.Run(ctx, controller.binary(policy), arguments...)
	if err != nil {
		return false, false, err
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return false, false, fmt.Errorf("unexpected MySQL role status output")
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

func (controller *MySQLRoleController) binary(policy ClusterPolicy) string {
	if binary := strings.TrimSpace(policy.MySQLBinary); binary != "" {
		return binary
	}
	return controller.mysqlBinary
}

// MySQLClientArguments builds the pinned local client arguments used for
// every MySQL connection: no loose defaults, optional defaults file, TCP
// against 127.0.0.1, batch output. Shared by the role and power controllers
// so database mutations and probes always speak to the same instance.
func MySQLClientArguments(policy ClusterPolicy) []string {
	arguments := []string{"--no-defaults"}
	if policy.MySQLDefaultsFile != "" {
		arguments = []string{"--defaults-file=" + policy.MySQLDefaultsFile}
	}
	return append(arguments, "--protocol=tcp", "--host=127.0.0.1", fmt.Sprintf("--port=%d", policy.MySQLPort), "--batch", "--skip-column-names")
}

func mysqlBoolean(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "true":
		return true, nil
	case "0", "off", "false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected MySQL boolean value")
	}
}

func (controller *MySQLRoleController) PersistReadOnly(ctx context.Context, policy ClusterPolicy, readOnly bool) error {
	if readOnly {
		if err := controller.persistState(policy, true); err != nil {
			return err
		}
	}
	majorVersion, err := controller.majorVersion(ctx, policy)
	if err != nil {
		return err
	}
	if majorVersion >= 8 {
		arguments := MySQLClientArguments(policy)
		arguments = append(arguments, "--execute", "SET PERSIST_ONLY super_read_only = ON; SET PERSIST_ONLY read_only = ON")
		if _, err := controller.runner.Run(ctx, controller.binary(policy), arguments...); err != nil {
			return fmt.Errorf("persist MySQL restart read-only state: %w", err)
		}
	}
	value := "OFF"
	if readOnly {
		value = "ON"
	}
	arguments := MySQLClientArguments(policy)
	arguments = append(arguments, "--execute", "SET GLOBAL super_read_only = "+value+"; SET GLOBAL read_only = "+value)
	if _, err := controller.runner.Run(ctx, controller.binary(policy), arguments...); err != nil {
		return err
	}
	if readOnly {
		return nil
	}
	return controller.persistState(policy, false)
}

func (controller *MySQLRoleController) majorVersion(ctx context.Context, policy ClusterPolicy) (int, error) {
	arguments := MySQLClientArguments(policy)
	arguments = append(arguments, "--execute", "SELECT SUBSTRING_INDEX(VERSION(), '.', 1)")
	output, err := controller.runner.Run(ctx, controller.binary(policy), arguments...)
	if err != nil {
		return 0, fmt.Errorf("inspect MySQL major version: %w", err)
	}
	value := strings.TrimSpace(string(output))
	majorVersion, err := strconv.Atoi(value)
	if err != nil || majorVersion < 1 {
		return 0, fmt.Errorf("unexpected MySQL major version %q", value)
	}
	return majorVersion, nil
}

func (controller *MySQLRoleController) persistState(policy ClusterPolicy, readOnly bool) error {
	if err := os.MkdirAll(controller.stateDirectory, 0750); err != nil {
		return fmt.Errorf("create role state directory: %w", err)
	}
	contents, err := json.Marshal(map[string]interface{}{"cluster_id": policy.ClusterID, "instance_id": policy.InstanceID, "read_only": readOnly})
	if err != nil {
		return fmt.Errorf("encode role state: %w", err)
	}
	path := filepath.Join(controller.stateDirectory, string(policy.ClusterID)+".json")
	temporary, err := os.CreateTemp(controller.stateDirectory, ".role-*.tmp")
	if err != nil {
		return fmt.Errorf("create role state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
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
		return fmt.Errorf("commit role state: %w", err)
	}
	return nil
}

func (controller *MySQLRoleController) persistedReadOnly(policy ClusterPolicy) (bool, error) {
	path := filepath.Join(controller.stateDirectory, string(policy.ClusterID)+".json")
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read role state: %w", err)
	}
	var state struct {
		ClusterID  model.ResourceID `json:"cluster_id"`
		InstanceID model.ResourceID `json:"instance_id"`
		ReadOnly   bool             `json:"read_only"`
	}
	if err := json.Unmarshal(contents, &state); err != nil {
		return false, fmt.Errorf("decode role state: %w", err)
	}
	if state.ClusterID != policy.ClusterID || state.InstanceID != policy.InstanceID {
		return false, fmt.Errorf("role state identity does not match the agent policy")
	}
	return state.ReadOnly, nil
}

func (controller *MySQLRoleController) IsolationStatus(ctx context.Context, policy ClusterPolicy) (MySQLIsolationStatus, error) {
	status := MySQLIsolationStatus{}
	if strings.TrimSpace(policy.MySQLService) == "" || strings.TrimSpace(policy.MySQLServerBinary) == "" || strings.TrimSpace(policy.MySQLServerDefaultsFile) == "" {
		return status, fmt.Errorf("MySQL durable restart fence is not configured")
	}
	persisted, err := controller.persistedReadOnly(policy)
	if err != nil {
		return status, err
	}
	status.PersistedReadOnly = persisted

	serviceRunning, err := controller.serviceRunning(ctx, policy.MySQLService)
	if err != nil {
		return status, err
	}
	status.ServiceRunning = serviceRunning

	defaultsOutput, err := controller.runner.Run(ctx, policy.MySQLServerBinary, "--defaults-file="+policy.MySQLServerDefaultsFile, "--verbose", "--help")
	if err != nil {
		return status, fmt.Errorf("inspect MySQL restart defaults: %w", err)
	}
	readOnlyDefault, superReadOnlyDefault, err := parseMySQLRestartDefaults(defaultsOutput)
	if err != nil {
		return status, err
	}
	dataDirectory, err := parseMySQLDataDirectory(defaultsOutput)
	if err != nil {
		return status, err
	}
	persistedReadOnly, persistedSuperReadOnly, err := readMySQLPersistedRoleOverrides(dataDirectory)
	if err != nil {
		return status, err
	}
	if persistedReadOnly != nil {
		readOnlyDefault = *persistedReadOnly
	}
	if persistedSuperReadOnly != nil {
		superReadOnlyDefault = *persistedSuperReadOnly
	}
	status.RestartReadOnly = readOnlyDefault && superReadOnlyDefault

	readOnly, superReadOnly, roleErr := controller.Status(ctx, policy)
	if roleErr == nil {
		status.DatabaseReachable = true
		status.ReadOnly = readOnly
		status.SuperReadOnly = superReadOnly
		return status, nil
	}
	if status.ServiceRunning {
		return status, fmt.Errorf("running MySQL role state is unavailable: %w", roleErr)
	}
	return status, nil
}

func (controller *MySQLRoleController) serviceRunning(ctx context.Context, service string) (bool, error) {
	serviceOutput, serviceErr := controller.runner.Run(ctx, "/usr/bin/systemctl", "is-active", service)
	switch strings.TrimSpace(string(serviceOutput)) {
	case "active":
		if serviceErr != nil {
			return false, fmt.Errorf("inspect MySQL service state: %w", serviceErr)
		}
		return true, nil
	case "inactive", "failed":
		return false, nil
	case "activating":
		// Restart=always units briefly enter activating/auto-restart with no
		// process after mysqld exits. Treat only that exact, empty state as
		// stopped. A transitional unit with a PID remains live and therefore
		// cannot be used as old-primary fencing evidence.
		propertiesOutput, propertiesErr := controller.runner.Run(ctx, "/usr/bin/systemctl", "show", service,
			"--property=ActiveState", "--property=SubState", "--property=MainPID", "--property=ControlGroup", "--no-pager")
		if propertiesErr != nil {
			return false, fmt.Errorf("inspect restarting MySQL service state: %w", propertiesErr)
		}
		properties := make(map[string]string)
		for _, line := range strings.Split(string(propertiesOutput), "\n") {
			name, value, found := strings.Cut(strings.TrimSpace(line), "=")
			if found {
				properties[name] = strings.TrimSpace(value)
			}
		}
		mainPID, pidErr := strconv.Atoi(properties["MainPID"])
		if pidErr != nil || mainPID < 0 {
			return false, fmt.Errorf("restarting MySQL service has an invalid main PID")
		}
		if mainPID > 0 || properties["ControlGroup"] != "" {
			return true, nil
		}
		if properties["ActiveState"] == "activating" && properties["SubState"] == "auto-restart" {
			return false, nil
		}
		return false, fmt.Errorf("restarting MySQL service state is not safely empty")
	default:
		if serviceErr != nil {
			return false, fmt.Errorf("inspect MySQL service state: %w", serviceErr)
		}
		return false, fmt.Errorf("unexpected MySQL service state")
	}
}

func parseMySQLRestartDefaults(output []byte) (bool, bool, error) {
	var readOnly, superReadOnly bool
	var readOnlyFound, superReadOnlyFound bool
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "read-only":
			value, err := mysqlBoolean(fields[1])
			if err != nil {
				return false, false, fmt.Errorf("invalid MySQL read-only restart default: %w", err)
			}
			readOnly, readOnlyFound = value, true
		case "super-read-only":
			value, err := mysqlBoolean(fields[1])
			if err != nil {
				return false, false, fmt.Errorf("invalid MySQL super-read-only restart default: %w", err)
			}
			superReadOnly, superReadOnlyFound = value, true
		}
	}
	if !readOnlyFound || !superReadOnlyFound {
		return false, false, fmt.Errorf("MySQL restart defaults do not expose both read-only controls")
	}
	return readOnly, superReadOnly, nil
}

func parseMySQLDataDirectory(output []byte) (string, error) {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "datadir") {
			continue
		}
		dataDirectory := filepath.Clean(strings.TrimSpace(fields[1]))
		if !filepath.IsAbs(dataDirectory) {
			return "", fmt.Errorf("MySQL data directory is not an absolute path")
		}
		return dataDirectory, nil
	}
	return "", fmt.Errorf("MySQL restart defaults do not expose the data directory")
}

func readMySQLPersistedRoleOverrides(dataDirectory string) (*bool, *bool, error) {
	path := filepath.Join(dataDirectory, "mysqld-auto.cnf")
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read MySQL persisted globals: %w", err)
	}
	var document struct {
		DynamicVariables map[string]struct {
			Value interface{} `json:"Value"`
		} `json:"mysql_dynamic_variables"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		return nil, nil, fmt.Errorf("decode MySQL persisted globals: %w", err)
	}
	var readOnly, superReadOnly *bool
	for name, variable := range document.DynamicVariables {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "read_only":
			value, err := mysqlBoolean(fmt.Sprint(variable.Value))
			if err != nil {
				return nil, nil, fmt.Errorf("invalid persisted MySQL read_only value: %w", err)
			}
			readOnly = boolPointer(value)
		case "super_read_only":
			value, err := mysqlBoolean(fmt.Sprint(variable.Value))
			if err != nil {
				return nil, nil, fmt.Errorf("invalid persisted MySQL super_read_only value: %w", err)
			}
			superReadOnly = boolPointer(value)
		}
	}
	return readOnly, superReadOnly, nil
}

func boolPointer(value bool) *bool {
	return &value
}
