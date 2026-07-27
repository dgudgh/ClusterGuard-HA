package postgresql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	postgresqlSyncAuto       = "auto"
	postgresqlSyncRewind     = "pg_rewind"
	postgresqlSyncBaseBackup = "pg_basebackup"
)

func postgresqlRequestedSyncMethod(request adapter.OperationRequest) string {
	method := strings.ToLower(strings.TrimSpace(request.Parameters["sync_method"]))
	if method == "" {
		return postgresqlSyncAuto
	}
	return method
}

func postgresqlNodeSyncMethod(request adapter.OperationRequest) (string, error) {
	if request.Resolved == nil {
		return "", adapter.ErrUnsupported
	}
	primary := request.Resolved.Primary
	target := request.Resolved.Target
	primaryMajor, primaryErr := postgresqlMajorVersion(primary.EngineMetadata["version"])
	targetVersion := strings.TrimSpace(target.EngineMetadata["version"])
	if targetVersion == "" {
		targetVersion = strings.TrimSpace(request.Parameters["target_version"])
	}
	targetMajor, targetErr := postgresqlMajorVersion(targetVersion)
	if primaryErr != nil || targetErr != nil || primaryMajor != targetMajor {
		return "", fmt.Errorf("PostgreSQL node synchronization requires matching major releases")
	}
	clusterSystemID := strings.TrimSpace(request.Resolved.Cluster.EngineIdentity["system_identifier"])
	targetSystemID := strings.TrimSpace(target.EngineIdentity["system_identifier"])
	if targetSystemID != "" && targetSystemID != clusterSystemID {
		return "", fmt.Errorf("target contains data from another PostgreSQL cluster")
	}
	rewindReady := targetSystemID == clusterSystemID && postgresqlRewindReady(target)
	switch postgresqlRequestedSyncMethod(request) {
	case postgresqlSyncAuto:
		if rewindReady {
			return postgresqlSyncRewind, nil
		}
		return postgresqlSyncBaseBackup, nil
	case postgresqlSyncRewind:
		if !rewindReady {
			return "", fmt.Errorf("pg_rewind requires matching system identity and data checksums or wal_log_hints")
		}
		return postgresqlSyncRewind, nil
	case postgresqlSyncBaseBackup:
		return postgresqlSyncBaseBackup, nil
	default:
		return "", fmt.Errorf("unsupported PostgreSQL synchronization method %q", postgresqlRequestedSyncMethod(request))
	}
}

func (adapterInstance *Adapter) nodeSyncPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationNodeSync || request.Operation.Engine != model.EnginePostgreSQL || request.Resolved == nil || !model.ValidResourceID(request.Operation.ResourceID) {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	checks := make([]model.Check, 0, 10)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	add("cluster_scope", resolved.Cluster.ResourceID == request.Operation.ClusterID && resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Primary.ResourceID != resolved.Target.ResourceID,
		"source and target are distinct resources in the selected cluster", "node synchronization scope is invalid")
	clusterSystemID := strings.TrimSpace(resolved.Cluster.EngineIdentity["system_identifier"])
	targetSystemID := strings.TrimSpace(resolved.Target.EngineIdentity["system_identifier"])
	targetResourceID := strings.TrimSpace(resolved.Target.EngineIdentity["resource_id"])
	add("native_identity", postgresqlStableInstanceIdentity(resolved.Primary, clusterSystemID) && model.ValidResourceID(resolved.Target.ResourceID) &&
		(targetSystemID == "" || targetSystemID == clusterSystemID) &&
		(targetResourceID == "" || model.ValidResourceID(model.ResourceID(targetResourceID))),
		"stable platform identity and PostgreSQL cluster identity are consistent", "target identity is missing, changed, or belongs to another PostgreSQL cluster")
	add("current_primary", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy &&
		strings.EqualFold(resolved.Primary.EngineMetadata["in_recovery"], "false") && strings.EqualFold(resolved.Primary.EngineMetadata["transaction_read_only"], "false"),
		"current primary is healthy and writable", "a unique healthy writable current primary is required")
	add("target_endpoint", resolved.Target.Engine == model.EnginePostgreSQL && resolved.Target.Port > 0 &&
		(strings.TrimSpace(resolved.Target.Hostname) != "" || strings.TrimSpace(resolved.Target.IPAddress) != ""),
		"target endpoint is an inventory PostgreSQL resource", "target endpoint is incomplete or outside the PostgreSQL inventory")
	method, methodErr := postgresqlNodeSyncMethod(request)
	add("sync_method", methodErr == nil, "selected guarded PostgreSQL synchronization method: "+method, postgresqlPublicError(methodErr))
	replicationUser := strings.TrimSpace(resolved.ReplicationCredentials.Username)
	if replicationUser == "" {
		replicationUser = strings.TrimSpace(request.ReplicationCredentials.Username)
	}
	add("replication_credentials", replicationUser != "", "PostgreSQL replication credentials are configured", "PostgreSQL replication credentials are required")
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) {
		add("postgresql_target_agent", false, "", "restricted PostgreSQL node control is not configured")
	} else {
		_, _, statusErr := adapterInstance.nodeController.Status(ctx, *resolved, resolved.Target)
		add("postgresql_target_agent", statusErr == nil, "target agent is reachable", "target agent status is unavailable")
	}
	add("writer_endpoint_owner", postgresqlEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, *resolved)),
		"writer endpoint remains owned only by the current primary", "writer endpoint ownership is unsafe for target synchronization")
	return checks, nil
}

