package mysql

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type FailoverSafetyProvider interface {
	Precheck(context.Context, adapter.ResolvedOperation) []model.Check
	Fence(context.Context, adapter.ResolvedOperation) error
	Verify(context.Context, adapter.ResolvedOperation) model.Check
}

type UnsupportedFailoverSafetyProvider struct{}

func (UnsupportedFailoverSafetyProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{
		{Name: "stable_primary_failure", Status: model.CheckFail, Message: "stable primary-failure observation is not configured"},
		{Name: "controller_quorum", Status: model.CheckFail, Message: "controller quorum is not configured"},
		{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"},
	}
}
func (UnsupportedFailoverSafetyProvider) Fence(context.Context, adapter.ResolvedOperation) error {
	return adapter.ErrUnsupported
}
func (UnsupportedFailoverSafetyProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"}
}

func (adapterInstance *Adapter) selectedFailoverCandidate(resolved *adapter.ResolvedOperation) (model.CandidateAssessment, bool) {
	candidates := make([]model.DatabaseInstance, 0, len(resolved.Snapshot.Instances)-1)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID {
			candidates = append(candidates, instance)
		}
	}
	assessments := evaluateCandidates(adapter.CandidateRequest{
		Cluster: resolved.Cluster, Primary: resolved.Primary, Instances: candidates, Links: resolved.Snapshot.Links,
		Probes: resolved.Snapshot.Probes, ObservedAt: resolved.Snapshot.ObservedAt,
		Policy: model.CandidatePolicy{MaximumLagSeconds: 30, RequireGTID: true},
	})
	for _, assessment := range assessments {
		if assessment.InstanceID == resolved.Target.ResourceID {
			return assessment, true
		}
	}
	return model.CandidateAssessment{}, false
}

func (adapterInstance *Adapter) failoverPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationFailover || request.Resolved == nil {
		return nil, adapter.ErrUnsupported
	}
	resolved := request.Resolved
	checks := make([]model.Check, 0, 8)
	appendResult := func(name string, ok bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !ok {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	appendResult("primary_failure_state", resolved.Primary.Health.State == model.HealthUnhealthy || resolved.Primary.Health.State == model.HealthUnknown, "current primary is in a failed state", "failure switchover requires a failed or unreachable primary")
	assessment, found := adapterInstance.selectedFailoverCandidate(resolved)
	appendResult("recommended_candidate", found && assessment.Eligible && assessment.Rank == 1, "selected target is the lowest-risk eligible candidate", "selected target is not the lowest-risk eligible candidate")
	appendResult("data_loss_risk_known", found && assessment.DataLossRisk != dataLossRiskUnknown, "candidate data-loss risk is known: "+assessment.DataLossRisk, "candidate data-loss risk is unknown")
	for _, check := range adapterInstance.failoverSafety.Precheck(ctx, *resolved) {
		checks = append(checks, check)
	}
	appendResult("writer_endpoint_provider", adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx), "writer endpoint provider is executable", "writer endpoint provider is not executable")
	return checks, nil
}

func (adapterInstance *Adapter) failoverPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.failoverPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	steps := []model.PlanStep{
		{Index: 1, Name: "revalidate_failure_evidence", Owner: "platform", TargetID: resolved.Primary.ResourceID, Postcondition: "stable failure, quorum, and fencing evidence remain valid"},
		{Index: 2, Name: "fence_old_primary", Owner: "safety", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "old primary cannot serve writes or own the VIP"},
		{Index: 3, Name: "promote_failover_target", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "selected target is detached and writable"},
	}
	followers := make([]model.DatabaseInstance, 0)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Primary.ResourceID && instance.ResourceID != resolved.Target.ResourceID && instance.Health.State == model.HealthHealthy {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		steps = append(steps, model.PlanStep{Index: len(steps) + 1, Name: "reparent_failover_follower_" + string(follower.ResourceID), Owner: "mysql", TargetID: follower.ResourceID, Mutating: true, Postcondition: "reachable follower follows the new primary"})
	}
	steps = append(steps,
		model.PlanStep{Index: len(steps) + 1, Name: "transfer_failover_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "writer endpoint has one new-primary owner"},
		model.PlanStep{Index: len(steps) + 2, Name: "verify_failover", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "one writer, one endpoint owner, and old-primary isolation are verified"},
	)
	plan := model.OperationPlan{
		OperationID: request.Operation.ResourceID, ClusterID: resolved.Cluster.ResourceID, SourceID: resolved.Primary.ResourceID, TargetID: resolved.Target.ResourceID,
		Stage: model.StagePlan, ObservationToken: string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
		ResourceRevisions: planResourceRevisions(resolved), Checks: checks, Steps: steps, Mutating: true, Summary: "guarded MySQL failover is ready",
	}
	if planHasBlockingChecks(checks) {
		plan.Summary = "guarded MySQL failover is blocked"
	}
	plan.Digest, err = operationPlanDigest(plan)
	return plan, err
}

