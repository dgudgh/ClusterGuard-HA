package mysql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func rejoinEndpointSafe(checks []model.Check) bool {
	for _, check := range checks {
		if check.Name == "writer_endpoint_provider" && check.Status == model.CheckPass {
			return true
		}
	}
	return false
}

func (adapterInstance *Adapter) rejoinPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	checks := []model.Check{}
	if request.Operation.Kind != model.OperationFormerPrimaryRejoin || request.Resolved == nil {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	appendCheck := func(name string, ok bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !ok {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	appendCheck("inventory_membership", resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID && resolved.Primary.ResourceID != resolved.Target.ResourceID, "current and former primary are inventory resources", "current and former primary must be distinct resources in the selected cluster")
	primaryReadOnly, primaryKnown := candidateReadOnly(resolved.Primary.EngineMetadata)
	appendCheck("current_primary_writable", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy && primaryKnown && !primaryReadOnly, "current primary is healthy and writable", "current primary must be healthy and writable")
	targetReadOnly, targetReadOnlyKnown := boolMetadata(resolved.Target.EngineMetadata, "read_only")
	targetSuperReadOnly, targetSuperReadOnlyKnown := boolMetadata(resolved.Target.EngineMetadata, "super_read_only")
	targetReachable := resolved.Target.Health.State == model.HealthHealthy || resolved.Target.Health.State == model.HealthDegraded
	appendCheck("former_primary_read_only", targetReachable && targetReadOnlyKnown && targetSuperReadOnlyKnown && targetReadOnly && targetSuperReadOnly, "former primary is reachable and fully read-only", "former primary must be reachable and fully read-only")
	appendCheck("mysql_identity", strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"]) != "" && strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"]) != "", "native MySQL identities are present", "native MySQL identities are required")
	primarySet, primaryErr := ParseGTIDSet(resolved.Primary.EngineMetadata["gtid_executed"])
	formerSet, formerErr := ParseGTIDSet(resolved.Target.EngineMetadata["gtid_executed"])
	comparison, compareErr := CompareGTIDSets(primarySet, formerSet)
	subset := primaryErr == nil && formerErr == nil && compareErr == nil && comparison.ErrantTransactions == 0
	appendCheck("former_primary_gtid_subset", subset, "former primary GTID history is a subset of the current primary", "former primary has invalid or errant GTID history")
	appendCheck("rebuild_required", subset, "fast rejoin is safe; rebuild is not required", "fast rejoin is blocked; rebuild the former primary from a current source")
	primaryFamily, primaryVersionErr := mysqlReleaseFamily(resolved.Primary.EngineMetadata["version"])
	targetFamily, targetVersionErr := mysqlReleaseFamily(resolved.Target.EngineMetadata["version"])
	appendCheck("version_compatibility", primaryVersionErr == nil && targetVersionErr == nil && primaryFamily == targetFamily, "current and former primary release families are compatible", "current and former primary release families are incompatible")
	providerChecks := adapterInstance.endpointProvider.Precheck(ctx, *resolved)
	appendCheck("former_primary_vip_absent", rejoinEndpointSafe(providerChecks), "VIP is owned only by the current primary", "VIP ownership is incomplete or includes the former primary")
	return checks, nil
}

func (adapterInstance *Adapter) rejoinPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.rejoinPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID,
		SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID, Stage: model.StagePlan,
		ObservationToken:  resolvedObservationToken(resolved),
		ResourceRevisions: planResourceRevisions(resolved), Checks: checks, Mutating: true,
		Steps: []model.PlanStep{
			{Index: 1, Name: "revalidate_former_primary", Owner: "mysql", TargetID: resolved.Target.ResourceID, Postcondition: "identity, GTID, VIP, and read-only state remain safe"},
			{Index: 2, Name: "attach_former_primary", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "former primary follows the current primary"},
			{Index: 3, Name: "verify_former_primary", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "former primary is a healthy read-only replica without VIP ownership"},
		},
		Summary: "former primary is ready for fast GTID rejoin",
	}
	if planHasBlockingChecks(checks) {
		plan.Summary = "former-primary fast rejoin is blocked; review rebuild guidance"
	}
	plan.Digest, err = operationPlanDigest(plan)
	return plan, err
}

