package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	metricsservice "clusterguard.io/ha/internal/metrics"
	"clusterguard.io/ha/pkg/model"
)

func mysqlInstance(clusterID model.ResourceID, hostname string, ipAddress string, port int) model.DatabaseInstance {
	return model.DatabaseInstance{
		ClusterID: clusterID,
		Engine:    model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   ipAddress,
		Port:        port,
		Role:        model.RoleReplica,
		Health:      model.Health{State: model.HealthHealthy},
	}
}

func TestReconcileInstanceKeepsResourceIDWhenMySQLHostnameChanges(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	first, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306))
	if err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	second, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-renamed", "192.0.2.20", 3310))
	if err != nil {
		t.Fatalf("second reconciliation: %v", err)
	}
	if second.Instance.ResourceID != first.Instance.ResourceID {
		t.Fatalf("same MySQL identity created a new resource: first=%s second=%s", first.Instance.ResourceID, second.Instance.ResourceID)
	}
	if second.Instance.MetadataRevision != first.Instance.MetadataRevision+1 {
		t.Fatalf("expected metadata revision increment: first=%d second=%d", first.Instance.MetadataRevision, second.Instance.MetadataRevision)
	}
	if second.Instance.Hostname != "mysql-renamed" || second.Instance.IPAddress != "192.0.2.20" || second.Instance.Port != 3310 {
		t.Fatalf("mutable endpoint was not updated: %+v", second.Instance)
	}
	aliases := second.Instance.Aliases
	if !contains(aliases, "mysql-a:3306") || !contains(aliases, "192.0.2.10:3306") {
		t.Fatalf("previous endpoint was not retained as aliases: %v", aliases)
	}
	instances := repository.Instances(clusterID)
	if len(instances) != 1 {
		t.Fatalf("same engine identity must leave one instance, got %d", len(instances))
	}
}

func TestFileRepositoryPersistsReconciledIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	clusterID := model.NewResourceID()
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	result, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload repository: %v", err)
	}
	instances := reloaded.Instances(clusterID)
	if len(instances) != 1 || instances[0].ResourceID != result.Instance.ResourceID {
		t.Fatalf("persisted identity was not restored: %+v", instances)
	}
}

func TestReconcileRejectsEndpointOwnedByDifferentIdentity(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	if _, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306)); err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	conflict := mysqlInstance(clusterID, "mysql-b", "192.0.2.10", 3306)
	conflict.EngineIdentity["server_uuid"] = "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"
	if _, err := repository.ReconcileInstance(conflict); err == nil {
		t.Fatalf("endpoint collision with a distinct engine identity must fail")
	}
}

func TestRepositoryPersistsAuditAndReportResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	operationID := model.NewResourceID()
	repository.RecordAudit(model.AuditEvent{OperationID: operationID, Stage: model.StageExecute, Message: "executed"})
	repository.RecordReport(model.Report{OperationID: operationID, Title: "operation report", Summary: "verified"})
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Audits()) != 1 || reloaded.Audits()[0].OperationID != operationID {
		t.Fatalf("audit was not persisted: %+v", reloaded.Audits())
	}
	if len(reloaded.Reports()) != 1 || reloaded.Reports()[0].OperationID != operationID {
		t.Fatalf("report was not persisted: %+v", reloaded.Reports())
	}
}

func TestRepositoryPersistsInventoryLinksAndBoundedMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine:      model.EngineMySQL,
		DisplayName: "production",
	}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: cluster.ResourceID,
		Kind:      model.EndpointDatabase,
		Hostname:  "mysql-a",
		Port:      3306,
		Active:    true,
	})
	if err != nil {
		t.Fatalf("upsert first endpoint: %v", err)
	}
	second, err := repository.UpsertEndpoint(model.Endpoint{
		ClusterID: cluster.ResourceID,
		Kind:      model.EndpointDatabase,
		Hostname:  "mysql-b",
		Port:      3306,
		Active:    true,
	})
	if err != nil {
		t.Fatalf("upsert second endpoint: %v", err)
	}
	if first.ResourceID == "" || second.ResourceID == "" {
		t.Fatal("endpoint UUID is required")
	}
	link := model.ReplicationLink{
		ResourceMeta:     model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:        cluster.ResourceID,
		SourceInstanceID: model.NewResourceID(),
		TargetInstanceID: model.NewResourceID(),
		Healthy:          true,
	}
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{link}); err != nil {
		t.Fatalf("replace replication links: %v", err)
	}
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{
		InstanceID: link.TargetInstanceID,
		ObservedAt: time.Now(),
		Values:     map[string]float64{"qps": 4},
	}}, 60); err != nil {
		t.Fatalf("store metric samples: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload repository: %v", err)
	}
	if len(reloaded.Endpoints(cluster.ResourceID)) != 2 || len(reloaded.ReplicationLinks(cluster.ResourceID)) != 1 || len(reloaded.MetricSamples(cluster.ResourceID)) != 1 {
		t.Fatalf("inventory snapshot did not round trip: endpoints=%d links=%d samples=%d", len(reloaded.Endpoints(cluster.ResourceID)), len(reloaded.ReplicationLinks(cluster.ResourceID)), len(reloaded.MetricSamples(cluster.ResourceID)))
	}
}

func TestRepositoryFindsInstanceByMySQLIdentityWithoutLeakingIdentityMaps(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "identity-lookup"}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "192.0.2.10", 3306)
	instance.EngineMetadata = map[string]string{"version": "8.0"}
	instance.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "source-uuid"}
	lagSeconds := int64(3)
	instance.Replication.LagSeconds = &lagSeconds
	result, err := repository.ReconcileInstance(instance)
	if err != nil {
		t.Fatalf("reconcile instance: %v", err)
	}
	found, ok := repository.FindInstanceByIdentity(cluster.ResourceID, model.EngineMySQL, model.EngineIdentity{
		"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if !ok || found.ResourceID != result.Instance.ResourceID {
		t.Fatalf("MySQL instance was not found by server UUID: %+v, found=%t", found, ok)
	}
	found.EngineIdentity["server_uuid"] = "mutated"
	found.EngineMetadata["version"] = "mutated"
	found.Replication.SourceIdentity["server_uuid"] = "mutated"
	*found.Replication.LagSeconds = 999
	again, ok := repository.FindInstanceByIdentity(cluster.ResourceID, model.EngineMySQL, model.EngineIdentity{
		"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if !ok || again.EngineIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || again.EngineMetadata["version"] != "8.0" || again.Replication.SourceIdentity["server_uuid"] != "source-uuid" || again.Replication.LagSeconds == nil || *again.Replication.LagSeconds != 3 {
		t.Fatalf("instance read leaked nested maps: %+v, found=%t", again, ok)
	}
}

func TestCreateClusterWithEndpointsRejectsInvalidSetAtomically(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	_, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID},
		Engine:       model.EngineMySQL,
		DisplayName:  "invalid-endpoint-set",
	}, []model.Endpoint{
		{ClusterID: clusterID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{ClusterID: clusterID, Kind: model.EndpointDatabase, IPAddress: "MYSQL-A", Port: 3306, Active: true},
	})
	if err == nil {
		t.Fatal("colliding endpoint set must fail")
	}
	if _, ok := repository.Cluster(clusterID); ok {
		t.Fatal("invalid endpoint set persisted its cluster")
	}
	if len(repository.Endpoints(clusterID)) != 0 {
		t.Fatal("invalid endpoint set persisted endpoints")
	}
}

func TestCreateClusterWithEndpointsRejectsDuplicateNameAndGlobalDatabaseAddressAtomically(t *testing.T) {
	repository := NewMemory()
	first, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "Payments",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create first cluster: %v", err)
	}

	duplicateNameID := model.NewResourceID()
	if _, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: duplicateNameID}, Engine: model.EngineMySQL, DisplayName: " payments ",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3306, Active: true}}); err == nil {
		t.Fatal("case-insensitive duplicate display name must fail")
	}
	if _, exists := repository.Cluster(duplicateNameID); exists || len(repository.Endpoints(duplicateNameID)) != 0 {
		t.Fatal("duplicate display name published partial inventory")
	}

	duplicateAddressID := model.NewResourceID()
	if _, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: duplicateAddressID}, Engine: model.EngineMySQL, DisplayName: "reporting",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, IPAddress: "192.0.2.10", Port: 3306, Active: true}}); err == nil {
		t.Fatal("database address already owned by another cluster must fail")
	}
	if _, exists := repository.Cluster(duplicateAddressID); exists || len(repository.Endpoints(duplicateAddressID)) != 0 {
		t.Fatal("duplicate global endpoint published partial inventory")
	}
	if clusters := repository.Clusters(); len(clusters) != 1 || clusters[0].ResourceID != first.ResourceID {
		t.Fatalf("failed registrations changed existing inventory: %+v", clusters)
	}
}

