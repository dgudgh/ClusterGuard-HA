package mysql

import (
	"context"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const defaultMaximumReplicationLagSeconds int64 = 10

type Adapter struct {
	runner                       SQLRunner
	executor                     SQLExecutor
	endpointProvider             adapter.HAEndpointProvider
	maintenance                  MaintenanceStore
	failoverSafety               FailoverSafetyProvider
	verificationAttempts         int
	verificationInterval         time.Duration
	maximumReplicationLagSeconds int64
	semiSyncRequired             bool
}

func (adapterInstance *Adapter) RequireSemiSync(required bool) *Adapter {
	adapterInstance.semiSyncRequired = required
	return adapterInstance
}

func New(runner SQLRunner) *Adapter {
	return NewWithEndpointProvider(runner, UnsupportedHAEndpointProvider{})
}

func NewWithEndpointProvider(runner SQLRunner, endpointProvider adapter.HAEndpointProvider) *Adapter {
	return NewWithProviders(runner, endpointProvider, UnsupportedMaintenanceStore{})
}

func NewWithProviders(runner SQLRunner, endpointProvider adapter.HAEndpointProvider, maintenance MaintenanceStore) *Adapter {
	return NewWithSafetyProviders(runner, endpointProvider, maintenance, UnsupportedFailoverSafetyProvider{})
}

func NewWithSafetyProviders(runner SQLRunner, endpointProvider adapter.HAEndpointProvider, maintenance MaintenanceStore, failoverSafety FailoverSafetyProvider) *Adapter {
	if runner == nil {
		runner = CLIQueryRunner{}
	}
	if endpointProvider == nil {
		endpointProvider = UnsupportedHAEndpointProvider{}
	}
	if maintenance == nil {
		maintenance = UnsupportedMaintenanceStore{}
	}
	if failoverSafety == nil {
		failoverSafety = UnsupportedFailoverSafetyProvider{}
	}
	executor, _ := runner.(SQLExecutor)
	return &Adapter{
		runner:                       runner,
		executor:                     executor,
		endpointProvider:             endpointProvider,
		maintenance:                  maintenance,
		failoverSafety:               failoverSafety,
		verificationAttempts:         15,
		verificationInterval:         time.Second,
		maximumReplicationLagSeconds: defaultMaximumReplicationLagSeconds,
	}
}

func (adapterInstance *Adapter) replicationLagMaximum() int64 {
	if adapterInstance.maximumReplicationLagSeconds < 0 {
		return 0
	}
	return adapterInstance.maximumReplicationLagSeconds
}

func (adapterInstance *Adapter) Engine() model.Engine { return model.EngineMySQL }

func (adapterInstance *Adapter) Capabilities(ctx context.Context) adapter.Capabilities {
	executionAvailable := adapterInstance.executor != nil && adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx)
	executionReason := "a mutating SQL executor and writer-endpoint provider are required"
	if executionAvailable {
		executionReason = "guarded switchover, former-primary rejoin, and replication repair are implemented"
	}
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover:          {Available: true, Reason: "read-only discovery is implemented"},
		adapter.CapabilityTopology:          {Available: true, Reason: "read-only native replication topology is implemented"},
		adapter.CapabilityHealth:            {Available: true, Reason: "read-only health is implemented"},
		adapter.CapabilityPrecheck:          {Available: true, Reason: "guarded planned-switchover precheck is implemented"},
		adapter.CapabilityPlan:              {Available: true, Reason: "guarded planned-switchover planning is implemented"},
		adapter.CapabilityExecute:           {Available: executionAvailable, Mutating: true, Reason: executionReason},
		adapter.CapabilityVerify:            {Available: executionAvailable, Reason: executionReason},
		adapter.CapabilityNodeSync:          {Available: false, Mutating: true, Reason: "node synchronization is not implemented"},
		adapter.CapabilityMetadataReconcile: {Available: true, Reason: "metadata reconciliation is implemented by the platform repository"},
		adapter.CapabilityMetrics:           {Available: true, Reason: "read-only performance metrics are implemented"},
		adapter.CapabilityCandidates:        {Available: true, Reason: "read-only promotion candidate evaluation is implemented"},
	}}
}

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	return discover(ctx, adapterInstance.runner, request, adapterInstance.semiSyncRequired)
}

