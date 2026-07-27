package adapter_test

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestRegistryProvidesAllSupportedDatabaseEngines(t *testing.T) {
	registry := adapter.NewRegistry()
	for _, candidate := range []adapter.DatabaseHAAdapter{
		mysql.New(nil),
		postgresql.New(),
		oracle.New(),
		sqlserver.New(),
	} {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register %s: %v", candidate.Engine(), err)
		}
	}
	for _, engine := range model.SupportedEngines() {
		candidate, ok := registry.Get(engine)
		if !ok || candidate.Engine() != engine {
			t.Fatalf("adapter missing for %s", engine)
		}
	}
	if len(registry.Engines()) != 4 {
		t.Fatalf("unexpected registered engines: %v", registry.Engines())
	}
}

func TestSkeletonAdaptersFailClosedForMutation(t *testing.T) {
	request := adapter.OperationRequest{Operation: model.Operation{Engine: model.EnginePostgreSQL, Kind: model.OperationFailover}}
	for _, candidate := range []adapter.DatabaseHAAdapter{postgresql.New(), oracle.NewWithBrokerRunner(oracle.UnsupportedBrokerRunner{}), sqlserver.NewWithRunner(sqlserver.UnsupportedRunner{})} {
		capabilities := candidate.Capabilities(context.Background())
		if capabilities.Supports(adapter.CapabilityExecute) {
			t.Fatalf("%s must not advertise execution without configured runner/providers", candidate.Engine())
		}
		if _, err := candidate.Execute(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s execute must fail closed, got %v", candidate.Engine(), err)
		}
	}
}

func TestUnimplementedAdaptersFailClosedForTopologyReadExtensions(t *testing.T) {
	for _, candidate := range []adapter.DatabaseHAAdapter{oracle.New(), sqlserver.New()} {
		capabilities := candidate.Capabilities(context.Background())
		if capabilities.Supports(adapter.CapabilityMetrics) {
			t.Fatalf("%s must not advertise metrics before implementation", candidate.Engine())
		}
		if _, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s metrics must be unsupported: %v", candidate.Engine(), err)
		}
		if !capabilities.Supports(adapter.CapabilityCandidates) {
			t.Fatalf("%s must advertise metadata-based candidate evaluation", candidate.Engine())
		}
		if _, err := candidate.EvaluateCandidates(context.Background(), adapter.CandidateRequest{}); err != nil {
			t.Fatalf("%s empty candidate evaluation must be safe: %v", candidate.Engine(), err)
		}
	}
}

type postgresqlMetricsRunner struct{}

func (postgresqlMetricsRunner) Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]postgresql.Row, error) {
	return []postgresql.Row{{
		"connections": "18", "active_connections": "3", "transactions_total": "100",
		"deadlocks_total": "1", "temp_bytes_total": "4096", "blocks_read_total": "25",
		"blocks_hit_total": "975", "database_size_bytes": "1048576", "replication_clients": "2",
		"max_transaction_age_seconds": "",
	}}, nil
}

func TestPostgreSQLProvidesReadCapabilitiesAndFailsClosedForExecution(t *testing.T) {
	candidate := postgresql.New(postgresqlMetricsRunner{})
	capabilities := candidate.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityCandidates) {
		t.Fatal("postgresql must advertise candidate evaluation")
	}
	if !capabilities.Supports(adapter.CapabilityMetrics) {
		t.Fatalf("postgresql must advertise native metrics: %+v", capabilities.Features)
	}
	if capabilities.Supports(adapter.CapabilityExecute) {
		t.Fatalf("postgresql must fail closed without node and endpoint providers: %+v", capabilities.Features)
	}
	samples, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{})
	if err != nil {
		t.Fatalf("postgresql metrics: %v", err)
	}
	if len(samples) != 1 || samples[0].Values["connections"] != 18 || samples[0].Values["buffer_cache_hit_ratio"] != 0.975 {
		t.Fatalf("unexpected postgresql metric samples: %+v", samples)
	}
}

type mysqlMetricsRunner struct{}

func (mysqlMetricsRunner) Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]mysql.Row, error) {
	return []mysql.Row{
		{"Variable_name": "Questions", "Value": "1000"},
		{"Variable_name": "Com_commit", "Value": "60"},
		{"Variable_name": "Com_rollback", "Value": "40"},
		{"Variable_name": "Threads_connected", "Value": "18"},
		{"Variable_name": "Threads_running", "Value": "3"},
		{"Variable_name": "Slow_queries", "Value": "7"},
		{"Variable_name": "Innodb_buffer_pool_reads", "Value": "25"},
		{"Variable_name": "Innodb_buffer_pool_read_requests", "Value": "1000"},
	}, nil
}

func TestMySQLProvidesMetricsAndCandidatesWithoutExecution(t *testing.T) {
	candidate := mysql.New(mysqlMetricsRunner{})
	capabilities := candidate.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityMetrics) {
		t.Fatal("mysql must advertise implemented metrics")
	}
	if !capabilities.Supports(adapter.CapabilityCandidates) {
		t.Fatal("mysql must advertise implemented candidate evaluation")
	}
	if capabilities.Supports(adapter.CapabilityExecute) {
		t.Fatal("mysql must not advertise mutation execution")
	}
	samples, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{})
	if err != nil {
		t.Fatalf("mysql metrics: %v", err)
	}
	if len(samples) != 1 || samples[0].Values["questions_total"] != 1000 || samples[0].Values["transactions_total"] != 100 {
		t.Fatalf("unexpected mysql metric samples: %+v", samples)
	}
	assessments, err := candidate.EvaluateCandidates(context.Background(), adapter.CandidateRequest{})
	if err != nil || len(assessments) != 0 {
		t.Fatalf("mysql candidate evaluation: assessments=%+v err=%v", assessments, err)
	}
	if _, err := candidate.Execute(context.Background(), adapter.OperationRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("mysql execute must remain unsupported: %v", err)
	}
}
