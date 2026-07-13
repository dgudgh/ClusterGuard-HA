package lifecycle

import (
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
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
		targetPlan := TargetPlan{Target: target, ReusesNodeSlot: request.Action == ActionRebuild || target.Rebuild}
		if targetPlan.NodeID == "" && request.Action == ActionAdd {
			targetPlan.NodeID = model.NewResourceID()
		}
		if target.Kind == model.NodeData || target.Kind == model.NodeMixed {
			targetPlan.DatabaseRole = model.RoleReplica
			method, reason, err := SelectSyncMethod(request.SyncMethod, capabilities.SourceVersion, target.MySQLVersion, capabilities)
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
