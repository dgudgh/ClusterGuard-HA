package oracle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type BrokerRunner interface {
	Executable(context.Context) bool
	RunDGMGRL(context.Context, adapter.Endpoint, adapter.Credentials, []string) ([]string, error)
}

type SQLPlusRunner interface {
	Executable(context.Context) bool
	QuerySQLPlus(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]string, error)
}

type BrokerDiscoveryProvider interface {
	Executable(context.Context) bool
	Discover(context.Context, adapter.DiscoverRequest) (adapter.DiscoveryResult, error)
}

type Adapter struct {
	adapter.UnsupportedAdapter
	runner            BrokerRunner
	sqlplus           SQLPlusRunner
	discoveryProvider BrokerDiscoveryProvider
	controller        BrokerController
	endpointProvider  adapter.HAEndpointProvider
	verifyMaxAttempts int
	verifyRetryDelay  time.Duration
}

const (
	defaultOracleVerifyMaxAttempts = 20
	defaultOracleVerifyRetryDelay  = 5 * time.Second
)

type UnsupportedBrokerRunner struct{}

func (UnsupportedBrokerRunner) Executable(context.Context) bool { return false }
func (UnsupportedBrokerRunner) RunDGMGRL(context.Context, adapter.Endpoint, adapter.Credentials, []string) ([]string, error) {
	return nil, adapter.ErrUnsupported
}

type UnsupportedSQLPlusRunner struct{}

func (UnsupportedSQLPlusRunner) Executable(context.Context) bool { return false }
func (UnsupportedSQLPlusRunner) QuerySQLPlus(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]string, error) {
	return nil, adapter.ErrUnsupported
}

type UnsupportedBrokerDiscoveryProvider struct{}

func (UnsupportedBrokerDiscoveryProvider) Executable(context.Context) bool { return false }
func (UnsupportedBrokerDiscoveryProvider) Discover(context.Context, adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	return adapter.DiscoveryResult{}, adapter.ErrUnsupported
}

type CLIBrokerRunner struct {
	Binary string
}

type CLISQLPlusRunner struct {
	Binary string
}

func (runner CLIBrokerRunner) Executable(context.Context) bool {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "dgmgrl"
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

func (runner CLIBrokerRunner) RunDGMGRL(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, commands []string) ([]string, error) {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "dgmgrl"
	}
	connect := oracleConnectDescriptor(endpoint, credentials)
	if connect == "" || strings.TrimSpace(credentials.Username) == "" {
		return nil, fmt.Errorf("Oracle DGMGRL endpoint, service, and username are required")
	}
	command := exec.CommandContext(ctx, binary, "-silent")
	input := append([]string{"CONNECT " + oracleCredentialConnect(credentials, connect)}, commands...)
	command.Stdin = strings.NewReader(strings.Join(input, "\n") + "\nEXIT\n")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("DGMGRL command failed: %s", redactOracleSecret(string(output), credentials.Password))
	}
	lines := make([]string, 0)
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			lines = append(lines, string(line))
		}
	}
	return lines, nil
}

func (runner CLISQLPlusRunner) Executable(context.Context) bool {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "sqlplus"
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

func (runner CLISQLPlusRunner) QuerySQLPlus(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, statement string) ([]string, error) {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "sqlplus"
	}
	connect := oracleConnectDescriptor(endpoint, credentials)
	if connect == "" || strings.TrimSpace(credentials.Username) == "" {
		return nil, fmt.Errorf("Oracle SQLPlus endpoint, service, and username are required")
	}
	connection := oracleCredentialConnect(credentials, connect)
	if strings.EqualFold(strings.TrimSpace(credentials.Username), "sys") && !strings.Contains(strings.ToLower(connection), " as ") {
		connection += " as sysdba"
	}
	command := exec.CommandContext(ctx, binary, "-L", "-s", "/nolog")
	command.Stdin = strings.NewReader("CONNECT " + connection + "\n" + statement + "\nexit\n")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("SQLPlus command failed: %s", redactOracleSecret(string(output), credentials.Password))
	}
	lines := make([]string, 0)
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			lines = append(lines, string(line))
		}
	}
	return lines, nil
}

func redactOracleSecret(output, secret string) string {
	output = strings.TrimSpace(output)
	if secret = strings.TrimSpace(secret); secret != "" {
		output = strings.ReplaceAll(output, secret, "[REDACTED]")
	}
	return output
}

func New() *Adapter {
	return NewWithProviders(CLIBrokerRunner{}, CLISQLPlusRunner{}, UnsupportedBrokerController{})
}

func NewWithBrokerRunner(runner BrokerRunner) *Adapter {
	return NewWithProviders(runner, UnsupportedSQLPlusRunner{}, UnsupportedBrokerController{})
}

func NewWithRunners(runner BrokerRunner, sqlplus SQLPlusRunner) *Adapter {
	return NewWithProviders(runner, sqlplus, UnsupportedBrokerController{})
}

func NewWithProviders(runner BrokerRunner, sqlplus SQLPlusRunner, controller BrokerController) *Adapter {
	return NewWithDiscoveryProvider(runner, sqlplus, UnsupportedBrokerDiscoveryProvider{}, controller)
}

func NewWithDiscoveryProvider(runner BrokerRunner, sqlplus SQLPlusRunner, discoveryProvider BrokerDiscoveryProvider, controller BrokerController) *Adapter {
	return NewWithRuntimeProviders(runner, sqlplus, discoveryProvider, controller, UnsupportedHAEndpointProvider{})
}

func NewWithRuntimeProviders(
	runner BrokerRunner,
	sqlplus SQLPlusRunner,
	discoveryProvider BrokerDiscoveryProvider,
	controller BrokerController,
	endpointProvider adapter.HAEndpointProvider,
) *Adapter {
	if runner == nil {
		runner = UnsupportedBrokerRunner{}
	}
	if sqlplus == nil {
		sqlplus = UnsupportedSQLPlusRunner{}
	}
	if discoveryProvider == nil {
		discoveryProvider = UnsupportedBrokerDiscoveryProvider{}
	}
	if controller == nil {
		controller = UnsupportedBrokerController{}
	}
	if endpointProvider == nil {
		endpointProvider = UnsupportedHAEndpointProvider{}
	}
	return &Adapter{
		UnsupportedAdapter: adapter.NewUnsupported(model.EngineOracle),
		runner:             runner, sqlplus: sqlplus, discoveryProvider: discoveryProvider,
		controller: controller, endpointProvider: endpointProvider,
		verifyMaxAttempts: defaultOracleVerifyMaxAttempts,
		verifyRetryDelay:  defaultOracleVerifyRetryDelay,
	}
}