func TestClusterDisplayNameInvariantUsesTypedValidationAndConflictErrors(t *testing.T) {
	repository := NewMemory()
	alpha, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: " Alpha "})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if alpha.DisplayName != "Alpha" {
		t.Fatalf("display name was not trimmed: %q", alpha.DisplayName)
	}
	if _, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "   "}); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank upsert error = %v, want validation", err)
	}
	if _, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "   "}, nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank create error = %v, want validation", err)
	}
	if _, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: " alpha "}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error = %v, want conflict", err)
	}
	beta, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "Beta"})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	beta.DisplayName = "ALPHA"
	if _, err := repository.UpsertCluster(beta); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename collision error = %v, want conflict", err)
	}
	storedBeta, found := repository.Cluster(beta.ResourceID)
	if !found || storedBeta.DisplayName != "Beta" {
		t.Fatalf("rename collision changed existing cluster: %+v found=%t", storedBeta, found)
	}
}

func TestUpsertClusterPersistenceFailurePublishesNeitherCreateNorUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "before-rename",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 18, 0, 0, 0, time.UTC)
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	instance.Role = model.RolePrimary
	if _, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID:    cluster.ResourceID,
		Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}},
		ObservedAt:   observedAt,
	}); err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	beforeCluster, _ := repository.Cluster(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	repository.path = t.TempDir()

	cluster.DisplayName = "must-not-publish"
	if _, err := repository.UpsertCluster(cluster); err == nil {
		t.Fatal("rename must fail when persistence fails")
	}
	afterCluster, _ := repository.Cluster(cluster.ResourceID)
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !reflect.DeepEqual(afterCluster, beforeCluster) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) {
		t.Fatalf("failed rename published state: cluster=%+v topology=%+v", afterCluster, afterTopology)
	}

	newID := model.NewResourceID()
	if _, err := repository.UpsertCluster(model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: newID}, Engine: model.EngineMySQL, DisplayName: "must-not-create"}); err == nil {
		t.Fatal("create must fail when persistence fails")
	}
	if _, found := repository.Cluster(newID); found {
		t.Fatal("failed cluster create published in memory")
	}
}

func TestClusterEngineAndNativeIdentityAreImmutableAndGloballyUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	alpha, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "alpha"})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	alpha.EngineIdentity = model.EngineIdentity{"server_uuid": " AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE "}
	alpha, err = repository.UpsertCluster(alpha)
	if err != nil {
		t.Fatalf("establish identity: %v", err)
	}
	before := alpha

	changedEngine := alpha
	changedEngine.Engine = model.EnginePostgreSQL
	if _, err := repository.UpsertCluster(changedEngine); !errors.Is(err, ErrConflict) {
		t.Fatalf("engine change error = %v", err)
	}
	changedIdentity := alpha
	changedIdentity.EngineIdentity = model.EngineIdentity{"server_uuid": "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"}
	if _, err := repository.UpsertCluster(changedIdentity); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity change error = %v", err)
	}
	clearedIdentity := alpha
	clearedIdentity.EngineIdentity = nil
	if _, err := repository.UpsertCluster(clearedIdentity); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity clear error = %v", err)
	}
	if _, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "invalid", EngineIdentity: model.EngineIdentity{"wrong": "value"}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid identity error = %v", err)
	}
	duplicateID := model.NewResourceID()
	if _, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: duplicateID}, Engine: model.EngineMySQL, DisplayName: "duplicate", EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate identity error = %v", err)
	}
	if _, found := repository.Cluster(duplicateID); found {
		t.Fatal("duplicate identity published cluster")
	}
	stored, _ := repository.Cluster(alpha.ResourceID)
	if !reflect.DeepEqual(stored, before) {
		t.Fatalf("rejections changed established cluster: before=%+v after=%+v", before, stored)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	persisted, _ := reopened.Cluster(alpha.ResourceID)
	if !reflect.DeepEqual(persisted, before) {
		t.Fatalf("rejections changed durable cluster: before=%+v after=%+v", before, persisted)
	}
}

func TestUpsertClusterIdentityEstablishmentPersistenceFailureIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "alpha"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	repository.path = t.TempDir()
	candidate := cluster
	candidate.EngineIdentity = model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	if _, err := repository.UpsertCluster(candidate); err == nil {
		t.Fatal("identity persistence must fail")
	}
	stored, _ := repository.Cluster(cluster.ResourceID)
	if len(stored.EngineIdentity) != 0 || !reflect.DeepEqual(stored, cluster) {
		t.Fatalf("failed identity establishment changed live cluster: %+v", stored)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	persisted, _ := reopened.Cluster(cluster.ResourceID)
	if !reflect.DeepEqual(persisted, cluster) {
		t.Fatalf("failed identity establishment changed durable cluster: %+v", persisted)
	}
}

func TestUpsertEndpointRejectsInvalidUnknownAndDuplicateActiveAddresses(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.UpsertEndpoint(model.Endpoint{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}); err == nil {
		t.Fatal("empty cluster ID must fail")
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: model.NewResourceID(), Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}); err == nil {
		t.Fatal("unknown cluster ID must fail")
	}
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "endpoint-validation"}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 0, Active: true}); err == nil {
		t.Fatal("invalid endpoint port must fail")
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}); err != nil {
		t.Fatalf("upsert first endpoint: %v", err)
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, IPAddress: "MYSQL-A", Port: 3306, Active: true}); err == nil {
		t.Fatal("duplicate active endpoint address must fail")
	}
}

func TestActiveDatabaseEndpointRequiresTrimmedNonemptyAddressAtomically(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "addresses"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "   ", IPAddress: "\t", Port: 3306, Active: true}); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank active database address error = %v", err)
	}
	if len(repository.Endpoints(cluster.ResourceID)) != 0 {
		t.Fatalf("blank address published endpoint: %+v", repository.Endpoints(cluster.ResourceID))
	}
	endpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: " mysql-a ", IPAddress: " 192.0.2.10 ", Port: 3306, Active: true})
	if err != nil {
		t.Fatalf("create trimmed endpoint: %v", err)
	}
	if endpoint.Hostname != "mysql-a" || endpoint.IPAddress != "192.0.2.10" {
		t.Fatalf("endpoint coordinates were not trimmed: %+v", endpoint)
	}
}

func TestStoreMetricSamplesBoundsEachInstanceOrdersSamplesAndClonesValues(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metric-bounds"}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	firstInstanceID := model.NewResourceID()
	secondInstanceID := model.NewResourceID()
	observedAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	if err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{
		{InstanceID: firstInstanceID, ObservedAt: observedAt.Add(3 * time.Minute), Values: map[string]float64{"qps": 3}},
		{InstanceID: firstInstanceID, ObservedAt: observedAt.Add(1 * time.Minute), Values: map[string]float64{"qps": 1}},
		{InstanceID: firstInstanceID, ObservedAt: observedAt.Add(2 * time.Minute), Values: map[string]float64{"qps": 2}},
		{InstanceID: secondInstanceID, ObservedAt: observedAt, Values: map[string]float64{"qps": 4}},
	}, 2); err != nil {
		t.Fatalf("store metric samples: %v", err)
	}
	samples := repository.MetricSamples(cluster.ResourceID)
	if len(samples) != 3 {
		t.Fatalf("expected two samples for one instance and one for another, got %d", len(samples))
	}
	var firstInstanceSamples []model.MetricSample
	for _, sample := range samples {
		if sample.InstanceID == firstInstanceID {
			firstInstanceSamples = append(firstInstanceSamples, sample)
		}
	}
	if len(firstInstanceSamples) != 2 || firstInstanceSamples[0].Values["qps"] != 2 || firstInstanceSamples[1].Values["qps"] != 3 || !firstInstanceSamples[0].ObservedAt.Before(firstInstanceSamples[1].ObservedAt) {
		t.Fatalf("metric samples were not bounded and ordered by observation time: %+v", firstInstanceSamples)
	}
	samples[0].Values["qps"] = 999
	if repository.MetricSamples(cluster.ResourceID)[0].Values["qps"] == 999 {
		t.Fatal("metric sample values map leaked from repository")
	}
}

