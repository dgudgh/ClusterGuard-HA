package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	gtidPositionQuery   = "SELECT @@GLOBAL.gtid_executed AS gtid_executed"
	setSuperReadOnlyOn  = "SET GLOBAL super_read_only = ON"
	setReadOnlyOn       = "SET GLOBAL read_only = ON"
	setSuperReadOnlyOff = "SET GLOBAL super_read_only = OFF"
	setReadOnlyOff      = "SET GLOBAL read_only = OFF"
)

type switchoverFailure struct {
	class   string
	message string
	err     error
}

func (failure *switchoverFailure) Error() string        { return failure.message }
func (failure *switchoverFailure) Unwrap() error        { return failure.err }
func (failure *switchoverFailure) FailureClass() string { return failure.class }

func publicSwitchoverError(err error) string {
	if err == nil {
		return "MySQL switchover failed"
	}
	switch {
	case errors.Is(err, adapter.ErrUnsupported):
		return adapter.ErrUnsupported.Error()
	case errors.Is(err, context.Canceled):
		return "MySQL switchover request was canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "MySQL switchover step timed out"
	}
	message := strings.TrimSpace(err.Error())
	if separator := strings.Index(message, ": "); separator > 0 {
		message = message[:separator]
		var queryError *QueryError
		if errors.As(err, &queryError) && queryError.Code > 0 {
			return fmt.Sprintf("%s (MySQL error %d)", message, queryError.Code)
		}
		return message
	}
	return "MySQL switchover safety check failed"
}

func sanitizeEndpointCheck(check model.Check, expectedName string) model.Check {
	check.Name = strings.TrimSpace(check.Name)
	if check.Name == "" {
		return model.Check{Name: expectedName, Status: model.CheckFail, Message: "writer endpoint provider returned unnamed evidence"}
	}
	if check.Name != expectedName {
		return model.Check{Name: expectedName, Status: model.CheckFail, Message: "writer endpoint provider returned unexpected evidence"}
	}
	switch check.Status {
	case model.CheckPass:
		check.Message = "writer endpoint evidence passed"
	case model.CheckFail:
		check.Message = "writer endpoint evidence failed"
	default:
		check.Message = "writer endpoint evidence is incomplete"
	}
	return check
}

func appendSwitchoverCheck(checks *[]model.Check, name string, status model.CheckStatus, message string) {
	*checks = append(*checks, model.Check{Name: name, Status: status, Message: message})
}

func boolMetadata(metadata map[string]string, name string) (bool, bool) {
	value, found := metadata[name]
	if !found {
		return false, false
	}
	parsed, err := parseMySQLBoolean(value)
	return parsed, err == nil
}

func planHasBlockingChecks(checks []model.Check) bool {
	if len(checks) == 0 {
		return true
	}
	for _, check := range checks {
		if strings.TrimSpace(check.Name) == "" || check.Status != model.CheckPass {
			return true
		}
	}
	return false
}

func switchoverPlanHasBlockingChecks(checks []model.Check) bool {
	if len(checks) == 0 {
		return true
	}
	for _, check := range checks {
		if strings.TrimSpace(check.Name) == "" {
			return true
		}
		switch check.Status {
		case model.CheckPass, model.CheckWarn:
		default:
			return true
		}
	}
	return false
}