func (adapterInstance *Adapter) Engine() model.Engine { return model.EngineOracle }

func (adapterInstance *Adapter) Capabilities(ctx context.Context) adapter.Capabilities {
	brokerExecutable := adapterInstance.runner != nil && adapterInstance.runner.Executable(ctx)
	controlExecutable := adapterInstance.controller != nil && adapterInstance.controller.Executable(ctx)
	endpointExecutable := adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx)
	localReadExecutable := brokerExecutable || (adapterInstance.sqlplus != nil && adapterInstance.sqlplus.Executable(ctx))
	agentReadExecutable := adapterInstance.discoveryProvider != nil && adapterInstance.discoveryProvider.Executable(ctx)
	readExecutable := localReadExecutable || agentReadExecutable
	reason := "Oracle Data Guard Broker execution requires the restricted signed node controller"
	if controlExecutable {
		reason = "Oracle Data Guard Broker switchover is configured through the restricted node controller"
	}
	if controlExecutable && endpointExecutable {
		reason = "Oracle Data Guard Broker and the fixed writer endpoint are configured as one controlled transition"
	}
	readReason := "Oracle discovery requires the restricted node agent, DGMGRL, or SQLPlus"
	if agentReadExecutable {
		readReason = "Oracle read-only discovery is configured through the restricted signed node agent"
	} else if localReadExecutable {
		readReason = "Oracle read-only discovery is configured through DGMGRL or SQLPlus"
	}
	return adapter.Capabilities{Engine: model.EngineOracle, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover:          {Available: readExecutable, Reason: readReason},
		adapter.CapabilityTopology:          {Available: true, Reason: "Oracle Data Guard topology can be built from discovered broker identity"},
		adapter.CapabilityHealth:            {Available: readExecutable, Reason: readReason},
		adapter.CapabilityPrecheck:          {Available: true, Reason: "Oracle Data Guard Broker prechecks are implemented"},
		adapter.CapabilityPlan:              {Available: true, Reason: "Oracle Data Guard Broker switchover planning is implemented"},
		adapter.CapabilityExecute:           {Available: controlExecutable, Mutating: true, Reason: reason},
		adapter.CapabilityVerify:            {Available: controlExecutable, Reason: reason},
		adapter.CapabilityNodeSync:          {Reason: "Oracle node sync is not implemented"},
		adapter.CapabilityMetadataReconcile: {Available: true, Reason: "Oracle DBID and DB_UNIQUE_NAME metadata checks are implemented"},
		adapter.CapabilityMetrics:           {Available: localReadExecutable, Reason: "Oracle Data Guard lag and health metrics require DGMGRL or SQLPlus on the controller"},
		adapter.CapabilityCandidates:        {Available: true, Reason: "Oracle Data Guard standby candidate evaluation is implemented"},
	}}
}

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	if adapterInstance.discoveryProvider != nil && adapterInstance.discoveryProvider.Executable(ctx) {
		return adapterInstance.discoveryProvider.Discover(ctx, request)
	}
	if adapterInstance.runner != nil && adapterInstance.runner.Executable(ctx) {
		return adapterInstance.discoverWithBroker(ctx, request)
	}
	if adapterInstance.sqlplus != nil && adapterInstance.sqlplus.Executable(ctx) {
		return adapterInstance.discoverWithSQLPlus(ctx, request)
	}
	return adapter.DiscoveryResult{}, adapter.ErrUnsupported
}