func TestRepositoryDoesNotPublishInventoryMutationsWhenPersistenceFails(t *testing.T) {
	newRepository := func(t *testing.T) (*Repository, model.DatabaseCluster) {
		t.Helper()
		repository := NewMemory()
		cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "persistence-" + t.Name()})
		if err != nil {
			t.Fatalf("create cluster: %v", err)
		}
		repository.path = t.TempDir()
		return repository, cluster
	}

	t.Run("endpoint", func(t *testing.T) {
		repository, cluster := newRepository(t)
		_, err := repository.UpsertEndpoint(model.Endpoint{
			ClusterID: cluster.ResourceID,
			Kind:      model.EndpointDatabase,
			Hostname:  "mysql-a",
			Port:      3306,
			Active:    true,
		})
		if err == nil {
			t.Fatal("endpoint persistence must fail when snapshot path is a directory")
		}
		if endpoints := repository.Endpoints(cluster.ResourceID); len(endpoints) != 0 {
			t.Fatalf("failed endpoint persistence mutated memory: %+v", endpoints)
		}
	})

	t.Run("replication links", func(t *testing.T) {
		repository, cluster := newRepository(t)
		err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{
			SourceInstanceID: model.NewResourceID(),
			TargetInstanceID: model.NewResourceID(),
		}})
		if err == nil {
			t.Fatal("replication link persistence must fail when snapshot path is a directory")
		}
		if links := repository.ReplicationLinks(cluster.ResourceID); len(links) != 0 {
			t.Fatalf("failed replication link persistence mutated memory: %+v", links)
		}
	})

	t.Run("metric samples", func(t *testing.T) {
		repository, cluster := newRepository(t)
		err := repository.StoreMetricSamples(cluster.ResourceID, []model.MetricSample{{
			InstanceID: model.NewResourceID(),
			ObservedAt: time.Now(),
			Values:     map[string]float64{"qps": 1},
		}}, 10)
		if err == nil {
			t.Fatal("metric persistence must fail when snapshot path is a directory")
		}
		if samples := repository.MetricSamples(cluster.ResourceID); len(samples) != 0 {
			t.Fatalf("failed metric persistence mutated memory: %+v", samples)
		}
	})
}

func TestReplicationLinksAndMetricSamplesRejectUnknownClusters(t *testing.T) {
	repository := NewMemory()
	unknownClusterID := model.NewResourceID()
	if err := repository.ReplaceReplicationLinks(unknownClusterID, nil); err == nil {
		t.Fatal("replication links for an unknown cluster must fail")
	}
	if err := repository.StoreMetricSamples(unknownClusterID, nil, 10); err == nil {
		t.Fatal("metric samples for an unknown cluster must fail")
	}
}

func TestReplaceReplicationLinksPreservesEdgeIdentityOnRefresh(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "link-identity"}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	sourceInstanceID := model.NewResourceID()
	targetInstanceID := model.NewResourceID()
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{
		SourceInstanceID: sourceInstanceID,
		TargetInstanceID: targetInstanceID,
		Healthy:          true,
	}}); err != nil {
		t.Fatalf("store initial link: %v", err)
	}
	initial := repository.ReplicationLinks(cluster.ResourceID)
	if len(initial) != 1 || initial[0].ResourceID == "" || initial[0].MetadataRevision != 1 {
		t.Fatalf("new edge was not assigned one initialized resource: %+v", initial)
	}
	if err := repository.ReplaceReplicationLinks(cluster.ResourceID, []model.ReplicationLink{{
		SourceInstanceID: sourceInstanceID,
		TargetInstanceID: targetInstanceID,
		Healthy:          false,
	}}); err != nil {
		t.Fatalf("refresh link: %v", err)
	}
	refreshed := repository.ReplicationLinks(cluster.ResourceID)
	if len(refreshed) != 1 || refreshed[0].ResourceID != initial[0].ResourceID || !refreshed[0].CreatedAt.Equal(initial[0].CreatedAt) || refreshed[0].MetadataRevision != initial[0].MetadataRevision+1 || refreshed[0].Healthy {
		t.Fatalf("link refresh did not preserve edge identity and revision: initial=%+v refreshed=%+v", initial, refreshed)
	}
}

func TestReconcileInstancePersistsLatestReplicationAndDiscoveryMetadata(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	initialLag := int64(2)
	initial := mysqlInstance(clusterID, "mysql-b", "192.0.2.20", 3306)
	initial.Replication = model.ReplicationStatus{
		SourceIdentity:    model.EngineIdentity{"server_uuid": "primary-v1"},
		IOThread:          model.ThreadRunning,
		SQLThread:         model.ThreadRunning,
		LagSeconds:        &initialLag,
		RetrievedPosition: "gtid:1-10",
		ExecutedPosition:  "gtid:1-9",
	}
	initial.EngineMetadata = map[string]string{"version": "8.0.35", "gtid_mode": "ON"}
	initial.Maintenance = false
	initial.PromotionEligible = true
	first, err := repository.ReconcileInstance(initial)
	if err != nil {
		t.Fatalf("initial reconciliation: %v", err)
	}

	latestLag := int64(17)
	latest := mysqlInstance(clusterID, "mysql-b", "192.0.2.20", 3306)
	latest.Replication = model.ReplicationStatus{
		SourceIdentity:    model.EngineIdentity{"server_uuid": "primary-v2"},
		IOThread:          model.ThreadStopped,
		SQLThread:         model.ThreadStopped,
		LagSeconds:        &latestLag,
		RetrievedPosition: "gtid:1-20",
		ExecutedPosition:  "gtid:1-11",
		LastError:         "relay log read failure",
	}
	latest.EngineMetadata = map[string]string{"version": "8.0.36", "gtid_mode": "OFF"}
	latest.Maintenance = true
	latest.PromotionEligible = false
	second, err := repository.ReconcileInstance(latest)
	if err != nil {
		t.Fatalf("latest reconciliation: %v", err)
	}

	if second.Instance.ResourceID != first.Instance.ResourceID || second.Instance.Replication.LagSeconds == nil || *second.Instance.Replication.LagSeconds != latestLag {
		t.Fatalf("replication refresh did not preserve identity and lag: first=%+v second=%+v", first.Instance, second.Instance)
	}
	if second.Instance.Replication.SourceIdentity["server_uuid"] != "primary-v2" || second.Instance.Replication.IOThread != model.ThreadStopped || second.Instance.Replication.SQLThread != model.ThreadStopped || second.Instance.Replication.RetrievedPosition != "gtid:1-20" || second.Instance.Replication.ExecutedPosition != "gtid:1-11" || second.Instance.Replication.LastError == "" {
		t.Fatalf("latest replication state was not persisted: %+v", second.Instance.Replication)
	}
	if second.Instance.EngineMetadata["version"] != "8.0.36" || second.Instance.EngineMetadata["gtid_mode"] != "OFF" || !second.Instance.Maintenance || second.Instance.PromotionEligible {
		t.Fatalf("latest discovery metadata was not persisted: %+v", second.Instance)
	}
	stored := repository.Instances(clusterID)
	if len(stored) != 1 || stored[0].Replication.LagSeconds == nil || *stored[0].Replication.LagSeconds != latestLag || stored[0].EngineMetadata["version"] != "8.0.36" || !stored[0].Maintenance || stored[0].PromotionEligible {
		t.Fatalf("repository did not retain latest discovery fields: %+v", stored)
	}
}

