package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const identityQuery = "SELECT @@server_uuid AS server_uuid, @@hostname AS hostname, @@port AS port, @@server_id AS server_id, @@version AS version, @@read_only AS read_only, @@super_read_only AS super_read_only, @@gtid_mode AS gtid_mode, @@GLOBAL.gtid_executed AS gtid_executed, @@GLOBAL.gtid_purged AS gtid_purged, @@log_bin AS log_bin, @@binlog_format AS binlog_format"

const (
	replicaStatusQuery = "SHOW REPLICA STATUS"
	slaveStatusQuery   = "SHOW SLAVE STATUS"
)

type identityProbe struct {
	serverUUID    string
	hostname      string
	port          int
	serverID      string
	version       string
	readOnly      bool
	superReadOnly bool
	gtidMode      string
	gtidExecuted  string
	gtidPurged    string
	logBin        string
	binlogFormat  string
}

func probeIdentity(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (identityProbe, error) {
	rows, err := runner.Query(ctx, endpoint, credentials, identityQuery)
	if err != nil {
		return identityProbe{}, err
	}
	if len(rows) != 1 {
		return identityProbe{}, fmt.Errorf("unexpected MySQL identity response: got %d rows", len(rows))
	}
	return parseIdentity(rows[0])
}

func parseIdentity(row Row) (identityProbe, error) {
	port, err := strconv.Atoi(first(row, "port"))
	if err != nil || port <= 0 {
		return identityProbe{}, fmt.Errorf("invalid MySQL port in identity response")
	}
	serverID := first(row, "server_id")
	if _, err := strconv.ParseUint(serverID, 10, 64); err != nil {
		return identityProbe{}, fmt.Errorf("invalid MySQL server ID in identity response")
	}
	readOnly, err := parseMySQLBoolean(first(row, "read_only"))
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid MySQL read_only value: %w", err)
	}
	superReadOnly, err := parseMySQLBoolean(first(row, "super_read_only"))
	if err != nil {
		return identityProbe{}, fmt.Errorf("invalid MySQL super_read_only value: %w", err)
	}
	serverUUID := strings.ToLower(first(row, "server_uuid"))
	if serverUUID == "" {
		return identityProbe{}, fmt.Errorf("MySQL server UUID is empty")
	}
	return identityProbe{
		serverUUID:    serverUUID,
		hostname:      first(row, "hostname"),
		port:          port,
		serverID:      serverID,
		version:       first(row, "version"),
		readOnly:      readOnly,
		superReadOnly: superReadOnly,
		gtidMode:      first(row, "gtid_mode"),
		gtidExecuted:  first(row, "gtid_executed"),
		gtidPurged:    first(row, "gtid_purged"),
		logBin:        first(row, "log_bin"),
		binlogFormat:  first(row, "binlog_format"),
	}, nil
}

func parseMySQLBoolean(value string) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "1", "ON", "YES", "TRUE":
		return true, nil
	case "0", "OFF", "NO", "FALSE":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected boolean %q", value)
	}
}

func probeReplication(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (model.ReplicationStatus, bool, error) {
	rows, err := runner.Query(ctx, endpoint, credentials, replicaStatusQuery)
	if err != nil {
		if !errors.Is(err, ErrStatementUnsupported) {
			return model.ReplicationStatus{}, false, fmt.Errorf("probe MySQL replication: %w", err)
		}
		rows, err = runner.Query(ctx, endpoint, credentials, slaveStatusQuery)
		if err != nil {
			return model.ReplicationStatus{}, false, fmt.Errorf("probe MySQL replication with legacy statement: %w", err)
		}
	}
	if len(rows) == 0 {
		return model.ReplicationStatus{IOThread: model.ThreadUnknown, SQLThread: model.ThreadUnknown}, false, nil
	}
	if len(rows) > 1 {
		return model.ReplicationStatus{}, true, fmt.Errorf("multiple MySQL replication channels are unsupported")
	}
	status, err := parseReplication(rows[0])
	if err != nil {
		return model.ReplicationStatus{}, true, err
	}
	return status, true, nil
}

func parseReplication(row Row) (model.ReplicationStatus, error) {
	lastIOError := first(row, "Last_IO_Error")
	lastSQLError := first(row, "Last_SQL_Error")
	status := model.ReplicationStatus{
		SourceIdentity:    model.EngineIdentity{},
		IOThread:          parseThreadState(first(row, "Replica_IO_Running", "Slave_IO_Running")),
		SQLThread:         parseThreadState(first(row, "Replica_SQL_Running", "Slave_SQL_Running")),
		RetrievedPosition: first(row, "Retrieved_Gtid_Set"),
		ExecutedPosition:  first(row, "Executed_Gtid_Set"),
		LastIOError:       lastIOError,
		LastSQLError:      lastSQLError,
		LastError:         first(row, "Last_SQL_Error", "Last_IO_Error", "Last_Error"),
	}
	if sourceUUID := first(row, "Source_UUID", "Master_UUID"); sourceUUID != "" {
		status.SourceIdentity["server_uuid"] = strings.ToLower(sourceUUID)
	}
	lag := first(row, "Seconds_Behind_Source", "Seconds_Behind_Master")
	if lag != "" && !strings.EqualFold(lag, "NULL") {
		seconds, err := strconv.ParseInt(lag, 10, 64)
		if err != nil {
			return model.ReplicationStatus{}, fmt.Errorf("invalid MySQL replication lag %q: %w", lag, err)
		}
		status.LagSeconds = &seconds
	}
	return status, nil
}

func parseThreadState(value string) model.ThreadState {
	if value == "" {
		return model.ThreadUnknown
	}
	if strings.EqualFold(value, "Yes") {
		return model.ThreadRunning
	}
	if strings.EqualFold(value, "Connecting") {
		return model.ThreadConnecting
	}
	return model.ThreadStopped
}

func first(row Row, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(row[name]); value != "" {
			return value
		}
	}
	return ""
}

