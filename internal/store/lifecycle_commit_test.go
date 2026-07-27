package store

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

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

func lifecyclePostgreSQLInstance(clusterID, nodeID model.ResourceID, systemIdentifier, hostname, ipAddress string, port int) model.DatabaseInstance {
	return model.DatabaseInstance{
		ClusterID: clusterID,
		NodeID:    nodeID,
		Engine:    model.EnginePostgreSQL,
		EngineIdentity: model.EngineIdentity{
			"resource_id":       string(nodeID),
			"system_identifier": systemIdentifier,
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   ipAddress,
		Port:        port,
		Role:        model.RoleStandby,
		Health:      model.Health{State: model.HealthHealthy},
		Replication: model.ReplicationStatus{
			SourceIdentity: model.EngineIdentity{"resource_id": string(model.NewResourceID()), "system_identifier": systemIdentifier},
			IOThread:       model.ThreadRunning,
			SQLThread:      model.ThreadRunning,
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

func TestCommitLifecycleAddPublishesVerifiedPostgreSQLStandby(t *testing.T) {
	repository := NewMemory()
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EnginePostgreSQL, DisplayName: "pg-ha"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "pg-primary", IPAddress: "192.0.2.50", Port: 5432, Active: true}},
	)
	if err != nil {
		t.Fatalf("create PostgreSQL cluster inventory: %v", err)
	}
	nodeID := model.NewResourceID()
	target := lifecycle.TargetPlan{Target: lifecycle.Target{
		NodeID: nodeID, NodeName: "cg-pg-0002", Kind: model.NodeMixed,
		Hostname: "pg-standby", IPAddress: "192.0.2.51", PostgreSQLPort: 5432,
	}, DatabaseRole: model.RoleStandby, SyncMethod: lifecycle.SyncPostgreSQLBaseBackup}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionAdd, []lifecycle.TargetPlan{target})
	verified := lifecyclePostgreSQLInstance(cluster.ResourceID, nodeID, "7664793534806288468", "pg-standby", "192.0.2.51", 5432)

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{verified}}); err != nil {
		t.Fatalf("commit verified PostgreSQL lifecycle result: %v", err)
	}

	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].Engine != model.EnginePostgreSQL || instances[0].NodeID != nodeID || instances[0].Role != model.RoleStandby {
		t.Fatalf("committed PostgreSQL instances=%+v", instances)
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
		t.Fatalf("verified PostgreSQL endpoint was not published: %+v", targetEndpoint)
	}
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

func TestCommitLifecycleRebuildResyncsExistingNativeInstanceInPlace(t *testing.T) {
	repository := NewMemory()
	node, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0004", Kind: model.NodeData, Hostname: "mysql-old", IPAddress: "192.0.2.30", Active: true,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.30", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	serverUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	oldResult, err := repository.ReconcileInstance(lifecycleMySQLInstance(cluster.ResourceID, node.ResourceID, serverUUID, "mysql-old", "192.0.2.30", 3306))
	if err != nil {
		t.Fatalf("register existing instance: %v", err)
	}
	oldEndpoint := endpoints[0]
	oldEndpoint.InstanceID = oldResult.Instance.ResourceID
	if _, err := repository.UpsertEndpoint(oldEndpoint); err != nil {
		t.Fatalf("bind existing endpoint: %v", err)
	}
	target := lifecycle.TargetPlan{Target: lifecycle.Target{
		NodeID: node.ResourceID, NodeName: node.NodeName, Kind: model.NodeData,
		Hostname: "mysql-renamed", IPAddress: "192.0.2.31", MySQLPort: 3310, Rebuild: true,
	}, DatabaseRole: model.RoleReplica, ReusesNodeSlot: true}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionRebuild, []lifecycle.TargetPlan{target})
	resynced := lifecycleMySQLInstance(cluster.ResourceID, node.ResourceID, serverUUID, "mysql-renamed", "192.0.2.31", 3310)

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{resynced}}); err != nil {
		t.Fatalf("commit in-place lifecycle resync: %v", err)
	}

	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].ResourceID != oldResult.Instance.ResourceID || instances[0].EngineIdentity["server_uuid"] != serverUUID {
		t.Fatalf("in-place resync changed native resource identity: before=%+v after=%+v", oldResult.Instance, instances)
	}
	if instances[0].Hostname != resynced.Hostname || instances[0].IPAddress != resynced.IPAddress || instances[0].Port != resynced.Port {
		t.Fatalf("in-place resync did not update mutable endpoint: %+v", instances[0])
	}
	if !contains(instances[0].Aliases, "mysql-old:3306") || !contains(instances[0].Aliases, "192.0.2.30:3306") {
		t.Fatalf("old instance endpoints were not retained as aliases: %+v", instances[0].Aliases)
	}
	updatedEndpoints := repository.Endpoints(cluster.ResourceID)
	if len(updatedEndpoints) != 1 || updatedEndpoints[0].ResourceID != oldEndpoint.ResourceID || updatedEndpoints[0].InstanceID != oldResult.Instance.ResourceID || updatedEndpoints[0].Hostname != resynced.Hostname || updatedEndpoints[0].IPAddress != resynced.IPAddress || updatedEndpoints[0].Port != resynced.Port {
		t.Fatalf("in-place resync endpoint=%+v", updatedEndpoints)
	}
}

