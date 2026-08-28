package coordination

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type ownershipInventoryStub struct {
	clusters   []model.DatabaseCluster
	snapshots  map[model.ResourceID]model.TopologySnapshot
	resources  map[model.ResourceID][]model.HAEndpoint
	endpoints  map[model.ResourceID]model.Endpoint
	operations map[model.ResourceID][]model.OperationRecord
	commits    int
}

var errStaleOwnershipObservation = errors.New("stale ownership observation")

func (inventory *ownershipInventoryStub) Clusters() []model.DatabaseCluster {
	return append([]model.DatabaseCluster{}, inventory.clusters...)
}
func (inventory *ownershipInventoryStub) TopologySnapshot(clusterID model.ResourceID) (model.TopologySnapshot, bool) {
	snapshot, found := inventory.snapshots[clusterID]
	return snapshot, found
}
func (inventory *ownershipInventoryStub) HAEndpoints(clusterID model.ResourceID) []model.HAEndpoint {
	return append([]model.HAEndpoint{}, inventory.resources[clusterID]...)
}
func (inventory *ownershipInventoryStub) Endpoint(resourceID model.ResourceID) (model.Endpoint, bool) {
	value, found := inventory.endpoints[resourceID]
	return value, found
}
func (inventory *ownershipInventoryStub) Operations(clusterID model.ResourceID) []model.OperationRecord {
	return append([]model.OperationRecord{}, inventory.operations[clusterID]...)
}
func (inventory *ownershipInventoryStub) CommitHAEndpointOwner(clusterID, resourceID, ownerID model.ResourceID, healthy bool) error {
	resources := inventory.resources[clusterID]
	for index := range resources {
		if resources[index].ResourceID != resourceID {
			continue
		}
		resources[index].OwnerID = ownerID
		resources[index].Healthy = healthy
		resources[index].MetadataRevision++
		inventory.resources[clusterID] = resources
		endpointValue := inventory.endpoints[resources[index].EndpointID]
		endpointValue.InstanceID = ownerID
		endpointValue.MetadataRevision++
		inventory.endpoints[resources[index].EndpointID] = endpointValue
		inventory.commits++
		return nil
	}
	return errors.New("resource missing")
}

func (inventory *ownershipInventoryStub) CommitObservedHAEndpointOwner(clusterID, resourceID model.ResourceID, expectedResourceRevision, expectedEndpointRevision uint64, ownerID model.ResourceID, healthy bool) error {
	resources := inventory.resources[clusterID]
	for index := range resources {
		if resources[index].ResourceID != resourceID {
			continue
		}
		endpointValue := inventory.endpoints[resources[index].EndpointID]
		if resources[index].MetadataRevision != expectedResourceRevision || endpointValue.MetadataRevision != expectedEndpointRevision {
			return errStaleOwnershipObservation
		}
		return inventory.CommitHAEndpointOwner(clusterID, resourceID, ownerID, healthy)
	}
	return errors.New("resource missing")
}

type ownershipObserverStub struct {
	result       endpoint.OwnershipObservation
	err          error
	calls        int
	beforeReturn func()
}

type blockingOwnershipObserver struct{}

func (blockingOwnershipObserver) ObserveOwnership(ctx context.Context, _ model.DatabaseCluster, _ model.TopologySnapshot) (endpoint.OwnershipObservation, error) {
	<-ctx.Done()
	return endpoint.OwnershipObservation{}, ctx.Err()
}

type gatedOwnershipObserver struct {
	started chan model.ResourceID
	release <-chan struct{}
}

func (observer gatedOwnershipObserver) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, _ model.TopologySnapshot) (endpoint.OwnershipObservation, error) {
	select {
	case observer.started <- cluster.ResourceID:
	case <-ctx.Done():
		return endpoint.OwnershipObservation{}, ctx.Err()
	}
	select {
	case <-observer.release:
		return endpoint.OwnershipObservation{}, errors.New("probe released")
	case <-ctx.Done():
		return endpoint.OwnershipObservation{}, ctx.Err()
	}
}

func (observer *ownershipObserverStub) ObserveOwnership(context.Context, model.DatabaseCluster, model.TopologySnapshot) (endpoint.OwnershipObservation, error) {
	observer.calls++
	if observer.beforeReturn != nil {
		observer.beforeReturn()
	}
	return observer.result, observer.err
}

type ownershipLeaseStub struct {
	requests []endpoint.LeaseRequest
	err      error
	batches  int
}

type ownershipAuthorityStub struct{ err error }

func (authority ownershipAuthorityStub) RequireMutationAuthority(context.Context) error {
	return authority.err
}

func (store *ownershipLeaseStub) AcquireStableBatch(_ context.Context, requests []endpoint.LeaseRequest) error {
	store.batches++
	store.requests = append(store.requests, requests...)
	return store.err
}

