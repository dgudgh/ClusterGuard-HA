package recovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/observability"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	AutomaticRecoveryActor    = "clusterguard-automatic-recovery"
	maximumParallelRecoveries = 4
	maximumTopologyAge        = 15 * time.Second
	// defaultAutomaticFailoverOperationTimeout reproduces the budget that was
	// hard-coded at the call sites before the timeout became configurable.
	defaultAutomaticFailoverOperationTimeout = 5 * time.Minute
)

type MutationAuthority interface {
	RequireMutationAuthority(context.Context) error
}

type FailureEvidence interface {
	Incident(model.ResourceID, time.Time) (time.Time, bool)
}

type StateReader interface {
	Clusters() []model.DatabaseCluster
	TopologySnapshot(model.ResourceID) (model.TopologySnapshot, bool)
	Operations(model.ResourceID) []model.OperationRecord
	HAEndpoints(model.ResourceID) []model.HAEndpoint
}

type CandidateSelector interface {
	Select(context.Context, model.DatabaseCluster, model.TopologySnapshot, model.ResourceID) (model.ResourceID, error)
}

type OperationExecutor interface {
	ExecuteAutomatic(context.Context, adapter.OperationRequest, string) (model.Execution, error)
}

type Controller struct {
	state            StateReader
	failures         FailureEvidence
	selector         CandidateSelector
	executor         OperationExecutor
	authority        MutationAuthority
	engine           model.Engine
	retryDelay       time.Duration
	interval         time.Duration
	operationTimeout time.Duration
	// operationTimeoutProvider is consulted every round so a change to the
	// replicated cluster policy takes effect on the next cycle instead of at
	// the next restart. operationTimeout stays as the start-up value and as the
	// fallback when no policy is configured.
	operationTimeoutProvider func() time.Duration
	// suppressed pauses automatic failover for planned maintenance. Evidence is
	// still recorded, so lifting the pause cannot resurrect a stale series.
	suppressed func() bool
	now        func() time.Time
	onError          func(error)
	errorReminder    *observability.ErrorReminder
	recoveryGate     chan struct{}
	inFlightMu       sync.Mutex
	inFlight         map[model.ResourceID]struct{}
}

type Option func(*Controller)

func WithInterval(interval time.Duration) Option {
	return func(controller *Controller) {
		if interval > 0 {
			controller.interval = interval
		}
	}
}

func WithEngine(engine model.Engine) Option {
	return func(controller *Controller) {
		controller.engine = engine
	}
}

// WithOperationTimeout bounds how long a single automatic failover operation
// may run. The default preserves the historical five minute budget.
func WithOperationTimeout(timeout time.Duration) Option {
	return func(controller *Controller) {
		if timeout > 0 {
			controller.operationTimeout = timeout
		}
	}
}

// WithOperationTimeoutProvider makes the operation budget read from the
// replicated cluster policy on every round. The provider returns zero when no
// override is set, which keeps the start-up budget in force.
func WithOperationTimeoutProvider(provider func() time.Duration) Option {
	return func(controller *Controller) {
		if provider != nil {
			controller.operationTimeoutProvider = provider
		}
	}
}

// WithSuppression pauses automatic failover while the provider reports true.
// The controller keeps recording evidence, so a maintenance window cannot be
// used to hide an incident that was already accumulating.
func WithSuppression(suppressed func() bool) Option {
	return func(controller *Controller) {
		if suppressed != nil {
			controller.suppressed = suppressed
		}
	}
}

func WithErrorHandler(handler func(error)) Option {
	return func(controller *Controller) {
		if handler != nil {
			controller.onError = handler
		}
	}
}

