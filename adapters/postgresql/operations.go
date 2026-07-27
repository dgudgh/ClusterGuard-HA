package postgresql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const postgresqlOperationPrivilegesQuery = `SELECT row_to_json(clusterguard_privileges) FROM (
  SELECT
    current_setting('is_superuser') = 'on' AS superuser,
    has_parameter_privilege(current_user, 'default_transaction_read_only', 'ALTER SYSTEM') AS alter_system,
    has_function_privilege(current_user, 'pg_reload_conf()', 'EXECUTE') AS reload_config,
    pg_has_role(current_user, 'pg_signal_backend', 'MEMBER') AS signal_backends
) AS clusterguard_privileges`

func postgresqlInstanceEndpoint(instance model.DatabaseInstance) adapter.Endpoint {
	return adapter.Endpoint{Hostname: instance.Hostname, IPAddress: instance.IPAddress, Port: instance.Port}
}

func postgresqlBlockingChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func postgresqlObservationToken(resolved *adapter.ResolvedOperation) string {
	if resolved == nil {
		return ""
	}
	if value := strings.TrimSpace(resolved.ObservationToken); value != "" {
		return value
	}
	return string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano)
}

func postgresqlPlanRevisions(resolved *adapter.ResolvedOperation) map[model.ResourceID]uint64 {
	result := map[model.ResourceID]uint64{resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision}
	for _, instance := range resolved.Snapshot.Instances {
		result[instance.ResourceID] = instance.MetadataRevision
	}
	return result
}

func postgresqlStableInstanceIdentity(instance model.DatabaseInstance, systemIdentifier string) bool {
	return model.ValidResourceID(instance.ResourceID) &&
		instance.Engine == model.EnginePostgreSQL &&
		model.ValidResourceID(model.ResourceID(strings.TrimSpace(instance.EngineIdentity["resource_id"]))) &&
		strings.TrimSpace(systemIdentifier) != "" &&
		instance.EngineIdentity["system_identifier"] == systemIdentifier
}

func postgresqlNativeNodeID(instance model.DatabaseInstance) model.ResourceID {
	value := model.ResourceID(strings.TrimSpace(instance.EngineIdentity["resource_id"]))
	if !model.ValidResourceID(value) {
		return ""
	}
	return value
}

func postgresqlSourceIdentityMatches(source model.EngineIdentity, expected model.DatabaseInstance, systemIdentifier string) bool {
	return postgresqlNativeNodeID(expected) != "" &&
		source["resource_id"] == string(postgresqlNativeNodeID(expected)) &&
		strings.TrimSpace(systemIdentifier) != "" && source["system_identifier"] == systemIdentifier
}

func postgresqlInstanceIdentityMatches(live, expected model.DatabaseInstance, systemIdentifier string) bool {
	expectedNodeID := postgresqlNativeNodeID(expected)
	return live.Engine == model.EnginePostgreSQL && expected.Engine == model.EnginePostgreSQL &&
		expectedNodeID != "" && postgresqlNativeNodeID(live) == expectedNodeID &&
		strings.TrimSpace(systemIdentifier) != "" && live.EngineIdentity["system_identifier"] == systemIdentifier
}

func postgresqlInstanceByResourceID(resolved adapter.ResolvedOperation, resourceID model.ResourceID) (model.DatabaseInstance, bool) {
	if resourceID == "" {
		return model.DatabaseInstance{}, false
	}
	if resolved.Primary.ResourceID == resourceID {
		return resolved.Primary, true
	}
	if resolved.Target.ResourceID == resourceID {
		return resolved.Target, true
	}
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID == resourceID {
			return instance, true
		}
	}
	return model.DatabaseInstance{}, false
}

