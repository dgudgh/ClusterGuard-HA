package postgresql

import (
	"context"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

type Adapter struct {
	adapter.UnsupportedAdapter
	runner           SQLRunner
	executor         SQLExecutor
	endpointProvider adapter.HAEndpointProvider
	nodeController   NodeController
	failoverSafety   FailoverSafetyProvider
}

func New(runners ...SQLRunner) *Adapter {
	var runner SQLRunner = CLIQueryRunner{}
	if len(runners) > 0 && runners[0] != nil {
		runner = runners[0]
	}
	return NewWithProviders(runner, UnsupportedHAEndpointProvider{}, UnsupportedNodeController{}, UnsupportedFailoverSafetyProvider{})
}

func NewWithProviders(runner SQLRunner, endpointProvider adapter.HAEndpointProvider, nodeController NodeController, failoverSafety FailoverSafetyProvider) *Adapter {
	if runner == nil {
		runner = CLIQueryRunner{}
	}
	if endpointProvider == nil {
		endpointProvider = UnsupportedHAEndpointProvider{}
	}
	if nodeController == nil {
		nodeController = UnsupportedNodeController{}
	}
	if failoverSafety == nil {
		failoverSafety = UnsupportedFailoverSafetyProvider{}
	}
	executor, _ := runner.(SQLExecutor)
	return &Adapter{
		UnsupportedAdapter: adapter.NewUnsupported(model.EnginePostgreSQL), runner: runner, executor: executor,
		endpointProvider: endpointProvider, nodeController: nodeController, failoverSafety: failoverSafety,
	}
}

func (adapterInstance *Adapter) Engine() model.Engine { return model.EnginePostgreSQL }

func (adapterInstance *Adapter) Capabilities(ctx context.Context) adapter.Capabilities {
	executionAvailable := adapterInstance.executor != nil && adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx) &&
		adapterInstance.nodeController != nil && adapterInstance.nodeController.Executable(ctx)
	executionReason := "PostgreSQL execution requires a mutating SQL runner, restricted node controller, and writer-endpoint provider"
	if executionAvailable {
		executionReason = "guarded PostgreSQL switchover, failover, recovery, and repair are configured"
	}
	return adapter.Capabilities{Engine: model.EnginePostgreSQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover:          {Available: true, Reason: "read-only PostgreSQL discovery is implemented"},
		adapter.CapabilityTopology:          {Available: true, Reason: "read-only streaming replication topology is implemented"},
		adapter.CapabilityHealth:            {Available: true, Reason: "read-only PostgreSQL health is implemented"},
		adapter.CapabilityPrecheck:          {Available: true, Reason: "PostgreSQL HA prechecks are implemented and fail closed"},
		adapter.CapabilityPlan:              {Available: true, Reason: "deterministic PostgreSQL HA planning is implemented"},
		adapter.CapabilityExecute:           {Available: executionAvailable, Mutating: true, Reason: executionReason},
		adapter.CapabilityVerify:            {Available: executionAvailable, Reason: executionReason},
		adapter.CapabilityNodeSync:          {Available: executionAvailable, Mutating: true, Reason: executionReason},
		adapter.CapabilityMetadataReconcile: {Available: true, Reason: "stable PostgreSQL identities support endpoint reconciliation"},
		adapter.CapabilityMetrics:           {Available: true, Reason: "native PostgreSQL sessions, transactions, storage, and replication metrics are implemented"},
		adapter.CapabilityCandidates:        {Available: true, Reason: "read-only timeline and WAL candidate evaluation is implemented"},
	}}
}

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	return discover(ctx, adapterInstance.runner, request)
}

