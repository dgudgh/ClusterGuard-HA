package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type fakeRunner struct {
	identity        Row
	replication     Row
	replicationRows []Row
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
		if runner.replicationRows != nil {
			return runner.replicationRows, nil
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
	if len(result.Instance.EngineIdentity) != 1 || result.Instance.EngineMetadata["server_id"] != "22" {
		t.Fatalf("server ID must be metadata, not native identity: identity=%+v metadata=%+v", result.Instance.EngineIdentity, result.Instance.EngineMetadata)
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
	if got := result.Instance.EngineMetadata; got["version"] != "8.0.44" || got["gtid_mode"] != "ON" || got["gtid_purged"] != testPrimaryServerUUID+":1-5" || got["log_bin"] != "1" || got["binlog_format"] != "ROW" {
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
	if !capabilities.Supports(adapter.CapabilityCandidates) {
		t.Fatalf("MySQL must advertise read-only candidate evaluation")
	}
	if _, err := adapterInstance.Execute(context.Background(), adapter.OperationRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("MySQL execute must remain unsupported, got %v", err)
	}
}

func TestTopologyReportsReadOnlyNativeReplicationLink(t *testing.T) {
	runner := healthyReplicaRunner("8.0.44", modernReplicationRow())
	adapterInstance := New(runner)
	discovery, err := adapterInstance.Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	queryCount := len(runner.queries)
	result, err := adapterInstance.Topology(context.Background(), adapterRequest(), discovery)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(runner.queries) != queryCount {
		t.Fatalf("topology repeated the database probe: before=%d after=%d", queryCount, len(runner.queries))
	}
	if !adapterInstance.Capabilities(context.Background()).Supports(adapter.CapabilityTopology) {
		t.Fatal("MySQL must advertise implemented read-only topology")
	}
	if len(result.Links) != 1 {
		t.Fatalf("topology links = %+v", result.Links)
	}
	link := result.Links[0]
	if link.SourceIdentity["server_uuid"] != "source-uuid" || link.TargetIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("topology link identities = %+v", link)
	}
	if !link.Healthy || link.LagSeconds == nil || *link.LagSeconds != 2 {
		t.Fatalf("topology link health = %+v", link)
	}
}

func TestDiscoverCapturesGlobalGTIDExecutedForWritablePrimary(t *testing.T) {
	runner := &fakeRunner{identity: identityRow("8.0.44", "0", "0")}
	runner.identity["gtid_executed"] = testPrimaryServerUUID + ":1-20"

	result, err := New(runner).Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover primary: %v", err)
	}
	if result.Instance.Role != model.RolePrimary || result.Instance.EngineMetadata["gtid_executed"] != testPrimaryServerUUID+":1-20" {
		t.Fatalf("global GTID was not captured for writable primary: %+v", result.Instance)
	}
	if len(runner.queries) == 0 || !strings.Contains(runner.queries[0], "@@GLOBAL.gtid_executed AS gtid_executed") {
		t.Fatalf("identity query did not read global GTID state: %v", runner.queries)
	}
	if !strings.Contains(runner.queries[0], "@@GLOBAL.gtid_purged AS gtid_purged") {
		t.Fatalf("identity query did not read purged GTID state: %v", runner.queries)
	}
}

func TestDiscoverDoesNotInferGlobalGTIDFromReplicaStatus(t *testing.T) {
	runner := healthyReplicaRunner("8.0.44", modernReplicationRow())
	runner.identity["gtid_executed"] = ""

	result, err := New(runner).Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover replica: %v", err)
	}
	if result.Instance.Replication.ExecutedPosition == "" {
		t.Fatalf("test fixture has no replica executed position: %+v", result.Instance.Replication)
	}
	if got := result.Instance.EngineMetadata["gtid_executed"]; got != "" {
		t.Fatalf("global GTID %q was inferred from replica-only status", got)
	}
}

func TestDiscoverAllowsEmptyGlobalGTIDWhenGTIDModeIsDisabled(t *testing.T) {
	runner := &fakeRunner{identity: identityRow("8.0.44", "0", "0")}
	runner.identity["gtid_mode"] = "OFF"
	runner.identity["gtid_executed"] = ""

	result, err := New(runner).Discover(context.Background(), adapterRequest())
	if err != nil {
		t.Fatalf("discover with GTID disabled: %v", err)
	}
	if result.Instance.EngineMetadata["gtid_mode"] != "OFF" || result.Instance.EngineMetadata["gtid_executed"] != "" {
		t.Fatalf("unexpected GTID metadata: %+v", result.Instance.EngineMetadata)
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
		"gtid_executed":   testPrimaryServerUUID + ":1-10",
		"gtid_purged":     testPrimaryServerUUID + ":1-5",
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