func postgresqlPlanDigest(plan model.OperationPlan) (string, error) {
	canonical := plan
	canonical.ResourceMeta = model.ResourceMeta{}
	canonical.Digest = ""
	contents, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode PostgreSQL operation plan: %w", err)
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func postgresqlCoreChecks(request adapter.OperationRequest) ([]model.Check, error) {
	if request.Resolved == nil || !model.ValidResourceID(request.Operation.ResourceID) || request.Operation.Engine != model.EnginePostgreSQL {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	checks := make([]model.Check, 0, 8)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	systemID := strings.TrimSpace(resolved.Cluster.EngineIdentity["system_identifier"])
	add("cluster_scope", resolved.Cluster.ResourceID == request.Operation.ClusterID && resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID && resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID,
		"operation resources belong to one cluster", "operation resources are outside the selected cluster")
	add("native_identity", postgresqlStableInstanceIdentity(resolved.Primary, systemID) && postgresqlStableInstanceIdentity(resolved.Target, systemID),
		"PostgreSQL cluster and node identities match", "PostgreSQL native identities are incomplete or mismatched")
	add("current_primary", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy && strings.EqualFold(resolved.Primary.EngineMetadata["in_recovery"], "false") && strings.EqualFold(resolved.Primary.EngineMetadata["transaction_read_only"], "false"),
		"current primary is healthy and writable", "current primary state is not safe for planned transition")
	add("target_standby", resolved.Target.Role == model.RoleStandby && resolved.Target.Health.State == model.HealthHealthy && resolved.Target.PromotionEligible,
		"target is a healthy promotion-eligible standby", "target is not a healthy promotion-eligible standby")
	add("target_streaming", resolved.Target.Replication.IOThread == model.ThreadRunning && resolved.Target.Replication.SQLThread == model.ThreadRunning && !strings.EqualFold(resolved.Target.EngineMetadata["replay_paused"], "true"),
		"target WAL receive and replay are active", "target WAL receive or replay is not active")
	add("target_upstream", postgresqlSourceIdentityMatches(resolved.Target.Replication.SourceIdentity, resolved.Primary, systemID),
		"target follows the current primary identity", "target does not follow the current primary identity")
	primaryTimeline := strings.TrimSpace(resolved.Primary.EngineMetadata["timeline_id"])
	add("timeline", primaryTimeline != "" && primaryTimeline == strings.TrimSpace(resolved.Target.EngineMetadata["timeline_id"]),
		"target is on the current primary timeline", "target timeline differs from the current primary")
	primaryLSN, primaryErr := parseLSN(resolved.Primary.EngineMetadata["current_lsn"])
	targetLSN, targetErr := parseLSN(resolved.Target.Replication.ExecutedPosition)
	add("wal_position", primaryErr == nil && targetErr == nil && targetLSN >= primaryLSN,
		"target replay reached the sampled primary WAL position", "target WAL position is missing or behind the sampled primary")
	add("replication_lag", resolved.Target.Replication.LagSeconds != nil && *resolved.Target.Replication.LagSeconds == 0,
		"target replay lag is zero", "target replay lag must be known and zero")
	return checks, nil
}

func (adapterInstance *Adapter) postgresqlOperationPrivileges(ctx context.Context, resolved adapter.ResolvedOperation, instance model.DatabaseInstance, requireBackendSignal bool) model.Check {
	check := model.Check{Name: "postgresql_operation_privileges", Status: model.CheckFail}
	if adapterInstance.executor == nil {
		check.Message = "mutating PostgreSQL SQL execution is not configured"
		return check
	}
	rows, err := adapterInstance.runner.Query(ctx, postgresqlInstanceEndpoint(instance), resolved.Credentials, postgresqlOperationPrivilegesQuery)
	if err != nil || len(rows) != 1 {
		check.Message = "PostgreSQL operation account privileges could not be verified"
		return check
	}
	row := rows[0]
	if strings.EqualFold(row["superuser"], "true") {
		check.Status = model.CheckPass
		check.Message = "PostgreSQL operation account has the required control privileges"
		return check
	}
	missing := make([]string, 0, 3)
	if !strings.EqualFold(row["alter_system"], "true") {
		missing = append(missing, "ALTER SYSTEM on default_transaction_read_only")
	}
	if !strings.EqualFold(row["reload_config"], "true") {
		missing = append(missing, "EXECUTE on pg_reload_conf")
	}
	if requireBackendSignal && !strings.EqualFold(row["signal_backends"], "true") {
		missing = append(missing, "pg_signal_backend membership")
	}
	if len(missing) > 0 {
		check.Message = "PostgreSQL operation account is missing " + strings.Join(missing, ", ")
		return check
	}
	check.Status = model.CheckPass
	check.Message = "PostgreSQL operation account has the required control privileges"
	return check
}

func (adapterInstance *Adapter) switchoverPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationSwitchover {
		return nil, adapter.ErrUnsupported
	}
	checks, err := postgresqlCoreChecks(request)
	if err != nil {
		return nil, err
	}
	checks = append(checks, adapterInstance.nodeController.Precheck(ctx, *request.Resolved)...)
	checks = append(checks, adapterInstance.endpointProvider.Precheck(ctx, *request.Resolved)...)
	status := model.CheckFail
	message := "mutating PostgreSQL SQL execution is not configured"
	if adapterInstance.executor != nil {
		status, message = model.CheckPass, "mutating PostgreSQL SQL execution is configured"
	}
	checks = append(checks, model.Check{Name: "postgresql_sql_executor", Status: status, Message: message})
	checks = append(checks, adapterInstance.postgresqlOperationPrivileges(ctx, *request.Resolved, request.Resolved.Primary, true))
	return checks, nil
}

func (adapterInstance *Adapter) switchoverPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.switchoverPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	steps := []model.PlanStep{
		{Index: 1, Name: "revalidate_topology", Owner: "platform", TargetID: resolved.Cluster.ResourceID, Postcondition: "identities and WAL evidence remain current"},
		{Index: 2, Name: "authorize_target_transition", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target transition lease remains active"},
		{Index: 3, Name: "fence_source_writes", Owner: "postgresql", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "new transactions are read-only and client sessions are drained"},
		{Index: 4, Name: "capture_source_wal", Owner: "postgresql", TargetID: resolved.Primary.ResourceID, Postcondition: "final transactional WAL position is captured after write fencing"},
		{Index: 5, Name: "stop_source", Owner: "postgresql-agent", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "old primary service is stopped"},
		{Index: 6, Name: "verify_source_stopped", Owner: "postgresql-agent", TargetID: resolved.Primary.ResourceID, Postcondition: "old primary is hard fenced"},
		{Index: 7, Name: "wait_target_wal", Owner: "postgresql", TargetID: resolved.Target.ResourceID, Postcondition: "target replay reaches the fenced primary WAL position"},
		{Index: 8, Name: "promote_target", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target is out of recovery"},
		{Index: 9, Name: "activate_target_writes", Owner: "postgresql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "new primary accepts writable transactions"},
		{Index: 10, Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "VIP has one target owner"},
	}
	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		steps = append(steps, model.PlanStep{Index: len(steps) + 1, Name: "repoint_follower_" + string(follower.ResourceID), Owner: "postgresql-agent", TargetID: follower.ResourceID, Mutating: true, Postcondition: "standby streams from the new primary"})
	}
	steps = append(steps,
		model.PlanStep{Index: len(steps) + 1, Name: "rewind_former_primary", Owner: "postgresql-agent", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "former primary returns as a standby"},
		model.PlanStep{Index: len(steps) + 2, Name: "verify_roles_and_endpoint", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "one writer, healthy standbys, and one endpoint owner are verified"},
	)
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID, SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID,
		Stage: model.StagePlan, ObservationToken: postgresqlObservationToken(resolved), ResourceRevisions: postgresqlPlanRevisions(resolved),
		Checks: checks, Steps: steps, Summary: "guarded PostgreSQL planned switchover is ready", Mutating: true,
	}
	if postgresqlBlockingChecks(checks) {
		plan.Summary = "guarded PostgreSQL planned switchover is blocked"
	}
	plan.Digest, err = postgresqlPlanDigest(plan)
	return plan, err
}

func (adapterInstance *Adapter) failoverPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationFailover || request.Resolved == nil || !model.ValidResourceID(request.Operation.ResourceID) || request.Operation.Engine != model.EnginePostgreSQL {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	checks := make([]model.Check, 0, 14)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	addStatus := func(name string, status model.CheckStatus, message string) {
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	systemID := strings.TrimSpace(resolved.Cluster.EngineIdentity["system_identifier"])
	sourceDisconnected := hasCurrentReachableProbe(resolved.Snapshot.Probes, resolved.Target.ResourceID, resolved.Snapshot.ObservedAt) &&
		postgresqlSafeSourceLossEvidence(systemID, resolved.Primary, resolved.Target)
	add("cluster_scope", resolved.Cluster.ResourceID == request.Operation.ClusterID && resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID && resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID,
		"operation resources belong to one cluster", "operation resources are outside the selected cluster")
	add("native_identity", postgresqlStableInstanceIdentity(resolved.Primary, systemID) && postgresqlStableInstanceIdentity(resolved.Target, systemID),
		"PostgreSQL cluster and node identities match", "PostgreSQL native identities are incomplete or mismatched")
	add("primary_failure", resolved.Primary.Health.State != model.HealthHealthy,
		"current primary is reported unhealthy", "fault failover requires an unhealthy current primary")
	if resolved.Target.Role == model.RoleStandby && resolved.Target.Health.State == model.HealthHealthy && resolved.Target.PromotionEligible {
		addStatus("target_standby", model.CheckPass, "target is a healthy promotion-eligible standby")
	} else if sourceDisconnected {
		addStatus("target_standby", model.CheckWarn, "target remains a reachable read-only standby after confirmed primary source loss")
	} else {
		addStatus("target_standby", model.CheckFail, "target is not a safe promotion candidate")
	}
	if hasCurrentHealthyProbe(resolved.Snapshot.Probes, resolved.Target.ResourceID, resolved.Snapshot.ObservedAt) {
		addStatus("target_probe_evidence", model.CheckPass, "current bound probe confirms a healthy target")
	} else if sourceDisconnected {
		addStatus("target_probe_evidence", model.CheckWarn, "current bound probe confirms the degraded standby remains reachable after source loss")
	} else {
		addStatus("target_probe_evidence", model.CheckFail, "current bound target probe is missing, stale, or unreachable")
	}
	if resolved.Target.Replication.IOThread == model.ThreadRunning && resolved.Target.Replication.SQLThread == model.ThreadRunning && !strings.EqualFold(resolved.Target.EngineMetadata["replay_paused"], "true") {
		addStatus("target_streaming", model.CheckPass, "target WAL receive and replay are active")
	} else if sourceDisconnected {
		addStatus("target_streaming", model.CheckWarn, "target replay is complete while the WAL receiver is stopped by primary source loss")
	} else {
		addStatus("target_streaming", model.CheckFail, "target WAL receive or replay state is unsafe")
	}
	add("target_upstream", postgresqlSourceIdentityMatches(resolved.Target.Replication.SourceIdentity, resolved.Primary, systemID),
		"target follows the failed primary identity", "target does not follow the failed primary identity")
	primaryTimeline := strings.TrimSpace(resolved.Primary.EngineMetadata["timeline_id"])
	add("timeline", primaryTimeline != "" && primaryTimeline == strings.TrimSpace(resolved.Target.EngineMetadata["timeline_id"]),
		"target is on the last observed primary timeline", "target timeline differs from the last observed primary")
	primaryLSN, primaryErr := parseLSN(resolved.Primary.EngineMetadata["current_lsn"])
	targetLSN, targetErr := parseLSN(resolved.Target.Replication.ExecutedPosition)
	add("wal_position", primaryErr == nil && targetErr == nil && targetLSN >= primaryLSN,
		"target replay reached the last sampled primary WAL position", "target replay WAL position is missing or behind the last primary sample")
	add("data_loss_risk", resolved.Target.Replication.LagSeconds != nil && *resolved.Target.Replication.LagSeconds == 0,
		"sampled replay lag is zero", "automatic PostgreSQL failover blocks when replay lag is unknown or non-zero")
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) {
		add("postgresql_target_agent", false, "", "restricted PostgreSQL node control is not configured")
	} else {
		running, inRecovery, err := adapterInstance.nodeController.Status(ctx, *resolved, resolved.Target)
		add("postgresql_target_agent", err == nil && running && inRecovery, "target agent proves a running standby", "target agent cannot prove a running standby")
	}
	checks = append(checks, adapterInstance.failoverSafety.Precheck(ctx, *resolved)...)
	add("writer_endpoint_provider", adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx),
		"writer endpoint provider is configured", "a real writer-endpoint provider is required")
	add("postgresql_sql_executor", adapterInstance.executor != nil,
		"mutating PostgreSQL SQL execution is configured", "mutating PostgreSQL SQL execution is not configured")
	checks = append(checks, adapterInstance.postgresqlOperationPrivileges(ctx, *resolved, resolved.Target, false))
	return checks, nil
}

