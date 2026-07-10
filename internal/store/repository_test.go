package store

import (
	"path/filepath"
	"testing"
	"time"

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
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL}, nil)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	instance := mysqlInstance(cluster.ResourceID, "mysql-a", "192.0.2.10", 3306)
	instance.EngineMetadata = map[string]string{"version": "8.0"}
	instance.Replication.SourceIdentity = model.EngineIdentity{"server_uuid": "source-uuid"}
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
	again, ok := repository.FindInstanceByIdentity(cluster.ResourceID, model.EngineMySQL, model.EngineIdentity{
		"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if !ok || again.EngineIdentity["server_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || again.EngineMetadata["version"] != "8.0" || again.Replication.SourceIdentity["server_uuid"] != "source-uuid" {
		t.Fatalf("instance read leaked nested maps: %+v, found=%t", again, ok)
	}
}

func TestCreateClusterWithEndpointsRejectsInvalidSetAtomically(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	_, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{
		ResourceMeta: model.ResourceMeta{ResourceID: clusterID},
		Engine:       model.EngineMySQL,
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

func TestUpsertEndpointRejectsInvalidUnknownAndDuplicateActiveAddresses(t *testing.T) {
	repository := NewMemory()
	if _, err := repository.UpsertEndpoint(model.Endpoint{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}); err == nil {
		t.Fatal("empty cluster ID must fail")
	}
	if _, err := repository.UpsertEndpoint(model.Endpoint{ClusterID: model.NewResourceID(), Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}); err == nil {
		t.Fatal("unknown cluster ID must fail")
	}
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL}, nil)
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

func TestStoreMetricSamplesBoundsEachInstanceOrdersSamplesAndClonesValues(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL}, nil)
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

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
