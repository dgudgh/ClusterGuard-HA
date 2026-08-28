package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	followerReplicationStartTimeout  = 30 * time.Second
	followerReplicationPollInterval  = 250 * time.Millisecond
	followerReplicationStableSamples = 3
	replicationSourceConnectRetry    = 5
	replicationSourceRetryCount      = 86400
)

func mysqlStringLiteral(value string) string {
	value = strings.ReplaceAll(value, `'`, `''`)
	return "'" + value + "'"
}

func mysqlLiteralStatement(statement string, values ...string) string {
	for _, value := range values {
		if strings.Contains(value, `\`) {
			return "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES'; " + statement
		}
	}
	return statement
}

func buildChangeSourceStatement(version string, target model.DatabaseInstance, credentials adapter.Credentials) (string, error) {
	dialect, err := dialectForVersion(version)
	if err != nil {
		return "", err
	}
	host := strings.TrimSpace(target.IPAddress)
	if host == "" {
		host = strings.TrimSpace(target.Hostname)
	}
	if host == "" || target.Port <= 0 || target.Port > 65535 || strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
		return "", fmt.Errorf("replication source endpoint and credentials are required")
	}
	if dialect.ModernSource {
		statement := "CHANGE REPLICATION SOURCE TO " +
			"SOURCE_HOST=" + mysqlStringLiteral(host) + ", " +
			"SOURCE_PORT=" + strconv.Itoa(target.Port) + ", " +
			"SOURCE_USER=" + mysqlStringLiteral(credentials.Username) + ", " +
			"SOURCE_PASSWORD=" + mysqlStringLiteral(credentials.Password) + ", " +
			"SOURCE_AUTO_POSITION=1, " +
			"SOURCE_CONNECT_RETRY=" + strconv.Itoa(replicationSourceConnectRetry) + ", " +
			"SOURCE_RETRY_COUNT=" + strconv.Itoa(replicationSourceRetryCount) + ", " +
			dialect.PublicKeySourceOption
		return mysqlLiteralStatement(statement, host, credentials.Username, credentials.Password), nil
	}
	statement := "CHANGE MASTER TO " +
		"MASTER_HOST=" + mysqlStringLiteral(host) + ", " +
		"MASTER_PORT=" + strconv.Itoa(target.Port) + ", " +
		"MASTER_USER=" + mysqlStringLiteral(credentials.Username) + ", " +
		"MASTER_PASSWORD=" + mysqlStringLiteral(credentials.Password) + ", " +
		"MASTER_AUTO_POSITION=1, " +
		"MASTER_CONNECT_RETRY=" + strconv.Itoa(replicationSourceConnectRetry) + ", " +
		"MASTER_RETRY_COUNT=" + strconv.Itoa(replicationSourceRetryCount)
	if dialect.PublicKeySourceOption != "" {
		statement += ", " + dialect.PublicKeySourceOption
	}
	return mysqlLiteralStatement(statement, host, credentials.Username, credentials.Password), nil
}

func waitForFollowerReplicationHealthy(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials, targetUUID string, maximumLag int64) (model.ReplicationStatus, error) {
	waitContext, cancel := context.WithTimeout(ctx, followerReplicationStartTimeout)
	defer cancel()
	ticker := time.NewTicker(followerReplicationPollInterval)
	defer ticker.Stop()
	var lastError error
	stableSamples := 0
	for {
		status, configured, err := probeReplication(waitContext, runner, endpoint, credentials)
		if err == nil && configured && strings.ToLower(strings.TrimSpace(status.SourceIdentity["server_uuid"])) == targetUUID &&
			status.IOThread == model.ThreadRunning && status.SQLThread == model.ThreadRunning &&
			status.LagSeconds != nil && *status.LagSeconds >= 0 && *status.LagSeconds <= maximumLag {
			stableSamples++
			if stableSamples >= followerReplicationStableSamples {
				return status, nil
			}
		} else {
			stableSamples = 0
		}
		lastError = err
		select {
		case <-waitContext.Done():
			if lastError != nil {
				return model.ReplicationStatus{}, fmt.Errorf("replication health did not converge: %w", lastError)
			}
			return model.ReplicationStatus{}, fmt.Errorf("replication source, threads, or lag did not remain within the %ds maximum for %d samples in %s", maximumLag, followerReplicationStableSamples, followerReplicationStartTimeout)
		case <-ticker.C:
		}
	}
}

func (adapterInstance *Adapter) reparentFollower(ctx context.Context, follower model.DatabaseInstance, target model.DatabaseInstance, administrative adapter.Credentials, replication adapter.Credentials) error {
	endpoint := instanceEndpoint(follower)
	if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, setSuperReadOnlyOn); err != nil {
		return fmt.Errorf("fence follower: %w", err)
	}
	if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, setReadOnlyOn); err != nil {
		return fmt.Errorf("confirm follower read-only: %w", err)
	}
	_, configured, err := probeReplication(ctx, adapterInstance.runner, endpoint, administrative)
	if err != nil {
		return fmt.Errorf("probe follower replication: %w", err)
	}
	dialect, err := dialectForVersion(follower.EngineMetadata["version"])
	if err != nil {
		return err
	}
	if configured {
		if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, dialect.StopReplication); err != nil {
			return fmt.Errorf("stop follower replication: %w", err)
		}
		if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, dialect.ResetReplication); err != nil {
			return fmt.Errorf("reset follower replication: %w", err)
		}
	}
	statement, err := buildChangeSourceStatement(follower.EngineMetadata["version"], target, replication)
	if err != nil {
		return err
	}
	if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, statement); err != nil {
		return fmt.Errorf("configure follower source: %w", err)
	}
	if err := adapterInstance.executor.Exec(ctx, endpoint, administrative, dialect.StartReplication); err != nil {
		return fmt.Errorf("start follower replication: %w", err)
	}
	targetUUID := strings.ToLower(strings.TrimSpace(target.EngineIdentity["server_uuid"]))
	maximumLag := adapterInstance.replicationLagMaximum()
	if _, err := waitForFollowerReplicationHealthy(ctx, adapterInstance.runner, endpoint, administrative, targetUUID, maximumLag); err != nil {
		return fmt.Errorf("verify follower replication configuration: %w", err)
	}
	targetRows, err := adapterInstance.runner.Query(ctx, instanceEndpoint(target), administrative, gtidPositionQuery)
	if err != nil || len(targetRows) != 1 {
		if err == nil {
			err = fmt.Errorf("target GTID response is invalid")
		}
		return fmt.Errorf("capture target GTID for follower catch-up: %w", err)
	}
	if targetGTID := strings.TrimSpace(targetRows[0]["gtid_executed"]); targetGTID != "" {
		if err := waitForExecutedGTIDSet(ctx, adapterInstance.runner, endpoint, administrative, targetGTID); err != nil {
			return fmt.Errorf("wait for follower GTID catch-up: %w", err)
		}
	}
	if _, err := waitForFollowerReplicationHealthy(ctx, adapterInstance.runner, endpoint, administrative, targetUUID, maximumLag); err != nil {
		return fmt.Errorf("follower did not remain healthy after GTID catch-up: %w", err)
	}
	return nil
}

func (adapterInstance *Adapter) verifyFollower(ctx context.Context, follower model.DatabaseInstance, target model.DatabaseInstance, credentials adapter.Credentials) ([]model.Check, bool) {
	checks, writable, observed := adapterInstance.verifyFollowerWriterState(ctx, follower, credentials)
	if !observed {
		return checks, writable
	}
	checks = append(checks, adapterInstance.verifyFollowerReplication(ctx, follower, target, credentials))
	return checks, writable
}

func (adapterInstance *Adapter) verifyFollowerWriterState(ctx context.Context, follower model.DatabaseInstance, credentials adapter.Credentials) ([]model.Check, bool, bool) {
	suffix := string(follower.ResourceID)
	checks := make([]model.Check, 0, 2)
	identity, err := probeIdentity(ctx, adapterInstance.runner, instanceEndpoint(follower), credentials)
	if err != nil {
		return []model.Check{{Name: "follower_reachable_" + suffix, Status: model.CheckFail, Message: "follower reachability is unknown"}}, false, false
	}
	expectedUUID := strings.ToLower(strings.TrimSpace(follower.EngineIdentity["server_uuid"]))
	if identity.serverUUID == expectedUUID {
		checks = append(checks, model.Check{Name: "follower_identity_" + suffix, Status: model.CheckPass, Message: "follower identity matches the immutable resource"})
	} else {
		checks = append(checks, model.Check{Name: "follower_identity_" + suffix, Status: model.CheckFail, Message: "follower identity changed"})
	}
	if identity.readOnly && identity.superReadOnly {
		checks = append(checks, model.Check{Name: "follower_read_only_" + suffix, Status: model.CheckPass, Message: "follower is fully read-only"})
	} else {
		checks = append(checks, model.Check{Name: "follower_read_only_" + suffix, Status: model.CheckFail, Message: "follower is not fully read-only"})
	}
	return checks, !identity.readOnly && !identity.superReadOnly, true
}

func (adapterInstance *Adapter) verifyFollowerReplication(ctx context.Context, follower model.DatabaseInstance, target model.DatabaseInstance, credentials adapter.Credentials) model.Check {
	suffix := string(follower.ResourceID)
	status, configured, err := probeReplication(ctx, adapterInstance.runner, instanceEndpoint(follower), credentials)
	targetUUID := strings.ToLower(strings.TrimSpace(target.EngineIdentity["server_uuid"]))
	maximumLag := adapterInstance.replicationLagMaximum()
	if err == nil && configured && strings.ToLower(strings.TrimSpace(status.SourceIdentity["server_uuid"])) == targetUUID &&
		status.IOThread == model.ThreadRunning && status.SQLThread == model.ThreadRunning &&
		status.LagSeconds != nil && *status.LagSeconds >= 0 && *status.LagSeconds <= maximumLag {
		return model.Check{
			Name:   "follower_replication_" + suffix,
			Status: model.CheckPass,
			Message: fmt.Sprintf(
				"follower replication is healthy on the selected target with %ds lag within the %ds verification maximum",
				*status.LagSeconds,
				maximumLag,
			),
		}
	}
	return model.Check{
		Name:   "follower_replication_" + suffix,
		Status: model.CheckFail,
		Message: fmt.Sprintf(
			"follower replication source, threads, or lag does not satisfy the %ds verification maximum",
			maximumLag,
		),
	}
}