func (adapterInstance *Adapter) failoverPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.failoverPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	steps := []model.PlanStep{
		{Index: 1, Name: "revalidate_failure_and_target", Owner: "platform", TargetID: resolved.Cluster.ResourceID, Postcondition: "failure evidence and target identity remain current"},
		{Index: 2, Name: "fence_old_primary", Owner: "safety", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "old primary cannot serve writes or own the VIP"},
		{Index: 3, Name: "verify_old_primary_fenced", Owner: "safety", TargetID: resolved.Primary.ResourceID, Postcondition: "old-primary isolation is independently verified"},
		{Index: 4, Name: "authorize_target_transition", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target transition lease remains active"},
		{Index: 5, Name: "promote_target", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target is out of recovery"},
		{Index: 6, Name: "activate_target_writes", Owner: "postgresql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "new primary accepts writable transactions"},
		{Index: 7, Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "VIP has one target owner"},
	}
	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		steps = append(steps, model.PlanStep{Index: len(steps) + 1, Name: "repoint_follower_" + string(follower.ResourceID), Owner: "postgresql-agent", TargetID: follower.ResourceID, Mutating: true, Postcondition: "reachable standby streams from the new primary"})
	}
	steps = append(steps, model.PlanStep{Index: len(steps) + 1, Name: "verify_failover", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "one writer, one endpoint owner, and old-primary isolation are verified"})
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID, SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID,
		Stage: model.StagePlan, ObservationToken: postgresqlObservationToken(resolved), ResourceRevisions: postgresqlPlanRevisions(resolved),
		Checks: checks, Steps: steps, Summary: "guarded PostgreSQL fault failover is ready", Mutating: true,
	}
	if postgresqlBlockingChecks(checks) {
		plan.Summary = "guarded PostgreSQL fault failover is blocked"
	}
	plan.Digest, err = postgresqlPlanDigest(plan)
	return plan, err
}

