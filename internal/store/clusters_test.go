package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func seedRetirementCluster(t *testing.T, repository *Repository) (model.DatabaseCluster, model.DatabaseInstance, model.Endpoint) {
	t.Helper()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders-mysql"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	inventory, found := repository.DiscoveryInventory(cluster.ResourceID)
	if !found {
		t.Fatal("created cluster has no discovery inventory")
	}
	instance := model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"},
		DisplayName:    "mysql-a:3306", Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306,
		Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}
	topology, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: inventory.Generation, ObservedAt: time.Now().UTC(),
		Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		Health:       model.Health{State: model.HealthHealthy},
	})
	if err != nil {
		t.Fatalf("seed discovery: %v", err)
	}
	return cluster, topology.Instances[0], endpoints[0]
}

func TestRetireClusterRemovesLiveInventoryAndKeepsHistory(t *testing.T) {
	repository := NewMemory()
	now := time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	cluster, instance, _ := seedRetirementCluster(t, repository)
	resource, _, err := repository.PutHAEndpoint(HAEndpointSpec{
		ClusterID: cluster.ResourceID, Kind: model.EndpointVIP, IPAddress: "192.0.2.100",
		Interface: "eth0", Prefix: 24, OwnerID: instance.ResourceID, Active: true,
	})
	if err != nil {
		t.Fatalf("put HA endpoint: %v", err)
	}
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{InstanceID: instance.ResourceID, ObservedAt: now, Values: map[string]float64{"qps": 12}}}, 10); err != nil {
		t.Fatalf("store metrics: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(cluster.ResourceID, []model.MetadataAnomaly{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: "test", Severity: "warning", Message: "historical anomaly"}}); err != nil {
		t.Fatalf("store anomaly: %v", err)
	}
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{SourceInstanceID: instance.ResourceID, TargetInstanceID: model.NewResourceID(), Healthy: true}}); err != nil {
		t.Fatalf("store replication link: %v", err)
	}
	historicalRequest := model.OperationRecord{
		Operation:      model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "operator"},
		TargetID:       instance.ResourceID,
		IdempotencyKey: "retirement-history-operation",
	}
	historicalOperation, _, err := repository.CreateOperation(historicalRequest)
	if err != nil {
		t.Fatalf("create historical operation: %v", err)
	}
	historicalOperation, err = repository.TransitionOperation(historicalOperation.ResourceID, historicalOperation.MetadataRevision, model.OperationTransition{Stage: model.StageReport, Status: model.OperationFailed, Message: "historical failure"})
	if err != nil {
		t.Fatalf("finish historical operation: %v", err)
	}
	historyOperationID := model.NewResourceID()
	if err := repository.RecordAudit(model.AuditEvent{OperationID: historyOperationID, Stage: model.StageAudit, Actor: "operator", Message: "historical event"}); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if err := repository.RecordReport(model.Report{OperationID: historyOperationID, Title: "historical report", Status: model.OperationSucceeded, Summary: "kept"}); err != nil {
		t.Fatalf("record report: %v", err)
	}
	task, err := repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID,
		Status: lifecycle.TaskSucceeded, Message: "historical task",
	})
	if err != nil {
		t.Fatalf("record terminal lifecycle task: %v", err)
	}

	result, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin")
	if err != nil {
		t.Fatalf("retire cluster: %v", err)
	}
	if result.Cluster.ResourceID != cluster.ResourceID || result.InstancesRemoved != 1 || result.EndpointsRemoved != 2 || result.HAEndpointsRemoved != 1 || result.ReplicationLinksRemoved != 1 || result.MetricSamplesRemoved != 1 || result.AnomaliesRemoved != 1 || result.RetiredAt != now {
		t.Fatalf("retirement summary = %+v", result)
	}
	if _, found := repository.Cluster(cluster.ResourceID); found || len(repository.Clusters()) != 0 || len(repository.Instances(cluster.ResourceID)) != 0 || len(repository.Endpoints(cluster.ResourceID)) != 0 || len(repository.HAEndpoints(cluster.ResourceID)) != 0 || len(repository.MetricSamples(cluster.ResourceID)) != 0 {
		t.Fatalf("live cluster inventory remains: clusters=%+v instances=%+v endpoints=%+v HA=%+v metrics=%+v", repository.Clusters(), repository.Instances(cluster.ResourceID), repository.Endpoints(cluster.ResourceID), repository.HAEndpoints(cluster.ResourceID), repository.MetricSamples(cluster.ResourceID))
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("retired topology snapshot remains active")
	}
	if _, found := repository.HAEndpoint(resource.ResourceID); found {
		t.Fatal("retired HA endpoint remains active")
	}
	if len(repository.Audits()) != 2 || repository.Audits()[0].Message != "historical event" || !strings.Contains(repository.Audits()[1].Message, cluster.DisplayName) || repository.Audits()[1].Actor != "admin" {
		t.Fatalf("audit history was not retained and extended: %+v", repository.Audits())
	}
	if len(repository.Reports()) != 1 || repository.Reports()[0].Title != "historical report" {
		t.Fatalf("report history changed: %+v", repository.Reports())
	}
	retirementAudit := repository.Audits()[1].Message
	for _, count := range []string{"instances=1", "endpoints=2", "ha_endpoints=1", "replication_links=1", "metric_samples=1", "anomalies=1"} {
		if !strings.Contains(retirementAudit, count) {
			t.Fatalf("retirement audit missing %q: %s", count, retirementAudit)
		}
	}
	replayed, existing, err := repository.CreateOperation(historicalRequest)
	if err != nil || !existing || replayed.ResourceID != historicalOperation.ResourceID || replayed.Status != model.OperationFailed {
		t.Fatalf("terminal operation history was not retained: replay=%+v existing=%t err=%v", replayed, existing, err)
	}
	if persisted, found := repository.LifecycleTask(task.ResourceID); !found || persisted.Status != lifecycle.TaskSucceeded {
		t.Fatalf("terminal lifecycle history changed: %+v found=%t", persisted, found)
	}
	if recreated, _, createErr := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: cluster.DisplayName}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-new", Port: 3306, Active: true}}); createErr != nil || recreated.ResourceID == cluster.ResourceID {
		t.Fatalf("retired display name was not reusable with a new UUID: cluster=%+v err=%v", recreated, createErr)
	}
}

