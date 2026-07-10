package identity

import (
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

func value(identity model.EngineIdentity, key string) string {
	return strings.ToLower(strings.TrimSpace(identity[key]))
}

func InstanceKey(engine model.Engine, identity model.EngineIdentity) (string, error) {
	switch engine {
	case model.EngineMySQL:
		if serverUUID := value(identity, "server_uuid"); serverUUID != "" {
			return "mysql:" + serverUUID, nil
		}
	case model.EnginePostgreSQL:
		if resourceID := value(identity, "resource_id"); resourceID != "" {
			return "postgresql:" + resourceID, nil
		}
	case model.EngineOracle:
		dbid := value(identity, "dbid")
		database := value(identity, "db_unique_name")
		instance := value(identity, "instance_name")
		if dbid != "" && database != "" && instance != "" {
			return fmt.Sprintf("oracle:%s:%s:%s", dbid, database, instance), nil
		}
	case model.EngineSQLServer:
		groupID := value(identity, "group_id")
		replicaID := value(identity, "replica_id")
		if groupID != "" && replicaID != "" {
			return fmt.Sprintf("sqlserver:%s:%s", groupID, replicaID), nil
		}
	}
	return "", fmt.Errorf("missing native %s instance identity", engine)
}

func ClusterKey(engine model.Engine, identity model.EngineIdentity) (string, error) {
	switch engine {
	case model.EngineMySQL:
		if serverUUID := value(identity, "server_uuid"); serverUUID != "" {
			return "mysql:" + serverUUID, nil
		}
	case model.EnginePostgreSQL:
		if systemID := value(identity, "system_identifier"); systemID != "" {
			return "postgresql:" + systemID, nil
		}
	case model.EngineOracle:
		dbid := value(identity, "dbid")
		database := value(identity, "db_unique_name")
		if dbid != "" && database != "" {
			return fmt.Sprintf("oracle:%s:%s", dbid, database), nil
		}
	case model.EngineSQLServer:
		if groupID := value(identity, "group_id"); groupID != "" {
			return "sqlserver:" + groupID, nil
		}
	}
	return "", fmt.Errorf("missing native %s cluster identity", engine)
}