func postgresqlEndpointSafe(checks []model.Check) bool {
	for _, check := range checks {
		if check.Name == "writer_endpoint_provider" && check.Status == model.CheckPass {
			return true
		}
	}
	return false
}

func postgresqlMajorVersion(value string) (uint64, error) {
	major := strings.TrimSpace(value)
	if index := strings.IndexByte(major, '.'); index >= 0 {
		major = major[:index]
	}
	parsed, err := strconv.ParseUint(major, 10, 16)
	if err != nil || parsed < 10 {
		return 0, fmt.Errorf("invalid PostgreSQL version %q", value)
	}
	return parsed, nil
}

func postgresqlRewindReady(instance model.DatabaseInstance) bool {
	hints, hintsErr := parsePostgreSQLBoolean(instance.EngineMetadata["wal_log_hints"])
	checksums, checksumErr := strconv.ParseUint(strings.TrimSpace(instance.EngineMetadata["data_checksum_version"]), 10, 32)
	return (hintsErr == nil && hints) || (checksumErr == nil && checksums > 0)
}

func (adapterInstance *Adapter) rejoinPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationFormerPrimaryRejoin || request.Resolved == nil || request.Operation.Engine != model.EnginePostgreSQL || !model.ValidResourceID(request.Operation.ResourceID) {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	checks := make([]model.Check, 0, 10)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	systemID := strings.TrimSpace(resolved.Cluster.EngineIdentity["system_identifier"])
	add("cluster_scope", resolved.Cluster.ResourceID == request.Operation.ClusterID && resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID && resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID && resolved.Primary.ResourceID != resolved.Target.ResourceID,
		"current and former primary are distinct inventory resources", "current and former primary must be distinct resources in the selected cluster")
	add("native_identity", postgresqlStableInstanceIdentity(resolved.Primary, systemID) && postgresqlStableInstanceIdentity(resolved.Target, systemID),
		"PostgreSQL native identities match", "PostgreSQL native identities are incomplete or mismatched")
	add("current_primary", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy && strings.EqualFold(resolved.Primary.EngineMetadata["in_recovery"], "false") && strings.EqualFold(resolved.Primary.EngineMetadata["transaction_read_only"], "false"),
		"current primary is healthy and writable", "current primary must be healthy and writable")
	currentMajor, currentVersionErr := postgresqlMajorVersion(resolved.Primary.EngineMetadata["version"])
	formerMajor, formerVersionErr := postgresqlMajorVersion(resolved.Target.EngineMetadata["version"])
	add("version_compatibility", currentVersionErr == nil && formerVersionErr == nil && currentMajor == formerMajor,
		"current and former primary use the same PostgreSQL major release", "pg_rewind requires matching PostgreSQL major releases")
	add("rewind_prerequisite", postgresqlRewindReady(resolved.Target),
		"data checksums or wal_log_hints allow pg_rewind", "pg_rewind is unsafe without data checksums or wal_log_hints; use full node synchronization")
	currentTimeline, currentTimelineErr := strconv.ParseUint(strings.TrimSpace(resolved.Primary.EngineMetadata["timeline_id"]), 10, 32)
	formerTimeline, formerTimelineErr := strconv.ParseUint(strings.TrimSpace(resolved.Target.EngineMetadata["timeline_id"]), 10, 32)
	add("timeline_history", currentTimelineErr == nil && formerTimelineErr == nil && currentTimeline >= formerTimeline,
		"current primary timeline is not older than the former primary", "PostgreSQL timeline evidence is invalid for rewind")
	add("replication_credentials", strings.TrimSpace(resolved.ReplicationCredentials.Username) != "" || strings.TrimSpace(request.ReplicationCredentials.Username) != "",
		"replication credentials are configured", "replication credentials are required for former-primary recovery")
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) {
		add("former_primary_agent", false, "", "restricted PostgreSQL node control is not configured")
	} else {
		_, _, statusErr := adapterInstance.nodeController.Status(ctx, *resolved, resolved.Target)
		add("former_primary_agent", statusErr == nil, "former-primary agent is reachable for controlled stop and rewind", "former-primary agent status is unavailable")
	}
	providerChecks := adapterInstance.endpointProvider.Precheck(ctx, *resolved)
	add("former_primary_vip_absent", postgresqlEndpointSafe(providerChecks),
		"writer endpoint remains owned only by the current primary", "writer endpoint ownership is unsafe for former-primary recovery")
	return checks, nil
}