func (adapterInstance *Adapter) discoverWithBroker(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	started := time.Now()
	databaseName := strings.TrimSpace(request.Credentials.Database)
	if databaseName == "" {
		databaseName = strings.TrimSpace(request.Endpoint.Hostname)
	}
	if databaseName == "" {
		return adapter.DiscoveryResult{}, fmt.Errorf("Oracle Data Guard Broker discovery requires database service or DB_UNIQUE_NAME")
	}
	output, err := adapterInstance.runner.RunDGMGRL(ctx, request.Endpoint, request.Credentials, []string{"SHOW DATABASE VERBOSE " + databaseName})
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	probe := parseOracleBrokerDatabase(output)
	if probe.dbUniqueName == "" {
		probe.dbUniqueName = databaseName
	}
	role := model.RoleUnknown
	health := model.HealthDegraded
	summary := "Oracle Data Guard database is reachable but broker state is not fully healthy"
	promotionEligible := false
	switch strings.ToUpper(probe.role) {
	case "PRIMARY":
		role = model.RolePrimary
		if probe.healthy {
			health = model.HealthHealthy
			summary = "Oracle Data Guard primary is broker-healthy"
		}
	case "PHYSICAL STANDBY", "LOGICAL STANDBY", "SNAPSHOT STANDBY", "STANDBY":
		role = model.RoleStandby
		if probe.healthy && probe.applyLagSeconds != nil {
			health = model.HealthHealthy
			summary = "Oracle Data Guard standby is broker-healthy"
			promotionEligible = true
		}
	}
	lag := maxInt64Ptr(probe.transportLagSeconds, probe.applyLagSeconds)
	hostname := strings.TrimSpace(request.Endpoint.Hostname)
	if hostname == "" {
		hostname = probe.dbUniqueName
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EngineOracle,
		EngineIdentity: model.EngineIdentity{
			"db_unique_name": probe.dbUniqueName,
			"instance_name":  probe.dbUniqueName,
		},
		DisplayName: probe.dbUniqueName,
		Hostname:    hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        request.Endpoint.Port,
		Role:        role,
		Health: model.Health{
			State:       health,
			Summary:     summary,
			ObservedAt:  time.Now().UTC(),
			LatencyMS:   time.Since(started).Milliseconds(),
			Replication: strings.ToLower(probe.status),
		},
		Replication: model.ReplicationStatus{
			IOThread:   oracleBrokerThreadState(probe.transportLagSeconds, probe.status),
			SQLThread:  oracleBrokerThreadState(probe.applyLagSeconds, probe.status),
			LagSeconds: lag,
		},
		PromotionEligible: promotionEligible,
		EngineMetadata: map[string]string{
			"data_guard_broker":     "enabled",
			"dgbroker":              "enabled",
			"db_unique_name":        probe.dbUniqueName,
			"database_role":         probe.role,
			"database_status":       probe.status,
			"intended_state":        probe.intendedState,
			"transport_lag_seconds": formatOptionalInt64(probe.transportLagSeconds),
			"apply_lag_seconds":     formatOptionalInt64(probe.applyLagSeconds),
		},
	}
	if probe.dbid != "" {
		instance.EngineIdentity["dbid"] = probe.dbid
		instance.EngineMetadata["dbid"] = probe.dbid
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func (adapterInstance *Adapter) discoverWithSQLPlus(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	started := time.Now()
	output, err := adapterInstance.sqlplus.QuerySQLPlus(ctx, request.Endpoint, request.Credentials, oracleSQLPlusDiscoveryQuery)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	probe, err := parseOracleSQLPlusProbe(output)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	role := model.RoleUnknown
	health := model.HealthDegraded
	summary := "Oracle database is reachable but role or open mode requires review"
	promotionEligible := false
	switch strings.ToUpper(probe.role) {
	case "PRIMARY":
		role = model.RolePrimary
		if strings.Contains(strings.ToUpper(probe.openMode), "READ WRITE") {
			health = model.HealthHealthy
			summary = "Oracle primary is reachable"
		}
	case "PHYSICAL STANDBY", "LOGICAL STANDBY", "SNAPSHOT STANDBY", "STANDBY":
		role = model.RoleStandby
		if strings.Contains(strings.ToUpper(probe.openMode), "MOUNTED") || strings.Contains(strings.ToUpper(probe.openMode), "READ ONLY") {
			health = model.HealthHealthy
			summary = "Oracle standby is reachable"
			promotionEligible = true
		}
	}
	lag := maxInt64Ptr(probe.transportLagSeconds, probe.applyLagSeconds)
	hostname := strings.TrimSpace(request.Endpoint.Hostname)
	if probe.hostName != "" {
		hostname = probe.hostName
	}
	if hostname == "" {
		hostname = strings.TrimSpace(request.Endpoint.IPAddress)
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EngineOracle,
		EngineIdentity: model.EngineIdentity{
			"db_unique_name": probe.dbUniqueName,
			"instance_name":  probe.instanceName,
		},
		DisplayName: probe.dbUniqueName,
		Hostname:    hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        request.Endpoint.Port,
		Role:        role,
		Health: model.Health{
			State:       health,
			Summary:     summary,
			ObservedAt:  time.Now().UTC(),
			LatencyMS:   time.Since(started).Milliseconds(),
			Replication: strings.ToLower(probe.switchoverStatus),
		},
		Replication: model.ReplicationStatus{
			IOThread:   oracleBrokerThreadState(probe.transportLagSeconds, probe.openMode),
			SQLThread:  oracleBrokerThreadState(probe.applyLagSeconds, probe.openMode),
			LagSeconds: lag,
		},
		PromotionEligible: promotionEligible,
		EngineMetadata: map[string]string{
			"data_guard_broker":     strings.ToLower(strconv.FormatBool(probe.brokerEnabled)),
			"dgbroker":              strings.ToLower(strconv.FormatBool(probe.brokerEnabled)),
			"db_unique_name":        probe.dbUniqueName,
			"database_name":         probe.databaseName,
			"database_role":         probe.role,
			"open_mode":             probe.openMode,
			"protection_mode":       probe.protectionMode,
			"switchover_status":     probe.switchoverStatus,
			"instance_name":         probe.instanceName,
			"oracle_host_name":      probe.hostName,
			"oracle_version":        probe.version,
			"transport_lag_seconds": formatOptionalInt64(probe.transportLagSeconds),
			"apply_lag_seconds":     formatOptionalInt64(probe.applyLagSeconds),
		},
	}
	if probe.dbid != "" {
		instance.EngineIdentity["dbid"] = probe.dbid
		instance.EngineMetadata["dbid"] = probe.dbid
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func (adapterInstance *Adapter) Topology(_ context.Context, _ adapter.DiscoverRequest, discovery adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	instance := discovery.Instance
	if instance.Engine != model.EngineOracle {
		return adapter.TopologyResult{}, adapter.ErrUnsupported
	}
	// A member-local Broker probe does not provide the primary's complete native
	// identity. The discovery service builds Oracle links only after it has a
	// complete, DBID-consistent observation of every registered member.
	return adapter.TopologyResult{}, nil
}

func (adapterInstance *Adapter) Health(ctx context.Context, request adapter.DiscoverRequest) (model.Health, error) {
	discovery, err := adapterInstance.Discover(ctx, request)
	if err != nil {
		return model.Health{}, err
	}
	return discovery.Instance.Health, nil
}

func (adapterInstance *Adapter) Metrics(ctx context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	if adapterInstance.runner != nil && adapterInstance.runner.Executable(ctx) {
		return adapterInstance.metricsWithBroker(ctx, request)
	}
	if adapterInstance.sqlplus != nil && adapterInstance.sqlplus.Executable(ctx) {
		discovery, err := adapterInstance.discoverWithSQLPlus(ctx, request)
		if err != nil {
			return nil, err
		}
		values := map[string]float64{
			"broker_status_healthy": booleanFloat(oracleBrokerEnabled(discovery.Instance)),
			"role_primary":          booleanFloat(discovery.Instance.Role == model.RolePrimary),
		}
		if discovery.Instance.Replication.LagSeconds != nil {
			values["apply_lag_seconds"] = float64(*discovery.Instance.Replication.LagSeconds)
		}
		return []model.MetricSample{{ObservedAt: time.Now().UTC(), Values: values}}, nil
	}
	return nil, adapter.ErrUnsupported
}

func (adapterInstance *Adapter) metricsWithBroker(ctx context.Context, request adapter.DiscoverRequest) ([]model.MetricSample, error) {
	databaseName := strings.TrimSpace(request.Credentials.Database)
	if databaseName == "" {
		databaseName = strings.TrimSpace(request.Endpoint.Hostname)
	}
	if databaseName == "" {
		return nil, fmt.Errorf("Oracle Data Guard Broker metrics require database service or DB_UNIQUE_NAME")
	}
	output, err := adapterInstance.runner.RunDGMGRL(ctx, request.Endpoint, request.Credentials, []string{"SHOW DATABASE VERBOSE " + databaseName})
	if err != nil {
		return nil, err
	}
	probe := parseOracleBrokerDatabase(output)
	values := map[string]float64{
		"broker_status_healthy": booleanFloat(probe.healthy && strings.Contains(strings.ToUpper(probe.status), "SUCCESS")),
	}
	if probe.transportLagSeconds != nil {
		values["transport_lag_seconds"] = float64(*probe.transportLagSeconds)
	}
	if probe.applyLagSeconds != nil {
		values["apply_lag_seconds"] = float64(*probe.applyLagSeconds)
	}
	if probe.role != "" {
		values["role_primary"] = booleanFloat(strings.EqualFold(probe.role, "PRIMARY"))
	}
	return []model.MetricSample{{ObservedAt: time.Now().UTC(), Values: values}}, nil
}

func (adapterInstance *Adapter) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	assessments := make([]model.CandidateAssessment, 0, len(request.Instances))
	for _, instance := range request.Instances {
		if instance.ResourceID == request.Primary.ResourceID || instance.Engine != model.EngineOracle {
			continue
		}
		checks := []model.Check{
			{Name: "standby_role", Status: passFail(instance.Role == model.RoleStandby), Message: "candidate must be a Data Guard standby"},
			{Name: "healthy", Status: passFail(instance.Health.State == model.HealthHealthy), Message: "candidate must be broker-healthy"},
			{Name: "broker_enabled", Status: passFail(oracleBrokerEnabled(instance)), Message: "candidate must be managed by Data Guard Broker"},
			{Name: "promotion_eligible", Status: passFail(instance.PromotionEligible), Message: "candidate must be promotion eligible"},
		}
		eligible := !hasFailedCheck(checks)
		risk := "low"
		dataLoss := "none"
		if !eligible {
			risk = "high"
			dataLoss = "unknown"
		}
		if instance.Replication.LagSeconds != nil && *instance.Replication.LagSeconds > request.Policy.MaximumLagSeconds && request.Policy.MaximumLagSeconds > 0 {
			eligible = false
			risk = "high"
			dataLoss = "possible"
			checks = append(checks, model.Check{Name: "lag_policy", Status: model.CheckFail, Message: "candidate exceeds maximum apply lag policy"})
		}
		assessments = append(assessments, model.CandidateAssessment{InstanceID: instance.ResourceID, Eligible: eligible, RiskLevel: risk, DataLossRisk: dataLoss, Checks: checks})
	}
	sort.SliceStable(assessments, func(i, j int) bool {
		if assessments[i].Eligible != assessments[j].Eligible {
			return assessments[i].Eligible
		}
		return string(assessments[i].InstanceID) < string(assessments[j].InstanceID)
	})
	rank := 1
	for index := range assessments {
		if assessments[index].Eligible {
			assessments[index].Rank = rank
			rank++
		}
	}
	return assessments, nil
}

func (adapterInstance *Adapter) Precheck(ctx context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Engine != model.EngineOracle || request.Resolved == nil {
		return nil, adapter.ErrUnsupported
	}
	switch request.Operation.Kind {
	case model.OperationSwitchover, model.OperationFailover:
	default:
		return nil, adapter.ErrUnsupported
	}
	checks := oracleBrokerChecks(request)
	if request.Operation.Kind == model.OperationFailover {
		checks = append(checks, model.Check{
			Name: "oracle_failover_fencing", Status: model.CheckFail,
			Message: "Oracle failover remains blocked until old-primary fencing is configured",
		})
		return checks, nil
	}
	checks = append(checks, adapterInstance.controller.Precheck(ctx, *request.Resolved)...)
	if adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx) {
		checks = append(checks, adapterInstance.endpointProvider.Precheck(ctx, *request.Resolved)...)
	}
	return checks, nil
}

func (adapterInstance *Adapter) BuildPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.Precheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	operationName := "switchover"
	if request.Operation.Kind == model.OperationFailover {
		operationName = "failover"
	}
	steps := []model.PlanStep{
		{Index: 1, Name: "dgbroker_validate_configuration", Owner: "oracle", TargetID: resolved.Cluster.ResourceID, Postcondition: "Data Guard Broker reports a valid configuration"},
		{Index: 2, Name: "dgbroker_validate_target", Owner: "oracle", TargetID: resolved.Target.ResourceID, Postcondition: "target standby is broker-visible and ready"},
		{Index: 3, Name: "dgbroker_" + operationName, Owner: "oracle", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target database assumes primary role through DGMGRL"},
		{Index: 4, Name: "verify_oracle_role_transition", Owner: "oracle", TargetID: resolved.Target.ResourceID, Postcondition: "broker reports the expected primary and standby roles"},
	}
	if adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx) {
		steps = []model.PlanStep{
			{Index: 1, Name: "dgbroker_validate_configuration", Owner: "oracle", TargetID: resolved.Cluster.ResourceID, Postcondition: "Data Guard Broker reports a valid configuration"},
			{Index: 2, Name: "dgbroker_validate_target", Owner: "oracle", TargetID: resolved.Target.ResourceID, Postcondition: "target standby is broker-visible and ready"},
			{Index: 3, Name: "authorize_target_transition", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Postcondition: "a durable target ownership lease is active"},
			{Index: 4, Name: "dgbroker_" + operationName, Owner: "oracle", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target database assumes primary role through DGMGRL"},
			{Index: 5, Name: "transfer_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "the fixed VIP is owned only by the new primary"},
			{Index: 6, Name: "verify_oracle_role_transition", Owner: "oracle", TargetID: resolved.Target.ResourceID, Postcondition: "broker reports the expected primary and standby roles"},
			{Index: 7, Name: "verify_writer_endpoint", Owner: "endpoint", TargetID: resolved.Target.ResourceID, Postcondition: "physical and canonical VIP ownership match the new primary"},
		}
	}
	plan := model.OperationPlan{
		OperationID:       request.Operation.ResourceID,
		ClusterID:         request.Operation.ClusterID,
		SourceID:          resolved.Primary.ResourceID,
		TargetID:          request.TargetID,
		Stage:             model.StagePlan,
		ObservationToken:  strings.TrimSpace(resolved.ObservationToken),
		ResourceRevisions: oraclePlanResourceRevisions(resolved),
		Checks:            checks,
		Steps:             steps,
		Mutating:          true,
		Summary:           "Oracle Data Guard Broker " + operationName + " plan is ready",
	}
	if hasFailedCheck(checks) {
		plan.Summary = "Oracle Data Guard Broker " + operationName + " plan is blocked"
	}
	plan.Digest, err = oracleOperationPlanDigest(plan)
	if err != nil {
		return model.OperationPlan{}, err
	}
	return plan, nil
}

func oraclePlanResourceRevisions(resolved *adapter.ResolvedOperation) map[model.ResourceID]uint64 {
	revisions := map[model.ResourceID]uint64{
		resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision,
	}
	for _, instance := range resolved.Snapshot.Instances {
		revisions[instance.ResourceID] = instance.MetadataRevision
	}
	revisions[resolved.Primary.ResourceID] = resolved.Primary.MetadataRevision
	revisions[resolved.Target.ResourceID] = resolved.Target.MetadataRevision
	return revisions
}

func oracleOperationPlanDigest(plan model.OperationPlan) (string, error) {
	canonical := plan
	canonical.ResourceMeta = model.ResourceMeta{}
	canonical.Digest = ""
	contents, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical Oracle operation plan: %w", err)
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (adapterInstance *Adapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if request.Operation.Kind != model.OperationSwitchover {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationUnsupported, StartedAt: started, Message: "only controlled Oracle switchover is implemented"}, adapter.ErrUnsupported
	}
	if adapterInstance.controller == nil || !adapterInstance.controller.Executable(ctx) {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationUnsupported, StartedAt: started, Message: "restricted Oracle Broker controller is not configured"}, adapter.ErrUnsupported
	}
	checks, err := adapterInstance.Precheck(ctx, request)
	if err != nil {
		return model.Execution{}, err
	}
	if hasFailedCheck(checks) {
		execution := model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
			StartedAt: started, FinishedAt: time.Now().UTC(),
			Message: "Oracle Data Guard Broker checks blocked execution",
		}
		return execution, fmt.Errorf("%s", execution.Message)
	}
	leaseID := adapter.OperationLeaseID(ctx)
	if !model.ValidResourceID(leaseID) {
		err := fmt.Errorf("active durable operation lease is required for Oracle switchover")
		return model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
			StartedAt: started, FinishedAt: time.Now().UTC(), Message: err.Error(),
		}, err
	}
	mutationCtx := ctx
	resolved := *request.Resolved
	var authorization adapter.TransitionAuthorization
	endpointManaged := adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx)
	if endpointManaged {
		authorization, err = adapterInstance.endpointProvider.AuthorizeTransition(ctx, resolved)
		if err != nil {
			return model.Execution{
				OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
				StartedAt: started, FinishedAt: time.Now().UTC(),
				Message: "Oracle writer endpoint transition authorization failed",
			}, fmt.Errorf("authorize Oracle writer endpoint transition: %w", err)
		}
		if authorization.Context == nil || authorization.Cancel == nil || authorization.Abort == nil ||
			authorization.Finalize == nil || !model.ValidResourceID(authorization.LeaseID) {
			if authorization.Cancel != nil {
				authorization.Cancel()
			}
			return model.Execution{
				OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
				StartedAt: started, FinishedAt: time.Now().UTC(),
				Message: "Oracle writer endpoint returned an incomplete transition lease",
			}, fmt.Errorf("Oracle writer endpoint returned an incomplete transition lease")
		}
		defer authorization.Cancel()
		mutationCtx = authorization.Context
		leaseID = authorization.LeaseID
		completeStep(context.WithoutCancel(ctx), request.Progress, "authorize_target_transition", "target transition lease is active")
	}
	if err := adapterInstance.controller.Switchover(mutationCtx, resolved, leaseID); err != nil {
		failure := &oracleTransitionFailure{err: fmt.Errorf("Oracle Data Guard Broker switchover outcome requires verification: %w", err)}
		return model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationIndeterminate,
			StartedAt: started, FinishedAt: time.Now().UTC(), Message: failure.Error(),
		}, failure
	}
	completeStep(context.WithoutCancel(ctx), request.Progress, "dgbroker_switchover", "restricted Oracle node agent confirmed the Broker role transition")
	if endpointManaged {
		if err := adapterInstance.endpointProvider.Transfer(mutationCtx, resolved); err != nil {
			failure := &oracleTransitionFailure{err: fmt.Errorf("Oracle role changed but VIP transfer requires recovery: %w", err)}
			return model.Execution{
				OperationID: request.Operation.ResourceID, Status: model.OperationIndeterminate,
				StartedAt: started, FinishedAt: time.Now().UTC(), Message: failure.Error(),
			}, failure
		}
		completeStep(context.WithoutCancel(ctx), request.Progress, "transfer_writer_endpoint", "fixed Oracle VIP follows the new primary")
		endpointCheck := adapterInstance.endpointProvider.Verify(mutationCtx, resolved)
		if endpointCheck.Name != "writer_endpoint_owner" || endpointCheck.Status != model.CheckPass {
			failure := &oracleTransitionFailure{err: fmt.Errorf("Oracle role changed but VIP ownership is not verified")}
			return model.Execution{
				OperationID: request.Operation.ResourceID, Status: model.OperationIndeterminate,
				StartedAt: started, FinishedAt: time.Now().UTC(), Message: failure.Error(),
			}, failure
		}
		completeStep(context.WithoutCancel(ctx), request.Progress, "verify_writer_endpoint", endpointCheck.Message)
		if err := authorization.Finalize(mutationCtx); err != nil {
			failure := &oracleTransitionFailure{err: fmt.Errorf("Oracle role and VIP changed but the ownership lease did not stabilize: %w", err)}
			return model.Execution{
				OperationID: request.Operation.ResourceID, Status: model.OperationIndeterminate,
				StartedAt: started, FinishedAt: time.Now().UTC(), Message: failure.Error(),
			}, failure
		}
	}
	return model.Execution{
		OperationID: request.Operation.ResourceID, Status: model.OperationRunning,
		StartedAt: started, Message: "Oracle Data Guard Broker role and fixed writer endpoint transitioned; verification is required",
	}, nil
}

