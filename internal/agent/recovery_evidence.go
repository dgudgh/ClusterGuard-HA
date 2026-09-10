package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

type RecoveryEvidenceInspector interface {
	RecoveryInspect(context.Context, ClusterPolicy) (model.RecoveryEvidence, error)
}

type RecoveryWALInspector interface {
	RecoveryWAL(context.Context, ClusterPolicy, model.RecoveryWALRequest) (model.RecoveryWALResult, error)
}

func (c *RuntimePostgreSQLController) RecoveryInspect(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	selected, err := c.selected(p)
	if err != nil {
		return model.RecoveryEvidence{}, err
	}
	inspector, ok := selected.(RecoveryEvidenceInspector)
	if !ok {
		return model.RecoveryEvidence{}, fmt.Errorf("PostgreSQL offline evidence inspector unavailable")
	}
	return inspector.RecoveryInspect(ctx, p)
}

func (c *RuntimePostgreSQLController) RecoveryWAL(ctx context.Context, p ClusterPolicy, r model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	selected, err := c.selected(p)
	if err != nil {
		return model.RecoveryWALResult{}, err
	}
	inspector, ok := selected.(RecoveryWALInspector)
	if !ok {
		return model.RecoveryWALResult{}, fmt.Errorf("PostgreSQL WAL verifier unavailable")
	}
	return inspector.RecoveryWAL(ctx, p, r)
}

func (c *RuntimeRoleController) RecoveryInspect(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	selected, err := c.selected(p)
	if err != nil {
		return model.RecoveryEvidence{}, err
	}
	inspector, ok := selected.(RecoveryEvidenceInspector)
	if !ok {
		return model.RecoveryEvidence{}, fmt.Errorf("MySQL recovery evidence inspector unavailable")
	}
	return inspector.RecoveryInspect(ctx, p)
}

type recoveryMySQLState struct {
	UUID                   string `json:"uuid"`
	Executed               string `json:"executed"`
	Purged                 string `json:"purged"`
	GTIDMode               string `json:"gtid_mode"`
	Consistency            string `json:"consistency"`
	LogBin                 int    `json:"log_bin"`
	LogUpdates             int    `json:"log_updates"`
	ReadOnly               int    `json:"read_only"`
	SuperReadOnly          int    `json:"super_read_only"`
	ActiveReceivers        int    `json:"active_receivers"`
	ActiveAppliers         int    `json:"active_appliers"`
	PendingRelay           int    `json:"pending_relay"`
	UnsafeRestartOverrides int    `json:"unsafe_restart_overrides"`
}

const recoveryMySQLStateQuery = `SELECT JSON_OBJECT(
'uuid', @@GLOBAL.server_uuid, 'executed', @@GLOBAL.gtid_executed, 'purged', @@GLOBAL.gtid_purged,
'gtid_mode', @@GLOBAL.gtid_mode, 'consistency', @@GLOBAL.enforce_gtid_consistency,
'log_bin', @@GLOBAL.log_bin, 'log_updates', @@GLOBAL.log_slave_updates,
'read_only', @@GLOBAL.read_only, 'super_read_only', @@GLOBAL.super_read_only,
'active_receivers', (SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE SERVICE_STATE <> 'OFF'),
'active_appliers', (SELECT COUNT(*) FROM performance_schema.replication_applier_status WHERE SERVICE_STATE <> 'OFF'),
'pending_relay', (SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE GTID_SUBSET(RECEIVED_TRANSACTION_SET, @@GLOBAL.gtid_executed) <> 1),
'unsafe_restart_overrides', (SELECT COUNT(*) FROM performance_schema.persisted_variables WHERE VARIABLE_NAME IN ('read_only', 'super_read_only') AND COALESCE(VARIABLE_VALUE, '') NOT IN ('ON', '1')))`