func discover(ctx context.Context, runner SQLRunner, request adapter.DiscoverRequest, semiSyncRequired bool) (adapter.DiscoveryResult, error) {
	started := time.Now()
	identity, err := probeIdentity(ctx, runner, request.Endpoint, request.Credentials)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	semiSync, semiSyncErr := probeSemiSync(ctx, runner, request.Endpoint, request.Credentials)
	replication, configured, err := probeReplication(ctx, runner, request.Endpoint, request.Credentials)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}

	role := model.RoleUnknown
	healthState := model.HealthDegraded
	healthSummary := "MySQL instance is read-only with no replication source"
	replicaReadOnly := identity.readOnly || identity.superReadOnly
	if configured {
		role = model.RoleReplica
		healthSummary = "MySQL replica is reachable but replication threads are not running"
		if replication.IOThread == model.ThreadRunning && replication.SQLThread == model.ThreadRunning {
			if replicaReadOnly {
				healthState = model.HealthHealthy
				healthSummary = "MySQL replica is reachable, read-only, and replication threads are running"
			} else {
				healthSummary = "MySQL replica is writable while replication is configured"
			}
		}
	} else if !identity.readOnly && !identity.superReadOnly {
		role = model.RolePrimary
		healthState = model.HealthHealthy
		healthSummary = "MySQL primary is reachable and writable"
	}
	promotionEligible := configured && replicaReadOnly && healthState == model.HealthHealthy
	if semiSyncRequired {
		switch {
		case semiSyncErr != nil:
			healthState = model.HealthDegraded
			healthSummary = "MySQL semi-sync durability evidence is unavailable"
			promotionEligible = false
		case role == model.RolePrimary && !semiSync.sourceReady():
			healthState = model.HealthDegraded
			healthSummary = "MySQL primary has no active semi-sync replica acknowledgement"
		case role == model.RoleReplica && !semiSync.replicaReady():
			healthState = model.HealthDegraded
			healthSummary = "MySQL replica is not actively acknowledging semi-sync transactions"
			promotionEligible = false
		}
	}

	engineMetadata := map[string]string{
		"server_id":       identity.serverID,
		"version":         identity.version,
		"gtid_mode":       identity.gtidMode,
		"gtid_executed":   identity.gtidExecuted,
		"gtid_purged":     identity.gtidPurged,
		"log_bin":         identity.logBin,
		"binlog_format":   identity.binlogFormat,
		"read_only":       strconv.FormatBool(identity.readOnly),
		"super_read_only": strconv.FormatBool(identity.superReadOnly),
	}
	for key, value := range semiSync.metadata() {
		engineMetadata[key] = value
	}
	engineMetadata["semi_sync_required"] = strconv.FormatBool(semiSyncRequired)
	if semiSyncErr != nil {
		engineMetadata["semi_sync_probe_error"] = "true"
	}

	displayName := identity.hostname
	if displayName == "" {
		displayName = request.Endpoint.Hostname
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": identity.serverUUID,
		},
		DisplayName: displayName,
		Hostname:    identity.hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        identity.port,
		Role:        role,
		Health: model.Health{
			State:       healthState,
			Summary:     healthSummary,
			ObservedAt:  time.Now().UTC(),
			LatencyMS:   time.Since(started).Milliseconds(),
			Replication: string(replication.SQLThread),
		},
		Replication:       replication,
		PromotionEligible: promotionEligible,
		EngineMetadata:    engineMetadata,
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}
