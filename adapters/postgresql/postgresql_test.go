package postgresql

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestCapabilitiesAdvertisePlanningButNotUnconfiguredPostgreSQLExecution(t *testing.T) {
	candidate := New(&fakeRunner{rows: []Row{primaryProbeRow()}})
	capabilities := candidate.Capabilities(context.Background())
	for _, feature := range []adapter.Capability{
		adapter.CapabilityDiscover,
		adapter.CapabilityTopology,
		adapter.CapabilityHealth,
		adapter.CapabilityCandidates,
		adapter.CapabilityMetadataReconcile,
		adapter.CapabilityPrecheck,
		adapter.CapabilityPlan,
		adapter.CapabilityMetrics,
	} {
		if !capabilities.Supports(feature) {
			t.Fatalf("PostgreSQL capability %s must be available: %+v", feature, capabilities.Features[feature])
		}
	}
	for _, feature := range []adapter.Capability{
		adapter.CapabilityExecute,
		adapter.CapabilityVerify,
		adapter.CapabilityNodeSync,
	} {
		if capabilities.Supports(feature) {
			t.Fatalf("PostgreSQL capability %s must remain unavailable", feature)
		}
	}
}

func TestTopologyUsesDiscoveryEvidenceWithoutSecondProbe(t *testing.T) {
	runner := &fakeRunner{rows: []Row{standbyProbeRow()}}
	candidate := New(runner)
	discovery, err := candidate.Discover(context.Background(), postgresqlRequest())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	queryCount := len(runner.queries)
	topology, err := candidate.Topology(context.Background(), postgresqlRequest(), discovery)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(runner.queries) != queryCount {
		t.Fatalf("topology repeated database query: before=%d after=%d", queryCount, len(runner.queries))
	}
	if len(topology.Links) != 1 {
		t.Fatalf("links = %+v", topology.Links)
	}
	link := topology.Links[0]
	if link.SourceIdentity["resource_id"] != testPostgreSQLPrimaryID || link.TargetIdentity["resource_id"] != testPostgreSQLNodeID || !link.Healthy {
		t.Fatalf("unexpected link: %+v", link)
	}
}

func TestPostgreSQLMutationMethodsFailClosed(t *testing.T) {
	candidate := New(&fakeRunner{})
	request := adapter.OperationRequest{Operation: model.Operation{Engine: model.EnginePostgreSQL, Kind: model.OperationSwitchover}}
	if _, err := candidate.Precheck(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("precheck error = %v", err)
	}
	if _, err := candidate.BuildPlan(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("plan error = %v", err)
	}
	if _, err := candidate.Execute(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("execute error = %v", err)
	}
	if _, err := candidate.Verify(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("verify error = %v", err)
	}
	if _, err := candidate.ExecuteNodeSync(context.Background(), request); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("node sync error = %v", err)
	}
}

func TestMetadataReconciliationRequiresBothStableIdentities(t *testing.T) {
	candidate := New(&fakeRunner{})
	valid := model.DatabaseInstance{
		Engine: model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       testPostgreSQLNodeID,
			"system_identifier": "7428625847249870011",
		},
	}
	checks, err := candidate.MetadataPrecheck(context.Background(), adapter.MetadataRequest{Instance: valid})
	if err != nil || len(checks) != 1 || checks[0].Status != model.CheckPass {
		t.Fatalf("valid metadata precheck = %+v, %v", checks, err)
	}
	result, err := candidate.ReconcileMetadata(context.Background(), adapter.MetadataRequest{Instance: valid})
	if err != nil || len(result.Checks) != 1 || result.Checks[0].Status != model.CheckPass {
		t.Fatalf("valid metadata reconcile = %+v, %v", result, err)
	}

	invalid := valid
	invalid.EngineIdentity = model.EngineIdentity{"resource_id": testPostgreSQLNodeID}
	checks, err = candidate.MetadataPrecheck(context.Background(), adapter.MetadataRequest{Instance: invalid})
	if err != nil || len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("invalid metadata precheck = %+v, %v", checks, err)
	}
}
