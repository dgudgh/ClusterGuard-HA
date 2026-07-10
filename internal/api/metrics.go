package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	metricsservice "clusterguard.io/ha/internal/metrics"
	"clusterguard.io/ha/pkg/model"
)

type instanceMetrics struct {
	InstanceID model.ResourceID   `json:"instance_id"`
	Values     map[string]float64 `json:"values"`
}

func (server *Server) persistedMetrics(clusterID model.ResourceID) ([]instanceMetrics, model.TopologySnapshot, bool) {
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return nil, model.TopologySnapshot{}, false
	}
	derived := metricsservice.NewService().Derive(server.store.MetricSamples(clusterID))
	result := make([]instanceMetrics, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		values := make(map[string]float64)
		for name, value := range derived[instance.ResourceID] {
			if finiteMetric(value) {
				values[name] = value
			}
		}
		if instance.Replication.LagSeconds != nil && *instance.Replication.LagSeconds >= 0 {
			values["replication_lag_seconds"] = float64(*instance.Replication.LagSeconds)
		}
		result = append(result, instanceMetrics{InstanceID: instance.ResourceID, Values: values})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstanceID < result[j].InstanceID })
	return result, snapshot, true
}

func (server *Server) clusterMetrics(writer http.ResponseWriter, clusterID model.ResourceID) {
	instances, snapshot, found := server.persistedMetrics(clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "cluster has no persisted topology observation")
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
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	for _, instance := range instances {
		names := make([]string, 0, len(instance.Values))
		for name, value := range instance.Values {
			if prometheusMetricName(name) != "" && finiteMetric(value) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintf(writer, "%s{cluster_id=\"%s\",instance_id=\"%s\"} %s\n",
				prometheusMetricName(name), escapePrometheusLabel(string(clusterID)), escapePrometheusLabel(string(instance.InstanceID)),
				strconv.FormatFloat(instance.Values[name], 'g', -1, 64))
		}
	}
}

func prometheusMetricName(name string) string {
	switch name {
	case "qps", "tps", "slow_queries_per_second", "connections", "running_threads", "buffer_pool_hit_ratio", "replication_lag_seconds":
		return "clusterguard_mysql_" + name
	default:
		return ""
	}
}

func finiteMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func escapePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
