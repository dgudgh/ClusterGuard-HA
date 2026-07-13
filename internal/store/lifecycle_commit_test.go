package store

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func lifecycleMySQLInstance(clusterID, nodeID model.ResourceID, serverUUID, hostname, ipAddress string, port int) model.DatabaseInstance {
	return model.DatabaseInstance{
		ClusterID: clusterID,
		NodeID:    nodeID,
		Engine:    model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": serverUUID,
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   ipAddress,
		Port:        port,
		Role:        model.RoleReplica,
		Health:      model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			IOThread:  model.ThreadRunning,
			SQLThread: model.ThreadRunning,
		},
		PromotionEligible: true,
	}
}

func persistVerifyingLifecycleTask(t *testing.T, repository *Repository, clusterID model.ResourceID, action lifecycle.Action, targets []lifecycle.TargetPlan) lifecycle.Task {
	t.Helper()
	requestTargets := make([]lifecycle.Target, len(targets))
	for index := range targets {
		requestTargets[index] = targets[index].Target
	}
	task, err := repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:    clusterID,
		Request:      lifecycle.Request{ClusterID: clusterID, Action: action, Targets: requestTargets},
		Plan:         lifecycle.Plan{ClusterID: clusterID, Action: action, Targets: targets},
		Status:       lifecycle.TaskVerifying,
	})
	if err != nil {
		t.Fatalf("persist verifying lifecycle task: %v", err)
	}
	return task
}

func TestCommitLifecycleAddPublishesVerifiedNodeInstanceAndEndpointAtomically(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-primary", IPAddress: "192.0.2.10", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	beforeGeneration := currentInventoryGeneration(t, repository, cluster.ResourceID)
	nodeID := model.NewResourceID()
	target := lifecycle.TargetPlan{Target: lifecycle.Target{
		NodeID: nodeID, NodeName: "cg-data-0002", Kind: model.NodeData,
		Hostname: "mysql-replica", IPAddress: "192.0.2.11", MySQLPort: 3306,
	}, DatabaseRole: model.RoleReplica}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionAdd, []lifecycle.TargetPlan{target})
	verified := lifecycleMySQLInstance(cluster.ResourceID, nodeID, "11111111-2222-3333-4444-555555555555", "mysql-replica", "192.0.2.11", 3306)

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{verified}}); err != nil {
		t.Fatalf("commit verified lifecycle result: %v", err)
	}

	node, found := repository.Node(nodeID)
	if !found || node.NodeName != "cg-data-0002" || node.Hostname != "mysql-replica" || !node.Active {
		t.Fatalf("committed node=%+v found=%t", node, found)
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].NodeID != nodeID || instances[0].EngineIdentity["server_uuid"] != verified.EngineIdentity["server_uuid"] {
		t.Fatalf("committed instances=%+v", instances)
	}
	endpoints := repository.Endpoints(cluster.ResourceID)
	if len(endpoints) != 2 {
		t.Fatalf("endpoint count=%d, want 2: %+v", len(endpoints), endpoints)
	}
	var targetEndpoint model.Endpoint
	for _, endpoint := range endpoints {
		if endpoint.InstanceID == instances[0].ResourceID {
			targetEndpoint = endpoint
		}
	}
	if targetEndpoint.ResourceID == "" || targetEndpoint.Hostname != verified.Hostname || targetEndpoint.IPAddress != verified.IPAddress || targetEndpoint.Port != verified.Port || !targetEndpoint.Active {
		t.Fatalf("verified endpoint was not published: %+v", targetEndpoint)
	}
	afterGeneration := currentInventoryGeneration(t, repository, cluster.ResourceID)
	if afterGeneration != beforeGeneration+1 {
		t.Fatalf("inventory generation=%d, want %d", afterGeneration, beforeGeneration+1)
	}
	updatedCluster, _ := repository.Cluster(cluster.ResourceID)
	if updatedCluster.Health.State != model.HealthUnknown {
		t.Fatalf("cluster health=%+v, want unknown after inventory mutation", updatedCluster.Health)
	}
}

