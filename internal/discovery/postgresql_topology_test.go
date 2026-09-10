package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pgadapter "clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	pgReplayPrimaryNativeID      = "11000000-0000-4000-8000-000000000002"
	pgReplayStandbyOneNativeID   = "11000000-0000-4000-8000-000000000001"
	pgReplayStandbyThreeNativeID = "11000000-0000-4000-8000-000000000003"
	pgReplayOldPrimaryGUC        = "11000000-0000-4000-8000-000000000099"
	pgReplaySystemID             = "7428625847249870011"
)

var pgReplayStart = time.Date(2026, time.September, 7, 8, 0, 0, 0, time.UTC)

func TestPostgreSQLTopologyOfflineReplayStaysStableAcrossFiftyRoundsAndReplication(t *testing.T) {
	fixture := newPostgreSQLTopologyFixture(t)
	var snapshot model.TopologySnapshot
	var platformIDs map[string]model.ResourceID

	for round := 0; round < 50; round++ {
		fixture.clock.Set(pgReplayStart.Add(time.Duration(round) * time.Second))
		primaryGUC := ""
		if round > 0 {
			switch round % 3 {
			case 1:
				primaryGUC = pgReplayOldPrimaryGUC
			case 2:
				primaryGUC = string(platformIDs[pgReplayPrimaryNativeID])
			}
		}
		fixture.runner.updateRow("pg01", func(row pgadapter.Row) { row["primary_node_id"] = primaryGUC })
		fixture.runner.updateRow("pg03", func(row pgadapter.Row) { row["primary_node_id"] = primaryGUC })

		var err error
		snapshot, err = fixture.service.Refresh(context.Background(), fixture.cluster.ResourceID)
		if err != nil {
			t.Fatalf("offline replay round %d: %v", round+1, err)
		}
		assertPostgreSQLGreenTopology(t, snapshot)
		currentIDs := pgReplayPlatformIDs(snapshot.Instances)
		if round == 0 {
			platformIDs = currentIDs
			for nativeID, platformID := range platformIDs {
				if string(platformID) == nativeID {
					t.Fatalf("native ID was reused as platform ID for %s", nativeID)
				}
			}
		} else {
			for nativeID, want := range platformIDs {
				if got := currentIDs[nativeID]; got != want {
					t.Fatalf("round %d platform ID for %s=%s, want stable %s", round+1, nativeID, got, want)
				}
			}
		}
		for _, instance := range snapshot.Instances {
			if instance.Role != model.RoleStandby {
				continue
			}
			if got := instance.Replication.SourceIdentity["resource_id"]; got != pgReplayPrimaryNativeID {
				t.Fatalf("round %d standby %s trusted GUC %q instead of native primary %q", round+1, instance.Hostname, got, pgReplayPrimaryNativeID)
			}
		}
	}

	state, err := fixture.repository.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	follower := store.NewMemory()
	if err := follower.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply replicated state: %v", err)
	}
	replicated, found := follower.TopologySnapshot(fixture.cluster.ResourceID)
	if !found {
		t.Fatal("replicated state lost PostgreSQL topology snapshot")
	}
	assertPostgreSQLGreenTopology(t, replicated)
	if got, want := pgReplayEdges(replicated.Links), pgReplayEdges(snapshot.Links); !equalStringSlices(got, want) {
		t.Fatalf("replicated edges=%v, want %v", got, want)
	}

	fixture.clock.Set(snapshot.ObservedAt.Add(-time.Second))
	if _, err := fixture.service.Refresh(context.Background(), fixture.cluster.ResourceID); !errors.Is(err, store.ErrStaleObservation) {
		t.Fatalf("old observation error=%v, want ErrStaleObservation", err)
	}
	retained, found := fixture.repository.TopologySnapshot(fixture.cluster.ResourceID)
	if !found || !retained.ObservedAt.Equal(snapshot.ObservedAt) || !equalStringSlices(pgReplayEdges(retained.Links), pgReplayEdges(snapshot.Links)) {
		t.Fatalf("old observation overwrote topology: retained=%+v previous=%+v", retained, snapshot)
	}
}

