package coordination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mysqladapter "clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/internal/endpoint"
	pkgadapter "clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type recordingOwnershipLeaseStore struct {
	store    *LeaseStore
	requests []endpoint.LeaseRequest
}

func (store *recordingOwnershipLeaseStore) AcquireStableBatch(ctx context.Context, requests []endpoint.LeaseRequest) error {
	store.requests = append(store.requests, requests...)
	return store.store.AcquireStableBatch(ctx, requests)
}

type replicaFailureOwnershipHarness struct {
	now        time.Time
	inventory  *ownershipInventoryStub
	observer   *ownershipObserverStub
	records    *leaseRecordStore
	membership *Membership
	leases     *recordingOwnershipLeaseStore
	keeper     *OwnershipKeeper
	primary    model.DatabaseInstance
	replicas   []model.DatabaseInstance
	resource   model.HAEndpoint
	lease      endpoint.Lease
}

func newReplicaFailureOwnershipHarness(t *testing.T, outcome model.ProbeOutcome) *replicaFailureOwnershipHarness {
	t.Helper()
	harness := &replicaFailureOwnershipHarness{now: time.Date(2026, time.October, 15, 2, 0, 0, 0, time.UTC)}
	harness.inventory, harness.observer, _, harness.primary, harness.resource = ownershipKeeperFixture(harness.now)
	first := addOwnershipReplica(harness.inventory, harness.primary, harness.now, "bbbbbbbb")
	second := addOwnershipReplica(harness.inventory, harness.primary, harness.now, "cccccccc")
	harness.replicas = []model.DatabaseInstance{first, second}
	harness.observer.result.ObservedInstanceIDs = []model.ResourceID{harness.primary.ResourceID, first.ResourceID, second.ResourceID}

	harness.records = &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	harness.membership = authoritativeMembership(t)
	realStore := NewLeaseStore(harness.records, harness.membership, func() time.Time { return harness.now })
	harness.leases = &recordingOwnershipLeaseStore{store: realStore}
	harness.keeper = NewOwnershipKeeper(
		harness.inventory, harness.observer, harness.leases, harness.membership,
		func() time.Time { return harness.now }, 5*time.Second, 15*time.Second,
	)

	// Seed the durable stable lease through the normal healthy keeper path.
	if err := harness.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("seed healthy stable lease: %v", err)
	}
	if len(harness.records.records) != 1 {
		t.Fatalf("seeded lease records=%+v", harness.records.records)
	}
	for _, record := range harness.records.records {
		harness.lease = record.Lease
	}
	if harness.lease.OwnerID != harness.primary.ResourceID || harness.lease.OperationID != harness.resource.ResourceID {
		t.Fatalf("seeded stable lease=%+v", harness.lease)
	}
	harness.inventory.commits = 0
	harness.leases.requests = nil
	harness.setAckMissingEvidence(outcome)
	return harness
}

