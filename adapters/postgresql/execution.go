package postgresql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	postgresqlFenceWritesSQL                 = `ALTER SYSTEM SET default_transaction_read_only = 'on'`
	postgresqlActivateWritesSQL              = `ALTER SYSTEM RESET default_transaction_read_only`
	postgresqlReloadConfigSQL                = `SELECT pg_reload_conf()`
	postgresqlTerminateClientsSQL            = `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'`
	postgresqlCaptureWALQuery                = `SELECT row_to_json(clusterguard_wal) FROM (SELECT pg_current_wal_flush_lsn()::text AS current_lsn) AS clusterguard_wal`
	postgresqlReplayWALQuery                 = `WITH receiver AS (SELECT status, latest_end_lsn FROM pg_stat_wal_receiver LIMIT 1) SELECT row_to_json(clusterguard_wal) FROM (SELECT COALESCE(pg_last_wal_receive_lsn()::text, '') AS receive_lsn, COALESCE(pg_last_wal_replay_lsn()::text, '') AS replay_lsn, COALESCE((SELECT status FROM receiver), '') AS receiver_status, COALESCE((SELECT latest_end_lsn::text FROM receiver), '') AS receiver_latest_end_lsn, pg_is_wal_replay_paused() AS replay_paused) AS clusterguard_wal`
	postgresqlVerificationConvergenceTimeout = 15 * time.Second
	postgresqlVerificationRetryInterval      = 250 * time.Millisecond
)

func postgresqlSwitchoverMutationTimeout() time.Duration {
	return 30 * time.Minute
}

type postgresqlFailure struct {
	class string
	err   error
}

func (failure *postgresqlFailure) Error() string        { return failure.err.Error() }
func (failure *postgresqlFailure) Unwrap() error        { return failure.err }
func (failure *postgresqlFailure) FailureClass() string { return failure.class }

func postgresqlPublicError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 320 {
		message = message[:320]
	}
	return message
}

func newPostgreSQLExecution(operationID model.ResourceID, status model.OperationStatus, started time.Time, message string) model.Execution {
	now := time.Now().UTC()
	return model.Execution{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operationID, Status: status, StartedAt: started, FinishedAt: now, Message: message,
	}
}

func postgresqlExecutionFailure(operationID model.ResourceID, started time.Time, status model.OperationStatus, class string, err error) (model.Execution, error) {
	message := postgresqlPublicError(err)
	return newPostgreSQLExecution(operationID, status, started, message), &postgresqlFailure{class: class, err: err}
}

func validatePostgreSQLPlan(request adapter.OperationRequest, kind model.OperationKind, verification bool) error {
	if request.Plan == nil || request.Resolved == nil {
		return fmt.Errorf("an immutable PostgreSQL operation plan and resolved context are required")
	}
	plan := *request.Plan
	digest, err := postgresqlPlanDigest(plan)
	if err != nil {
		return err
	}
	resolved := request.Resolved
	if plan.Digest == "" || plan.Digest != digest {
		return fmt.Errorf("PostgreSQL operation plan digest changed")
	}
	if request.Operation.Kind != kind || request.Operation.Engine != model.EnginePostgreSQL ||
		plan.OperationID != request.Operation.ResourceID || plan.ClusterID != request.Operation.ClusterID ||
		plan.SourceID != resolved.Primary.ResourceID || plan.TargetID != request.TargetID || plan.TargetID != resolved.Target.ResourceID {
		return fmt.Errorf("PostgreSQL operation plan resource scope changed")
	}
	if strings.TrimSpace(plan.ObservationToken) == "" || (!verification && plan.ObservationToken != postgresqlObservationToken(resolved)) {
		return fmt.Errorf("PostgreSQL operation observation changed")
	}
	if postgresqlBlockingChecks(plan.Checks) {
		return fmt.Errorf("PostgreSQL operation plan contains blocking checks")
	}
	semanticObservation := strings.TrimSpace(resolved.ObservationToken) != ""
	clusterRevision := plan.ResourceRevisions[resolved.Cluster.ResourceID]
	if resolved.Cluster.MetadataRevision == 0 || clusterRevision == 0 || (!semanticObservation && clusterRevision != resolved.Cluster.MetadataRevision) {
		return fmt.Errorf("PostgreSQL cluster revision changed from the approved plan")
	}
	seen := make(map[model.ResourceID]struct{}, len(resolved.Snapshot.Instances))
	for _, instance := range resolved.Snapshot.Instances {
		plannedRevision := plan.ResourceRevisions[instance.ResourceID]
		if !model.ValidResourceID(instance.ResourceID) || instance.MetadataRevision == 0 || plannedRevision == 0 || (!semanticObservation && plannedRevision != instance.MetadataRevision) {
			return fmt.Errorf("PostgreSQL node revision changed from the approved plan")
		}
		if _, duplicate := seen[instance.ResourceID]; duplicate {
			return fmt.Errorf("PostgreSQL topology contains duplicate resource identity")
		}
		seen[instance.ResourceID] = struct{}{}
	}
	_, primaryPresent := seen[resolved.Primary.ResourceID]
	_, targetPresent := seen[resolved.Target.ResourceID]
	if !primaryPresent || !targetPresent ||
		resolved.Primary.MetadataRevision == 0 || plan.ResourceRevisions[resolved.Primary.ResourceID] == 0 ||
		resolved.Target.MetadataRevision == 0 || plan.ResourceRevisions[resolved.Target.ResourceID] == 0 ||
		(!semanticObservation && (plan.ResourceRevisions[resolved.Primary.ResourceID] != resolved.Primary.MetadataRevision ||
			plan.ResourceRevisions[resolved.Target.ResourceID] != resolved.Target.MetadataRevision)) ||
		len(plan.ResourceRevisions) != len(seen)+1 {
		return fmt.Errorf("PostgreSQL operation resource revisions changed from the approved plan")
	}
	return nil
}