func TestReconcileInstancePersistenceFailureDoesNotMutateLiveInstance(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	initialLag := int64(2)
	initial := mysqlInstance(clusterID, "mysql-b", "192.0.2.20", 3306)
	initial.Replication = model.ReplicationStatus{IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &initialLag}
	initial.EngineMetadata = map[string]string{"version": "8.0.35"}
	initial.PromotionEligible = true
	first, err := repository.ReconcileInstance(initial)
	if err != nil {
		t.Fatalf("initial reconciliation: %v", err)
	}

	repository.path = t.TempDir()
	failedLag := int64(99)
	changed := mysqlInstance(clusterID, "mysql-renamed", "192.0.2.99", 3310)
	changed.Replication = model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadStopped, LagSeconds: &failedLag, LastError: "failed refresh"}
	changed.EngineMetadata = map[string]string{"version": "9.9.99"}
	changed.Maintenance = true
	changed.PromotionEligible = false
	if _, err := repository.ReconcileInstance(changed); err == nil {
		t.Fatal("reconciliation must fail when the snapshot path is a directory")
	}

	stored := repository.Instances(clusterID)
	if len(stored) != 1 {
		t.Fatalf("failed reconciliation changed instance count: %+v", stored)
	}
	got := stored[0]
	if got.ResourceID != first.Instance.ResourceID || got.Hostname != initial.Hostname || got.Port != initial.Port || got.MetadataRevision != first.Instance.MetadataRevision {
		t.Fatalf("failed persistence published endpoint changes: before=%+v after=%+v", first.Instance, got)
	}
	if got.Replication.LagSeconds == nil || *got.Replication.LagSeconds != initialLag || got.Replication.IOThread != model.ThreadRunning || got.EngineMetadata["version"] != "8.0.35" || got.Maintenance || !got.PromotionEligible {
		t.Fatalf("failed persistence published discovery state: before=%+v after=%+v", first.Instance, got)
	}
}

func TestReplaceClusterAnomaliesIsClusterScopedAndAtomic(t *testing.T) {
	repository := NewMemory()
	firstCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "first"})
	if err != nil {
		t.Fatalf("create first cluster: %v", err)
	}
	secondCluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "second"})
	if err != nil {
		t.Fatalf("create second cluster: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(firstCluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "old-first", Severity: "warning"}}); err != nil {
		t.Fatalf("seed first anomalies: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(secondCluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "second", Severity: "warning"}}); err != nil {
		t.Fatalf("seed second anomalies: %v", err)
	}
	if err := repository.ReplaceClusterAnomalies(firstCluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "new-first", Severity: "critical"}}); err != nil {
		t.Fatalf("replace first anomalies: %v", err)
	}

	firstAnomalies := anomaliesForCluster(repository.Anomalies(), firstCluster.ResourceID)
	secondAnomalies := anomaliesForCluster(repository.Anomalies(), secondCluster.ResourceID)
	if len(firstAnomalies) != 1 || firstAnomalies[0].Kind != "new-first" || firstAnomalies[0].ClusterID != firstCluster.ResourceID {
		t.Fatalf("first cluster anomalies were not replaced: %+v", firstAnomalies)
	}
	if len(secondAnomalies) != 1 || secondAnomalies[0].Kind != "second" {
		t.Fatalf("second cluster anomalies were not retained: %+v", secondAnomalies)
	}

	repository.path = t.TempDir()
	if err := repository.ReplaceClusterAnomalies(firstCluster.ResourceID, []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "must-not-publish", Severity: "critical"}}); err == nil {
		t.Fatal("anomaly replacement must fail when the snapshot path is a directory")
	}
	afterFailure := anomaliesForCluster(repository.Anomalies(), firstCluster.ResourceID)
	if len(afterFailure) != 1 || afterFailure[0].Kind != "new-first" {
		t.Fatalf("failed anomaly persistence mutated live state: %+v", afterFailure)
	}
	if retained := anomaliesForCluster(repository.Anomalies(), secondCluster.ResourceID); len(retained) != 1 || retained[0].Kind != "second" {
		t.Fatalf("failed anomaly persistence changed another cluster: %+v", retained)
	}
}

func TestApplyDiscoveryRefreshDeduplicatesIdentityAndPublishesTopology(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "transaction"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	first, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true})
	if err != nil {
		t.Fatalf("create first endpoint: %v", err)
	}
	alias, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a.internal", Port: 3307, Active: true})
	if err != nil {
		t.Fatalf("create alias endpoint: %v", err)
	}
	replicaEndpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3308, Active: true})
	if err != nil {
		t.Fatalf("create replica endpoint: %v", err)
	}
	primary := mysqlInstance(cluster.ResourceID, first.Hostname, "", first.Port)
	primary.EngineIdentity["server_uuid"] = "primary-native"
	primary.Role = model.RolePrimary
	aliasPrimary := primary
	aliasPrimary.Hostname = alias.Hostname
	aliasPrimary.Port = alias.Port
	replica := mysqlInstance(cluster.ResourceID, replicaEndpoint.Hostname, "", replicaEndpoint.Port)
	replica.EngineIdentity["server_uuid"] = "replica-native"
	replica.Replication = model.ReplicationStatus{
		SourceIdentity: model.EngineIdentity{"server_uuid": "primary-native"},
		IOThread:       model.ThreadRunning,
		SQLThread:      model.ThreadRunning,
	}
	samples := make([]model.MetricSample, 61)
	for index := range samples {
		samples[index] = model.MetricSample{ObservedAt: time.Unix(int64(index), 0).UTC(), Values: map[string]float64{"qps": float64(index)}}
	}

	snapshot, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []DiscoveryObservation{
			{EndpointID: first.ResourceID, Instance: primary, Metrics: samples},
			{EndpointID: alias.ResourceID, Instance: aliasPrimary},
			{EndpointID: replicaEndpoint.ResourceID, Instance: replica, Metrics: []model.MetricSample{{ObservedAt: time.Unix(100, 0).UTC(), Values: map[string]float64{"qps": 1}}}},
		},
		Anomalies: []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "discovery", Severity: "warning"}},
	})
	if err != nil {
		t.Fatalf("apply discovery refresh: %v", err)
	}
	if len(snapshot.Instances) != 2 || len(snapshot.Probes) != 3 || len(snapshot.Links) != 1 || len(snapshot.Anomalies) != 1 {
		t.Fatalf("unexpected transactional topology: %+v", snapshot)
	}
	probes := make(map[model.ResourceID]model.ProbeStatus, len(snapshot.Probes))
	for _, probe := range snapshot.Probes {
		probes[probe.EndpointID] = probe
	}
	if probes[first.ResourceID].InstanceID == "" || probes[first.ResourceID].InstanceID != probes[alias.ResourceID].InstanceID || probes[replicaEndpoint.ResourceID].InstanceID == probes[first.ResourceID].InstanceID {
		t.Fatalf("transaction did not deduplicate endpoint bindings: %+v", snapshot.Probes)
	}
	if snapshot.Links[0].SourceInstanceID != probes[first.ResourceID].InstanceID || snapshot.Links[0].TargetInstanceID != probes[replicaEndpoint.ResourceID].InstanceID {
		t.Fatalf("transaction did not resolve replication link: %+v", snapshot.Links)
	}
	bound := repository.Endpoints(cluster.ResourceID)
	bindings := make(map[model.ResourceID]model.ResourceID, len(bound))
	for _, endpoint := range bound {
		bindings[endpoint.ResourceID] = endpoint.InstanceID
	}
	if bindings[first.ResourceID] != bindings[alias.ResourceID] || bindings[first.ResourceID] == "" || bindings[replicaEndpoint.ResourceID] == "" {
		t.Fatalf("transaction did not publish endpoint bindings: %+v", bound)
	}
	storedSamples := repository.MetricSamples(cluster.ResourceID)
	if len(storedSamples) != 2 {
		t.Fatalf("current-cycle transaction metrics = %d, want one per resolved instance", len(storedSamples))
	}
	counts := map[model.ResourceID]int{}
	for _, sample := range storedSamples {
		if sample.InstanceID == "" {
			t.Fatalf("transaction stored unbound metric sample: %+v", sample)
		}
		counts[sample.InstanceID]++
	}
	if counts[probes[first.ResourceID].InstanceID] != 1 || counts[probes[replicaEndpoint.ResourceID].InstanceID] != 1 {
		t.Fatalf("transaction metric deduplication by instance = %+v", counts)
	}
}

