package api

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func TestMetricsRoutesUsePersistedSamplesAndReplicationLag(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "metrics-api",
	}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create metrics inventory: %v", err)
	}
	start := time.Date(2026, time.July, 11, 13, 0, 0, 0, time.UTC)
	lag := int64(3)
	primary := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-a"},
		Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}
	replica := model.DatabaseInstance{
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "native-b"},
		Hostname: "mysql-b", Port: 3306, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"server_uuid": "native-a"}, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
		},
	}
	probes := []model.ProbeStatus{
		{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy, ObservedAt: start}},
		{EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthHealthy, ObservedAt: start}},
	}
	first, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica, Metrics: []model.MetricSample{{ObservedAt: start, Values: map[string]float64{
				"questions_total": 100, "transactions_total": 40, "slow_queries_total": 5, "connections": 8,
			}}}},
		},
		Probes: probes, Health: model.Health{State: model.HealthHealthy, ObservedAt: start}, ObservedAt: start,
	})
	if err != nil {
		t.Fatalf("first metrics observation: %v", err)
	}
	replicaID := model.ResourceID("")
	for _, instance := range first.Instances {
		if instance.Role == model.RoleReplica {
			replicaID = instance.ResourceID
		}
	}
	if replicaID == "" {
		t.Fatal("replica UUID was not persisted")
	}
	secondTime := start.Add(10 * time.Second)
	for index := range probes {
		probes[index].Health.ObservedAt = secondTime
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica, Metrics: []model.MetricSample{{ObservedAt: secondTime, Values: map[string]float64{
				"questions_total": 150, "transactions_total": 60, "slow_queries_total": 15,
				"connections": 12, "running_threads": 3, "buffer_pool_hit_ratio": math.NaN(),
			}}}},
		},
		Probes: probes, Health: model.Health{State: model.HealthHealthy, ObservedAt: secondTime}, ObservedAt: secondTime,
	}); err != nil {
		t.Fatalf("second metrics observation: %v", err)
	}

	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	jsonResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics", nil)
	if jsonResponse.Code != http.StatusOK {
		t.Fatalf("JSON metrics status: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	for _, expected := range []string{`"instance_id":"` + string(replicaID) + `"`, `"qps":5`, `"tps":2`, `"slow_queries_per_second":1`, `"replication_lag_seconds":3`, `"connections":12`} {
		if !strings.Contains(jsonResponse.Body.String(), expected) {
			t.Fatalf("JSON metrics missing %s: %s", expected, jsonResponse.Body.String())
		}
	}
	if strings.Contains(jsonResponse.Body.String(), "NaN") {
		t.Fatalf("JSON metrics emitted non-finite value: %s", jsonResponse.Body.String())
	}

	prometheus := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics/prometheus", nil)
	if prometheus.Code != http.StatusOK {
		t.Fatalf("Prometheus metrics status: %d %s", prometheus.Code, prometheus.Body.String())
	}
	if contentType := prometheus.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Prometheus content type = %q", contentType)
	}
	for _, metricName := range []string{"clusterguard_mysql_replication_lag_seconds", "clusterguard_mysql_connections", "clusterguard_mysql_qps", "clusterguard_mysql_tps"} {
		if !strings.Contains(prometheus.Body.String(), metricName+`{cluster_id="`+string(cluster.ResourceID)+`",instance_id="`+string(replicaID)+`"}`) {
			t.Fatalf("Prometheus metrics missing UUID labels for %s: %s", metricName, prometheus.Body.String())
		}
	}
	if strings.Contains(prometheus.Body.String(), "hostname=") || strings.Contains(prometheus.Body.String(), "mysql-a") || strings.Contains(prometheus.Body.String(), "mysql-b") || strings.Contains(prometheus.Body.String(), "NaN") || strings.Contains(prometheus.Body.String(), "+Inf") {
		t.Fatalf("Prometheus metrics exposed mutable identity or non-finite values: %s", prometheus.Body.String())
	}
	requests, databaseCalls := candidate.captured()
	if len(requests) != 0 || databaseCalls != 0 {
		t.Fatalf("metrics reads invoked adapter: requests=%d database_calls=%d", len(requests), databaseCalls)
	}
}