func (adapterInstance *Adapter) Topology(_ context.Context, _ adapter.DiscoverRequest, discovery adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	instance := discovery.Instance
	if len(instance.Replication.SourceIdentity) == 0 {
		return adapter.TopologyResult{Links: []adapter.TopologyLink{}}, nil
	}
	link := adapter.TopologyLink{
		SourceIdentity: instance.Replication.SourceIdentity.Clone(),
		TargetIdentity: instance.EngineIdentity.Clone(),
		Healthy: instance.Health.State == model.HealthHealthy &&
			instance.Replication.IOThread == model.ThreadRunning &&
			instance.Replication.SQLThread == model.ThreadRunning,
		LagSeconds: cloneInt64(instance.Replication.LagSeconds),
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

func (adapterInstance *Adapter) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	return evaluateCandidates(request), nil
}

func (adapterInstance *Adapter) MetadataPrecheck(_ context.Context, request adapter.MetadataRequest) ([]model.Check, error) {
	if request.Instance.Engine != model.EnginePostgreSQL {
		return []model.Check{{Name: "postgresql_identity", Status: model.CheckFail, Message: "instance engine must be postgresql"}}, nil
	}
	_, instanceErr := identity.InstanceKey(model.EnginePostgreSQL, request.Instance.EngineIdentity)
	_, clusterErr := identity.ClusterKey(model.EnginePostgreSQL, request.Instance.EngineIdentity)
	if instanceErr != nil || clusterErr != nil {
		return []model.Check{{Name: "postgresql_identity", Status: model.CheckFail, Message: "resource_id and system_identifier are required"}}, nil
	}
	return []model.Check{{Name: "postgresql_identity", Status: model.CheckPass, Message: "stable node and PostgreSQL system identities are valid"}}, nil
}

func (adapterInstance *Adapter) ReconcileMetadata(ctx context.Context, request adapter.MetadataRequest) (adapter.MetadataResult, error) {
	checks, err := adapterInstance.MetadataPrecheck(ctx, request)
	if err != nil {
		return adapter.MetadataResult{}, err
	}
	if len(checks) != 1 || checks[0].Status != model.CheckPass {
		return adapter.MetadataResult{Checks: checks, Summary: "metadata reconciliation is blocked"}, nil
	}
	return adapter.MetadataResult{
		Checks:  checks,
		Summary: "metadata reconciliation may update this PostgreSQL node endpoint without changing its resource identity",
	}, nil
}

func (adapterInstance *Adapter) Metrics(ctx context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	return queryPostgreSQLMetrics(ctx, adapterInstance.runner, request)
}

func (adapterInstance *Adapter) Precheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverPrecheck(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverPrecheck(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinPrecheck(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairPrecheck(ctx, request)
	default:
		return nil, adapter.ErrUnsupported
	}
}

func (adapterInstance *Adapter) BuildPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverPlan(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverPlan(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinPlan(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairPlan(ctx, request)
	default:
		return model.OperationPlan{}, adapter.ErrUnsupported
	}
}

func (adapterInstance *Adapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverExecute(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverExecute(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinExecute(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairExecute(ctx, request)
	default:
		return model.Execution{}, adapter.ErrUnsupported
	}
}

func (adapterInstance *Adapter) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	switch request.Operation.Kind {
	case model.OperationSwitchover:
		return adapterInstance.switchoverVerify(ctx, request)
	case model.OperationFailover:
		return adapterInstance.failoverVerify(ctx, request)
	case model.OperationFormerPrimaryRejoin:
		return adapterInstance.rejoinVerify(ctx, request)
	case model.OperationReplicationRepair:
		return adapterInstance.repairVerify(ctx, request)
	default:
		return model.Verification{}, adapter.ErrUnsupported
	}
}

func (adapterInstance *Adapter) NodeSyncPrecheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	return adapterInstance.nodeSyncPrecheck(ctx, request)
}

func (adapterInstance *Adapter) BuildNodeSyncPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	return adapterInstance.nodeSyncPlan(ctx, request)
}

func (adapterInstance *Adapter) ExecuteNodeSync(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	return adapterInstance.nodeSyncExecute(ctx, request)
}

var _ adapter.DatabaseHAAdapter = (*Adapter)(nil)