func (adapterInstance *Adapter) switchoverPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Kind != model.OperationSwitchover {
		return nil, adapter.ErrUnsupported
	}
	if request.Resolved == nil {
		return nil, fmt.Errorf("resolved MySQL operation context is required")
	}
	resolved := *request.Resolved
	checks := make([]model.Check, 0, 20)

	validScope := request.Operation.Engine == model.EngineMySQL &&
		resolved.Cluster.Engine == model.EngineMySQL &&
		request.Operation.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Snapshot.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Primary.ClusterID == resolved.Cluster.ResourceID &&
		resolved.Target.ClusterID == resolved.Cluster.ResourceID &&
		request.TargetID == resolved.Target.ResourceID &&
		model.ValidResourceID(resolved.Primary.ResourceID) && model.ValidResourceID(resolved.Target.ResourceID)
	if validScope {
		appendSwitchoverCheck(&checks, "inventory_membership", model.CheckPass, "source and target are UUID-scoped members of the selected MySQL cluster")
	} else {
		appendSwitchoverCheck(&checks, "inventory_membership", model.CheckFail, "source or target is outside the selected MySQL cluster inventory")
	}

	if resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy {
		appendSwitchoverCheck(&checks, "primary_health", model.CheckPass, "current primary is healthy")
	} else {
		appendSwitchoverCheck(&checks, "primary_health", model.CheckFail, "current primary must be healthy and have the primary role")
	}
	primaryReadOnly, primaryReadOnlyKnown := boolMetadata(resolved.Primary.EngineMetadata, "read_only")
	primarySuperReadOnly, primarySuperReadOnlyKnown := boolMetadata(resolved.Primary.EngineMetadata, "super_read_only")
	if primaryReadOnlyKnown && primarySuperReadOnlyKnown && !primaryReadOnly && !primarySuperReadOnly {
		appendSwitchoverCheck(&checks, "primary_writable", model.CheckPass, "current primary is writable")
	} else {
		appendSwitchoverCheck(&checks, "primary_writable", model.CheckFail, "current primary must have current writable-state evidence")
	}

	if resolved.Target.Role == model.RoleReplica {
		appendSwitchoverCheck(&checks, "target_role", model.CheckPass, "selected target is a replica")
	} else {
		appendSwitchoverCheck(&checks, "target_role", model.CheckFail, "selected target must be a replica")
	}
	if resolved.Target.Health.State == model.HealthHealthy {
		appendSwitchoverCheck(&checks, "target_health", model.CheckPass, "selected target is healthy")
	} else {
		appendSwitchoverCheck(&checks, "target_health", model.CheckFail, "selected target must be healthy")
	}
	if resolved.Target.PromotionEligible {
		appendSwitchoverCheck(&checks, "target_promotion", model.CheckPass, "selected target is promotion eligible")
	} else {
		appendSwitchoverCheck(&checks, "target_promotion", model.CheckFail, "selected target is not promotion eligible")
	}
	targetReadOnly, targetReadOnlyKnown := candidateReadOnly(resolved.Target.EngineMetadata)
	if targetReadOnlyKnown && targetReadOnly {
		appendSwitchoverCheck(&checks, "target_read_only", model.CheckPass, "selected target is read-only")
	} else {
		appendSwitchoverCheck(&checks, "target_read_only", model.CheckFail, "selected target must have current read-only evidence")
	}
	if resolved.Target.Maintenance {
		appendSwitchoverCheck(&checks, "target_maintenance", model.CheckFail, "selected target is in maintenance")
	} else {
		appendSwitchoverCheck(&checks, "target_maintenance", model.CheckPass, "selected target is not in maintenance")
	}

	if resolved.Target.Replication.IOThread == model.ThreadRunning && resolved.Target.Replication.SQLThread == model.ThreadRunning {
		appendSwitchoverCheck(&checks, "replication_threads", model.CheckPass, "replication IO and SQL threads are running")
	} else {
		appendSwitchoverCheck(&checks, "replication_threads", model.CheckFail, "replication IO and SQL threads must both be running")
	}
	primaryUUID := strings.ToLower(strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"]))
	sourceUUID := strings.ToLower(strings.TrimSpace(resolved.Target.Replication.SourceIdentity["server_uuid"]))
	if primaryUUID != "" && sourceUUID == primaryUUID {
		appendSwitchoverCheck(&checks, "replication_source", model.CheckPass, "selected target directly follows the current primary")
	} else {
		appendSwitchoverCheck(&checks, "replication_source", model.CheckFail, "selected target must directly follow the current primary")
	}
	if resolved.Target.Replication.LagSeconds != nil && *resolved.Target.Replication.LagSeconds == 0 {
		appendSwitchoverCheck(&checks, "replication_lag", model.CheckPass, "selected target has zero observed replication lag")
	} else {
		appendSwitchoverCheck(&checks, "replication_lag", model.CheckFail, "selected target must have known zero replication lag")
	}

	primaryGTID := strings.EqualFold(strings.TrimSpace(resolved.Primary.EngineMetadata["gtid_mode"]), "ON")
	targetGTID := strings.EqualFold(strings.TrimSpace(resolved.Target.EngineMetadata["gtid_mode"]), "ON")
	if primaryGTID && targetGTID {
		appendSwitchoverCheck(&checks, "gtid_mode", model.CheckPass, "GTID mode is ON for source and target")
	} else {
		appendSwitchoverCheck(&checks, "gtid_mode", model.CheckFail, "GTID mode must be ON for source and target")
	}
	primaryLogBin, primaryLogBinKnown := boolMetadata(resolved.Primary.EngineMetadata, "log_bin")
	targetLogBin, targetLogBinKnown := boolMetadata(resolved.Target.EngineMetadata, "log_bin")
	if primaryLogBinKnown && targetLogBinKnown && primaryLogBin && targetLogBin {
		appendSwitchoverCheck(&checks, "binary_logging", model.CheckPass, "binary logging is enabled for source and target")
	} else {
		appendSwitchoverCheck(&checks, "binary_logging", model.CheckFail, "binary logging must be enabled for source and target")
	}
	primarySet, primarySetError := ParseGTIDSet(resolved.Primary.EngineMetadata["gtid_executed"])
	targetSet, targetSetError := ParseGTIDSet(resolved.Target.Replication.ExecutedPosition)
	if primarySetError != nil || targetSetError != nil {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, "source or target GTID position is invalid")
	} else if comparison, err := CompareGTIDSets(primarySet, targetSet); err != nil {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, "source or target GTID position exceeds supported limits")
	} else if comparison.ErrantTransactions != 0 {
		temporalSkew, temporalSkewError := likelyTemporalGTIDSamplingSkew(
			primarySet,
			targetSet,
			primaryUUID,
			resolved.Primary.Health.ObservedAt,
			resolved.Target.Health.ObservedAt,
		)
		if temporalSkewError != nil {
			appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, "source or target GTID position exceeds supported limits")
		} else if temporalSkew {
			appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckWarn, "target has source-owned GTIDs from a later sample; live validation is required before execution")
		} else {
			appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckFail, fmt.Sprintf("target has %d missing and %d errant transactions", comparison.MissingTransactions, comparison.ErrantTransactions))
		}
	} else if comparison.MissingTransactions != 0 {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckWarn, fmt.Sprintf("target is missing %d transactions that will be caught up after source fencing", comparison.MissingTransactions))
	} else {
		appendSwitchoverCheck(&checks, "gtid_consistency", model.CheckPass, "source and target GTID histories are identical")
	}
	primaryFamily, primaryVersionError := mysqlReleaseFamily(resolved.Primary.EngineMetadata["version"])
	targetFamily, targetVersionError := mysqlReleaseFamily(resolved.Target.EngineMetadata["version"])
	if primaryVersionError == nil && targetVersionError == nil && primaryFamily == targetFamily {
		appendSwitchoverCheck(&checks, "version_compatibility", model.CheckPass, "source and target use the same MySQL release family")
	} else {
		appendSwitchoverCheck(&checks, "version_compatibility", model.CheckFail, "source and target MySQL release families are incompatible")
	}

	coverageComplete := !resolved.Snapshot.ObservedAt.IsZero() && len(resolved.Snapshot.Probes) >= len(resolved.Snapshot.Instances)
	for _, instance := range resolved.Snapshot.Instances {
		if !hasHealthyBoundProbe(resolved.Snapshot.Probes, instance.ResourceID, resolved.Snapshot.ObservedAt) {
			coverageComplete = false
			break
		}
	}
	if coverageComplete {
		appendSwitchoverCheck(&checks, "probe_coverage", model.CheckPass, "all explicit inventory probes are current and healthy")
	} else {
		appendSwitchoverCheck(&checks, "probe_coverage", model.CheckFail, "all explicit inventory probes must be current and healthy")
	}
	if len(resolved.Snapshot.Instances) >= 2 {
		appendSwitchoverCheck(&checks, "topology_scope", model.CheckPass, "all non-target inventory members will be reparented to the selected target")
	} else {
		appendSwitchoverCheck(&checks, "topology_scope", model.CheckFail, "planned switchover requires a primary and at least one replica")
	}
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID == resolved.Primary.ResourceID || instance.ResourceID == resolved.Target.ResourceID {
			continue
		}
		checks = append(checks, followerReadinessCheck(resolved.Primary, instance))
	}

	provider := adapterInstance.endpointProvider
	if provider == nil {
		provider = UnsupportedHAEndpointProvider{}
	}
	for _, check := range provider.Precheck(ctx, resolved) {
		checks = append(checks, sanitizeEndpointCheck(check, "writer_endpoint_provider"))
	}
	if !provider.Executable(ctx) {
		if !failedCheckNamed(checks, "writer_endpoint_provider") {
			appendSwitchoverCheck(&checks, "writer_endpoint_provider", model.CheckFail, "writer endpoint provider does not support execution")
		}
	} else if !passedCheckNamed(checks, "writer_endpoint_provider") {
		appendSwitchoverCheck(&checks, "writer_endpoint_provider", model.CheckFail, "writer endpoint provider did not return explicit ready evidence")
	}
	return checks, nil
}

