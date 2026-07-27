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
}

type CandidateSelector interface {
	Select(context.Context, model.DatabaseCluster, model.TopologySnapshot) (model.ResourceID, error)
}

type OperationExecutor interface {
	ExecuteAutomatic(context.Context, adapter.OperationRequest, string) (model.Execution, error)
}

type Controller struct {
	state         StateReader
	failures      FailureEvidence
	selector      CandidateSelector
	executor      OperationExecutor
	authority     MutationAuthority
	engine        model.Engine
	retryDelay    time.Duration
	interval      time.Duration
	now           func() time.Time
	onError       func(error)
	errorReminder *observability.ErrorReminder
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
		engine: model.EngineMySQL, retryDelay: retryDelay, interval: 5 * time.Second, now: now,
		onError:       func(err error) { log.Printf("automatic recovery cycle failed: %v", err) },
		errorReminder: observability.NewErrorReminder(5*time.Minute, now),
	}
	for _, option := range options {
		if option != nil {
			option(controller)
		}
	}
	return controller
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := controller.authority.RequireMutationAuthority(ctx); err != nil {
		return nil
	}
	clusters := controller.state.Clusters()
	gate := make(chan struct{}, maximumParallelRecoveries)
	errorsFound := make(chan error, len(clusters))
	var wait sync.WaitGroup
	for _, cluster := range clusters {
		if cluster.Engine != controller.engine || !model.ValidResourceID(cluster.ResourceID) {
			continue
		}
		cluster := cluster
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
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

func (controller *Controller) recoverCluster(ctx context.Context, cluster model.DatabaseCluster) error {
	now := controller.now().UTC()
	incident, stable := controller.failures.Incident(cluster.ResourceID, now)
	if !stable {
		return nil
	}
	snapshot, found := controller.state.TopologySnapshot(cluster.ResourceID)
	if !found || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) || now.Sub(snapshot.ObservedAt) > maximumTopologyAge {
		return nil
	}
	primaryID, safe := failedPrimary(snapshot)
	if !safe {
		return nil
	}
	targetID, err := controller.selector.Select(ctx, cluster, snapshot)
	if err != nil {
		return fmt.Errorf("select automatic failover candidate for %s: %w", cluster.ResourceID, err)
	}
	if !model.ValidResourceID(targetID) || targetID == primaryID || !snapshotContains(snapshot, targetID) {
		return nil
	}
	sourcePrefix := automaticFailoverSourcePrefix(cluster.ResourceID, primaryID)
	prefix := automaticFailoverPrefix(cluster.ResourceID, primaryID, incident)
	attempt, allowed := nextAutomaticAttempt(controller.state.Operations(cluster.ResourceID), primaryID, sourcePrefix, prefix, incident, now, controller.retryDelay)
	if !allowed {
		return nil
	}
	request := adapter.OperationRequest{
		Operation: model.Operation{
			ClusterID: cluster.ResourceID, Engine: cluster.Engine, Kind: model.OperationFailover,
			RequestedBy: AutomaticRecoveryActor,
		},
		TargetID: targetID, IdempotencyKey: prefix + strconv.Itoa(attempt),
		Parameters: map[string]string{"trigger": "stable_primary_failure"},
	}
	operationContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	incidentID := strings.TrimSuffix(prefix, ":")
	_, err = controller.executor.ExecuteAutomatic(operationContext, request, incidentID)
	if err != nil {
		return fmt.Errorf("execute automatic failover for %s: %w", cluster.ResourceID, err)
	}
	return nil
}

func failedPrimary(snapshot model.TopologySnapshot) (model.ResourceID, bool) {
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
	return primaryID, model.ValidResourceID(primaryID)
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
			if operation.UpdatedAt.IsZero() || operation.UpdatedAt.After(now) || now.Sub(operation.UpdatedAt) < retryDelay {
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
	controller.reportError(controller.RunOnce(ctx))
	ticker := time.NewTicker(controller.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			controller.reportError(controller.RunOnce(ctx))
		}
	}
}
