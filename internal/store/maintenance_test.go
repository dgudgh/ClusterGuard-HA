package store

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestSetMaintenancePersistsCanonicalStateAndTopologyOverlay(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "maintenance"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	instanceID := model.NewResourceID()
	repository.snapshot.Instances[instanceID] = model.DatabaseInstance{
		ResourceMeta: model.ResourceMeta{ResourceID: instanceID, MetadataRevision: 1}, ClusterID: cluster.ResourceID,
		Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
	}
	repository.snapshot.TopologySnapshots[cluster.ResourceID] = model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: []model.DatabaseInstance{{ResourceMeta: model.ResourceMeta{ResourceID: instanceID, MetadataRevision: 1}, ClusterID: cluster.ResourceID, Engine: model.EngineMySQL}}}

	if err := repository.SetMaintenance(context.Background(), cluster.ResourceID, instanceID, true); err != nil {
		t.Fatalf("begin maintenance: %v", err)
	}
	state, err := repository.Maintenance(context.Background(), cluster.ResourceID, instanceID)
	if err != nil || !state {
		t.Fatalf("maintenance=%t err=%v", state, err)
	}
	topology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !found || len(topology.Instances) != 1 || !topology.Instances[0].Maintenance {
		t.Fatalf("topology did not overlay canonical maintenance: %+v", topology)
	}
}
