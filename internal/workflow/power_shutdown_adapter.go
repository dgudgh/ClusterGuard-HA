package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// AgentTransport is the narrow transport surface the power adapter needs to
// run commands on database hosts. It mirrors writerendpoint.AgentTransport so
// tests can inject a fake without depending on the SSH implementation.
type AgentTransport interface {
	Send(ctx context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error)
}

// PowerShutdownAdapter drives the power_shutdown operation kind through the
// standard workflow (SafetyGuard → Lock → Approve → Execute → Verify → Audit →
// Report). It is registered once under adapter.WildcardEngine because the
// shutdown lifecycle is engine-independent; registering per engine would
// collide with the database-specific adapters.
//
// Execute applies the fail-closed protections (recovery freeze + instance
// maintenance) and performs the real shutdown through signed Agent requests.
// Agent transport and signing material are mandatory: a shutdown can never
// degrade into metadata-only success.
type PowerShutdownAdapter struct {
	store     *store.Repository
	transport AgentTransport
	secret    string
}

func NewPowerShutdownAdapter(repository *store.Repository, transport AgentTransport, secret string) *PowerShutdownAdapter {
	return &PowerShutdownAdapter{store: repository, transport: transport, secret: strings.TrimSpace(secret)}
}

func (shutdown *PowerShutdownAdapter) Engine() model.Engine {
	return adapter.WildcardEngine
}

func (shutdown *PowerShutdownAdapter) Capabilities(context.Context) adapter.Capabilities {
	features := map[adapter.Capability]adapter.CapabilityState{}
	features[adapter.CapabilityPrecheck] = adapter.CapabilityState{Available: true}
	features[adapter.CapabilityPlan] = adapter.CapabilityState{Available: true}
	agentReady := shutdown.agentReady()
	reason := ""
	if !agentReady {
		reason = "signed Agent transport is required for power execution"
	}
	features[adapter.CapabilityExecute] = adapter.CapabilityState{Available: agentReady, Mutating: true, Reason: reason}
	features[adapter.CapabilityVerify] = adapter.CapabilityState{Available: agentReady, Reason: reason}
	for _, capability := range []adapter.Capability{
		adapter.CapabilityDiscover,
		adapter.CapabilityTopology,
		adapter.CapabilityHealth,
		adapter.CapabilityNodeSync,
		adapter.CapabilityMetadataReconcile,
		adapter.CapabilityMetrics,
		adapter.CapabilityCandidates,
	} {
		features[capability] = adapter.CapabilityState{Reason: "power lifecycle is not a database adapter"}
	}
	return adapter.Capabilities{Engine: adapter.WildcardEngine, Features: features}
}

func (shutdown *PowerShutdownAdapter) agentReady() bool {
	return shutdown.transport != nil && strings.TrimSpace(shutdown.secret) != ""
}

func (shutdown *PowerShutdownAdapter) Discover(context.Context, adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	return adapter.DiscoveryResult{}, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) Topology(context.Context, adapter.DiscoverRequest, adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	return adapter.TopologyResult{}, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) Health(context.Context, adapter.DiscoverRequest) (model.Health, error) {
	return model.Health{}, adapter.ErrUnsupported
}

// Precheck reports whether the active power operation is in a state that may
// proceed to execution. SHUTDOWN_PLANNED starts a shutdown and SHUTTING_DOWN
// resumes the same frozen lifecycle after a transient Agent failure. Every
// other state, including an absent operation, blocks the workflow.
func (shutdown *PowerShutdownAdapter) Precheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	operation, found := shutdown.store.ActivePowerOperation(ctx, request.Operation.ClusterID)
	if !found {
		return []model.Check{{
			Name: "power_operation", Status: model.CheckFail,
			Message: "no active power operation for cluster " + string(request.Operation.ClusterID),
		}}, nil
	}
	if operation.State != model.PowerShutdownPlanned && operation.State != model.PowerShuttingDown {
		return []model.Check{{
			Name: "power_operation", Status: model.CheckFail,
			Message: "power operation is in state " + string(operation.State) + "; shutdown_planned or shutting_down required",
		}}, nil
	}
	if !shutdown.agentReady() {
		return []model.Check{{
			Name: "power_agent", Status: model.CheckFail,
			Message: "signed Agent transport is not configured; real shutdown is blocked",
		}}, nil
	}
	return []model.Check{{
		Name: "power_operation", Status: model.CheckPass,
		Message: "power operation " + string(operation.ResourceID) + " is ready for shutdown execution or protected retry",
	}}, nil
}