func (adapterInstance *Adapter) nodeSyncPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.nodeSyncPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	method, methodErr := postgresqlNodeSyncMethod(request)
	if methodErr != nil {
		method = "blocked"
	}
	resolved := request.Resolved
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID,
		SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID, Stage: model.StagePlan,
		ObservationToken: postgresqlObservationToken(resolved), ResourceRevisions: postgresqlPlanRevisions(resolved),
		Checks: checks, Mutating: true,
		Steps: []model.PlanStep{
			{Index: 1, Name: "revalidate_node_sync", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "identity, source, version, and VIP ownership remain safe"},
			{Index: 2, Name: "stop_sync_target", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target service is stopped"},
			{Index: 3, Name: "verify_sync_target_stopped", Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Postcondition: "target cannot serve stale data"},
			{Index: 4, Name: "synchronize_target_" + method, Owner: "postgresql-agent", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target data follows the current primary"},
			{Index: 5, Name: "verify_synchronized_standby", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "target is a healthy standby without writer endpoint ownership"},
		},
		Summary: "PostgreSQL target is ready for guarded " + method + " synchronization",
	}
	if postgresqlBlockingChecks(checks) {
		plan.Summary = "PostgreSQL node synchronization is blocked"
	}
	plan.Digest, err = postgresqlPlanDigest(plan)
	return plan, err
}

func postgresqlNodeSyncMethodFromPlan(plan *model.OperationPlan) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("PostgreSQL node synchronization plan is required")
	}
	for _, step := range plan.Steps {
		const prefix = "synchronize_target_"
		if !strings.HasPrefix(step.Name, prefix) {
			continue
		}
		method := strings.TrimPrefix(step.Name, prefix)
		if method == postgresqlSyncRewind || method == postgresqlSyncBaseBackup {
			return method, nil
		}
		return "", fmt.Errorf("PostgreSQL synchronization method in the plan is invalid")
	}
	return "", fmt.Errorf("PostgreSQL synchronization step is missing")
}

func (adapterInstance *Adapter) verifyNodeSync(ctx context.Context, request adapter.OperationRequest) []model.Check {
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	checks := make([]model.Check, 0, 3)
	running, inRecovery, roleErr := adapterInstance.nodeController.Status(ctx, resolved, resolved.Target)
	roleStatus, roleMessage := model.CheckFail, "synchronized target is not a running PostgreSQL standby"
	if roleErr == nil && running && inRecovery {
		roleStatus, roleMessage = model.CheckPass, "synchronized target is running in recovery mode"
	}
	checks = append(checks, model.Check{Name: "synchronized_target_role", Status: roleStatus, Message: roleMessage})
	checks = append(checks, adapterInstance.postgresqlInstanceVerificationCheck(
		ctx, resolved, "synchronized_target_database", resolved.Target, model.RoleStandby, resolved.Primary.ResourceID,
	))
	endpointStatus, endpointMessage := model.CheckFail, "writer endpoint ownership is unsafe after synchronization"
	if postgresqlEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, resolved)) {
		endpointStatus, endpointMessage = model.CheckPass, "writer endpoint remains owned only by the current primary"
	}
	checks = append(checks, model.Check{Name: "synchronized_target_endpoint_absent", Status: endpointStatus, Message: endpointMessage})
	return checks
}

func (adapterInstance *Adapter) nodeSyncExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validatePostgreSQLPlan(request, model.OperationNodeSync, false); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	method, err := postgresqlNodeSyncMethodFromPlan(request.Plan)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	primaryDiscovery, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{
		ClusterID: resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(resolved.Primary), Credentials: resolved.Credentials,
	})
	if err != nil || !postgresqlInstanceIdentityMatches(primaryDiscovery.Instance, resolved.Primary, resolved.Cluster.EngineIdentity["system_identifier"]) ||
		primaryDiscovery.Instance.Role != model.RolePrimary || primaryDiscovery.Instance.Health.State != model.HealthHealthy {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("current PostgreSQL primary changed before node synchronization"))
	}
	if !postgresqlEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, resolved)) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("writer endpoint ownership is unsafe before node synchronization"))
	}

	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Hour)
	defer cancel()
	permitID := request.Operation.ResourceID
	running, _, statusErr := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
	if statusErr != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", statusErr)
	}
	if running {
		if err := adapterInstance.nodeController.Stop(mutationCtx, resolved, resolved.Target, permitID); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("stop PostgreSQL synchronization target: %w", err))
		}
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "stop_sync_target", "target PostgreSQL service is stopped"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "target_stopped", err)
	}
	stopped, err := adapterInstance.nodeController.IsStopped(mutationCtx, resolved, resolved.Target)
	if err != nil || !stopped {
		if err == nil {
			err = fmt.Errorf("PostgreSQL synchronization target remains active")
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "target_stop_unknown", err)
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "verify_sync_target_stopped", "target cannot serve stale data"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "target_stopped", err)
	}
	step := "synchronize_target_" + method
	completed, err := postgresqlStepCompleted(mutationCtx, request, step)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "target_stopped", err)
	}
	if !completed {
		switch method {
		case postgresqlSyncRewind:
			err = adapterInstance.nodeController.Rewind(mutationCtx, resolved, resolved.Target, resolved.Primary, permitID)
		case postgresqlSyncBaseBackup:
			err = adapterInstance.nodeController.BaseBackup(mutationCtx, resolved, resolved.Target, resolved.Primary, permitID)
		}
		if err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "synchronization_failed", err)
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, step, "PostgreSQL data synchronization completed"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "synchronization_unknown", err)
		}
	}
	checks := adapterInstance.verifyNodeSync(mutationCtx, request)
	if postgresqlBlockingChecks(checks) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "verification_failed", fmt.Errorf("PostgreSQL synchronized target verification failed"))
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "verify_synchronized_standby", "target is a healthy standby without writer endpoint ownership"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "verification_unknown", err)
	}
	return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationSucceeded, started, "PostgreSQL node synchronization completed and was verified"), nil
}