func (harness *replicaFailureOwnershipHarness) setAckMissingEvidence(outcome model.ProbeOutcome) {
	snapshot := harness.inventory.snapshots[harness.primary.ClusterID]
	snapshot.ObservedAt = harness.now
	primary := &snapshot.Instances[0]
	primary.Health = model.Health{State: model.HealthDegraded, Summary: "semi-sync has no acknowledging replica", ObservedAt: harness.now}
	primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
	for key, value := range map[string]string{
		"semi_sync_ack_missing":            "true",
		"semi_sync_required":               "true",
		"semi_sync_available":              "true",
		"semi_sync_source_enabled":         "true",
		"semi_sync_wait_for_replica_count": "1",
		"semi_sync_wait_point":             "AFTER_SYNC",
		"semi_sync_source_status":          "false",
		"semi_sync_source_clients":         "0",
	} {
		primary.EngineMetadata[key] = value
	}
	delete(primary.EngineMetadata, "semi_sync_probe_error")
	harness.primary = *primary

	// One replica is unreachable at the SQL probe. The other remains reachable
	// but has stopped replication with an explicit error.
	first := &snapshot.Instances[1]
	first.Health = model.Health{State: model.HealthUnhealthy, Summary: "database unavailable", ObservedAt: harness.now}
	first.Replication.IOThread = model.ThreadStopped
	first.Replication.SQLThread = model.ThreadStopped
	first.Replication.LastIOError = "connection refused"
	second := &snapshot.Instances[2]
	second.Health = model.Health{State: model.HealthDegraded, Summary: "replication stopped", ObservedAt: harness.now}
	second.Replication.IOThread = model.ThreadStopped
	second.Replication.SQLThread = model.ThreadStopped
	second.Replication.LastSQLError = "replication applier stopped"
	harness.replicas = []model.DatabaseInstance{*first, *second}

	snapshot.Probes[0] = model.ProbeStatus{
		InstanceID: primary.ResourceID, Outcome: outcome,
		DiscoveryObservedAt: harness.now, Health: primary.Health,
	}
	snapshot.Probes[1] = model.ProbeStatus{
		InstanceID: first.ResourceID, Outcome: model.ProbeOutcomeDatabaseUnavailable,
		Health: first.Health,
	}
	snapshot.Probes[2] = model.ProbeStatus{
		InstanceID: second.ResourceID, Outcome: model.ProbeOutcomeReachable,
		DiscoveryObservedAt: harness.now, Health: second.Health,
	}
	harness.inventory.snapshots[harness.primary.ClusterID] = snapshot
}

func (harness *replicaFailureOwnershipHarness) advance(delta time.Duration, outcome model.ProbeOutcome) {
	harness.now = harness.now.Add(delta)
	harness.setAckMissingEvidence(outcome)
}

func (harness *replicaFailureOwnershipHarness) onlyLeaseRecord(t *testing.T) LeaseRecord {
	t.Helper()
	if len(harness.records.records) != 1 {
		t.Fatalf("lease records=%+v", harness.records.records)
	}
	for _, record := range harness.records.records {
		return record
	}
	return LeaseRecord{}
}

func (harness *replicaFailureOwnershipHarness) primaryInstance(t *testing.T) *model.DatabaseInstance {
	t.Helper()
	snapshot := harness.inventory.snapshots[harness.primary.ClusterID]
	if len(snapshot.Instances) == 0 {
		t.Fatal("primary fixture is missing")
	}
	return &snapshot.Instances[0]
}

func (harness *replicaFailureOwnershipHarness) putPrimary(instance model.DatabaseInstance) {
	snapshot := harness.inventory.snapshots[harness.primary.ClusterID]
	snapshot.Instances[0] = instance
	harness.inventory.snapshots[harness.primary.ClusterID] = snapshot
}

func TestOwnershipKeeperRetainsStableVIPAcrossReplicaFailuresAndMultipleTTLs(t *testing.T) {
	for _, outcome := range []model.ProbeOutcome{model.ProbeOutcomeReachable, model.ProbeOutcomeMetricsUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, outcome)
			seededID := harness.lease.ResourceID
			startedAt := harness.now

			// Each reconciliation enters the one-minute lease renewal window. Five
			// rounds prove the same durable lease stays live across more than two TTLs.
			for round := 0; round < 5; round++ {
				harness.advance(31*time.Second, outcome)
				if err := harness.keeper.RunOnce(context.Background()); err != nil {
					t.Fatalf("round %d retain stable VIP: %v", round+1, err)
				}
				record := harness.onlyLeaseRecord(t)
				if record.Lease.ResourceID != seededID || record.Lease.OwnerID != harness.primary.ResourceID || !record.Lease.ExpiresAt.After(harness.now) {
					t.Fatalf("round %d changed or expired stable lease=%+v", round+1, record.Lease)
				}
				request := harness.leases.requests[len(harness.leases.requests)-1]
				if !request.RenewOnly || request.OwnerID != harness.primary.ResourceID || request.OperationID != harness.resource.ResourceID {
					t.Fatalf("round %d lease request=%+v", round+1, request)
				}
				if harness.inventory.commits != 0 {
					t.Fatalf("round %d committed VIP metadata %d times", round+1, harness.inventory.commits)
				}
			}
			if harness.now.Sub(startedAt) <= 2*time.Minute {
				t.Fatalf("test did not cross two lease TTLs: elapsed=%s", harness.now.Sub(startedAt))
			}
		})
	}
}