// BuildPlan describes the shutdown steps. The first two are protective flags;
// the database-facing steps are executed by the agent transport in Execute.
// powerPlanDigest returns a deterministic digest for a power shutdown plan.
func powerPlanDigest(steps []model.PlanStep) string {
	hasher := sha256.New()
	for _, step := range steps {
		hasher.Write([]byte(step.Name))
		hasher.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func (shutdown *PowerShutdownAdapter) BuildPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	if request.Resolved == nil || !model.ValidResourceID(request.Resolved.Primary.ResourceID) {
		return model.OperationPlan{}, fmt.Errorf("power shutdown plan requires a resolved topology")
	}
	steps := []model.PlanStep{
		{Index: 1, Name: "freeze_recovery", Owner: "power", Mutating: true, Postcondition: "cluster recovery freeze active"},
		{Index: 2, Name: "set_maintenance", Owner: "power", Mutating: true, Postcondition: "every instance in maintenance"},
	}
	// The persist step applies only to engines with a PERSIST_ONLY-style
	// read-only setting (MySQL); a cluster without MySQL nodes skips it,
	// matching Execute's per-instance dispatch.
	hasMySQL := false
	for _, instance := range request.Resolved.Snapshot.Instances {
		if powerPersistEngine(instance.Engine) {
			hasMySQL = true
			break
		}
	}
	nextIndex := 3
	if hasMySQL {
		steps = append(steps, model.PlanStep{Index: nextIndex, Name: "persist_read_only", Owner: "power",
			Mutating: true, Postcondition: "PERSIST_ONLY read_only active on every node"})
		nextIndex++
	}
	steps = append(steps, model.PlanStep{Index: nextIndex, Name: "prepare_recovery_snapshot", Owner: "power",
		Mutating: true, Postcondition: "recovery snapshot durably stored on every node"})
	nextIndex++
	if hasMySQL {
		steps = append(steps, model.PlanStep{Index: nextIndex, Name: "release_vip_and_isolate", Owner: "power",
			Mutating: true, Postcondition: "VIP absent and every MySQL node durably read-only"})
		nextIndex++
	}
	steps = append(steps,
		model.PlanStep{Index: nextIndex, Name: "stop_replicas", Owner: "power", Mutating: true,
			Postcondition: "database service stopped on all replicas"},
		model.PlanStep{Index: nextIndex + 1, Name: "stop_primary", Owner: "power", Mutating: true,
			Postcondition: "database service stopped on the primary"},
	)
	revisions := map[model.ResourceID]uint64{
		request.Resolved.Cluster.ResourceID: request.Resolved.Cluster.MetadataRevision,
	}
	for _, instance := range request.Resolved.Snapshot.Instances {
		if model.ValidResourceID(instance.ResourceID) {
			revisions[instance.ResourceID] = instance.MetadataRevision
		}
	}
	return model.OperationPlan{
		Stage:             model.StagePlan,
		OperationID:       request.Operation.ResourceID,
		ClusterID:         request.Operation.ClusterID,
		SourceID:          request.Resolved.Primary.ResourceID,
		TargetID:          request.TargetID,
		ObservationToken:  request.Resolved.ObservationToken,
		ResourceRevisions: revisions,
		Steps:             steps,
		Digest:            powerPlanDigest(steps),
		Summary:           "power shutdown: freeze recovery, place instances in maintenance, then stop the cluster",
		Mutating:          true,
	}, nil
}

// Execute applies the fail-closed protections and advances the power state
// machine through SHUTTING_DOWN to POWER_OFF. Any agent failure keeps the
// protections active and leaves the operation in SHUTTING_DOWN so the same
// frozen lifecycle can be retried without briefly re-enabling recovery.
func (shutdown *PowerShutdownAdapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	if !shutdown.agentReady() {
		return failedPowerExecution(request.Operation.ResourceID, "signed Agent transport is required for real power execution")
	}
	if !model.ValidResourceID(adapter.OperationLeaseID(ctx)) {
		return failedPowerExecution(request.Operation.ResourceID, "an active operation lock lease is required for power execution")
	}
	if request.Resolved == nil || !model.ValidResourceID(request.Resolved.Cluster.ResourceID) ||
		!model.ValidResourceID(request.Resolved.Primary.ResourceID) {
		return failedPowerExecution(request.Operation.ResourceID, "power shutdown execution requires a resolved topology")
	}
	operation, found := shutdown.store.ActivePowerOperation(ctx, request.Operation.ClusterID)
	if !found {
		return failedPowerExecution(request.Operation.ResourceID, "no active power operation for cluster "+string(request.Operation.ClusterID))
	}
	if operation.State != model.PowerShutdownPlanned && operation.State != model.PowerShuttingDown {
		return failedPowerExecution(request.Operation.ResourceID,
			fmt.Sprintf("power operation %s is in state %s; shutdown_planned or shutting_down required", operation.ResourceID, operation.State))
	}
	if err := shutdown.store.ApplyPowerProtections(ctx, operation.ClusterID); err != nil {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}, err
	}
	transitioned := operation
	if operation.State == model.PowerShutdownPlanned {
		next, err := shutdown.store.TransitionPowerOperation(ctx, operation.ResourceID, operation.MetadataRevision,
			model.PowerShuttingDown, request.Operation.RequestedBy, "power shutdown executing", nil)
		if err != nil {
			return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}, err
		}
		transitioned = next
	}
	if err := shutdown.executeAgentShutdown(ctx, request, &transitioned); err != nil {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}, err
	}
	if _, err := shutdown.store.TransitionPowerOperation(ctx, transitioned.ResourceID, transitioned.MetadataRevision,
		model.PowerPoweredOff, request.Operation.RequestedBy, "shutdown complete", nil); err != nil {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationFailed, Message: err.Error()}, err
	}
	return model.Execution{
		OperationID: request.Operation.ResourceID,
		Status:      model.OperationSucceeded,
		Message:     "power protections applied and shutdown completed",
	}, nil
}