func ownershipKeeperFixture(now time.Time) (*ownershipInventoryStub, *ownershipObserverStub, *ownershipLeaseStub, model.DatabaseInstance, model.HAEndpoint) {
	clusterID := model.NewResourceID()
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: clusterID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		Role:           model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, EngineMetadata: map[string]string{"read_only": "false", "super_read_only": "false"},
	}
	resource := model.HAEndpoint{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1}, ClusterID: clusterID, EndpointID: model.NewResourceID(), Kind: model.EndpointVIP, OwnerID: primary.ResourceID, Healthy: true}
	inventory := &ownershipInventoryStub{
		clusters: []model.DatabaseCluster{{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL}},
		snapshots: map[model.ResourceID]model.TopologySnapshot{clusterID: {
			ClusterID: clusterID, ObservedAt: now, Instances: []model.DatabaseInstance{primary},
			Probes: []model.ProbeStatus{{InstanceID: primary.ResourceID, DiscoveryObservedAt: now, Health: primary.Health}},
		}},
		resources: map[model.ResourceID][]model.HAEndpoint{clusterID: {resource}},
		endpoints: map[model.ResourceID]model.Endpoint{resource.EndpointID: {ResourceMeta: model.ResourceMeta{ResourceID: resource.EndpointID, MetadataRevision: 1}, ClusterID: clusterID, InstanceID: primary.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100", Active: true}},
	}
	observer := &ownershipObserverStub{result: endpoint.OwnershipObservation{
		HAEndpointID: resource.ResourceID, HAEndpointRevision: 1, EndpointRevision: 1,
		CanonicalOwnerID: primary.ResourceID, EndpointOwnerID: primary.ResourceID,
		OwnerIDs: []model.ResourceID{primary.ResourceID}, Complete: true,
	}}
	return inventory, observer, &ownershipLeaseStub{}, primary, resource
}

func TestWritablePrimaryAcceptsHealthyPostgreSQLWriter(t *testing.T) {
	now := time.Date(2026, time.July, 21, 6, 30, 0, 0, time.UTC)
	primary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		Engine:       model.EnginePostgreSQL,
		Role:         model.RolePrimary,
		Health:       model.Health{State: model.HealthHealthy},
		EngineMetadata: map[string]string{
			"in_recovery":           "false",
			"transaction_read_only": "false",
		},
	}
	snapshot := model.TopologySnapshot{
		ObservedAt: now,
		Instances:  []model.DatabaseInstance{primary},
		Probes:     []model.ProbeStatus{{InstanceID: primary.ResourceID, DiscoveryObservedAt: now, Health: primary.Health}},
	}
	selected, err := writablePrimary(snapshot)
	if err != nil || selected.ResourceID != primary.ResourceID {
		t.Fatalf("healthy PostgreSQL writer was rejected: selected=%+v err=%v", selected, err)
	}

	primary.EngineMetadata["transaction_read_only"] = "true"
	snapshot.Instances[0] = primary
	if selected, err := writablePrimary(snapshot); err == nil || selected.ResourceID != "" {
		t.Fatalf("read-only PostgreSQL primary was accepted: selected=%+v err=%v", selected, err)
	}
}

func TestOwnershipKeeperDefersToActivePostgreSQLEndpointMutation(t *testing.T) {
	now := time.Date(2026, time.August, 27, 7, 30, 0, 0, time.UTC)
	inventory, observer, leases, primary, _ := ownershipKeeperFixture(now)
	clusterID := inventory.clusters[0].ResourceID
	inventory.clusters[0].Engine = model.EnginePostgreSQL
	primary.Engine = model.EnginePostgreSQL
	primary.EngineMetadata = map[string]string{"in_recovery": "false", "transaction_read_only": "false"}
	inventory.snapshots[clusterID] = model.TopologySnapshot{
		ClusterID: clusterID, ObservedAt: now, Instances: []model.DatabaseInstance{primary},
		Probes: []model.ProbeStatus{{InstanceID: primary.ResourceID, DiscoveryObservedAt: now, Health: primary.Health}},
	}
	inventory.operations = map[model.ResourceID][]model.OperationRecord{clusterID: {{
		Operation: model.Operation{ClusterID: clusterID, Engine: model.EnginePostgreSQL, Kind: model.OperationFormerPrimaryRejoin},
		Status:    model.OperationRunning,
	}}}

	keeper := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("active PostgreSQL endpoint operation should defer background ownership reconciliation: %v", err)
	}
	if observer.calls != 0 || leases.batches != 0 {
		t.Fatalf("background ownership raced active PostgreSQL operation: observer_calls=%d lease_batches=%d", observer.calls, leases.batches)
	}

	record := inventory.operations[clusterID][0]
	record.Status = model.OperationSucceeded
	inventory.operations[clusterID][0] = record
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("ownership reconciliation did not resume after operation completion: %v", err)
	}
	if observer.calls != 1 || leases.batches != 1 {
		t.Fatalf("ownership reconciliation did not resume: observer_calls=%d lease_batches=%d", observer.calls, leases.batches)
	}
}