func TestOwnershipKeeperAckMissingPartialObservationStillRequiresDataMajority(t *testing.T) {
	harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
	harness.observer.result.Complete = false
	harness.observer.result.ObservedInstanceIDs = []model.ResourceID{harness.primary.ResourceID, harness.replicas[0].ResourceID}
	harness.advance(31*time.Second, model.ProbeOutcomeReachable)
	if err := harness.keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("majority partial renewal: %v", err)
	}
	if len(harness.leases.requests) != 1 || !harness.leases.requests[0].RenewOnly {
		t.Fatalf("partial renewal requests=%+v", harness.leases.requests)
	}
	if harness.inventory.commits != 0 {
		t.Fatalf("partial renewal committed metadata %d times", harness.inventory.commits)
	}
}

func TestOwnershipKeeperAckMissingAcceptsEitherMissingAckSignal(t *testing.T) {
	tests := []struct {
		name, status, clients string
	}{
		{name: "source status is false", status: "false", clients: "1"},
		{name: "client count is below required", status: "true", clients: "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
			harness.advance(31*time.Second, model.ProbeOutcomeReachable)
			primary := *harness.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			primary.EngineMetadata["semi_sync_source_status"] = test.status
			primary.EngineMetadata["semi_sync_source_clients"] = test.clients
			harness.putPrimary(primary)
			if err := harness.keeper.RunOnce(context.Background()); err != nil {
				t.Fatalf("valid missing-ACK signal rejected: %v", err)
			}
			if len(harness.leases.requests) != 1 || !harness.leases.requests[0].RenewOnly || harness.inventory.commits != 0 {
				t.Fatalf("missing-ACK signal requests=%+v commits=%d", harness.leases.requests, harness.inventory.commits)
			}
		})
	}
}

func TestOwnershipKeeperAckMissingNeverCreatesOrRecoversUnsafeLease(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*replicaFailureOwnershipHarness)
	}{
		{name: "missing lease", mutate: func(h *replicaFailureOwnershipHarness) { h.records.records = map[model.ResourceID]LeaseRecord{} }},
		{name: "expired lease", mutate: func(h *replicaFailureOwnershipHarness) {
			record := h.onlyLeaseRecord(t)
			record.Lease.ExpiresAt = h.now
			h.records.records[record.Lease.ResourceID] = record
		}},
		{name: "foreign owner stable lease", mutate: func(h *replicaFailureOwnershipHarness) {
			record := h.onlyLeaseRecord(t)
			record.Lease.OwnerID = model.NewResourceID()
			h.records.records[record.Lease.ResourceID] = record
		}},
		{name: "foreign operation stable lease", mutate: func(h *replicaFailureOwnershipHarness) {
			record := h.onlyLeaseRecord(t)
			record.Lease.OperationID = model.NewResourceID()
			h.records.records[record.Lease.ResourceID] = record
		}},
		{name: "transition lease", mutate: func(h *replicaFailureOwnershipHarness) {
			record := h.onlyLeaseRecord(t)
			record.Lease.PreviousOwnerID = h.primary.ResourceID
			record.Lease.OwnerID = model.NewResourceID()
			record.Lease.OperationID = model.NewResourceID()
			h.records.records[record.Lease.ResourceID] = record
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
			test.mutate(harness)
			harness.advance(31*time.Second, model.ProbeOutcomeReachable)
			err := harness.keeper.RunOnce(context.Background())
			if !errors.Is(err, endpoint.ErrLeaseConflict) {
				t.Fatalf("unsafe lease renewal error=%v", err)
			}
			if harness.inventory.commits != 0 {
				t.Fatalf("unsafe lease changed metadata commits=%d", harness.inventory.commits)
			}
			for _, record := range harness.records.records {
				if record.Lease.ResourceID != harness.lease.ResourceID {
					t.Fatalf("unsafe renewal created replacement lease=%+v", record.Lease)
				}
			}
		})
	}
}