func (adapterInstance *Adapter) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	if request.Operation.Engine != model.EngineOracle || request.Resolved == nil {
		return model.Verification{}, adapter.ErrUnsupported
	}
	if adapterInstance.controller == nil || !adapterInstance.controller.Executable(ctx) {
		return model.Verification{OperationID: request.Operation.ResourceID, ObservedAt: time.Now().UTC(), Checks: []model.Check{{Name: "oracle_broker_controller", Status: model.CheckFail, Message: "restricted Oracle Broker controller is not configured"}}}, nil
	}
	maxAttempts := adapterInstance.verifyMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var verification model.Verification
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		verification = adapterInstance.verifyOnce(ctx, request)
		if verification.Passed {
			completeStep(ctx, request.Progress, "verify_oracle_role_transition", "both Oracle roles and Broker health were verified")
			return verification, nil
		}
		if attempt == maxAttempts {
			break
		}
		timer := time.NewTimer(adapterInstance.verifyRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return verification, ctx.Err()
		case <-timer.C:
		}
	}
	return verification, nil
}

func (adapterInstance *Adapter) verifyOnce(ctx context.Context, request adapter.OperationRequest) model.Verification {
	observed := time.Now().UTC()
	resolved := *request.Resolved
	type statusResult struct {
		status BrokerControlStatus
		err    error
	}
	targetResult := make(chan statusResult, 1)
	sourceResult := make(chan statusResult, 1)
	go func() {
		status, err := adapterInstance.controller.Status(ctx, resolved, resolved.Target, oracleBrokerName(resolved.Primary))
		targetResult <- statusResult{status: status, err: err}
	}()
	go func() {
		status, err := adapterInstance.controller.Status(ctx, resolved, resolved.Primary, oracleBrokerName(resolved.Target))
		sourceResult <- statusResult{status: status, err: err}
	}()
	target := <-targetResult
	source := <-sourceResult
	targetStatus, targetErr := target.status, target.err
	sourceStatus, sourceErr := source.status, source.err
	checks := []model.Check{
		oracleStatusCheck("oracle_new_primary", targetErr == nil && strings.EqualFold(targetStatus.Role, "PRIMARY"),
			"selected Oracle standby is now PRIMARY", "selected Oracle standby did not become PRIMARY"),
		oracleStatusCheck("oracle_former_primary", sourceErr == nil && strings.Contains(strings.ToUpper(sourceStatus.Role), "STANDBY"),
			"former Oracle primary is now a standby", "former Oracle primary is not a standby"),
		oracleStatusCheck("oracle_broker_configuration",
			targetErr == nil && sourceErr == nil &&
				strings.HasPrefix(strings.ToUpper(targetStatus.ConfigurationStatus), "SUCCESS") &&
				strings.HasPrefix(strings.ToUpper(sourceStatus.ConfigurationStatus), "SUCCESS"),
			"Data Guard Broker configuration is healthy on both nodes", "Data Guard Broker configuration is not healthy on both nodes"),
		oracleStatusCheck("oracle_apply_converged",
			targetErr == nil && targetStatus.TransportLagSeconds != nil && *targetStatus.TransportLagSeconds == 0 &&
				targetStatus.ApplyLagSeconds != nil && *targetStatus.ApplyLagSeconds == 0,
			"former primary is following the new primary with zero lag", "former primary replication has not converged"),
	}
	if adapterInstance.endpointProvider != nil && adapterInstance.endpointProvider.Executable(ctx) {
		checks = append(checks, adapterInstance.endpointProvider.Verify(ctx, resolved))
	}
	passed := !hasFailedCheck(checks)
	return model.Verification{OperationID: request.Operation.ResourceID, Passed: passed, Checks: checks, ObservedAt: observed}
}

