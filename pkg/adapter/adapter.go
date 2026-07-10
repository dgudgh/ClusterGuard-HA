package adapter

import (
	"context"
	"errors"

	"clusterguard.io/ha/pkg/model"
)

var ErrUnsupported = errors.New("capability is unsupported")

type Capability string

const (
	CapabilityDiscover          Capability = "discover"
	CapabilityTopology          Capability = "topology"
	CapabilityHealth            Capability = "health"
	CapabilityPrecheck          Capability = "precheck"
	CapabilityPlan              Capability = "plan"
	CapabilityExecute           Capability = "execute"
	CapabilityVerify            Capability = "verify"
	CapabilityNodeSync          Capability = "node_sync"
	CapabilityMetadataReconcile Capability = "metadata_reconcile"
	CapabilityMetrics           Capability = "metrics"
	CapabilityCandidates        Capability = "candidates"
)

type CapabilityState struct {
	Available bool   `json:"available"`
	Mutating  bool   `json:"mutating"`
	Reason    string `json:"reason,omitempty"`
}

type Capabilities struct {
	Engine   model.Engine                   `json:"engine"`
	Features map[Capability]CapabilityState `json:"features"`
}

func (capabilities Capabilities) Supports(capability Capability) bool {
	state, ok := capabilities.Features[capability]
	return ok && state.Available
}

type Endpoint struct {
	Hostname  string `json:"hostname,omitempty"`
	IPAddress string `json:"ip_address,omitempty"`
	Port      int    `json:"port"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"-"`
}

type DiscoverRequest struct {
	ClusterID   model.ResourceID `json:"cluster_id"`
	Endpoint    Endpoint         `json:"endpoint"`
	Credentials Credentials      `json:"-"`
}

type DiscoveryResult struct {
	Instance model.DatabaseInstance `json:"instance"`
}

type TopologyResult struct {
	Links []model.ReplicationLink `json:"links"`
}

type OperationRequest struct {
	Operation   model.Operation   `json:"operation"`
	TargetID    model.ResourceID  `json:"target_id,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Credentials Credentials       `json:"-"`
}

type MetadataRequest struct {
	ClusterID model.ResourceID       `json:"cluster_id"`
	Instance  model.DatabaseInstance `json:"instance"`
}

type MetadataResult struct {
	Checks  []model.Check `json:"checks"`
	Summary string        `json:"summary"`
}

type CandidateRequest struct {
	Cluster   model.DatabaseCluster    `json:"cluster"`
	Primary   model.DatabaseInstance   `json:"primary"`
	Instances []model.DatabaseInstance `json:"instances"`
	Links     []model.ReplicationLink  `json:"links"`
	Probes    []model.ProbeStatus      `json:"probes,omitempty"`
	Policy    model.CandidatePolicy    `json:"policy"`
}

type DatabaseHAAdapter interface {
	Engine() model.Engine
	Capabilities(context.Context) Capabilities
	Discover(context.Context, DiscoverRequest) (DiscoveryResult, error)
	Topology(context.Context, DiscoverRequest) (TopologyResult, error)
	Health(context.Context, DiscoverRequest) (model.Health, error)
	Precheck(context.Context, OperationRequest) ([]model.Check, error)
	BuildPlan(context.Context, OperationRequest) (model.OperationPlan, error)
	Execute(context.Context, OperationRequest) (model.Execution, error)
	Verify(context.Context, OperationRequest) (model.Verification, error)
	NodeSyncPrecheck(context.Context, OperationRequest) ([]model.Check, error)
	BuildNodeSyncPlan(context.Context, OperationRequest) (model.OperationPlan, error)
	ExecuteNodeSync(context.Context, OperationRequest) (model.Execution, error)
	MetadataPrecheck(context.Context, MetadataRequest) ([]model.Check, error)
	ReconcileMetadata(context.Context, MetadataRequest) (MetadataResult, error)
	Metrics(context.Context, DiscoverRequest) ([]model.MetricSample, error)
	EvaluateCandidates(context.Context, CandidateRequest) ([]model.CandidateAssessment, error)
}

type UnsupportedAdapter struct {
	EngineName model.Engine
}

func NewUnsupported(engine model.Engine) UnsupportedAdapter {
	return UnsupportedAdapter{EngineName: engine}
}

func (adapter UnsupportedAdapter) Engine() model.Engine { return adapter.EngineName }

func (adapter UnsupportedAdapter) Capabilities(context.Context) Capabilities {
	features := map[Capability]CapabilityState{}
	for _, capability := range []Capability{CapabilityDiscover, CapabilityTopology, CapabilityHealth, CapabilityPrecheck, CapabilityPlan, CapabilityExecute, CapabilityVerify, CapabilityNodeSync, CapabilityMetadataReconcile, CapabilityMetrics, CapabilityCandidates} {
		features[capability] = CapabilityState{Reason: "not implemented in phase one"}
	}
	return Capabilities{Engine: adapter.EngineName, Features: features}
}

func (adapter UnsupportedAdapter) Discover(context.Context, DiscoverRequest) (DiscoveryResult, error) {
	return DiscoveryResult{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Topology(context.Context, DiscoverRequest) (TopologyResult, error) {
	return TopologyResult{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Health(context.Context, DiscoverRequest) (model.Health, error) {
	return model.Health{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Precheck(context.Context, OperationRequest) ([]model.Check, error) {
	return nil, ErrUnsupported
}
func (adapter UnsupportedAdapter) BuildPlan(context.Context, OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Execute(context.Context, OperationRequest) (model.Execution, error) {
	return model.Execution{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Verify(context.Context, OperationRequest) (model.Verification, error) {
	return model.Verification{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) NodeSyncPrecheck(context.Context, OperationRequest) ([]model.Check, error) {
	return nil, ErrUnsupported
}
func (adapter UnsupportedAdapter) BuildNodeSyncPlan(context.Context, OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) ExecuteNodeSync(context.Context, OperationRequest) (model.Execution, error) {
	return model.Execution{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) MetadataPrecheck(context.Context, MetadataRequest) ([]model.Check, error) {
	return nil, ErrUnsupported
}
func (adapter UnsupportedAdapter) ReconcileMetadata(context.Context, MetadataRequest) (MetadataResult, error) {
	return MetadataResult{}, ErrUnsupported
}
func (adapter UnsupportedAdapter) Metrics(context.Context, DiscoverRequest) ([]model.MetricSample, error) {
	return nil, ErrUnsupported
}
func (adapter UnsupportedAdapter) EvaluateCandidates(context.Context, CandidateRequest) ([]model.CandidateAssessment, error) {
	return nil, ErrUnsupported
}