func TestApplyDiscoveryRefreshPersistenceFailureRollsBackAllRefreshState(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "rollback"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	primaryEndpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true})
	if err != nil {
		t.Fatalf("create primary endpoint: %v", err)
	}
	replicaEndpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3307, Active: true})
	if err != nil {
		t.Fatalf("create replica endpoint: %v", err)
	}
	newEndpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-c", Port: 3308, Active: true})
	if err != nil {
		t.Fatalf("create future endpoint: %v", err)
	}
	primary := mysqlInstance(cluster.ResourceID, primaryEndpoint.Hostname, "", primaryEndpoint.Port)
	primary.EngineIdentity["server_uuid"] = "primary-native"
	primary.Role = model.RolePrimary
	replica := mysqlInstance(cluster.ResourceID, replicaEndpoint.Hostname, "", replicaEndpoint.Port)
	replica.EngineIdentity["server_uuid"] = "replica-native"
	replica.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "primary-native"}
	replica.Replication.IOThread = model.ThreadRunning
	replica.Replication.SQLThread = model.ThreadRunning
	if _, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []DiscoveryObservation{
			{EndpointID: primaryEndpoint.ResourceID, Instance: primary, Metrics: []model.MetricSample{{ObservedAt: time.Unix(1, 0).UTC(), Values: map[string]float64{"qps": 1}}}},
			{EndpointID: replicaEndpoint.ResourceID, Instance: replica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: primaryEndpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}},
			{EndpointID: replicaEndpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}},
			{EndpointID: newEndpoint.ResourceID, Health: model.Health{State: model.HealthUnknown}},
		},
		Health:     model.Health{State: model.HealthDegraded, Summary: "before"},
		ObservedAt: time.Unix(1, 0).UTC(),
		Anomalies:  []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "before", Severity: "warning"}},
	}); err != nil {
		t.Fatalf("seed discovery refresh: %v", err)
	}
	before := captureStoreDiscoveryState(repository, cluster.ResourceID)

	repository.path = t.TempDir()
	changedPrimary := primary
	changedPrimary.EngineMetadata = map[string]string{"version": "changed"}
	changedPrimary.Maintenance = true
	newReplica := mysqlInstance(cluster.ResourceID, newEndpoint.Hostname, "", newEndpoint.Port)
	newReplica.EngineIdentity["server_uuid"] = "new-replica-native"
	newReplica.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "primary-native"}
	newReplica.Replication.IOThread = model.ThreadRunning
	newReplica.Replication.SQLThread = model.ThreadRunning
	_, err = repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []DiscoveryObservation{
			{EndpointID: primaryEndpoint.ResourceID, Instance: changedPrimary, Metrics: []model.MetricSample{{ObservedAt: time.Unix(2, 0).UTC(), Values: map[string]float64{"qps": 2}}}},
			{EndpointID: newEndpoint.ResourceID, Instance: newReplica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: primaryEndpoint.ResourceID, Health: model.Health{State: model.HealthDegraded}},
			{EndpointID: replicaEndpoint.ResourceID, Health: model.Health{State: model.HealthUnknown}},
			{EndpointID: newEndpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}},
		},
		Health:     model.Health{State: model.HealthUnhealthy, Summary: "must-not-publish"},
		ObservedAt: time.Unix(2, 0).UTC(),
		Anomalies:  []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: "after", Severity: "critical"}},
	})
	if err == nil {
		t.Fatal("discovery refresh must fail when snapshot path is a directory")
	}
	after := captureStoreDiscoveryState(repository, cluster.ResourceID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed discovery transaction published partial state:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestDiscoverySnapshotPersistsCompleteInventoryTopologyAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "durable-topology",
	}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-never", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	primary := mysqlInstance(cluster.ResourceID, endpoints[0].Hostname, "", endpoints[0].Port)
	primary.EngineIdentity["server_uuid"] = "primary-native"
	primary.Role = model.RolePrimary
	replica := mysqlInstance(cluster.ResourceID, endpoints[1].Hostname, "", endpoints[1].Port)
	replica.EngineIdentity["server_uuid"] = "replica-native"
	replica.Replication = model.ReplicationStatus{
		SourceIdentity: model.EngineIdentity{"server_uuid": "primary-native"},
		IOThread:       model.ThreadRunning, SQLThread: model.ThreadRunning,
	}
	firstObservedAt := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	first, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []DiscoveryObservation{
			{EndpointID: endpoints[0].ResourceID, Instance: primary},
			{EndpointID: endpoints[1].ResourceID, Instance: replica},
		},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy, ObservedAt: firstObservedAt}},
			{EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthHealthy, ObservedAt: firstObservedAt}},
			{EndpointID: endpoints[2].ResourceID, Health: model.Health{State: model.HealthUnknown, ObservedAt: firstObservedAt}},
		},
		Health:     model.Health{State: model.HealthDegraded, Summary: "one endpoint unavailable", ObservedAt: firstObservedAt},
		ObservedAt: firstObservedAt,
	})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	if len(first.Links) != 1 {
		t.Fatalf("seed topology link count = %d, want 1", len(first.Links))
	}
	firstProbes := probeStatusesByEndpoint(first.Probes)
	replicaID := firstProbes[endpoints[1].ResourceID].InstanceID
	linkID := first.Links[0].ResourceID

	secondObservedAt := firstObservedAt.Add(time.Minute)
	second, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Observations: []DiscoveryObservation{{
			EndpointID: endpoints[0].ResourceID,
			Instance:   primary,
			Metrics:    []model.MetricSample{{ObservedAt: secondObservedAt, Values: map[string]float64{"connections": 7}}},
		}},
		Probes: []model.ProbeStatus{
			{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy, ObservedAt: secondObservedAt}},
			{EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthUnknown, Summary: "database probe unavailable", ObservedAt: secondObservedAt}},
			{EndpointID: endpoints[2].ResourceID, Health: model.Health{State: model.HealthUnknown, Summary: "database probe unavailable", ObservedAt: secondObservedAt}},
		},
		Health:     model.Health{State: model.HealthDegraded, Summary: "database probe unavailable", ObservedAt: secondObservedAt},
		ObservedAt: secondObservedAt,
	})
	if err != nil {
		t.Fatalf("publish failed-member topology: %v", err)
	}
	if len(second.Instances) != 2 {
		t.Fatalf("failed bound member disappeared from topology: %+v", second.Instances)
	}
	instances := instancesByID(second.Instances)
	if instances[replicaID].Health.State != model.HealthUnknown || instances[replicaID].Health.ObservedAt != secondObservedAt {
		t.Fatalf("failed member exposed stale health: %+v", instances[replicaID].Health)
	}
	secondProbes := probeStatusesByEndpoint(second.Probes)
	if secondProbes[endpoints[1].ResourceID].InstanceID != replicaID {
		t.Fatalf("failed bound endpoint lost stable instance UUID: %+v", secondProbes[endpoints[1].ResourceID])
	}
	if secondProbes[endpoints[2].ResourceID].InstanceID != "" {
		t.Fatalf("never-successful endpoint invented instance UUID: %+v", secondProbes[endpoints[2].ResourceID])
	}
	if len(second.Links) != 1 || second.Links[0].ResourceID != linkID || second.Links[0].Healthy {
		t.Fatalf("last-known relation was not retained as unhealthy: %+v", second.Links)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	persisted, found := reopened.TopologySnapshot(cluster.ResourceID)
	if !found {
		t.Fatal("reopened repository lost topology snapshot")
	}
	if !reflect.DeepEqual(persisted, second) {
		t.Fatalf("topology snapshot changed across restart:\nwant=%+v\ngot=%+v", second, persisted)
	}
	if samples := reopened.MetricSamples(cluster.ResourceID); len(samples) != 1 || samples[0].InstanceID == "" {
		t.Fatalf("topology metrics did not persist atomically: %+v", samples)
	}
}

func TestApplyDiscoveryRefreshDerivesEvidenceFromSuccessfulObservations(t *testing.T) {
	repository := NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "derived-evidence",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 15, 30, 0, 0, time.UTC)
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	instance.Role = model.RolePrimary
	first, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID:    cluster.ResourceID,
		Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
		Probes: []model.ProbeStatus{{
			EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt.Add(-time.Hour), MetricsObservedAt: observedAt.Add(-time.Hour),
			Health: model.Health{State: model.HealthHealthy},
		}},
		ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatalf("publish discovery-only observation: %v", err)
	}
	if first.Probes[0].DiscoveryObservedAt != observedAt || !first.Probes[0].MetricsObservedAt.IsZero() {
		t.Fatalf("repository trusted supplied evidence instead of observations: %+v", first.Probes[0])
	}

	secondObservedAt := observedAt.Add(time.Minute)
	second, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID,
		Probes: []model.ProbeStatus{{
			EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: secondObservedAt, MetricsObservedAt: secondObservedAt,
			Health: model.Health{State: model.HealthUnknown},
		}},
		ObservedAt: secondObservedAt,
	})
	if err != nil {
		t.Fatalf("publish failed observation: %v", err)
	}
	if !second.Probes[0].DiscoveryObservedAt.IsZero() || !second.Probes[0].MetricsObservedAt.IsZero() {
		t.Fatalf("failed observation retained forged evidence: %+v", second.Probes[0])
	}
}

