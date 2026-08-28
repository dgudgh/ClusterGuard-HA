package postgresql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func (adapterInstance *Adapter) rejoinLivePrecheck(ctx context.Context, resolved adapter.ResolvedOperation) error {
	current, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{
		ClusterID: resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(resolved.Primary), Credentials: resolved.Credentials,
	})
	if err != nil {
		return fmt.Errorf("probe current PostgreSQL primary: %w", err)
	}
	if !postgresqlInstanceIdentityMatches(current.Instance, resolved.Primary, resolved.Cluster.EngineIdentity["system_identifier"]) ||
		current.Instance.Role != model.RolePrimary || current.Instance.Health.State != model.HealthHealthy {
		return fmt.Errorf("current PostgreSQL primary identity or writable role changed")
	}
	if !postgresqlEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, resolved)) {
		return fmt.Errorf("writer endpoint ownership is unsafe for former-primary recovery")
	}
	if _, _, err := adapterInstance.nodeController.Status(ctx, resolved, resolved.Target); err != nil {
		return fmt.Errorf("probe PostgreSQL former-primary agent: %w", err)
	}
	return nil
}

func (adapterInstance *Adapter) rejoinExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validatePostgreSQLPlan(request, model.OperationFormerPrimaryRejoin, false); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if verification, err := adapterInstance.rejoinVerify(ctx, request); err == nil && verification.Passed {
		return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL former primary is already attached and verified"), nil
	}
	if err := adapterInstance.rejoinLivePrecheck(ctx, *request.Resolved); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	stableAuthorizer, ok := adapterInstance.endpointProvider.(adapter.StableHAEndpointAuthorizer)
	if !ok {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", fmt.Errorf("writer endpoint cannot protect stable primary ownership during recovery"))
	}
	stableAuthorization, err := stableAuthorizer.AuthorizeStableOwner(mutationCtx, resolved)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("protect current PostgreSQL primary endpoint: %w", err))
	}
	if stableAuthorization.Context == nil || stableAuthorization.Cancel == nil || !model.ValidResourceID(stableAuthorization.LeaseID) {
		if stableAuthorization.Cancel != nil {
			stableAuthorization.Cancel()
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("writer endpoint returned an incomplete stable ownership authorization"))
	}
	defer stableAuthorization.Cancel()
	mutationCtx = stableAuthorization.Context
	permitID := request.Operation.ResourceID

	stoppedStep, err := postgresqlStepCompleted(mutationCtx, request, "stop_former_primary")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if !stoppedStep {
		running, _, statusErr := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
		if statusErr != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", statusErr)
		}
		if running {
			if err := adapterInstance.nodeController.Stop(mutationCtx, resolved, resolved.Target, permitID); err != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("stop PostgreSQL former primary: %w", err))
			}
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "stop_former_primary", "former primary service is stopped"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", err)
		}
	}
	stopped, err := adapterInstance.nodeController.IsStopped(mutationCtx, resolved, resolved.Target)
	if err != nil || !stopped {
		if err == nil {
			err = fmt.Errorf("former primary service remains active")
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", err)
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "verify_former_primary_stopped", "former primary cannot serve writes"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", err)
	}

	rewound, err := postgresqlStepCompleted(mutationCtx, request, "rewind_former_primary")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced", err)
	}
	if !rewound {
		recoveryMessage := "former primary follows the current primary after pg_rewind"
		if rewindErr := adapterInstance.nodeController.Rewind(mutationCtx, resolved, resolved.Target, resolved.Primary, permitID); rewindErr != nil {
			if baseBackupErr := adapterInstance.nodeController.BaseBackup(mutationCtx, resolved, resolved.Target, resolved.Primary, permitID); baseBackupErr != nil {
				return postgresqlExecutionFailure(
					request.Operation.ResourceID,
					started,
					model.OperationIndeterminate,
					"rebuild_failed",
					errors.Join(
						fmt.Errorf("rewind PostgreSQL former primary: %w", rewindErr),
						fmt.Errorf("full base backup of PostgreSQL former primary: %w", baseBackupErr),
					),
				)
			}
			recoveryMessage = "former primary follows the current primary after full base backup fallback"
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "rewind_former_primary", recoveryMessage); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "rewind_unknown", err)
		}
	}
	running, inRecovery, err := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
	if err != nil || !running || !inRecovery {
		if err == nil {
			err = fmt.Errorf("rewound PostgreSQL node is not a running standby")
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "rewind_unknown", err)
	}
	return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL former primary was synchronized; verification is required"), nil
}

