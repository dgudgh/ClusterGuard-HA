package coordination

import (
	"fmt"
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
	if len(snapshot.Probes) != len(snapshot.Instances) {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe coverage is incomplete")
	}
	for _, probe := range snapshot.Probes {
		instance, found := instances[probe.InstanceID]
		if !found || !probe.DiscoveryObservedAt.UTC().Equal(observedAt) {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe evidence is incomplete or stale")
		}
		probeCount[probe.InstanceID]++
		if probeCount[probe.InstanceID] != 1 || probe.Health.State != instance.Health.State {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap probe evidence is inconsistent")
		}
	}

	followers := make(map[model.ResourceID]struct{}, len(snapshot.Instances)-1)
	for _, instance := range snapshot.Instances {
		if instance.ResourceID == canonicalOwnerID {
			continue
		}
		if instance.Role != model.RoleReplica || instance.Health.State != model.HealthHealthy ||
			instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning ||
			!zeroLag(instance.Replication.LagSeconds) ||
			!strings.EqualFold(strings.TrimSpace(instance.Replication.SourceIdentity["server_uuid"]), canonicalUUID) {
			return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replica evidence is unsafe")
		}
		followers[instance.ResourceID] = struct{}{}
	}

	if len(snapshot.Links) != len(followers) {
		return model.DatabaseInstance{}, fmt.Errorf("reboot bootstrap replication links are incomplete")
	}
	linked := make(map[model.ResourceID]struct{}, len(snapshot.Links))
	for _, link := range snapshot.Links {
		if link.ClusterID != snapshot.ClusterID || link.SourceInstanceID != canonicalOwnerID || !zeroLag(link.LagSeconds) {
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
	return canonical, nil
}
