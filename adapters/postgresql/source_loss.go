package postgresql

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
	"encoding/hex"
)

// ResolveTopologyObservationsWithHistory preserves only previously double-ended
// native provenance. It never calls disconnected replication healthy, invents a
// live link, or uses the bootstrap primary_node_id setting as evidence.
func (a *Adapter) ResolveTopologyObservationsWithHistory(observations []adapter.TopologyObservation, previous model.TopologySnapshot) {
	a.ResolveTopologyObservations(observations)
	for i := range observations {
		o := &observations[i]
		v := &o.Discovery.Instance
		if v.Role != model.RoleStandby {
			continue
		}
		fingerprint := v.EngineMetadata["replication_config_fingerprint"]
		decoded, err := hex.DecodeString(fingerprint)
		if err != nil || len(decoded) != 32 {
			continue
		}
		if v.Health.State == model.HealthHealthy && len(v.Replication.SourceIdentity) > 0 {
			for _, source := range observations {
				sv := source.Discovery.Instance
				if postgresqlSourceIdentityMatches(v.Replication.SourceIdentity, sv, v.EngineIdentity["system_identifier"]) {
					v.EngineMetadata["verified_upstream_source_endpoint"] = observedEndpointKey(source.Endpoint)
				}
			}
			v.EngineMetadata["verified_upstream_endpoint"] = observedEndpointKey(o.Endpoint)
			v.EngineMetadata["verified_upstream_config"] = fingerprint
			v.EngineMetadata["verified_upstream_timeline"] = v.EngineMetadata["timeline_id"]
			continue
		}
		// Any reachable primary (including a read-only one) requires fresh double-
		// ended evidence. This path is solely for a missing primary SQL observation.
		unsafe := v.EngineMetadata["topology_identity_conflict"] != ""
		for _, peer := range observations {
			if peer.Discovery.Instance.Role == model.RolePrimary {
				unsafe = true
			}
		}
		if unsafe || v.EngineMetadata["in_recovery"] != "true" || v.EngineMetadata["transaction_read_only"] != "true" || v.EngineMetadata["replay_paused"] != "false" || v.Replication.SQLThread != model.ThreadRunning || (v.Replication.IOThread != model.ThreadStopped && v.Replication.IOThread != model.ThreadConnecting) || v.Replication.LastSQLError != "" || v.Replication.LagSeconds == nil || *v.Replication.LagSeconds != 0 {
			continue
		}
		received, e1 := parseLSN(v.Replication.RetrievedPosition)
		replayed, e2 := parseLSN(v.Replication.ExecutedPosition)
		if e1 != nil || e2 != nil || received != replayed {
			continue
		}
		key, err := identity.InstanceKey(v.Engine, v.EngineIdentity)
		if err != nil {
			continue
		}
		matches := []model.DatabaseInstance{}
		for _, old := range previous.Instances {
			if k, e := identity.InstanceKey(old.Engine, old.EngineIdentity); e == nil && k == key && old.ClusterID == v.ClusterID {
				matches = append(matches, old)
			}
		}
		if len(matches) != 1 {
			continue
		}
		old := matches[0]
		oldReplay, err := parseLSN(old.Replication.ExecutedPosition)
		if err != nil || replayed < oldReplay || old.EngineMetadata["verified_upstream_endpoint"] != observedEndpointKey(o.Endpoint) || old.EngineMetadata["verified_upstream_config"] != fingerprint || old.EngineMetadata["verified_upstream_timeline"] != v.EngineMetadata["timeline_id"] {
			continue
		}
		sourceMatches := 0
		for _, source := range previous.Instances {
			if source.ClusterID == v.ClusterID && source.ResourceID != old.ResourceID && postgresqlSourceIdentityMatches(old.Replication.SourceIdentity, source, v.EngineIdentity["system_identifier"]) && source.EngineMetadata["timeline_id"] == v.EngineMetadata["timeline_id"] && old.EngineMetadata["verified_upstream_source_endpoint"] == observedEndpointKey(adapter.Endpoint{Hostname: source.Hostname, IPAddress: source.IPAddress, Port: source.Port}) {
				sourceMatches++
			}
		}
		if sourceMatches != 1 {
			continue
		}
		v.Replication.SourceIdentity = old.Replication.SourceIdentity.Clone()
		for _, field := range []string{"verified_upstream_endpoint", "verified_upstream_config", "verified_upstream_timeline", "verified_upstream_source_endpoint"} {
			v.EngineMetadata[field] = old.EngineMetadata[field]
		}
		v.Health.Summary = "PostgreSQL source disconnected; verified upstream provenance retained, current recovery/WAL checks required"
	}
}