func postgresqlCompleteStep(ctx context.Context, request adapter.OperationRequest, step, message string) error {
	if request.Progress == nil {
		return nil
	}
	return request.Progress.CompleteStep(ctx, step, message)
}

func postgresqlStepCompleted(ctx context.Context, request adapter.OperationRequest, step string) (bool, error) {
	if request.Progress == nil {
		return false, nil
	}
	reader, ok := request.Progress.(adapter.OperationProgressReader)
	if !ok {
		return false, nil
	}
	return reader.StepCompleted(ctx, step)
}

func postgresqlStepResult(ctx context.Context, request adapter.OperationRequest, step string) (string, bool, error) {
	if request.Progress == nil {
		return "", false, nil
	}
	reader, ok := request.Progress.(adapter.OperationProgressResultReader)
	if !ok {
		completed, err := postgresqlStepCompleted(ctx, request, step)
		if completed && err == nil {
			return "", true, fmt.Errorf("durable PostgreSQL step result is unavailable")
		}
		return "", completed, err
	}
	return reader.StepResult(ctx, step)
}

func postgresqlAllMutationsCompleted(ctx context.Context, request adapter.OperationRequest) (bool, error) {
	if request.Plan == nil || request.Progress == nil {
		return false, nil
	}
	for _, step := range request.Plan.Steps {
		if !step.Mutating {
			continue
		}
		completed, err := postgresqlStepCompleted(ctx, request, step.Name)
		if err != nil || !completed {
			return false, err
		}
	}
	return true, nil
}

type postgresqlLiveStandbyTopology struct {
	source         model.DatabaseInstance
	target         model.DatabaseInstance
	targetTopology adapter.TopologyResult
}

func (adapterInstance *Adapter) postgresqlResolveLiveStandbyTopology(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	expectedSource model.DatabaseInstance,
	expectedTarget model.DatabaseInstance,
) (postgresqlLiveStandbyTopology, error) {
	observations := make([]adapter.TopologyObservation, 0, 2)
	for _, expected := range []model.DatabaseInstance{expectedSource, expectedTarget} {
		result, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{
			ClusterID: resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(expected), Credentials: resolved.Credentials,
		})
		if err != nil {
			return postgresqlLiveStandbyTopology{}, fmt.Errorf("discover pinned PostgreSQL node %s: %w", expected.ResourceID, err)
		}
		observations = append(observations, adapter.TopologyObservation{
			Endpoint:  postgresqlInstanceEndpoint(expected),
			Discovery: result,
		})
	}
	adapterInstance.ResolveTopologyObservations(observations)
	return postgresqlLiveStandbyTopology{
		source:         observations[0].Discovery.Instance,
		target:         observations[1].Discovery.Instance,
		targetTopology: observations[1].Topology,
	}, nil
}

func postgresqlLiveStandbyTopologyMatches(
	live postgresqlLiveStandbyTopology,
	expectedSource model.DatabaseInstance,
	expectedTarget model.DatabaseInstance,
	systemIdentifier string,
) bool {
	if !postgresqlInstanceIdentityMatches(live.source, expectedSource, systemIdentifier) ||
		!postgresqlInstanceIdentityMatches(live.target, expectedTarget, systemIdentifier) ||
		live.source.Role != model.RolePrimary || live.source.Health.State != model.HealthHealthy ||
		live.target.Role != model.RoleStandby || live.target.Health.State != model.HealthHealthy || !live.target.PromotionEligible ||
		!postgresqlSourceIdentityMatches(live.target.Replication.SourceIdentity, expectedSource, systemIdentifier) ||
		len(live.targetTopology.Links) != 1 {
		return false
	}
	link := live.targetTopology.Links[0]
	return link.Healthy &&
		postgresqlSourceIdentityMatches(link.SourceIdentity, expectedSource, systemIdentifier) &&
		postgresqlSourceIdentityMatches(link.TargetIdentity, expectedTarget, systemIdentifier)
}

func (adapterInstance *Adapter) postgresqlLiveSwitchoverPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) error {
	live, err := adapterInstance.postgresqlResolveLiveStandbyTopology(ctx, resolved, resolved.Primary, resolved.Target)
	if err != nil {
		return fmt.Errorf("revalidate PostgreSQL switchover topology: %w", err)
	}
	systemIdentifier := resolved.Cluster.EngineIdentity["system_identifier"]
	if !postgresqlInstanceIdentityMatches(live.source, resolved.Primary, systemIdentifier) ||
		!postgresqlInstanceIdentityMatches(live.target, resolved.Target, systemIdentifier) {
		return fmt.Errorf("live PostgreSQL source or target identity changed")
	}
	if !postgresqlLiveStandbyTopologyMatches(live, resolved.Primary, resolved.Target, systemIdentifier) {
		return fmt.Errorf("live PostgreSQL target is not verified by both peers as following the selected primary")
	}
	return nil
}

