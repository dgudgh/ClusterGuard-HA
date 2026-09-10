package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

func recoveryReplicationSlot(p ClusterPolicy) (string, error) {
	if p.RecoveryArchiveID == "" {
		return "", nil
	}
	if !model.ValidResourceID(p.RecoveryArchiveID) || !model.ValidResourceID(p.PostgreSQLNodeID) {
		return "", fmt.Errorf("recovery slot requires a task and native member UUID")
	}
	return "cg_" + strings.ReplaceAll(strings.ToLower(string(p.PostgreSQLNodeID)), "-", ""), nil
}

func prepareRecoverySlot(p ClusterPolicy, source PostgreSQLPeer, query func(string) ([]byte, error)) error {
	slot, err := recoveryReplicationSlot(p)
	if err != nil || slot == "" {
		return fmt.Errorf("replica recovery slot identity is unavailable")
	}
	identity, err := query("SELECT current_setting('clusterguard.node_id') || E'\\t' || pg_is_in_recovery()::text")
	if err != nil || strings.TrimSpace(string(identity)) != string(source.NodeID)+"\tfalse" {
		return fmt.Errorf("replication slot source is not the selected native primary")
	}
	row, err := query("SELECT json_build_object('type',slot_type,'active',active,'temporary',temporary,'restart',restart_lsn::text,'wal_status',wal_status) FROM pg_replication_slots WHERE slot_name=" + postgresqlSQLLiteral(slot))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(row))) > 0 {
		var state struct {
			Type      string `json:"type"`
			Active    *bool  `json:"active"`
			Temporary *bool  `json:"temporary"`
			Restart   string `json:"restart"`
			WALStatus string `json:"wal_status"`
		}
		if err = json.Unmarshal(row, &state); err != nil || state.Type != "physical" || state.Active == nil || state.Temporary == nil || *state.Active || *state.Temporary {
			return fmt.Errorf("recovery slot is active, conflicting or unverified")
		}
		if state.Restart != "" && (state.WALStatus == "reserved" || state.WALStatus == "extended") {
			return nil
		}
		if state.WALStatus != "lost" && state.WALStatus != "unreserved" && state.Restart != "" {
			return fmt.Errorf("recovery slot WAL retention state is unverified")
		}
		// Only this stopped member's derived physical slot may be recreated.
		// PostgreSQL itself rejects a concurrent activation before the drop.
		if _, err = query("SELECT pg_drop_replication_slot(" + postgresqlSQLLiteral(slot) + ")"); err != nil {
			return err
		}
	}
	created, err := query("SELECT slot_name FROM pg_create_physical_replication_slot(" + postgresqlSQLLiteral(slot) + ",true,false)")
	if err != nil || strings.TrimSpace(string(created)) != slot {
		return fmt.Errorf("recovery physical slot was not reserved: %w", err)
	}
	return nil
}

func (c *PostgreSQLLocalController) recoverySlot(ctx context.Context, p ClusterPolicy, source PostgreSQLPeer) error {
	connection, err := postgresqlSourceURIForUser(p, source, p.PostgreSQLUser)
	if err != nil {
		return err
	}
	return prepareRecoverySlot(p, source, func(sql string) ([]byte, error) {
		return c.runAsPostgreSQLUser(ctx, p, "/usr/bin/env", "PGPASSFILE="+p.PostgreSQLPassfile,
			filepath.Join(p.PostgreSQLBinaryDirectory, "psql"), "--no-password", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", "--dbname", connection, "--command", sql)
	})
}

func (c *DockerPostgreSQLController) recoverySlot(ctx context.Context, p ClusterPolicy, source PostgreSQLPeer) error {
	connection, err := postgresqlSourceURIForUser(p, source, p.PostgreSQLUser)
	if err != nil {
		return err
	}
	image, err := c.serviceImage(ctx, p)
	if err != nil {
		return err
	}
	return prepareRecoverySlot(p, source, func(sql string) ([]byte, error) {
		return c.runRecoveryTool(ctx, p, image, "psql", p.PostgreSQLDataDirectory, p.DockerPostgreSQLDataDirectory,
			"--no-password", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", "--dbname", connection, "--command", sql)
	})
}