func TestOwnershipKeeperAckMissingRejectsUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*replicaFailureOwnershipHarness)
		silentSkip bool
	}{
		{name: "primary read only", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			primary.EngineMetadata["read_only"] = "true"
			h.putPrimary(primary)
		}},
		{name: "primary super read only", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			primary.EngineMetadata["super_read_only"] = "true"
			h.putPrimary(primary)
		}},
		{name: "primary is in maintenance", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.Maintenance = true
			h.putPrimary(primary)
		}},
		{name: "primary SQL unreachable", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Probes[0].Outcome = model.ProbeOutcomeDatabaseUnavailable
			snapshot.Probes[0].DiscoveryObservedAt = time.Time{}
			snapshot.Probes[0].Health = model.Health{State: model.HealthUnhealthy, ObservedAt: h.now}
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "credentials unavailable", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Probes[0].Outcome = model.ProbeOutcomeCredentialsUnavailable
			snapshot.Probes[0].DiscoveryObservedAt = time.Time{}
			snapshot.Probes[0].Health = model.Health{State: model.HealthUnknown, ObservedAt: h.now}
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "unknown probe outcome", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Probes[0].Outcome = model.ProbeOutcomeUnknown
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "expired primary probe", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Probes[0].DiscoveryObservedAt = h.now.Add(-time.Minute)
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "unclassified degraded primary", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			delete(primary.EngineMetadata, "semi_sync_ack_missing")
			h.putPrimary(primary)
		}},
		{name: "semi sync not required", mutate: setPrimaryMetadata(t, "semi_sync_required", "false")},
		{name: "semi sync plugin unavailable", mutate: setPrimaryMetadata(t, "semi_sync_available", "false")},
		{name: "semi sync source disabled", mutate: setPrimaryMetadata(t, "semi_sync_source_enabled", "false")},
		{name: "semi sync wrong wait point", mutate: setPrimaryMetadata(t, "semi_sync_wait_point", "AFTER_COMMIT")},
		{name: "semi sync probe error", mutate: setPrimaryMetadata(t, "semi_sync_probe_error", "true")},
		{name: "invalid semi sync probe error", mutate: setPrimaryMetadata(t, "semi_sync_probe_error", "unknown")},
		{name: "primary has a replication source", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "dddddddd-bbbb-cccc-dddd-eeeeeeeeeeee"}
			h.putPrimary(primary)
		}},
		{name: "ACK recovered despite stale flag", mutate: func(h *replicaFailureOwnershipHarness) {
			primary := *h.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			primary.EngineMetadata["semi_sync_source_status"] = "true"
			primary.EngineMetadata["semi_sync_source_clients"] = "1"
			h.putPrimary(primary)
		}},
		{name: "multiple primary roles", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Instances[1].Role = model.RolePrimary
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "instance has invalid resource ID", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Instances[1].ResourceID = ""
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "instance resource IDs are duplicated", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Instances[1].ResourceID = h.primary.ResourceID
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "instance belongs to another cluster", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Instances[1].ClusterID = model.NewResourceID()
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "instance uses another engine", mutate: func(h *replicaFailureOwnershipHarness) {
			snapshot := h.inventory.snapshots[h.primary.ClusterID]
			snapshot.Instances[1].Engine = model.EnginePostgreSQL
			h.inventory.snapshots[h.primary.ClusterID] = snapshot
		}},
		{name: "multiple observed VIP owners", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.OwnerIDs = append(h.observer.result.OwnerIDs, h.replicas[0].ResourceID)
		}},
		{name: "zero observed VIP owners", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.OwnerIDs = nil
		}},
		{name: "actual owner differs from primary", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.OwnerIDs = []model.ResourceID{h.replicas[0].ResourceID}
		}},
		{name: "canonical owner differs from primary", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.CanonicalOwnerID = h.replicas[0].ResourceID
		}},
		{name: "endpoint owner differs from primary", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.EndpointOwnerID = h.replicas[0].ResourceID
		}},
		{name: "only one of three data agents observed", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.Complete = false
			h.observer.result.ObservedInstanceIDs = []model.ResourceID{h.primary.ResourceID}
		}},
		{name: "partial majority excludes current owner", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.Complete = false
			h.observer.result.ObservedInstanceIDs = []model.ResourceID{h.replicas[0].ResourceID, h.replicas[1].ResourceID}
		}},
		{name: "complete observation misses one instance", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.ObservedInstanceIDs = []model.ResourceID{h.primary.ResourceID, h.replicas[0].ResourceID}
		}},
		{name: "complete observation duplicates an instance", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.ObservedInstanceIDs = []model.ResourceID{h.primary.ResourceID, h.replicas[0].ResourceID, h.replicas[0].ResourceID}
		}},
		{name: "complete observation includes unknown instance", mutate: func(h *replicaFailureOwnershipHarness) {
			h.observer.result.ObservedInstanceIDs = []model.ResourceID{h.primary.ResourceID, h.replicas[0].ResourceID, model.NewResourceID()}
		}},
		{name: "controller has no majority", silentSkip: true, mutate: func(h *replicaFailureOwnershipHarness) {
			for id := range h.membership.controllers {
				if id != h.membership.localID {
					if err := h.membership.SetHealth(id, false); err != nil {
						t.Fatalf("revoke controller health: %v", err)
					}
				}
			}
		}},
		{name: "disaster recovery active", silentSkip: true, mutate: func(h *replicaFailureOwnershipHarness) {
			h.inventory.clusters[0].RecoveryFreeze = true
			h.inventory.clusters[0].Recovery = &model.RecoveryState{LastRecoveryStatus: "running"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
			test.mutate(harness)
			err := harness.keeper.RunOnce(context.Background())
			if !test.silentSkip && err == nil {
				t.Fatal("unsafe ACK-missing evidence was accepted")
			}
			if len(harness.leases.requests) != 0 {
				t.Fatalf("unsafe evidence emitted lease requests=%+v err=%v", harness.leases.requests, err)
			}
			if harness.inventory.commits != 0 {
				t.Fatalf("unsafe evidence committed metadata %d times", harness.inventory.commits)
			}
		})
	}
}