func TestWritablePrimaryAllowsCurrentFailedProbeForFormerPrimary(t *testing.T) {
	now := time.Date(2026, time.July, 21, 6, 35, 0, 0, time.UTC)
	current := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		Engine:       model.EnginePostgreSQL,
		Role:         model.RolePrimary,
		Health:       model.Health{State: model.HealthHealthy, ObservedAt: now},
		EngineMetadata: map[string]string{
			"in_recovery":           "false",
			"transaction_read_only": "false",
		},
	}
	former := current
	former.ResourceID = model.NewResourceID()
	former.Health = model.Health{State: model.HealthUnknown, Summary: "database probe failed", ObservedAt: now}
	snapshot := model.TopologySnapshot{
		ObservedAt: now,
		Instances:  []model.DatabaseInstance{former, current},
		Probes: []model.ProbeStatus{
			{InstanceID: former.ResourceID, Health: former.Health},
			{InstanceID: current.ResourceID, DiscoveryObservedAt: now, Health: current.Health},
		},
	}

	selected, err := writablePrimary(snapshot)
	if err != nil || selected.ResourceID != current.ResourceID {
		t.Fatalf("verified current writer was rejected beside unreachable former primary: selected=%+v err=%v", selected, err)
	}
}

func TestWritablePrimaryRejectsAmbiguousFormerPrimaryEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 21, 6, 40, 0, 0, time.UTC)
	writer := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		Engine:       model.EnginePostgreSQL,
		Role:         model.RolePrimary,
		Health:       model.Health{State: model.HealthHealthy, ObservedAt: now},
		EngineMetadata: map[string]string{
			"in_recovery":           "false",
			"transaction_read_only": "false",
		},
	}
	former := writer
	former.ResourceID = model.NewResourceID()
	former.Health = model.Health{State: model.HealthUnknown, Summary: "database probe failed", ObservedAt: now}
	base := model.TopologySnapshot{
		ObservedAt: now,
		Instances:  []model.DatabaseInstance{former, writer},
		Probes: []model.ProbeStatus{
			{InstanceID: former.ResourceID, Health: former.Health},
			{InstanceID: writer.ResourceID, DiscoveryObservedAt: now, Health: writer.Health},
		},
	}

	tests := []struct {
		name   string
		mutate func(*model.TopologySnapshot)
	}{
		{name: "former primary has no bound probe", mutate: func(snapshot *model.TopologySnapshot) {
			snapshot.Probes = snapshot.Probes[1:]
		}},
		{name: "former primary failure is stale", mutate: func(snapshot *model.TopologySnapshot) {
			snapshot.Probes[0].Health.ObservedAt = now.Add(-time.Second)
		}},
		{name: "former primary remains reachable", mutate: func(snapshot *model.TopologySnapshot) {
			snapshot.Instances[0].Health = model.Health{State: model.HealthHealthy, ObservedAt: now}
			snapshot.Probes[0] = model.ProbeStatus{InstanceID: former.ResourceID, DiscoveryObservedAt: now, Health: snapshot.Instances[0].Health}
		}},
		{name: "writer lacks current successful probe", mutate: func(snapshot *model.TopologySnapshot) {
			snapshot.Probes[1].DiscoveryObservedAt = time.Time{}
			snapshot.Probes[1].Health = model.Health{State: model.HealthUnknown, ObservedAt: now}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.Instances = append([]model.DatabaseInstance{}, base.Instances...)
			snapshot.Probes = append([]model.ProbeStatus{}, base.Probes...)
			test.mutate(&snapshot)
			if selected, err := writablePrimary(snapshot); err == nil || selected.ResourceID != "" {
				t.Fatalf("ambiguous topology selected=%+v err=%v", selected, err)
			}
		})
	}
}

func rebootBootstrapFixture(now time.Time) (*ownershipInventoryStub, *ownershipObserverStub, *ownershipLeaseStub, model.DatabaseInstance, model.DatabaseInstance) {
	inventory, observer, leases, canonical, _ := ownershipKeeperFixture(now)
	lag := int64(0)
	canonical.Role = model.RoleUnknown
	canonical.Health = model.Health{State: model.HealthDegraded, Summary: "MySQL instance is read-only with no replication source"}
	canonical.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
	replica := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: canonical.ClusterID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"},
		Role:           model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			SourceIdentity: canonical.EngineIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
		},
		EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
	}
	inventory.snapshots[canonical.ClusterID] = model.TopologySnapshot{
		ClusterID: canonical.ClusterID, ObservedAt: now, Instances: []model.DatabaseInstance{canonical, replica},
		Links: []model.ReplicationLink{{
			ClusterID: canonical.ClusterID, SourceInstanceID: canonical.ResourceID, TargetInstanceID: replica.ResourceID, Healthy: false, LagSeconds: &lag,
		}},
		Probes: []model.ProbeStatus{
			{InstanceID: canonical.ResourceID, DiscoveryObservedAt: now, Health: canonical.Health},
			{InstanceID: replica.ResourceID, DiscoveryObservedAt: now, Health: replica.Health},
		},
		Health: model.Health{State: model.HealthDegraded},
	}
	observer.result.OwnerIDs = nil
	return inventory, observer, leases, canonical, replica
}

