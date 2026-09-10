package postgresql

import (
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestResolveTopologyObservationsRejectsDuplicateNativeIdentities(t *testing.T) {
	for _, duplicateIndex := range []int{0, 1} {
		observations := resolverTopologyObservations()
		duplicate := observations[duplicateIndex]
		duplicate.Endpoint.IPAddress = "192.0.2.240"
		duplicate.Endpoint.Hostname = "cloned-node"
		observations = append(observations, duplicate)
		New().ResolveTopologyObservations(observations)
		for _, index := range []int{duplicateIndex, len(observations) - 1} {
			instance := observations[index].Discovery.Instance
			if instance.Health.State != model.HealthDegraded || instance.PromotionEligible || instance.EngineMetadata["topology_identity_conflict"] != "duplicate_native_identity" {
				t.Fatalf("duplicate identity at index %d remained healthy: %+v", index, instance)
			}
			if len(observations[index].Topology.Links) != 0 {
				t.Fatalf("duplicate identity produced links at index %d", index)
			}
		}
	}
}

func TestResolveTopologyObservationsDeduplicatesSameNetworkEndpoint(t *testing.T) {
	observations := resolverTopologyObservations()
	alias := observations[0]
	alias.Endpoint.Hostname = "same-address-alias"
	observations = append(observations, alias)
	New().ResolveTopologyObservations(observations)
	for _, index := range []int{1, 2} {
		if observations[index].Discovery.Instance.Health.State != model.HealthHealthy || len(observations[index].Topology.Links) != 1 {
			t.Fatalf("same IP and port were treated as different instances: %+v", observations[index])
		}
	}
}