func TestOwnershipKeeperAckMissingRejectsMissingOrMalformedMetadata(t *testing.T) {
	keys := []string{
		"semi_sync_ack_missing", "semi_sync_required", "semi_sync_available", "semi_sync_source_enabled",
		"semi_sync_wait_for_replica_count", "semi_sync_wait_point", "semi_sync_source_status", "semi_sync_source_clients",
	}
	for _, key := range keys {
		t.Run("missing "+key, func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
			primary := *harness.primaryInstance(t)
			primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
			delete(primary.EngineMetadata, key)
			harness.putPrimary(primary)
			if err := harness.keeper.RunOnce(context.Background()); err == nil {
				t.Fatalf("missing %s was accepted", key)
			}
			if len(harness.leases.requests) != 0 || harness.inventory.commits != 0 {
				t.Fatalf("missing %s emitted requests=%+v commits=%d", key, harness.leases.requests, harness.inventory.commits)
			}
		})
	}

	malformed := []struct{ key, value string }{
		{key: "semi_sync_ack_missing", value: "yes"},
		{key: "semi_sync_required", value: "yes"},
		{key: "semi_sync_source_enabled", value: "enabled"},
		{key: "semi_sync_source_status", value: "off"},
		{key: "semi_sync_source_status", value: "TRUE"},
		{key: "semi_sync_wait_for_replica_count", value: "-1"},
		{key: "semi_sync_wait_for_replica_count", value: "not-an-integer"},
		{key: "semi_sync_source_clients", value: "-1"},
		{key: "semi_sync_source_clients", value: "not-an-integer"},
	}
	for _, test := range malformed {
		t.Run("malformed "+test.key+"="+test.value, func(t *testing.T) {
			harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
			setPrimaryMetadata(t, test.key, test.value)(harness)
			if err := harness.keeper.RunOnce(context.Background()); err == nil {
				t.Fatalf("malformed %s=%q was accepted", test.key, test.value)
			}
			if len(harness.leases.requests) != 0 || harness.inventory.commits != 0 {
				t.Fatalf("malformed metadata emitted requests=%+v commits=%d", harness.leases.requests, harness.inventory.commits)
			}
		})
	}
}

