package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestNodeNameIsGloballyUniqueImmutableAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	node, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0001", DisplayName: "Database host 1", Kind: model.NodeData,
		Hostname: "mysql-a", IPAddress: "192.0.2.10", Active: true,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	if !model.ValidResourceID(node.ResourceID) || node.NodeName != "cg-data-0001" || node.MetadataRevision != 1 {
		t.Fatalf("registered node=%+v", node)
	}
	if _, err := repository.PutNode(model.DatabaseNode{NodeName: "CG-DATA-0001", Kind: model.NodeData, Hostname: "mysql-b", Active: true}); err == nil {
		t.Fatal("case-insensitive duplicate global node name was accepted")
	}
	renamed := node
	renamed.NodeName = "cg-data-renamed"
	if _, err := repository.PutNode(renamed); err == nil {
		t.Fatal("fixed node name was changed after registration")
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	got, found := reopened.Node(node.ResourceID)
	if !found || !reflect.DeepEqual(got, node) {
		t.Fatalf("durable node=%+v found=%t, want %+v", got, found, node)
	}
}

func TestNodeCoordinateChangePreservesIdentityAndRecordsAliases(t *testing.T) {
	repository := NewMemory()
	node, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-mixed-0001", Kind: model.NodeMixed, Hostname: "mysql-old", IPAddress: "192.0.2.10", Active: true,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	replacement := node
	replacement.Hostname = "mysql-new"
	replacement.IPAddress = "192.0.2.20"
	updated, err := repository.PutNode(replacement)
	if err != nil {
		t.Fatalf("update node coordinates: %v", err)
	}
	if updated.ResourceID != node.ResourceID || updated.NodeName != node.NodeName || updated.MetadataRevision != node.MetadataRevision+1 {
		t.Fatalf("coordinate update changed fixed identity: before=%+v after=%+v", node, updated)
	}
	if !contains(updated.Aliases, "mysql-old") || !contains(updated.Aliases, "192.0.2.10") {
		t.Fatalf("old node coordinates were not retained as aliases: %+v", updated.Aliases)
	}
}

func TestInstanceBindingRequiresRegisteredNodeAndSurvivesDiscovery(t *testing.T) {
	repository := NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	unknown := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	unknown.NodeID = model.NewResourceID()
	if _, err := repository.ReconcileInstance(unknown); err == nil {
		t.Fatal("instance was bound to an unregistered node UUID")
	}
	node, err := repository.PutNode(model.DatabaseNode{NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "mysql-a", Active: true})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	observed := mysqlInstance(cluster.ResourceID, "mysql-a", "", 3306)
	observed.NodeID = node.ResourceID
	first, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: currentInventoryGeneration(t, repository, cluster.ResourceID),
		ObservedAt: repository.now().UTC(), Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	withoutNode := observed
	withoutNode.NodeID = ""
	withoutNode.Hostname = "reported-hostname-changed"
	second, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: currentInventoryGeneration(t, repository, cluster.ResourceID),
		ObservedAt: first.ObservedAt.Add(1), Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: withoutNode}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if len(second.Instances) != 1 || second.Instances[0].ResourceID != first.Instances[0].ResourceID || second.Instances[0].NodeID != node.ResourceID {
		t.Fatalf("discovery changed stable node binding: first=%+v second=%+v", first.Instances, second.Instances)
	}
}

func TestDiscoveryBindsInstanceToRegisteredNodeByInventoryCoordinates(t *testing.T) {
	repository := NewMemory()
	node, err := repository.PutNode(model.DatabaseNode{NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "mysql-a", IPAddress: "192.0.2.10", Active: true})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observed := mysqlInstance(cluster.ResourceID, "mysql-a", "192.0.2.10", 3306)
	snapshot, err := repository.ApplyDiscoveryRefresh(DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: currentInventoryGeneration(t, repository, cluster.ResourceID),
		ObservedAt: repository.now().UTC(), Observations: []DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: observed}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("discover instance: %v", err)
	}
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].NodeID != node.ResourceID {
		t.Fatalf("instance did not bind fixed node identity: %+v", snapshot.Instances)
	}
}
