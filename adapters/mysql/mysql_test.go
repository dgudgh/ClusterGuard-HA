package mysql

import (
	"context"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type fakeRunner struct {
	identity        Row
	replication     Row
	status          []Row
	replicaError    error
	queries         []string
	legacyQueryRuns int
}

func (runner *fakeRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	runner.queries = append(runner.queries, query)
	switch {
	case strings.HasPrefix(query, "SELECT"):
		return []Row{runner.identity}, nil
	case query == "SHOW REPLICA STATUS":
		if runner.replicaError != nil {
			return nil, runner.replicaError
		}
		if runner.replication == nil {
			return nil, nil
		}
		return []Row{runner.replication}, nil
	case query == "SHOW SLAVE STATUS":
		runner.legacyQueryRuns++
		if runner.replication == nil {
			return nil, nil
		}
		return []Row{runner.replication}, nil
	case query == "SHOW GLOBAL STATUS":
		return runner.status, nil
	default:
		return nil, nil
	}
}

func TestDiscoverUsesMySQLServerUUIDNotHostnameAsIdentity(t *testing.T) {
	runner := healthyReplicaRunner("8.0.44", modernReplicationRow())
	adapterInstance := New(runner)
	result, err := adapterInstance.Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if result.Instance.Engine != model.EngineMySQL || result.Instance.EngineIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("unexpected native identity: %+v", result.Instance)
	}
	if result.Instance.Hostname != "mysql-renamed" || result.Instance.IPAddress != "192.0.2.10" || result.Instance.Port != 3310 {
		t.Fatalf("unexpected discovered endpoint: %+v", result.Instance)
	}
	if result.Instance.Role != model.RoleReplica || result.Instance.Health.State != model.HealthHealthy {
		t.Fatalf("unexpected discovered role/health: %+v", result.Instance)
	}
	if result.Instance.Replication.SourceIdentity["server_uuid"] != "source-uuid" {
		t.Fatalf("unexpected replication state: %+v", result.Instance.Replication)
	}
	if got := result.Instance.EngineMetadata; got["version"] != "8.0.44" || got["gtid_mode"] != "ON" || got["log_bin"] != "1" || got["binlog_format"] != "ROW" {
		t.Fatalf("unexpected engine metadata: %+v", got)
	}
}

func TestHealthIsReadOnlyAndDoesNotAdvertiseExecution(t *testing.T) {
	runner := healthyReplicaRunner("8.0.44", modernReplicationRow())
	adapterInstance := New(runner)
	health, err := adapterInstance.Health(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.State != model.HealthHealthy || !strings.Contains(health.Summary, "replica") {
		t.Fatalf("unexpected health: %+v", health)
	}
	capabilities := adapterInstance.Capabilities(context.Background())
	if capabilities.Supports(adapter.CapabilityExecute) {
		t.Fatalf("MySQL must not advertise mutation execution")
	}
	if !capabilities.Supports(adapter.CapabilityMetrics) {
		t.Fatalf("MySQL must advertise read-only metrics")
	}
}

func healthyReplicaRunner(version string, replication Row) *fakeRunner {
	return &fakeRunner{
		identity:    identityRow(version, "1", "1"),
		replication: replication,
	}
}

func identityRow(version, readOnly, superReadOnly string) Row {
	return Row{
		"server_uuid":     "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"hostname":        "mysql-renamed",
		"port":            "3310",
		"server_id":       "22",
		"version":         version,
		"read_only":       readOnly,
		"super_read_only": superReadOnly,
		"gtid_mode":       "ON",
		"log_bin":         "1",
		"binlog_format":   "ROW",
	}
}

func modernReplicationRow() Row {
	return Row{
		"Source_UUID":           "source-uuid",
		"Replica_IO_Running":    "Yes",
		"Replica_SQL_Running":   "Yes",
		"Seconds_Behind_Source": "2",
		"Retrieved_Gtid_Set":    "source-uuid:1-10",
		"Executed_Gtid_Set":     "source-uuid:1-10",
	}
}

func legacyReplicationRow() Row {
	return Row{
		"Master_UUID":           "source-uuid",
		"Slave_IO_Running":      "Yes",
		"Slave_SQL_Running":     "Yes",
		"Seconds_Behind_Master": "2",
		"Retrieved_Gtid_Set":    "source-uuid:1-10",
		"Executed_Gtid_Set":     "source-uuid:1-10",
	}
}

func adapterRequest() adapter.DiscoverRequest {
	return adapter.DiscoverRequest{
		ClusterID:   model.NewResourceID(),
		Endpoint:    adapter.Endpoint{Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306},
		Credentials: adapter.Credentials{Username: "monitor", Password: "secret"},
	}
}