func setPrimaryMetadata(t *testing.T, key, value string) func(*replicaFailureOwnershipHarness) {
	t.Helper()
	return func(harness *replicaFailureOwnershipHarness) {
		primary := *harness.primaryInstance(t)
		primary.EngineMetadata = cloneStringMap(primary.EngineMetadata)
		primary.EngineMetadata[key] = value
		harness.putPrimary(primary)
	}
}

type ackMissingDiscoverRunner struct{}

func (ackMissingDiscoverRunner) Query(_ context.Context, _ pkgadapter.Endpoint, _ pkgadapter.Credentials, query string) ([]mysqladapter.Row, error) {
	switch {
	case strings.Contains(query, "@@server_uuid"):
		return []mysqladapter.Row{{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "hostname": "mysql-primary", "port": "3306",
			"server_id": "1", "version": "8.4.10", "read_only": "0", "super_read_only": "0",
			"gtid_mode": "ON", "gtid_executed": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-100",
			"gtid_purged": "", "log_bin": "1", "binlog_format": "ROW",
		}}, nil
	case strings.Contains(query, "SHOW GLOBAL VARIABLES LIKE"):
		return []mysqladapter.Row{
			{"Variable_name": "rpl_semi_sync_source_enabled", "Value": "ON"},
			{"Variable_name": "rpl_semi_sync_replica_enabled", "Value": "ON"},
			{"Variable_name": "rpl_semi_sync_source_wait_for_replica_count", "Value": "1"},
			{"Variable_name": "rpl_semi_sync_source_timeout", "Value": "10000"},
			{"Variable_name": "rpl_semi_sync_source_wait_no_replica", "Value": "ON"},
			{"Variable_name": "rpl_semi_sync_source_wait_point", "Value": "AFTER_SYNC"},
		}, nil
	case strings.Contains(query, "SHOW GLOBAL STATUS LIKE"):
		return []mysqladapter.Row{
			{"Variable_name": "Rpl_semi_sync_source_status", "Value": "OFF"},
			{"Variable_name": "Rpl_semi_sync_source_clients", "Value": "0"},
			{"Variable_name": "Rpl_semi_sync_replica_status", "Value": "OFF"},
		}, nil
	case query == "SHOW REPLICA STATUS":
		return nil, nil
	default:
		return nil, errors.New("unexpected MySQL probe query: " + query)
	}
}