func (adapterInstance *Adapter) failoverExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.executor == nil || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validateMySQLPlan(request, model.OperationFailover, true); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	completed, err := allMutationStepsCompleted(ctx, request)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("read failover progress: %w", err))
	}
	if completed {
		verification, verifyErr := adapterInstance.failoverVerify(ctx, request)
		if verifyErr != nil || !verification.Passed {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("completed failover postconditions are not satisfied")
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", verifyErr)
		}
		return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "MySQL failover was already completed and remains verified"), nil
	}
	resolved := *request.Resolved
	if err := adapterInstance.failoverSafety.Fence(ctx, resolved); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("fence old primary: %w", err))
	}
	if check := adapterInstance.failoverSafety.Verify(ctx, resolved); check.Name != "old_primary_fenced" || check.Status != model.CheckPass {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("old-primary isolation could not be verified"))
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "fence_old_primary", "old primary is isolated"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", err)
	}
	targetEndpoint := instanceEndpoint(resolved.Target)
	identity, err := probeIdentity(ctx, adapterInstance.runner, targetEndpoint, resolved.Credentials)
	if err != nil || identity.serverUUID != strings.ToLower(strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"])) || (!identity.readOnly && !identity.superReadOnly) {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("failover target live identity or read-only state is unsafe"))
	}
	dialect, err := dialectForVersion(identity.version)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", err)
	}
	_, configured, err := probeReplication(ctx, adapterInstance.runner, targetEndpoint, resolved.Credentials)
	if err != nil || !configured {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("failover target replication state is unavailable"))
	}
	if err := adapterInstance.executor.Exec(ctx, targetEndpoint, resolved.Credentials, dialect.StopReplication); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", err)
	}
	if err := adapterInstance.executor.Exec(ctx, targetEndpoint, resolved.Credentials, dialect.ResetReplication); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", err)
	}
	if err := adapterInstance.executor.Exec(ctx, targetEndpoint, resolved.Credentials, setSuperReadOnlyOff); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := adapterInstance.executor.Exec(ctx, targetEndpoint, resolved.Credentials, setReadOnlyOff); err != nil {
		_ = adapterInstance.fenceInstance(ctx, targetEndpoint, resolved.Credentials)
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "promote_failover_target", "selected failover target is writable"); err != nil {
		_ = adapterInstance.fenceInstance(ctx, targetEndpoint, resolved.Credentials)
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	for _, follower := range resolved.Snapshot.Instances {
		if follower.ResourceID == resolved.Primary.ResourceID || follower.ResourceID == resolved.Target.ResourceID || follower.Health.State != model.HealthHealthy {
			continue
		}
		if err := adapterInstance.reparentFollower(ctx, follower, resolved.Target, resolved.Credentials, resolved.ReplicationCredentials); err != nil {
			_ = adapterInstance.fenceInstance(ctx, targetEndpoint, resolved.Credentials)
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("reparent reachable follower: %w", err))
		}
		if err := completeOperationStep(context.WithoutCancel(ctx), request, "reparent_failover_follower_"+string(follower.ResourceID), "reachable follower follows the new primary"); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
		}
	}
	if err := adapterInstance.endpointProvider.Transfer(ctx, resolved); err != nil {
		_ = adapterInstance.fenceInstance(ctx, targetEndpoint, resolved.Credentials)
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("transfer failover endpoint: %w", err))
	}
	if check := adapterInstance.endpointProvider.Verify(ctx, resolved); !endpointOwnerVerified(check) {
		_ = adapterInstance.fenceInstance(ctx, targetEndpoint, resolved.Credentials)
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("failover endpoint ownership is unverified"))
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "transfer_failover_endpoint", "writer endpoint follows the new primary"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", err)
	}
	return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "MySQL failover completed; verification is required"), nil
}

func (adapterInstance *Adapter) failoverVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	now := time.Now().UTC()
	verification := model.Verification{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}, OperationID: request.Operation.ResourceID, ObservedAt: now}
	if err := validateMySQLPlan(request, model.OperationFailover, false); err != nil {
		verification.Checks = []model.Check{{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()}}
		return verification, nil
	}
	resolved := request.Resolved
	targetIdentity, err := probeIdentity(ctx, adapterInstance.runner, instanceEndpoint(resolved.Target), resolved.Credentials)
	if err == nil && targetIdentity.serverUUID == strings.ToLower(strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"])) && !targetIdentity.readOnly && !targetIdentity.superReadOnly {
		verification.Checks = append(verification.Checks, model.Check{Name: "new_primary_writable", Status: model.CheckPass, Message: "selected target is the only controlled writable candidate"})
	} else {
		verification.Checks = append(verification.Checks, model.Check{Name: "new_primary_writable", Status: model.CheckFail, Message: "selected target writable state is unverified"})
	}
	for _, follower := range resolved.Snapshot.Instances {
		if follower.ResourceID == resolved.Primary.ResourceID || follower.ResourceID == resolved.Target.ResourceID || follower.Health.State != model.HealthHealthy {
			continue
		}
		checks, _ := adapterInstance.verifyFollower(ctx, follower, resolved.Target, resolved.Credentials)
		verification.Checks = append(verification.Checks, checks...)
	}
	verification.Checks = append(verification.Checks, adapterInstance.failoverSafety.Verify(ctx, *resolved))
	verification.Checks = append(verification.Checks, sanitizeEndpointCheck(adapterInstance.endpointProvider.Verify(ctx, *resolved), "writer_endpoint_owner"))
	verification.Passed = !planHasBlockingChecks(verification.Checks)
	return verification, nil
}