func followerReadinessCheck(primary model.DatabaseInstance, follower model.DatabaseInstance) model.Check {
	name := "follower_readiness_" + string(follower.ResourceID)
	fail := func(message string) model.Check {
		return model.Check{Name: name, Status: model.CheckFail, Message: message}
	}
	if follower.Role != model.RoleReplica || follower.Health.State != model.HealthHealthy || follower.Maintenance {
		return fail("follower must be a healthy replica outside maintenance")
	}
	readOnly, known := candidateReadOnly(follower.EngineMetadata)
	if !known || !readOnly {
		return fail("follower must have current read-only evidence")
	}
	if follower.Replication.IOThread != model.ThreadRunning || follower.Replication.SQLThread != model.ThreadRunning {
		return fail("follower replication threads must both be running")
	}
	primaryUUID := strings.ToLower(strings.TrimSpace(primary.EngineIdentity["server_uuid"]))
	if primaryUUID == "" || strings.ToLower(strings.TrimSpace(follower.Replication.SourceIdentity["server_uuid"])) != primaryUUID {
		return fail("follower must directly follow the current primary")
	}
	if follower.Replication.LagSeconds == nil || *follower.Replication.LagSeconds != 0 {
		return fail("follower replication lag must be known and zero")
	}
	if !strings.EqualFold(strings.TrimSpace(follower.EngineMetadata["gtid_mode"]), "ON") {
		return fail("follower GTID mode must be ON")
	}
	logBin, logBinKnown := boolMetadata(follower.EngineMetadata, "log_bin")
	if !logBinKnown || !logBin {
		return fail("follower binary logging must be enabled")
	}
	primarySet, primaryErr := ParseGTIDSet(primary.EngineMetadata["gtid_executed"])
	followerSet, followerErr := ParseGTIDSet(follower.Replication.ExecutedPosition)
	if primaryErr != nil || followerErr != nil {
		return fail("follower GTID position is invalid")
	}
	comparison, err := CompareGTIDSets(primarySet, followerSet)
	if err != nil {
		return fail("follower GTID history is not identical to the current primary")
	}
	if comparison.ErrantTransactions != 0 {
		temporalSkew, temporalSkewError := likelyTemporalGTIDSamplingSkew(
			primarySet,
			followerSet,
			primaryUUID,
			primary.Health.ObservedAt,
			follower.Health.ObservedAt,
		)
		if temporalSkewError != nil || !temporalSkew {
			return fail("follower GTID history is not identical to the current primary")
		}
		return model.Check{Name: name, Status: model.CheckWarn, Message: "follower has source-owned GTIDs from a later sample; live validation is required before execution"}
	}
	if comparison.MissingTransactions != 0 {
		return model.Check{Name: name, Status: model.CheckWarn, Message: fmt.Sprintf("follower is missing %d transactions and will catch up after reparenting", comparison.MissingTransactions)}
	}
	primaryFamily, primaryVersionErr := mysqlReleaseFamily(primary.EngineMetadata["version"])
	followerFamily, followerVersionErr := mysqlReleaseFamily(follower.EngineMetadata["version"])
	if primaryVersionErr != nil || followerVersionErr != nil || primaryFamily != followerFamily {
		return fail("follower MySQL release family is incompatible")
	}
	return model.Check{Name: name, Status: model.CheckPass, Message: "follower is safe to reparent after promotion"}
}

func failedCheckNamed(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func passedCheckNamed(checks []model.Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == model.CheckPass {
			return true
		}
	}
	return false
}