type oracleTransitionFailure struct{ err error }

func (failure *oracleTransitionFailure) Error() string        { return failure.err.Error() }
func (failure *oracleTransitionFailure) Unwrap() error        { return failure.err }
func (failure *oracleTransitionFailure) FailureClass() string { return "post_commit_unknown" }

func oracleStatusCheck(name string, passed bool, success, failure string) model.Check {
	if passed {
		return model.Check{Name: name, Status: model.CheckPass, Message: success}
	}
	return model.Check{Name: name, Status: model.CheckFail, Message: failure}
}

func (adapterInstance *Adapter) MetadataPrecheck(_ context.Context, request adapter.MetadataRequest) ([]model.Check, error) {
	if request.Instance.Engine != model.EngineOracle {
		return []model.Check{{Name: "oracle_identity", Status: model.CheckFail, Message: "instance engine must be oracle"}}, nil
	}
	if strings.TrimSpace(request.Instance.EngineIdentity["dbid"]) == "" || strings.TrimSpace(request.Instance.EngineIdentity["db_unique_name"]) == "" {
		return []model.Check{{Name: "oracle_identity", Status: model.CheckFail, Message: "DBID and DB_UNIQUE_NAME are required"}}, nil
	}
	return []model.Check{{Name: "oracle_identity", Status: model.CheckPass, Message: "Oracle DBID and DB_UNIQUE_NAME are stable"}}, nil
}

