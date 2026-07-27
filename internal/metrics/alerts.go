package metrics

import (
	"fmt"
	"sort"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Alert struct {
	ID         string           `json:"id"`
	ClusterID  model.ResourceID `json:"cluster_id"`
	InstanceID model.ResourceID `json:"instance_id,omitempty"`
	Code       string           `json:"code"`
	Severity   Severity         `json:"severity"`
	Message    string           `json:"message"`
	ObservedAt time.Time        `json:"observed_at"`
}

type HAEndpointState struct {
	ResourceID model.ResourceID `json:"resource_id"`
	OwnerID    model.ResourceID `json:"owner_id,omitempty"`
	Active     bool             `json:"active"`
	Healthy    bool             `json:"healthy"`
}

type AlertPolicy struct {
	WarningLagSeconds  int64
	CriticalLagSeconds int64
}

func (policy AlertPolicy) normalized() AlertPolicy {
	if policy.WarningLagSeconds <= 0 {
		policy.WarningLagSeconds = 10
	}
	if policy.CriticalLagSeconds <= policy.WarningLagSeconds {
		policy.CriticalLagSeconds = 30
	}
	return policy
}

func EvaluateAlerts(snapshot model.TopologySnapshot, endpoints []HAEndpointState, policy AlertPolicy) []Alert {
	policy = policy.normalized()
	alerts := make([]Alert, 0)
	add := func(code string, severity Severity, instanceID model.ResourceID, message string) {
		alerts = append(alerts, Alert{
			ID: fmt.Sprintf("%s:%s:%s", snapshot.ClusterID, code, instanceID), ClusterID: snapshot.ClusterID,
			InstanceID: instanceID, Code: code, Severity: severity, Message: message, ObservedAt: snapshot.ObservedAt,
		})
	}

	primaries := make([]model.DatabaseInstance, 0, 1)
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary {
			primaries = append(primaries, instance)
		}
		switch instance.Health.State {
		case model.HealthUnhealthy:
			add("instance_unhealthy", SeverityCritical, instance.ResourceID, "database instance is unhealthy")
		case model.HealthDegraded:
			add("instance_degraded", SeverityWarning, instance.ResourceID, "database instance is degraded")
		}
		if instance.Role != model.RoleReplica && instance.Role != model.RoleStandby {
			continue
		}
		if instance.Replication.IOThread == model.ThreadStopped {
			add("replication_io_thread_stopped", SeverityCritical, instance.ResourceID, "replication IO thread is stopped")
		} else if instance.Replication.IOThread == model.ThreadUnknown {
			add("replication_io_thread_unknown", SeverityWarning, instance.ResourceID, "replication IO thread state is unknown")
		}
		if instance.Replication.SQLThread == model.ThreadStopped {
			add("replication_sql_thread_stopped", SeverityCritical, instance.ResourceID, "replication SQL thread is stopped")
		} else if instance.Replication.SQLThread == model.ThreadUnknown {
			add("replication_sql_thread_unknown", SeverityWarning, instance.ResourceID, "replication SQL thread state is unknown")
		}
		if lag := instance.Replication.LagSeconds; lag != nil {
			switch {
			case *lag >= policy.CriticalLagSeconds:
				add("replication_lag_critical", SeverityCritical, instance.ResourceID, "replication lag exceeds the critical threshold")
			case *lag >= policy.WarningLagSeconds:
				add("replication_lag_warning", SeverityWarning, instance.ResourceID, "replication lag exceeds the warning threshold")
			}
		}
	}
	if len(primaries) == 0 {
		add("primary_missing", SeverityCritical, "", "cluster has no observed primary")
	} else if len(primaries) > 1 {
		add("multiple_primaries", SeverityCritical, "", "cluster has multiple observed primary instances")
	}

	activeEndpoints := make([]HAEndpointState, 0, 1)
	for _, endpoint := range endpoints {
		if endpoint.Active {
			activeEndpoints = append(activeEndpoints, endpoint)
		}
	}
	if len(activeEndpoints) > 1 {
		add("multiple_active_vips", SeverityCritical, "", "cluster has multiple active VIP resources")
	} else if len(activeEndpoints) == 0 || !activeEndpoints[0].Healthy || activeEndpoints[0].OwnerID == "" {
		add("vip_state_unknown", SeverityWarning, "", "VIP ownership is not currently verified")
	} else if len(primaries) == 1 && activeEndpoints[0].OwnerID != primaries[0].ResourceID {
		add("vip_owner_mismatch", SeverityCritical, activeEndpoints[0].OwnerID, "VIP owner does not match the current primary")
	}

	severityRank := map[Severity]int{SeverityCritical: 0, SeverityWarning: 1, SeverityInfo: 2}
	sort.Slice(alerts, func(i, j int) bool {
		if severityRank[alerts[i].Severity] != severityRank[alerts[j].Severity] {
			return severityRank[alerts[i].Severity] < severityRank[alerts[j].Severity]
		}
		if alerts[i].Code != alerts[j].Code {
			return alerts[i].Code < alerts[j].Code
		}
		return alerts[i].InstanceID < alerts[j].InstanceID
	})
	return alerts
}