func operationPlanDigest(plan model.OperationPlan) (string, error) {
	canonical := plan
	canonical.ResourceMeta = model.ResourceMeta{}
	canonical.Digest = ""
	contents, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical operation plan: %w", err)
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (adapterInstance *Adapter) switchoverPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	if !model.ValidResourceID(request.Operation.ResourceID) {
		return model.OperationPlan{}, fmt.Errorf("operation ID is required before planning")
	}
	checks, err := adapterInstance.switchoverPrecheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := *request.Resolved
	steps := []model.PlanStep{
		{Index: 1, Name: "revalidate_topology", Owner: "platform", TargetID: resolved.Cluster.ResourceID, Postcondition: "observation and resource revisions are unchanged"},
		{Index: 2, Name: "validate_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Postcondition: "endpoint provider is executable"},
		{Index: 3, Name: "fence_source", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "source is read-only"},
		{Index: 4, Name: "capture_source_gtid", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Postcondition: "source GTID position is captured after fencing"},
		{Index: 5, Name: "wait_target_gtid", Owner: "mysql", TargetID: resolved.Target.ResourceID, Postcondition: "target executed the fenced source GTID position"},
		{Index: 6, Name: "stop_target_replication", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target replication is stopped"},
		{Index: 7, Name: "authorize_target_transition", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target has an active transition lease"},
		{Index: 8, Name: "promote_target", Owner: "mysql", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target is writable"},
	}
	followers := make([]model.DatabaseInstance, 0, len(resolved.Snapshot.Instances)-1)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		steps = append(steps, model.PlanStep{
			Index: len(steps) + 1, Name: "reparent_follower_" + string(follower.ResourceID), Owner: "mysql",
			TargetID: follower.ResourceID, Mutating: true, Postcondition: "follower is read-only and replicates from the selected target",
		})
	}
	steps = append(steps,
		model.PlanStep{Index: len(steps) + 1, Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "writer endpoint has one target owner"},
		model.PlanStep{Index: len(steps) + 2, Name: "retain_source_read_only", Owner: "mysql", TargetID: resolved.Primary.ResourceID, Mutating: true, Postcondition: "former primary remains read-only"},
		model.PlanStep{Index: len(steps) + 3, Name: "verify_roles_and_endpoint", Owner: "platform", TargetID: resolved.Target.ResourceID, Postcondition: "one writable primary, healthy followers, and one endpoint owner are proven"},
	)
	resourceRevisions := map[model.ResourceID]uint64{resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision}
	for _, instance := range resolved.Snapshot.Instances {
		resourceRevisions[instance.ResourceID] = instance.MetadataRevision
	}
	plan := model.OperationPlan{
		OperationID:       request.Operation.ResourceID,
		ClusterID:         resolved.Cluster.ResourceID,
		SourceID:          resolved.Primary.ResourceID,
		TargetID:          resolved.Target.ResourceID,
		Stage:             model.StagePlan,
		ObservationToken:  string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		ResourceRevisions: resourceRevisions,
		Checks:            checks,
		Steps:             steps,
		Summary:           "guarded MySQL planned switchover is ready",
		Mutating:          true,
	}
	if switchoverPlanHasBlockingChecks(checks) {
		plan.Summary = "guarded MySQL planned switchover is blocked"
	}
	plan.Digest, err = operationPlanDigest(plan)
	if err != nil {
		return model.OperationPlan{}, err
	}
	return plan, nil
}

func instanceEndpoint(instance model.DatabaseInstance) adapter.Endpoint {
	return adapter.Endpoint{Hostname: instance.Hostname, IPAddress: instance.IPAddress, Port: instance.Port}
}

func validateExecutionPlan(request adapter.OperationRequest) error {
	if request.Plan == nil {
		return fmt.Errorf("an immutable operation plan is required")
	}
	if request.Resolved == nil {
		return fmt.Errorf("resolved MySQL operation context is required")
	}
	plan := *request.Plan
	digest, err := operationPlanDigest(plan)
	if err != nil {
		return err
	}
	resolved := *request.Resolved
	expectedObservation := string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if plan.Digest == "" || digest != plan.Digest {
		return fmt.Errorf("operation plan digest changed")
	}
	if plan.OperationID != request.Operation.ResourceID || plan.ClusterID != request.Operation.ClusterID ||
		plan.SourceID != resolved.Primary.ResourceID || plan.TargetID != request.TargetID || plan.TargetID != resolved.Target.ResourceID {
		return fmt.Errorf("operation plan resource scope changed")
	}
	if plan.ObservationToken != expectedObservation {
		return fmt.Errorf("operation plan observation token changed")
	}
	for resourceID, revision := range map[model.ResourceID]uint64{
		resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision,
	} {
		if revision == 0 || plan.ResourceRevisions[resourceID] != revision {
			return fmt.Errorf("operation plan resource revision changed")
		}
	}
	for _, instance := range resolved.Snapshot.Instances {
		if instance.MetadataRevision == 0 || plan.ResourceRevisions[instance.ResourceID] != instance.MetadataRevision {
			return fmt.Errorf("operation plan follower resource revision changed")
		}
	}
	if switchoverPlanHasBlockingChecks(plan.Checks) {
		return fmt.Errorf("operation plan contains blocking checks")
	}
	return nil
}

func validateVerificationPlan(request adapter.OperationRequest) error {
	if request.Plan == nil || request.Resolved == nil {
		return fmt.Errorf("an immutable operation plan and resolved context are required")
	}
	plan := *request.Plan
	digest, err := operationPlanDigest(plan)
	if err != nil {
		return err
	}
	resolved := *request.Resolved
	if plan.Digest == "" || digest != plan.Digest {
		return fmt.Errorf("operation plan digest changed")
	}
	if plan.OperationID != request.Operation.ResourceID || plan.ClusterID != request.Operation.ClusterID ||
		plan.SourceID != resolved.Primary.ResourceID || plan.TargetID != request.TargetID || plan.TargetID != resolved.Target.ResourceID {
		return fmt.Errorf("operation plan resource scope changed")
	}
	if strings.TrimSpace(plan.ObservationToken) == "" {
		return fmt.Errorf("operation plan observation token is missing")
	}
	resourceIDs := []model.ResourceID{resolved.Cluster.ResourceID}
	for _, instance := range resolved.Snapshot.Instances {
		resourceIDs = append(resourceIDs, instance.ResourceID)
	}
	for _, resourceID := range resourceIDs {
		if plan.ResourceRevisions[resourceID] == 0 {
			return fmt.Errorf("operation plan resource revisions are incomplete")
		}
	}
	if switchoverPlanHasBlockingChecks(plan.Checks) {
		return fmt.Errorf("operation plan contains blocking checks")
	}
	return nil
}

func newExecution(operationID model.ResourceID, status model.OperationStatus, started time.Time, message string) model.Execution {
	now := time.Now().UTC()
	return model.Execution{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  operationID,
		Status:       status,
		StartedAt:    started,
		FinishedAt:   now,
		Message:      message,
	}
}

func executionFailure(operationID model.ResourceID, started time.Time, status model.OperationStatus, class string, err error) (model.Execution, error) {
	message := publicSwitchoverError(err)
	return newExecution(operationID, status, started, message), &switchoverFailure{class: class, message: message, err: err}
}

func completeOperationStep(ctx context.Context, request adapter.OperationRequest, step string, message string) error {
	if request.Progress == nil {
		return nil
	}
	return request.Progress.CompleteStep(ctx, step, message)
}

func operationStepCompleted(ctx context.Context, request adapter.OperationRequest, step string) (bool, error) {
	if request.Progress == nil {
		return false, nil
	}
	reader, ok := request.Progress.(adapter.OperationProgressReader)
	if !ok {
		return false, nil
	}
	return reader.StepCompleted(ctx, step)
}

func allMutationStepsCompleted(ctx context.Context, request adapter.OperationRequest) (bool, error) {
	if request.Plan == nil || request.Progress == nil {
		return false, nil
	}
	for _, step := range request.Plan.Steps {
		if !step.Mutating {
			continue
		}
		completed, err := operationStepCompleted(ctx, request, step.Name)
		if err != nil {
			return false, err
		}
		if !completed {
			return false, nil
		}
	}
	return true, nil
}

func endpointOwnerVerified(check model.Check) bool {
	return check.Name == "writer_endpoint_owner" && check.Status == model.CheckPass
}

func queryFencedState(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (bool, error) {
	identity, err := probeIdentity(ctx, runner, endpoint, credentials)
	if err != nil {
		return false, err
	}
	return identity.readOnly && identity.superReadOnly, nil
}

func queryWritableState(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (bool, error) {
	identity, err := probeIdentity(ctx, runner, endpoint, credentials)
	if err != nil {
		return false, err
	}
	return !identity.readOnly && !identity.superReadOnly, nil
}

func waitForExecutedGTIDSet(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials, position string) error {
	position = strings.TrimSpace(position)
	if _, err := ParseGTIDSet(position); err != nil {
		return fmt.Errorf("GTID position is invalid: %w", err)
	}
	query := fmt.Sprintf("SELECT WAIT_FOR_EXECUTED_GTID_SET(%s, 30) AS wait_result", mysqlStringLiteral(position))
	rows, err := runner.Query(ctx, endpoint, credentials, query)
	if err != nil {
		return err
	}
	if len(rows) != 1 || strings.TrimSpace(rows[0]["wait_result"]) != "0" {
		return fmt.Errorf("GTID position was not executed within 30 seconds")
	}
	return nil
}

func queryExecutedGTIDSet(ctx context.Context, runner SQLRunner, endpoint adapter.Endpoint, credentials adapter.Credentials) (GTIDSet, error) {
	rows, err := runner.Query(ctx, endpoint, credentials, gtidPositionQuery)
	if err != nil {
		return GTIDSet{}, err
	}
	if len(rows) != 1 {
		return GTIDSet{}, fmt.Errorf("GTID position query returned %d rows", len(rows))
	}
	position, ok := rows[0]["gtid_executed"]
	if !ok {
		return GTIDSet{}, fmt.Errorf("GTID position query omitted gtid_executed")
	}
	set, err := ParseGTIDSet(position)
	if err != nil {
		return GTIDSet{}, fmt.Errorf("parse GTID position: %w", err)
	}
	return set, nil
}

func (adapterInstance *Adapter) liveSwitchoverPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) error {
	credentials := resolved.Credentials
	sourceEndpoint := instanceEndpoint(resolved.Primary)
	targetEndpoint := instanceEndpoint(resolved.Target)
	sourceIdentity, err := probeIdentity(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live source identity: %w", err)
	}
	targetIdentity, err := probeIdentity(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live target identity: %w", err)
	}
	if sourceIdentity.serverUUID != strings.ToLower(strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"])) ||
		targetIdentity.serverUUID != strings.ToLower(strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"])) {
		return fmt.Errorf("live MySQL identity no longer matches the immutable operation resources")
	}
	if sourceIdentity.readOnly || sourceIdentity.superReadOnly {
		return fmt.Errorf("live source is no longer writable before fencing")
	}
	if !targetIdentity.readOnly && !targetIdentity.superReadOnly {
		return fmt.Errorf("live target is writable before promotion")
	}
	if !strings.EqualFold(sourceIdentity.gtidMode, "ON") || !strings.EqualFold(targetIdentity.gtidMode, "ON") {
		return fmt.Errorf("live source and target must keep GTID mode ON")
	}
	sourceLogBin, sourceLogBinError := parseMySQLBoolean(sourceIdentity.logBin)
	targetLogBin, targetLogBinError := parseMySQLBoolean(targetIdentity.logBin)
	if sourceLogBinError != nil || targetLogBinError != nil || !sourceLogBin || !targetLogBin {
		return fmt.Errorf("live source and target must keep binary logging enabled")
	}
	sourceFamily, sourceVersionError := mysqlReleaseFamily(sourceIdentity.version)
	targetFamily, targetVersionError := mysqlReleaseFamily(targetIdentity.version)
	if sourceVersionError != nil || targetVersionError != nil || sourceFamily != targetFamily {
		return fmt.Errorf("live source and target MySQL release families are incompatible")
	}
	if _, err := dialectForVersion(targetIdentity.version); err != nil {
		return err
	}
	_, sourceReplicationConfigured, err := probeReplication(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live source replication: %w", err)
	}
	if sourceReplicationConfigured {
		return fmt.Errorf("live source unexpectedly has a replication source")
	}
	targetReplication, targetReplicationConfigured, err := probeReplication(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live target replication: %w", err)
	}
	if !targetReplicationConfigured || targetReplication.IOThread != model.ThreadRunning || targetReplication.SQLThread != model.ThreadRunning {
		return fmt.Errorf("live target replication threads are not both running")
	}
	if strings.ToLower(strings.TrimSpace(targetReplication.SourceIdentity["server_uuid"])) != sourceIdentity.serverUUID {
		return fmt.Errorf("live target no longer follows the selected source")
	}
	if targetReplication.LagSeconds == nil || *targetReplication.LagSeconds < 0 {
		return fmt.Errorf("live target replication lag must be known and non-negative")
	}
	targetSet, targetSetError := ParseGTIDSet(targetReplication.ExecutedPosition)
	if targetSetError != nil {
		return fmt.Errorf("live source or target GTID position is invalid")
	}
	sourceSet, sourceSetError := queryExecutedGTIDSet(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if sourceSetError != nil {
		return fmt.Errorf("refresh live source GTID after target sample: %w", sourceSetError)
	}
	comparison, err := CompareGTIDSets(sourceSet, targetSet)
	if err != nil || comparison.ErrantTransactions != 0 {
		return fmt.Errorf("live source and target GTID histories are not identical")
	}
	for _, follower := range resolved.Snapshot.Instances {
		if follower.ResourceID == resolved.Primary.ResourceID || follower.ResourceID == resolved.Target.ResourceID {
			continue
		}
		followerEndpoint := instanceEndpoint(follower)
		followerIdentity, err := probeIdentity(ctx, adapterInstance.runner, followerEndpoint, credentials)
		if err != nil {
			return fmt.Errorf("probe live follower identity: %w", err)
		}
		if followerIdentity.serverUUID != strings.ToLower(strings.TrimSpace(follower.EngineIdentity["server_uuid"])) {
			return fmt.Errorf("live follower identity no longer matches the immutable operation resource")
		}
		if !followerIdentity.readOnly || !followerIdentity.superReadOnly {
			return fmt.Errorf("live follower is not fully read-only")
		}
		if !strings.EqualFold(followerIdentity.gtidMode, "ON") {
			return fmt.Errorf("live follower must keep GTID mode ON")
		}
		followerLogBin, parseErr := parseMySQLBoolean(followerIdentity.logBin)
		if parseErr != nil || !followerLogBin {
			return fmt.Errorf("live follower must keep binary logging enabled")
		}
		followerFamily, versionErr := mysqlReleaseFamily(followerIdentity.version)
		if versionErr != nil || followerFamily != sourceFamily {
			return fmt.Errorf("live follower MySQL release family is incompatible")
		}
		followerReplication, configured, err := probeReplication(ctx, adapterInstance.runner, followerEndpoint, credentials)
		if err != nil {
			return fmt.Errorf("probe live follower replication: %w", err)
		}
		if !configured || followerReplication.IOThread != model.ThreadRunning || followerReplication.SQLThread != model.ThreadRunning {
			return fmt.Errorf("live follower replication threads are not both running")
		}
		if strings.ToLower(strings.TrimSpace(followerReplication.SourceIdentity["server_uuid"])) != sourceIdentity.serverUUID {
			return fmt.Errorf("live follower no longer follows the selected source")
		}
		if followerReplication.LagSeconds == nil || *followerReplication.LagSeconds < 0 {
			return fmt.Errorf("live follower replication lag must be known and non-negative")
		}
		followerSet, parseErr := ParseGTIDSet(followerReplication.ExecutedPosition)
		if parseErr != nil {
			return fmt.Errorf("live follower GTID position is invalid")
		}
		refreshedSourceSet, refreshErr := queryExecutedGTIDSet(ctx, adapterInstance.runner, sourceEndpoint, credentials)
		if refreshErr != nil {
			return fmt.Errorf("refresh live source GTID after follower sample: %w", refreshErr)
		}
		followerComparison, compareErr := CompareGTIDSets(refreshedSourceSet, followerSet)
		if compareErr != nil || followerComparison.ErrantTransactions != 0 {
			return fmt.Errorf("live source and follower GTID histories are not identical")
		}
	}
	return nil
}

func (adapterInstance *Adapter) liveSwitchoverResumePrecheck(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation) error {
	credentials := resolved.Credentials
	sourceEndpoint := instanceEndpoint(resolved.Primary)
	targetEndpoint := instanceEndpoint(resolved.Target)
	sourceIdentity, err := probeIdentity(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live source identity: %w", err)
	}
	targetIdentity, err := probeIdentity(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live target identity: %w", err)
	}
	if sourceIdentity.serverUUID != strings.ToLower(strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"])) ||
		targetIdentity.serverUUID != strings.ToLower(strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"])) {
		return fmt.Errorf("live MySQL identity no longer matches the immutable operation resources")
	}
	if !sourceIdentity.readOnly || !sourceIdentity.superReadOnly {
		return fmt.Errorf("live source is not fully fenced during operation recovery")
	}
	if !strings.EqualFold(sourceIdentity.gtidMode, "ON") || !strings.EqualFold(targetIdentity.gtidMode, "ON") {
		return fmt.Errorf("live source and target must keep GTID mode ON")
	}
	sourceLogBin, sourceLogBinError := parseMySQLBoolean(sourceIdentity.logBin)
	targetLogBin, targetLogBinError := parseMySQLBoolean(targetIdentity.logBin)
	if sourceLogBinError != nil || targetLogBinError != nil || !sourceLogBin || !targetLogBin {
		return fmt.Errorf("live source and target must keep binary logging enabled")
	}
	sourceFamily, sourceVersionError := mysqlReleaseFamily(sourceIdentity.version)
	targetFamily, targetVersionError := mysqlReleaseFamily(targetIdentity.version)
	if sourceVersionError != nil || targetVersionError != nil || sourceFamily != targetFamily {
		return fmt.Errorf("live source and target MySQL release families are incompatible")
	}
	if _, err := dialectForVersion(targetIdentity.version); err != nil {
		return err
	}
	promoted, err := operationStepCompleted(ctx, request, "promote_target")
	if err != nil {
		return fmt.Errorf("read target promotion progress: %w", err)
	}
	sourceReparented, err := operationStepCompleted(ctx, request, "reparent_follower_"+string(resolved.Primary.ResourceID))
	if err != nil {
		return fmt.Errorf("read former-primary reparent progress: %w", err)
	}
	sourceReplication, sourceReplicationConfigured, err := probeReplication(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live source replication: %w", err)
	}
	if sourceReplicationConfigured {
		if !sourceReparented {
			return fmt.Errorf("live source has a replication source without durable reparent progress")
		}
		if sourceReplication.IOThread != model.ThreadRunning || sourceReplication.SQLThread != model.ThreadRunning {
			return fmt.Errorf("live former-primary replication threads are not both running")
		}
		if strings.ToLower(strings.TrimSpace(sourceReplication.SourceIdentity["server_uuid"])) != targetIdentity.serverUUID {
			return fmt.Errorf("live former primary does not follow the selected target")
		}
		if sourceReplication.LagSeconds == nil || *sourceReplication.LagSeconds != 0 {
			return fmt.Errorf("live former-primary replication lag must be known and zero")
		}
	} else if sourceReparented {
		return fmt.Errorf("durably reparented former primary no longer has a replication source")
	}
	targetReplication, targetReplicationConfigured, err := probeReplication(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return fmt.Errorf("probe live target replication: %w", err)
	}
	targetFenced := targetIdentity.readOnly && targetIdentity.superReadOnly
	targetWritable := !targetIdentity.readOnly && !targetIdentity.superReadOnly
	if !targetFenced && !targetWritable {
		return fmt.Errorf("live target has a partial read-only state during operation recovery")
	}
	if targetFenced {
		if targetReplicationConfigured {
			if promoted {
				return fmt.Errorf("durably promoted target unexpectedly has a replication source")
			}
			if targetReplication.IOThread != model.ThreadRunning || targetReplication.SQLThread != model.ThreadRunning {
				return fmt.Errorf("live target replication threads are not both running")
			}
			if strings.ToLower(strings.TrimSpace(targetReplication.SourceIdentity["server_uuid"])) != sourceIdentity.serverUUID {
				return fmt.Errorf("live target no longer follows the selected source")
			}
			if targetReplication.LagSeconds == nil || *targetReplication.LagSeconds != 0 {
				return fmt.Errorf("live target replication lag must be known and zero")
			}
		} else if !promoted {
			return fmt.Errorf("live target is detached without durable promotion progress")
		}
	} else {
		if !promoted {
			return fmt.Errorf("live target is writable without durable promotion progress")
		}
		if targetReplicationConfigured {
			return fmt.Errorf("promoted target still has a replication source")
		}
	}
	sourceSet, sourceSetError := ParseGTIDSet(sourceIdentity.gtidExecuted)
	targetSet, targetSetError := ParseGTIDSet(targetIdentity.gtidExecuted)
	if sourceSetError != nil || targetSetError != nil {
		return fmt.Errorf("live source or target GTID position is invalid")
	}
	comparison, err := CompareGTIDSets(sourceSet, targetSet)
	if err != nil || comparison.MissingTransactions != 0 || (targetFenced && comparison.ErrantTransactions != 0) {
		return fmt.Errorf("live source and target GTID histories are unsafe for operation recovery")
	}
	return nil
}

func (adapterInstance *Adapter) fenceInstance(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	fenced, err := queryFencedState(recoveryContext, adapterInstance.runner, endpoint, credentials)
	if err == nil && fenced {
		return nil
	}
	if err := adapterInstance.executor.Exec(recoveryContext, endpoint, credentials, setSuperReadOnlyOn); err != nil {
		return err
	}
	return adapterInstance.executor.Exec(recoveryContext, endpoint, credentials, setReadOnlyOn)
}

func (adapterInstance *Adapter) authorizeEndpointTransition(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	authorization, err := adapterInstance.endpointProvider.AuthorizeTransition(ctx, resolved)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	if authorization.Context == nil || authorization.Cancel == nil || authorization.Finalize == nil {
		return adapter.TransitionAuthorization{}, fmt.Errorf("endpoint provider returned an incomplete transition authorization")
	}
	if err := authorization.Context.Err(); err != nil {
		authorization.Cancel()
		return adapter.TransitionAuthorization{}, fmt.Errorf("target transition authorization is inactive: %w", context.Cause(authorization.Context))
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "authorize_target_transition", "target transition lease is active"); err != nil {
		authorization.Cancel()
		return adapter.TransitionAuthorization{}, err
	}
	return authorization, nil
}

func (adapterInstance *Adapter) restoreCandidateBeforePromotion(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	promoted, err := operationStepCompleted(recoveryContext, request, "promote_target")
	if err != nil {
		return fmt.Errorf("read target promotion progress before candidate restoration: %w", err)
	}
	if promoted {
		return fmt.Errorf("candidate has durable promotion progress and cannot be restored as a replica")
	}
	if err := adapterInstance.reparentFollower(recoveryContext, resolved.Target, resolved.Primary, resolved.Credentials, resolved.ReplicationCredentials); err != nil {
		return fmt.Errorf("restore candidate replication to fenced source: %w", err)
	}
	return nil
}

func (adapterInstance *Adapter) failBeforeCandidatePromotion(ctx context.Context, request adapter.OperationRequest, resolved adapter.ResolvedOperation, started time.Time, cause error) (model.Execution, error) {
	if err := adapterInstance.restoreCandidateBeforePromotion(ctx, request, resolved); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced_unknown", fmt.Errorf("%v; candidate restoration failed: %w", cause, err))
	}
	return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("%v; candidate replication was restored to the fenced source", cause))
}

func (adapterInstance *Adapter) switchoverExecute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if request.Operation.Kind != model.OperationSwitchover {
		return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if adapterInstance.executor == nil || adapterInstance.endpointProvider == nil || !adapterInstance.endpointProvider.Executable(ctx) {
		return executionFailure(request.Operation.ResourceID, started, model.OperationUnsupported, "pre_commit", adapter.ErrUnsupported)
	}
	if err := validateExecutionPlan(request); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	if err := ctx.Err(); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
	}
	resolved := *request.Resolved
	credentials := resolved.Credentials
	sourceEndpoint := instanceEndpoint(resolved.Primary)
	targetEndpoint := instanceEndpoint(resolved.Target)
	completed, err := allMutationStepsCompleted(ctx, request)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("read completed operation progress: %w", err))
	}
	if completed {
		verification, verifyErr := adapterInstance.switchoverVerify(ctx, request)
		if verifyErr != nil || !verification.Passed {
			if verifyErr == nil {
				verifyErr = fmt.Errorf("completed operation postconditions are not satisfied")
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", verifyErr)
		}
		return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "MySQL switchover was already completed and remains verified"), nil
	}

	sourceFenced, err := queryFencedState(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("probe source fencing state: %w", err))
	}
	if sourceFenced {
		ownedFence, err := operationStepCompleted(ctx, request, "fence_source")
		if err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("read source fencing progress: %w", err))
		}
		if !ownedFence {
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "pre_commit", fmt.Errorf("source is fenced without durable ownership by this operation"))
		}
		if err := adapterInstance.liveSwitchoverResumePrecheck(ctx, request, resolved); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", err)
		}
	} else {
		if err := adapterInstance.liveSwitchoverPrecheck(ctx, resolved); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", err)
		}
		if err := adapterInstance.executor.Exec(ctx, sourceEndpoint, credentials, setSuperReadOnlyOn); err != nil {
			probeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if fenced, probeErr := queryFencedState(probeContext, adapterInstance.runner, sourceEndpoint, credentials); probeErr != nil {
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "fenced_unknown", fmt.Errorf("source fencing result is unknown after error: %w", err))
			} else if fenced {
				return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("source was fenced but the fencing command returned an error: %w", err))
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationFailed, "pre_commit", fmt.Errorf("fence source: %w", err))
		}
		sourceFenced = true
		if err := adapterInstance.executor.Exec(ctx, sourceEndpoint, credentials, setReadOnlyOn); err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("confirm source read-only state: %w", err))
		}
	}
	if err := completeOperationStep(context.WithoutCancel(ctx), request, "fence_source", "source is read-only and fenced"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("persist source fencing progress: %w", err))
	}

	mutationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	rows, err := adapterInstance.runner.Query(mutationContext, sourceEndpoint, credentials, gtidPositionQuery)
	if err != nil || len(rows) != 1 || strings.TrimSpace(rows[0]["gtid_executed"]) == "" {
		if err == nil {
			err = fmt.Errorf("source GTID response is empty")
		}
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("capture fenced source GTID: %w", err))
	}
	gtidPosition := strings.TrimSpace(rows[0]["gtid_executed"])
	if _, err := ParseGTIDSet(gtidPosition); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("validate fenced source GTID: %w", err))
	}
	if err := completeOperationStep(mutationContext, request, "capture_source_gtid", "fenced source GTID position captured"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("persist source GTID progress: %w", err))
	}
	if err := waitForExecutedGTIDSet(mutationContext, adapterInstance.runner, targetEndpoint, credentials, gtidPosition); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("target did not execute the fenced source GTID: %w", err))
	}
	if err := completeOperationStep(mutationContext, request, "wait_target_gtid", "target executed the fenced source GTID position"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("persist target catch-up progress: %w", err))
	}
	authorization, err := adapterInstance.authorizeEndpointTransition(mutationContext, request, resolved)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("authorize target transition: %w", err))
	}
	defer authorization.Cancel()
	mutationContext = authorization.Context

	_, replicationConfigured, err := probeReplication(mutationContext, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("probe target replication before promotion: %w", err))
	}
	if replicationConfigured {
		dialect, err := dialectForVersion(resolved.Target.EngineMetadata["version"])
		if err != nil {
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", err)
		}
		if err := adapterInstance.executor.Exec(mutationContext, targetEndpoint, credentials, dialect.StopReplication); err != nil {
			return adapterInstance.failBeforeCandidatePromotion(mutationContext, request, resolved, started, fmt.Errorf("stop target replication: %w", err))
		}
		if err := adapterInstance.executor.Exec(mutationContext, targetEndpoint, credentials, dialect.ResetReplication); err != nil {
			return adapterInstance.failBeforeCandidatePromotion(mutationContext, request, resolved, started, fmt.Errorf("reset target replication: %w", err))
		}
	}
	if err := completeOperationStep(mutationContext, request, "stop_target_replication", "target replication is stopped and detached"); err != nil {
		return adapterInstance.failBeforeCandidatePromotion(mutationContext, request, resolved, started, fmt.Errorf("persist target replication progress: %w", err))
	}

	targetWritable, err := queryWritableState(mutationContext, adapterInstance.runner, targetEndpoint, credentials)
	if err != nil {
		return adapterInstance.failBeforeCandidatePromotion(mutationContext, request, resolved, started, fmt.Errorf("probe target writable state: %w", err))
	}
	if !targetWritable {
		if err := adapterInstance.executor.Exec(mutationContext, targetEndpoint, credentials, setSuperReadOnlyOff); err != nil {
			if fenceErr := adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials); fenceErr != nil {
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("prepare target promotion failed and target fencing failed: %w", fenceErr))
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationBlocked, "fenced", fmt.Errorf("prepare target promotion: %w", err))
		}
		if err := adapterInstance.executor.Exec(mutationContext, targetEndpoint, credentials, setReadOnlyOff); err != nil {
			_ = adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("target promotion result is uncertain: %w", err))
		}
	}
	if err := completeOperationStep(mutationContext, request, "promote_target", "selected target is writable"); err != nil {
		_ = adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("persist target promotion progress: %w", err))
	}
	followers := make([]model.DatabaseInstance, 0, len(resolved.Snapshot.Instances)-1)
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID != resolved.Target.ResourceID {
			followers = append(followers, instance)
		}
	}
	sort.Slice(followers, func(i, j int) bool { return followers[i].ResourceID < followers[j].ResourceID })
	for _, follower := range followers {
		step := "reparent_follower_" + string(follower.ResourceID)
		completed, progressErr := operationStepCompleted(mutationContext, request, step)
		if progressErr != nil {
			_ = adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("read follower reparent progress: %w", progressErr))
		}
		if !completed {
			if err := adapterInstance.reparentFollower(mutationContext, follower, resolved.Target, credentials, resolved.ReplicationCredentials); err != nil {
				_ = adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("reparent follower: %w", err))
			}
			if err := completeOperationStep(mutationContext, request, step, "follower is read-only and follows the selected target"); err != nil {
				_ = adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("persist follower reparent progress: %w", err))
			}
		}
	}
	endpointEvidence := sanitizeEndpointCheck(adapterInstance.endpointProvider.Verify(mutationContext, resolved), "writer_endpoint_owner")
	if !endpointOwnerVerified(endpointEvidence) {
		if endpointEvidence.Name != "writer_endpoint_owner" || endpointEvidence.Status != model.CheckFail {
			if err := adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials); err != nil {
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("writer endpoint ownership is unknown and target fencing failed: %w", err))
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("writer endpoint ownership is unknown before transfer"))
		}
		if err := adapterInstance.endpointProvider.Transfer(mutationContext, resolved); err != nil {
			fenceErr := adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials)
			if fenceErr != nil {
				err = fmt.Errorf("writer endpoint transfer failed (%v) and target fencing failed: %w", err, fenceErr)
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("writer endpoint transfer is unverified: %w", err))
		}
		endpointEvidence = sanitizeEndpointCheck(adapterInstance.endpointProvider.Verify(mutationContext, resolved), "writer_endpoint_owner")
		if !endpointOwnerVerified(endpointEvidence) {
			if fenceErr := adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials); fenceErr != nil {
				return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("writer endpoint transfer postcondition is unverified and target fencing failed: %w", fenceErr))
			}
			return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("writer endpoint transfer postcondition is unverified"))
		}
	}
	if err := authorization.Finalize(mutationContext); err != nil {
		if fenceErr := adapterInstance.fenceInstance(mutationContext, targetEndpoint, credentials); fenceErr != nil {
			err = fmt.Errorf("%v; target fencing failed: %w", err, fenceErr)
		}
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("stabilize writer endpoint lease: %w", err))
	}
	if err := completeOperationStep(mutationContext, request, "transfer_writer_endpoint", "writer endpoint transferred to selected target"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("persist writer endpoint progress: %w", err))
	}
	if err := completeOperationStep(mutationContext, request, "retain_source_read_only", "former primary remains read-only"); err != nil {
		return executionFailure(request.Operation.ResourceID, started, model.OperationIndeterminate, "promoted_unverified", fmt.Errorf("persist former-primary state progress: %w", err))
	}
	return newExecution(request.Operation.ResourceID, model.OperationRunning, started, "MySQL role transition and writer endpoint transfer completed; verification is required"), nil
}

