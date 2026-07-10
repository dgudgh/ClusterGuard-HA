package identity

import (
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestInstanceKeyUsesEngineNativeIdentity(t *testing.T) {
	tests := []struct {
		engine   model.Engine
		identity model.EngineIdentity
		want     string
	}{
		{model.EngineMySQL, model.EngineIdentity{"server_uuid": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}, "mysql:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{model.EnginePostgreSQL, model.EngineIdentity{"resource_id": "node-uuid"}, "postgresql:node-uuid"},
		{model.EngineOracle, model.EngineIdentity{"dbid": "1234", "db_unique_name": "PROD", "instance_name": "PROD1"}, "oracle:1234:prod:prod1"},
		{model.EngineSQLServer, model.EngineIdentity{"group_id": "GROUP", "replica_id": "REPLICA"}, "sqlserver:group:replica"},
	}
	for _, test := range tests {
		got, err := InstanceKey(test.engine, test.identity)
		if err != nil {
			t.Fatalf("%s identity failed: %v", test.engine, err)
		}
		if got != test.want {
			t.Fatalf("%s key: got %q want %q", test.engine, got, test.want)
		}
	}
}

func TestInstanceKeyRejectsMissingNativeIdentity(t *testing.T) {
	for _, engine := range model.SupportedEngines() {
		if _, err := InstanceKey(engine, model.EngineIdentity{}); err == nil {
			t.Fatalf("%s must reject an empty engine identity", engine)
		}
	}
}