func TestApplyDiscoveryRefreshRejectsEqualAndOlderObservationsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "stale-ordering",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	baseTime := time.Date(2026, time.July, 12, 9, 0, 0, 0, time.UTC)
	primary := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	primary.Role = model.RolePrimary
	refresh := func(observedAt time.Time, version string) (model.TopologySnapshot, error) {
		candidate := primary
		candidate.EngineMetadata = map[string]string{"version": version}
		return repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
			ClusterID: cluster.ResourceID,
			Observations: []DiscoveryObservation{{
				EndpointID: endpoints[0].ResourceID, Instance: candidate,
				Metrics: []model.MetricSample{{ObservedAt: observedAt, Values: map[string]float64{"questions_total": float64(len(version))}}},
			}},
			Probes:     []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
			ObservedAt: observedAt,
			Anomalies:  []model.MetadataAnomaly{{Engine: model.EngineMySQL, Kind: version, Severity: "warning"}},
		})
	}
	first, err := refresh(baseTime, "first")
	if err != nil {
		t.Fatalf("publish first observation: %v", err)
	}
	before := captureStoreDiscoveryState(repository, cluster.ResourceID)
	for _, observedAt := range []time.Time{baseTime, baseTime.Add(-time.Second)} {
		if _, err := refresh(observedAt, "stale"); !errors.Is(err, ErrStaleObservation) {
			t.Fatalf("stale observation %s error = %v", observedAt, err)
		}
		after := captureStoreDiscoveryState(repository, cluster.ResourceID)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("stale observation mutated repository:\nbefore=%+v\nafter=%+v", before, after)
		}
	}

	laterTime := baseTime.Add(time.Second)
	later, err := refresh(laterTime, "later")
	if err != nil {
		t.Fatalf("publish later observation: %v", err)
	}
	if !later.ObservedAt.Equal(laterTime) || reflect.DeepEqual(later, first) {
		t.Fatalf("later observation was not published: %+v", later)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	persisted, found := reopened.TopologySnapshot(cluster.ResourceID)
	if !found || !persisted.ObservedAt.Equal(laterTime) {
		t.Fatalf("later observation did not survive reopen: %+v found=%t", persisted, found)
	}
}

func TestDiscoveryWatermarkAndInventoryGenerationSurviveInvalidationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "ordering"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	inventory, found := repository.DiscoveryInventory(cluster.ResourceID)
	if !found || inventory.Generation == 0 {
		t.Fatalf("initial discovery inventory = %+v found=%t", inventory, found)
	}
	observedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	instance := mysqlInstance(cluster.ResourceID, "mysql-old", "", 3306)
	instance.Role = model.RolePrimary
	apply := func(at time.Time, generation uint64, hostname string) (model.TopologySnapshot, error) {
		candidate := instance
		candidate.Hostname = hostname
		return repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
			ClusterID: cluster.ResourceID, InventoryGeneration: generation, ObservedAt: at,
			Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: candidate}},
			Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		})
	}
	if _, err := apply(observedAt, inventory.Generation, "mysql-old"); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	changed := repository.Endpoints(cluster.ResourceID)[0]
	changed.Hostname = "mysql-new"
	if _, err := repository.UpsertEndpoint(changed); err != nil {
		t.Fatalf("invalidate inventory: %v", err)
	}
	current, _ := repository.DiscoveryInventory(cluster.ResourceID)
	if current.Generation <= inventory.Generation {
		t.Fatalf("inventory generation did not advance: old=%d current=%d", inventory.Generation, current.Generation)
	}
	before := captureStoreDiscoveryState(repository, cluster.ResourceID)
	if _, err := apply(observedAt, current.Generation, "mysql-new"); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("equal observation after invalidation error = %v", err)
	}
	if _, err := apply(observedAt.Add(time.Minute), inventory.Generation, "mysql-old"); !errors.Is(err, ErrInventoryChanged) {
		t.Fatalf("old inventory generation error = %v", err)
	}
	if after := captureStoreDiscoveryState(repository, cluster.ResourceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected refresh changed state: before=%+v after=%+v", before, after)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	persistedInventory, _ := reopened.DiscoveryInventory(cluster.ResourceID)
	if persistedInventory.Generation != current.Generation {
		t.Fatalf("inventory generation not durable: got=%d want=%d", persistedInventory.Generation, current.Generation)
	}
	if watermark, found := reopened.ObservationWatermark(cluster.ResourceID); !found || !watermark.Equal(observedAt) {
		t.Fatalf("observation watermark not durable: %s found=%t", watermark, found)
	}
	repository = reopened
	accepted, err := apply(observedAt.Add(2*time.Minute), current.Generation, "mysql-new")
	if err != nil || accepted.Instances[0].Hostname != "mysql-new" {
		t.Fatalf("current inventory refresh not accepted: snapshot=%+v err=%v", accepted, err)
	}
}

func TestApplyDiscoveryRefreshDeduplicatesAliasMetricsPerResolvedInstance(t *testing.T) {
	repository := NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "aliases"}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-alias", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	selectedEndpoint := endpoints[0].ResourceID
	if endpoints[1].ResourceID < selectedEndpoint {
		selectedEndpoint = endpoints[1].ResourceID
	}
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	complete := func(questions float64) map[string]float64 {
		return map[string]float64{"questions_total": questions, "transactions_total": questions, "slow_queries_total": questions, "connections": 10, "running_threads": 2, "buffer_pool_hit_ratio": .99}
	}
	start := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	refresh := func(observedAt time.Time, lowerValue float64, higherValue float64) model.TopologySnapshot {
		t.Helper()
		valuesByEndpoint := map[model.ResourceID]float64{endpoints[0].ResourceID: higherValue, endpoints[1].ResourceID: higherValue}
		valuesByEndpoint[selectedEndpoint] = lowerValue
		snapshot, refreshErr := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
			ClusterID: cluster.ResourceID, ObservedAt: observedAt,
			Observations: []DiscoveryObservation{
				{EndpointID: endpoints[0].ResourceID, Instance: instance, Metrics: []model.MetricSample{{ObservedAt: observedAt, Values: complete(valuesByEndpoint[endpoints[0].ResourceID])}}},
				{EndpointID: endpoints[1].ResourceID, Instance: instance, Metrics: []model.MetricSample{{ObservedAt: observedAt, Values: complete(valuesByEndpoint[endpoints[1].ResourceID])}}},
			},
			Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}, {EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		})
		if refreshErr != nil {
			t.Fatalf("refresh: %v", refreshErr)
		}
		return snapshot
	}
	first := refresh(start, 100, 900)
	second := refresh(start.Add(10*time.Second), 150, 950)
	samples := repository.MetricSamples(cluster.ResourceID)
	if len(samples) != 2 || samples[0].Values["questions_total"] != 100 || samples[1].Values["questions_total"] != 150 {
		t.Fatalf("alias samples were not deduplicated across cycles: %+v", samples)
	}
	derived := metricsservice.NewService().Derive(samples)[samples[0].InstanceID]
	if derived.Values["qps"] != 5 {
		t.Fatalf("alias rate compared within one refresh instead of across refreshes: %+v", derived)
	}
	for _, snapshot := range []model.TopologySnapshot{first, second} {
		for _, probe := range snapshot.Probes {
			if probe.EndpointID == selectedEndpoint && probe.MetricsObservedAt.IsZero() {
				t.Fatalf("selected alias lacks metrics evidence: %+v", snapshot.Probes)
			}
			if probe.EndpointID != selectedEndpoint && !probe.MetricsObservedAt.IsZero() {
				t.Fatalf("non-selected alias has ambiguous metrics evidence: %+v", snapshot.Probes)
			}
		}
	}
}

