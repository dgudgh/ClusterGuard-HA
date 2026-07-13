package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MySQLRoleController struct {
	runner         CommandRunner
	mysqlBinary    string
	stateDirectory string
}

func NewMySQLRoleController(runner CommandRunner, mysqlBinary string, stateDirectory string) *MySQLRoleController {
	return &MySQLRoleController{runner: runner, mysqlBinary: mysqlBinary, stateDirectory: stateDirectory}
}

func (controller *MySQLRoleController) Status(ctx context.Context, policy ClusterPolicy) (bool, bool, error) {
	arguments := controller.mysqlArguments(policy)
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

func (controller *MySQLRoleController) mysqlArguments(policy ClusterPolicy) []string {
	arguments := []string{"--no-defaults"}
	if policy.MySQLDefaultsFile != "" {
		arguments = []string{"--defaults-extra-file=" + policy.MySQLDefaultsFile}
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
	value := "OFF"
	if readOnly {
		value = "ON"
	}
	arguments := controller.mysqlArguments(policy)
	arguments = append(arguments, "--execute", "SET GLOBAL super_read_only = "+value+"; SET GLOBAL read_only = "+value)
	if _, err := controller.runner.Run(ctx, controller.binary(policy), arguments...); err != nil {
		return err
	}
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