func TestMySQLDiscoverAckMissingEvidenceRenewsExistingVIPLeaseEndToEnd(t *testing.T) {
	harness := newReplicaFailureOwnershipHarness(t, model.ProbeOutcomeReachable)
	discoverer := mysqladapter.New(ackMissingDiscoverRunner{}).RequireSemiSync(true)
	seededID := harness.lease.ResourceID
	request := pkgadapter.DiscoverRequest{
		ClusterID: harness.primary.ClusterID,
		Endpoint:  pkgadapter.Endpoint{Hostname: "mysql-primary", IPAddress: "192.0.2.10", Port: 3306},
		Credentials: pkgadapter.Credentials{
			Username: "monitor", Password: "fixture-only",
		},
	}

	for round := 0; round < 3; round++ {
		harness.advance(31*time.Second, model.ProbeOutcomeReachable)
		discovery, err := discoverer.Discover(context.Background(), request)
		if err != nil {
			t.Fatalf("round %d MySQL discover: %v", round+1, err)
		}
		instance := discovery.Instance
		if instance.Health.State != model.HealthDegraded || instance.EngineMetadata["semi_sync_ack_missing"] != "true" {
			t.Fatalf("round %d producer omitted ACK-loss classification: %+v", round+1, instance)
		}
		instance.ResourceMeta = harness.primary.ResourceMeta
		instance.ClusterID = harness.primary.ClusterID
		instance.Health.ObservedAt = harness.now
		snapshot := harness.inventory.snapshots[harness.primary.ClusterID]
		snapshot.Instances[0] = instance
		snapshot.Probes[0] = model.ProbeStatus{
			InstanceID: instance.ResourceID, Outcome: model.ProbeOutcomeReachable,
			DiscoveryObservedAt: harness.now, Health: instance.Health,
		}
		harness.inventory.snapshots[harness.primary.ClusterID] = snapshot
		harness.primary = instance

		if err := harness.keeper.RunOnce(context.Background()); err != nil {
			t.Fatalf("round %d end-to-end renewal: %v", round+1, err)
		}
		record := harness.onlyLeaseRecord(t)
		if record.Lease.ResourceID != seededID || record.Lease.OwnerID != instance.ResourceID || !record.Lease.ExpiresAt.After(harness.now) {
			t.Fatalf("round %d end-to-end lease=%+v", round+1, record.Lease)
		}
		if got := harness.leases.requests[len(harness.leases.requests)-1]; !got.RenewOnly {
			t.Fatalf("round %d end-to-end request=%+v", round+1, got)
		}
		if harness.inventory.commits != 0 {
			t.Fatalf("round %d end-to-end metadata commits=%d", round+1, harness.inventory.commits)
		}
	}
}

func TestOwnershipKeeperHealthyPrimaryWithoutAckMissingFlagKeepsExistingBehavior(t *testing.T) {
	now := time.Date(2026, time.October, 15, 3, 0, 0, 0, time.UTC)
	inventory, observer, _, primary, resource := ownershipKeeperFixture(now)
	first := addOwnershipReplica(inventory, primary, now, "bbbbbbbb")
	second := addOwnershipReplica(inventory, primary, now, "cccccccc")
	observer.result.ObservedInstanceIDs = []model.ResourceID{primary.ResourceID, first.ResourceID, second.ResourceID}
	records := &leaseRecordStore{records: map[model.ResourceID]LeaseRecord{}}
	membership := authoritativeMembership(t)
	realStore := NewLeaseStore(records, membership, func() time.Time { return now })
	recording := &recordingOwnershipLeaseStore{store: realStore}
	keeper := NewOwnershipKeeper(inventory, observer, recording, membership, func() time.Time { return now }, 5*time.Second, 15*time.Second)
	if err := keeper.RunOnce(context.Background()); err != nil {
		t.Fatalf("healthy primary ownership: %v", err)
	}
	if len(recording.requests) != 1 || recording.requests[0].RenewOnly || recording.requests[0].OwnerID != primary.ResourceID {
		t.Fatalf("healthy primary requests=%+v", recording.requests)
	}
	if len(records.records) != 1 || inventory.commits != 1 {
		t.Fatalf("healthy path records=%+v commits=%d", records.records, inventory.commits)
	}
	for _, record := range records.records {
		if record.Lease.OperationID != resource.ResourceID || record.Lease.OwnerID != primary.ResourceID {
			t.Fatalf("healthy stable lease=%+v", record.Lease)
		}
	}
}
