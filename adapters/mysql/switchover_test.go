package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	primaryUUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	targetUUID  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	extraUUID   = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

type endpointProviderStub struct {
	executable bool
	checks     []model.Check
	transfer   func(adapter.ResolvedOperation) error
	verify     model.Check
}

func (provider endpointProviderStub) Executable(context.Context) bool { return provider.executable }
func (provider endpointProviderStub) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return append([]model.Check{}, provider.checks...)
}
func (provider endpointProviderStub) AuthorizeTransition(ctx context.Context, _ adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	guarded, cancel := context.WithCancel(ctx)
	return adapter.TransitionAuthorization{Context: guarded, Cancel: cancel, Finalize: func(context.Context) error { return nil }}, nil
}
func (provider endpointProviderStub) Transfer(_ context.Context, resolved adapter.ResolvedOperation) error {
	if provider.transfer != nil {
		return provider.transfer(resolved)
	}
	return nil
}
func (provider endpointProviderStub) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	if provider.verify.Name == "" {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "writer endpoint has one target owner"}
	}
	return provider.verify
}

func passingEndpointProvider() endpointProviderStub {
	return endpointProviderStub{
		executable: true,
		checks:     []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "writer endpoint provider is ready"}},
	}
}

func switchoverRequestFixture() adapter.OperationRequest {
	observedAt := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	clusterID := model.NewResourceID()
	primaryID := model.NewResourceID()
	targetID := model.NewResourceID()
	lag := int64(0)
	cluster := model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID, MetadataRevision: 3},
		Engine:       model.EngineMySQL,
		DisplayName:  "mysql-production",
	}
	primary := model.DatabaseInstance{
		ResourceMeta:   model.ResourceMeta{ResourceID: primaryID, MetadataRevision: 5},
		ClusterID:      clusterID,
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": primaryUUID},
		DisplayName:    "db-primary",
		Hostname:       "db-primary",
		IPAddress:      "192.0.2.10",
		Port:           3306,
		Role:           model.RolePrimary,
		Health:         model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
		EngineMetadata: map[string]string{
			"server_id": "10", "version": "8.0.46", "gtid_mode": "ON",
			"gtid_executed": primaryUUID + ":1-100", "log_bin": "ON",
			"read_only": "false", "super_read_only": "false",
		},
	}
	target := model.DatabaseInstance{
		ResourceMeta:   model.ResourceMeta{ResourceID: targetID, MetadataRevision: 7},
		ClusterID:      clusterID,
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": targetUUID},
		DisplayName:    "db-replica",
		Hostname:       "db-replica",
		IPAddress:      "192.0.2.11",
		Port:           3306,
		Role:           model.RoleReplica,
		Health:         model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
		Replication: model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"server_uuid": primaryUUID},
			IOThread:       model.ThreadRunning, SQLThread: model.ThreadRunning,
			LagSeconds: &lag, ExecutedPosition: primaryUUID + ":1-100",
		},
		PromotionEligible: true,
		EngineMetadata: map[string]string{
			"server_id": "11", "version": "8.0.46", "gtid_mode": "ON",
			"gtid_executed": primaryUUID + ":1-100", "log_bin": "ON",
			"read_only": "true", "super_read_only": "true",
		},
	}
	snapshot := model.TopologySnapshot{
		ClusterID: clusterID,
		Instances: []model.DatabaseInstance{primary, target},
		Probes: []model.ProbeStatus{
			{InstanceID: primaryID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
			{InstanceID: targetID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}},
		},
		ObservedAt: observedAt,
	}
	return adapter.OperationRequest{
		Operation: model.Operation{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
			ClusterID:    clusterID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "dba",
		},
		TargetID: targetID,
		Resolved: &adapter.ResolvedOperation{
			Cluster: cluster, Snapshot: snapshot, Primary: primary, Target: target,
			Credentials:            adapter.Credentials{Username: "clusterguard", Password: "secret"},
			ReplicationCredentials: adapter.Credentials{Username: "replicator", Password: "replication-secret"},
		},
	}
}

