package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	metricsservice "clusterguard.io/ha/internal/metrics"
	"clusterguard.io/ha/pkg/model"
)

type clusterMonitoring struct {
	ClusterID     model.ResourceID       `json:"cluster_id"`
	Engine        model.Engine           `json:"engine"`
	ObservedAt    time.Time              `json:"observed_at,omitempty"`
	Health        model.HealthState      `json:"health"`
	PrimaryID     model.ResourceID       `json:"primary_id,omitempty"`
	ReplicaCount  int                    `json:"replica_count"`
	MaximumLag    *int64                 `json:"maximum_lag_seconds,omitempty"`
	Alerts        []metricsservice.Alert `json:"alerts"`
	CriticalCount int                    `json:"critical_count"`
	WarningCount  int                    `json:"warning_count"`
}

type zabbixMetric struct {
	Key        string           `json:"key"`
	ClusterID  model.ResourceID `json:"cluster_id"`
	InstanceID model.ResourceID `json:"instance_id,omitempty"`
	Value      float64          `json:"value"`
	ObservedAt time.Time        `json:"observed_at,omitempty"`
}

func (server *Server) activeHAEndpointStates(clusterID model.ResourceID) []metricsservice.HAEndpointState {
	states := make([]metricsservice.HAEndpointState, 0, 1)
	for _, resource := range server.store.HAEndpoints(clusterID) {
		endpoint, found := server.store.Endpoint(resource.EndpointID)
		if !found || resource.Kind != model.EndpointVIP {
			continue
		}
		states = append(states, metricsservice.HAEndpointState{
			ResourceID: resource.ResourceID, OwnerID: resource.OwnerID, Active: endpoint.Active, Healthy: resource.Healthy,
		})
	}
	return states
}

func (server *Server) fleetMonitoring() ([]clusterMonitoring, string) {
	clusters := server.store.Clusters()
	result := make([]clusterMonitoring, 0, len(clusters))
	overall := "healthy"
	for _, cluster := range clusters {
		snapshot, found := server.store.TopologySnapshot(cluster.ResourceID)
		if !found {
			snapshot = model.TopologySnapshot{ClusterID: cluster.ResourceID}
		}
		item := clusterMonitoring{ClusterID: cluster.ResourceID, Engine: cluster.Engine, ObservedAt: snapshot.ObservedAt, Health: snapshot.Health.State}
		for _, instance := range snapshot.Instances {
			if instance.Role == model.RolePrimary {
				item.PrimaryID = instance.ResourceID
			}
			if instance.Role == model.RoleReplica {
				item.ReplicaCount++
			}
			if lag := instance.Replication.LagSeconds; lag != nil && (item.MaximumLag == nil || *lag > *item.MaximumLag) {
				value := *lag
				item.MaximumLag = &value
			}
		}
		item.Alerts = metricsservice.EvaluateAlerts(snapshot, server.activeHAEndpointStates(cluster.ResourceID), metricsservice.AlertPolicy{})
		for _, alert := range item.Alerts {
			switch alert.Severity {
			case metricsservice.SeverityCritical:
				item.CriticalCount++
				overall = "critical"
			case metricsservice.SeverityWarning:
				item.WarningCount++
				if overall == "healthy" {
					overall = "warning"
				}
			}
		}
		result = append(result, item)
	}
	return result, overall
}

func (server *Server) monitoringRoute(writer http.ResponseWriter, action string) {
	switch action {
	case "health":
		clusters, overall := server.fleetMonitoring()
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"overall_status": overall, "clusters": clusters}})
	case "metrics":
		result := make([]map[string]interface{}, 0)
		for _, cluster := range server.store.Clusters() {
			instances, snapshot, found := server.persistedMetrics(cluster.ResourceID)
			if !found {
				continue
			}
			result = append(result, map[string]interface{}{"cluster_id": cluster.ResourceID, "observed_at": snapshot.ObservedAt, "instances": instances})
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": result})
	case "zabbix":
		server.zabbixMonitoring(writer)
	case "prometheus":
		server.fleetPrometheus(writer)
	default:
		writeError(writer, http.StatusNotFound, "monitoring route not found")
	}
}