func (adapterInstance *Adapter) ReconcileMetadata(ctx context.Context, request adapter.MetadataRequest) (adapter.MetadataResult, error) {
	checks, err := adapterInstance.MetadataPrecheck(ctx, request)
	if err != nil {
		return adapter.MetadataResult{}, err
	}
	summary := "Oracle metadata reconciliation may update endpoint aliases without changing resource_id"
	if hasFailedCheck(checks) {
		summary = "Oracle metadata reconciliation is blocked"
	}
	return adapter.MetadataResult{Checks: checks, Summary: summary}, nil
}

func oracleBrokerChecks(request adapter.OperationRequest) []model.Check {
	resolved := request.Resolved
	checks := make([]model.Check, 0, 6)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	add("oracle_cluster_scope", resolved.Cluster.Engine == model.EngineOracle && resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID,
		"operation resources belong to the selected Oracle cluster", "operation resources are outside the selected Oracle cluster")
	add("oracle_identity", sameOracleDBID(resolved.Primary, resolved.Target) && oracleBrokerName(resolved.Target) != "",
		"Oracle DBID and DB_UNIQUE_NAME are present", "Oracle DBID or DB_UNIQUE_NAME is incomplete")
	add("dgbroker_enabled", oracleBrokerEnabled(resolved.Primary) && oracleBrokerEnabled(resolved.Target),
		"Data Guard Broker evidence is enabled for source and target", "Data Guard Broker evidence is missing")
	add("current_primary", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy,
		"current Oracle primary is healthy", "current Oracle primary is not healthy")
	targetStandbyReady := resolved.Target.Role == model.RoleStandby && resolved.Target.Health.State == model.HealthHealthy
	targetStandbyPass := targetStandbyReady
	targetStandbyPassMessage := "target Oracle standby is healthy; live Broker readiness will authorize planned switchover"
	targetStandbyFailMessage := "target Oracle standby is not healthy"
	if request.Operation.Kind == model.OperationFailover {
		targetStandbyPass = targetStandbyReady && resolved.Target.PromotionEligible
		targetStandbyPassMessage = "target Oracle standby is healthy and promotion eligible"
		targetStandbyFailMessage = "target Oracle standby is not promotion eligible"
	}
	add("target_standby", targetStandbyPass, targetStandbyPassMessage, targetStandbyFailMessage)
	if request.Operation.Kind == model.OperationFailover {
		add("failover_primary_state", resolved.Primary.Health.State != model.HealthHealthy,
			"current primary is not healthy, failover path is allowed", "Oracle failover requires failed primary evidence")
	}
	return checks
}

