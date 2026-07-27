package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func TestPrometheusMetricNamesAreScopedByDatabaseEngine(t *testing.T) {
	tests := []struct {
		engine model.Engine
		name   string
		want   string
	}{
		{model.EngineMySQL, "qps", "clusterguard_mysql_qps"},
		{model.EngineMySQL, "replication_lag_seconds", "clusterguard_mysql_replication_lag_seconds"},
		{model.EnginePostgreSQL, "replication_lag_seconds", "clusterguard_postgresql_replication_lag_seconds"},
		{model.EnginePostgreSQL, "connections", "clusterguard_postgresql_connections"},
		{model.EnginePostgreSQL, "transactions_total", "clusterguard_postgresql_transactions_total"},
		{model.EnginePostgreSQL, "conflicts_total", "clusterguard_postgresql_conflicts_total"},
		{model.EnginePostgreSQL, "buffer_cache_hit_ratio", "clusterguard_postgresql_buffer_cache_hit_ratio"},
		{model.EnginePostgreSQL, "wal_bytes", "clusterguard_postgresql_wal_bytes"},
		{model.EnginePostgreSQL, "qps", ""},
		{model.EngineOracle, "replication_lag_seconds", "clusterguard_oracle_replication_lag_seconds"},
		{model.EngineOracle, "broker_status_healthy", "clusterguard_oracle_broker_status_healthy"},
		{model.EngineOracle, "apply_lag_seconds", "clusterguard_oracle_apply_lag_seconds"},
		{model.EngineOracle, "qps", ""},
		{model.EngineSQLServer, "replication_lag_seconds", "clusterguard_sqlserver_replication_lag_seconds"},
		{model.EngineSQLServer, "always_on_healthy", "clusterguard_sqlserver_always_on_healthy"},
		{model.EngineSQLServer, "redo_queue_bytes", "clusterguard_sqlserver_redo_queue_bytes"},
		{model.EngineSQLServer, "qps", ""},
	}
	for _, test := range tests {
		if got := prometheusMetricName(test.engine, test.name); got != test.want {
			t.Errorf("prometheusMetricName(%s, %s)=%q want %q", test.engine, test.name, got, test.want)
		}
	}
}

func TestMetricsRoutesUsePersistedSamplesAndReplicationLag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("open metrics repository: %v", err)
	}
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
		{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: start, Health: model.Health{State: model.HealthHealthy, ObservedAt: start}},
		{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: start, MetricsObservedAt: start, Health: model.Health{State: model.HealthHealthy, ObservedAt: start}},
	}
	first, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
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
		probes[index].DiscoveryObservedAt = secondTime
	}
	probes[1].MetricsObservedAt = secondTime
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica, Metrics: []model.MetricSample{{ObservedAt: secondTime, Values: map[string]float64{
				"questions_total": 150, "transactions_total": 60, "slow_queries_total": 15,
				"connections": 12, "running_threads": 3, "buffer_pool_hit_ratio": 0.98,
			}}}},
		},
		Probes: probes, Health: model.Health{State: model.HealthHealthy, ObservedAt: secondTime}, ObservedAt: secondTime,
	}); err != nil {
		t.Fatalf("second metrics observation: %v", err)
	}

	repository, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen good metrics repository: %v", err)
	}
	candidate := newCandidateAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	jsonResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics", nil)
	if jsonResponse.Code != http.StatusOK {
		t.Fatalf("JSON metrics status: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	for _, expected := range []string{`"instance_id":"` + string(replicaID) + `"`, `"metrics_observed_at":"` + secondTime.Format(time.RFC3339) + `"`, `"qps":5`, `"tps":2`, `"slow_queries_per_second":1`, `"replication_lag_seconds":3`, `"connections":12`} {
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

	metricsFailureTime := secondTime.Add(10 * time.Second)
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: metricsFailureTime, Health: model.Health{State: model.HealthHealthy, ObservedAt: metricsFailureTime}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: metricsFailureTime, Health: model.Health{State: model.HealthDegraded, ObservedAt: metricsFailureTime}},
		},
		ObservedAt: metricsFailureTime,
	}); err != nil {
		t.Fatalf("publish metrics failure: %v", err)
	}
	repository, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen metrics failure: %v", err)
	}
	server = newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	jsonResponse = callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics", nil)
	if jsonResponse.Code != http.StatusOK || !strings.Contains(jsonResponse.Body.String(), `"replication_lag_seconds":3`) {
		t.Fatalf("metrics failure lost current replication lag: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	for _, stale := range []string{`"qps"`, `"tps"`, `"connections"`, `"metrics_observed_at"`} {
		if strings.Contains(jsonResponse.Body.String(), stale) {
			t.Fatalf("metrics failure emitted stale %s: %s", stale, jsonResponse.Body.String())
		}
	}
	prometheus = callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics/prometheus", nil)
	if strings.Contains(prometheus.Body.String(), "clusterguard_mysql_qps") || !strings.Contains(prometheus.Body.String(), "clusterguard_mysql_replication_lag_seconds") {
		t.Fatalf("Prometheus metrics failure freshness is wrong: %s", prometheus.Body.String())
	}

	databaseFailureTime := metricsFailureTime.Add(10 * time.Second)
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations:        []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: primary}},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: databaseFailureTime, Health: model.Health{State: model.HealthHealthy, ObservedAt: databaseFailureTime}},
			{EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthUnknown, ObservedAt: databaseFailureTime}},
		},
		ObservedAt: databaseFailureTime,
	}); err != nil {
		t.Fatalf("publish database failure: %v", err)
	}
	repository, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen database failure: %v", err)
	}
	server = newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	jsonResponse = callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics", nil)
	for _, stale := range []string{`"qps"`, `"connections"`, `"replication_lag_seconds"`, `"metrics_observed_at"`} {
		if strings.Contains(jsonResponse.Body.String(), stale) {
			t.Fatalf("database failure emitted stale %s after reopen: %s", stale, jsonResponse.Body.String())
		}
	}
	prometheus = callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics/prometheus", nil)
	if strings.Contains(prometheus.Body.String(), "clusterguard_mysql_") {
		t.Fatalf("database failure emitted stale Prometheus metrics: %s", prometheus.Body.String())
	}
}