func (adapterInstance *Adapter) rejoinPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.rejoinPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID,
		SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID, Stage: model.StagePlan,
		ObservationToken: postgresqlObservationToken(resolved), ResourceRevisions: postgresqlPlanRevisions(resolved),
		Checks: checks, Mutating: true,
		Steps: []model.PlanStep{
			{Index: 1, Name: "revalidate_former_primary", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "identity, version, timeline, and VIP ownership remain safe"},
			{Index: 2, Name: "stop_former_primary", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "former primary service is stopped"},
			{Index: 3, Name: "verify_former_primary_stopped", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Postcondition: "former primary cannot serve writes"},
			{Index: 4, Name: "rewind_former_primary", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "former primary starts in recovery and follows the current primary"},
			{Index: 5, Name: "verify_former_primary", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "former primary is a healthy standby without endpoint ownership"},
		},
		Summary: "PostgreSQL former primary is ready for guarded pg_rewind recovery",
	}
	if postgresqlBlockingChecks(checks) {
		plan.Summary = "PostgreSQL former-primary rewind is blocked; use controlled full node synchronization"
	}
	plan.Digest, err = postgresqlPlanDigest(plan)
	return plan, err
}

const (
	postgresqlRepairCollect = "collect_replication_status"
	postgresqlRepairResume  = "resume_wal_replay"
	postgresqlRepairReload  = "reload_configuration"
)

