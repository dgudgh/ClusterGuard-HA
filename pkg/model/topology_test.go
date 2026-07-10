package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTopologyContractsCarryPortableReplicationState(t *testing.T) {
	lag := int64(3)
	instance := DatabaseInstance{
		ResourceMeta: ResourceMeta{ResourceID: NewResourceID()},
		Engine:       EngineMySQL,
		Replication: ReplicationStatus{
			SourceIdentity: EngineIdentity{"server_uuid": "source-uuid"},
			IOThread:       ThreadRunning,
			SQLThread:      ThreadRunning,
			LagSeconds:     &lag,
		},
	}
	if instance.Replication.SourceIdentity["server_uuid"] != "source-uuid" || *instance.Replication.LagSeconds != 3 {
		t.Fatalf("portable replication state was lost: %+v", instance)
	}
}

func TestProbeStatusCarriesDurableCurrentCycleEvidence(t *testing.T) {
	discoveryObservedAt := time.Date(2026, time.July, 11, 14, 0, 0, 0, time.UTC)
	metricsObservedAt := discoveryObservedAt.Add(250 * time.Millisecond)
	probe := ProbeStatus{
		EndpointID:          NewResourceID(),
		InstanceID:          NewResourceID(),
		DiscoveryObservedAt: discoveryObservedAt,
		MetricsObservedAt:   metricsObservedAt,
		Health:              Health{State: HealthHealthy, ObservedAt: discoveryObservedAt},
	}
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("marshal probe evidence: %v", err)
	}
	for _, field := range []string{`"discovery_observed_at"`, `"metrics_observed_at"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("probe evidence JSON missing %s: %s", field, encoded)
		}
	}
	if probe.DiscoveryObservedAt != discoveryObservedAt || probe.MetricsObservedAt != metricsObservedAt {
		t.Fatalf("probe evidence timestamps were lost: %+v", probe)
	}
}

func TestTopologySnapshotCarriesEndpointProbeHealthAndClusterAnomalies(t *testing.T) {
	endpointID := NewResourceID()
	instanceID := NewResourceID()
	observedAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	snapshot := TopologySnapshot{
		ClusterID: NewResourceID(),
		Probes: []ProbeStatus{{
			EndpointID: endpointID,
			InstanceID: instanceID,
			Health:     Health{State: HealthUnknown, ObservedAt: observedAt},
		}},
		Health: Health{State: HealthDegraded, ObservedAt: observedAt},
		Anomalies: []MetadataAnomaly{{
			Kind:     "multiple_writable_primaries",
			Severity: "critical",
		}},
		ObservedAt: observedAt,
	}

	if len(snapshot.Probes) != 1 || snapshot.Probes[0].EndpointID != endpointID || snapshot.Probes[0].InstanceID != instanceID || snapshot.Probes[0].Health.State != HealthUnknown {
		t.Fatalf("probe status was not retained: %+v", snapshot.Probes)
	}
	if snapshot.Health.State != HealthDegraded || len(snapshot.Anomalies) != 1 || snapshot.Anomalies[0].Severity != "critical" {
		t.Fatalf("cluster discovery state was not retained: %+v", snapshot)
	}
}
