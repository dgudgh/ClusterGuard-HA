package model

import "testing"

func TestTopologyContractsCarryPortableReplicationState(t *testing.T) {
	lag := int64(3)
	instance := DatabaseInstance{
		ResourceMeta: ResourceMeta{ResourceID: NewResourceID()},
		Engine:       EngineMySQL,
		Replication: ReplicationStatus{
			SourceIdentity: EngineIdentity{"server_uuid": "source-uuid"},
			IOThread:       ThreadRunning,
			SQLThread:      ThreadRunning,
			LagSeconds:     &lag,
		},
	}
	if instance.Replication.SourceIdentity["server_uuid"] != "source-uuid" || *instance.Replication.LagSeconds != 3 {
		t.Fatalf("portable replication state was lost: %+v", instance)
	}
}