func TestTopologySnapshotOverlaysCanonicalCoordinatesButPreservesObservedRuntimeFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "coordinate-overlay",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 16, 0, 0, 0, time.UTC)
	observed := mysqlInstance(cluster.ResourceID, "mysql-old", "192.0.2.10", 3306)
	observed.Role = model.RolePrimary
	observed.Health = model.Health{State: model.HealthHealthy, ObservedAt: observedAt}
	observed.PromotionEligible = true
	first, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID:    cluster.ResourceID,
		Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}},
		Probes: []model.ProbeStatus{{
			EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt,
			Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt},
		}},
		ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatalf("publish topology: %v", err)
	}
	instanceID := first.Instances[0].ResourceID
	metadata := observed
	metadata.ResourceID = instanceID
	metadata.DisplayName = "mysql-renamed"
	metadata.Hostname = "mysql-renamed"
	metadata.IPAddress = "192.0.2.99"
	metadata.Port = 4406
	metadata.Aliases = []string{"mysql-writer"}
	metadata.NodeID = model.NewResourceID()
	metadata.Role = model.RoleReplica
	metadata.Health = model.Health{State: model.HealthUnhealthy}
	metadata.Replication = model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadStopped}
	metadata.PromotionEligible = false
	if _, err := repository.ReconcileInstance(metadata); err != nil {
		t.Fatalf("reconcile metadata coordinates: %v", err)
	}

	assertTopology := func(t *testing.T, repository *Repository) {
		t.Helper()
		topology, found := repository.TopologySnapshot(cluster.ResourceID)
		if !found || len(topology.Instances) != 1 {
			t.Fatalf("topology missing after metadata reconciliation: %+v found=%t", topology, found)
		}
		instance := topology.Instances[0]
		if instance.ResourceID != instanceID || instance.EngineIdentity["server_uuid"] != observed.EngineIdentity["server_uuid"] {
			t.Fatalf("stable identity changed: %+v", instance)
		}
		if instance.DisplayName != metadata.DisplayName || instance.Hostname != metadata.Hostname || instance.IPAddress != metadata.IPAddress || instance.Port != metadata.Port || !contains(instance.Aliases, "mysql-writer") || instance.NodeID != metadata.NodeID {
			t.Fatalf("canonical coordinates were not overlaid: %+v", instance)
		}
		if instance.Role != model.RolePrimary || instance.Health.State != model.HealthHealthy || instance.Replication.IOThread != observed.Replication.IOThread || instance.Replication.SQLThread != observed.Replication.SQLThread || !instance.PromotionEligible {
			t.Fatalf("metadata payload replaced observed runtime facts: %+v", instance)
		}
	}
	assertTopology(t, repository)
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	assertTopology(t, reopened)
}

func TestActiveInventoryMutationInvalidatesPersistedTopologyAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Repository, model.DatabaseCluster, model.Endpoint)
		valid  bool
	}{
		{
			name: "add active endpoint",
			mutate: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, _ model.Endpoint) {
				if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3307, Active: true}); err != nil {
					t.Fatalf("add active endpoint: %v", err)
				}
			},
		},
		{
			name: "activate endpoint",
			mutate: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, _ model.Endpoint) {
				endpoint, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-b", Port: 3307, Active: false})
				if err != nil {
					t.Fatalf("add inactive endpoint: %v", err)
				}
				endpoint.Active = true
				if _, err := repository.UpsertEndpoint(endpoint); err != nil {
					t.Fatalf("activate endpoint: %v", err)
				}
			},
		},
		{
			name: "deactivate endpoint",
			mutate: func(t *testing.T, repository *Repository, _ model.DatabaseCluster, endpoint model.Endpoint) {
				endpoint.Active = false
				if _, err := repository.UpsertEndpoint(endpoint); err != nil {
					t.Fatalf("deactivate endpoint: %v", err)
				}
			},
		},
		{
			name: "change active address",
			mutate: func(t *testing.T, repository *Repository, _ model.DatabaseCluster, endpoint model.Endpoint) {
				endpoint.Hostname = "mysql-renamed"
				endpoint.IPAddress = "192.0.2.44"
				endpoint.Port = 4406
				if _, err := repository.UpsertEndpoint(endpoint); err != nil {
					t.Fatalf("change endpoint address: %v", err)
				}
			},
		},
		{
			name: "add inactive endpoint",
			mutate: func(t *testing.T, repository *Repository, cluster model.DatabaseCluster, _ model.Endpoint) {
				if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: cluster.ResourceID, Kind: model.EndpointDatabase, Hostname: "mysql-idle", Port: 3310, Active: false}); err != nil {
					t.Fatalf("add inactive endpoint: %v", err)
				}
			},
			valid: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata.json")
			repository, err := Open(path)
			if err != nil {
				t.Fatalf("open repository: %v", err)
			}
			cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "inventory-" + strings.ReplaceAll(test.name, " ", "-")}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
			if err != nil {
				t.Fatalf("create inventory: %v", err)
			}
			observedAt := time.Date(2026, time.July, 11, 17, 0, 0, 0, time.UTC)
			instance := mysqlInstance(cluster.ResourceID, endpoints[0].Hostname, endpoints[0].IPAddress, endpoints[0].Port)
			instance.Role = model.RolePrimary
			if _, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
				ClusterID:    cluster.ResourceID,
				Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
				Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}},
				ObservedAt:   observedAt,
			}); err != nil {
				t.Fatalf("seed topology: %v", err)
			}
			test.mutate(t, repository, cluster, endpoints[0])
			_, found := repository.TopologySnapshot(cluster.ResourceID)
			if found != test.valid {
				t.Fatalf("topology validity after mutation = %t, want %t", found, test.valid)
			}
			storedCluster, _ := repository.Cluster(cluster.ResourceID)
			if !test.valid && storedCluster.Health.State != model.HealthUnknown {
				t.Fatalf("invalidated inventory retained cluster health: %+v", storedCluster.Health)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("reopen repository: %v", err)
			}
			_, reopenedFound := reopened.TopologySnapshot(cluster.ResourceID)
			if reopenedFound != test.valid {
				t.Fatalf("reopened topology validity = %t, want %t", reopenedFound, test.valid)
			}
		})
	}
}

func TestReconcileMetadataCoordinatesUpdatesBoundEndpointAndPreservesRuntimeFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	lag := int64(7)
	observed := mysqlInstance(cluster.ResourceID, "mysql-old", "192.0.2.10", 3306)
	observed.Role = model.RolePrimary
	observed.Health = model.Health{State: model.HealthHealthy}
	observed.Replication = model.ReplicationStatus{IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning, LagSeconds: &lag}
	observed.Maintenance = true
	observed.PromotionEligible = true
	observed.EngineMetadata = map[string]string{"version": "8.4"}
	snapshot, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{ClusterID: cluster.ResourceID, ObservedAt: time.Now().UTC(), Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("publish topology: %v", err)
	}
	instanceID := snapshot.Instances[0].ResourceID
	nodeID := model.NewResourceID()
	malicious := snapshot.Instances[0]
	malicious.DisplayName = "mysql-new"
	malicious.Hostname = "mysql-new"
	malicious.IPAddress = "192.0.2.20"
	malicious.Port = 4406
	malicious.Aliases = []string{"writer.example:4406"}
	malicious.NodeID = nodeID
	malicious.EngineIdentity = observed.EngineIdentity.Clone()
	malicious.Role = model.RoleReplica
	malicious.Health = model.Health{State: model.HealthUnhealthy}
	malicious.Replication = model.ReplicationStatus{IOThread: model.ThreadStopped, SQLThread: model.ThreadStopped}
	malicious.Maintenance = false
	malicious.PromotionEligible = false
	malicious.EngineMetadata = map[string]string{"version": "forged"}

	updated, endpoint, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: malicious, EndpointID: endpoints[0].ResourceID})
	if err != nil {
		t.Fatalf("reconcile coordinates: %v", err)
	}
	if updated.ResourceID != instanceID || updated.EngineIdentity["server_uuid"] != observed.EngineIdentity["server_uuid"] || updated.Role != model.RolePrimary || updated.Health.State != model.HealthHealthy || updated.Replication.IOThread != model.ThreadRunning || !updated.Maintenance || !updated.PromotionEligible || updated.EngineMetadata["version"] != "8.4" {
		t.Fatalf("metadata changed runtime authority: %+v", updated)
	}
	if updated.DisplayName != "mysql-new" || updated.Hostname != "mysql-new" || updated.IPAddress != "192.0.2.20" || updated.Port != 4406 || updated.NodeID != nodeID || !contains(updated.Aliases, "mysql-old:3306") || !contains(updated.Aliases, "192.0.2.10:3306") {
		t.Fatalf("coordinates not reconciled: %+v", updated)
	}
	if endpoint.ResourceID != endpoints[0].ResourceID || endpoint.Hostname != updated.Hostname || endpoint.IPAddress != updated.IPAddress || endpoint.Port != updated.Port {
		t.Fatalf("bound endpoint not updated: %+v", endpoint)
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("metadata endpoint update must invalidate topology")
	}
	storedCluster, _ := repository.Cluster(cluster.ResourceID)
	if storedCluster.Health.State != model.HealthUnknown {
		t.Fatalf("cluster health not reset: %+v", storedCluster.Health)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Instances(cluster.ResourceID)[0]
	gotEndpoint := reopened.Endpoints(cluster.ResourceID)[0]
	if !reflect.DeepEqual(got, updated) || !reflect.DeepEqual(gotEndpoint, endpoint) {
		t.Fatalf("metadata transaction not durable: instance=%+v endpoint=%+v", got, gotEndpoint)
	}
	failedAt := snapshot.ObservedAt.Add(time.Minute)
	failed, err := reopened.ApplyDiscoveryRefresh(DiscoveryRefresh{ClusterID: cluster.ResourceID, ObservedAt: failedAt, Probes: []model.ProbeStatus{{EndpointID: endpoint.ResourceID, Health: model.Health{State: model.HealthUnknown}}}})
	if err != nil {
		t.Fatalf("publish failed discovery: %v", err)
	}
	failedInstance := failed.Instances[0]
	if failedInstance.Role != model.RolePrimary || failedInstance.Health.State != model.HealthUnknown || failedInstance.Replication.IOThread != model.ThreadRunning || !failedInstance.Maintenance || !failedInstance.PromotionEligible || failedInstance.EngineMetadata["version"] != "8.4" {
		t.Fatalf("malicious metadata leaked into failed discovery topology: %+v", failedInstance)
	}
	reopened, err = Open(path)
	if err != nil {
		t.Fatalf("reopen failed discovery: %v", err)
	}
	persistedFailed, found := reopened.TopologySnapshot(cluster.ResourceID)
	if !found || persistedFailed.Instances[0].Role != model.RolePrimary || persistedFailed.Instances[0].Health.State != model.HealthUnknown {
		t.Fatalf("failed discovery runtime authority not durable: %+v found=%t", persistedFailed, found)
	}
}