func sameOracleDBID(left, right model.DatabaseInstance) bool {
	dbid := strings.TrimSpace(left.EngineIdentity["dbid"])
	return dbid != "" && dbid == strings.TrimSpace(right.EngineIdentity["dbid"])
}

func oracleBrokerName(instance model.DatabaseInstance) string {
	if value := strings.TrimSpace(instance.EngineIdentity["db_unique_name"]); value != "" {
		return value
	}
	return strings.TrimSpace(instance.DisplayName)
}

func oracleBrokerEnabled(instance model.DatabaseInstance) bool {
	for _, key := range []string{"data_guard_broker", "dgbroker", "broker_managed"} {
		value := strings.ToLower(strings.TrimSpace(instance.EngineMetadata[key]))
		if value == "true" || value == "enabled" || value == "yes" {
			return true
		}
	}
	return false
}

func oracleBrokerEndpoint(instance model.DatabaseInstance) adapter.Endpoint {
	return adapter.Endpoint{Hostname: instance.Hostname, IPAddress: instance.IPAddress, Port: instance.Port}
}

func oracleConnectDescriptor(endpoint adapter.Endpoint, credentials adapter.Credentials) string {
	host := strings.TrimSpace(endpoint.IPAddress)
	if host == "" {
		host = strings.TrimSpace(endpoint.Hostname)
	}
	service := strings.TrimSpace(credentials.Database)
	if service == "" {
		service = strings.TrimSpace(endpoint.Hostname)
	}
	if host == "" || endpoint.Port <= 0 || service == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d/%s", host, endpoint.Port, service)
}

func oracleCredentialConnect(credentials adapter.Credentials, connect string) string {
	username := strings.TrimSpace(credentials.Username)
	if strings.TrimSpace(credentials.Password) == "" {
		return username + "@" + connect
	}
	return username + "/" + credentials.Password + "@" + connect
}

func oracleBrokerOutputHealthy(output []string) bool {
	joined := strings.ToUpper(strings.Join(output, "\n"))
	return !strings.Contains(joined, "ORA-") && !strings.Contains(joined, "ERROR") &&
		(strings.Contains(joined, "SUCCESS") || strings.Contains(joined, "PRIMARY") || strings.Contains(joined, "SUCCESSFUL"))
}

type oracleBrokerProbe struct {
	dbid                string
	dbUniqueName        string
	role                string
	intendedState       string
	status              string
	transportLagSeconds *int64
	applyLagSeconds     *int64
	healthy             bool
}

type oracleSQLPlusProbe struct {
	dbid                string
	databaseName        string
	dbUniqueName        string
	role                string
	openMode            string
	protectionMode      string
	switchoverStatus    string
	instanceName        string
	hostName            string
	version             string
	instanceStatus      string
	brokerEnabled       bool
	transportLagSeconds *int64
	applyLagSeconds     *int64
}

const oracleSQLPlusDiscoveryQuery = `
set heading off feedback off pages 0 lines 32767 trimspool on verify off echo off
select 'DATABASE|'||dbid||'|'||name||'|'||db_unique_name||'|'||database_role||'|'||open_mode||'|'||protection_mode||'|'||switchover_status from v$database;
select 'INSTANCE|'||instance_name||'|'||host_name||'|'||version||'|'||status from v$instance;
select 'PARAMETER|'||name||'|'||value from v$parameter where name = 'dg_broker_start';
select 'DATAGUARD_STAT|'||name||'|'||value from v$dataguard_stats where name in ('transport lag','apply lag');
`

var oracleLagPattern = regexp.MustCompile(`(?i)(\d+)\s+(day|days|hour|hours|minute|minutes|second|seconds)`)