func (adapterInstance *Adapter) postgresqlLiveFailoverTargetPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) error {
	result, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{
		ClusterID: resolved.Cluster.ResourceID, Endpoint: postgresqlInstanceEndpoint(resolved.Target), Credentials: resolved.Credentials,
	})
	if err != nil {
		return fmt.Errorf("revalidate PostgreSQL failover target: %w", err)
	}
	live := result.Instance
	identityMatches := postgresqlInstanceIdentityMatches(live, resolved.Target, resolved.Cluster.EngineIdentity["system_identifier"])
	liveForEvidence := live
	if identityMatches {
		liveForEvidence.ResourceID = resolved.Target.ResourceID
	}
	safeSourceLoss := postgresqlSafeSourceLossEvidence(resolved.Cluster.EngineIdentity["system_identifier"], resolved.Primary, liveForEvidence)
	if !identityMatches {
		return fmt.Errorf("live PostgreSQL failover target identity, streaming state, or upstream changed")
	}
	if safeSourceLoss {
		return nil
	}

	liveTopology, err := adapterInstance.postgresqlResolveLiveStandbyTopology(ctx, resolved, resolved.Primary, resolved.Target)
	if err != nil {
		return fmt.Errorf("revalidate PostgreSQL failover topology: %w", err)
	}
	normalStreaming := postgresqlLiveStandbyTopologyMatches(
		liveTopology, resolved.Primary, resolved.Target, resolved.Cluster.EngineIdentity["system_identifier"],
	) && liveTopology.target.Replication.LagSeconds != nil && *liveTopology.target.Replication.LagSeconds == 0
	if !normalStreaming {
		return fmt.Errorf("live PostgreSQL failover target identity, streaming state, or upstream changed")
	}
	return nil
}

func capturePostgreSQLWAL(ctx context.Context, runner SQLRunner, resolved adapter.ResolvedOperation) (string, error) {
	rows, err := runner.Query(ctx, postgresqlInstanceEndpoint(resolved.Primary), resolved.Credentials, postgresqlCaptureWALQuery)
	if err != nil {
		return "", err
	}
	if len(rows) != 1 {
		return "", fmt.Errorf("PostgreSQL WAL capture returned %d rows", len(rows))
	}
	position := strings.TrimSpace(rows[0]["current_lsn"])
	if _, err := parseLSN(position); err != nil {
		return "", fmt.Errorf("invalid fenced PostgreSQL WAL position: %w", err)
	}
	return position, nil
}

