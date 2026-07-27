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

func TestPostgreSQLLifecycleAutoUsesBaseBackupAndRejectsCrossMajor(t *testing.T) {
	request := Request{
		ClusterID: model.NewResourceID(), Engine: model.EnginePostgreSQL, Action: ActionAdd, SyncMethod: SyncAuto,
		Targets: []Target{{NodeName: "cg-pg-0002", Kind: model.NodeData, Hostname: "pg-02", PostgreSQLVersion: "16.4", PostgreSQLPort: 5432}},
	}
	capabilities := Capabilities{SourceVersion: "16.3", PostgreSQLBaseBackupAvailable: true, PostgreSQLRewindAvailable: true}
	plan := BuildPlan(request, capabilities)
	if plan.Blocked || len(plan.Targets) != 1 || plan.Targets[0].SyncMethod != SyncPostgreSQLBaseBackup {
		t.Fatalf("PostgreSQL auto synchronization plan=%+v", plan)
	}
	request.Targets[0].PostgreSQLVersion = "15.8"
	plan = BuildPlan(request, capabilities)
	if !plan.Blocked || !failedLifecycleCheck(plan.Checks, "sync_method_cg-pg-0002") {
		t.Fatalf("cross-major PostgreSQL lifecycle was accepted: %+v", plan)
	}
}

func TestPostgreSQLLifecycleExplicitRewindRequiresCapability(t *testing.T) {
	request := Request{
		ClusterID: model.NewResourceID(), Engine: model.EnginePostgreSQL, Action: ActionRebuild, SyncMethod: SyncPostgreSQLRewind,
		Targets: []Target{{NodeID: model.NewResourceID(), NodeName: "cg-pg-0002", Kind: model.NodeData, Hostname: "pg-02", PostgreSQLVersion: "16.4", PostgreSQLPort: 5432, Rebuild: true}},
	}
	blocked := BuildPlan(request, Capabilities{SourceVersion: "16.3", PostgreSQLBaseBackupAvailable: true})
	if !blocked.Blocked {
		t.Fatalf("PostgreSQL rewind without capability was accepted: %+v", blocked)
	}
	accepted := BuildPlan(request, Capabilities{SourceVersion: "16.3", PostgreSQLBaseBackupAvailable: true, PostgreSQLRewindAvailable: true})
	if accepted.Blocked || accepted.Targets[0].SyncMethod != SyncPostgreSQLRewind {
		t.Fatalf("configured PostgreSQL rewind was rejected: %+v", accepted)
	}
}

func TestLifecyclePlanRejectsInvalidTargetTransportAndDatabaseCoordinates(t *testing.T) {
	base := Target{
		NodeName: "cg-pg-0002", Kind: model.NodeData, Hostname: "pg-02.example.test", IPAddress: "192.0.2.32",
		SSHUser: "root", SSHPort: 22, PostgreSQLVersion: "16.4", PostgreSQLPort: 5432,
	}
	capabilities := Capabilities{SourceVersion: "16.3", PostgreSQLBaseBackupAvailable: true}
	tests := []struct {
		name   string
		mutate func(*Target)
	}{
		{name: "fixed node name", mutate: func(target *Target) { target.NodeName = "cg pg 0002" }},
		{name: "hostname", mutate: func(target *Target) { target.Hostname = "pg 02" }},
		{name: "ip address", mutate: func(target *Target) { target.IPAddress = "999.0.2.32" }},
		{name: "ssh user", mutate: func(target *Target) { target.SSHUser = "root;id" }},
		{name: "ssh port", mutate: func(target *Target) { target.SSHPort = 70000 }},
		{name: "database port", mutate: func(target *Target) { target.PostgreSQLPort = 0 }},
		{name: "database version", mutate: func(target *Target) { target.PostgreSQLVersion = "16 latest" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := base
			test.mutate(&target)
			plan := BuildPlan(Request{
				ClusterID: model.NewResourceID(), Engine: model.EnginePostgreSQL, Action: ActionAdd,
				SyncMethod: SyncAuto, Targets: []Target{target},
			}, capabilities)
			if !plan.Blocked || !failedLifecycleCheck(plan.Checks, "target_configuration_cg-pg-0002") {
				t.Fatalf("invalid %s was accepted: %+v", test.name, plan)
			}
		})
	}
}

