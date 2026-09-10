package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

// Quiescing preserves received relay transactions. RESET REPLICA and changing
// upstream before collecting evidence would destroy the basis for selection.
type RecoveryQuiescer interface {
	RecoveryQuiesce(context.Context, ClusterPolicy) (model.RecoveryEvidence, error)
}

const recoveryMySQLChannelsQuery = `SELECT JSON_OBJECT(
'channels', (SELECT COUNT(*) FROM performance_schema.replication_connection_configuration),
'unsupported', (SELECT COUNT(*) FROM performance_schema.replication_connection_configuration WHERE CHANNEL_NAME <> '' OR AUTO_POSITION <> 1),
'filters', (SELECT COUNT(*) FROM performance_schema.replication_applier_filters) + (SELECT COUNT(*) FROM performance_schema.replication_applier_global_filters),
'applier_errors', (SELECT COUNT(*) FROM performance_schema.replication_applier_status_by_worker WHERE LAST_ERROR_NUMBER <> 0) + (SELECT COUNT(*) FROM performance_schema.replication_applier_status_by_coordinator WHERE LAST_ERROR_NUMBER <> 0))`

const recoveryMySQLReceivedQuery = `SELECT JSON_OBJECT(
'rows', (SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME = ''),
'running', (SELECT COUNT(*) FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME = '' AND SERVICE_STATE <> 'OFF'),
'received', (SELECT RECEIVED_TRANSACTION_SET FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME = ''))`

func recoveryMySQLReplicationVerb(output []byte) (string, error) {
	version := strings.SplitN(strings.TrimSpace(string(output)), "-", 2)[0]
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("unsupported MySQL recovery version")
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	patch, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || major != 8 || (minor != 0 && minor != 4) || patch < 0 {
		return "", fmt.Errorf("MySQL recovery requires a qualified MySQL 8.0 or 8.4 server")
	}
	if minor == 0 && patch < 22 {
		return "SLAVE", nil
	}
	return "REPLICA", nil
}

func recoveryMySQLChannelCount(output []byte) (int, error) {
	var fields map[string]*int
	if err := json.Unmarshal(output, &fields); err != nil {
		return 0, fmt.Errorf("incomplete MySQL replication channel evidence")
	}
	for _, key := range []string{"channels", "unsupported", "filters", "applier_errors"} {
		if fields[key] == nil || *fields[key] < 0 {
			return 0, fmt.Errorf("incomplete MySQL replication channel evidence")
		}
	}
	if *fields["channels"] > 1 || *fields["unsupported"] != 0 || *fields["filters"] != 0 || *fields["applier_errors"] != 0 {
		return 0, fmt.Errorf("MySQL automatic recovery requires one unfiltered GTID channel without applier errors")
	}
	return *fields["channels"], nil
}

func quiesceMySQLRecovery(ctx context.Context, policy ClusterPolicy, roles DurableRoleController, query func(context.Context, ClusterPolicy, string) ([]byte, error)) (model.RecoveryEvidence, error) {
	var empty model.RecoveryEvidence
	isolation, err := roles.IsolationStatus(ctx, policy)
	if err != nil || !isolation.Isolated() || !isolation.DatabaseReachable {
		return empty, fmt.Errorf("MySQL relay drain requires a reachable durably write-fenced member")
	}
	version, err := query(ctx, policy, "SELECT @@version")
	if err != nil {
		return empty, err
	}
	verb, err := recoveryMySQLReplicationVerb(version)
	if err != nil {
		return empty, err
	}
	output, err := query(ctx, policy, recoveryMySQLChannelsQuery)
	if err != nil {
		return empty, err
	}
	channels, err := recoveryMySQLChannelCount(output)
	if err != nil {
		return empty, err
	}
	if channels == 0 {
		return inspectMySQLRecovery(ctx, policy, roles, query)
	}
	if _, err := query(ctx, policy, "STOP "+verb+" IO_THREAD"); err != nil {
		return empty, fmt.Errorf("stop MySQL recovery receiver: %w", err)
	}
	// Even a failed wait must stop the applier. Never leave replication running
	// after returning a blocked recovery task.
	drainErr := func() error {
		out, err := query(ctx, policy, recoveryMySQLReceivedQuery)
		if err != nil {
			return err
		}
		var receiver struct {
			Rows     *int    `json:"rows"`
			Running  *int    `json:"running"`
			Received *string `json:"received"`
		}
		if err := json.Unmarshal(out, &receiver); err != nil || receiver.Rows == nil || *receiver.Rows != 1 || receiver.Running == nil || *receiver.Running != 0 || receiver.Received == nil {
			return fmt.Errorf("cannot verify stopped MySQL receiver and received GTID history")
		}
		received := strings.TrimSpace(*receiver.Received)
		if _, err := gtid.ParseGTIDSet(received); err != nil {
			return fmt.Errorf("invalid received MySQL recovery GTID set")
		}
		if received != "" {
			if _, err := query(ctx, policy, "START "+verb+" SQL_THREAD"); err != nil {
				return fmt.Errorf("start MySQL recovery relay drain: %w", err)
			}
			// ParseGTIDSet validates the complete literal before it enters SQL.
			out, err := query(ctx, policy, "SELECT WAIT_FOR_EXECUTED_GTID_SET('"+received+"', 60)")
			if err != nil || strings.TrimSpace(string(out)) != "0" {
				return fmt.Errorf("MySQL received relay transactions could not be fully applied; recovery remains fenced")
			}
		}
		return nil
	}()
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, stopErr := query(stopCtx, policy, "STOP "+verb+" SQL_THREAD")
	if err := errors.Join(drainErr, stopErr); err != nil {
		return empty, err
	}
	return inspectMySQLRecovery(ctx, policy, roles, query)
}

func (c *MySQLRoleController) RecoveryQuiesce(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	return quiesceMySQLRecovery(ctx, p, c, c.recoveryQuery)
}

func (c *DockerMySQLRoleController) RecoveryQuiesce(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	return quiesceMySQLRecovery(ctx, p, c, c.recoveryQuery)
}

func (c *RuntimeRoleController) RecoveryQuiesce(ctx context.Context, p ClusterPolicy) (model.RecoveryEvidence, error) {
	selected, err := c.selected(p)
	if err != nil {
		return model.RecoveryEvidence{}, err
	}
	controller, ok := selected.(RecoveryQuiescer)
	if !ok {
		return model.RecoveryEvidence{}, fmt.Errorf("MySQL recovery relay drain unavailable")
	}
	return controller.RecoveryQuiesce(ctx, p)
}
