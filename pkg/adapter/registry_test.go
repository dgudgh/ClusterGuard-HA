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
	for _, candidate := range []adapter.DatabaseHAAdapter{postgresql.New(), oracle.New(), sqlserver.New()} {
		capabilities := candidate.Capabilities(context.Background())
		if capabilities.Supports(adapter.CapabilityExecute) {
			t.Fatalf("%s must not advertise execution in phase one", candidate.Engine())
		}
		if _, err := candidate.Execute(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s execute must fail closed, got %v", candidate.Engine(), err)
		}
	}
}

func TestAdaptersFailClosedForTopologyReadExtensions(t *testing.T) {
	for _, candidate := range []adapter.DatabaseHAAdapter{postgresql.New(), oracle.New(), sqlserver.New()} {
		capabilities := candidate.Capabilities(context.Background())
		if capabilities.Supports(adapter.CapabilityMetrics) {
			t.Fatalf("%s must not advertise metrics before implementation", candidate.Engine())
		}
		if capabilities.Supports(adapter.CapabilityCandidates) {
			t.Fatalf("%s must not advertise candidates before implementation", candidate.Engine())
		}
		if _, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s metrics must be unsupported: %v", candidate.Engine(), err)
		}
		if _, err := candidate.EvaluateCandidates(context.Background(), adapter.CandidateRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
			t.Fatalf("%s candidates must be unsupported: %v", candidate.Engine(), err)
		}
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

func TestMySQLProvidesMetricsButCandidatesRemainUnavailable(t *testing.T) {
	candidate := mysql.New(mysqlMetricsRunner{})
	capabilities := candidate.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityMetrics) {
		t.Fatal("mysql must advertise implemented metrics")
	}
	if capabilities.Supports(adapter.CapabilityCandidates) {
		t.Fatal("mysql must not advertise candidates before implementation")
	}
	samples, err := candidate.Metrics(context.Background(), adapter.DiscoverRequest{})
	if err != nil {
		t.Fatalf("mysql metrics: %v", err)
	}
	if len(samples) != 1 || samples[0].Values["questions_total"] != 1000 || samples[0].Values["transactions_total"] != 100 {
		t.Fatalf("unexpected mysql metric samples: %+v", samples)
	}
	if _, err := candidate.EvaluateCandidates(context.Background(), adapter.CandidateRequest{}); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("mysql candidates must be unsupported: %v", err)
	}
}
