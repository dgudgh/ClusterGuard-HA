package mysql

import (
	"context"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Adapter struct {
	runner SQLRunner
}

func New(runner SQLRunner) *Adapter {
	if runner == nil {
		runner = CLIQueryRunner{}
	}
	return &Adapter{runner: runner}
}

func (adapterInstance *Adapter) Engine() model.Engine { return model.EngineMySQL }

func (adapterInstance *Adapter) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover:          {Available: true, Reason: "read-only discovery is implemented"},
		adapter.CapabilityTopology:          {Available: false, Reason: "topology discovery is scheduled after phase one"},
		adapter.CapabilityHealth:            {Available: true, Reason: "read-only health is implemented"},
		adapter.CapabilityPrecheck:          {Available: false, Reason: "HA mutation precheck is not implemented"},
		adapter.CapabilityPlan:              {Available: false, Reason: "HA mutation planning is not implemented"},
		adapter.CapabilityExecute:           {Available: false, Mutating: true, Reason: "HA mutation execution is not implemented"},
		adapter.CapabilityVerify:            {Available: false, Reason: "HA mutation verification is not implemented"},
		adapter.CapabilityNodeSync:          {Available: false, Mutating: true, Reason: "node synchronization is not implemented"},
		adapter.CapabilityMetadataReconcile: {Available: true, Reason: "metadata reconciliation is implemented by the platform repository"},
		adapter.CapabilityMetrics:           {Available: true, Reason: "read-only performance metrics are implemented"},
		adapter.CapabilityCandidates:        {Available: false, Reason: "candidate evaluation is scheduled after the common contract"},
	}}
}

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	return discover(ctx, adapterInstance.runner, request)
}

func (adapterInstance *Adapter) Topology(context.Context, adapter.DiscoverRequest) (adapter.TopologyResult, error) {
	return adapter.TopologyResult{}, adapter.ErrUnsupported
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

func (adapterInstance *Adapter) EvaluateCandidates(context.Context, adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	return nil, adapter.ErrUnsupported
}

func (adapterInstance *Adapter) Precheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) BuildPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) Execute(context.Context, adapter.OperationRequest) (model.Execution, error) {
	return model.Execution{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) Verify(context.Context, adapter.OperationRequest) (model.Verification, error) {
	return model.Verification{}, adapter.ErrUnsupported
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
