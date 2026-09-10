package postgresql

import (
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	topologyClusterID          = "10000000-0000-4000-8000-000000000001"
	topologyPrimaryNativeID    = "20000000-0000-4000-8000-000000000002"
	topologyStandbyOneNative   = "30000000-0000-4000-8000-000000000003"
	topologyStandbyThreeNative = "40000000-0000-4000-8000-000000000004"
	topologyPrimaryPlatformID  = "50000000-0000-4000-8000-000000000005"
	topologyStandbyPlatformID  = "60000000-0000-4000-8000-000000000006"
	topologySystemID           = "7428625847249870011"
)

func TestResolveTopologyObservationsUsesBilateralNativeIdentityEvidence(t *testing.T) {
	observations := resolverTopologyObservations()
	standby := &observations[1].Discovery.Instance
	standby.Replication.SourceIdentity = model.EngineIdentity{
		"resource_id":       topologyPrimaryPlatformID,
		"system_identifier": topologySystemID,
	}

	New().ResolveTopologyObservations(observations)

	for _, index := range []int{1, 2} {
		observation := observations[index]
		instance := observation.Discovery.Instance
		if instance.Health.State != model.HealthHealthy || !instance.PromotionEligible {
			t.Fatalf("standby %d was not verified healthy: %+v", index, instance)
		}
		if got := instance.Replication.SourceIdentity["resource_id"]; got != topologyPrimaryNativeID {
			t.Fatalf("standby %d source resource_id=%q, want native ID %q", index, got, topologyPrimaryNativeID)
		}
		if got := instance.Replication.SourceIdentity["system_identifier"]; got != topologySystemID {
			t.Fatalf("standby %d source system_identifier=%q", index, got)
		}
		if len(observation.Topology.Links) != 1 {
			t.Fatalf("standby %d links=%+v", index, observation.Topology.Links)
		}
		link := observation.Topology.Links[0]
		if !link.Healthy || link.SourceIdentity["resource_id"] != topologyPrimaryNativeID ||
			link.TargetIdentity["resource_id"] != instance.EngineIdentity["resource_id"] {
			t.Fatalf("standby %d link=%+v", index, link)
		}
	}
	if len(observations[0].Topology.Links) != 0 {
		t.Fatalf("primary unexpectedly retained topology links: %+v", observations[0].Topology.Links)
	}
}

func TestResolveTopologyObservationsRejectsUnsafeOrAmbiguousEvidence(t *testing.T) {
	tests := map[string]func([]adapter.TopologyObservation){
		"duplicate native application name": func(observations []adapter.TopologyObservation) {
			evidence := observations[0].Discovery.TopologyEvidence.(*streamingEvidence)
			evidence.Senders = append(evidence.Senders, evidence.Senders[0])
		},
		"platform ID used as application name": func(observations []adapter.TopologyObservation) {
			observations[0].Discovery.TopologyEvidence.(*streamingEvidence).Senders[0].ApplicationName = topologyStandbyPlatformID
		},
		"unknown receiver address": func(observations []adapter.TopologyObservation) {
			observations[1].Discovery.TopologyEvidence.(*streamingEvidence).SenderHost = "192.0.2.99"
		},
		"wrong receiver port": func(observations []adapter.TopologyObservation) {
			observations[1].Discovery.TopologyEvidence.(*streamingEvidence).SenderPort = 6432
		},
		"different system identifier": func(observations []adapter.TopologyObservation) {
			observations[1].Discovery.Instance.EngineIdentity["system_identifier"] = "7428625847249870099"
		},
		"different timeline": func(observations []adapter.TopologyObservation) {
			observations[1].Discovery.Instance.EngineMetadata["timeline_id"] = "8"
		},
		"two writable primaries": func(observations []adapter.TopologyObservation) {
			observations[2].Discovery.Instance.Role = model.RolePrimary
		},
		"empty sender evidence": func(observations []adapter.TopologyObservation) {
			observations[0].Discovery.TopologyEvidence.(*streamingEvidence).Senders = nil
		},
		"missing receiver evidence": func(observations []adapter.TopologyObservation) {
			observations[1].Discovery.TopologyEvidence = nil
		},
		"receiver disconnected": func(observations []adapter.TopologyObservation) {
			instance := &observations[1].Discovery.Instance
			instance.EngineMetadata["wal_receiver_status"] = ""
			instance.Replication.IOThread = model.ThreadStopped
		},
		"replay paused": func(observations []adapter.TopologyObservation) {
			instance := &observations[1].Discovery.Instance
			instance.EngineMetadata["replay_paused"] = "true"
			instance.Replication.SQLThread = model.ThreadStopped
		},
		"sender not streaming": func(observations []adapter.TopologyObservation) {
			observations[0].Discovery.TopologyEvidence.(*streamingEvidence).Senders[0].State = "catchup"
		},
		"sender replay LSN invalid": func(observations []adapter.TopologyObservation) {
			observations[0].Discovery.TopologyEvidence.(*streamingEvidence).Senders[0].ReplayLSN = "not-an-lsn"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			observations := resolverTopologyObservations()
			mutate(observations)
			New().ResolveTopologyObservations(observations)

			standby := observations[1].Discovery.Instance
			if len(standby.Replication.SourceIdentity) != 0 || standby.PromotionEligible || standby.Health.State != model.HealthDegraded {
				t.Fatalf("unsafe standby evidence remained trusted: %+v", standby)
			}
			if len(observations[1].Topology.Links) != 0 {
				t.Fatalf("unsafe evidence produced links: %+v", observations[1].Topology.Links)
			}
		})
	}
}