func TestCommitLifecycleRebuildPreservesPublishedTopologyWhileResyncingReplica(t *testing.T) {
	repository := NewMemory()
	primaryNode, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "mysql-primary", IPAddress: "192.0.2.40", Active: true,
	})
	if err != nil {
		t.Fatalf("register primary node: %v", err)
	}
	replicaNode, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0002", Kind: model.NodeData, Hostname: "mysql-replica", IPAddress: "192.0.2.41", Active: true,
	})
	if err != nil {
		t.Fatalf("register replica node: %v", err)
	}
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "payments"},
		[]model.Endpoint{
			{Kind: model.EndpointDatabase, Hostname: "mysql-primary", IPAddress: "192.0.2.40", Port: 3306, Active: true},
			{Kind: model.EndpointDatabase, Hostname: "mysql-replica", IPAddress: "192.0.2.41", Port: 3306, Active: true},
		},
	)
	if err != nil {
		t.Fatalf("create cluster inventory: %v", err)
	}
	primary := lifecycleMySQLInstance(cluster.ResourceID, primaryNode.ResourceID, "11111111-2222-3333-4444-555555555555", "mysql-primary", "192.0.2.40", 3306)
	primary.Role = model.RolePrimary
	primary.Replication = model.ReplicationStatus{}
	primaryResult, err := repository.ReconcileInstance(primary)
	if err != nil {
		t.Fatalf("register primary instance: %v", err)
	}
	replicaUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	replica := lifecycleMySQLInstance(cluster.ResourceID, replicaNode.ResourceID, replicaUUID, "mysql-replica", "192.0.2.41", 3306)
	replica.Replication.SourceIdentity = primary.EngineIdentity.Clone()
	replicaResult, err := repository.ReconcileInstance(replica)
	if err != nil {
		t.Fatalf("register replica instance: %v", err)
	}
	for index, endpoint := range endpoints {
		if index == 0 {
			endpoint.InstanceID = primaryResult.Instance.ResourceID
		} else {
			endpoint.InstanceID = replicaResult.Instance.ResourceID
		}
		if _, err := repository.UpsertEndpoint(endpoint); err != nil {
			t.Fatalf("bind endpoint %d: %v", index, err)
		}
	}
	observedAt := time.Date(2026, time.July, 14, 13, 46, 42, 0, time.UTC)
	lag := int64(0)
	repository.snapshot.TopologySnapshots[cluster.ResourceID] = model.TopologySnapshot{
		ClusterID: cluster.ResourceID,
		Instances: []model.DatabaseInstance{primaryResult.Instance, replicaResult.Instance},
		Links: []model.ReplicationLink{{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
			ClusterID:    cluster.ResourceID, SourceInstanceID: primaryResult.Instance.ResourceID,
			TargetInstanceID: replicaResult.Instance.ResourceID, Healthy: true, LagSeconds: &lag,
		}},
		Health: model.Health{State: model.HealthHealthy, ObservedAt: observedAt}, ObservedAt: observedAt,
	}

	target := lifecycle.TargetPlan{Target: lifecycle.Target{
		NodeID: replicaNode.ResourceID, NodeName: replicaNode.NodeName, Kind: model.NodeData,
		Hostname: "mysql-replica-renamed", IPAddress: "192.0.2.42", MySQLPort: 3310, Rebuild: true,
	}, DatabaseRole: model.RoleReplica, ReusesNodeSlot: true}
	task := persistVerifyingLifecycleTask(t, repository, cluster.ResourceID, lifecycle.ActionRebuild, []lifecycle.TargetPlan{target})
	resynced := lifecycleMySQLInstance(cluster.ResourceID, replicaNode.ResourceID, replicaUUID, "mysql-replica-renamed", "192.0.2.42", 3310)
	resynced.Replication.SourceIdentity = primary.EngineIdentity.Clone()

	if err := repository.Commit(context.Background(), task, lifecycle.ExecutionResult{Verified: true, Instances: []model.DatabaseInstance{resynced}}); err != nil {
		t.Fatalf("commit lifecycle resync: %v", err)
	}

	topology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !found {
		t.Fatal("verified replica lifecycle commit removed the published topology")
	}
	if !topology.ObservedAt.Equal(observedAt) {
		t.Fatalf("topology observed_at=%s, want original verified time %s", topology.ObservedAt, observedAt)
	}
	if len(topology.Instances) != 2 {
		t.Fatalf("topology instances=%+v, want primary and resynchronized replica", topology.Instances)
	}
	var publishedPrimary, publishedReplica model.DatabaseInstance
	for _, instance := range topology.Instances {
		switch instance.ResourceID {
		case primaryResult.Instance.ResourceID:
			publishedPrimary = instance
		case replicaResult.Instance.ResourceID:
			publishedReplica = instance
		}
	}
	if publishedPrimary.ResourceID == "" || publishedPrimary.Role != model.RolePrimary || publishedPrimary.Health.State != model.HealthHealthy {
		t.Fatalf("current primary disappeared during lifecycle commit: %+v", publishedPrimary)
	}
	if publishedReplica.ResourceID == "" || publishedReplica.Hostname != resynced.Hostname || publishedReplica.IPAddress != resynced.IPAddress || publishedReplica.Port != resynced.Port || publishedReplica.Role != model.RoleReplica {
		t.Fatalf("resynchronized replica was not patched into topology: %+v", publishedReplica)
	}
	if len(topology.Links) != 1 || topology.Links[0].SourceInstanceID != publishedPrimary.ResourceID || topology.Links[0].TargetInstanceID != publishedReplica.ResourceID {
		t.Fatalf("replication link continuity was lost: %+v", topology.Links)
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