func (adapterInstance *Adapter) switchoverVerify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	now := time.Now().UTC()
	verification := model.Verification{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  request.Operation.ResourceID,
		ObservedAt:   now,
		Checks:       []model.Check{},
	}
	if request.Operation.Kind != model.OperationSwitchover || request.Resolved == nil {
		return verification, adapter.ErrUnsupported
	}
	if err := validateVerificationPlan(request); err != nil {
		verification.Checks = append(verification.Checks, model.Check{Name: "plan_integrity", Status: model.CheckFail, Message: err.Error()})
		return verification, nil
	}
	resolved := *request.Resolved
	credentials := resolved.Credentials
	sourceEndpoint := instanceEndpoint(resolved.Primary)
	targetEndpoint := instanceEndpoint(resolved.Target)

	sourceIdentity, sourceErr := probeIdentity(ctx, adapterInstance.runner, sourceEndpoint, credentials)
	if sourceErr != nil {
		verification.Checks = append(verification.Checks, model.Check{Name: "source_reachable", Status: model.CheckFail, Message: "former primary reachability is unknown: " + sourceErr.Error()})
	} else {
		if sourceIdentity.serverUUID == strings.ToLower(strings.TrimSpace(resolved.Primary.EngineIdentity["server_uuid"])) {
			verification.Checks = append(verification.Checks, model.Check{Name: "source_identity", Status: model.CheckPass, Message: "former primary identity matches the immutable source resource"})
		} else {
			verification.Checks = append(verification.Checks, model.Check{Name: "source_identity", Status: model.CheckFail, Message: "former primary identity does not match the immutable source resource"})
		}
		if sourceIdentity.readOnly && sourceIdentity.superReadOnly {
			verification.Checks = append(verification.Checks, model.Check{Name: "source_read_only", Status: model.CheckPass, Message: "former primary is read-only"})
		} else {
			verification.Checks = append(verification.Checks, model.Check{Name: "source_read_only", Status: model.CheckFail, Message: "former primary is not fully read-only"})
		}
	}
	targetIdentity, targetErr := probeIdentity(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if targetErr != nil {
		verification.Checks = append(verification.Checks, model.Check{Name: "target_reachable", Status: model.CheckFail, Message: "target reachability is unknown: " + targetErr.Error()})
	} else {
		if targetIdentity.serverUUID == strings.ToLower(strings.TrimSpace(resolved.Target.EngineIdentity["server_uuid"])) {
			verification.Checks = append(verification.Checks, model.Check{Name: "target_identity", Status: model.CheckPass, Message: "target identity matches the immutable target resource"})
		} else {
			verification.Checks = append(verification.Checks, model.Check{Name: "target_identity", Status: model.CheckFail, Message: "target identity does not match the immutable target resource"})
		}
		if !targetIdentity.readOnly && !targetIdentity.superReadOnly {
			verification.Checks = append(verification.Checks, model.Check{Name: "target_writable", Status: model.CheckPass, Message: "selected target is writable"})
		} else {
			verification.Checks = append(verification.Checks, model.Check{Name: "target_writable", Status: model.CheckFail, Message: "selected target is not writable"})
		}
	}
	_, targetReplicationConfigured, replicationErr := probeReplication(ctx, adapterInstance.runner, targetEndpoint, credentials)
	if replicationErr != nil {
		verification.Checks = append(verification.Checks, model.Check{Name: "target_replication_detached", Status: model.CheckFail, Message: "target replication state is unknown: " + replicationErr.Error()})
	} else if targetReplicationConfigured {
		verification.Checks = append(verification.Checks, model.Check{Name: "target_replication_detached", Status: model.CheckFail, Message: "selected target is still configured as a replica"})
	} else {
		verification.Checks = append(verification.Checks, model.Check{Name: "target_replication_detached", Status: model.CheckPass, Message: "selected target is no longer configured as a replica"})
	}
	writableCount := 0
	if targetErr == nil && !targetIdentity.readOnly && !targetIdentity.superReadOnly {
		writableCount++
	}
	for _, follower := range resolved.Snapshot.Instances {
		if follower.ResourceID == resolved.Target.ResourceID {
			continue
		}
		checks, writable := adapterInstance.verifyFollower(ctx, follower, resolved.Target, credentials)
		verification.Checks = append(verification.Checks, checks...)
		if writable {
			writableCount++
		}
	}
	if writableCount == 1 {
		verification.Checks = append(verification.Checks, model.Check{Name: "writable_primary_uniqueness", Status: model.CheckPass, Message: "exactly one controlled instance is writable"})
	} else {
		verification.Checks = append(verification.Checks, model.Check{Name: "writable_primary_uniqueness", Status: model.CheckFail, Message: fmt.Sprintf("%d controlled instances are writable", writableCount)})
	}
	provider := adapterInstance.endpointProvider
	if provider == nil {
		provider = UnsupportedHAEndpointProvider{}
	}
	verification.Checks = append(verification.Checks, sanitizeEndpointCheck(provider.Verify(ctx, resolved), "writer_endpoint_owner"))
	verification.Passed = !planHasBlockingChecks(verification.Checks) && passedCheckNamed(verification.Checks, "writer_endpoint_owner")
	return verification, nil
}