// executeAgentShutdown runs the real shutdown through the agent transport:
// PERSIST_ONLY read-only on every node, MySQL service stopped on replicas,
// then the primary, then (poweroff mode) the hosts powered off. Steps run
// sequentially so the first failure aborts the whole operation with the
// protections still active — the operator must fail the lifecycle and retry.
func (shutdown *PowerShutdownAdapter) executeAgentShutdown(ctx context.Context, request adapter.OperationRequest, operation *model.PowerOperation) error {
	resolved := request.Resolved
	if resolved == nil || !model.ValidResourceID(resolved.Cluster.ResourceID) {
		return fmt.Errorf("power shutdown execution requires a resolved topology")
	}

	// Phase A: persist read-only on every MySQL node (replicas and primary).
	// The agent's persist_role command performs SET PERSIST_ONLY read_only so
	// a later restart can never come up writable by accident. Engines without
	// a PERSIST_ONLY equivalent (PostgreSQL) skip the step.
	persistRequest, err := shutdown.powerAgentRequest(ctx, resolved, model.EngineMySQL, agent.CommandPersistRole)
	if err != nil {
		return err
	}
	persistRequest.ReadOnly = true
	persistRequest, err = shutdown.resignPowerAgentRequest(persistRequest)
	if err != nil {
		return err
	}
	for _, instance := range resolved.Snapshot.Instances {
		if !powerPersistEngine(instance.Engine) {
			continue
		}
		if err := shutdown.sendPowerAgent(ctx, instance, persistRequest, "persist read_only"); err != nil {
			return err
		}
	}

	// Phase B: atomically persist the frozen topology on every node before any
	// database service is stopped. Boot-time restore units use this exact
	// snapshot and independently verify the control-plane power state before
	// changing role protection.
	recoverySnapshot := powerRecoverySnapshot(resolved, operation)
	for _, instance := range resolved.Snapshot.Instances {
		prepareRequest, err := shutdown.powerAgentRequest(ctx, resolved, instance.Engine, agent.CommandPowerPrepare)
		if err != nil {
			return err
		}
		prepareRequest.PowerSnapshot = &recoverySnapshot
		prepareRequest, err = shutdown.resignPowerAgentRequest(prepareRequest)
		if err != nil {
			return err
		}
		if err := shutdown.sendPowerAgent(ctx, instance, prepareRequest, "prepare recovery snapshot"); err != nil {
			return err
		}
	}

	// Phase C: explicitly remove the writer VIP from every MySQL node and
	// confirm durable read-only isolation. The periodic reconciler provides a
	// second line of defence, but shutdown must not depend on its next tick:
	// clients must lose the writer endpoint before any database service stops.
	for _, instance := range resolved.Snapshot.Instances {
		if instance.Engine != model.EngineMySQL {
			continue
		}
		isolateRequest, err := shutdown.powerAgentRequest(ctx, resolved, instance.Engine, agent.CommandSelfIsolate)
		if err != nil {
			return err
		}
		if err := shutdown.sendPowerAgent(ctx, instance, isolateRequest, "release VIP and isolate MySQL"); err != nil {
			return err
		}
	}

	// Phase D: stop the database service on every replica, using the stop
	// command for the instance's engine (mysql_service_stop / postgresql_stop).
	for _, instance := range resolved.Snapshot.Instances {
		if instance.ResourceID == resolved.Primary.ResourceID {
			continue
		}
		stopRequest, err := shutdown.powerAgentRequest(ctx, resolved, instance.Engine, powerStopCommand(instance.Engine))
		if err != nil {
			return err
		}
		if err := shutdown.sendPowerAgent(ctx, instance, stopRequest, powerStopLabel(instance.Engine, "replica")); err != nil {
			return err
		}
	}

	// Phase E: stop the database service on the primary last — the cluster is
	// quiesced only once every replica is already down.
	primaryStopRequest, err := shutdown.powerAgentRequest(ctx, resolved, resolved.Primary.Engine, powerStopCommand(resolved.Primary.Engine))
	if err != nil {
		return err
	}
	if err := shutdown.sendPowerAgent(ctx, resolved.Primary, primaryStopRequest, powerStopLabel(resolved.Primary.Engine, "primary")); err != nil {
		return err
	}

	// Phase F: poweroff mode — shut the hosts down after the database service.
	if operation.OperationType == model.PowerPowerOff {
		for _, instance := range resolved.Snapshot.Instances {
			poweroffRequest, err := shutdown.powerAgentRequest(ctx, resolved, instance.Engine, agent.CommandNodePoweroff)
			if err != nil {
				return err
			}
			if err := shutdown.sendPowerAgent(ctx, instance, poweroffRequest, "power off node"); err != nil {
				return err
			}
		}
	}
	return nil
}