func TestReconcileMetadataCoordinatesRequiresUnambiguousOwnedEndpointAndRollsBack(t *testing.T) {
	repository := NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
		{Kind: model.EndpointDatabase, Hostname: "mysql-alias", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	observed := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	snapshot, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{ClusterID: cluster.ResourceID, ObservedAt: observedAt, Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}, {EndpointID: endpoints[1].ResourceID, Instance: observed}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}, {EndpointID: endpoints[1].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("publish topology: %v", err)
	}
	instance := snapshot.Instances[0]
	instance.Hostname = "mysql-new"
	before := captureStoreDiscoveryState(repository, cluster.ResourceID)
	if _, _, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: instance}); !errors.Is(err, ErrValidation) {
		t.Fatalf("ambiguous endpoint error = %v", err)
	}
	if after := captureStoreDiscoveryState(repository, cluster.ResourceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("ambiguous update changed state: before=%+v after=%+v", before, after)
	}
	foreignCluster, foreignEndpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "billing"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "taken", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create collision inventory: %v", err)
	}
	_ = foreignCluster
	instance.Hostname = foreignEndpoints[0].Hostname
	instance.Port = foreignEndpoints[0].Port
	if _, _, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: instance, EndpointID: endpoints[0].ResourceID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("collision error = %v", err)
	}
	if after := captureStoreDiscoveryState(repository, cluster.ResourceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("collision changed state: before=%+v after=%+v", before, after)
	}
	if _, _, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: instance, EndpointID: foreignEndpoints[0].ResourceID}); !errors.Is(err, ErrValidation) {
		t.Fatalf("foreign endpoint error = %v", err)
	}

	repository.path = t.TempDir()
	instance.Hostname = "persist-fail"
	if _, _, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: instance, EndpointID: endpoints[0].ResourceID}); err == nil {
		t.Fatal("persistence failure must be returned")
	}
	if after := captureStoreDiscoveryState(repository, cluster.ResourceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed persistence changed state: before=%+v after=%+v", before, after)
	}
}

func TestReconcileMetadataCoordinatesRejectsEngineAndNativeIdentityMismatchAtomically(t *testing.T) {
	repository := NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-trust"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 12, 14, 0, 0, 0, time.UTC)
	observed := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	snapshot, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{ClusterID: cluster.ResourceID, ObservedAt: observedAt, Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("publish topology: %v", err)
	}
	before := captureStoreDiscoveryState(repository, cluster.ResourceID)
	beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	tests := []struct {
		name   string
		mutate func(model.DatabaseInstance) model.DatabaseInstance
	}{
		{name: "payload engine", mutate: func(instance model.DatabaseInstance) model.DatabaseInstance {
			instance.Engine = model.EnginePostgreSQL
			return instance
		}},
		{name: "native identity", mutate: func(instance model.DatabaseInstance) model.DatabaseInstance {
			instance.EngineIdentity = model.EngineIdentity{"server_uuid": "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"}
			return instance
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.mutate(snapshot.Instances[0])
			candidate.Hostname = "must-not-publish"
			if _, _, err := repository.ReconcileMetadataCoordinates(MetadataCoordinates{Instance: candidate, EndpointID: endpoints[0].ResourceID}); !errors.Is(err, ErrValidation) {
				t.Fatalf("trust mismatch error = %v", err)
			}
			if after := captureStoreDiscoveryState(repository, cluster.ResourceID); !reflect.DeepEqual(after, before) {
				t.Fatalf("trust mismatch changed state: before=%+v after=%+v", before, after)
			}
			afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
			afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
			if afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
				t.Fatalf("trust mismatch changed ordering state: generation=%d watermark=%s", afterInventory.Generation, afterWatermark)
			}
		})
	}
}

func TestActiveInventoryInvalidationRollsBackWhenPersistenceFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		Engine: model.EngineMySQL, DisplayName: "inventory-rollback",
	}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	instance.Role = model.RolePrimary
	if _, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID:    cluster.ResourceID,
		Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		ObservedAt:   observedAt,
	}); err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	beforeEndpoint := repository.Endpoints(cluster.ResourceID)[0]
	beforeCluster, _ := repository.Cluster(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	repository.path = t.TempDir()
	changed := beforeEndpoint
	changed.Hostname = "must-not-publish"
	changed.Port = 4406
	if _, err := repository.UpsertEndpoint(changed); err == nil {
		t.Fatal("active inventory change must fail when persistence fails")
	}
	afterEndpoint := repository.Endpoints(cluster.ResourceID)[0]
	afterCluster, _ := repository.Cluster(cluster.ResourceID)
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	if !reflect.DeepEqual(afterEndpoint, beforeEndpoint) || !reflect.DeepEqual(afterCluster, beforeCluster) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) || afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
		t.Fatalf("failed invalidation published partial state: endpoint=%+v cluster=%+v topology=%+v", afterEndpoint, afterCluster, afterTopology)
	}
}

type storeDiscoveryState struct {
	instances []model.DatabaseInstance
	endpoints []model.Endpoint
	links     []model.ReplicationLink
	metrics   []model.MetricSample
	anomalies []model.MetadataAnomaly
	cluster   model.DatabaseCluster
	topology  model.TopologySnapshot
	found     bool
}

func probeStatusesByEndpoint(probes []model.ProbeStatus) map[model.ResourceID]model.ProbeStatus {
	result := make(map[model.ResourceID]model.ProbeStatus, len(probes))
	for _, probe := range probes {
		result[probe.EndpointID] = probe
	}
	return result
}

func instancesByID(instances []model.DatabaseInstance) map[model.ResourceID]model.DatabaseInstance {
	result := make(map[model.ResourceID]model.DatabaseInstance, len(instances))
	for _, instance := range instances {
		result[instance.ResourceID] = instance
	}
	return result
}

func captureStoreDiscoveryState(repository *Repository, clusterID model.ResourceID) storeDiscoveryState {
	cluster, _ := repository.Cluster(clusterID)
	topology, found := repository.TopologySnapshot(clusterID)
	return storeDiscoveryState{
		instances: repository.Instances(clusterID),
		endpoints: repository.Endpoints(clusterID),
		links:     repository.ReplicationLinks(clusterID),
		metrics:   repository.MetricSamples(clusterID),
		anomalies: anomaliesForCluster(repository.Anomalies(), clusterID),
		cluster:   cluster,
		topology:  topology,
		found:     found,
	}
}

func anomaliesForCluster(anomalies []model.MetadataAnomaly, clusterID model.ResourceID) []model.MetadataAnomaly {
	result := make([]model.MetadataAnomaly, 0)
	for _, anomaly := range anomalies {
		if anomaly.ClusterID == clusterID {
			result = append(result, anomaly)
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
