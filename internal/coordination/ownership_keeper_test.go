package coordination

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type ownershipInventoryStub struct {
	clusters  []model.DatabaseCluster
	snapshots map[model.ResourceID]model.TopologySnapshot
	resources map[model.ResourceID][]model.HAEndpoint
	endpoints map[model.ResourceID]model.Endpoint
	commits   int
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
		clusters:  []model.DatabaseCluster{{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL}},
		snapshots: map[model.ResourceID]model.TopologySnapshot{clusterID: {ClusterID: clusterID, ObservedAt: now, Instances: []model.DatabaseInstance{primary}}},
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

	inventory, observer, leases, _, _ = rebootBootstrapFixture(now)
	observer.result.OwnerIDs = []model.ResourceID{canonical.ResourceID}
	_ = NewOwnershipKeeper(inventory, observer, leases, ownershipAuthorityStub{}, func() time.Time { return now }, 5*time.Second, 15*time.Second).RunOnce(context.Background())
	if len(leases.requests) != 0 {
		t.Fatalf("non-zero-owner reboot bootstrap leases=%+v", leases.requests)
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
	if request.OwnerID != primary.ResourceID || request.HAEndpointID != resource.ResourceID || request.OperationID != resource.ResourceID || request.TTL != 30*time.Second {
		t.Fatalf("stable ownership lease=%+v", request)
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