func TestRebootBootstrapCandidateRequiresCompleteReadOnlyCanonicalTopology(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 30, 0, 0, time.UTC)
	inventory, _, _, canonical, replica := rebootBootstrapFixture(now)
	snapshot := inventory.snapshots[canonical.ClusterID]
	candidate, err := RebootBootstrapCandidate(snapshot, canonical.ResourceID, now, 15*time.Second)
	if err != nil || candidate.ResourceID != canonical.ResourceID {
		t.Fatalf("strict reboot candidate=%+v err=%v", candidate, err)
	}

	unsafe := []struct {
		name   string
		mutate func(*model.TopologySnapshot)
	}{
		{name: "stale topology", mutate: func(value *model.TopologySnapshot) { value.ObservedAt = now.Add(-time.Minute) }},
		{name: "canonical partially writable", mutate: func(value *model.TopologySnapshot) { value.Instances[0].EngineMetadata["read_only"] = "false" }},
		{name: "replica source mismatch", mutate: func(value *model.TopologySnapshot) {
			value.Instances[1].Replication.SourceIdentity["server_uuid"] = "cccccccc-dddd-eeee-ffff-aaaaaaaaaaaa"
		}},
		{name: "replica lag", mutate: func(value *model.TopologySnapshot) {
			lag := int64(1)
			value.Instances[1].Replication.LagSeconds = &lag
			value.Links[0].LagSeconds = &lag
		}},
		{name: "replica thread stopped", mutate: func(value *model.TopologySnapshot) { value.Instances[1].Replication.SQLThread = model.ThreadStopped }},
		{name: "unrelated replica degradation", mutate: func(value *model.TopologySnapshot) {
			value.Instances[1].Health = model.Health{State: model.HealthDegraded, Summary: "replica health is unsafe"}
			value.Probes[1].Health = value.Instances[1].Health
		}},
		{name: "incomplete probe coverage", mutate: func(value *model.TopologySnapshot) { value.Probes = value.Probes[:1] }},
		{name: "wrong canonical owner", mutate: func(value *model.TopologySnapshot) { value.Instances[0].ResourceID = model.NewResourceID() }},
		{name: "unexpected replication edge", mutate: func(value *model.TopologySnapshot) { value.Links[0].SourceInstanceID = replica.ResourceID }},
	}
	for _, test := range unsafe {
		t.Run(test.name, func(t *testing.T) {
			value := inventory.snapshots[canonical.ClusterID]
			value.Instances = append([]model.DatabaseInstance{}, value.Instances...)
			value.Links = append([]model.ReplicationLink{}, value.Links...)
			value.Probes = append([]model.ProbeStatus{}, value.Probes...)
			for index := range value.Instances {
				value.Instances[index].EngineMetadata = cloneStringMap(value.Instances[index].EngineMetadata)
				value.Instances[index].EngineIdentity = value.Instances[index].EngineIdentity.Clone()
				value.Instances[index].Replication.SourceIdentity = value.Instances[index].Replication.SourceIdentity.Clone()
			}
			test.mutate(&value)
			if candidate, err := RebootBootstrapCandidate(value, canonical.ResourceID, now, 15*time.Second); err == nil || candidate.ResourceID != "" {
				t.Fatalf("unsafe candidate=%+v err=%v", candidate, err)
			}
		})
	}
}

