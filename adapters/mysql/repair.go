package mysql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	repairRefresh                  = "refresh"
	repairCollectReplicationStatus = "collect_replication_status"
	repairStartIOThread            = "start_io_thread"
	repairStartSQLThread           = "start_sql_thread"
	repairBeginMaintenance         = "begin_maintenance"
	repairEndMaintenance           = "end_maintenance"
)

var repairAllowlist = map[string]bool{
	repairRefresh: true, repairCollectReplicationStatus: true,
	repairStartIOThread: true, repairStartSQLThread: true,
	repairBeginMaintenance: true, repairEndMaintenance: true,
}

func repairAction(request adapter.OperationRequest) string {
	return strings.ToLower(strings.TrimSpace(request.Parameters["action"]))
}

func (adapterInstance *Adapter) repairPrecheck(_ context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationReplicationRepair || request.Resolved == nil {
		return nil, adapter.ErrUnsupported
	}
	action := repairAction(request)
	allowed := repairAllowlist[action]
	checks := []model.Check{{Name: "repair_action_allowlist", Status: model.CheckFail, Message: "repair action is not in the low-risk allowlist"}}
	if allowed {
		checks[0] = model.Check{Name: "repair_action_allowlist", Status: model.CheckPass, Message: "repair action is in the low-risk allowlist"}
	}
	target := request.Resolved.Target
	identityReady := target.ClusterID == request.Resolved.Cluster.ResourceID && strings.TrimSpace(target.EngineIdentity["server_uuid"]) != ""
	status := model.CheckFail
	message := "repair target must be an inventory resource with native identity"
	if identityReady {
		status = model.CheckPass
		message = "repair target is an immutable inventory resource"
	}
	checks = append(checks, model.Check{Name: "repair_target_identity", Status: status, Message: message})
	if action == repairStartIOThread || action == repairStartSQLThread {
		configured := strings.TrimSpace(target.Replication.SourceIdentity["server_uuid"]) != ""
		status, message = model.CheckFail, "replication must be configured before starting a thread"
		if configured {
			status, message = model.CheckPass, "replication is configured on the selected target"
		}
		checks = append(checks, model.Check{Name: "repair_replication_configured", Status: status, Message: message})
	}
	return checks, nil
}

func (adapterInstance *Adapter) repairPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.repairPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	action := repairAction(request)
	mutating := action == repairStartIOThread || action == repairStartSQLThread || action == repairBeginMaintenance || action == repairEndMaintenance
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID, SourceID: resolved.Primary.ResourceID,
		TargetID: resolved.Target.ResourceID, Stage: model.StagePlan,
		ObservationToken:  string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
		ResourceRevisions: planResourceRevisions(resolved), Checks: checks, Mutating: mutating,
		Steps:   []model.PlanStep{{Index: 1, Name: "repair_" + action, Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: mutating, Postcondition: "requested low-risk repair postcondition is verified"}},
		Summary: "low-risk replication repair is ready",
	}
	if planHasBlockingChecks(checks) {
		plan.Summary = "replication repair is blocked"
	}
	plan.Digest, err = operationPlanDigest(plan)
	return plan, err
}

func (adapterInstance *Adapter) repairExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if err := validateMySQLPlan(request, model.OperationReplicationRepair, true); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	action := repairAction(request)
	endpoint := instanceEndpoint(request.Resolved.Target)
	credentials := request.Resolved.Credentials
	if _, err := probeIdentity(ctx, adapterInstance.runner, endpoint, credentials); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("probe repair target: %w", err))
	}
	status, configured, err := probeReplication(ctx, adapterInstance.runner, endpoint, credentials)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if action == repairRefresh || action == repairCollectReplicationStatus {
		return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "replication status was collected; verification is required"), nil
	}
	if action == repairBeginMaintenance || action == repairEndMaintenance {
		if adapterInstance.maintenance == nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
		}
		desired := action == repairBeginMaintenance
		if err := adapterInstance.maintenance.SetMaintenance(ctx, request.Resolved.Cluster.ResourceID, request.Resolved.Target.ResourceID, desired); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("update maintenance state: %w", err))
		}
		return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "maintenance state was updated; verification is required"), nil
	}
	if adapterInstance.executor == nil || !configured {
		return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	dialect, err := dialectForVersion(request.Resolved.Target.EngineMetadata["version"])
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	statement := ""
	if action == repairStartIOThread && status.IOThread != model.ThreadRunning {
		statement = dialect.StartReplication + " IO_THREAD"
	}
	if action == repairStartSQLThread && status.SQLThread != model.ThreadRunning {
		statement = dialect.StartReplication + " SQL_THREAD"
	}
	if statement != "" {
		if err := adapterInstance.executor.Exec(ctx, endpoint, credentials, statement); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("execute low-risk replication repair: %w", err))
		}
	}
	return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "replication thread repair completed; verification is required"), nil
}

func (adapterInstance *Adapter) repairVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	now := time.Now().UTC()
	verification := model.Verification{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}, OperationID: request.Operation.ResourceID, ObservedAt: now}
	if err := validateMySQLPlan(request, model.OperationReplicationRepair, false); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	action := repairAction(request)
	status, configured, err := probeReplication(ctx, adapterInstance.runner, instanceEndpoint(request.Resolved.Target), request.Resolved.Credentials)
	if action == repairRefresh || action == repairCollectReplicationStatus {
		checkStatus := model.CheckFail
		if err == nil {
			checkStatus = model.CheckPass
		}
		verification.Checks = []model.Check{{Name: "replication_status_collected", Status: checkStatus, Message: "replication status collection was verified"}}
		verification.Passed = err == nil
		return verification, nil
	}
	if action == repairBeginMaintenance || action == repairEndMaintenance {
		state, stateErr := adapterInstance.maintenance.Maintenance(ctx, request.Resolved.Cluster.ResourceID, request.Resolved.Target.ResourceID)
		desired := action == repairBeginMaintenance
		passed := stateErr == nil && state == desired
		checkStatus := model.CheckFail
		if passed {
			checkStatus = model.CheckPass
		}
		verification.Checks = []model.Check{{Name: "maintenance_state", Status: checkStatus, Message: "requested maintenance state was verified"}}
		verification.Passed = passed
		return verification, nil
	}
	passed := err == nil && configured
	if action == repairStartIOThread {
		passed = passed && status.IOThread == model.ThreadRunning
	}
	if action == repairStartSQLThread {
		passed = passed && status.SQLThread == model.ThreadRunning
	}
	checkStatus := model.CheckFail
	if passed {
		checkStatus = model.CheckPass
	}
	verification.Checks = []model.Check{{Name: "repair_postcondition", Status: checkStatus, Message: "requested replication thread state was verified"}}
	verification.Passed = passed
	return verification, nil
}