func waitForPostgreSQLReplay(ctx context.Context, runner SQLRunner, resolved adapter.ResolvedOperation, position string) error {
	expected, err := parseLSN(position)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, queryErr := runner.Query(waitCtx, postgresqlInstanceEndpoint(resolved.Target), resolved.Credentials, postgresqlReplayWALQuery)
		if queryErr == nil && len(rows) == 1 && postgresqlReplayReached(rows[0], expected) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("PostgreSQL target did not replay fenced WAL %s: %w", position, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func postgresqlReplayReached(row Row, expected uint64) bool {
	replay, replayErr := parseLSN(row["replay_lsn"])
	paused, pausedErr := parsePostgreSQLBoolean(row["replay_paused"])
	// Replay is the durable promotion boundary. pg_last_wal_receive_lsn() can
	// reset after a standby restart and the receiver row disappears once the
	// fenced source stops, even though recovery already replayed that WAL.
	return replayErr == nil && pausedErr == nil && !paused && replay >= expected
}

func (adapterInstance *Adapter) restorePostgreSQLSourceWrites(ctx context.Context, resolved adapter.ResolvedOperation) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return executePostgreSQLStatements(
		recoveryCtx,
		adapterInstance.executor,
		postgresqlInstanceEndpoint(resolved.Primary),
		resolved.Credentials,
		postgresqlActivateWritesSQL,
		postgresqlReloadConfigSQL,
	)
}

func executePostgreSQLStatements(ctx context.Context, executor SQLExecutor, endpoint adapter.Endpoint, credentials adapter.Credentials, statements ...string) error {
	for index, statement := range statements {
		if err := executor.Exec(ctx, endpoint, credentials, statement); err != nil {
			return fmt.Errorf("PostgreSQL control statement %d failed: %w", index+1, err)
		}
	}
	return nil
}

func (adapterInstance *Adapter) fencePostgreSQLSourceWrites(ctx context.Context, resolved adapter.ResolvedOperation) error {
	return executePostgreSQLStatements(
		ctx,
		adapterInstance.executor,
		postgresqlInstanceEndpoint(resolved.Primary),
		resolved.Credentials,
		postgresqlFenceWritesSQL,
		postgresqlReloadConfigSQL,
		postgresqlTerminateClientsSQL,
	)
}

func (adapterInstance *Adapter) activatePostgreSQLTargetWrites(ctx context.Context, resolved adapter.ResolvedOperation) error {
	return executePostgreSQLStatements(
		ctx,
		adapterInstance.executor,
		postgresqlInstanceEndpoint(resolved.Target),
		resolved.Credentials,
		postgresqlActivateWritesSQL,
		postgresqlReloadConfigSQL,
	)
}

func (adapterInstance *Adapter) authorizePostgreSQLTransition(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	authorization, err := adapterInstance.endpointProvider.AuthorizeTransition(ctx, resolved)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	if authorization.Context == nil || authorization.Cancel == nil || authorization.Abort == nil || authorization.Finalize == nil || !model.ValidResourceID(authorization.LeaseID) {
		if authorization.Cancel != nil {
			authorization.Cancel()
		}
		return adapter.TransitionAuthorization{}, fmt.Errorf("writer endpoint returned an incomplete PostgreSQL transition lease")
	}
	if err := authorization.Context.Err(); err != nil {
		authorization.Cancel()
		return adapter.TransitionAuthorization{}, fmt.Errorf("PostgreSQL transition lease is inactive: %w", context.Cause(authorization.Context))
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(ctx), request, "authorize_target_transition", "target transition lease is active"); err != nil {
		authorization.Cancel()
		return adapter.TransitionAuthorization{}, err
	}
	return authorization, nil
}

func postgresqlWriterEndpointVerified(check model.Check) bool {
	return check.Name == "writer_endpoint_owner" && check.Status == model.CheckPass
}

func postgresqlOnlyWriterEndpointFailed(verification model.Verification) bool {
	found := false
	for _, check := range verification.Checks {
		if check.Status != model.CheckFail {
			continue
		}
		if check.Name != "writer_endpoint_owner" {
			return false
		}
		found = true
	}
	return found
}

func (adapterInstance *Adapter) reconcilePostgreSQLWriterEndpoint(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation, recorded bool, operation string) error {
	check := adapterInstance.endpointProvider.Verify(ctx, resolved)
	if !postgresqlWriterEndpointVerified(check) {
		if err := adapterInstance.endpointProvider.Transfer(ctx, resolved); err != nil {
			return fmt.Errorf("transfer %s writer endpoint: %w", operation, err)
		}
		check = adapterInstance.endpointProvider.Verify(ctx, resolved)
	}
	if !postgresqlWriterEndpointVerified(check) {
		return fmt.Errorf("%s writer endpoint ownership is unverified: %s", operation, strings.TrimSpace(check.Message))
	}
	if !recorded {
		if err := postgresqlCompleteStep(context.WithoutCancel(ctx), request, "transfer_writer_endpoint", "writer endpoint follows the new primary"); err != nil {
			return err
		}
	}
	return nil
}

func (adapterInstance *Adapter) reconcileCompletedPostgreSQLWriterEndpoint(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation, operation string) error {
	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	authorization, err := adapterInstance.authorizePostgreSQLTransition(mutationCtx, request, resolved)
	if err != nil {
		return fmt.Errorf("authorize %s endpoint reconciliation: %w", operation, err)
	}
	defer authorization.Cancel()
	if err := adapterInstance.reconcilePostgreSQLWriterEndpoint(authorization.Context, request, resolved, true, operation); err != nil {
		return err
	}
	if err := authorization.Finalize(authorization.Context); err != nil {
		return fmt.Errorf("stabilize %s endpoint lease: %w", operation, err)
	}
	return nil
}

func (adapterInstance *Adapter) switchoverExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.executor == nil || adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validatePostgreSQLPlan(request, model.OperationSwitchover, false); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	completed, err := postgresqlAllMutationsCompleted(ctx, request)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if completed {
		verification, verifyErr := adapterInstance.switchoverVerify(ctx, request)
		if verifyErr == nil && postgresqlOnlyWriterEndpointFailed(verification) {
			resolved := *request.Resolved
			resolved.PlanDigest = request.Plan.Digest
			if reconcileErr := adapterInstance.reconcileCompletedPostgreSQLWriterEndpoint(ctx, request, resolved, "PostgreSQL switchover"); reconcileErr != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", reconcileErr)
			}
			return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL switchover writer endpoint was reconciled; verification is required"), nil
		}
		if verifyErr != nil || !verification.Passed {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("completed PostgreSQL switchover is not verified")
			}
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", verifyErr)
		}
		return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL switchover was already completed and remains verified"), nil
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	if err := adapterInstance.postgresqlLiveSwitchoverPrecheck(ctx, resolved); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}

	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postgresqlSwitchoverMutationTimeout())
	defer cancel()
	authorization, err := adapterInstance.authorizePostgreSQLTransition(mutationCtx, request, resolved)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("authorize PostgreSQL transition: %w", err))
	}
	defer authorization.Cancel()
	mutationCtx = authorization.Context
	leaseID := authorization.LeaseID
	abortBeforeSourceStop := func(cause error, restoreWrites bool) (model.Execution, error) {
		recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer recoveryCancel()
		failures := []error{cause}
		cleanupFailed := false
		if restoreWrites {
			if restoreErr := adapterInstance.restorePostgreSQLSourceWrites(recoveryCtx, resolved); restoreErr != nil {
				failures = append(failures, fmt.Errorf("restore PostgreSQL source writes: %w", restoreErr))
				cleanupFailed = true
			}
		}
		if abortErr := authorization.Abort(recoveryCtx); abortErr != nil {
			failures = append(failures, fmt.Errorf("rollback PostgreSQL transition lease: %w", abortErr))
			cleanupFailed = true
		}
		if cleanupFailed {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", errors.Join(failures...))
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", cause)
	}
	rollbackAfterSourceStop := func(cause error) (model.Execution, error) {
		recoveryCtx, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer recoveryCancel()
		failures := []error{cause}
		cleanupFailed := false
		if startErr := adapterInstance.nodeController.Start(recoveryCtx, resolved, resolved.Primary, leaseID); startErr != nil {
			failures = append(failures, fmt.Errorf("restart PostgreSQL source: %w", startErr))
			cleanupFailed = true
		} else {
			running, inRecovery, statusErr := adapterInstance.nodeController.Status(recoveryCtx, resolved, resolved.Primary)
			if statusErr != nil || !running || inRecovery {
				if statusErr == nil {
					statusErr = fmt.Errorf("restarted PostgreSQL source role is not a running primary")
				}
				failures = append(failures, statusErr)
				cleanupFailed = true
			} else if restoreErr := adapterInstance.restorePostgreSQLSourceWrites(recoveryCtx, resolved); restoreErr != nil {
				failures = append(failures, fmt.Errorf("restore PostgreSQL source writes: %w", restoreErr))
				cleanupFailed = true
			}
		}
		if abortErr := authorization.Abort(recoveryCtx); abortErr != nil {
			failures = append(failures, fmt.Errorf("rollback PostgreSQL transition lease: %w", abortErr))
			cleanupFailed = true
		}
		if cleanupFailed {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", errors.Join(failures...))
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", cause)
	}

	fenced, err := postgresqlStepCompleted(mutationCtx, request, "fence_source_writes")
	if err != nil {
		return abortBeforeSourceStop(err, false)
	}
	if !fenced {
		if err := adapterInstance.fencePostgreSQLSourceWrites(mutationCtx, resolved); err != nil {
			return abortBeforeSourceStop(fmt.Errorf("fence PostgreSQL source writes: %w", err), true)
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "fence_source_writes", "source accepts no new writable client transactions"); err != nil {
			return abortBeforeSourceStop(err, true)
		}
	}

	position, captured, err := postgresqlStepResult(mutationCtx, request, "capture_source_wal")
	if err != nil {
		return abortBeforeSourceStop(err, true)
	}
	if !captured {
		position, err = capturePostgreSQLWAL(mutationCtx, adapterInstance.runner, resolved)
		if err != nil {
			return abortBeforeSourceStop(fmt.Errorf("capture fenced source WAL: %w", err), true)
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "capture_source_wal", position); err != nil {
			return abortBeforeSourceStop(err, true)
		}
	}
	if _, err := parseLSN(position); err != nil {
		return abortBeforeSourceStop(fmt.Errorf("durable source WAL is invalid: %w", err), true)
	}

	stoppedStep, err := postgresqlStepCompleted(mutationCtx, request, "stop_source")
	if err != nil {
		return abortBeforeSourceStop(err, true)
	}
	if !stoppedStep {
		if err := adapterInstance.nodeController.Stop(mutationCtx, resolved, resolved.Primary, leaseID); err != nil {
			stopped, statusErr := adapterInstance.nodeController.IsStopped(context.WithoutCancel(mutationCtx), resolved, resolved.Primary)
			if statusErr != nil || stopped {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", errors.Join(err, statusErr))
			}
			return abortBeforeSourceStop(fmt.Errorf("stop PostgreSQL source: %w", err), true)
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "stop_source", "source PostgreSQL service stop command completed"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", err)
		}
	}
	stopped, err := adapterInstance.nodeController.IsStopped(mutationCtx, resolved, resolved.Primary)
	if err != nil || !stopped {
		if err == nil {
			return abortBeforeSourceStop(fmt.Errorf("source PostgreSQL service remains active"), true)
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", err)
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "verify_source_stopped", "source service is stopped and cannot accept writes"); err != nil {
		return rollbackAfterSourceStop(err)
	}
	if err := waitForPostgreSQLReplay(mutationCtx, adapterInstance.runner, resolved, position); err != nil {
		return rollbackAfterSourceStop(err)
	}
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "wait_target_wal", position); err != nil {
		return rollbackAfterSourceStop(err)
	}

	promoted, err := postgresqlStepCompleted(mutationCtx, request, "promote_target")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", err)
	}
	if !promoted {
		running, inRecovery, statusErr := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
		if statusErr != nil || !running || !inRecovery {
			if statusErr == nil {
				statusErr = fmt.Errorf("target is not a running standby before promotion")
			}
			return rollbackAfterSourceStop(statusErr)
		}
		if err := adapterInstance.nodeController.Promote(mutationCtx, resolved, resolved.Target, leaseID); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "source_stopped", fmt.Errorf("promote PostgreSQL target: %w", err))
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "promote_target", "target left recovery mode"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}
	running, inRecovery, err := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
	if err != nil || !running || inRecovery {
		if err == nil {
			err = fmt.Errorf("promoted target role cannot be proven")
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}

	activated, err := postgresqlStepCompleted(mutationCtx, request, "activate_target_writes")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if !activated {
		if err := adapterInstance.activatePostgreSQLTargetWrites(mutationCtx, resolved); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("activate PostgreSQL target writes: %w", err))
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "activate_target_writes", "new primary accepts writable transactions"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}

	transferred, err := postgresqlStepCompleted(mutationCtx, request, "transfer_writer_endpoint")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := adapterInstance.reconcilePostgreSQLWriterEndpoint(mutationCtx, request, resolved, transferred, "PostgreSQL switchover"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := authorization.Finalize(mutationCtx); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("stabilize PostgreSQL writer endpoint lease: %w", err))
	}

	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		step := "repoint_follower_" + string(follower.ResourceID)
		completed, progressErr := postgresqlStepCompleted(mutationCtx, request, step)
		if progressErr != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", progressErr)
		}
		if !completed {
			if err := adapterInstance.nodeController.Repoint(mutationCtx, resolved, follower, resolved.Target, leaseID); err != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("repoint PostgreSQL follower: %w", err))
			}
			if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, step, "standby follows the new primary"); err != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
			}
		}
	}
	rewound, err := postgresqlStepCompleted(mutationCtx, request, "rewind_former_primary")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if !rewound {
		recoveryMessage := "former primary follows the new primary after pg_rewind"
		if rewindErr := adapterInstance.nodeController.Rewind(mutationCtx, resolved, resolved.Primary, resolved.Target, leaseID); rewindErr != nil {
			if baseBackupErr := adapterInstance.nodeController.BaseBackup(mutationCtx, resolved, resolved.Primary, resolved.Target, leaseID); baseBackupErr != nil {
				return postgresqlExecutionFailure(
					request.Operation.ResourceID,
					started,
					model.OperationIndeterminate,
					"promoted_unverified",
					errors.Join(
						fmt.Errorf("rewind PostgreSQL former primary: %w", rewindErr),
						fmt.Errorf("full base backup of PostgreSQL former primary: %w", baseBackupErr),
					),
				)
			}
			recoveryMessage = "former primary follows the new primary after full base backup fallback"
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "rewind_former_primary", recoveryMessage); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}
	return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL primary, standbys, and writer endpoint transitioned; verification is required"), nil
}

