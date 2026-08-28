package coordination

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func strictMySQLBoolean(metadata map[string]string, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "true", "1", "on":
		return true, nil
	case "false", "0", "off":
		return false, nil
	default:
		return false, fmt.Errorf("MySQL %s evidence is missing or invalid", key)
	}
}

func zeroLag(lag *int64) bool {
	return lag != nil && *lag == 0
}

func normalizedGTIDPosition(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func mysqlBooleanIs(metadata map[string]string, key string, expected bool) bool {
	value, err := strictMySQLBoolean(metadata, key)
	return err == nil && value == expected
}

// MySQLSemisyncIdleReplicaSafe reports whether a degraded replica is fully
// caught up but is not the replica currently selected for semi-sync ACKs.
// Every piece of safety evidence is required so callers remain fail-closed.
func MySQLSemisyncIdleReplicaSafe(canonical, replica model.DatabaseInstance) bool {
	if replica.Health.State != model.HealthDegraded ||
		replica.Health.Summary != "MySQL replica is not actively acknowledging semi-sync transactions" {
		return false
	}
	for _, key := range []string{"semi_sync_required", "semi_sync_available", "semi_sync_source_enabled"} {
		if !mysqlBooleanIs(canonical.EngineMetadata, key, true) {
			return false
		}
	}
	if !mysqlBooleanIs(canonical.EngineMetadata, "semi_sync_source_status", true) {
		return false
	}
	clients, clientsErr := strconv.Atoi(strings.TrimSpace(canonical.EngineMetadata["semi_sync_source_clients"]))
	required, requiredErr := strconv.Atoi(strings.TrimSpace(canonical.EngineMetadata["semi_sync_wait_for_replica_count"]))
	if clientsErr != nil || requiredErr != nil || required < 1 || clients < required {
		return false
	}
	for _, key := range []string{"semi_sync_required", "semi_sync_available", "semi_sync_source_enabled", "semi_sync_replica_enabled"} {
		if !mysqlBooleanIs(replica.EngineMetadata, key, true) {
			return false
		}
	}
	if !mysqlBooleanIs(replica.EngineMetadata, "semi_sync_replica_status", false) ||
		strings.EqualFold(strings.TrimSpace(replica.EngineMetadata["semi_sync_probe_error"]), "true") {
		return false
	}
	canonicalGTID := normalizedGTIDPosition(canonical.EngineMetadata["gtid_executed"])
	replicaGTID := normalizedGTIDPosition(replica.Replication.ExecutedPosition)
	return canonicalGTID != "" && replicaGTID == canonicalGTID
}

// RebootBootstrapCandidate proves that a rebooted canonical primary is the only
// instance that can be reactivated. It intentionally accepts no partial or
// stale evidence because the caller uses the result to grant a writer lease.
func RebootBootstrapCandidate(snapshot model.TopologySnapshot, canonicalOwnerID model.ResourceID, now time.Time, maxAge time.Duration) (model.DatabaseInstance, error) {
	if !model.ValidResourceID(snapshot.ClusterID) || !model.ValidResourceID(canonicalOwnerID) {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap scope is invalid")
	}
	if maxAge <= 0 {
		maxAge = 15 * time.Second
	}
	now = now.UTC()
	observedAt := snapshot.ObservedAt.UTC()
	if observedAt.IsZero() || now.Sub(observedAt) > maxAge || observedAt.Sub(now) > maxAge {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap topology is stale")
	}
	if len(snapshot.Instances) < 2 {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap requires at least one verified replica")
	}
	if len(snapshot.Anomalies) != 0 {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap topology has unresolved anomalies")
	}

	instances := make(map[model.ResourceID]model.DatabaseInstance, len(snapshot.Instances))
	var canonical model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if !model.ValidResourceID(instance.ResourceID) || instance.ClusterID != snapshot.ClusterID || instance.Engine != model.EngineMySQL {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap instance scope is invalid")
		}
		if _, duplicate := instances[instance.ResourceID]; duplicate {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap topology contains duplicate instances")
		}
		readOnly, readOnlyErr := strictMySQLBoolean(instance.EngineMetadata, "read_only")
		superReadOnly, superReadOnlyErr := strictMySQLBoolean(instance.EngineMetadata, "super_read_only")
		if readOnlyErr != nil || superReadOnlyErr != nil || !readOnly || !superReadOnly {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap requires every instance to be fully read-only")
		}
		if instance.Maintenance {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap is blocked by maintenance state")
		}
		instances[instance.ResourceID] = instance
		if instance.ResourceID == canonicalOwnerID {
			canonical = instance
		}
	}
	if canonical.ResourceID == "" {
		return model.DatabaseInstance{}, fmt.Errorf("canonical primary is absent from the current topology")
	}
	canonicalUUID := strings.ToLower(strings.TrimSpace(canonical.EngineIdentity["server_uuid"]))
	if canonicalUUID == "" || canonical.Role != model.RoleUnknown || canonical.Health.State != model.HealthDegraded || len(canonical.Replication.SourceIdentity) != 0 {
		return model.DatabaseInstance{}, fmt.Errorf("canonical instance is not a fenced rebooted primary")
	}

	probeCount := make(map[model.ResourceID]int, len(snapshot.Probes))
	unavailableReplicas := make(map[model.ResourceID]struct{})
	if len(snapshot.Probes) != len(snapshot.Instances) {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe coverage is incomplete")
	}
	for _, probe := range snapshot.Probes {
		instance, found := instances[probe.InstanceID]
		if !found {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe evidence is incomplete or stale")
		}
		probeCount[probe.InstanceID]++
		if probeCount[probe.InstanceID] != 1 || probe.Health.State != instance.Health.State {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe evidence is inconsistent")
		}
		if probe.DiscoveryObservedAt.UTC().Equal(observedAt) {
			continue
		}
		failedCurrentProbe := probe.DiscoveryObservedAt.IsZero() &&
			probe.Health.ObservedAt.UTC().Equal(observedAt) &&
			(probe.Health.State == model.HealthUnknown || probe.Health.State == model.HealthUnhealthy)
		if !failedCurrentProbe {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe evidence is incomplete or stale")
		}
		// A rebuilt or offline replica may be absent from database discovery.
		// The ownership keeper separately requires complete host-agent VIP
		// coverage before issuing the bootstrap lease, while the last durable
		// database evidence must still prove that this node was a fully
		// read-only replica. An unavailable primary or writable node remains a
		// hard block.
		if instance.ResourceID == canonicalOwnerID || instance.Role != model.RoleReplica ||
			!mysqlBooleanIs(instance.EngineMetadata, "read_only", true) ||
			!mysqlBooleanIs(instance.EngineMetadata, "super_read_only", true) {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap unavailable instance is not a proven read-only replica")
		}
		unavailableReplicas[instance.ResourceID] = struct{}{}
	}

	followers := make(map[model.ResourceID]struct{}, len(snapshot.Instances)-1)
	for _, instance := range snapshot.Instances {
		if instance.ResourceID == canonicalOwnerID {
			continue
		}
		if _, unavailable := unavailableReplicas[instance.ResourceID]; unavailable {
			continue
		}
		healthSafe := instance.Health.State == model.HealthHealthy || MySQLSemisyncIdleReplicaSafe(canonical, instance)
		if instance.Role != model.RoleReplica || !healthSafe ||
			instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning ||
			!zeroLag(instance.Replication.LagSeconds) ||
			!strings.EqualFold(strings.TrimSpace(instance.Replication.SourceIdentity["server_uuid"]), canonicalUUID) {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replica evidence is unsafe")
		}
		followers[instance.ResourceID] = struct{}{}
	}
	if len(followers) == 0 {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap requires at least one verified replica")
	}

	linked := make(map[model.ResourceID]struct{}, len(snapshot.Links))
	for _, link := range snapshot.Links {
		if link.ClusterID != snapshot.ClusterID || link.SourceInstanceID != canonicalOwnerID {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication edge is unsafe")
		}
		if _, unavailable := unavailableReplicas[link.TargetInstanceID]; unavailable {
			if link.Healthy {
				return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap unavailable replica edge is inconsistent")
			}
			if _, duplicate := linked[link.TargetInstanceID]; duplicate {
				return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication target is duplicated")
			}
			linked[link.TargetInstanceID] = struct{}{}
			continue
		}
		if !zeroLag(link.LagSeconds) {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication edge is unsafe")
		}
		if _, follower := followers[link.TargetInstanceID]; !follower {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication target is invalid")
		}
		if _, duplicate := linked[link.TargetInstanceID]; duplicate {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication target is duplicated")
		}
		linked[link.TargetInstanceID] = struct{}{}
	}
	for followerID := range followers {
		if _, found := linked[followerID]; !found {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication links are incomplete")
		}
	}
	return canonical, nil
}
