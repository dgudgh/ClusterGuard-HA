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
	for _, candidate := range []adapter.DatabaseHAAdapter{mysql.New(nil), postgresql.New(), oracle.New(), sqlserver.New()} {
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