func (adapterInstance *Adapter) failoverExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.executor == nil || adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) || adapterInstance.failoverSafety == nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validatePostgreSQLPlan(request, model.OperationFailover, false); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	completed, err := postgresqlAllMutationsCompleted(ctx, request)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if completed {
		verification, verifyErr := adapterInstance.failoverVerify(ctx, request)
		if verifyErr == nil && postgresqlOnlyWriterEndpointFailed(verification) {
			resolved := *request.Resolved
			resolved.PlanDigest = request.Plan.Digest
			isolationCheck := adapterInstance.failoverSafety.Verify(ctx, resolved)
			if isolationCheck.Name != "old_primary_fenced" || isolationCheck.Status != model.CheckPass {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", fmt.Errorf("PostgreSQL old-primary isolation is unverified during endpoint reconciliation"))
			}
			resolved.VerifiedIsolatedSourceID = resolved.Primary.ResourceID
			if reconcileErr := adapterInstance.reconcileCompletedPostgreSQLWriterEndpoint(ctx, request, resolved, "PostgreSQL failover"); reconcileErr != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", reconcileErr)
			}
			return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL failover writer endpoint was reconciled; verification is required"), nil
		}
		if verifyErr != nil || !verification.Passed {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("completed PostgreSQL failover is not verified")
			}
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", verifyErr)
		}
		return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL failover was already completed and remains verified"), nil
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	promotedBeforeContinuation, err := postgresqlStepCompleted(ctx, request, "promote_target")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if !promotedBeforeContinuation {
		checks, precheckErr := adapterInstance.failoverPrecheck(ctx, request)
		if precheckErr != nil || postgresqlBlockingChecks(checks) {
			if precheckErr == nil {
				precheckErr = fmt.Errorf("PostgreSQL failover precheck changed or is blocked")
			}
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", precheckErr)
		}
		if err := adapterInstance.postgresqlLiveFailoverTargetPrecheck(ctx, resolved); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
		}
	}

	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	fenced, err := postgresqlStepCompleted(mutationCtx, request, "fence_old_primary")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if !fenced {
		if err := adapterInstance.failoverSafety.Fence(mutationCtx, resolved); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("fence failed PostgreSQL primary: %w", err))
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "fence_old_primary", "old primary isolation command completed"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", err)
		}
	}
	if check := adapterInstance.failoverSafety.Verify(mutationCtx, resolved); check.Name != "old_primary_fenced" || check.Status != model.CheckPass {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", fmt.Errorf("PostgreSQL old-primary isolation is unverified"))
	}
	resolved.VerifiedIsolatedSourceID = resolved.Primary.ResourceID
	if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "verify_old_primary_fenced", "old primary cannot serve writes or own the VIP"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", err)
	}

	authorization, err := adapterInstance.authorizePostgreSQLTransition(mutationCtx, request, resolved)
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("authorize PostgreSQL failover transition: %w", err))
	}
	defer authorization.Cancel()
	mutationCtx = authorization.Context
	leaseID := authorization.LeaseID

	promoted, err := postgresqlStepCompleted(mutationCtx, request, "promote_target")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fence_unknown", err)
	}
	if !promoted {
		running, inRecovery, statusErr := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
		if statusErr != nil || !running || !inRecovery {
			if statusErr == nil {
				statusErr = fmt.Errorf("failover target is not a running standby")
			}
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", statusErr)
		}
		if err := adapterInstance.nodeController.Promote(mutationCtx, resolved, resolved.Target, leaseID); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promotion_unknown", fmt.Errorf("promote PostgreSQL failover target: %w", err))
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "promote_target", "target left recovery mode"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}
	running, inRecovery, err := adapterInstance.nodeController.Status(mutationCtx, resolved, resolved.Target)
	if err != nil || !running || inRecovery {
		if err == nil {
			err = fmt.Errorf("promoted failover target role cannot be proven")
		}
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}

	activated, err := postgresqlStepCompleted(mutationCtx, request, "activate_target_writes")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if !activated {
		if err := adapterInstance.activatePostgreSQLTargetWrites(mutationCtx, resolved); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("activate PostgreSQL failover target writes: %w", err))
		}
		if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, "activate_target_writes", "new primary accepts writable transactions"); err != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}

	transferred, err := postgresqlStepCompleted(mutationCtx, request, "transfer_writer_endpoint")
	if err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := adapterInstance.reconcilePostgreSQLWriterEndpoint(mutationCtx, request, resolved, transferred, "PostgreSQL failover"); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := authorization.Finalize(mutationCtx); err != nil {
		return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("stabilize PostgreSQL failover endpoint lease: %w", err))
	}

	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID && postgresqlFailoverFollowerAvailable(&resolved, instance) {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		step := "repoint_follower_" + string(follower.ResourceID)
		done, progressErr := postgresqlStepCompleted(mutationCtx, request, step)
		if progressErr != nil {
			return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", progressErr)
		}
		if !done {
			if err := adapterInstance.nodeController.Repoint(mutationCtx, resolved, follower, resolved.Target, leaseID); err != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("repoint PostgreSQL failover follower: %w", err))
			}
			if err := postgresqlCompleteStep(context.WithoutCancel(mutationCtx), request, step, "standby follows the new primary"); err != nil {
				return postgresqlExecutionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
			}
		}
	}
	return newPostgreSQLExecution(request.Operation.ResourceID, model.OperationRunning, started, "PostgreSQL primary and writer endpoint failed over; verification is required"), nil
}