func TestParseStreamingEvidenceRejectsMalformedBoundaries(t *testing.T) {
	for name, row := range map[string]Row{
		"non numeric sender port": {"receiver_sender_port": "postgres"},
		"zero sender port":        {"receiver_sender_port": "0"},
		"oversized sender port":   {"receiver_sender_port": "65536"},
		"malformed sender JSON":   {"replication_senders": "{"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStreamingEvidence(row); err == nil {
				t.Fatal("malformed streaming evidence was accepted")
			}
		})
	}
	if evidence, err := parseStreamingEvidence(Row{}); err != nil || evidence == nil || evidence.SenderHost != "" || evidence.SenderPort != 0 || len(evidence.Senders) != 0 {
		t.Fatalf("empty optional evidence was not parsed safely: evidence=%+v err=%v", evidence, err)
	}

	New().ResolveTopologyObservations(nil)
}

func resolverTopologyObservations() []adapter.TopologyObservation {
	lag := int64(0)
	primaryIdentity := model.EngineIdentity{"resource_id": topologyPrimaryNativeID, "system_identifier": topologySystemID}
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(topologyPrimaryPlatformID)},
		ClusterID:    topologyClusterID, Engine: model.EnginePostgreSQL, EngineIdentity: primaryIdentity,
		Hostname: "pg02", IPAddress: "10.20.0.2", Port: 5432, Role: model.RolePrimary,
		Health: model.Health{State: model.HealthHealthy},
		EngineMetadata: map[string]string{
			"in_recovery": "false", "transaction_read_only": "false", "timeline_id": "7",
		},
	}
	standby := func(platformID, nativeID, hostname, ip string) model.DatabaseInstance {
		return model.DatabaseInstance{
			ResourceMeta: model.ResourceMeta{ResourceID: model.ResourceID(platformID)},
			ClusterID:    topologyClusterID, Engine: model.EnginePostgreSQL,
			EngineIdentity: model.EngineIdentity{"resource_id": nativeID, "system_identifier": topologySystemID},
			Hostname:       hostname, IPAddress: ip, Port: 5432, Role: model.RoleStandby,
			Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
			Replication: model.ReplicationStatus{
				SourceIdentity: model.EngineIdentity{"resource_id": "70000000-0000-4000-8000-000000000007", "system_identifier": topologySystemID},
				IOThread:       model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
				ExecutedPosition: "0/5000040",
			},
			EngineMetadata: map[string]string{
				"in_recovery": "true", "transaction_read_only": "true", "replay_paused": "false",
				"wal_receiver_status": "streaming", "timeline_id": "7",
			},
		}
	}
	primaryEvidence := &streamingEvidence{Senders: []walSenderEvidence{
		{ApplicationName: topologyStandbyOneNative, State: "streaming", ClientAddress: "10.20.0.1", ReplayLSN: "0/5000040"},
		{ApplicationName: topologyStandbyThreeNative, State: "streaming", ClientAddress: "10.20.0.3", ReplayLSN: "0/5000030"},
	}}
	staleLink := adapter.TopologyLink{SourceIdentity: model.EngineIdentity{"resource_id": topologyPrimaryPlatformID}, Healthy: true}
	return []adapter.TopologyObservation{
		{
			Endpoint:  adapter.Endpoint{Hostname: "pg02", IPAddress: "10.20.0.2", Port: 5432},
			Discovery: adapter.DiscoveryResult{Instance: primary, TopologyEvidence: primaryEvidence},
			Topology:  adapter.TopologyResult{Links: []adapter.TopologyLink{staleLink}},
		},
		{
			Endpoint:  adapter.Endpoint{Hostname: "pg01", IPAddress: "10.20.0.1", Port: 5432},
			Discovery: adapter.DiscoveryResult{Instance: standby(topologyStandbyPlatformID, topologyStandbyOneNative, "pg01", "10.20.0.1"), TopologyEvidence: &streamingEvidence{SenderHost: "pg02", SenderPort: 5432}},
			Topology:  adapter.TopologyResult{Links: []adapter.TopologyLink{staleLink}},
		},
		{
			Endpoint:  adapter.Endpoint{Hostname: "pg03", IPAddress: "10.20.0.3", Port: 5432},
			Discovery: adapter.DiscoveryResult{Instance: standby("80000000-0000-4000-8000-000000000008", topologyStandbyThreeNative, "pg03", "10.20.0.3"), TopologyEvidence: &streamingEvidence{SenderHost: "10.20.0.2", SenderPort: 5432}},
			Topology:  adapter.TopologyResult{Links: []adapter.TopologyLink{staleLink}},
		},
	}
}
