package coordination

import (
	"strconv"
	"strings"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

// ackDegradedPrimaryForRenewal preserves only the existing writer endpoint.
// It never supplies a candidate for promotion, ownership transfer or bootstrap.
// The caller must request RenewOnly so the lease store also proves a live,
// stable lease for this exact owner and endpoint at the mutation boundary.
func ackDegradedPrimaryForRenewal(snapshot model.TopologySnapshot, observation endpoint.OwnershipObservation) (model.DatabaseInstance, bool) {
	reject := func() (model.DatabaseInstance, bool) { return model.DatabaseInstance{}, false }
	if !model.ValidResourceID(snapshot.ClusterID) {
		return reject()
	}
	instances := make(map[model.ResourceID]bool, len(snapshot.Instances))
	var primary model.DatabaseInstance
	for _, instance := range snapshot.Instances {
		if !model.ValidResourceID(instance.ResourceID) || instances[instance.ResourceID] ||
			instance.ClusterID != snapshot.ClusterID || instance.Engine != model.EngineMySQL {
			return reject()
		}
		instances[instance.ResourceID] = true
		if instance.Role == model.RolePrimary {
			if primary.ResourceID != "" {
				return reject()
			}
			primary = instance
		}
	}
	if !model.ValidResourceID(primary.ResourceID) || primary.Maintenance ||
		primary.Health.State != model.HealthDegraded || !primaryProvenWritable(primary) ||
		len(primary.Replication.SourceIdentity) != 0 || !mysqlPrimaryACKOnlyDegraded(primary.EngineMetadata) ||
		!currentACKDegradedProbe(snapshot, primary.ResourceID) {
		return reject()
	}
	if observation.CanonicalOwnerID != primary.ResourceID || observation.EndpointOwnerID != primary.ResourceID ||
		len(observation.OwnerIDs) != 1 || observation.OwnerIDs[0] != primary.ResourceID {
		return reject()
	}
	observed := make(map[model.ResourceID]bool, len(observation.ObservedInstanceIDs))
	for _, id := range observation.ObservedInstanceIDs {
		if !instances[id] || observed[id] {
			return reject()
		}
		observed[id] = true
	}
	if !observed[primary.ResourceID] || (observation.Complete && len(observed) != len(instances)) {
		return reject()
	}
	return primary, true
}

func mysqlPrimaryACKOnlyDegraded(metadata map[string]string) bool {
	for _, key := range []string{"semi_sync_ack_missing", "semi_sync_required", "semi_sync_available", "semi_sync_source_enabled"} {
		if !mysqlBooleanIs(metadata, key, true) {
			return false
		}
	}
	if _, present := metadata["semi_sync_probe_error"]; present && !mysqlBooleanIs(metadata, "semi_sync_probe_error", false) {
		return false
	}
	if strings.TrimSpace(metadata["semi_sync_wait_point"]) != "AFTER_SYNC" {
		return false
	}
	required, requiredErr := strconv.Atoi(strings.TrimSpace(metadata["semi_sync_wait_for_replica_count"]))
	clients, clientsErr := strconv.Atoi(strings.TrimSpace(metadata["semi_sync_source_clients"]))
	status := strings.TrimSpace(metadata["semi_sync_source_status"])
	if requiredErr != nil || clientsErr != nil || (status != "true" && status != "false") || required < 1 || clients < 0 {
		return false
	}
	return status == "false" || clients < required
}

func currentACKDegradedProbe(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	found := false
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID {
			continue
		}
		found = true
		if !probe.DiscoveryObservedAt.Equal(snapshot.ObservedAt) || probe.Health.State != model.HealthDegraded ||
			(probe.Outcome != model.ProbeOutcomeReachable && probe.Outcome != model.ProbeOutcomeMetricsUnavailable) {
			return false
		}
	}
	return found
}