func (adapterInstance *Adapter) switchoverVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	if adapterInstance.executor == nil || adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return model.Verification{}, adapter.ErrUnsupported
	}
	now := time.Now().UTC()
	verification := model.Verification{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  request.Operation.ResourceID, ObservedAt: now,
	}
	if err := validatePostgreSQLPlan(request, model.OperationSwitchover, true); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	appendRoleCheck := func(name string, instance model.DatabaseInstance, expectRecovery bool) {
		running, recovery, err := adapterInstance.nodeController.Status(ctx, resolved, instance)
		if err == nil && running && recovery == expectRecovery {
			verification.Checks = append(verification.Checks, model.Check{Name: name, Status: model.CheckPass, Message: "PostgreSQL service role matches the completed transition"})
		} else {
			verification.Checks = append(verification.Checks, model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL service role is unverified"})
		}
	}
	appendRoleCheck("new_primary_role", resolved.Target, false)
	appendRoleCheck("former_primary_role", resolved.Primary, true)
	verification.Checks = append(verification.Checks,
		adapterInstance.postgresqlInstanceVerificationCheck(ctx, resolved, "new_primary_database", resolved.Target, model.RolePrimary, ""),
		adapterInstance.postgresqlInstanceVerificationCheck(ctx, resolved, "former_primary_database", resolved.Primary, model.RoleStandby, resolved.Target.ResourceID),
	)
	siblings := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID {
			siblings = append(siblings, instance)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].ResourceID < siblings[j].ResourceID })
	for _, instance := range siblings {
		appendRoleCheck("standby_role_"+string(instance.ResourceID), instance, true)
		verification.Checks = append(verification.Checks, adapterInstance.postgresqlInstanceVerificationCheck(
			ctx, resolved, "standby_database_"+string(instance.ResourceID), instance, model.RoleStandby, resolved.Target.ResourceID,
		))
	}
	verification.Checks = append(verification.Checks, adapterInstance.endpointProvider.Verify(ctx, resolved))
	verification.Passed = !postgresqlBlockingChecks(verification.Checks)
	return verification, nil
}