func TestPostgreSQLTopologyOfflineReplayDoesNotDriftGreenOnInvalidEvidence(t *testing.T) {
	tests := map[string]struct {
		mutate           func(*postgresqlTopologyFixture)
		wantRefreshError bool
	}{
		"duplicate application name": {mutate: func(f *postgresqlTopologyFixture) {
			f.setPrimarySenders(pgReplayStandbyOneNativeID, pgReplayStandbyOneNativeID, pgReplayStandbyThreeNativeID)
		}},
		"application name is platform ID": {mutate: func(f *postgresqlTopologyFixture) {
			f.setPrimarySenders(string(f.platformIDs[pgReplayStandbyOneNativeID]), pgReplayStandbyThreeNativeID)
		}},
		"unknown receiver address": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["receiver_sender_host"] = "203.0.113.77" })
		}},
		"wrong receiver port": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["receiver_sender_port"] = "6432" })
		}},
		"wrong system identifier": {wantRefreshError: true, mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["system_identifier"] = "7428625847249870099" })
		}},
		"wrong timeline": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["timeline_id"] = "8" })
		}},
		"two writable primaries": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.setRow("pg01", pgReplayPrimaryRow(pgReplayStandbyOneNativeID))
		}},
		"empty sender evidence": {mutate: func(f *postgresqlTopologyFixture) {
			f.setPrimarySenders()
		}},
		"receiver disconnected": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["wal_receiver_status"] = "" })
		}},
		"replay paused": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.updateRow("pg01", func(row pgadapter.Row) { row["replay_paused"] = "true" })
		}},
		"database probe fails": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.setIdentityError("pg02", errors.New("fixture database unavailable"))
		}},
		"primary metrics fail": {mutate: func(f *postgresqlTopologyFixture) {
			f.runner.setMetricsError("pg02", errors.New("fixture metrics unavailable"))
		}},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPostgreSQLTopologyFixture(t)
			baseline, err := fixture.service.Refresh(context.Background(), fixture.cluster.ResourceID)
			if err != nil {
				t.Fatalf("baseline refresh: %v", err)
			}
			assertPostgreSQLGreenTopology(t, baseline)
			fixture.platformIDs = pgReplayPlatformIDs(baseline.Instances)
			test.mutate(fixture)
			fixture.clock.Set(baseline.ObservedAt.Add(time.Second))

			snapshot, err := fixture.service.Refresh(context.Background(), fixture.cluster.ResourceID)
			if test.wantRefreshError {
				if err == nil {
					t.Fatal("invalid mixed cluster identity was published")
				}
				retained, found := fixture.repository.TopologySnapshot(fixture.cluster.ResourceID)
				if !found || !retained.ObservedAt.Equal(baseline.ObservedAt) {
					t.Fatalf("failed refresh replaced last durable observation: %+v", retained)
				}
				return
			}
			if err != nil {
				t.Fatalf("invalid-evidence refresh: %v", err)
			}
			if !snapshot.ObservedAt.After(baseline.ObservedAt) {
				t.Fatalf("invalid evidence did not publish a new degraded observation: baseline=%s got=%s", baseline.ObservedAt, snapshot.ObservedAt)
			}
			if snapshot.Health.State == model.HealthHealthy {
				t.Fatalf("invalid evidence drifted cluster green: %+v", snapshot)
			}
			healthyLinks := 0
			for _, link := range snapshot.Links {
				if link.Healthy {
					healthyLinks++
				}
			}
			if healthyLinks == 2 {
				t.Fatalf("invalid evidence retained two healthy links: %+v", snapshot.Links)
			}
		})
	}
}

type postgresqlTopologyFixture struct {
	repository  *store.Repository
	service     *Service
	runner      *postgresqlReplayRunner
	clock       *postgresqlReplayClock
	cluster     model.DatabaseCluster
	platformIDs map[string]model.ResourceID
}

func newPostgreSQLTopologyFixture(t *testing.T) *postgresqlTopologyFixture {
	t.Helper()
	repository := store.NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-offline-replay"})
	if err != nil {
		t.Fatalf("create PostgreSQL cluster: %v", err)
	}
	for _, endpoint := range []model.Endpoint{
		{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "pg01", IPAddress: "10.20.0.1", Port: 5432, Active: true},
		{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "pg02", IPAddress: "10.20.0.2", Port: 5432, Active: true},
		{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "pg03", IPAddress: "10.20.0.3", Port: 5432, Active: true},
	} {
		if _, err := repository.UpsertEndpoint(endpoint); err != nil {
			t.Fatalf("create endpoint %s: %v", endpoint.Hostname, err)
		}
	}
	runner := newPostgreSQLReplayRunner()
	clock := &postgresqlReplayClock{now: pgReplayStart}
	registry := adapter.NewRegistry()
	if err := registry.Register(pgadapter.New(runner)); err != nil {
		t.Fatalf("register real PostgreSQL adapter: %v", err)
	}
	service := New(registry, repository, CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		return adapter.Credentials{Username: "probe", Password: "offline-fixture", Database: "postgres"}, nil
	}), clock.Now)
	return &postgresqlTopologyFixture{repository: repository, service: service, runner: runner, clock: clock, cluster: cluster}
}