func postgresqlRepairAction(request adapter.OperationRequest) string {
	return strings.ToLower(strings.TrimSpace(request.Parameters["action"]))
}

func (adapterInstance *Adapter) repairPrecheck(_ context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationReplicationRepair || request.Resolved == nil || request.Operation.Engine != model.EnginePostgreSQL {
		return nil, adapter.ErrUnsupported
	}
	action := postgresqlRepairAction(request)
	allowed := action == postgresqlRepairCollect || action == postgresqlRepairResume || action == postgresqlRepairReload
	checks := make([]model.Check, 0, 4)
	status, message := model.CheckFail, "repair action is outside the PostgreSQL low-risk allowlist"
	if allowed {
		status, message = model.CheckPass, "repair action is in the PostgreSQL low-risk allowlist"
	}
	checks = append(checks, model.Check{Name: "repair_action_allowlist", Status: status, Message: message})
	target := request.Resolved.Target
	identityReady := target.ClusterID == request.Resolved.Cluster.ResourceID && postgresqlStableInstanceIdentity(target, request.Resolved.Cluster.EngineIdentity["system_identifier"])
	status, message = model.CheckFail, "repair target must be an inventory PostgreSQL resource with native identity"
	if identityReady {
		status, message = model.CheckPass, "repair target is an immutable PostgreSQL inventory resource"
	}
	checks = append(checks, model.Check{Name: "repair_target_identity", Status: status, Message: message})
	if action == postgresqlRepairResume {
		standby := target.Role == model.RoleStandby && strings.EqualFold(target.EngineMetadata["in_recovery"], "true")
		status, message = model.CheckFail, "WAL replay can only be resumed on a standby"
		if standby {
			status, message = model.CheckPass, "target is a PostgreSQL standby"
		}
		checks = append(checks, model.Check{Name: "repair_standby_role", Status: status, Message: message})
	}
	if (action == postgresqlRepairResume || action == postgresqlRepairReload) && adapterInstance.executor == nil {
		checks = append(checks, model.Check{Name: "postgresql_sql_executor", Status: model.CheckFail, Message: "mutating PostgreSQL SQL execution is not configured"})
	}
	return checks, nil
}

func (adapterInstance *Adapter) repairPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.repairPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	action := postgresqlRepairAction(request)
	mutating := action == postgresqlRepairResume || action == postgresqlRepairReload
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID,
		SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID, Stage: model.StagePlan,
		ObservationToken: postgresqlObservationToken(resolved), ResourceRevisions: postgresqlPlanRevisions(resolved),
		Checks: checks, Mutating: mutating,
		Steps:   []model.PlanStep{{Index: 1, Name: "repair_" + action, Owner: "postgresql", TargetID: resolved.Target.ResourceID, Mutating: mutating, Postcondition: "requested PostgreSQL repair postcondition is verified"}},
		Summary: "low-risk PostgreSQL replication repair is ready",
	}
	if postgresqlBlockingChecks(checks) {
		plan.Summary = "PostgreSQL replication repair is blocked"
	}
	plan.Digest, err = postgresqlPlanDigest(plan)
	return plan, err
}