func TestRebootBootstrapCandidateAllowsCurrentFailedProbeForKnownReadOnlyReplica(t *testing.T) {
	now := time.Date(2026, time.August, 11, 3, 15, 41, 0, time.UTC)
	inventory, _, _, canonical, _ := rebootBootstrapFixture(now)
	snapshot := inventory.snapshots[canonical.ClusterID]
	lag := int64(0)
	offline := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: canonical.ClusterID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "dddddddd-eeee-ffff-aaaa-bbbbbbbbbbbb"},
		Role:           model.RoleReplica,
		Health:         model.Health{State: model.HealthUnknown, Summary: "database probe failed", ObservedAt: now},
		Replication: model.ReplicationStatus{
			SourceIdentity: canonical.EngineIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
		},
		EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
	}
	snapshot.Instances = append(snapshot.Instances, offline)
	snapshot.Probes = append(snapshot.Probes, model.ProbeStatus{
		InstanceID: offline.ResourceID,
		Health:     offline.Health,
	})
	snapshot.Links = append(snapshot.Links, model.ReplicationLink{
		ClusterID: canonical.ClusterID, SourceInstanceID: canonical.ResourceID, TargetInstanceID: offline.ResourceID,
		Healthy: false, LagSeconds: &lag,
	})

	candidate, err := RebootBootstrapCandidate(snapshot, canonical.ResourceID, now, 15*time.Second)
	if err != nil || candidate.ResourceID != canonical.ResourceID {
		t.Fatalf("known read-only offline replica blocked reboot candidate=%+v err=%v", candidate, err)
	}

	unsafeWritable := snapshot
	unsafeWritable.Instances = append([]model.DatabaseInstance{}, snapshot.Instances...)
	unsafeWritable.Instances[2].EngineMetadata = cloneStringMap(snapshot.Instances[2].EngineMetadata)
	unsafeWritable.Instances[2].EngineMetadata["read_only"] = "false"
	if candidate, err := RebootBootstrapCandidate(unsafeWritable, canonical.ResourceID, now, 15*time.Second); err == nil || candidate.ResourceID != "" {
		t.Fatalf("last-known writable offline replica accepted candidate=%+v err=%v", candidate, err)
	}

	unsafePrimary := snapshot
	unsafePrimary.Instances = append([]model.DatabaseInstance{}, snapshot.Instances...)
	unsafePrimary.Instances[2].Role = model.RolePrimary
	if candidate, err := RebootBootstrapCandidate(unsafePrimary, canonical.ResourceID, now, 15*time.Second); err == nil || candidate.ResourceID != "" {
		t.Fatalf("offline primary accepted candidate=%+v err=%v", candidate, err)
	}
}

func TestRebootBootstrapCandidateAcceptsSemisyncIdleReplicaWithMatchingGTID(t *testing.T) {
	now := time.Date(2026, time.August, 9, 9, 53, 0, 0, time.UTC)
	inventory, _, _, canonical, _ := rebootBootstrapFixture(now)
	snapshot := inventory.snapshots[canonical.ClusterID]
	gtid := "83baf934-7556-11f1-ae21-000c29938c48:1-97997"
	snapshot.Instances[0].EngineMetadata["gtid_executed"] = gtid
	snapshot.Instances[0].EngineMetadata["semi_sync_required"] = "true"
	snapshot.Instances[0].EngineMetadata["semi_sync_available"] = "true"
	snapshot.Instances[0].EngineMetadata["semi_sync_source_enabled"] = "true"
	snapshot.Instances[0].EngineMetadata["semi_sync_source_status"] = "true"
	snapshot.Instances[0].EngineMetadata["semi_sync_source_clients"] = "1"
	snapshot.Instances[0].EngineMetadata["semi_sync_wait_for_replica_count"] = "1"
	replica := &snapshot.Instances[1]
	replica.Health = model.Health{
		State:   model.HealthDegraded,
		Summary: "MySQL replica is not actively acknowledging semi-sync transactions",
	}
	replica.Replication.ExecutedPosition = gtid
	replica.EngineMetadata["gtid_executed"] = gtid
	replica.EngineMetadata["semi_sync_required"] = "true"
	replica.EngineMetadata["semi_sync_available"] = "true"
	replica.EngineMetadata["semi_sync_source_enabled"] = "true"
	replica.EngineMetadata["semi_sync_replica_enabled"] = "true"
	replica.EngineMetadata["semi_sync_replica_status"] = "false"
	snapshot.Probes[1].Health = replica.Health

	candidate, err := RebootBootstrapCandidate(snapshot, canonical.ResourceID, now, 15*time.Second)
	if err != nil || candidate.ResourceID != canonical.ResourceID {
		t.Fatalf("semi-sync idle replica blocked safe reboot candidate=%+v err=%v", candidate, err)
	}

	snapshot.Instances[1].Replication.ExecutedPosition = gtid + ",aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1"
	if candidate, err := RebootBootstrapCandidate(snapshot, canonical.ResourceID, now, 15*time.Second); err == nil || candidate.ResourceID != "" {
		t.Fatalf("GTID-divergent degraded replica accepted candidate=%+v err=%v", candidate, err)
	}
}

func cloneStringMap(value map[string]string) map[string]string {
	clone := make(map[string]string, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

func TestOwnershipKeeperGrantsBootstrapLeaseOnlyToRebootedCanonicalPrimary(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 45, 0, 0, time.UTC)
	inventory, observer, leases, canonical, _ := rebootBootstrapFixture(now)
	keeper := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("reboot bootstrap lease: %v", err)
	}
	if len(leases.requests) != 1 || leases.requests[0].OwnerID != canonical.ResourceID || inventory.commits != 1 {
		t.Fatalf("bootstrap leases=%+v commits=%d", leases.requests, inventory.commits)
	}
	if inventory.resources[canonical.ClusterID][0].Healthy {
		t.Fatal("zero-owner bootstrap was incorrectly marked healthy before agent verification")
	}
}