func (fixture *postgresqlTopologyFixture) setPrimarySenders(applicationNames ...string) {
	senders := make([]string, 0, len(applicationNames))
	for index, applicationName := range applicationNames {
		senders = append(senders, fmt.Sprintf(`{"application_name":%q,"state":"streaming","client_addr":"10.20.0.%d","replay_lsn":"0/50000%02X"}`, applicationName, index+1, 40+index))
	}
	fixture.runner.updateRow("pg02", func(row pgadapter.Row) {
		row["replication_senders"] = "[" + strings.Join(senders, ",") + "]"
	})
}

type postgresqlReplayClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *postgresqlReplayClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *postgresqlReplayClock) Set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

type postgresqlReplayRunner struct {
	mu             sync.Mutex
	rows           map[string]pgadapter.Row
	identityErrors map[string]error
	metricsErrors  map[string]error
}

func newPostgreSQLReplayRunner() *postgresqlReplayRunner {
	runner := &postgresqlReplayRunner{
		rows: map[string]pgadapter.Row{
			"pg01": pgReplayStandbyRow(pgReplayStandbyOneNativeID),
			"pg02": pgReplayPrimaryRow(pgReplayPrimaryNativeID),
			"pg03": pgReplayStandbyRow(pgReplayStandbyThreeNativeID),
		},
		identityErrors: map[string]error{},
		metricsErrors:  map[string]error{},
	}
	return runner
}

func (runner *postgresqlReplayRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]pgadapter.Row, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if strings.Contains(query, "clusterguard_probe") {
		if err := runner.identityErrors[endpoint.Hostname]; err != nil {
			return nil, err
		}
		row, found := runner.rows[endpoint.Hostname]
		if !found {
			return nil, fmt.Errorf("no identity fixture for %s", endpoint.Hostname)
		}
		return []pgadapter.Row{clonePostgreSQLReplayRow(row)}, nil
	}
	if strings.Contains(query, "clusterguard_metrics") {
		if err := runner.metricsErrors[endpoint.Hostname]; err != nil {
			return nil, err
		}
		return []pgadapter.Row{pgReplayMetricsRow()}, nil
	}
	return nil, fmt.Errorf("unexpected PostgreSQL fixture query")
}

func (runner *postgresqlReplayRunner) updateRow(host string, update func(pgadapter.Row)) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	row := clonePostgreSQLReplayRow(runner.rows[host])
	update(row)
	runner.rows[host] = row
}

func (runner *postgresqlReplayRunner) setRow(host string, row pgadapter.Row) {
	runner.mu.Lock()
	runner.rows[host] = clonePostgreSQLReplayRow(row)
	runner.mu.Unlock()
}

func (runner *postgresqlReplayRunner) setIdentityError(host string, err error) {
	runner.mu.Lock()
	runner.identityErrors[host] = err
	runner.mu.Unlock()
}

func (runner *postgresqlReplayRunner) setMetricsError(host string, err error) {
	runner.mu.Lock()
	runner.metricsErrors[host] = err
	runner.mu.Unlock()
}

func pgReplayPrimaryRow(nativeID string) pgadapter.Row {
	return pgadapter.Row{
		"node_id": nativeID, "primary_node_id": "", "system_identifier": pgReplaySystemID,
		"hostname": "postgres-primary-container", "port": "5432", "version": "16.4",
		"in_recovery": "false", "transaction_read_only": "off", "replay_paused": "false",
		"wal_receiver_status": "", "receiver_sender_host": "", "receiver_sender_port": "",
		"replication_senders": fmt.Sprintf(`[{"application_name":%q,"state":"streaming","client_addr":"10.20.0.1","replay_lsn":"0/5000040"},{"application_name":%q,"state":"streaming","client_addr":"10.20.0.3","replay_lsn":"0/5000030"}]`, pgReplayStandbyOneNativeID, pgReplayStandbyThreeNativeID),
		"current_lsn":         "0/5000060", "receive_lsn": "", "replay_lsn": "", "receiver_latest_end_lsn": "",
		"lag_seconds": "", "timeline_id": "7", "wal_log_hints": "true", "data_checksum_version": "1",
	}
}

