package postgresql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const identityQuery = `SELECT row_to_json(clusterguard_probe) FROM (
  SELECT
    current_setting('clusterguard.node_id', true) AS node_id,
    current_setting('clusterguard.primary_node_id', true) AS primary_node_id,
    (pg_control_system()).system_identifier::text AS system_identifier,
    COALESCE(NULLIF(current_setting('clusterguard.hostname', true), ''), inet_server_addr()::text) AS hostname,
    inet_server_port()::text AS port,
    current_setting('server_version') AS version,
	current_setting('wal_log_hints') AS wal_log_hints,
	(pg_control_init()).data_page_checksum_version::text AS data_checksum_version,
    pg_is_in_recovery() AS in_recovery,
    current_setting('transaction_read_only') AS transaction_read_only,
    CASE WHEN pg_is_in_recovery() THEN pg_is_wal_replay_paused() ELSE false END AS replay_paused,
    COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '') AS wal_receiver_status,
    CASE WHEN pg_is_in_recovery() THEN '' ELSE pg_current_wal_lsn()::text END AS current_lsn,
    COALESCE(pg_last_wal_receive_lsn()::text, '') AS receive_lsn,
    COALESCE(pg_last_wal_replay_lsn()::text, '') AS replay_lsn,
    CASE
      WHEN pg_is_in_recovery()
        AND pg_last_wal_receive_lsn() IS NOT NULL
        AND pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn()
      THEN 0
      WHEN pg_is_in_recovery() AND pg_last_xact_replay_timestamp() IS NOT NULL
      THEN GREATEST(0, FLOOR(EXTRACT(EPOCH FROM clock_timestamp() - pg_last_xact_replay_timestamp())))::bigint
      ELSE NULL
    END AS lag_seconds,
    CASE
      WHEN pg_is_in_recovery()
      THEN COALESCE(
        (SELECT received_tli::text FROM pg_stat_wal_receiver LIMIT 1),
        (pg_control_checkpoint()).timeline_id::text
      )
      ELSE (pg_control_checkpoint()).timeline_id::text
    END AS timeline_id
) AS clusterguard_probe`

type identityProbe struct {
	nodeID              model.ResourceID
	primaryNodeID       model.ResourceID
	systemIdentifier    string
	hostname            string
	port                int
	version             string
	walLogHints         bool
	dataChecksumVersion uint64
	inRecovery          bool
	transactionReadOnly bool
	replayPaused        bool
	walReceiverStatus   string
	currentLSN          string
	receiveLSN          string
	replayLSN           string
	lagSeconds          *int64
	timelineID          string
}

