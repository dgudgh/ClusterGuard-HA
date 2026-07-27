package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	metricsservice "clusterguard.io/ha/internal/metrics"
	"clusterguard.io/ha/pkg/model"
)

type instanceMetrics struct {
	InstanceID        model.ResourceID   `json:"instance_id"`
	MetricsObservedAt *time.Time         `json:"metrics_observed_at,omitempty"`
	Values            map[string]float64 `json:"values"`
}

func (server *Server) persistedMetrics(clusterID model.ResourceID) ([]instanceMetrics, model.TopologySnapshot, bool) {
	cluster, clusterFound := server.store.Cluster(clusterID)
	if !clusterFound {
		return nil, model.TopologySnapshot{}, false
	}
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return nil, model.TopologySnapshot{}, false
	}
	allSamples := server.store.MetricSamples(clusterID)
	result := make([]instanceMetrics, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		values := make(map[string]float64)
		var metricsObservedAt time.Time
		discoveryObserved := false
		for _, probe := range snapshot.Probes {
			if probe.InstanceID != instance.ResourceID {
				continue
			}
			if !probe.DiscoveryObservedAt.IsZero() {
				discoveryObserved = true
			}
			if probe.MetricsObservedAt.After(metricsObservedAt) {
				metricsObservedAt = probe.MetricsObservedAt
			}
		}
		if !metricsObservedAt.IsZero() {
			eligibleSamples := make([]model.MetricSample, 0)
			for _, sample := range allSamples {
				if sample.InstanceID == instance.ResourceID && !sample.ObservedAt.After(metricsObservedAt) {
					eligibleSamples = append(eligibleSamples, sample)
				}
			}
			derived, derivedFound := metricsservice.NewService().DeriveForEngine(cluster.Engine, eligibleSamples)[instance.ResourceID]
			if derivedFound && derived.ObservedAt.Equal(metricsObservedAt) {
				for name, value := range derived.Values {
					if finiteMetric(value) {
						values[name] = value
					}
				}
			} else {
				metricsObservedAt = time.Time{}
			}
		}
		if discoveryObserved && instance.Replication.LagSeconds != nil && *instance.Replication.LagSeconds >= 0 {
			values["replication_lag_seconds"] = float64(*instance.Replication.LagSeconds)
		}
		item := instanceMetrics{InstanceID: instance.ResourceID, Values: values}
		if !metricsObservedAt.IsZero() {
			item.MetricsObservedAt = &metricsObservedAt
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstanceID < result[j].InstanceID })
	return result, snapshot, true
}

func (server *Server) clusterMetrics(writer http.ResponseWriter, clusterID model.ResourceID, expectedObservation *time.Time) {
	instances, snapshot, found := server.persistedMetrics(clusterID)
	if !found {
		if expectedObservation != nil {
			writeError(writer, http.StatusConflict, "topology observation changed")
			return
		}
		writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
		return
	}
	if !matchesObservation(expectedObservation, snapshot.ObservedAt) {
		writeError(writer, http.StatusConflict, "topology observation changed")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"cluster_id": clusterID, "observed_at": snapshot.ObservedAt, "instances": instances,
	}})
}

func (server *Server) clusterPrometheusMetrics(writer http.ResponseWriter, clusterID model.ResourceID) {
	instances, _, found := server.persistedMetrics(clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
		return
	}
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	emittedMetadata := make(map[string]bool)
	for _, instance := range instances {
		names := make([]string, 0, len(instance.Values))
		for name, value := range instance.Values {
			if prometheusMetricName(cluster.Engine, name) != "" && finiteMetric(value) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			metricName := writePrometheusMetricMetadata(writer, emittedMetadata, cluster.Engine, name)
			_, _ = fmt.Fprintf(writer, "%s{cluster_id=\"%s\",instance_id=\"%s\"} %s\n",
				metricName, escapePrometheusLabel(string(clusterID)), escapePrometheusLabel(string(instance.InstanceID)),
				strconv.FormatFloat(instance.Values[name], 'g', -1, 64))
		}
	}
}

type prometheusMetricDescriptor struct {
	Name string
	Type string
	Help string
}

func prometheusMetricDescriptorFor(engine model.Engine, name string) (prometheusMetricDescriptor, bool) {
	descriptor := prometheusMetricDescriptor{Type: "gauge"}
	switch engine {
	case model.EngineMySQL:
		switch name {
		case "qps", "tps", "slow_queries_per_second", "connections", "running_threads", "buffer_pool_hit_ratio", "replication_lag_seconds":
			descriptor.Name = "clusterguard_mysql_" + name
			descriptor.Help = "Observed MySQL " + name + " for an immutable database instance."
		default:
			return prometheusMetricDescriptor{}, false
		}
	case model.EnginePostgreSQL:
		switch name {
		case "connections", "active_connections", "transactions_total", "deadlocks_total", "conflicts_total",
			"temp_bytes_total", "blocks_read_total", "blocks_hit_total", "database_size_bytes", "replication_clients",
			"buffer_cache_hit_ratio", "wal_bytes", "checkpoints_total", "max_transaction_age_seconds", "replication_lag_seconds":
			descriptor.Name = "clusterguard_postgresql_" + name
			descriptor.Help = "Observed PostgreSQL " + name + " for an immutable database instance."
			if strings.HasSuffix(name, "_total") {
				descriptor.Type = "counter"
			}
		default:
			return prometheusMetricDescriptor{}, false
		}
	case model.EngineOracle:
		switch name {
		case "broker_status_healthy", "transport_lag_seconds", "apply_lag_seconds", "role_primary", "replication_lag_seconds":
			descriptor.Name = "clusterguard_oracle_" + name
			descriptor.Help = "Observed Oracle " + name + " for an immutable database instance."
		default:
			return prometheusMetricDescriptor{}, false
		}
	case model.EngineSQLServer:
		switch name {
		case "always_on_healthy", "connected", "synchronized", "synchronous_commit",
			"log_send_queue_bytes", "redo_queue_bytes", "role_primary", "role_secondary", "replication_lag_seconds":
			descriptor.Name = "clusterguard_sqlserver_" + name
			descriptor.Help = "Observed SQL Server " + name + " for an immutable database instance."
		default:
			return prometheusMetricDescriptor{}, false
		}
	default:
		return prometheusMetricDescriptor{}, false
	}
	return descriptor, true
}

func writePrometheusMetricMetadata(writer http.ResponseWriter, emitted map[string]bool, engine model.Engine, name string) string {
	descriptor, found := prometheusMetricDescriptorFor(engine, name)
	if !found {
		return ""
	}
	if !emitted[descriptor.Name] {
		_, _ = fmt.Fprintf(writer, "# HELP %s %s\n", descriptor.Name, descriptor.Help)
		_, _ = fmt.Fprintf(writer, "# TYPE %s %s\n", descriptor.Name, descriptor.Type)
		emitted[descriptor.Name] = true
	}
	return descriptor.Name
}

func prometheusMetricName(engine model.Engine, name string) string {
	descriptor, found := prometheusMetricDescriptorFor(engine, name)
	if found {
		return descriptor.Name
	}
	return ""
}

func finiteMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func escapePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