func pgReplayStandbyRow(nativeID string) pgadapter.Row {
	return pgadapter.Row{
		"node_id": nativeID, "primary_node_id": "", "system_identifier": pgReplaySystemID,
		"hostname": "postgres-standby-container", "port": "5432", "version": "16.4",
		"in_recovery": "true", "transaction_read_only": "on", "replay_paused": "false",
		"wal_receiver_status": "streaming", "receiver_sender_host": "pg02", "receiver_sender_port": "5432",
		"replication_senders": "[]", "current_lsn": "", "receive_lsn": "0/5000050", "replay_lsn": "0/5000040",
		"receiver_latest_end_lsn": "0/5000050", "lag_seconds": "0", "timeline_id": "7",
		"wal_log_hints": "true", "data_checksum_version": "1",
	}
}

func pgReplayMetricsRow() pgadapter.Row {
	return pgadapter.Row{
		"connections": "12", "active_connections": "2", "transactions_total": "1000",
		"deadlocks_total": "0", "conflicts_total": "0", "temp_bytes_total": "0",
		"blocks_read_total": "10", "blocks_hit_total": "990", "database_size_bytes": "1048576",
		"replication_clients": "2", "wal_bytes": "4096", "checkpoints_total": "4",
		"max_transaction_age_seconds": "1",
	}
}

func clonePostgreSQLReplayRow(row pgadapter.Row) pgadapter.Row {
	cloned := make(pgadapter.Row, len(row))
	for key, value := range row {
		cloned[key] = value
	}
	return cloned
}

func assertPostgreSQLGreenTopology(t *testing.T, snapshot model.TopologySnapshot) {
	t.Helper()
	if snapshot.Health.State != model.HealthHealthy || len(snapshot.Instances) != 3 || len(snapshot.Links) != 2 || len(snapshot.Probes) != 3 {
		t.Fatalf("topology is not complete and healthy: %+v", snapshot)
	}
	instances := make(map[string]model.DatabaseInstance, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		instances[instance.Hostname] = instance
		if instance.EngineIdentity["system_identifier"] != pgReplaySystemID || instance.EngineMetadata["timeline_id"] != "7" {
			t.Fatalf("instance lost system/timeline identity: %+v", instance)
		}
	}
	if instances["pg02"].Role != model.RolePrimary || instances["pg02"].EngineIdentity["resource_id"] != pgReplayPrimaryNativeID {
		t.Fatalf("pg02 is not the native primary: %+v", instances["pg02"])
	}
	for _, host := range []string{"pg01", "pg03"} {
		instance := instances[host]
		if instance.Role != model.RoleStandby || instance.Health.State != model.HealthHealthy || !instance.PromotionEligible ||
			instance.Replication.SourceIdentity["resource_id"] != pgReplayPrimaryNativeID {
			t.Fatalf("%s is not a verified standby: %+v", host, instance)
		}
	}
	for _, link := range snapshot.Links {
		if !link.Healthy || link.SourceInstanceID != instances["pg02"].ResourceID ||
			(link.TargetInstanceID != instances["pg01"].ResourceID && link.TargetInstanceID != instances["pg03"].ResourceID) {
			t.Fatalf("unexpected PostgreSQL replication link: %+v", link)
		}
	}
}

func pgReplayPlatformIDs(instances []model.DatabaseInstance) map[string]model.ResourceID {
	result := make(map[string]model.ResourceID, len(instances))
	for _, instance := range instances {
		result[instance.EngineIdentity["resource_id"]] = instance.ResourceID
	}
	return result
}

func pgReplayEdges(links []model.ReplicationLink) []string {
	edges := make([]string, 0, len(links))
	for _, link := range links {
		edges = append(edges, fmt.Sprintf("%s->%s:%t", link.SourceInstanceID, link.TargetInstanceID, link.Healthy))
	}
	sort.Strings(edges)
	return edges
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
