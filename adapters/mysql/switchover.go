package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func appendSwitchoverCheck(checks *[]model.Check, name string, status model.CheckStatus, message string) {
	*checks = append(*checks, model.Check{Name: name, Status: status, Message: message})
}

func boolMetadata(metadata map[string]string, name string) (bool, bool) {
	value, found := metadata[name]
	if !found {
		return false, false
	}
	parsed, err := parseMySQLBoolean(value)
	return parsed, err == nil
}

func planHasBlockingChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func (adapterInstance *Adapter) switchoverPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationSwitchover {
		return nil, adapter.ErrUnsupported
	}
	if request.Resolved == nil {
		return nil, fmt.Errorf("resolved MySQL operation context is required")
	}
	resolved := *request.Resolved
	checks := make([]model.Check, 0, 20)

	validScope := request.Operation.Engine == model.EngineMySQL &&
		resolved.Cluster.Engine == model.EngineMySQL &&
		request.Operation.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Primary.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Target.ClusterID == resolved.Cluster.ResourceID &&
		request.TargetID == resolved.Target.ResourceID &&
		model.ValidResourceID(resolved.Primary.ResourceID) && model.ValidResourceID(resolved.Target.ResourceID)
	if validScope {
		appendSwitchoverCheck(&checks, "inventory_membership", model.CheckPass, "source and target are UUID-scoped members of the selected MySQL cluster")
	} else {
		appendSwitchoverCheck(&checks, "inventory_membership", model.CheckFail, "source or target is outside the selected MySQL cluster inventory")
	}

	if resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy {
		appendSwitchoverCheck(&checks, "primary_health", model.CheckPass, "current primary is healthy")
	} else {
		appendSwitchoverCheck(&checks, "primary_health", model.CheckFail, "current primary must be healthy and have the primary role")
	}
	primaryReadOnly, primaryReadOnlyKnown := boolMetadata(resolved.Primary.EngineMetadata, "read_only")
	primarySuperReadOnly, primarySuperReadOnlyKnown := boolMetadata(resolved.Primary.EngineMetadata, "super_read_only")
	if primaryReadOnlyKnown && primarySuperReadOnlyKnown && !primaryReadOnly && !primarySuperReadOnly {
		appendSwitchoverCheck(&checks, "primary_writable", model.CheckPass, "current primary is writable")
	} else {
		appendSwitchoverCheck(&checks, "primary_writable", model.CheckFail, "current primary must have current writable-state evidence")
	}

	if resolved.Target.Role == model.RoleReplica {
		appendSwitchoverCheck(&checks, "target_role", model.CheckPass, "selected target is a replica")
	} else {
		appendSwitchoverCheck(&checks, "target_role", model.CheckFail, "selected target must be a replica")
	}
	if resolved.Target.Health.State == model.HealthHealthy {
		appendSwitchoverCheck(&checks, "target_health", model.CheckPass, "selected target is healthy")
	} else {
		appendSwitchoverCheck(&checks, "target_health", model.CheckFail, "selected target must be healthy")
	}
	if resolved.Target.PromotionEligible {
		appendSwitchoverCheck(&checks, "target_promotion", model.CheckPass, "selected target is promotion eligible")
	} else {
		appendSwitchoverCheck(&checks, "target_promotion", model.CheckFail, "selected target is not promotion eligible")
	}
	targetReadOnly, targetReadOnlyKnown := candidateReadOnly(resolved.Target.EngineMetadata)
	if targetReadOnlyKnown && targetReadOnly {
		appendSwitchoverCheck(&checks, "target_read_only", model.CheckPass, "selected target is read-only")
	} else {
		appendSwitchoverCheck(&checks, "target_read_only", model.CheckFail, "selected target must have current read-only evidence")
	}
	if resolved.Target.Maintenance {
		appendSwitchoverCheck(&checks, "target_maintenance", model.CheckFail, "selected target is in maintenance")
	} else {
		appendSwitchoverCheck(&checks, "target_maintenance", model.CheckPass, "selected target is not in maintenance")
	}

	if resolved.Target.Replication.IOThread == model.ThreadRunning && resolved.Target.Replication.SQLThread == model.ThreadRunning {
		appendSwitchoverCheck(&checks, "replication_threads", model.CheckPass, "replication IO and SQL threads are running")
	} else {
		appendSwitchoverCheck(&checks, "replication_threads", model.CheckFail, "replication IO and SQL threads must both be running")
	}
	primaryUUID := strings.ToLower(strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"]))
	sourceUUID := strings.ToLower(strings.TrimSpace(resolved.Target.Replication.SourceIdentity["server_uuid"]))
	if primaryUUID != "" && sourceUUID == primaryUUID {
		appendSwitchoverCheck(&checks, "replication_source", model.CheckPass, "selected target directly follows the current primary")
	} else {
		appendSwitchoverCheck(&checks, "replication_source", model.CheckFail, "selected target must directly follow the current primary")
	}
	if resolved.Target.Replication.LagSeconds != nil && *resolved.Target.Replication.LagSeconds == 0 {
		appendSwitchoverCheck(&checks, "replication_lag", model.CheckPass, "selected target has zero observed replication lag")
	} else {
		appendSwitchoverCheck(&checks, "replication_lag", model.CheckFail, "selected target must have known zero replication lag")
	}

	primaryGTID := strings.EqualFold(strings.TrimSpace(resolved.Primary.EngineMetadata["gtid_mode"]), "ON")
	targetGTID := strings.EqualFold(strings.TrimSpace(resolved.Target.EngineMetadata["gtid_mode"]), "ON")
	if primaryGTID && targetGTID {
		appendSwitchoverCheck(&checks, "gtid_mode", model.CheckPass, "GTID mode is ON for source and target")
	} else {
		appendSwitchoverCheck(&checks, "gtid_mode", model.CheckFail, "GTID mode must be ON for source and target")
	}
	primaryLogBin, primaryLogBinKnown := boolMetadata(resolved.Primary.EngineMetadata, "log_bin")
	targetLogBin, targetLogBinKnown := boolMetadata(resolved.Target.EngineMetadata, "log_bin")
	if primaryLogBinKnown && targetLogBinKnown && primaryLogBin && targetLogBin {
		appendSwitchoverCheck(&checks, "binary_logging", model.CheckPass, "binary logging is enabled for source and target")
	} else {
		appendSwitchoverCheck(&checks, "binary_logging", model.CheckFail, "binary logging must be enabled for source and target")
	}
	primarySet, primarySetError := ParseGTIDSet(resolved.Primary.EngineMetadata["gtid_executed"])
	targetSet, targetSetError := ParseGTIDSet(resolved.Target.Replication.ExecutedPosition)
	if primarySetError != nil || targetSetError != nil {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, "source or target GTID position is invalid")
	} else if comparison, err := CompareGTIDSets(primarySet, targetSet); err != nil {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, "source or target GTID position exceeds supported limits")
	} else if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, fmt.Sprintf("target has %d missing and %d errant transactions", comparison.MissingTransactions, comparison.ErrantTransactions))
	} else {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckPass, "source and target GTID histories are identical")
	}
	primaryFamily, primaryVersionError := mysqlReleaseFamily(resolved.Primary.EngineMetadata["version"])
	targetFamily, targetVersionError := mysqlReleaseFamily(resolved.Target.EngineMetadata["version"])
	if primaryVersionError == nil && targetVersionError == nil && primaryFamily == targetFamily {
		appendSwitchoverCheck(&checks, "version_compatibility", model.CheckPass, "source and target use the same MySQL release family")
	} else {
		appendSwitchoverCheck(&checks, "version_compatibility", model.CheckFail, "source and target MySQL release families are incompatible")
	}

	coverageComplete := !resolved.Snapshot.ObservedAt.IsZero() && len(resolved.Snapshot.Probes) >= len(resolved.Snapshot.Instances)
	for _, instance := range resolved.Snapshot.Instances {
		if !hasHealthyBoundProbe(resolved.Snapshot.Probes, instance.ResourceID, resolved.Snapshot.ObservedAt) {
			coverageComplete = false
			break
		}
	}
	if coverageComplete {
		appendSwitchoverCheck(&checks, "probe_coverage", model.CheckPass, "all explicit inventory probes are current and healthy")
	} else {
		appendSwitchoverCheck(&checks, "probe_coverage", model.CheckFail, "all explicit inventory probes must be current and healthy")
	}
	if len(resolved.Snapshot.Instances) == 2 {
		appendSwitchoverCheck(&checks, "topology_scope", model.CheckPass, "two-node topology requires no replica reparenting")
	} else {
		appendSwitchoverCheck(&checks, "topology_scope", model.CheckFail, "this increment supports exactly two inventory members; replica reparenting is not implemented")
	}

	provider := adapterInstance.endpointProvider
	if provider == nil {
		provider = UnsupportedHAEndpointProvider{}
	}
	checks = append(checks, provider.Precheck(ctx, resolved)...)
	if !provider.Executable(ctx) && !failedCheckNamed(checks, "writer_endpoint_provider") {
		appendSwitchoverCheck(&checks, "writer_endpoint_provider", model.CheckFail, "writer endpoint provider does not support execution")
	}
	return checks, nil
}