func (adapterInstance *Adapter) failoverVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	if adapterInstance.executor == nil || adapterInstance.nodeController == nil || !adapterInstance.nodeController.Executable(ctx) || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) || adapterInstance.failoverSafety == nil {
		return model.Verification{}, adapter.ErrUnsupported
	}
	now := time.Now().UTC()
	verification := model.Verification{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  request.Operation.ResourceID, ObservedAt: now,
	}
	if err := validatePostgreSQLPlan(request, model.OperationFailover, true); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	resolved := *request.Resolved
	resolved.PlanDigest = request.Plan.Digest
	running, inRecovery, roleErr := adapterInstance.nodeController.Status(ctx, resolved, resolved.Target)
	roleStatus, roleMessage := model.CheckPass, "new PostgreSQL primary is running outside recovery"
	if roleErr != nil || !running || inRecovery {
		roleStatus, roleMessage = model.CheckFail, "new PostgreSQL primary role is unverified"
	}
	verification.Checks = append(verification.Checks, model.Check{Name: "new_primary_role", Status: roleStatus, Message: roleMessage})
	verification.Checks = append(verification.Checks, adapterInstance.postgresqlInstanceVerificationCheck(
		ctx, resolved, "new_primary_database", resolved.Target, model.RolePrimary, "",
	))
	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		if !postgresqlFailoverFollowerAvailable(&resolved, follower) {
			verification.Checks = append(verification.Checks, model.Check{
				Name:    "standby_unavailable_" + string(follower.ResourceID),
				Status:  model.CheckWarn,
				Message: "PostgreSQL standby was already unavailable at failover decision time and requires a separate recovery operation",
			})
			continue
		}
		running, recovery, statusErr := adapterInstance.nodeController.Status(ctx, resolved, follower)
		status, message := model.CheckPass, "PostgreSQL standby service is running in recovery"
		if statusErr != nil || !running || !recovery {
			status, message = model.CheckFail, "PostgreSQL standby service role is unverified"
		}
		verification.Checks = append(verification.Checks,
			model.Check{Name: "standby_role_" + string(follower.ResourceID), Status: status, Message: message},
			adapterInstance.postgresqlInstanceVerificationCheck(ctx, resolved, "standby_database_"+string(follower.ResourceID), follower, model.RoleStandby, resolved.Target.ResourceID),
		)
	}
	isolationCheck := adapterInstance.failoverSafety.Verify(ctx, resolved)
	verification.Checks = append(verification.Checks, isolationCheck)
	if isolationCheck.Name == "old_primary_fenced" && isolationCheck.Status == model.CheckPass {
		resolved.VerifiedIsolatedSourceID = resolved.Primary.ResourceID
	}
	verification.Checks = append(verification.Checks, adapterInstance.endpointProvider.Verify(ctx, resolved))
	verification.Passed = !postgresqlBlockingChecks(verification.Checks)
	return verification, nil
}