func threeNodeSwitchoverRequestFixture() adapter.OperationRequest {
	request := switchoverRequestFixture()
	lag := int64(0)
	sibling := model.DatabaseInstance{
		ResourceMeta:   model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 9},
		ClusterID:      request.Operation.ClusterID,
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": extraUUID},
		DisplayName:    "db-sibling",
		Hostname:       "db-sibling",
		IPAddress:      "192.0.2.12",
		Port:           3306,
		Role:           model.RoleReplica,
		Health:         model.Health{State: model.HealthHealthy, ObservedAt: request.Resolved.Snapshot.ObservedAt},
		Replication: model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"server_uuid": primaryUUID},
			IOThread:       model.ThreadRunning, SQLThread: model.ThreadRunning,
			LagSeconds: &lag, ExecutedPosition: primaryUUID + ":1-100",
		},
		PromotionEligible: true,
		EngineMetadata: map[string]string{
			"server_id": "12", "version": "8.0.46", "gtid_mode": "ON",
			"gtid_executed": primaryUUID + ":1-100", "log_bin": "ON",
			"read_only": "true", "super_read_only": "true",
		},
	}
	request.Resolved.Snapshot.Instances = append(request.Resolved.Snapshot.Instances, sibling)
	request.Resolved.Snapshot.Probes = append(request.Resolved.Snapshot.Probes, model.ProbeStatus{
		InstanceID: sibling.ResourceID, DiscoveryObservedAt: request.Resolved.Snapshot.ObservedAt,
		Health: model.Health{State: model.HealthHealthy},
	})
	request.Resolved.ReplicationCredentials = adapter.Credentials{Username: "replicator", Password: "replication-secret"}
	return request
}

func TestSwitchoverPrecheckAcceptsHealthyThreeNodeTopology(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("healthy three-node topology failed check %+v", check)
		}
	}
}

func TestSwitchoverPrecheckAllowsMissingTransactionsThatExecutionCanCatchUp(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Target.Replication.ExecutedPosition = primaryUUID + ":1-99"
	sibling := &request.Resolved.Snapshot.Instances[2]
	sibling.Replication.ExecutedPosition = primaryUUID + ":1-99"

	adapterInstance := NewWithEndpointProvider(nil, passingEndpointProvider())
	checks, err := adapterInstance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	warnings := map[string]bool{}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("catch-up-safe topology failed check %+v", check)
		}
		if check.Status == model.CheckWarn {
			warnings[check.Name] = true
		}
	}
	if !warnings["gtid_consistency"] || !warnings["follower_readiness_"+string(sibling.ResourceID)] {
		t.Fatalf("missing GTID warnings: %+v", checks)
	}
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if plan.Summary != "guarded MySQL planned switchover is ready" {
		t.Fatalf("catch-up-safe plan was blocked: %+v", plan)
	}
}

func TestSwitchoverPrecheckWarnsForPrimaryOwnedGTIDFromLaterTargetSample(t *testing.T) {
	request := switchoverRequestFixture()
	request.Resolved.Primary.Health.ObservedAt = request.Resolved.Snapshot.ObservedAt.Add(-25 * time.Millisecond)
	request.Resolved.Target.Health.ObservedAt = request.Resolved.Snapshot.ObservedAt
	request.Resolved.Target.Replication.ExecutedPosition = primaryUUID + ":1-101"

	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Name == "gtid_consistency" {
			if check.Status != model.CheckWarn {
				t.Fatalf("GTID consistency check = %+v, want warning", check)
			}
			return
		}
	}
	t.Fatalf("GTID consistency check is missing: %+v", checks)
}

func TestSwitchoverPrecheckWarnsForPrimaryOwnedGTIDFromLaterFollowerSample(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	request.Resolved.Primary.Health.ObservedAt = request.Resolved.Snapshot.ObservedAt.Add(-25 * time.Millisecond)
	sibling := &request.Resolved.Snapshot.Instances[2]
	sibling.Health.ObservedAt = request.Resolved.Snapshot.ObservedAt
	sibling.Replication.ExecutedPosition = primaryUUID + ":1-101"

	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	name := "follower_readiness_" + string(sibling.ResourceID)
	for _, check := range checks {
		if check.Name == name {
			if check.Status != model.CheckWarn {
				t.Fatalf("follower readiness check = %+v, want warning", check)
			}
			return
		}
	}
	t.Fatalf("follower readiness check is missing: %+v", checks)
}