func parseOracleBrokerDatabase(output []string) oracleBrokerProbe {
	probe := oracleBrokerProbe{healthy: true}
	for _, line := range output {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(strings.ToUpper(line), "ORA-") || strings.Contains(lower, "error") {
			probe.healthy = false
		}
		key, value, ok := splitBrokerField(line)
		if !ok {
			if strings.Contains(lower, "database status") && strings.Contains(lower, "success") {
				probe.status = "SUCCESS"
			}
			continue
		}
		switch normalizeBrokerKey(key) {
		case "databaseid", "dbid":
			probe.dbid = strings.TrimSpace(value)
		case "dbuniquename":
			probe.dbUniqueName = strings.TrimSpace(value)
		case "databaserole":
			probe.role = strings.TrimSpace(value)
		case "intendedstate":
			probe.intendedState = strings.TrimSpace(value)
		case "databasestatus":
			probe.status = strings.TrimSpace(value)
			if !strings.Contains(strings.ToUpper(value), "SUCCESS") {
				probe.healthy = false
			}
		case "transportlag":
			probe.transportLagSeconds = parseOracleLagSeconds(value)
		case "applylag":
			probe.applyLagSeconds = parseOracleLagSeconds(value)
		}
	}
	if probe.status == "" {
		probe.status = "UNKNOWN"
	}
	return probe
}

func parseOracleSQLPlusProbe(output []string) (oracleSQLPlusProbe, error) {
	probe := oracleSQLPlusProbe{}
	for _, line := range output {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "SQL>") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(fields[0])) {
		case "DATABASE":
			if len(fields) < 8 {
				continue
			}
			probe.dbid = strings.TrimSpace(fields[1])
			probe.databaseName = strings.TrimSpace(fields[2])
			probe.dbUniqueName = strings.TrimSpace(fields[3])
			probe.role = strings.TrimSpace(fields[4])
			probe.openMode = strings.TrimSpace(fields[5])
			probe.protectionMode = strings.TrimSpace(fields[6])
			probe.switchoverStatus = strings.TrimSpace(fields[7])
		case "INSTANCE":
			if len(fields) < 5 {
				continue
			}
			probe.instanceName = strings.TrimSpace(fields[1])
			probe.hostName = strings.TrimSpace(fields[2])
			probe.version = strings.TrimSpace(fields[3])
			probe.instanceStatus = strings.TrimSpace(fields[4])
		case "PARAMETER":
			if len(fields) >= 3 && strings.EqualFold(strings.TrimSpace(fields[1]), "dg_broker_start") {
				value := strings.ToLower(strings.TrimSpace(fields[2]))
				probe.brokerEnabled = value == "true" || value == "on" || value == "yes"
			}
		case "DATAGUARD_STAT":
			if len(fields) < 3 {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(fields[1])) {
			case "transport lag":
				probe.transportLagSeconds = parseOracleLagSeconds(fields[2])
			case "apply lag":
				probe.applyLagSeconds = parseOracleLagSeconds(fields[2])
			}
		}
	}
	if probe.dbUniqueName == "" || probe.role == "" {
		return oracleSQLPlusProbe{}, fmt.Errorf("Oracle SQLPlus discovery requires DB_UNIQUE_NAME and database role")
	}
	return probe, nil
}

func splitBrokerField(line string) (string, string, bool) {
	if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	return "", "", false
}

func normalizeBrokerKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "", "_", "", "-", "", "(", "", ")", "")
	return replacer.Replace(value)
}

func parseOracleLagSeconds(value string) *int64 {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "unknown") {
		return nil
	}
	if parsed := parseOracleClockLag(value); parsed != nil {
		return parsed
	}
	if strings.EqualFold(value, "0 seconds") || strings.EqualFold(value, "0 second") {
		zero := int64(0)
		return &zero
	}
	var total int64
	matched := false
	for _, match := range oracleLagPattern.FindAllStringSubmatch(value, -1) {
		number, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		matched = true
		switch strings.ToLower(match[2]) {
		case "day", "days":
			total += number * 86400
		case "hour", "hours":
			total += number * 3600
		case "minute", "minutes":
			total += number * 60
		case "second", "seconds":
			total += number
		}
	}
	if !matched {
		return nil
	}
	return &total
}

func parseOracleClockLag(value string) *int64 {
	value = strings.TrimPrefix(strings.TrimSpace(value), "+")
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.Contains(parts[1], ":") {
		return nil
	}
	days, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil
	}
	clock := strings.Split(parts[1], ":")
	if len(clock) != 3 {
		return nil
	}
	hours, err1 := strconv.ParseInt(clock[0], 10, 64)
	minutes, err2 := strconv.ParseInt(clock[1], 10, 64)
	seconds, err3 := strconv.ParseInt(clock[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	total := days*86400 + hours*3600 + minutes*60 + seconds
	return &total
}

func oracleBrokerThreadState(lag *int64, status string) model.ThreadState {
	if lag == nil {
		return model.ThreadUnknown
	}
	if strings.Contains(strings.ToUpper(status), "SUCCESS") {
		return model.ThreadRunning
	}
	return model.ThreadStopped
}

func formatOptionalInt64(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func maxInt64Ptr(left, right *int64) *int64 {
	if left == nil {
		return cloneInt64(right)
	}
	if right == nil {
		return cloneInt64(left)
	}
	value := *left
	if *right > value {
		value = *right
	}
	return &value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func passFail(passed bool) model.CheckStatus {
	if passed {
		return model.CheckPass
	}
	return model.CheckFail
}

func booleanFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func hasFailedCheck(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckFail {
			return true
		}
	}
	return false
}

func completeStep(ctx context.Context, progress adapter.OperationProgress, name, message string) {
	if progress != nil {
		_ = progress.CompleteStep(ctx, name, message)
	}
}

func unsupportedOracleNodeSync(operationID model.ResourceID) model.Execution {
	return model.Execution{OperationID: operationID, Status: model.OperationUnsupported, StartedAt: time.Now().UTC(), Message: "Oracle node sync is not implemented"}
}

func (adapterInstance *Adapter) NodeSyncPrecheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) BuildNodeSyncPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) ExecuteNodeSync(_ context.Context, request adapter.OperationRequest) (model.Execution, error) {
	return unsupportedOracleNodeSync(request.Operation.ResourceID), adapter.ErrUnsupported
}

func (adapterInstance *Adapter) String() string {
	return fmt.Sprintf("oracle.Adapter(executable=%t)", adapterInstance.controller != nil)
}

var _ adapter.DatabaseHAAdapter = (*Adapter)(nil)