func parseRecoveryMySQLState(output []byte) (recoveryMySQLState, error) {
	var state recoveryMySQLState
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output, &fields); err != nil {
		return state, fmt.Errorf("MySQL recovery evidence is incomplete")
	}
	for _, key := range []string{"uuid", "executed", "purged", "gtid_mode", "consistency", "log_bin", "log_updates", "read_only", "super_read_only", "active_receivers", "active_appliers", "pending_relay", "unsafe_restart_overrides"} {
		if value, ok := fields[key]; !ok || strings.TrimSpace(string(value)) == "null" {
			return state, fmt.Errorf("MySQL recovery evidence lacks %s", key)
		}
	}
	if err := json.Unmarshal(output, &state); err != nil {
		return state, fmt.Errorf("MySQL recovery evidence is incomplete")
	}
	if !model.ValidResourceID(model.ResourceID(state.UUID)) || state.GTIDMode != "ON" || state.Consistency != "ON" || state.LogBin != 1 || state.LogUpdates != 1 || state.ReadOnly != 1 || state.SuperReadOnly != 1 || state.ActiveReceivers != 0 || state.ActiveAppliers != 0 || state.PendingRelay != 0 {
		return state, fmt.Errorf("MySQL recovery requires GTID ON, durable read-only, complete binlog history and fully drained stopped replication")
	}
	if state.UnsafeRestartOverrides != 0 {
		return state, fmt.Errorf("MySQL persisted writable overrides invalidate the recovery restart fence")
	}
	executed, err := gtid.ParseGTIDSet(state.Executed)
	if err != nil {
		return state, err
	}
	purged, err := gtid.ParseGTIDSet(state.Purged)
	if err != nil {
		return state, err
	}
	if _, err := gtid.AssessGTIDRecovery(executed, purged, executed); err != nil {
		return state, err
	}
	return state, nil
}

func inspectMySQLRecovery(ctx context.Context, p ClusterPolicy, roles DurableRoleController, query func(context.Context, ClusterPolicy, string) ([]byte, error)) (model.RecoveryEvidence, error) {
	e := model.RecoveryEvidence{InstanceID: p.InstanceID, Engine: model.EngineMySQL, ObservedAt: time.Now().UTC()}
	isolation, err := roles.IsolationStatus(ctx, p)
	if err != nil || !isolation.Isolated() || !isolation.DatabaseReachable {
		return e, fmt.Errorf("MySQL must be reachable behind a verified durable write fence")
	}
	before, err := query(ctx, p, recoveryMySQLStateQuery)
	if err != nil {
		return e, err
	}
	state, err := parseRecoveryMySQLState(before)
	if err != nil {
		return e, err
	}
	xa, err := query(ctx, p, "XA RECOVER")
	if err != nil {
		return e, fmt.Errorf("cannot prove absence of prepared XA transactions")
	}
	if strings.TrimSpace(string(xa)) != "" {
		return e, fmt.Errorf("prepared XA transactions require manual recovery")
	}
	after, err := query(ctx, p, recoveryMySQLStateQuery)
	if err != nil {
		return e, err
	}
	last, err := parseRecoveryMySQLState(after)
	if err != nil || last != state {
		return e, fmt.Errorf("MySQL committed history changed during recovery inspection")
	}
	e.NativeID = state.UUID
	e.GTIDExecuted = state.Executed
	e.GTIDPurged = state.Purged
	e.Fenced = true
	e.Complete = true
	e.Fingerprint = disaster.EvidenceFingerprint(e)
	return e, nil
}

func (c *MySQLRoleController) recoveryQuery(ctx context.Context, p ClusterPolicy, query string) ([]byte, error) {
	args := append(MySQLClientArguments(p), "--raw", "--execute", query)
	return c.runner.Run(ctx, c.binary(p), args...)
}

func (c *MySQLRoleController) RecoveryInspect(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	return inspectMySQLRecovery(ctx, p, c, c.recoveryQuery)
}
func (c *DockerMySQLRoleController) RecoveryInspect(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	return inspectMySQLRecovery(ctx, p, c, c.recoveryQuery)
}
