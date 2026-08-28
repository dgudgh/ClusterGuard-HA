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
	semiSyncVariablesQuery = "SHOW GLOBAL VARIABLES LIKE 'rpl_semi_sync%'"
	semiSyncStatusQuery    = "SHOW GLOBAL STATUS LIKE 'Rpl_semi_sync%'"

	disableSemiSyncSource = "SET GLOBAL rpl_semi_sync_source_enabled = OFF"
	enableSemiSyncSource  = "SET GLOBAL rpl_semi_sync_source_enabled = ON"
	disableSemiSyncMaster = "SET GLOBAL rpl_semi_sync_master_enabled = OFF"
	enableSemiSyncMaster  = "SET GLOBAL rpl_semi_sync_master_enabled = ON"
)

type semiSyncProbe struct {
	available           bool
	sourceEnabled       bool
	sourceStatus        bool
	sourceClients       int
	replicaEnabled      bool
	replicaStatus       bool
	waitForReplicaCount int
	sourceTimeoutMS     int
	waitNoReplica       bool
	waitPoint           string
}

func probeSemiSync(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (semiSyncProbe, error) {
	variables, err := runner.Query(ctx, endpoint, credentials, semiSyncVariablesQuery)
	if err != nil {
		return semiSyncProbe{}, fmt.Errorf("probe MySQL semi-sync variables: %w", err)
	}
	status, err := runner.Query(ctx, endpoint, credentials, semiSyncStatusQuery)
	if err != nil {
		return semiSyncProbe{}, fmt.Errorf("probe MySQL semi-sync status: %w", err)
	}
	return parseSemiSync(variables, status)
}

func parseSemiSync(variableRows, statusRows []Row) (semiSyncProbe, error) {
	variables := semiSyncValues(variableRows)
	status := semiSyncValues(statusRows)
	probe := semiSyncProbe{available: len(variables) > 0 || len(status) > 0}
	var err error
	if probe.sourceEnabled, err = semiSyncBoolean(variables, "rpl_semi_sync_source_enabled", "rpl_semi_sync_master_enabled"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.replicaEnabled, err = semiSyncBoolean(variables, "rpl_semi_sync_replica_enabled", "rpl_semi_sync_slave_enabled"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.sourceStatus, err = semiSyncBoolean(status, "rpl_semi_sync_source_status", "rpl_semi_sync_master_status"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.replicaStatus, err = semiSyncBoolean(status, "rpl_semi_sync_replica_status", "rpl_semi_sync_slave_status"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.waitNoReplica, err = semiSyncBoolean(variables, "rpl_semi_sync_source_wait_no_replica", "rpl_semi_sync_master_wait_no_slave"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.sourceClients, err = semiSyncInteger(status, "rpl_semi_sync_source_clients", "rpl_semi_sync_master_clients"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.waitForReplicaCount, err = semiSyncInteger(variables, "rpl_semi_sync_source_wait_for_replica_count", "rpl_semi_sync_master_wait_for_slave_count"); err != nil {
		return semiSyncProbe{}, err
	}
	if probe.sourceTimeoutMS, err = semiSyncInteger(variables, "rpl_semi_sync_source_timeout", "rpl_semi_sync_master_timeout"); err != nil {
		return semiSyncProbe{}, err
	}
	probe.waitPoint = strings.ToUpper(semiSyncValue(variables, "rpl_semi_sync_source_wait_point", "rpl_semi_sync_master_wait_point"))
	return probe, nil
}

func semiSyncValues(rows []Row) map[string]string {
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		name := strings.ToLower(strings.TrimSpace(first(row, "Variable_name", "VARIABLE_NAME")))
		if name == "" {
			continue
		}
		values[name] = strings.TrimSpace(first(row, "Value", "VARIABLE_VALUE"))
	}
	return values
}

func semiSyncValue(values map[string]string, names ...string) string {
	for _, name := range names {
		if value, found := values[strings.ToLower(name)]; found {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func semiSyncBoolean(values map[string]string, names ...string) (bool, error) {
	value := semiSyncValue(values, names...)
	if value == "" {
		return false, nil
	}
	parsed, err := parseMySQLBoolean(value)
	if err != nil {
		return false, fmt.Errorf("invalid MySQL semi-sync boolean %q: %w", value, err)
	}
	return parsed, nil
}

func semiSyncInteger(values map[string]string, names ...string) (int, error) {
	value := semiSyncValue(values, names...)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid MySQL semi-sync integer %q", value)
	}
	return parsed, nil
}

func (probe semiSyncProbe) sourceConfigured() bool {
	return probe.available && probe.sourceEnabled && probe.waitForReplicaCount >= 1 && probe.waitPoint == "AFTER_SYNC"
}

func (probe semiSyncProbe) sourceReady() bool {
	return probe.sourceConfigured() && probe.sourceStatus && probe.sourceClients >= probe.waitForReplicaCount
}

func (probe semiSyncProbe) replicaReady() bool {
	return probe.sourceConfigured() && probe.replicaEnabled && probe.replicaStatus
}

func (probe semiSyncProbe) metadata() map[string]string {
	return map[string]string{
		"semi_sync_available":              strconv.FormatBool(probe.available),
		"semi_sync_source_enabled":         strconv.FormatBool(probe.sourceEnabled),
		"semi_sync_source_status":          strconv.FormatBool(probe.sourceStatus),
		"semi_sync_source_clients":         strconv.Itoa(probe.sourceClients),
		"semi_sync_replica_enabled":        strconv.FormatBool(probe.replicaEnabled),
		"semi_sync_replica_status":         strconv.FormatBool(probe.replicaStatus),
		"semi_sync_wait_for_replica_count": strconv.Itoa(probe.waitForReplicaCount),
		"semi_sync_source_timeout_ms":      strconv.Itoa(probe.sourceTimeoutMS),
		"semi_sync_wait_no_replica":        strconv.FormatBool(probe.waitNoReplica),
		"semi_sync_wait_point":             probe.waitPoint,
	}
}

func semiSyncFromMetadata(metadata map[string]string) (semiSyncProbe, bool) {
	readBool := func(name string) (bool, bool) {
		value, found := metadata[name]
		if !found {
			return false, false
		}
		parsed, err := parseMySQLBoolean(value)
		return parsed, err == nil
	}
	readInt := func(name string) (int, bool) {
		value, found := metadata[name]
		if !found {
			return 0, false
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil && parsed >= 0
	}
	probe := semiSyncProbe{}
	var known bool
	if probe.available, known = readBool("semi_sync_available"); !known {
		return semiSyncProbe{}, false
	}
	fields := []struct {
		name   string
		target *bool
	}{
		{"semi_sync_source_enabled", &probe.sourceEnabled},
		{"semi_sync_source_status", &probe.sourceStatus},
		{"semi_sync_replica_enabled", &probe.replicaEnabled},
		{"semi_sync_replica_status", &probe.replicaStatus},
		{"semi_sync_wait_no_replica", &probe.waitNoReplica},
	}
	for _, field := range fields {
		if *field.target, known = readBool(field.name); !known {
			return semiSyncProbe{}, false
		}
	}
	if probe.sourceClients, known = readInt("semi_sync_source_clients"); !known {
		return semiSyncProbe{}, false
	}
	if probe.waitForReplicaCount, known = readInt("semi_sync_wait_for_replica_count"); !known {
		return semiSyncProbe{}, false
	}
	if probe.sourceTimeoutMS, known = readInt("semi_sync_source_timeout_ms"); !known {
		return semiSyncProbe{}, false
	}
	probe.waitPoint = strings.ToUpper(strings.TrimSpace(metadata["semi_sync_wait_point"]))
	if probe.waitPoint == "" {
		return semiSyncProbe{}, false
	}
	return probe, true
}

func semiSyncSwitchoverCheck(primary, target model.DatabaseInstance) model.Check {
	primaryProbe, primaryKnown := semiSyncFromMetadata(primary.EngineMetadata)
	targetProbe, targetKnown := semiSyncFromMetadata(target.EngineMetadata)
	if !primaryKnown || !targetKnown {
		return model.Check{Name: "semi_sync_durability", Status: model.CheckFail, Message: "current semi-sync evidence is incomplete"}
	}
	if !primaryProbe.sourceReady() {
		return model.Check{Name: "semi_sync_durability", Status: model.CheckFail, Message: "current primary has no active semi-sync replica acknowledgement"}
	}
	if !targetProbe.replicaReady() {
		return model.Check{Name: "semi_sync_durability", Status: model.CheckFail, Message: "selected target is not an active semi-sync replica with promotion-ready source configuration"}
	}
	return model.Check{Name: "semi_sync_durability", Status: model.CheckPass, Message: "semi-sync acknowledgement and target promotion configuration are ready"}
}

func semiSyncFailoverCheck(target model.DatabaseInstance) model.Check {
	targetProbe, known := semiSyncFromMetadata(target.EngineMetadata)
	if !known || !targetProbe.replicaReady() {
		return model.Check{Name: "semi_sync_durability", Status: model.CheckFail, Message: "selected failover target lacks current active semi-sync replica evidence"}
	}
	return model.Check{Name: "semi_sync_durability", Status: model.CheckPass, Message: "selected failover target has current active semi-sync replica evidence"}
}

func verifySemiSyncPrimary(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) model.Check {
	probe, err := probeSemiSync(ctx, runner, endpoint, credentials)
	if err != nil {
		return model.Check{Name: "semi_sync_new_primary", Status: model.CheckFail, Message: "new primary semi-sync state is unknown: " + err.Error()}
	}
	if !probe.sourceReady() {
		return model.Check{Name: "semi_sync_new_primary", Status: model.CheckFail, Message: "new primary has no active semi-sync replica acknowledgement"}
	}
	return model.Check{Name: "semi_sync_new_primary", Status: model.CheckPass, Message: "new primary has active semi-sync replica acknowledgement"}
}

func activateSemiSyncSource(ctx context.Context, runner SQLRunner, executor SQLExecutor, endpoint adapter.Endpoint, credentials adapter.Credentials, version string) error {
	probe, err := probeSemiSync(ctx, runner, endpoint, credentials)
	if err != nil {
		return err
	}
	if probe.sourceReady() {
		return nil
	}
	if !probe.sourceConfigured() {
		return fmt.Errorf("promoted MySQL instance lacks complete semi-sync source configuration")
	}
	dialect, err := dialectForVersion(version)
	if err != nil {
		return err
	}
	disableStatement := disableSemiSyncMaster
	enableStatement := enableSemiSyncMaster
	if dialect.ModernSource {
		disableStatement = disableSemiSyncSource
		enableStatement = enableSemiSyncSource
	}
	if err := executor.Exec(ctx, endpoint, credentials, disableStatement); err != nil {
		return fmt.Errorf("disable promoted semi-sync source before reactivation: %w", err)
	}
	if err := executor.Exec(ctx, endpoint, credentials, enableStatement); err != nil {
		return fmt.Errorf("reactivate promoted semi-sync source: %w", err)
	}

	const attempts = 40
	for attempt := 1; attempt <= attempts; attempt++ {
		probe, err = probeSemiSync(ctx, runner, endpoint, credentials)
		if err == nil && probe.sourceReady() {
			return nil
		}
		if attempt == attempts {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return fmt.Errorf("verify promoted semi-sync source reactivation: %w", err)
	}
	return fmt.Errorf(
		"promoted semi-sync source is not ready after reactivation (clients=%d required=%d status=%t)",
		probe.sourceClients,
		probe.waitForReplicaCount,
		probe.sourceStatus,
	)
}