func TestSwitchoverPrecheckBlocksUnsafeSibling(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	sibling := &request.Resolved.Snapshot.Instances[2]
	sibling.Replication.ExecutedPosition += "," + targetUUID + ":1"
	checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "follower_readiness_"+string(sibling.ResourceID)) {
		t.Fatalf("unsafe sibling checks=%+v", checks)
	}
}

func TestSwitchoverPlanPinsEveryFollowerRevisionAndReparentStep(t *testing.T) {
	request := threeNodeSwitchoverRequestFixture()
	plan, err := NewWithEndpointProvider(nil, passingEndpointProvider()).BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	for _, instance := range request.Resolved.Snapshot.Instances {
		if plan.ResourceRevisions[instance.ResourceID] != instance.MetadataRevision {
			t.Fatalf("resource %s revision not pinned: %+v", instance.ResourceID, plan.ResourceRevisions)
		}
		if instance.ResourceID == request.TargetID {
			continue
		}
		step := "reparent_follower_" + string(instance.ResourceID)
		found := false
		for _, candidate := range plan.Steps {
			if candidate.Name == step && candidate.TargetID == instance.ResourceID {
				found = true
			}
		}
		if !found {
			t.Fatalf("plan missing %s: %+v", step, plan.Steps)
		}
	}
}

func failedCheck(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func TestSwitchoverPrecheckPassesOnlyStrictTwoNodeTopology(t *testing.T) {
	request := switchoverRequestFixture()
	adapterInstance := NewWithEndpointProvider(nil, passingEndpointProvider())
	checks, err := adapterInstance.Precheck(context.Background(), request)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	for _, check := range checks {
		if check.Status == model.CheckFail {
			t.Fatalf("strict fixture failed check %+v", check)
		}
	}
	capabilities := adapterInstance.Capabilities(context.Background())
	if !capabilities.Supports(adapter.CapabilityPrecheck) || !capabilities.Supports(adapter.CapabilityPlan) {
		t.Fatalf("implemented read-only switchover stages were not advertised: %+v", capabilities)
	}
}

func TestSwitchoverPrecheckBlocksUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name   string
		check  string
		mutate func(*adapter.OperationRequest)
	}{
		{name: "nonzero lag", check: "replication_lag", mutate: func(request *adapter.OperationRequest) {
			value := int64(1)
			request.Resolved.Target.Replication.LagSeconds = &value
		}},
		{name: "unknown lag", check: "replication_lag", mutate: func(request *adapter.OperationRequest) { request.Resolved.Target.Replication.LagSeconds = nil }},
		{name: "stopped thread", check: "replication_threads", mutate: func(request *adapter.OperationRequest) {
			request.Resolved.Target.Replication.SQLThread = model.ThreadStopped
		}},
		{name: "writable target", check: "target_read_only", mutate: func(request *adapter.OperationRequest) {
			request.Resolved.Target.EngineMetadata["read_only"] = "false"
			request.Resolved.Target.EngineMetadata["super_read_only"] = "false"
		}},
		{name: "maintenance", check: "target_maintenance", mutate: func(request *adapter.OperationRequest) { request.Resolved.Target.Maintenance = true }},
		{name: "wrong source", check: "replication_source", mutate: func(request *adapter.OperationRequest) {
			request.Resolved.Target.Replication.SourceIdentity["server_uuid"] = extraUUID
		}},
		{name: "gtid disabled", check: "gtid_mode", mutate: func(request *adapter.OperationRequest) { request.Resolved.Target.EngineMetadata["gtid_mode"] = "OFF" }},
		{name: "binary logging disabled", check: "binary_logging", mutate: func(request *adapter.OperationRequest) { request.Resolved.Primary.EngineMetadata["log_bin"] = "OFF" }},
		{name: "errant transaction", check: "gtid_consistency", mutate: func(request *adapter.OperationRequest) {
			request.Resolved.Target.Replication.ExecutedPosition += "," + extraUUID + ":1"
		}},
		{name: "version mismatch", check: "version_compatibility", mutate: func(request *adapter.OperationRequest) { request.Resolved.Target.EngineMetadata["version"] = "5.7.44" }},
		{name: "incomplete probe", check: "probe_coverage", mutate: func(request *adapter.OperationRequest) {
			request.Resolved.Snapshot.Probes[1].Health.State = model.HealthUnknown
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := switchoverRequestFixture()
			test.mutate(&request)
			checks, err := NewWithEndpointProvider(nil, passingEndpointProvider()).Precheck(context.Background(), request)
			if err != nil {
				t.Fatalf("precheck: %v", err)
			}
			if !failedCheck(checks, test.check) {
				t.Fatalf("missing blocking check %q: %+v", test.check, checks)
			}
		})
	}
}