func (adapterInstance *Adapter) rejoinLivePrecheck(ctx context.Context, resolved *adapter.ResolvedOperation) error {
	primary, err := probeIdentity(ctx, adapterInstance.runner, instanceEndpoint(resolved.Primary), resolved.Credentials)
	if err != nil {
		return fmt.Errorf("probe current primary: %w", err)
	}
	former, err := probeIdentity(ctx, adapterInstance.runner, instanceEndpoint(resolved.Target), resolved.Credentials)
	if err != nil {
		return fmt.Errorf("probe former primary: %w", err)
	}
	if primary.serverUUID != strings.ToLower(resolved.Primary.EngineIdentity["server_uuid"]) || former.serverUUID != strings.ToLower(resolved.Target.EngineIdentity["server_uuid"]) {
		return fmt.Errorf("live MySQL identity changed")
	}
	if primary.readOnly || primary.superReadOnly || !former.readOnly || !former.superReadOnly {
		return fmt.Errorf("current or former primary read-only state is unsafe")
	}
	primarySet, primaryErr := ParseGTIDSet(primary.gtidExecuted)
	formerSet, formerErr := ParseGTIDSet(former.gtidExecuted)
	comparison, compareErr := CompareGTIDSets(primarySet, formerSet)
	if primaryErr != nil || formerErr != nil || compareErr != nil || comparison.ErrantTransactions != 0 {
		return fmt.Errorf("former primary GTID history requires rebuild")
	}
	if !rejoinEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, *resolved)) {
		return fmt.Errorf("VIP ownership is unsafe for former-primary rejoin")
	}
	return nil
}

func (adapterInstance *Adapter) rejoinExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.executor == nil || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validateMySQLPlan(request, model.OperationFormerPrimaryRejoin, true); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	completed, err := allMutationStepsCompleted(ctx, request)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", fmt.Errorf("read former-primary progress: %w", err))
	}
	if completed {
		verification, verifyErr := adapterInstance.rejoinVerify(ctx, request)
		if verifyErr != nil || !verification.Passed {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("former-primary rejoin postconditions are not satisfied")
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", verifyErr)
		}
		return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "former primary was already attached and remains verified"), nil
	}
	if err := adapterInstance.rejoinLivePrecheck(ctx, request.Resolved); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if err := adapterInstance.reparentFollower(ctx, request.Resolved.Target, request.Resolved.Primary, request.Resolved.Credentials, request.Resolved.ReplicationCredentials); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("attach former primary: %w", err))
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "attach_former_primary", "former primary follows the current primary"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", err)
	}
	return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "former primary was attached; verification is required"), nil
}

func (adapterInstance *Adapter) rejoinVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	now := time.Now().UTC()
	verification := model.Verification{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}, OperationID: request.Operation.ResourceID, ObservedAt: now}
	if err := validateMySQLPlan(request, model.OperationFormerPrimaryRejoin, false); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	checks, writable := adapterInstance.verifyFollower(ctx, request.Resolved.Target, request.Resolved.Primary, request.Resolved.Credentials)
	verification.Checks = append(verification.Checks, checks...)
	if writable {
		verification.Checks = append(verification.Checks, model.Check{Name: "former_primary_writable", Status: model.CheckFail, Message: "former primary is writable"})
	}
	if rejoinEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, *request.Resolved)) {
		verification.Checks = append(verification.Checks, model.Check{Name: "former_primary_vip_absent", Status: model.CheckPass, Message: "VIP remains owned only by the current primary"})
	} else {
		verification.Checks = append(verification.Checks, model.Check{Name: "former_primary_vip_absent", Status: model.CheckFail, Message: "VIP ownership is unsafe"})
	}
	verification.Passed = !planHasBlockingChecks(verification.Checks)
	return verification, nil
}