func TestCommitLifecycleRebuildPreservesFixedNodeAndReplacesNativeInstance(t *testing.T) {
	repository := NewMemory()
	node, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0003", Kind: model.NodeData, Hostname: "mysql-old", IPAddress: "192.0.2.20", Active: true,
	})
	if err != nil {
		t.Fatalf("register old node: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "ledger"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.20", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	oldResult, err := repository.ReconcileInstance(lifecycleMySQLInstance(cluster.ResourceID, node.ResourceID, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "mysql-old", "192.0.2.20", 3306))
	if err != nil {
		t.Fatalf("register old instance: %v", err)
	}
	oldEndpoint := endpoints[0]
	oldEndpoint.InstanceID = oldResult.Instance.ResourceID
	if _, err := repository.UpsertEndpoint(oldEndpoint); err != nil {
		t.Fatalf("bind old endpoint: %v", err)
	}
	beforeGeneration := currentInventoryGeneration(t, repository, cluster.ResourceID)
	target := lifecycle.TargetPlan{Target: lifecycle.Target{
		NodeID: node.ResourceID, NodeName: node.NodeName, Kind: model.NodeData,
		Hostname: "mysql-rebuilt", IPAddress: "192.0.2.21", MySQLPort: 3310, Rebuild: true,
	}, DatabaseRole: model.RoleReplica, ReusesNodeSlot: true}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionRebuild, []lifecycle.TargetPlan{target})
	rebuilt := lifecycleMySQLInstance(cluster.ResourceID, node.ResourceID, "ffffffff-1111-2222-3333-444444444444", "mysql-rebuilt", "192.0.2.21", 3310)

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{rebuilt}}); err != nil {
		t.Fatalf("commit rebuilt lifecycle result: %v", err)
	}

	updatedNode, found := repository.Node(node.ResourceID)
	if !found || updatedNode.ResourceID != node.ResourceID || updatedNode.NodeName != node.NodeName || updatedNode.Hostname != rebuilt.Hostname || updatedNode.IPAddress != rebuilt.IPAddress {
		t.Fatalf("rebuilt node identity changed: before=%+v after=%+v", node, updatedNode)
	}
	if !contains(updatedNode.Aliases, node.Hostname) || !contains(updatedNode.Aliases, node.IPAddress) {
		t.Fatalf("old physical coordinates were not retained: %+v", updatedNode.Aliases)
	}
	if _, found := repository.Instance(oldResult.Instance.ResourceID); found {
		t.Fatal("old MySQL native instance remained active after physical rebuild")
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].ResourceID == oldResult.Instance.ResourceID || instances[0].NodeID != node.ResourceID || instances[0].EngineIdentity["server_uuid"] != rebuilt.EngineIdentity["server_uuid"] || instances[0].Role != model.RoleReplica {
		t.Fatalf("rebuilt active instances=%+v", instances)
	}
	updatedEndpoints := repository.Endpoints(cluster.ResourceID)
	if len(updatedEndpoints) != 1 || updatedEndpoints[0].ResourceID != oldEndpoint.ResourceID || updatedEndpoints[0].InstanceID != instances[0].ResourceID || updatedEndpoints[0].Hostname != rebuilt.Hostname || updatedEndpoints[0].Port != rebuilt.Port {
		t.Fatalf("rebuilt endpoint=%+v", updatedEndpoints)
	}
	afterGeneration := currentInventoryGeneration(t, repository, cluster.ResourceID)
	if afterGeneration != beforeGeneration+1 {
		t.Fatalf("inventory generation=%d, want %d", afterGeneration, beforeGeneration+1)
	}
}

func TestCommitLifecycleRejectsUnverifiedOrMismatchedResultsWithoutPartialMetadata(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "billing"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-primary", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	nodeID := model.NewResourceID()
	target := lifecycle.TargetPlan{Target: lifecycle.Target{NodeID: nodeID, NodeName: "cg-data-0004", Kind: model.NodeData, Hostname: "mysql-new", MySQLPort: 3306}, DatabaseRole: model.RoleReplica}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionAdd, []lifecycle.TargetPlan{target})
	before := cloneDiscoverySnapshot(repository.snapshot)

	badResults := []lifecycle.ExecutionResult{
		{Verified: false},
		{Verified: true},
		{Verified: true, Instances: []model.DatabaseInstance{lifecycleMySQLInstance(cluster.ResourceID, model.NewResourceID(), "11111111-2222-3333-4444-555555555555", "mysql-new", "", 3306)}},
	}
	for _, result := range badResults {
		if err := repository.Commit(context.Background(), task, result); err == nil {
			t.Fatalf("invalid lifecycle result was accepted: %+v", result)
		}
		if !reflect.DeepEqual(repository.snapshot, before) {
			t.Fatalf("invalid lifecycle result changed metadata: before=%+v after=%+v", before, repository.snapshot)
		}
	}
}

func TestCommitLifecyclePersistenceFailureDoesNotPublishPartialMetadata(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "inventory"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-primary", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	nodeID := model.NewResourceID()
	target := lifecycle.TargetPlan{Target: lifecycle.Target{NodeID: nodeID, NodeName: "cg-data-0005", Kind: model.NodeData, Hostname: "mysql-new", MySQLPort: 3306}, DatabaseRole: model.RoleReplica}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionAdd, []lifecycle.TargetPlan{target})
	before := cloneDiscoverySnapshot(repository.snapshot)
	repository.path = t.TempDir() + "/metadata.json"
	repository.syncFile = func(*os.File) error { return errors.New("forced snapshot failure") }
	verified := lifecycleMySQLInstance(cluster.ResourceID, nodeID, "11111111-2222-3333-4444-555555555555", "mysql-new", "", 3306)

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{verified}}); err == nil {
		t.Fatal("snapshot persistence failure was ignored")
	}
	if !reflect.DeepEqual(repository.snapshot, before) {
		t.Fatalf("persistence failure published partial metadata: before=%+v after=%+v", before, repository.snapshot)
	}
}