func TestOwnershipKeeperRenewsExistingBootstrapLeaseDuringDiscoveryConvergence(t *testing.T) {
	now := time.Date(2026, time.July, 28, 8, 30, 0, 0, time.UTC)
	inventory, observer, leases, canonical, _ := rebootBootstrapFixture(now)
	observer.result.OwnerIDs = []model.ResourceID{canonical.ResourceID}
	if err := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background()); err != nil {
		t.Fatalf("post-bootstrap convergence renewal: %v", err)
	}
	if len(leases.requests) != 1 || leases.requests[0].OwnerID != canonical.ResourceID || !leases.requests[0].RenewOnly {
		t.Fatalf("post-bootstrap convergence requests=%+v", leases.requests)
	}
	if inventory.commits != 0 {
		t.Fatalf("post-bootstrap convergence changed metadata commits=%d", inventory.commits)
	}
}

func TestOwnershipKeeperBlocksUnsafeBootstrapConvergenceRenewal(t *testing.T) {
	now := time.Date(2026, time.July, 28, 8, 35, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*ownershipInventoryStub, *ownershipObserverStub, model.DatabaseInstance)
	}{
		{
			name: "owner is not canonical",
			mutate: func(_ *ownershipInventoryStub, observer *ownershipObserverStub, _ model.DatabaseInstance) {
				observer.result.OwnerIDs = []model.ResourceID{model.NewResourceID()}
			},
		},
		{
			name: "multiple owners",
			mutate: func(_ *ownershipInventoryStub, observer *ownershipObserverStub, canonical model.DatabaseInstance) {
				observer.result.OwnerIDs = []model.ResourceID{canonical.ResourceID, model.NewResourceID()}
			},
		},
		{
			name: "replica lag",
			mutate: func(inventory *ownershipInventoryStub, observer *ownershipObserverStub, canonical model.DatabaseInstance) {
				observer.result.OwnerIDs = []model.ResourceID{canonical.ResourceID}
				snapshot := inventory.snapshots[canonical.ClusterID]
				lag := int64(1)
				snapshot.Instances[1].Replication.LagSeconds = &lag
				snapshot.Links[0].LagSeconds = &lag
				inventory.snapshots[canonical.ClusterID] = snapshot
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory, observer, leases, canonical, _ := rebootBootstrapFixture(now)
			test.mutate(inventory, observer, canonical)
			err := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background())
			if err == nil {
				t.Fatal("unsafe post-bootstrap convergence renewal was accepted")
			}
			if len(leases.requests) != 0 || inventory.commits != 0 {
				t.Fatalf("unsafe convergence requests=%+v commits=%d", leases.requests, inventory.commits)
			}
		})
	}
}

func TestOwnershipKeeperRenewsOnlyHealthyWritableCurrentPrimary(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 0, 0, 0, time.UTC)
	inventory, observer, leases, primary, resource := ownershipKeeperFixture(now)
	keeper := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("renew ownership: %v", err)
	}
	if len(leases.requests) != 1 {
		t.Fatalf("lease requests=%+v", leases.requests)
	}
	if leases.batches != 1 {
		t.Fatalf("stable leases used %d batches, want one", leases.batches)
	}
	request := leases.requests[0]
	if request.OwnerID != primary.ResourceID || request.HAEndpointID != resource.ResourceID || request.OperationID != resource.ResourceID || request.TTL != time.Minute {
		t.Fatalf("stable ownership lease=%+v", request)
	}
}

func addOwnershipReplica(inventory *ownershipInventoryStub, primary model.DatabaseInstance, now time.Time, suffix string) model.DatabaseInstance {
	lag := int64(0)
	replica := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: primary.ClusterID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": suffix + "-bbbb-cccc-dddd-eeeeeeeeeeee"},
		Role:           model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			SourceIdentity: primary.EngineIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag,
		},
		EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
	}
	snapshot := inventory.snapshots[primary.ClusterID]
	snapshot.Instances = append(snapshot.Instances, replica)
	snapshot.Probes = append(snapshot.Probes, model.ProbeStatus{InstanceID: replica.ResourceID, DiscoveryObservedAt: now, Health: replica.Health})
	inventory.snapshots[primary.ClusterID] = snapshot
	return replica
}

func TestOwnershipKeeperRenewsExistingLeaseWithMajorityCoverageWhenMissingNodeIsNotOwner(t *testing.T) {
	now := time.Date(2026, time.July, 28, 7, 10, 0, 0, time.UTC)
	inventory, observer, leases, primary, _ := ownershipKeeperFixture(now)
	firstReplica := addOwnershipReplica(inventory, primary, now, "bbbbbbbb")
	_ = addOwnershipReplica(inventory, primary, now, "cccccccc")
	observer.result.Complete = false
	observer.result.ObservedInstanceIDs = []model.ResourceID{primary.ResourceID, firstReplica.ResourceID}

	keeper := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("majority ownership renewal: %v", err)
	}
	if len(leases.requests) != 1 || !leases.requests[0].RenewOnly {
		t.Fatalf("partial coverage requests=%+v", leases.requests)
	}
}

