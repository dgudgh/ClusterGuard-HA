package metrics

import (
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func alertByCode(alerts []Alert, code string) (Alert, bool) {
	for _, alert := range alerts {
		if alert.Code == code {
			return alert, true
		}
	}
	return Alert{}, false
}

func TestAlertsClassifyStoppedThreadLagAndVIPMismatch(t *testing.T) {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	replicaID := model.NewResourceID()
	wrongOwnerID := model.NewResourceID()
	lag := int64(45)
	observedAt := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	snapshot := model.TopologySnapshot{ClusterID: clusterID, ObservedAt: observedAt, Instances: []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}},
		{ResourceMeta: model.ResourceMeta{ResourceID: replicaID}, ClusterID: clusterID, Role: model.RoleReplica, Health: model.Health{State: model.HealthDegraded}, Replication: model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadRunning, LagSeconds: &lag}},
	}}
	alerts := EvaluateAlerts(snapshot, []HAEndpointState{{ResourceID: model.NewResourceID(), OwnerID: wrongOwnerID, Active: true, Healthy: true}}, AlertPolicy{WarningLagSeconds: 10, CriticalLagSeconds: 30})
	for _, expected := range []struct {
		code     string
		severity Severity
		instance model.ResourceID
	}{
		{"replication_io_thread_stopped", SeverityCritical, replicaID},
		{"replication_lag_critical", SeverityCritical, replicaID},
		{"vip_owner_mismatch", SeverityCritical, wrongOwnerID},
	} {
		alert, found := alertByCode(alerts, expected.code)
		if !found || alert.Severity != expected.severity || alert.ClusterID != clusterID || alert.InstanceID != expected.instance || !alert.ObservedAt.Equal(observedAt) {
			t.Fatalf("alert %s=%+v found=%t", expected.code, alert, found)
		}
	}
}

func TestAlertsFailClosedForNoPrimaryMultiplePrimaryAndUnknownVIP(t *testing.T) {
	clusterID := model.NewResourceID()
	base := model.TopologySnapshot{ClusterID: clusterID, ObservedAt: time.Now().UTC()}
	alerts := EvaluateAlerts(base, nil, AlertPolicy{})
	if alert, found := alertByCode(alerts, "primary_missing"); !found || alert.Severity != SeverityCritical {
		t.Fatalf("missing-primary alert=%+v found=%t", alert, found)
	}
	if alert, found := alertByCode(alerts, "vip_state_unknown"); !found || alert.Severity != SeverityWarning {
		t.Fatalf("unknown-VIP alert=%+v found=%t", alert, found)
	}

	base.Instances = []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Role: model.RolePrimary},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Role: model.RolePrimary},
	}
	alerts = EvaluateAlerts(base, []HAEndpointState{{ResourceID: model.NewResourceID(), Active: true}}, AlertPolicy{})
	if alert, found := alertByCode(alerts, "multiple_primaries"); !found || alert.Severity != SeverityCritical {
		t.Fatalf("multiple-primary alert=%+v found=%t", alert, found)
	}
}

func TestHealthyClusterProducesNoActiveAlerts(t *testing.T) {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	zero := int64(0)
	snapshot := model.TopologySnapshot{ClusterID: clusterID, ObservedAt: time.Now().UTC(), Instances: []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy}, Replication: model.ReplicationStatus{IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &zero}},
	}}
	alerts := EvaluateAlerts(snapshot, []HAEndpointState{{ResourceID: model.NewResourceID(), OwnerID: primaryID, Active: true, Healthy: true}}, AlertPolicy{WarningLagSeconds: 10, CriticalLagSeconds: 30})
	if len(alerts) != 0 {
		t.Fatalf("healthy cluster alerts=%+v", alerts)
	}
}

func TestPostgreSQLStandbyProducesReplicationAlerts(t *testing.T) {
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	standbyID := model.NewResourceID()
	lag := int64(45)
	snapshot := model.TopologySnapshot{ClusterID: clusterID, ObservedAt: time.Now().UTC(), Instances: []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, Engine: model.EnginePostgreSQL, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}},
		{ResourceMeta: model.ResourceMeta{ResourceID: standbyID}, Engine: model.EnginePostgreSQL, Role: model.RoleStandby, Health: model.Health{State: model.HealthDegraded}, Replication: model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadRunning, LagSeconds: &lag}},
	}}

	alerts := EvaluateAlerts(snapshot, nil, AlertPolicy{WarningLagSeconds: 10, CriticalLagSeconds: 30})
	for _, code := range []string{"replication_io_thread_stopped", "replication_lag_critical"} {
		alert, found := alertByCode(alerts, code)
		if !found || alert.InstanceID != standbyID {
			t.Fatalf("PostgreSQL standby alert %s=%+v found=%t", code, alert, found)
		}
	}
}