func NewController(state StateReader, failures FailureEvidence, selector CandidateSelector, executor OperationExecutor, authority MutationAuthority, retryDelay time.Duration, now func() time.Time, options ...Option) *Controller {
	if retryDelay <= 0 {
		retryDelay = 30 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	controller := &Controller{
		state: state, failures: failures, selector: selector, executor: executor, authority: authority,
		engine: model.EngineMySQL, retryDelay: retryDelay, interval: 5 * time.Second,
		operationTimeout: defaultAutomaticFailoverOperationTimeout, now: now,
		onError:       func(err error) { log.Printf("automatic recovery cycle failed: %v", err) },
		errorReminder: observability.NewErrorReminder(5*time.Minute, now),
		recoveryGate:  make(chan struct{}, maximumParallelRecoveries),
		inFlight:      make(map[model.ResourceID]struct{}),
	}
	for _, option := range options {
		if option != nil {
			option(controller)
		}
	}
	return controller
}

// automaticOperationTimeout never returns a zero budget. A zero would make
// context.WithTimeout expire the operation immediately, so a Controller built
// without NewController still gets the documented default.
func (controller *Controller) automaticOperationTimeout() time.Duration {
	if controller == nil {
		return defaultAutomaticFailoverOperationTimeout
	}
	if controller.operationTimeoutProvider != nil {
		if timeout := controller.operationTimeoutProvider(); timeout > 0 {
			return timeout
		}
	}
	if controller.operationTimeout <= 0 {
		return defaultAutomaticFailoverOperationTimeout
	}
	return controller.operationTimeout
}

func (controller *Controller) reportError(err error) {
	if controller == nil || errors.Is(err, context.Canceled) {
		return
	}
	if err == nil && controller.hasStableIncident() {
		return
	}
	if controller.errorReminder != nil && !controller.errorReminder.ShouldReport(err) {
		return
	}
	if err != nil && controller.onError != nil {
		controller.onError(err)
	}
}

func (controller *Controller) hasStableIncident() bool {
	if controller == nil || controller.state == nil || controller.failures == nil {
		return false
	}
	now := controller.now().UTC()
	for _, cluster := range controller.state.Clusters() {
		if cluster.Engine != controller.engine || !model.ValidResourceID(cluster.ResourceID) {
			continue
		}
		if _, stable := controller.failures.Incident(cluster.ResourceID, now); stable {
			return true
		}
	}
	return false
}

func automaticFailoverSourcePrefix(clusterID, sourceID model.ResourceID) string {
	return fmt.Sprintf("automatic-failover:%s:%s:", clusterID, sourceID)
}

func automaticFailoverPrefix(clusterID, sourceID model.ResourceID, incident time.Time) string {
	return fmt.Sprintf("%s%d:", automaticFailoverSourcePrefix(clusterID, sourceID), incident.UTC().UnixNano())
}

func (controller *Controller) configured() bool {
	return controller != nil && (controller.engine == model.EngineMySQL || controller.engine == model.EnginePostgreSQL) && controller.state != nil && controller.failures != nil && controller.selector != nil && controller.executor != nil && controller.authority != nil
}

func (controller *Controller) RunOnce(ctx context.Context) error {
	if !controller.configured() {
		return fmt.Errorf("automatic %s recovery controller is not configured", controller.engine)
	}
	if controller.suppressed != nil && controller.suppressed() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := controller.authority.RequireMutationAuthority(ctx); err != nil {
		return nil
	}
	clusters := controller.state.Clusters()
	errorsFound := make(chan error, len(clusters))
	var wait sync.WaitGroup
	for _, cluster := range clusters {
		if cluster.Engine != controller.engine || !model.ValidResourceID(cluster.ResourceID) {
			continue
		}
		if !controller.beginRecovery(cluster.ResourceID) {
			continue
		}
		cluster := cluster
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer controller.finishRecovery(cluster.ResourceID)
			select {
			case controller.recoveryGate <- struct{}{}:
				defer func() { <-controller.recoveryGate }()
			case <-ctx.Done():
				errorsFound <- ctx.Err()
				return
			}
			if err := controller.recoverCluster(ctx, cluster); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	var failures []error
	for err := range errorsFound {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (controller *Controller) beginRecovery(clusterID model.ResourceID) bool {
	controller.inFlightMu.Lock()
	defer controller.inFlightMu.Unlock()
	if _, found := controller.inFlight[clusterID]; found {
		return false
	}
	controller.inFlight[clusterID] = struct{}{}
	return true
}

func (controller *Controller) finishRecovery(clusterID model.ResourceID) {
	controller.inFlightMu.Lock()
	defer controller.inFlightMu.Unlock()
	delete(controller.inFlight, clusterID)
}

func (controller *Controller) recoverCluster(ctx context.Context, cluster model.DatabaseCluster) error {
	now := controller.now().UTC()
	if cluster.RecoveryFreeze {
		// Planned-shutdown protection: automatic recovery stays frozen for this
		// cluster until an operator (or the cluster finalize flow) unfreezes it.
		return nil
	}
	snapshot, found := controller.state.TopologySnapshot(cluster.ResourceID)
	if !found || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) || now.Sub(snapshot.ObservedAt) > maximumTopologyAge {
		return nil
	}
	operations := controller.state.Operations(cluster.ResourceID)
	if previous, incidentID, resumable := promotedUnverifiedContinuation(operations, cluster, snapshot); resumable {
		request := adapter.OperationRequest{
			Operation: model.Operation{
				ClusterID: cluster.ResourceID, Engine: cluster.Engine, Kind: model.OperationFailover,
				RequestedBy: AutomaticRecoveryActor,
			},
			TargetID: previous.TargetID, IdempotencyKey: previous.IdempotencyKey,
			Parameters: map[string]string{"trigger": "resume_promoted_unverified"},
		}
		operationContext, cancel := context.WithTimeout(ctx, controller.automaticOperationTimeout())
		defer cancel()
		if _, err := controller.executor.ExecuteAutomatic(operationContext, request, incidentID); err != nil {
			return fmt.Errorf("resume automatic failover for %s: %w", cluster.ResourceID, err)
		}
		return nil
	}
	incident, stable := controller.failures.Incident(cluster.ResourceID, now)
	if !stable {
		return nil
	}
	primaryID, safe := failedPrimary(snapshot, controller.state.HAEndpoints(cluster.ResourceID))
	if !safe {
		return nil
	}
	sourcePrefix := automaticFailoverSourcePrefix(cluster.ResourceID, primaryID)
	targetID, err := controller.selector.Select(ctx, cluster, snapshot, primaryID)
	if err != nil {
		return fmt.Errorf("select automatic failover candidate for %s: %w", cluster.ResourceID, err)
	}
	if !model.ValidResourceID(targetID) || targetID == primaryID || !snapshotContains(snapshot, targetID) {
		return nil
	}
	prefix := automaticFailoverPrefix(cluster.ResourceID, primaryID, incident)
	attempt, allowed := nextAutomaticAttempt(operations, primaryID, sourcePrefix, prefix, incident, now, controller.retryDelay)
	if !allowed {
		return nil
	}
	request := adapter.OperationRequest{
		Operation: model.Operation{
			ClusterID: cluster.ResourceID, Engine: cluster.Engine, Kind: model.OperationFailover,
			RequestedBy: AutomaticRecoveryActor,
		},
		SourceID: primaryID, TargetID: targetID, AutomaticFailureIncidentAt: incident,
		IdempotencyKey: prefix + strconv.Itoa(attempt),
		Parameters:     map[string]string{"trigger": "stable_primary_failure"},
	}
	operationContext, cancel := context.WithTimeout(ctx, controller.automaticOperationTimeout())
	defer cancel()
	incidentID := strings.TrimSuffix(prefix, ":")
	_, err = controller.executor.ExecuteAutomatic(operationContext, request, incidentID)
	if err != nil {
		return fmt.Errorf("execute automatic failover for %s: %w", cluster.ResourceID, err)
	}
	return nil
}

func promotedUnverifiedContinuation(operations []model.OperationRecord, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (model.OperationRecord, string, bool) {
	var selected model.OperationRecord
	incidentID := ""
	for _, operation := range operations {
		sourceID := operation.Plan.SourceID
		sourceFailed := false
		for _, instance := range snapshot.Instances {
			if instance.ResourceID == sourceID && (instance.Health.State == model.HealthUnhealthy || instance.Health.State == model.HealthUnknown) {
				sourceFailed = true
				break
			}
		}
		sourcePrefix := automaticFailoverSourcePrefix(cluster.ResourceID, sourceID)
		if operation.Status != model.OperationIndeterminate || operation.FailureClass != "promoted_unverified" ||
			operation.Operation.Kind != model.OperationFailover || operation.Operation.RequestedBy != AutomaticRecoveryActor ||
			operation.Operation.ClusterID != cluster.ResourceID || operation.Operation.Engine != cluster.Engine ||
			!strings.HasPrefix(operation.IdempotencyKey, sourcePrefix) || !model.ValidResourceID(operation.ResourceID) ||
			!model.ValidResourceID(sourceID) || operation.Plan.TargetID != operation.TargetID || !sourceFailed ||
			!model.ValidResourceID(operation.TargetID) || operation.TargetID == sourceID || !snapshotContains(snapshot, operation.TargetID) {
			continue
		}
		newerTerminalOperation := false
		for _, candidate := range operations {
			if candidate.Status == model.OperationSucceeded && candidate.UpdatedAt.After(operation.UpdatedAt) &&
				(candidate.Operation.Kind == model.OperationFailover || candidate.Operation.Kind == model.OperationSwitchover) {
				newerTerminalOperation = true
				break
			}
		}
		if newerTerminalOperation {
			continue
		}
		separator := strings.LastIndex(operation.IdempotencyKey, ":")
		if separator <= 0 {
			continue
		}
		if _, err := strconv.Atoi(operation.IdempotencyKey[separator+1:]); err != nil {
			continue
		}
		if selected.ResourceID == "" || operation.UpdatedAt.After(selected.UpdatedAt) {
			selected = operation
			incidentID = operation.IdempotencyKey[:separator]
		}
	}
	return selected, incidentID, selected.ResourceID != ""
}

func failedPrimary(snapshot model.TopologySnapshot, haEndpoints []model.HAEndpoint) (model.ResourceID, bool) {
	primaryID := model.ResourceID("")
	for _, instance := range snapshot.Instances {
		if instance.Role != model.RolePrimary {
			continue
		}
		if primaryID != "" || (instance.Health.State != model.HealthUnhealthy && instance.Health.State != model.HealthUnknown) {
			return "", false
		}
		primaryID = instance.ResourceID
	}
	if model.ValidResourceID(primaryID) {
		return primaryID, true
	}

	// Discovery deliberately clears a failed instance's runtime role instead of
	// presenting stale primary state. The persisted HA endpoint owner remains the
	// authoritative failed-source identity, but it is usable only with fresh,
	// explicit database-unavailable evidence from the same topology snapshot.
	for _, endpoint := range haEndpoints {
		if endpoint.ClusterID != snapshot.ClusterID || endpoint.DesiredRole != model.RolePrimary || !model.ValidResourceID(endpoint.OwnerID) {
			continue
		}
		if primaryID != "" && primaryID != endpoint.OwnerID {
			return "", false
		}
		primaryID = endpoint.OwnerID
	}
	if !model.ValidResourceID(primaryID) {
		return "", false
	}
	for _, instance := range snapshot.Instances {
		if instance.ResourceID != primaryID {
			continue
		}
		if instance.Health.State != model.HealthUnhealthy && instance.Health.State != model.HealthUnknown {
			return "", false
		}
		return primaryID, hasCurrentDatabaseFailure(snapshot, primaryID)
	}
	return "", false
}

func hasCurrentDatabaseFailure(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	for _, probe := range snapshot.Probes {
		if probe.InstanceID != instanceID || probe.Outcome != model.ProbeOutcomeDatabaseUnavailable {
			continue
		}
		return !probe.Health.ObservedAt.IsZero() && probe.Health.ObservedAt.Equal(snapshot.ObservedAt)
	}
	return false
}

func snapshotContains(snapshot model.TopologySnapshot, instanceID model.ResourceID) bool {
	for _, instance := range snapshot.Instances {
		if instance.ResourceID == instanceID {
			return true
		}
	}
	return false
}

func nextAutomaticAttempt(operations []model.OperationRecord, sourceID model.ResourceID, sourcePrefix, incidentPrefix string, incident, now time.Time, retryDelay time.Duration) (int, bool) {
	tenureStartedAt := latestPrimaryAssignment(operations, sourceID, incident)
	next := 1
	for _, operation := range operations {
		if !strings.HasPrefix(operation.IdempotencyKey, sourcePrefix) || operation.Operation.Kind != model.OperationFailover || operation.Operation.RequestedBy != AutomaticRecoveryActor {
			continue
		}
		if !tenureStartedAt.IsZero() && !operation.UpdatedAt.IsZero() && !operation.UpdatedAt.After(tenureStartedAt) {
			continue
		}
		if strings.HasPrefix(operation.IdempotencyKey, incidentPrefix) {
			if attempt, err := strconv.Atoi(strings.TrimPrefix(operation.IdempotencyKey, incidentPrefix)); err == nil && attempt >= next {
				next = attempt + 1
			}
		}
		switch operation.Status {
		case model.OperationPlanned, model.OperationRunning, model.OperationSucceeded, model.OperationIndeterminate:
			return next, false
		case model.OperationBlocked, model.OperationFailed, model.OperationUnsupported:
			operationRetryDelay := retryDelay
			if operation.FailureClass == "pre_commit" && strings.Contains(strings.ToLower(operation.Message), "topology observation changed") && operationRetryDelay > 2*time.Second {
				operationRetryDelay = 2 * time.Second
			}
			if operation.UpdatedAt.IsZero() || operation.UpdatedAt.After(now) || now.Sub(operation.UpdatedAt) < operationRetryDelay {
				return next, false
			}
		default:
			return next, false
		}
	}
	return next, true
}

func latestPrimaryAssignment(operations []model.OperationRecord, sourceID model.ResourceID, incident time.Time) time.Time {
	var latest time.Time
	for _, operation := range operations {
		if operation.TargetID != sourceID || operation.Status != model.OperationSucceeded || operation.UpdatedAt.IsZero() || operation.UpdatedAt.After(incident) {
			continue
		}
		if operation.Operation.Kind != model.OperationFailover && operation.Operation.Kind != model.OperationSwitchover {
			continue
		}
		if operation.UpdatedAt.After(latest) {
			latest = operation.UpdatedAt
		}
	}
	return latest
}

func (controller *Controller) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	var cycles sync.WaitGroup
	runCycle := func() {
		cycles.Add(1)
		go func() {
			defer cycles.Done()
			controller.reportError(controller.RunOnce(ctx))
		}()
	}
	runCycle()
	ticker := time.NewTicker(controller.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cycles.Wait()
			return
		case <-ticker.C:
			runCycle()
		}
	}
}
