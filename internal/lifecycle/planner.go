package lifecycle

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

var (
	lifecycleNamePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	lifecycleSSHUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	lifecycleVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
	lifecycleServicePattern = regexp.MustCompile(`^[A-Za-z0-9@_.-]+\.service$`)
)

func BuildPlan(request Request, capabilities Capabilities) Plan {
	plan := Plan{ClusterID: request.ClusterID, Action: request.Action, FinalControllerCount: request.CurrentControllerCount, CreatedAt: time.Now().UTC()}
	appendCheck := func(name string, passed bool, passMessage, failMessage string) {
		status, message := model.CheckPass, passMessage
		if !passed {
			status, message, plan.Blocked = model.CheckFail, failMessage, true
		}
		plan.Checks = append(plan.Checks, model.Check{Name: name, Status: status, Message: message})
	}
	appendCheck("cluster_identity", model.ValidResourceID(request.ClusterID), "cluster UUID is valid", "cluster UUID is required")
	engine := request.Engine
	if engine == "" {
		engine = model.EngineMySQL
	}
	appendCheck("database_engine", engine == model.EngineMySQL || engine == model.EnginePostgreSQL, "database engine is supported by the lifecycle executor", "node lifecycle currently supports mysql and postgresql")
	appendCheck("lifecycle_action", request.Action == ActionAdd || request.Action == ActionRebuild, "lifecycle action is valid", "lifecycle action must be add or rebuild")
	appendCheck("target_count", len(request.Targets) > 0, "at least one lifecycle target is present", "at least one lifecycle target is required")

	names := make(map[string]struct{}, len(request.Targets))
	identityValid := true
	controllerTargets := 0
	for _, target := range request.Targets {
		name := strings.ToLower(strings.TrimSpace(target.NodeName))
		if name == "" || !target.Kind.Valid() || strings.TrimSpace(target.Hostname) == "" {
			identityValid = false
		}
		if _, duplicate := names[name]; duplicate {
			identityValid = false
		}
		names[name] = struct{}{}
		if request.Action == ActionRebuild || target.Rebuild {
			if !model.ValidResourceID(target.NodeID) {
				identityValid = false
			}
		}
		if target.Kind == model.NodeController || target.Kind == model.NodeMixed {
			controllerTargets++
			if request.Action == ActionAdd && !target.Rebuild {
				plan.FinalControllerCount++
			}
		}
		configurationValid := lifecycleTargetConfigurationValid(target, engine)
		appendCheck(
			"target_configuration_"+lifecycleCheckName(target.NodeName), configurationValid,
			"target transport and database coordinates are valid",
			"target fixed name, hostname, IP, SSH settings, database version, port, service, data directory, or package name is invalid",
		)
		if target.Kind == model.NodeData || target.Kind == model.NodeMixed {
			distinct := lifecycleDonorTargetDistinct(request.Donor, target, engine)
			appendCheck(
				"donor_target_"+lifecycleCheckName(target.NodeName), distinct,
				"synchronization donor and target are distinct resources",
				"synchronization donor cannot reference the target resource or database endpoint",
			)
		}
		targetPlan := TargetPlan{Target: target, ReusesNodeSlot: request.Action == ActionRebuild || target.Rebuild}
		if targetPlan.NodeID == "" && request.Action == ActionAdd {
			targetPlan.NodeID = model.NewResourceID()
		}
		if target.Kind == model.NodeData || target.Kind == model.NodeMixed {
			targetPlan.DatabaseRole = model.RoleReplica
			var method SyncMethod
			var reason string
			var err error
			switch engine {
			case model.EngineMySQL:
				method, reason, err = SelectSyncMethod(request.SyncMethod, capabilities.SourceVersion, target.MySQLVersion, capabilities)
			case model.EnginePostgreSQL:
				method, reason, err = SelectPostgreSQLSyncMethod(request.SyncMethod, request.Action, capabilities.SourceVersion, target.PostgreSQLVersion, capabilities)
			default:
				err = fmt.Errorf("node synchronization is unsupported for engine %s", engine)
			}
			if err != nil {
				plan.Checks = append(plan.Checks, model.Check{Name: "sync_method_" + target.NodeName, Status: model.CheckFail, Message: err.Error()})
				plan.Blocked = true
			} else {
				targetPlan.SyncMethod = method
				targetPlan.SyncReason = reason
				plan.Checks = append(plan.Checks, model.Check{Name: "sync_method_" + target.NodeName, Status: model.CheckPass, Message: reason})
			}
		}
		plan.Targets = append(plan.Targets, targetPlan)
	}
	appendCheck("target_identity", identityValid, "target fixed identities are unique and valid", "target node_name, node UUID, kind, or hostname is invalid or duplicated")
	if controllerTargets > 0 {
		validMembership := request.CurrentControllerCount >= 3 && request.CurrentControllerCount%2 == 1 && plan.FinalControllerCount >= 3 && plan.FinalControllerCount%2 == 1
		appendCheck("final_controller_membership", validMembership, fmt.Sprintf("final controller membership is odd: %d", plan.FinalControllerCount), fmt.Sprintf("final controller membership must be an odd count of at least three, got %d", plan.FinalControllerCount))
	}
	return plan
}