func (adapterInstance *Adapter) Topology(_ context.Context, _ adapter.DiscoverRequest, discovery adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	instance := discovery.Instance
	if len(instance.Replication.SourceIdentity) == 0 {
		return adapter.TopologyResult{Links: []adapter.TopologyLink{}}, nil
	}
	var lagSeconds *int64
	if instance.Replication.LagSeconds != nil {
		value := *instance.Replication.LagSeconds
		lagSeconds = &value
	}
	link := adapter.TopologyLink{
		SourceIdentity: instance.Replication.SourceIdentity.Clone(),
		TargetIdentity: instance.EngineIdentity.Clone(),
		Healthy: instance.Health.State == model.HealthHealthy &&
			instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning,
		LagSeconds: lagSeconds,
	}
	return adapter.TopologyResult{Links: []adapter.TopologyLink{link}}, nil
}

func (adapterInstance *Adapter) Health(ctx context.Context, request adapter.DiscoverRequest) (model.Health, error) {
	result, err := adapterInstance.Discover(ctx, request)
	if err != nil {
		return model.Health{State: model.HealthUnhealthy, Summary: err.Error(), ObservedAt: time.Now().UTC()}, err
	}
	return result.Instance.Health, nil
}

func (adapterInstance *Adapter) Metrics(ctx context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	return queryMetrics(ctx, adapterInstance.runner, request)
}

func (adapterInstance *Adapter) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	return evaluateCandidates(request), nil
}

func (adapterInstance *Adapter) Precheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverPrecheck(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinPrecheck(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairPrecheck(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverPrecheck(ctx, request)
	default:
		return nil, adapter.ErrUnsupported
	}
}
func (adapterInstance *Adapter) BuildPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverPlan(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinPlan(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairPlan(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverPlan(ctx, request)
	default:
		return model.OperationPlan{}, adapter.ErrUnsupported
	}
}
func (adapterInstance *Adapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverExecute(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinExecute(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairExecute(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverExecute(ctx, request)
	default:
		return model.Execution{}, adapter.ErrUnsupported
	}
}
func (adapterInstance *Adapter) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverVerify(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinVerify(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairVerify(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverVerify(ctx, request)
	default:
		return model.Verification{}, adapter.ErrUnsupported
	}
}
func (adapterInstance *Adapter) NodeSyncPrecheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) BuildNodeSyncPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) ExecuteNodeSync(context.Context, adapter.OperationRequest) (model.Execution, error) {
	return model.Execution{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) MetadataPrecheck(_ context.Context, request adapter.MetadataRequest) ([]model.Check, error) {
	if request.Instance.Engine != model.EngineMySQL || request.Instance.EngineIdentity["server_uuid"] == "" {
		return []model.Check{{Name: "mysql_identity", Status: model.CheckFail, Message: "server_uuid is required"}}, nil
	}
	return []model.Check{{Name: "mysql_identity", Status: model.CheckPass, Message: "server_uuid identifies the instance"}}, nil
}
func (adapterInstance *Adapter) ReconcileMetadata(_ context.Context, request adapter.MetadataRequest) (adapter.MetadataResult, error) {
	checks, _ := adapterInstance.MetadataPrecheck(context.Background(), request)
	if len(checks) == 1 && checks[0].Status == model.CheckFail {
		return adapter.MetadataResult{Checks: checks, Summary: "metadata reconciliation is blocked"}, nil
	}
	return adapter.MetadataResult{Checks: checks, Summary: "metadata reconciliation may update the endpoint for this server UUID"}, nil
}