func powerRecoverySnapshot(resolved *adapter.ResolvedOperation, operation *model.PowerOperation) model.PowerSnapshot {
	if operation != nil && operation.Snapshot != nil && operation.Snapshot.ClusterID == resolved.Cluster.ResourceID {
		return *operation.Snapshot
	}
	snapshot := model.PowerSnapshot{
		ClusterID: resolved.Cluster.ResourceID, ClusterName: resolved.Cluster.DisplayName,
		Engine: resolved.Cluster.Engine, CapturedAt: time.Now().UTC(),
	}
	for _, instance := range resolved.Snapshot.Instances {
		reference := model.PowerInstanceRef{
			InstanceID: instance.ResourceID, Hostname: instance.Hostname,
			IPAddress: instance.IPAddress, Port: instance.Port,
		}
		if instance.ResourceID == resolved.Primary.ResourceID {
			snapshot.Primary = reference
		} else {
			snapshot.Replicas = append(snapshot.Replicas, reference)
		}
	}
	return snapshot
}

func (shutdown *PowerShutdownAdapter) sendPowerAgent(ctx context.Context, instance model.DatabaseInstance, request agent.Request, action string) error {
	response, err := shutdown.transport.Send(ctx, instance, request)
	if err != nil {
		return fmt.Errorf("%s on %s: %w", action, instance.Hostname, err)
	}
	if response.Status != agent.StatusOK {
		return fmt.Errorf("%s on %s: %s", action, instance.Hostname, powerResponseDetail(response))
	}
	return nil
}

// powerPersistEngine reports whether the engine has a PERSIST_ONLY-style
// read-only setting that must be hardened before shutdown. MySQL persists
// read_only into the server's auto-config; PostgreSQL has no equivalent and
// skips the step.
func powerPersistEngine(engine model.Engine) bool {
	return engine == model.EngineMySQL
}