func TestSwitchoverPrecheckReportsUnsupportedEndpointProvider(t *testing.T) {
	checks, err := New(nil).Precheck(context.Background(), switchoverRequestFixture())
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if !failedCheck(checks, "writer_endpoint_provider") {
		t.Fatalf("endpoint provider blocker is missing: %+v", checks)
	}
	if New(nil).Capabilities(context.Background()).Supports(adapter.CapabilityExecute) {
		t.Fatal("default adapter advertised executable switchover")
	}
}

func TestSwitchoverPlanRejectsNonPassingEndpointEvidence(t *testing.T) {
	tests := []struct {
		name   string
		checks []model.Check
	}{
		{name: "missing evidence"},
		{name: "warning evidence", checks: []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckWarn, Message: "endpoint ownership probe is incomplete"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := endpointProviderStub{executable: true, checks: test.checks}
			plan, err := NewWithEndpointProvider(nil, provider).BuildPlan(context.Background(), switchoverRequestFixture())
			if err != nil {
				t.Fatalf("build blocked plan: %v", err)
			}
			if !planHasBlockingChecks(plan.Checks) || !failedCheckNamed(plan.Checks, "writer_endpoint_provider") {
				t.Fatalf("switchover plan did not block endpoint evidence without an explicit pass: %+v", plan.Checks)
			}
		})
	}
}

func TestEndpointEvidenceSanitizationRedactsUnnamedProviderDetails(t *testing.T) {
	for _, input := range []model.Check{
		{Message: "vip-token=top-secret command=/sbin/ip"},
		{Name: "vip-token=top-secret-/sbin/ip", Status: model.CheckPass, Message: "ready"},
	} {
		check := sanitizeEndpointCheck(input, "writer_endpoint_owner")
		if strings.Contains(check.Name, "top-secret") || strings.Contains(check.Name, "/sbin/ip") ||
			strings.Contains(check.Message, "top-secret") || strings.Contains(check.Message, "/sbin/ip") ||
			check.Name != "writer_endpoint_owner" || check.Status != model.CheckFail {
			t.Fatalf("unsafe endpoint evidence %+v", check)
		}
	}
}

func TestSwitchoverPlanIsCanonicalAndImmutableByDigest(t *testing.T) {
	request := switchoverRequestFixture()
	adapterInstance := NewWithEndpointProvider(nil, passingEndpointProvider())
	plan, err := adapterInstance.BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if plan.OperationID != request.Operation.ResourceID || plan.SourceID != request.Resolved.Primary.ResourceID || plan.TargetID != request.TargetID {
		t.Fatalf("plan resource scope is wrong: %+v", plan)
	}
	if len(plan.Steps) != 12 || plan.Digest == "" || plan.ObservationToken == "" {
		t.Fatalf("plan is incomplete: %+v", plan)
	}
	if plan.ResourceRevisions[request.Resolved.Cluster.ResourceID] != request.Resolved.Cluster.MetadataRevision ||
		plan.ResourceRevisions[request.Resolved.Primary.ResourceID] != request.Resolved.Primary.MetadataRevision ||
		plan.ResourceRevisions[request.Resolved.Target.ResourceID] != request.Resolved.Target.MetadataRevision {
		t.Fatalf("plan revisions are incomplete: %+v", plan.ResourceRevisions)
	}
	recomputed, err := operationPlanDigest(plan)
	if err != nil || recomputed != plan.Digest {
		t.Fatalf("plan digest is not canonical: recomputed=%q err=%v plan=%+v", recomputed, err, plan)
	}

	changed := plan
	changed.TargetID = model.NewResourceID()
	changedDigest, err := operationPlanDigest(changed)
	if err != nil || changedDigest == plan.Digest {
		t.Fatalf("material plan change did not change digest: digest=%q err=%v", changedDigest, err)
	}
}