func TestLifecyclePlanRejectsDonorThatReferencesTheTargetResourceOrEndpoint(t *testing.T) {
	targetID := model.NewResourceID()
	target := Target{
		NodeID: targetID, NodeName: "cg-pg-0002", Kind: model.NodeData, Hostname: "pg-02", IPAddress: "192.0.2.32",
		SSHPort: 22, PostgreSQLVersion: "16.4", PostgreSQLPort: 5432, Rebuild: true,
	}
	base := Request{
		ClusterID: model.NewResourceID(), Engine: model.EnginePostgreSQL, Action: ActionRebuild,
		SyncMethod: SyncPostgreSQLRewind, Targets: []Target{target},
		Donor: Donor{InstanceID: targetID, Hostname: "pg-primary", IPAddress: "192.0.2.31", Port: 5432},
	}
	capabilities := Capabilities{SourceVersion: "16.3", PostgreSQLRewindAvailable: true}
	if plan := BuildPlan(base, capabilities); !plan.Blocked || !failedLifecycleCheck(plan.Checks, "donor_target_cg-pg-0002") {
		t.Fatalf("self-referencing donor resource was accepted: %+v", plan)
	}
	base.Donor.InstanceID = model.NewResourceID()
	base.Donor.Hostname = target.Hostname
	base.Donor.IPAddress = target.IPAddress
	if plan := BuildPlan(base, capabilities); !plan.Blocked || !failedLifecycleCheck(plan.Checks, "donor_target_cg-pg-0002") {
		t.Fatalf("self-referencing donor endpoint was accepted: %+v", plan)
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

func TestInventoryPlanningRejectsUnknownRebuildAndExistingAddIdentity(t *testing.T) {
	existing := model.DatabaseNode{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "mysql-a", IPAddress: "192.0.2.10", Active: true}
	capabilities := Capabilities{SourceVersion: "8.0.44", CloneAvailable: true}
	add := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, SyncMethod: SyncAuto, Targets: []Target{lifecycleTarget("CG-DATA-0001", model.NodeData, "8.0.44")}}
	if plan := BuildPlanWithInventory(add, capabilities, []model.DatabaseNode{existing}); !plan.Blocked || !failedLifecycleCheck(plan.Checks, "inventory_target_CG-DATA-0001") {
		t.Fatalf("existing fixed node name was accepted for add: %+v", plan)
	}

	rebuild := Request{ClusterID: add.ClusterID, Action: ActionRebuild, SyncMethod: SyncAuto, Targets: []Target{{NodeID: model.NewResourceID(), NodeName: "cg-data-unknown", Kind: model.NodeData, Hostname: "mysql-b", MySQLVersion: "8.0.44", MySQLPort: 3306, Rebuild: true}}}
	if plan := BuildPlanWithInventory(rebuild, capabilities, []model.DatabaseNode{existing}); !plan.Blocked || !failedLifecycleCheck(plan.Checks, "inventory_target_cg-data-unknown") {
		t.Fatalf("unknown rebuild node UUID was accepted: %+v", plan)
	}
}

func TestInventoryPlanningDerivesControllerCountAndPreservesRebuildIdentity(t *testing.T) {
	controllers := []model.DatabaseNode{
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-control-01", Kind: model.NodeController, Hostname: "control-a", Active: true},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-control-02", Kind: model.NodeController, Hostname: "control-b", Active: true},
		{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, NodeName: "cg-mixed-03", Kind: model.NodeMixed, Hostname: "control-c", Active: true},
	}
	request := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, CurrentControllerCount: 99, Targets: []Target{lifecycleTarget("cg-control-04", model.NodeController, "8.0.44")}}
	plan := BuildPlanWithInventory(request, Capabilities{}, controllers)
	if !plan.Blocked || plan.FinalControllerCount != 4 || !failedLifecycleCheck(plan.Checks, "final_controller_membership") {
		t.Fatalf("controller count was trusted from caller instead of inventory: %+v", plan)
	}

	existing := controllers[2]
	rebuild := Request{ClusterID: request.ClusterID, Action: ActionRebuild, SyncMethod: SyncAuto, Targets: []Target{{NodeID: existing.ResourceID, NodeName: existing.NodeName, Kind: existing.Kind, Hostname: "control-c-rebuilt", MySQLVersion: "8.0.44", MySQLPort: 3306, Rebuild: true}}}
	rebuiltPlan := BuildPlanWithInventory(rebuild, Capabilities{SourceVersion: "8.0.44", CloneAvailable: true}, controllers)
	if rebuiltPlan.Blocked || rebuiltPlan.FinalControllerCount != 3 || rebuiltPlan.Targets[0].NodeID != existing.ResourceID || !rebuiltPlan.Targets[0].ReusesNodeSlot {
		t.Fatalf("registered mixed-node rebuild was not preserved: %+v", rebuiltPlan)
	}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
