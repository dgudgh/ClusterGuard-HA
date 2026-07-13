package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func mysqlStringLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
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
		return "CHANGE REPLICATION SOURCE TO " +
			"SOURCE_HOST=" + mysqlStringLiteral(host) + ", " +
			"SOURCE_PORT=" + strconv.Itoa(target.Port) + ", " +
			"SOURCE_USER=" + mysqlStringLiteral(credentials.Username) + ", " +
			"SOURCE_PASSWORD=" + mysqlStringLiteral(credentials.Password) + ", " +
			"SOURCE_AUTO_POSITION=1", nil
	}
	return "CHANGE MASTER TO " +
		"MASTER_HOST=" + mysqlStringLiteral(host) + ", " +
		"MASTER_PORT=" + strconv.Itoa(target.Port) + ", " +
		"MASTER_USER=" + mysqlStringLiteral(credentials.Username) + ", " +
		"MASTER_PASSWORD=" + mysqlStringLiteral(credentials.Password) + ", " +
		"MASTER_AUTO_POSITION=1", nil
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
	status, configured, err := probeReplication(ctx, adapterInstance.runner, endpoint, administrative)
	if err != nil || !configured {
		return fmt.Errorf("verify follower replication configuration")
	}
	targetUUID := strings.ToLower(strings.TrimSpace(target.EngineIdentity["server_uuid"]))
	if strings.ToLower(strings.TrimSpace(status.SourceIdentity["server_uuid"])) != targetUUID || status.IOThread != model.ThreadRunning || status.SQLThread != model.ThreadRunning {
		return fmt.Errorf("follower does not replicate from the selected target")
	}
	return nil
}

func (adapterInstance *Adapter) verifyFollower(ctx context.Context, follower model.DatabaseInstance, target model.DatabaseInstance, credentials adapter.Credentials) ([]model.Check, bool) {
	suffix := string(follower.ResourceID)
	checks := make([]model.Check, 0, 3)
	identity, err := probeIdentity(ctx, adapterInstance.runner, instanceEndpoint(follower), credentials)
	if err != nil {
		return []model.Check{{Name: "follower_reachable_" + suffix, Status: model.CheckFail, Message: "follower reachability is unknown"}}, false
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
	status, configured, err := probeReplication(ctx, adapterInstance.runner, instanceEndpoint(follower), credentials)
	targetUUID := strings.ToLower(strings.TrimSpace(target.EngineIdentity["server_uuid"]))
	if err == nil && configured && strings.ToLower(strings.TrimSpace(status.SourceIdentity["server_uuid"])) == targetUUID && status.IOThread == model.ThreadRunning && status.SQLThread == model.ThreadRunning && status.LagSeconds != nil && *status.LagSeconds == 0 {
		checks = append(checks, model.Check{Name: "follower_replication_" + suffix, Status: model.CheckPass, Message: "follower replication is healthy on the selected target"})
	} else {
		checks = append(checks, model.Check{Name: "follower_replication_" + suffix, Status: model.CheckFail, Message: "follower does not have verified healthy replication from the selected target"})
	}
	return checks, !identity.readOnly && !identity.superReadOnly
}