func TestOwnershipKeeperRejectsUnsafePartialCoverage(t *testing.T) {
	now := time.Date(2026, time.July, 28, 7, 15, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*ownershipObserverStub, model.DatabaseInstance, model.DatabaseInstance, model.DatabaseInstance)
	}{
		{
			name: "owner probe is missing",
			mutate: func(observer *ownershipObserverStub, primary, first, second model.DatabaseInstance) {
				observer.result.ObservedInstanceIDs = []model.ResourceID{first.ResourceID, second.ResourceID}
				observer.result.OwnerIDs = []model.ResourceID{primary.ResourceID}
			},
		},
		{
			name: "data node probe coverage is not a majority",
			mutate: func(observer *ownershipObserverStub, primary, _, _ model.DatabaseInstance) {
				observer.result.ObservedInstanceIDs = []model.ResourceID{primary.ResourceID}
			},
		},
		{
			name: "incomplete zero owner evidence cannot bootstrap",
			mutate: func(observer *ownershipObserverStub, primary, first, _ model.DatabaseInstance) {
				observer.result.ObservedInstanceIDs = []model.ResourceID{primary.ResourceID, first.ResourceID}
				observer.result.OwnerIDs = nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory, observer, leases, primary, _ := ownershipKeeperFixture(now)
			firstReplica := addOwnershipReplica(inventory, primary, now, "bbbbbbbb")
			secondReplica := addOwnershipReplica(inventory, primary, now, "cccccccc")
			observer.result.Complete = false
			test.mutate(observer, primary, firstReplica, secondReplica)

			err := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background())
			if err == nil {
				t.Fatal("unsafe partial ownership coverage was accepted")
			}
			if len(leases.requests) != 0 {
				t.Fatalf("unsafe partial coverage requests=%+v", leases.requests)
			}
		})
	}
}

func TestOwnershipKeeperAllowsZeroOwnerBootstrapOnlyForCanonicalPrimary(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 0, 0, 0, time.UTC)
	inventory, observer, leases, _, _ := ownershipKeeperFixture(now)
	observer.result.OwnerIDs = nil
	if err := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background()); err != nil {
		t.Fatalf("zero-owner bootstrap: %v", err)
	}
	if len(leases.requests) != 1 || inventory.commits != 1 || inventory.resources[inventory.clusters[0].ResourceID][0].Healthy {
		t.Fatalf("zero-owner state leases=%+v inventory=%+v", leases.requests, inventory.resources)
	}
}

func TestOwnershipKeeperCannotOverwriteNewerSwitchoverWithStaleObservation(t *testing.T) {
	now := time.Date(2026, time.July, 15, 1, 30, 0, 0, time.UTC)
	inventory, observer, leases, oldPrimary, resource := ownershipKeeperFixture(now)
	lag := int64(0)
	newPrimary := model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: oldPrimary.ClusterID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"},
		Role:           model.RoleReplica, Health: model.Health{State: model.HealthHealthy},
		Replication:    model.ReplicationStatus{SourceIdentity: oldPrimary.EngineIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag},
		EngineMetadata: map[string]string{"read_only": "true", "super_read_only": "true"},
	}
	snapshot := inventory.snapshots[oldPrimary.ClusterID]
	snapshot.Instances = append(snapshot.Instances, newPrimary)
	inventory.snapshots[oldPrimary.ClusterID] = snapshot

	// The keeper observed the temporary zero-owner window while the old topology
	// was still current. Before it can publish that observation, the real
	// switchover commits the new verified owner.
	observer.result.OwnerIDs = nil
	observer.beforeReturn = func() {
		if err := inventory.CommitHAEndpointOwner(oldPrimary.ClusterID, resource.ResourceID, newPrimary.ResourceID, true); err != nil {
			t.Fatalf("commit concurrent switchover owner: %v", err)
		}
	}

	err := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background())
	if !errors.Is(err, errStaleOwnershipObservation) {
		t.Fatalf("stale ownership keeper error=%v", err)
	}
	updated := inventory.resources[oldPrimary.ClusterID][0]
	updatedEndpoint := inventory.endpoints[updated.EndpointID]
	if updated.OwnerID != newPrimary.ResourceID || !updated.Healthy || updatedEndpoint.InstanceID != newPrimary.ResourceID {
		t.Fatalf("stale keeper replaced switchover owner: resource=%+v endpoint=%+v", updated, updatedEndpoint)
	}
	if len(leases.requests) != 0 {
		t.Fatalf("stale observation renewed ownership lease: %+v", leases.requests)
	}
}