func TestRetireClusterRequiresExactDisplayName(t *testing.T) {
	repository := NewMemory()
	cluster, _, _ := seedRetirementCluster(t, repository)
	for _, confirmation := range []string{"", "orders", "ORDERS-MYSQL", " orders-mysql "} {
		if _, err := repository.RetireCluster(cluster.ResourceID, confirmation, "admin"); !errors.Is(err, ErrValidation) {
			t.Fatalf("confirmation %q error = %v, want validation", confirmation, err)
		}
		if _, found := repository.Cluster(cluster.ResourceID); !found {
			t.Fatalf("confirmation %q retired the cluster", confirmation)
		}
	}
}

func TestRetireClusterMissingResourceReturnsNotFound(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.RetireCluster(model.NewResourceID(), "missing", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cluster retirement error = %v, want not found", err)
	}
}

func TestRetireClusterBlocksActiveWork(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *Repository, model.DatabaseCluster, model.DatabaseInstance)
	}{
		{name: "running operation", seed: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, instance model.DatabaseInstance) {
			operation, _, err := repository.CreateOperation(model.OperationRecord{
				Operation: model.Operation{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "admin"},
				TargetID:  instance.ResourceID, IdempotencyKey: "retirement-running-operation",
			})
			if err != nil {
				t.Fatalf("create operation: %v", err)
			}
			if _, err = repository.TransitionOperation(operation.ResourceID, operation.MetadataRevision, model.OperationTransition{Stage: model.StageExecute, Status: model.OperationRunning}); err != nil {
				t.Fatalf("start operation: %v", err)
			}
		}},
		{name: "operation lock", seed: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, _ model.DatabaseInstance) {
			now := repository.now().UTC()
			if err := repository.PutCoordinationOperationLock(coordination.OperationLockRecord{ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, OperationID: model.NewResourceID(), ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatalf("put lock: %v", err)
			}
		}},
		{name: "stable ownership lease", seed: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, instance model.DatabaseInstance) {
			now := repository.now().UTC()
			haEndpointID := model.NewResourceID()
			if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: endpoint.Lease{ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, HAEndpointID: haEndpointID, OperationID: haEndpointID, OwnerID: instance.ResourceID, ExpiresAt: now.Add(time.Minute), Active: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatalf("put lease: %v", err)
			}
		}},
		{name: "transition ownership lease", seed: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, instance model.DatabaseInstance) {
			now := repository.now().UTC()
			if err := repository.PutCoordinationLease(coordination.LeaseRecord{Lease: endpoint.Lease{ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, HAEndpointID: model.NewResourceID(), OperationID: model.NewResourceID(), OwnerID: instance.ResourceID, ExpiresAt: now.Add(time.Minute), Active: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatalf("put lease: %v", err)
			}
		}},
		{name: "lifecycle task", seed: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, _ model.DatabaseInstance) {
			if _, err := repository.PutLifecycleTask(lifecycle.Task{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID, Status: lifecycle.TaskRunning}); err != nil {
				t.Fatalf("put lifecycle task: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := NewMemory()
			cluster, instance, _ := seedRetirementCluster(t, repository)
			test.seed(t, repository, cluster, instance)
			if _, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin"); !errors.Is(err, ErrConflict) {
				t.Fatalf("retirement error = %v, want conflict", err)
			}
			if _, found := repository.Cluster(cluster.ResourceID); !found {
				t.Fatal("blocked retirement removed cluster")
			}
		})
	}
}

func TestRetireClusterPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, _, _ := seedRetirementCluster(t, repository)
	if _, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin"); err != nil {
		t.Fatalf("retire cluster: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	if _, found := reopened.Cluster(cluster.ResourceID); found || len(reopened.Audits()) != 1 || !strings.Contains(reopened.Audits()[0].Message, cluster.DisplayName) {
		t.Fatalf("retirement was not durable: cluster=%t audits=%+v", found, reopened.Audits())
	}
}

func TestRetireClusterReturnsCommittedResultAfterDirectorySyncWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, _, _ := seedRetirementCluster(t, repository)
	repository.syncDirectory = func(string) error { return errors.New("directory sync unavailable") }

	result, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin")
	if !errors.Is(err, ErrPostCommitDurability) {
		t.Fatalf("retirement error = %v, want post-commit durability warning", err)
	}
	if result.Cluster.ResourceID != cluster.ResourceID || result.InstancesRemoved != 1 || result.EndpointsRemoved != 1 {
		t.Fatalf("committed retirement result was discarded: %+v", result)
	}
	if _, found := repository.Cluster(cluster.ResourceID); found {
		t.Fatal("committed retirement remains in live inventory")
	}
	reopened, reopenErr := Open(path)
	if reopenErr != nil {
		t.Fatalf("reopen repository: %v", reopenErr)
	}
	if _, found := reopened.Cluster(cluster.ResourceID); found || len(reopened.Audits()) != 1 {
		t.Fatalf("committed retirement was not recoverable: found=%t audits=%+v", found, reopened.Audits())
	}
}

func TestRetireClusterPersistenceFailureKeepsLiveAndDiskInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, _, _ := seedRetirementCluster(t, repository)
	repository.syncFile = func(*os.File) error { return errors.New("file sync unavailable") }

	if _, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin"); err == nil || errors.Is(err, ErrPostCommitDurability) {
		t.Fatalf("retirement error = %v, want pre-commit persistence failure", err)
	}
	if _, found := repository.Cluster(cluster.ResourceID); !found || len(repository.Audits()) != 0 {
		t.Fatalf("failed retirement changed live state: found=%t audits=%+v", found, repository.Audits())
	}
	reopened, reopenErr := Open(path)
	if reopenErr != nil {
		t.Fatalf("reopen repository: %v", reopenErr)
	}
	if _, found := reopened.Cluster(cluster.ResourceID); !found || len(reopened.Audits()) != 0 {
		t.Fatalf("failed retirement changed disk state: found=%t audits=%+v", found, reopened.Audits())
	}
}

func TestRetireClusterRemovesExpiredOwnershipLease(t *testing.T) {
	repository := NewMemory()
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	cluster, instance, _ := seedRetirementCluster(t, repository)
	lease := coordination.LeaseRecord{
		Lease: endpoint.Lease{
			ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, HAEndpointID: model.NewResourceID(),
			OperationID: model.NewResourceID(), OwnerID: instance.ResourceID, ExpiresAt: now.Add(-time.Second), Active: true,
		},
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Second),
	}
	if err := repository.PutCoordinationLease(lease); err != nil {
		t.Fatalf("put expired ownership lease: %v", err)
	}
	lock := coordination.OperationLockRecord{
		ResourceID: model.NewResourceID(), ClusterID: cluster.ResourceID, OperationID: model.NewResourceID(),
		ExpiresAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Second),
	}
	if err := repository.PutCoordinationOperationLock(lock); err != nil {
		t.Fatalf("put expired operation lock: %v", err)
	}
	if _, err := repository.RetireCluster(cluster.ResourceID, cluster.DisplayName, "admin"); err != nil {
		t.Fatalf("retire cluster with expired lease: %v", err)
	}
	for _, persisted := range repository.CoordinationLeases() {
		if persisted.Lease.ResourceID == lease.Lease.ResourceID {
			t.Fatal("retirement kept an expired ownership lease")
		}
	}
	for _, persisted := range repository.CoordinationOperationLocks() {
		if persisted.ResourceID == lock.ResourceID {
			t.Fatal("retirement kept an expired operation lock")
		}
	}
}