func (adapterInstance *Adapter) rejoinVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	if adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return model.Verification{}, adapter.ErrUnsupported
	}
	now := time.Now().UTC()
	verification := model.Verification{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  request.Operation.ResourceID, ObservedAt: now,
	}
	if err := validatePostgreSQLPlan(request, model.OperationFormerPrimaryRejoin, true); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	running, inRecovery, roleErr := adapterInstance.nodeController.Status(ctx, resolved, resolved.Target)
	rolePassed := roleErr == nil && running && inRecovery
	roleStatus, roleMessage := model.CheckFail, "former primary is not a running standby"
	if rolePassed {
		roleStatus, roleMessage = model.CheckPass, "former primary is running in PostgreSQL recovery mode"
	}
	verification.Checks = append(verification.Checks, model.Check{Name: "former_primary_role", Status: roleStatus, Message: roleMessage})
	verification.Checks = append(verification.Checks, adapterInstance.postgresqlInstanceVerificationCheck(
		ctx, resolved, "former_primary_database", resolved.Target, model.RoleStandby, resolved.Primary.ResourceID,
	))
	endpointPassed := postgresqlEndpointSafe(adapterInstance.endpointProvider.Precheck(ctx, resolved))
	endpointStatus, endpointMessage := model.CheckFail, "writer endpoint ownership is unsafe"
	if endpointPassed {
		endpointStatus, endpointMessage = model.CheckPass, "writer endpoint remains owned only by the current primary"
	}
	verification.Checks = append(verification.Checks, model.Check{Name: "former_primary_endpoint_absent", Status: endpointStatus, Message: endpointMessage})
	verification.Passed = !postgresqlBlockingChecks(verification.Checks)
	return verification, nil
}

func (adapterInstance *Adapter) repairExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if err := validatePostgreSQLPlan(request, model.OperationReplicationRepair, false); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	action := postgresqlRepairAction(request)
	result, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{ClusterID: request.Resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(request.Resolved.Target), Credentials: request.Resolved.Credentials})
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("probe PostgreSQL repair target: %w", err))
	}
	if !postgresqlInstanceIdentityMatches(result.Instance, request.Resolved.Target, request.Resolved.Cluster.EngineIdentity["system_identifier"]) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("live PostgreSQL repair target identity changed"))
	}
	if action == postgresqlRepairCollect {
		return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL replication status was collected; verification is required"), nil
	}
	if adapterInstance.executor == nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	statement := ""
	switch action {
	case postgresqlRepairResume:
		statement = "SELECT pg_wal_replay_resume()"
	case postgresqlRepairReload:
		statement = "SELECT pg_reload_conf()"
	default:
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("PostgreSQL repair action is not allowlisted"))
	}
	if err := adapterInstance.executor.Exec(ctx, postgresqlInstanceEndpoint(request.Resolved.Target), request.Resolved.Credentials, statement); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("execute PostgreSQL low-risk repair: %w", err))
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(ctx), request, "repair_"+action, "PostgreSQL low-risk repair command completed"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "repair_unknown", err)
	}
	return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL low-risk repair completed; verification is required"), nil
}

func (adapterInstance *Adapter) repairVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	now := time.Now().UTC()
	verification := model.Verification{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  request.Operation.ResourceID, ObservedAt: now,
	}
	if err := validatePostgreSQLPlan(request, model.OperationReplicationRepair, true); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	result, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{ClusterID: request.Resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(request.Resolved.Target), Credentials: request.Resolved.Credentials})
	passed := err == nil && postgresqlInstanceIdentityMatches(result.Instance, request.Resolved.Target, request.Resolved.Cluster.EngineIdentity["system_identifier"])
	message := "PostgreSQL target status was collected"
	if postgresqlRepairAction(request) == postgresqlRepairResume {
		passed = passed && result.Instance.Role == model.RoleStandby && result.Instance.Health.State == model.HealthHealthy &&
			!strings.EqualFold(result.Instance.EngineMetadata["replay_paused"], "true") &&
			result.Instance.Replication.IOThread == model.ThreadRunning && result.Instance.Replication.SQLThread == model.ThreadRunning &&
			postgresqlSourceIdentityMatches(result.Instance.Replication.SourceIdentity, request.Resolved.Primary, request.Resolved.Cluster.EngineIdentity["system_identifier"])
		message = "PostgreSQL WAL replay is active"
	}
	status := model.CheckFail
	if passed {
		status = model.CheckPass
	} else {
		message = "PostgreSQL repair postcondition is unverified"
	}
	verification.Checks = []model.Check{{Name: "repair_postcondition", Status: status, Message: message}}
	verification.Passed = passed
	return verification, nil
}