func (adapterInstance *Adapter) postgresqlInstanceVerificationCheck(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	name string,
	expected model.DatabaseInstance,
	expectedRole model.InstanceRole,
	expectedSourceID model.ResourceID,
) model.Check {
	verifyCtx, cancel := context.WithTimeout(ctx, postgresqlVerificationConvergenceTimeout)
	defer cancel()

	for {
		check, retryable := adapterInstance.postgresqlInstanceVerificationCheckOnce(
			verifyCtx, resolved, name, expected, expectedRole, expectedSourceID,
		)
		if check.Status == model.CheckPass || !retryable {
			return check
		}

		timer := time.NewTimer(postgresqlVerificationRetryInterval)
		select {
		case <-verifyCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			check.Message += " before the bounded convergence deadline"
			return check
		case <-timer.C:
		}
	}
}

func (adapterInstance *Adapter) postgresqlInstanceVerificationCheckOnce(
	ctx context.Context,
	resolved adapter.ResolvedOperation,
	name string,
	expected model.DatabaseInstance,
	expectedRole model.InstanceRole,
	expectedSourceID model.ResourceID,
) (model.Check, bool) {
	systemIdentifier := resolved.Cluster.EngineIdentity["system_identifier"]
	if expectedRole == model.RoleStandby {
		expectedSource, sourceFound := postgresqlInstanceByResourceID(resolved, expectedSourceID)
		if !sourceFound {
			return model.Check{Name: name, Status: model.CheckFail, Message: "expected PostgreSQL replication source is missing from the pinned topology"}, false
		}
		live, err := adapterInstance.postgresqlResolveLiveStandbyTopology(ctx, resolved, expectedSource, expected)
		if err != nil {
			return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL source and standby state could not be discovered"}, true
		}
		if !postgresqlInstanceIdentityMatches(live.target, expected, systemIdentifier) {
			return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database identity does not match the pinned resource"}, false
		}
		if !postgresqlInstanceIdentityMatches(live.source, expectedSource, systemIdentifier) {
			return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL replication source identity does not match the pinned resource"}, false
		}
		if live.target.Role != model.RoleStandby || live.target.EngineMetadata["in_recovery"] != "true" {
			return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database is not the expected standby"}, false
		}
		if postgresqlLiveStandbyTopologyMatches(live, expectedSource, expected, systemIdentifier) {
			return model.Check{Name: name, Status: model.CheckPass, Message: "database identity, streaming state, and bilateral replication source are verified"}, false
		}
		return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL standby streaming topology has not converged at both peers"}, true
	}

	result, err := adapterInstance.Discover(ctx, adapter.DiscoverRequest{
		ClusterID:   resolved.Cluster.ResourceID,
		Endpoint:    postgresqlInstanceEndpoint(expected),
		Credentials: resolved.Credentials,
	})
	if err != nil {
		return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database state could not be discovered"}, true
	}
	instance := result.Instance
	identityMatches := postgresqlInstanceIdentityMatches(instance, expected, systemIdentifier)
	if !identityMatches {
		return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database identity does not match the pinned resource"}, false
	}
	roleMatches := instance.Role == expectedRole && instance.Health.State == model.HealthHealthy

	if expectedRole == model.RolePrimary {
		writable := instance.EngineMetadata["in_recovery"] == "false" && instance.EngineMetadata["transaction_read_only"] == "false"
		if roleMatches && writable {
			return model.Check{Name: name, Status: model.CheckPass, Message: "database identity and writable primary role are verified"}, false
		}
		return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database is not the expected writable primary"}, false
	}
	return model.Check{Name: name, Status: model.CheckFail, Message: "PostgreSQL database role is unsupported for verification"}, false
}
