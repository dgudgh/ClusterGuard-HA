package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func monitoringRequest(t *testing.T, server *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func prepareMonitoringAPI(t *testing.T) (*Server, model.DatabaseCluster, model.ResourceID) {
	t.Helper()
	server, repository := newTestServer(t)
	WithMonitoringToken("monitor-secret")(server)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{
			{Kind: model.EndpointDatabase, Hostname: "mysql-primary", Port: 3306, Active: true},
			{Kind: model.EndpointDatabase, Hostname: "mysql-replica", Port: 3306, Active: true},
		},
	)
	if err != nil {
		t.Fatalf("create monitoring inventory: %v", err)
	}
	primaryID := model.NewResourceID()
	replicaID := model.NewResourceID()
	lag := int64(45)
	observedAt := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	instances := []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}, Hostname: "mysql-primary", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, EngineMetadata: map[string]string{"version": "8.0.44"}},
		{ResourceMeta: model.ResourceMeta{ResourceID: replicaID}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"}, Hostname: "mysql-replica", Port: 3306, Role: model.RoleReplica, Health: model.Health{State: model.HealthDegraded}, Replication: model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadRunning, LagSeconds: &lag}, EngineMetadata: map[string]string{"version": "8.0.44"}},
	}
	published, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instances[0]}, {EndpointID: endpoints[1].ResourceID, Instance: instances[1]}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}, {EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthDegraded}}},
	})
	if err != nil {
		t.Fatalf("publish monitoring topology: %v", err)
	}
	for _, instance := range published.Instances {
		if instance.Role == model.RoleReplica {
			replicaID = instance.ResourceID
		}
	}
	return server, cluster, replicaID
}

func preparePostgreSQLMonitoringAPI(t *testing.T) (*Server, model.DatabaseCluster, model.ResourceID) {
	t.Helper()
	server, repository := newTestServer(t)
	WithMonitoringToken("monitor-secret")(server)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-orders"},
		[]model.Endpoint{
			{Kind: model.EndpointDatabase, Hostname: "pg-primary", Port: 5432, Active: true},
			{Kind: model.EndpointDatabase, Hostname: "pg-standby", Port: 5432, Active: true},
		},
	)
	if err != nil {
		t.Fatalf("create PostgreSQL monitoring inventory: %v", err)
	}
	primaryID := model.ResourceID("11111111-1111-4111-8111-111111111111")
	standbyID := model.ResourceID("22222222-2222-4222-8222-222222222222")
	systemID := "7428625847249870011"
	lag := int64(7)
	observedAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	instances := []model.DatabaseInstance{
		{
			ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: cluster.ResourceID, Engine: model.EnginePostgreSQL,
			EngineIdentity: model.EngineIdentity{"resource_id": string(primaryID), "system_identifier": systemID},
			Hostname:       "pg-primary", Port: 5432, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
			EngineMetadata: map[string]string{"version": "16.3", "timeline_id": "4"},
		},
		{
			ResourceMeta: model.ResourceMeta{ResourceID: standbyID}, ClusterID: cluster.ResourceID, Engine: model.EnginePostgreSQL,
			EngineIdentity: model.EngineIdentity{"resource_id": string(standbyID), "system_identifier": systemID},
			Hostname:       "pg-standby", Port: 5432, Role: model.RoleStandby, Health: model.Health{State: model.HealthHealthy},
			Replication: model.ReplicationStatus{
				SourceIdentity: model.EngineIdentity{"resource_id": string(primaryID), "system_identifier": systemID},
				IOThread:       model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
			},
			EngineMetadata: map[string]string{"version": "16.3", "timeline_id": "4"},
		},
	}
	published, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, ClusterIdentity: model.EngineIdentity{"system_identifier": systemID},
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt,
		Observations: []store.DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: instances[0]},
			{EndpointID: endpoints[1].ResourceID, Instance: instances[1], Metrics: []model.MetricSample{{ObservedAt: observedAt, Values: map[string]float64{
				"connections": 12, "active_connections": 3, "transactions_total": 140,
				"deadlocks_total": 2, "conflicts_total": 4, "temp_bytes_total": 8192,
				"blocks_read_total": 30, "blocks_hit_total": 1170, "database_size_bytes": 2097152,
				"replication_clients": 1, "buffer_cache_hit_ratio": 0.975, "wal_bytes": 16384,
			}}}},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
			{EndpointID: endpoints[1].ResourceID, DiscoveryObservedAt: observedAt, MetricsObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
		},
	})
	if err != nil {
		t.Fatalf("publish PostgreSQL monitoring topology: %v", err)
	}
	for _, instance := range published.Instances {
		if instance.Role == model.RoleStandby {
			standbyID = instance.ResourceID
		}
	}
	return server, cluster, standbyID
}