func discover(ctx context.Context, runner SQLRunner, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	if runner == nil {
		return adapter.DiscoveryResult{}, fmt.Errorf("PostgreSQL query runner is required")
	}
	started := time.Now()
	rows, err := runner.Query(ctx, request.Endpoint, request.Credentials, identityQuery)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	if len(rows) != 1 {
		return adapter.DiscoveryResult{}, fmt.Errorf("unexpected PostgreSQL identity response: got %d rows", len(rows))
	}
	probe, err := parseIdentityProbe(rows[0])
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}

	hostname := probe.hostname
	if hostname == "" {
		hostname = strings.TrimSpace(request.Endpoint.Hostname)
	}
	port := probe.port
	if port <= 0 {
		port = request.Endpoint.Port
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       string(probe.nodeID),
			"system_identifier": probe.systemIdentifier,
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        port,
		Role:        model.RoleUnknown,
		Health: model.Health{
			State:      model.HealthDegraded,
			Summary:    "PostgreSQL role or write state is unsafe",
			ObservedAt: time.Now().UTC(),
			LatencyMS:  time.Since(started).Milliseconds(),
		},
		Replication: model.ReplicationStatus{
			IOThread:          model.ThreadUnknown,
			SQLThread:         model.ThreadUnknown,
			LagSeconds:        cloneInt64(probe.lagSeconds),
			RetrievedPosition: probe.receiveLSN,
			ExecutedPosition:  probe.replayLSN,
		},
		EngineMetadata: map[string]string{
			"version":               probe.version,
			"wal_log_hints":         strconv.FormatBool(probe.walLogHints),
			"data_checksum_version": strconv.FormatUint(probe.dataChecksumVersion, 10),
			"system_identifier":     probe.systemIdentifier,
			"timeline_id":           probe.timelineID,
			"in_recovery":           strconv.FormatBool(probe.inRecovery),
			"transaction_read_only": strconv.FormatBool(probe.transactionReadOnly),
			"replay_paused":         strconv.FormatBool(probe.replayPaused),
			"wal_receiver_status":   probe.walReceiverStatus,
			"current_lsn":           probe.currentLSN,
			"receive_lsn":           probe.receiveLSN,
			"replay_lsn":            probe.replayLSN,
		},
	}

	if !probe.inRecovery && !probe.transactionReadOnly {
		instance.Role = model.RolePrimary
		instance.Health.State = model.HealthHealthy
		instance.Health.Summary = "PostgreSQL primary is reachable and writable"
		instance.Health.Replication = "primary"
		return adapter.DiscoveryResult{Instance: instance}, nil
	}
	if probe.inRecovery {
		instance.Role = model.RoleStandby
		if model.ValidResourceID(probe.primaryNodeID) {
			instance.Replication.SourceIdentity = model.EngineIdentity{
				"resource_id":       string(probe.primaryNodeID),
				"system_identifier": probe.systemIdentifier,
			}
		}
		instance.Replication.IOThread = postgresqlReceiverThreadState(probe.walReceiverStatus)
		if probe.replayPaused || probe.replayLSN == "" {
			instance.Replication.SQLThread = model.ThreadStopped
		} else {
			instance.Replication.SQLThread = model.ThreadRunning
		}
		instance.Health.Replication = probe.walReceiverStatus
		if probe.transactionReadOnly && model.ValidResourceID(probe.primaryNodeID) &&
			instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning {
			instance.Health.State = model.HealthHealthy
			instance.Health.Summary = "PostgreSQL standby is read-only and streaming WAL"
			instance.PromotionEligible = true
		} else {
			instance.Health.Summary = "PostgreSQL standby lacks safe streaming or identity evidence"
		}
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func parseIdentityProbe(row Row) (identityProbe, error) {
	nodeID := model.ResourceID(strings.TrimSpace(row["node_id"]))
	if !model.ValidResourceID(nodeID) {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL clusterguard.node_id")
	}
	primaryNodeID := model.ResourceID(strings.TrimSpace(row["primary_node_id"]))
	if primaryNodeID != "" && !model.ValidResourceID(primaryNodeID) {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL clusterguard.primary_node_id")
	}
	systemIdentifier := strings.TrimSpace(row["system_identifier"])
	if value, err := strconv.ParseUint(systemIdentifier, 10, 64); err != nil || value == 0 {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL system identifier")
	}
	port, err := strconv.Atoi(strings.TrimSpace(row["port"]))
	if err != nil || port <= 0 || port > 65535 {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL port")
	}
	inRecovery, err := parsePostgreSQLBoolean(row["in_recovery"])
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL recovery state: %w", err)
	}
	readOnly, err := parsePostgreSQLBoolean(row["transaction_read_only"])
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL transaction_read_only state: %w", err)
	}
	replayPaused, err := parsePostgreSQLBoolean(row["replay_paused"])
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL replay state: %w", err)
	}
	walLogHints, err := parsePostgreSQLBoolean(row["wal_log_hints"])
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL wal_log_hints state: %w", err)
	}
	dataChecksumVersion, err := strconv.ParseUint(strings.TrimSpace(row["data_checksum_version"]), 10, 32)
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL data checksum version")
	}
	currentLSN := strings.TrimSpace(row["current_lsn"])
	receiveLSN := strings.TrimSpace(row["receive_lsn"])
	replayLSN := strings.TrimSpace(row["replay_lsn"])
	for name, value := range map[string]string{"current_lsn": currentLSN, "receive_lsn": receiveLSN, "replay_lsn": replayLSN} {
		if value != "" {
			if _, err := parseLSN(value); err != nil {
				return identityProbe{}, fmt.Errorf("invalid PostgreSQL %s: %w", name, err)
			}
		}
	}
	timelineID := strings.TrimSpace(row["timeline_id"])
	if value, err := strconv.ParseUint(timelineID, 10, 32); err != nil || value == 0 {
		return identityProbe{}, fmt.Errorf("invalid PostgreSQL timeline ID")
	}
	var lagSeconds *int64
	if lag := strings.TrimSpace(row["lag_seconds"]); lag != "" {
		value, err := strconv.ParseInt(lag, 10, 64)
		if err != nil || value < 0 {
			return identityProbe{}, fmt.Errorf("invalid PostgreSQL replication lag")
		}
		lagSeconds = &value
	}
	return identityProbe{
		nodeID:              nodeID,
		primaryNodeID:       primaryNodeID,
		systemIdentifier:    systemIdentifier,
		hostname:            strings.TrimSpace(row["hostname"]),
		port:                port,
		version:             strings.TrimSpace(row["version"]),
		walLogHints:         walLogHints,
		dataChecksumVersion: dataChecksumVersion,
		inRecovery:          inRecovery,
		transactionReadOnly: readOnly,
		replayPaused:        replayPaused,
		walReceiverStatus:   strings.ToLower(strings.TrimSpace(row["wal_receiver_status"])),
		currentLSN:          currentLSN,
		receiveLSN:          receiveLSN,
		replayLSN:           replayLSN,
		lagSeconds:          lagSeconds,
		timelineID:          timelineID,
	}, nil
}

func parsePostgreSQLBoolean(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "t", "on", "1", "yes":
		return true, nil
	case "false", "f", "off", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected boolean %q", value)
	}
}

func postgresqlReceiverThreadState(value string) model.ThreadState {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "streaming":
		return model.ThreadRunning
	case "starting", "connecting", "catchup":
		return model.ThreadConnecting
	case "":
		return model.ThreadStopped
	default:
		return model.ThreadStopped
	}
}

func parseLSN(value string) (uint64, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("LSN %q must have X/Y form", value)
	}
	high, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN high word %q", parts[0])
	}
	low, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN low word %q", parts[1])
	}
	return high<<32 | low, nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