func TestOwnershipKeeperStopsRenewalForOldOwnerStaleTopologyOrMinority(t *testing.T) {
	now := time.Date(2026, time.July, 13, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*ownershipInventoryStub, *ownershipObserverStub)
		auth   error
	}{
		{name: "old owner", mutate: func(_ *ownershipInventoryStub, observer *ownershipObserverStub) {
			observer.result.OwnerIDs = []model.ResourceID{model.NewResourceID()}
		}},
		{name: "stale topology", mutate: func(inventory *ownershipInventoryStub, _ *ownershipObserverStub) {
			clusterID := inventory.clusters[0].ResourceID
			snapshot := inventory.snapshots[clusterID]
			snapshot.ObservedAt = now.Add(-time.Minute)
			inventory.snapshots[clusterID] = snapshot
		}},
		{name: "minority", auth: errors.New("no quorum")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory, observer, leases, _, _ := ownershipKeeperFixture(now)
			if test.mutate != nil {
				test.mutate(inventory, observer)
			}
			keeper := NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{err: test.auth}, func() time.Time { return now }, 5*time.Second, 15*time.Second)
			_ = keeper.RunOnce(context.Background())
			if len(leases.requests) != 0 {
				t.Fatalf("unsafe lease requests=%+v", leases.requests)
			}
		})
	}
}

func TestOwnershipKeeperBoundsEachClusterProbe(t *testing.T) {
	now := time.Now().UTC()
	inventory, _, leases, _, _ := ownershipKeeperFixture(now)
	keeper := NewOwnershipKeeper(
		inventory, blockingOwnershipObserver{}, leases, ownershipAuthorityStub{}, func() time.Time { return now },
		5*time.Second, 15*time.Second, WithOwnershipProbeTimeout(20*time.Millisecond),
	)
	started := time.Now()
	if err := keeper.RunOnce(context.Background()); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded ownership probe err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ownership probe exceeded timeout: %s", elapsed)
	}
}

func TestOwnershipKeeperProbesClustersConcurrently(t *testing.T) {
	now := time.Now().UTC()
	first, _, leases, _, _ := ownershipKeeperFixture(now)
	second, _, _, _, _ := ownershipKeeperFixture(now)
	secondCluster := second.clusters[0]
	first.clusters = append(first.clusters, secondCluster)
	first.snapshots[secondCluster.ResourceID] = second.snapshots[secondCluster.ResourceID]

	started := make(chan model.ResourceID, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	keeper := NewOwnershipKeeper(
		first, gatedOwnershipObserver{started: started, release: release}, leases, ownershipAuthorityStub{}, func() time.Time { return now },
		5*time.Second, 15*time.Second, WithOwnershipProbeTimeout(2*time.Second),
	)
	done := make(chan error, 1)
	go func() { done <- keeper.RunOnce(context.Background()) }()

	seen := make(map[model.ResourceID]bool, 2)
	for len(seen) < 2 {
		select {
		case clusterID := <-started:
			seen[clusterID] = true
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("cluster probes did not overlap; started=%v", seen)
		}
	}
	unblock()
	if err := <-done; err == nil {
		t.Fatal("released probes unexpectedly succeeded")
	}
}

func TestOwnershipKeeperSkipsEnginesWithoutAgentOwnershipProtocol(t *testing.T) {
	now := time.Now().UTC()
	inventory, observer, leases, _, _ := ownershipKeeperFixture(now)
	inventory.clusters = append(inventory.clusters,
		model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineOracle},
		model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Engine: model.EngineSQLServer},
	)

	keeper := NewOwnershipKeeper(
		inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now },
		5*time.Second, 15*time.Second,
	)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("ownership keeper should ignore engines without this ownership protocol: %v", err)
	}
	if observer.calls != 1 || len(leases.requests) != 1 {
		t.Fatalf("observer calls=%d leases=%+v", observer.calls, leases.requests)
	}
}

func TestOwnershipKeeperRunReportsBackgroundFailures(t *testing.T) {
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	keeper := NewOwnershipKeeper(nil, nil, nil, nil, nil, time.Hour, time.Minute,
		WithOwnershipErrorHandler(func(err error) {
			reported <- err
			cancel()
		}),
	)
	done := make(chan struct{})
	go func() {
		keeper.Run(ctx)
		close(done)
	}()
	select {
	case err := <-reported:
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("reported ownership error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ownership keeper swallowed its background failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ownership keeper did not stop after cancellation")
	}
}

func TestOwnershipKeeperRateLimitsRepeatedBackgroundFailures(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	reported := make([]string, 0, 3)
	keeper := NewOwnershipKeeper(nil, nil, nil, nil, func() time.Time { return now }, time.Second, time.Minute,
		WithOwnershipErrorHandler(func(err error) { reported = append(reported, err.Error()) }),
	)

	keeper.reportError(errors.New("ownership blocked"))
	keeper.reportError(errors.New("ownership blocked"))
	if len(reported) != 1 {
		t.Fatalf("duplicate ownership errors reported=%v", reported)
	}
	now = now.Add(5 * time.Minute)
	keeper.reportError(errors.New("ownership blocked"))
	if len(reported) != 2 {
		t.Fatalf("ownership reminder missing=%v", reported)
	}
	keeper.reportError(nil)
	keeper.reportError(errors.New("ownership blocked"))
	if len(reported) != 3 {
		t.Fatalf("ownership recovery did not reset suppression=%v", reported)
	}
}