func TestMonitoringAPIRequiresDedicatedReadToken(t *testing.T) {
	server, _, _ := prepareMonitoringAPI(t)
	for _, token := range []string{"", "wrong", testControlToken} {
		response := monitoringRequest(t, server, "/api/v1/monitoring/health", token)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("monitoring token %q status=%d body=%s", token, response.Code, response.Body.String())
		}
	}
	if response := monitoringRequest(t, server, "/api/v1/monitoring/health", "monitor-secret"); response.Code != http.StatusOK {
		t.Fatalf("valid monitoring token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMonitoringTokenRemainsDedicatedWhenPlatformAuthenticationIsEnabled(t *testing.T) {
	server, _, _ := prepareMonitoringAPI(t)
	server.authentication = platformauth.New(
		server.store,
		platformauth.Argon2Hasher{
			Params: platformauth.Argon2Params{
				Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
			},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 256)),
		},
		bytes.NewReader(bytes.Repeat([]byte{0x32}, 256)),
		time.Now,
		8*time.Hour,
	)

	response := monitoringRequest(t, server, "/api/v1/monitoring/health", "monitor-secret")
	if response.Code != http.StatusOK {
		t.Fatalf("platform authentication intercepted monitoring token: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMonitoringHealthAndZabbixExposeStableUUIDAlertsWithoutHostnames(t *testing.T) {
	server, cluster, replicaID := prepareMonitoringAPI(t)
	health := monitoringRequest(t, server, "/api/v1/monitoring/health", "monitor-secret")
	if health.Code != http.StatusOK {
		t.Fatalf("monitoring health: %d %s", health.Code, health.Body.String())
	}
	for _, expected := range []string{string(cluster.ResourceID), string(replicaID), "replication_io_thread_stopped", "replication_lag_critical", `"overall_status":"critical"`} {
		if !strings.Contains(health.Body.String(), expected) {
			t.Fatalf("monitoring health missing %q: %s", expected, health.Body.String())
		}
	}
	if strings.Contains(health.Body.String(), "mysql-primary") || strings.Contains(health.Body.String(), "mysql-replica") {
		t.Fatalf("monitoring health exposed mutable hostname: %s", health.Body.String())
	}

	zabbix := monitoringRequest(t, server, "/api/v1/monitoring/zabbix", "monitor-secret")
	if zabbix.Code != http.StatusOK {
		t.Fatalf("Zabbix API: %d %s", zabbix.Code, zabbix.Body.String())
	}
	for _, expected := range []string{`"key":"clusterguard.cluster.health"`, `"key":"clusterguard.alerts.critical"`, string(cluster.ResourceID)} {
		if !strings.Contains(zabbix.Body.String(), expected) {
			t.Fatalf("Zabbix API missing %q: %s", expected, zabbix.Body.String())
		}
	}
}

func TestFleetPrometheusAndJSONMetricsUseStableUUIDLabels(t *testing.T) {
	server, cluster, replicaID := prepareMonitoringAPI(t)
	prometheus := monitoringRequest(t, server, "/api/v1/monitoring/prometheus", "monitor-secret")
	if prometheus.Code != http.StatusOK || !strings.Contains(prometheus.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("fleet Prometheus: %d %s", prometheus.Code, prometheus.Body.String())
	}
	for _, expected := range []string{
		`clusterguard_control_plane_ready `,
		`clusterguard_control_plane_leader `,
		`clusterguard_control_plane_quorum_confirmed `,
		`clusterguard_control_plane_metadata_revision `,
		`clusterguard_control_plane_operations{state="active"} `,
		`clusterguard_control_plane_lifecycle_tasks{state="active"} `,
		`clusterguard_cluster_health{cluster_id="` + string(cluster.ResourceID) + `"} 0`,
		`clusterguard_alerts_total{cluster_id="` + string(cluster.ResourceID) + `",severity="critical"}`,
	} {
		if !strings.Contains(prometheus.Body.String(), expected) {
			t.Fatalf("fleet Prometheus missing %q: %s", expected, prometheus.Body.String())
		}
	}
	metrics := monitoringRequest(t, server, "/api/v1/monitoring/metrics", "monitor-secret")
	if metrics.Code != http.StatusOK || !bytes.Contains(metrics.Body.Bytes(), []byte(string(replicaID))) || !bytes.Contains(metrics.Body.Bytes(), []byte(string(cluster.ResourceID))) {
		t.Fatalf("fleet JSON metrics: %d %s", metrics.Code, metrics.Body.String())
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(metrics.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode fleet metrics: %v", err)
	}
}

func TestPostgreSQLMonitoringUsesEngineScopedLagAndCountsStandbys(t *testing.T) {
	server, cluster, standbyID := preparePostgreSQLMonitoringAPI(t)
	health := monitoringRequest(t, server, "/api/v1/monitoring/health", "monitor-secret")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"engine":"postgresql"`) || !strings.Contains(health.Body.String(), `"replica_count":1`) {
		t.Fatalf("PostgreSQL monitoring health: %d %s", health.Code, health.Body.String())
	}

	zabbix := monitoringRequest(t, server, "/api/v1/monitoring/zabbix", "monitor-secret")
	for _, expected := range []string{"clusterguard.postgresql.replication_lag_seconds_max", "clusterguard.postgresql.replication_lag_seconds", "clusterguard.postgresql.transactions_total", "clusterguard.postgresql.buffer_cache_hit_ratio", string(standbyID)} {
		if !strings.Contains(zabbix.Body.String(), expected) {
			t.Fatalf("PostgreSQL Zabbix metrics missing %q: %s", expected, zabbix.Body.String())
		}
	}
	if strings.Contains(zabbix.Body.String(), "clusterguard.mysql.") {
		t.Fatalf("PostgreSQL Zabbix metrics were mislabeled as MySQL: %s", zabbix.Body.String())
	}

	for _, path := range []string{"/api/v1/monitoring/prometheus", "/api/v1/clusters/" + string(cluster.ResourceID) + "/metrics/prometheus"} {
		prometheus := monitoringRequest(t, server, path, "monitor-secret")
		if prometheus.Code != http.StatusOK || !strings.Contains(prometheus.Body.String(), `clusterguard_postgresql_replication_lag_seconds{cluster_id="`+string(cluster.ResourceID)+`",instance_id="`+string(standbyID)+`"} 7`) || !strings.Contains(prometheus.Body.String(), `clusterguard_postgresql_transactions_total{cluster_id="`+string(cluster.ResourceID)+`",instance_id="`+string(standbyID)+`"} 140`) || !strings.Contains(prometheus.Body.String(), `clusterguard_postgresql_buffer_cache_hit_ratio{cluster_id="`+string(cluster.ResourceID)+`",instance_id="`+string(standbyID)+`"} 0.975`) {
			t.Fatalf("PostgreSQL Prometheus route %s: %d %s", path, prometheus.Code, prometheus.Body.String())
		}
		for _, metadata := range []string{
			"# HELP clusterguard_postgresql_transactions_total ",
			"# TYPE clusterguard_postgresql_transactions_total counter",
			"# HELP clusterguard_postgresql_connections ",
			"# TYPE clusterguard_postgresql_connections gauge",
		} {
			if !strings.Contains(prometheus.Body.String(), metadata) {
				t.Fatalf("PostgreSQL Prometheus route %s missing metadata %q: %s", path, metadata, prometheus.Body.String())
			}
		}
		if strings.Contains(prometheus.Body.String(), "clusterguard_mysql_") {
			t.Fatalf("PostgreSQL Prometheus metrics were mislabeled as MySQL: %s", prometheus.Body.String())
		}
	}
}