// powerStopCommand maps an engine to the agent command that stops its
// database service. Engine-agnostic commands (node_poweroff) stay shared.
func powerStopCommand(engine model.Engine) string {
	switch engine {
	case model.EnginePostgreSQL:
		return agent.CommandPostgreSQLStop
	default:
		return agent.CommandMySQLServiceStop
	}
}

// powerStatusCommand maps an engine to the agent command that reports its
// service state. Both engines fill response.ServiceRunning, so verify treats
// them identically.
func powerStatusCommand(engine model.Engine) string {
	switch engine {
	case model.EnginePostgreSQL:
		return agent.CommandPostgreSQLStatus
	default:
		return agent.CommandMySQLPowerStatus
	}
}

// powerStopLabel names the failing step in operator-visible messages.
func powerStopLabel(engine model.Engine, phase string) string {
	service := "database"
	switch engine {
	case model.EngineMySQL:
		service = "MySQL"
	case model.EnginePostgreSQL:
		service = "PostgreSQL"
	}
	return "stop " + service + " on " + phase
}

// powerAgentRequest builds a signed agent request scoped to the operation's
// frozen topology, plan digest, and active operation-lock lease. The engine is
// stamped per instance so the agent validates the request against its own
// allowlist. Read-only verification requests may run after the lock is
// released and therefore tolerate an empty lease ID.
func (shutdown *PowerShutdownAdapter) powerAgentRequest(ctx context.Context, resolved *adapter.ResolvedOperation, engine model.Engine, command string) (agent.Request, error) {
	digest := strings.TrimSpace(resolved.PlanDigest)
	if digest == "" {
		digest = "sha256:precheck:" + string(resolved.OperationID)
	}
	request := agent.Request{
		Command:     command,
		Engine:      engine,
		ClusterID:   resolved.Cluster.ResourceID,
		OperationID: resolved.OperationID,
		LeaseID:     adapter.OperationLeaseID(ctx),
		PlanDigest:  digest,
		ExpiresAt:   time.Now().UTC().Add(2 * time.Minute),
	}
	return shutdown.resignPowerAgentRequest(request)
}

func (shutdown *PowerShutdownAdapter) resignPowerAgentRequest(request agent.Request) (agent.Request, error) {
	signature, err := agent.SignRequest(request, shutdown.secret)
	if err != nil {
		return agent.Request{}, fmt.Errorf("sign power agent request: %w", err)
	}
	request.Signature = signature
	return request, nil
}

// powerFrozenInstance returns true when the instance is covered by the
// operation's frozen topology snapshot: same cluster, same ResourceID, and
// matching identity coordinates. Metadata revision is deliberately NOT
// compared: the operation itself mutates the instances (maintenance bumps
// each revision), so a revision check would reject every post-execute
// verification. Drift is still caught by identity — a replaced or renamed
// node fails the hostname/IP/engine match.
func powerFrozenInstance(resolved *adapter.ResolvedOperation, instance model.DatabaseInstance) bool {
	if instance.ClusterID != resolved.Cluster.ResourceID {
		return false
	}
	for _, frozen := range resolved.Snapshot.Instances {
		if frozen.ResourceID != instance.ResourceID {
			continue
		}
		return frozen.ClusterID == instance.ClusterID &&
			frozen.Engine == instance.Engine &&
			strings.TrimSpace(frozen.Hostname) == strings.TrimSpace(instance.Hostname) &&
			strings.TrimSpace(frozen.IPAddress) == strings.TrimSpace(instance.IPAddress) &&
			frozen.Port == instance.Port
	}
	return false
}