func lifecycleCheckName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unnamed"
	}
	return strings.Map(func(character rune) rune {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			return character
		}
		return '-'
	}, value)
}

func lifecycleTargetConfigurationValid(target Target, engine model.Engine) bool {
	if !lifecycleNamePattern.MatchString(strings.TrimSpace(target.NodeName)) ||
		!lifecycleNamePattern.MatchString(strings.TrimSpace(target.Hostname)) {
		return false
	}
	if target.IPAddress != "" && net.ParseIP(strings.TrimSpace(target.IPAddress)) == nil {
		return false
	}
	if target.SSHUser != "" && !lifecycleSSHUserPattern.MatchString(strings.TrimSpace(target.SSHUser)) {
		return false
	}
	if target.SSHPort < 0 || target.SSHPort > 65535 {
		return false
	}
	if target.PackageName != "" && filepath.Base(target.PackageName) != target.PackageName {
		return false
	}
	if target.Kind != model.NodeData && target.Kind != model.NodeMixed {
		return true
	}
	validPort := func(port int) bool { return port >= 1 && port <= 65535 }
	switch engine {
	case model.EngineMySQL:
		return lifecycleVersionPattern.MatchString(strings.TrimSpace(target.MySQLVersion)) && validPort(target.MySQLPort)
	case model.EnginePostgreSQL:
		if !lifecycleVersionPattern.MatchString(strings.TrimSpace(target.PostgreSQLVersion)) || !validPort(target.PostgreSQLPort) {
			return false
		}
		if target.PostgreSQLService != "" && !lifecycleServicePattern.MatchString(strings.TrimSpace(target.PostgreSQLService)) {
			return false
		}
		if target.PostgreSQLDataDirectory != "" {
			directory := strings.TrimSpace(target.PostgreSQLDataDirectory)
			if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || directory == "/var" || directory == "/var/lib" || directory == "/opt" || directory == "/etc" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func lifecycleDonorTargetDistinct(donor Donor, target Target, engine model.Engine) bool {
	if donor.InstanceID == "" {
		return true
	}
	if donor.InstanceID == target.NodeID {
		return false
	}
	targetPort := target.MySQLPort
	if engine == model.EnginePostgreSQL {
		targetPort = target.PostgreSQLPort
	}
	if donor.Port <= 0 || donor.Port != targetPort {
		return true
	}
	if donor.IPAddress != "" && target.IPAddress != "" && strings.EqualFold(strings.TrimSpace(donor.IPAddress), strings.TrimSpace(target.IPAddress)) {
		return false
	}
	return donor.Hostname == "" || !strings.EqualFold(strings.TrimSpace(donor.Hostname), strings.TrimSpace(target.Hostname))
}

func SelectPostgreSQLSyncMethod(requested SyncMethod, action Action, sourceVersion, targetVersion string, capabilities Capabilities) (SyncMethod, string, error) {
	if requested == "" {
		requested = SyncAuto
	}
	sourceMajor, sourceOK := postgresqlReleaseMajor(sourceVersion)
	targetMajor, targetOK := postgresqlReleaseMajor(targetVersion)
	if !sourceOK || !targetOK || sourceMajor != targetMajor {
		return "", "", fmt.Errorf("PostgreSQL source and target must use the same major release")
	}
	switch requested {
	case SyncAuto:
		if capabilities.PostgreSQLBaseBackupAvailable {
			return SyncPostgreSQLBaseBackup, "pg_basebackup is the safe PostgreSQL synchronization baseline", nil
		}
	case SyncPostgreSQLBaseBackup:
		if capabilities.PostgreSQLBaseBackupAvailable {
			return requested, "pg_basebackup is available for the matching PostgreSQL major release", nil
		}
	case SyncPostgreSQLRewind:
		if action != ActionRebuild {
			return "", "", fmt.Errorf("pg_rewind is only valid for a registered node rebuild")
		}
		if capabilities.PostgreSQLRewindAvailable {
			return requested, "pg_rewind is available for the registered PostgreSQL rebuild target", nil
		}
	default:
		return "", "", fmt.Errorf("unsupported PostgreSQL synchronization method")
	}
	return "", "", fmt.Errorf("requested PostgreSQL synchronization method %s is unavailable", requested)
}

func BuildPlanWithInventory(request Request, capabilities Capabilities, nodes []model.DatabaseNode) Plan {
	request.CurrentControllerCount = 0
	byID := make(map[model.ResourceID]model.DatabaseNode, len(nodes))
	byName := make(map[string]model.DatabaseNode, len(nodes))
	for _, node := range nodes {
		byID[node.ResourceID] = node
		byName[strings.ToLower(strings.TrimSpace(node.NodeName))] = node
		if node.Active && (node.Kind == model.NodeController || node.Kind == model.NodeMixed) {
			request.CurrentControllerCount++
		}
	}
	plan := BuildPlan(request, capabilities)
	for index, target := range request.Targets {
		passed := true
		message := "target fixed identity is consistent with the registered node inventory"
		nameKey := strings.ToLower(strings.TrimSpace(target.NodeName))
		rebuild := request.Action == ActionRebuild || target.Rebuild
		if rebuild {
			existing, found := byID[target.NodeID]
			passed = found && existing.NodeName == target.NodeName && existing.Kind == target.Kind
			if !passed {
				message = "rebuild requires the exact registered node UUID, fixed name, and node kind"
			}
		} else {
			_, nameExists := byName[nameKey]
			_, idExists := byID[plan.Targets[index].NodeID]
			passed = !nameExists && !idExists
			if !passed {
				message = "new node UUID or fixed node name is already registered"
			}
		}
		if passed {
			for _, existing := range nodes {
				if !existing.Active || existing.ResourceID == target.NodeID {
					continue
				}
				if (target.Hostname != "" && strings.EqualFold(existing.Hostname, target.Hostname)) || (target.IPAddress != "" && existing.IPAddress == target.IPAddress) {
					passed = false
					message = "target host coordinates already belong to another active fixed node"
					break
				}
			}
		}
		status := model.CheckPass
		if !passed {
			status = model.CheckFail
			plan.Blocked = true
		}
		plan.Checks = append(plan.Checks, model.Check{Name: "inventory_target_" + target.NodeName, Status: status, Message: message})
	}
	return plan
}

func SelectSyncMethod(requested SyncMethod, sourceVersion, targetVersion string, capabilities Capabilities) (SyncMethod, string, error) {
	if requested == "" {
		requested = SyncAuto
	}
	sourceFamily, sourceOK := mysqlReleaseFamily(sourceVersion)
	targetFamily, targetOK := mysqlReleaseFamily(targetVersion)
	cloneCompatible := sourceOK && targetOK && sourceFamily == targetFamily && sourceFamily != "5.7" && capabilities.CloneAvailable
	xtraBackupCompatible := sourceOK && targetOK && sourceFamily == targetFamily && capabilities.XtraBackupVersions[sourceFamily]
	logicalCompatible := capabilities.LogicalDumpAllowed
	selectMethod := func(method SyncMethod) (SyncMethod, string, error) {
		switch method {
		case SyncClone:
			if cloneCompatible {
				return method, "MySQL Clone is the first compatible synchronization method", nil
			}
		case SyncXtraBackup:
			if xtraBackupCompatible {
				return method, "matching XtraBackup is available", nil
			}
		case SyncLogicalDump:
			if logicalCompatible {
				return method, "logical backup is the compatibility fallback", nil
			}
		default:
			return "", "", fmt.Errorf("unsupported synchronization method")
		}
		return "", "", fmt.Errorf("requested synchronization method %s is incompatible", method)
	}
	if requested != SyncAuto {
		return selectMethod(requested)
	}
	for _, method := range []SyncMethod{SyncClone, SyncXtraBackup, SyncLogicalDump} {
		if selected, reason, err := selectMethod(method); err == nil {
			return selected, reason, nil
		}
	}
	return "", "", fmt.Errorf("no compatible synchronization method is available")
}

func mysqlReleaseFamily(version string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "." + parts[1], true
}

func postgresqlReleaseMajor(version string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}
	for _, character := range parts[0] {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	return parts[0], true
}
