package identity

import (
	"fmt"
	"strconv"
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
		resourceID := model.ResourceID(value(identity, "resource_id"))
		if model.ValidResourceID(resourceID) && validPostgreSQLSystemIdentifier(value(identity, "system_identifier")) {
			return "postgresql:" + string(resourceID), nil
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
		if systemID := value(identity, "system_identifier"); validPostgreSQLSystemIdentifier(systemID) {
			return "postgresql:" + systemID, nil
		}
	case model.EngineOracle:
		dbid := value(identity, "dbid")
		if dbid != "" {
			return "oracle:" + dbid, nil
		}
	case model.EngineSQLServer:
		if groupID := value(identity, "group_id"); groupID != "" {
			return "sqlserver:" + groupID, nil
		}
	}
	return "", fmt.Errorf("missing native %s cluster identity", engine)
}

func validPostgreSQLSystemIdentifier(value string) bool {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return err == nil && parsed > 0
}