func TestMetricsRoutesExcludeRetainedFutureSamplesAfterClockRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "rollback"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	t1 := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(10 * time.Second)
	t3 := t1.Add(time.Hour)
	instance := model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "rollback-native"}, Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}
	metricValues := func(questions float64, connections float64) map[string]float64 {
		return map[string]float64{"questions_total": questions, "transactions_total": questions, "slow_queries_total": questions, "connections": connections, "running_threads": 2, "buffer_pool_hit_ratio": .99}
	}
	first, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: t1, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance, Metrics: []model.MetricSample{{ObservedAt: t1, Values: metricValues(100, 10)}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: t1, MetricsObservedAt: t1, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	instanceID := first.Instances[0].ResourceID
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{InstanceID: instanceID, ObservedAt: t3, Values: metricValues(900, 999)}}, 60); err != nil {
		t.Fatalf("store future sample: %v", err)
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: t2, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance, Metrics: []model.MetricSample{{ObservedAt: t2, Values: metricValues(110, 20)}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: t2, MetricsObservedAt: t2, Health: model.Health{State: model.HealthHealthy}}}}); err != nil {
		t.Fatalf("rollback refresh: %v", err)
	}
	repository, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	server := newAPIServer(t, repository, newCandidateAdapterSpy(), &fakeRefresher{})
	jsonResponse := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics", nil)
	var body struct {
		Result struct {
			ObservedAt time.Time         `json:"observed_at"`
			Instances  []instanceMetrics `json:"instances"`
		} `json:"result"`
	}
	if err := json.Unmarshal(jsonResponse.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rollback metrics: %v", err)
	}
	if jsonResponse.Code != http.StatusOK || !body.Result.ObservedAt.Equal(t2) || len(body.Result.Instances) != 1 || body.Result.Instances[0].MetricsObservedAt == nil || !body.Result.Instances[0].MetricsObservedAt.Equal(t2) || body.Result.Instances[0].Values["connections"] != 20 || body.Result.Instances[0].Values["qps"] != 1 {
		t.Fatalf("rollback JSON metrics used wrong sample: %d %s", jsonResponse.Code, jsonResponse.Body.String())
	}
	prometheus := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/metrics/prometheus", nil)
	if !strings.Contains(prometheus.Body.String(), "clusterguard_mysql_connections") || strings.Contains(prometheus.Body.String(), " 999") {
		t.Fatalf("rollback Prometheus metrics used future sample: %s", prometheus.Body.String())
	}
}