func failedCheckNamed(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func operationPlanDigest(plan model.OperationPlan) (string, error) {
	canonical := plan
	canonical.ResourceMeta = model.ResourceMeta{}
	canonical.Digest = ""
	contents, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical operation plan: %w", err)
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (adapterInstance *Adapter) switchoverPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	if !model.ValidResourceID(request.Operation.ResourceID) {
		return model.OperationPlan{}, fmt.Errorf("operation ID is required before planning")
	}
	checks, err := adapterInstance.switchoverPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := *request.Resolved
	steps := []model.PlanStep{
		{Index: 1, Name: "revalidate_topology", Owner: "platform", TargetID: resolved.Cluster.ResourceID, Postcondition: "observation and resource revisions are unchanged"},
		{Index: 2, Name: "validate_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Postcondition: "endpoint provider is executable"},
		{Index: 3, Name: "fence_source", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "source is read-only"},
		{Index: 4, Name: "capture_source_gtid", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Postcondition: "source GTID position is captured after fencing"},
		{Index: 5, Name: "wait_target_gtid", Owner: "mysql", TargetID: resolved.Target.ResourceID, Postcondition: "target executed the fenced source GTID position"},
		{Index: 6, Name: "stop_target_replication", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target replication is stopped"},
		{Index: 7, Name: "promote_target", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target is writable"},
		{Index: 8, Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "writer endpoint has one target owner"},
		{Index: 9, Name: "retain_source_read_only", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "former primary remains read-only"},
		{Index: 10, Name: "verify_roles_and_endpoint", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "one writable primary and one endpoint owner are proven"},
	}
	plan := model.OperationPlan{
		OperationID:      request.Operation.ResourceID,
		ClusterID:        resolved.Cluster.ResourceID,
		SourceID:         resolved.Primary.ResourceID,
		TargetID:         resolved.Target.ResourceID,
		Stage:            model.StagePlan,
		ObservationToken: string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		ResourceRevisions: map[model.ResourceID]uint64{
			resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision,
			resolved.Primary.ResourceID: resolved.Primary.MetadataRevision,
			resolved.Target.ResourceID:  resolved.Target.MetadataRevision,
		},
		Checks:   checks,
		Steps:    steps,
		Summary:  "guarded MySQL planned switchover is ready",
		Mutating: true,
	}
	if planHasBlockingChecks(checks) {
		plan.Summary = "guarded MySQL planned switchover is blocked"
	}
	plan.Digest, err = operationPlanDigest(plan)
	if err != nil {
		return model.OperationPlan{}, err
	}
	return plan, nil
}