func (server *Server) zabbixMonitoring(writer http.ResponseWriter) {
	clusters, _ := server.fleetMonitoring()
	items := make([]zabbixMetric, 0)
	for _, cluster := range clusters {
		healthValue := 1.0
		if cluster.CriticalCount > 0 {
			healthValue = 0
		} else if cluster.WarningCount > 0 {
			healthValue = 0.5
		}
		items = append(items,
			zabbixMetric{Key: "clusterguard.cluster.health", ClusterID: cluster.ClusterID, Value: healthValue, ObservedAt: cluster.ObservedAt},
			zabbixMetric{Key: "clusterguard.alerts.critical", ClusterID: cluster.ClusterID, Value: float64(cluster.CriticalCount), ObservedAt: cluster.ObservedAt},
			zabbixMetric{Key: "clusterguard.alerts.warning", ClusterID: cluster.ClusterID, Value: float64(cluster.WarningCount), ObservedAt: cluster.ObservedAt},
		)
		if cluster.MaximumLag != nil {
			items = append(items, zabbixMetric{Key: "clusterguard.mysql.replication_lag_seconds_max", ClusterID: cluster.ClusterID, Value: float64(*cluster.MaximumLag), ObservedAt: cluster.ObservedAt})
		}
		instances, _, found := server.persistedMetrics(cluster.ClusterID)
		if !found {
			continue
		}
		for _, instance := range instances {
			for name, value := range instance.Values {
				if prometheusMetricName(name) != "" && finiteMetric(value) {
					items = append(items, zabbixMetric{Key: "clusterguard.mysql." + name, ClusterID: cluster.ClusterID, InstanceID: instance.InstanceID, Value: value, ObservedAt: cluster.ObservedAt})
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ClusterID != items[j].ClusterID {
			return items[i].ClusterID < items[j].ClusterID
		}
		if items[i].Key != items[j].Key {
			return items[i].Key < items[j].Key
		}
		return items[i].InstanceID < items[j].InstanceID
	})
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": items})
}

func (server *Server) fleetPrometheus(writer http.ResponseWriter) {
	clusters, _ := server.fleetMonitoring()
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	for _, cluster := range clusters {
		health := 1.0
		if cluster.CriticalCount > 0 {
			health = 0
		} else if cluster.WarningCount > 0 {
			health = 0.5
		}
		clusterLabel := escapePrometheusLabel(string(cluster.ClusterID))
		_, _ = fmt.Fprintf(writer, "clusterguard_cluster_health{cluster_id=\"%s\"} %s\n", clusterLabel, strconv.FormatFloat(health, 'g', -1, 64))
		_, _ = fmt.Fprintf(writer, "clusterguard_alerts_total{cluster_id=\"%s\",severity=\"critical\"} %d\n", clusterLabel, cluster.CriticalCount)
		_, _ = fmt.Fprintf(writer, "clusterguard_alerts_total{cluster_id=\"%s\",severity=\"warning\"} %d\n", clusterLabel, cluster.WarningCount)
		instances, _, found := server.persistedMetrics(cluster.ClusterID)
		if !found {
			continue
		}
		for _, instance := range instances {
			names := make([]string, 0, len(instance.Values))
			for name, value := range instance.Values {
				if prometheusMetricName(name) != "" && finiteMetric(value) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			for _, name := range names {
				_, _ = fmt.Fprintf(writer, "%s{cluster_id=\"%s\",instance_id=\"%s\"} %s\n", prometheusMetricName(name), clusterLabel, escapePrometheusLabel(string(instance.InstanceID)), strconv.FormatFloat(instance.Values[name], 'g', -1, 64))
			}
		}
	}
}
