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
		{model.EnginePostgreSQL, model.EngineIdentity{"resource_id": "11111111-1111-4111-8111-111111111111", "system_identifier": "7428625847249870011"}, "postgresql:11111111-1111-4111-8111-111111111111"},
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

func TestOracleClusterKeyUsesSharedDataGuardDBID(t *testing.T) {
	primary, err := ClusterKey(model.EngineOracle, model.EngineIdentity{"dbid": "1234", "db_unique_name": "MESDB"})
	if err != nil {
		t.Fatal(err)
	}
	standby, err := ClusterKey(model.EngineOracle, model.EngineIdentity{"dbid": "1234", "db_unique_name": "REPORTDB"})
	if err != nil {
		t.Fatal(err)
	}
	if primary != "oracle:1234" || standby != primary {
		t.Fatalf("Data Guard members must share one cluster key: primary=%q standby=%q", primary, standby)
	}
}

func TestPostgreSQLIdentityRejectsMutableOrMalformedNativeKeys(t *testing.T) {
	for _, candidate := range []model.EngineIdentity{
		{"resource_id": "pg01:5432", "system_identifier": "7428625847249870011"},
		{"resource_id": "11111111-1111-4111-8111-111111111111"},
		{"resource_id": "11111111-1111-4111-8111-111111111111", "system_identifier": "not-a-number"},
	} {
		if _, err := InstanceKey(model.EnginePostgreSQL, candidate); err == nil {
			t.Fatalf("invalid PostgreSQL instance identity unexpectedly accepted: %+v", candidate)
		}
	}
	for _, candidate := range []model.EngineIdentity{{}, {"system_identifier": "0"}, {"system_identifier": "not-a-number"}} {
		if _, err := ClusterKey(model.EnginePostgreSQL, candidate); err == nil {
			t.Fatalf("invalid PostgreSQL cluster identity unexpectedly accepted: %+v", candidate)
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
