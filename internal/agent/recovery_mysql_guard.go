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

type RecoveryReplicaGuard interface {
	RecoveryReplicaPrepare(context.Context, ClusterPolicy, model.ResourceID) error
	RecoveryReplicaVerify(context.Context, ClusterPolicy, model.ResourceID) error
	RecoveryReplicaRelease(context.Context, ClusterPolicy, model.ResourceID) error
}

type mysqlRecoveryGuard struct {
	TaskID          model.ResourceID `json:"task_id"`
	ClusterID       model.ResourceID `json:"cluster_id"`
	InstanceID      model.ResourceID `json:"instance_id"`
	Released        bool             `json:"released"`
	OriginalOffline bool             `json:"original_offline"`
}

type mysqlGuardQuery func(context.Context, ClusterPolicy, string) ([]byte, error)

func mysqlGuardPath(directory string, p ClusterPolicy) string {
	return filepath.Join(directory, string(p.ClusterID)+"-"+string(p.InstanceID)+"-recovery.json")
}

func readMySQLGuard(directory string, p ClusterPolicy, id model.ResourceID) (mysqlRecoveryGuard, error) {
	var state mysqlRecoveryGuard
	b, err := recoveryRegularFile(mysqlGuardPath(directory, p))
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("invalid MySQL recovery guard state")
	}
	if !model.ValidResourceID(id) || state.TaskID != id || state.ClusterID != p.ClusterID || state.InstanceID != p.InstanceID {
		return state, fmt.Errorf("MySQL recovery guard identity does not match")
	}
	return state, nil
}

func prepareMySQLGuard(ctx context.Context, directory string, p ClusterPolicy, id model.ResourceID, query mysqlGuardQuery, roles RoleController) error {
	if !model.ValidResourceID(id) {
		return fmt.Errorf("recovery task UUID is required")
	}
	if err := roles.PersistReadOnly(ctx, p, true); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	state, err := readMySQLGuard(directory, p, id)
	if err != nil && !os.IsNotExist(err) && !(state.Released && state.ClusterID == p.ClusterID && state.InstanceID == p.InstanceID) {
		return err
	}
	if err != nil || state.Released {
		output, queryErr := query(ctx, p, "SELECT @@GLOBAL.offline_mode")
		if queryErr != nil {
			return queryErr
		}
		on, queryErr := mysqlBoolean(strings.TrimSpace(string(output)))
		if queryErr != nil {
			return queryErr
		}
		state = mysqlRecoveryGuard{TaskID: id, ClusterID: p.ClusterID, InstanceID: p.InstanceID, OriginalOffline: on}
	}
	b, _ := json.Marshal(state)
	if err = recoveryAtomicFile(mysqlGuardPath(directory, p), b, 0600); err != nil {
		return err
	}
	if _, err = query(ctx, p, "SET PERSIST offline_mode=ON"); err != nil {
		return fmt.Errorf("enable MySQL recovery offline mode: %w", err)
	}
	return verifyMySQLGuard(ctx, directory, p, id, query, roles)
}

func verifyMySQLGuard(ctx context.Context, directory string, p ClusterPolicy, id model.ResourceID, query mysqlGuardQuery, roles RoleController) error {
	state, err := readMySQLGuard(directory, p, id)
	if err != nil {
		return err
	}
	if state.Released {
		readOnly, superReadOnly, err := roles.Status(ctx, p)
		if err != nil || !readOnly || !superReadOnly {
			return fmt.Errorf("completed recovery replica is not read-only")
		}
		return nil
	}
	output, err := query(ctx, p, "SELECT @@GLOBAL.offline_mode")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(output)) != "1" {
		// A clone restart may import the donor's persisted variables. Restore
		// offline mode before granting any further reconstruction permission.
		if _, err = query(ctx, p, "SET PERSIST offline_mode=ON"); err != nil {
			return err
		}
		output, err = query(ctx, p, "SELECT @@GLOBAL.offline_mode")
	}
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("MySQL recovery offline-mode guard is not verified")
	}
	return nil
}

func releaseMySQLGuard(ctx context.Context, directory string, p ClusterPolicy, id model.ResourceID, query mysqlGuardQuery, roles RoleController) error {
	state, err := readMySQLGuard(directory, p, id)
	if err != nil {
		return err
	}
	if err = roles.PersistReadOnly(ctx, p, true); err != nil {
		return err
	}
	readOnly, superReadOnly, err := roles.Status(ctx, p)
	if err != nil || !readOnly || !superReadOnly {
		return fmt.Errorf("replica must be read-only before releasing the reconstruction guard")
	}
	setting := "OFF"
	if state.OriginalOffline {
		setting = "ON"
	}
	if _, err = query(ctx, p, "SET PERSIST offline_mode="+setting); err != nil {
		return err
	}
	state.Released = true
	b, _ := json.Marshal(state)
	return recoveryAtomicFile(mysqlGuardPath(directory, p), b, 0600)
}

func (c *MySQLRoleController) RecoveryReplicaPrepare(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return prepareMySQLGuard(ctx, c.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *MySQLRoleController) RecoveryReplicaVerify(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return verifyMySQLGuard(ctx, c.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *MySQLRoleController) RecoveryReplicaRelease(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return releaseMySQLGuard(ctx, c.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *DockerMySQLRoleController) RecoveryReplicaPrepare(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return prepareMySQLGuard(ctx, c.state.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *DockerMySQLRoleController) RecoveryReplicaVerify(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return verifyMySQLGuard(ctx, c.state.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *DockerMySQLRoleController) RecoveryReplicaRelease(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	return releaseMySQLGuard(ctx, c.state.stateDirectory, p, id, c.recoveryQuery, c)
}
func (c *RuntimeRoleController) recoveryReplicaController(p ClusterPolicy) (RecoveryReplicaGuard, error) {
	selected, e := c.selected(p)
	if e != nil {
		return nil, e
	}
	guard, ok := selected.(RecoveryReplicaGuard)
	if !ok {
		return nil, fmt.Errorf("runtime lacks MySQL recovery replica guard")
	}
	return guard, nil
}
func (c *RuntimeRoleController) RecoveryReplicaPrepare(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryReplicaController(p)
	if e != nil {
		return e
	}
	return g.RecoveryReplicaPrepare(ctx, p, id)
}
func (c *RuntimeRoleController) RecoveryReplicaVerify(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryReplicaController(p)
	if e != nil {
		return e
	}
	return g.RecoveryReplicaVerify(ctx, p, id)
}
func (c *RuntimeRoleController) RecoveryReplicaRelease(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryReplicaController(p)
	if e != nil {
		return e
	}
	return g.RecoveryReplicaRelease(ctx, p, id)
}