// Verify confirms the shutdown actually landed: the protective flags are
// active and, when agents are wired, the database services are stopped (or the
// hosts are unreachable in poweroff mode).
func (shutdown *PowerShutdownAdapter) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	frozen, err := shutdown.store.RecoveryFrozen(ctx, request.Operation.ClusterID)
	if err != nil {
		return model.Verification{}, fmt.Errorf("read recovery freeze for power verify: %w", err)
	}
	checks := []model.Check{{
		Name: "recovery_freeze", Status: checkForPowerCondition(frozen),
		Message: fmt.Sprintf("cluster recovery freeze active=%t", frozen),
	}}
	if !shutdown.agentReady() {
		checks = append(checks, model.Check{
			Name: "power_agent", Status: model.CheckFail,
			Message: "signed Agent transport is not configured; service shutdown cannot be verified",
		})
	}

	operation, hasOperation := shutdown.store.ActivePowerOperation(ctx, request.Operation.ClusterID)
	poweroffMode := hasOperation && operation.OperationType == model.PowerPowerOff
	if shutdown.agentReady() && request.Resolved != nil {
		for _, instance := range shutdown.store.Instances(request.Operation.ClusterID) {
			statusRequest, reqErr := shutdown.powerAgentRequest(ctx, request.Resolved, instance.Engine, powerStatusCommand(instance.Engine))
			if reqErr != nil {
				checks = append(checks, model.Check{Name: "agent_status_" + string(instance.ResourceID), Status: model.CheckFail, Message: reqErr.Error()})
				continue
			}
			checks = append(checks, shutdown.verifyAgentInstance(ctx, request.Resolved, instance, statusRequest, poweroffMode))
		}
	}

	covered := true
	for _, instance := range shutdown.store.Instances(request.Operation.ClusterID) {
		if !instance.Maintenance {
			covered = false
			checks = append(checks, model.Check{
				Name: "maintenance_" + string(instance.ResourceID), Status: model.CheckFail,
				Message: "instance " + instance.DisplayName + " is not in maintenance",
			})
		}
	}
	if covered {
		checks = append(checks, model.Check{Name: "instances_maintenance", Status: model.CheckPass, Message: "all instances are in maintenance"})
	}
	passed := true
	for _, check := range checks {
		if check.Status == model.CheckFail {
			passed = false
		}
	}
	return model.Verification{
		OperationID: request.Operation.ResourceID,
		Passed:      passed,
		Checks:      checks,
		ObservedAt:  time.Now().UTC(),
	}, nil
}

func (shutdown *PowerShutdownAdapter) verifyAgentInstance(ctx context.Context, resolved *adapter.ResolvedOperation, instance model.DatabaseInstance, statusRequest agent.Request, poweroffMode bool) model.Check {
	check := model.Check{Name: "power_status_" + string(instance.ResourceID)}
	if !powerFrozenInstance(resolved, instance) {
		check.Status = model.CheckFail
		check.Message = instance.Hostname + " is outside the frozen operation topology"
		return check
	}
	response, err := shutdown.transport.Send(ctx, instance, statusRequest)
	if err != nil {
		if poweroffMode {
			check.Status = model.CheckPass
			check.Message = instance.Hostname + " is unreachable (expected after poweroff)"
			return check
		}
		check.Status = model.CheckFail
		check.Message = instance.Hostname + " status probe failed: " + err.Error()
		return check
	}
	if response.ServiceRunning != nil && !*response.ServiceRunning {
		check.Status = model.CheckPass
		check.Message = instance.Hostname + " service stopped"
		return check
	}
	if response.ServiceRunning == nil {
		check.Status = model.CheckFail
		check.Message = instance.Hostname + " did not report service state"
		return check
	}
	check.Status = model.CheckFail
	check.Message = instance.Hostname + " service still running"
	return check
}

func powerResponseDetail(response agent.Response) string {
	if detail := strings.TrimSpace(response.Error); detail != "" {
		return detail
	}
	if detail := strings.TrimSpace(response.Message); detail != "" {
		return detail
	}
	return "agent returned a non-ok status"
}

func failedPowerExecution(operationID model.ResourceID, message string) (model.Execution, error) {
	return model.Execution{OperationID: operationID, Status: model.OperationFailed, Message: message}, errors.New(message)
}

func checkForPowerCondition(condition bool) model.CheckStatus {
	if condition {
		return model.CheckPass
	}
	return model.CheckFail
}

func (shutdown *PowerShutdownAdapter) NodeSyncPrecheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) BuildNodeSyncPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) ExecuteNodeSync(context.Context, adapter.OperationRequest) (model.Execution, error) {
	return model.Execution{}, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) MetadataPrecheck(context.Context, adapter.MetadataRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) ReconcileMetadata(context.Context, adapter.MetadataRequest) (adapter.MetadataResult, error) {
	return adapter.MetadataResult{}, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) Metrics(context.Context, adapter.DiscoverRequest) ([]model.MetricSample, error) {
	return nil, adapter.ErrUnsupported
}
func (shutdown *PowerShutdownAdapter) EvaluateCandidates(context.Context, adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	return nil, adapter.ErrUnsupported
}
