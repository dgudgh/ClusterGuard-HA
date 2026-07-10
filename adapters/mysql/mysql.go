package mysql

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type SQLRunner interface {
	Query(context.Context, adapter.Endpoint, adapter.Credentials, string) (string, error)
}

type CLIQueryRunner struct {
	Binary string
}

func (runner CLIQueryRunner) Query(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, query string) (string, error) {
	binary := runner.Binary
	if binary == "" {
		binary = "mysql"
	}
	host := endpoint.Hostname
	if host == "" {
		host = endpoint.IPAddress
	}
	if host == "" || endpoint.Port <= 0 || credentials.Username == "" {
		return "", fmt.Errorf("database endpoint, port, and username are required")
	}
	command := exec.CommandContext(ctx, binary, "--batch", "--raw", "--skip-column-names", "--connect-timeout=5", "-h", host, "-P", strconv.Itoa(endpoint.Port), "-u", credentials.Username, "-e", query)
	command.Env = append(os.Environ(), "MYSQL_PWD="+credentials.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mysql query failed: %s", strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

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
		adapter.CapabilityMetrics:           {Available: false, Reason: "metrics collection is scheduled after the common contract"},
		adapter.CapabilityCandidates:        {Available: false, Reason: "candidate evaluation is scheduled after the common contract"},
	}}
}

const identityQuery = "SELECT @@server_uuid, @@hostname, '', @@port, @@server_id, @@version, @@read_only, @@super_read_only"

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	started := time.Now()
	output, err := adapterInstance.runner.Query(ctx, request.Endpoint, request.Credentials, identityQuery)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	fields := strings.Split(strings.TrimSpace(output), "\t")
	if len(fields) < 8 {
		return adapter.DiscoveryResult{}, fmt.Errorf("unexpected MySQL identity response")
	}
	port, err := strconv.Atoi(fields[3])
	if err != nil || port <= 0 {
		return adapter.DiscoveryResult{}, fmt.Errorf("invalid MySQL port in identity response")
	}
	if _, err := strconv.ParseUint(fields[4], 10, 64); err != nil {
		return adapter.DiscoveryResult{}, fmt.Errorf("invalid MySQL server ID in identity response")
	}
	ipAddress := strings.TrimSpace(fields[2])
	if ipAddress == "" {
		ipAddress = request.Endpoint.IPAddress
	}
	role := model.RoleReplica
	if fields[6] == "0" && fields[7] == "0" {
		role = model.RolePrimary
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": strings.ToLower(strings.TrimSpace(fields[0])),
			"server_id":   fields[4],
			"version":     fields[5],
		},
		DisplayName: strings.TrimSpace(fields[1]),
		Hostname:    strings.TrimSpace(fields[1]),
		IPAddress:   ipAddress,
		Port:        port,
		Role:        role,
		Health: model.Health{
			State:      model.HealthHealthy,
			Summary:    "MySQL instance is reachable",
			ObservedAt: time.Now().UTC(),
			LatencyMS:  time.Since(started).Milliseconds(),
		},
	}
	if instance.DisplayName == "" {
		instance.DisplayName = request.Endpoint.Hostname
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func (adapterInstance *Adapter) Topology(context.Context, adapter.DiscoverRequest) (adapter.TopologyResult, error) {
	return adapter.TopologyResult{}, adapter.ErrUnsupported
}

func (adapterInstance *Adapter) Health(ctx context.Context, request adapter.DiscoverRequest) (model.Health, error) {
	result, err := adapterInstance.Discover(ctx, request)
	if err != nil {
		return model.Health{State: model.HealthUnhealthy, Summary: err.Error(), ObservedAt: time.Now().UTC()}, err
	}
	health := result.Instance.Health
	if result.Instance.Role == model.RoleReplica {
		health.Summary = "MySQL instance is reachable and read-only"
	} else {
		health.Summary = "MySQL instance is reachable and writable"
	}
	return health, nil
}

func (adapterInstance *Adapter) Metrics(context.Context, adapter.DiscoverRequest) ([]model.MetricSample, error) {
	return nil, adapter.ErrUnsupported
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
