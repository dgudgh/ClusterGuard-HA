package lifecycle

import (
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func lifecycleTarget(name string, kind model.NodeKind, version string) Target {
	return Target{NodeName: name, Kind: kind, Hostname: name + ".example.test", MySQLVersion: version, MySQLPort: 3306}
}

func failedLifecycleCheck(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func TestDataNodePlanningDoesNotImposeAClusterSizeLimit(t *testing.T) {
	targets := make([]Target, 32)
	for index := range targets {
		targets[index] = lifecycleTarget("cg-data-"+twoDigits(index+1), model.NodeData, "8.0.44")
	}
	plan := BuildPlan(Request{ClusterID: model.NewResourceID(), Action: ActionAdd, Targets: targets, SyncMethod: SyncAuto}, Capabilities{
		SourceVersion: "8.0.44", CloneAvailable: true, XtraBackupVersions: map[string]bool{"8.0": true}, LogicalDumpAllowed: true,
	})
	if plan.Blocked || len(plan.Targets) != len(targets) {
		t.Fatalf("unlimited data-node plan was blocked: %+v", plan)
	}
}

func TestControlNodeExpansionRequiresAtomicFinalOddMembership(t *testing.T) {
	base := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, CurrentControllerCount: 3, SyncMethod: SyncAuto}
	base.Targets = []Target{lifecycleTarget("cg-control-04", model.NodeController, "8.0.44")}
	blocked := BuildPlan(base, Capabilities{})
	if !blocked.Blocked || !failedLifecycleCheck(blocked.Checks, "final_controller_membership") {
		t.Fatalf("3 -> 4 controller expansion was accepted: %+v", blocked)
	}
	base.Targets = append(base.Targets, lifecycleTarget("cg-control-05", model.NodeController, "8.0.44"))
	accepted := BuildPlan(base, Capabilities{})
	if accepted.Blocked || accepted.FinalControllerCount != 5 {
		t.Fatalf("3 -> 5 controller expansion was rejected: %+v", accepted)
	}
}

func TestDamagedFormerPrimaryRebuildReusesNodeSlotAsReplica(t *testing.T) {
	nodeID := model.NewResourceID()
	request := Request{
		ClusterID: model.NewResourceID(), Action: ActionRebuild, CurrentControllerCount: 3, SyncMethod: SyncAuto,
		Targets: []Target{{NodeID: nodeID, NodeName: "cg-mixed-0001", Kind: model.NodeMixed, Hostname: "replacement", MySQLVersion: "8.0.44", MySQLPort: 3306, Rebuild: true}},
	}
	plan := BuildPlan(request, Capabilities{SourceVersion: "8.0.44", CloneAvailable: true})
	if plan.Blocked || len(plan.Targets) != 1 || plan.Targets[0].NodeID != nodeID || plan.Targets[0].DatabaseRole != model.RoleReplica || !plan.Targets[0].ReusesNodeSlot {
		t.Fatalf("former-primary rebuild plan=%+v", plan)
	}
	if plan.FinalControllerCount != 3 {
		t.Fatalf("mixed-node rebuild changed controller count: %+v", plan)
	}
}

func TestAutomaticSyncSkipsCloneForMySQL57AndUsesFallbackOrder(t *testing.T) {
	request := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, SyncMethod: SyncAuto, Targets: []Target{lifecycleTarget("cg-data-0001", model.NodeData, "5.7.44")}}
	plan := BuildPlan(request, Capabilities{
		SourceVersion: "5.7.44", CloneAvailable: true, XtraBackupVersions: map[string]bool{"5.7": true}, LogicalDumpAllowed: true,
	})
	if plan.Blocked || plan.Targets[0].SyncMethod != SyncXtraBackup {
		t.Fatalf("MySQL 5.7 did not skip Clone: %+v", plan)
	}
	plan = BuildPlan(request, Capabilities{SourceVersion: "5.7.44", CloneAvailable: true, LogicalDumpAllowed: true})
	if plan.Blocked || plan.Targets[0].SyncMethod != SyncLogicalDump {
		t.Fatalf("MySQL 5.7 did not fall back to logical dump: %+v", plan)
	}
	request.SyncMethod = SyncClone
	plan = BuildPlan(request, Capabilities{SourceVersion: "5.7.44", CloneAvailable: true, LogicalDumpAllowed: true})
	if !plan.Blocked || !failedLifecycleCheck(plan.Checks, "sync_method_cg-data-0001") {
		t.Fatalf("forced Clone on MySQL 5.7 was accepted: %+v", plan)
	}
}

func TestLifecyclePlanRejectsDuplicateFixedNodeNames(t *testing.T) {
	request := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, SyncMethod: SyncAuto, Targets: []Target{
		lifecycleTarget("cg-data-0001", model.NodeData, "8.0.44"), lifecycleTarget("CG-DATA-0001", model.NodeData, "8.0.44"),
	}}
	plan := BuildPlan(request, Capabilities{SourceVersion: "8.0.44", CloneAvailable: true})
	if !plan.Blocked || !failedLifecycleCheck(plan.Checks, "target_identity") {
		t.Fatalf("duplicate fixed node names were accepted: %+v", plan)
	}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
