package sqlserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Runner interface {
	Executable(context.Context) bool
	Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) (string, error)
}

type Row map[string]string

type Adapter struct {
	adapter.UnsupportedAdapter
	runner Runner
}

type UnsupportedRunner struct{}

func (UnsupportedRunner) Executable(context.Context) bool { return false }
func (UnsupportedRunner) Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) (string, error) {
	return "", adapter.ErrUnsupported
}

type SQLCmdRunner struct {
	Binary string
}

func (runner SQLCmdRunner) Executable(context.Context) bool {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "sqlcmd"
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

func (runner SQLCmdRunner) Exec(ctx context.Context, endpoint adapter.Endpoint, credentials adapter.Credentials, statement string) (string, error) {
	binary := strings.TrimSpace(runner.Binary)
	if binary == "" {
		binary = "sqlcmd"
	}
	host := strings.TrimSpace(endpoint.IPAddress)
	if host == "" {
		host = strings.TrimSpace(endpoint.Hostname)
	}
	if host == "" || endpoint.Port <= 0 || strings.TrimSpace(credentials.Username) == "" {
		return "", fmt.Errorf("SQL Server endpoint, port, and username are required")
	}
	server := host + "," + strconv.Itoa(endpoint.Port)
	database := strings.TrimSpace(credentials.Database)
	if database == "" {
		database = "master"
	}
	arguments := []string{
		"-S", server,
		"-U", strings.TrimSpace(credentials.Username),
		"-d", database,
		"-b",
		"-W",
		"-s", "|",
		"-h", "-1",
		"-Q", statement,
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = append(os.Environ(), "SQLCMDPASSWORD="+credentials.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sqlcmd command failed: %s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func New() *Adapter {
	return NewWithRunner(SQLCmdRunner{})
}

func NewWithRunner(runner Runner) *Adapter {
	if runner == nil {
		runner = UnsupportedRunner{}
	}
	return &Adapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineSQLServer), runner: runner}
}

func (adapterInstance *Adapter) Engine() model.Engine { return model.EngineSQLServer }

func (adapterInstance *Adapter) Capabilities(ctx context.Context) adapter.Capabilities {
	executable := adapterInstance.runner != nil && adapterInstance.runner.Executable(ctx)
	reason := "SQL Server Always On execution requires configured sqlcmd runner"
	if executable {
		reason = "SQL Server Always On planned failover execution is configured"
	}
	return adapter.Capabilities{Engine: model.EngineSQLServer, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityDiscover:          {Available: executable, Reason: reason},
		adapter.CapabilityTopology:          {Available: true, Reason: "SQL Server topology can be built from discovered Always On identity"},
		adapter.CapabilityHealth:            {Available: executable, Reason: reason},
		adapter.CapabilityPrecheck:          {Available: true, Reason: "SQL Server Always On prechecks are implemented"},
		adapter.CapabilityPlan:              {Available: true, Reason: "SQL Server Always On switchover planning is implemented"},
		adapter.CapabilityExecute:           {Available: executable, Mutating: true, Reason: reason},
		adapter.CapabilityVerify:            {Available: executable, Reason: reason},
		adapter.CapabilityNodeSync:          {Reason: "SQL Server node sync is not implemented"},
		adapter.CapabilityMetadataReconcile: {Available: true, Reason: "SQL Server AG group_id and replica_id metadata checks are implemented"},
		adapter.CapabilityMetrics:           {Available: executable, Reason: "SQL Server Always On synchronization and queue metrics are implemented"},
		adapter.CapabilityCandidates:        {Available: true, Reason: "SQL Server Always On synchronized-secondary candidate evaluation is implemented"},
	}}
}

const sqlServerDiscoveryQuery = `
SET NOCOUNT ON;
SELECT
  CAST(ar.group_id AS varchar(36)) AS group_id,
  CAST(ar.replica_id AS varchar(36)) AS replica_id,
  COALESCE(ag.name, '') AS availability_group_name,
  COALESCE(ar.replica_server_name, SERVERPROPERTY('MachineName')) AS replica_server_name,
  COALESCE(ars.role_desc, '') AS role_desc,
  COALESCE(ars.connected_state_desc, '') AS connected_state_desc,
  COALESCE(ars.synchronization_health_desc, '') AS synchronization_health_desc,
  COALESCE(drs.synchronization_state_desc, ars.synchronization_health_desc, '') AS synchronization_state_desc,
  COALESCE(ar.availability_mode_desc, '') AS availability_mode_desc,
  COALESCE(CAST(drs.log_send_queue_size AS varchar(30)), '') AS log_send_queue_size,
  COALESCE(CAST(drs.redo_queue_size AS varchar(30)), '') AS redo_queue_size
FROM sys.availability_replicas ar
JOIN sys.availability_groups ag ON ag.group_id = ar.group_id
LEFT JOIN sys.dm_hadr_availability_replica_states ars ON ar.replica_id = ars.replica_id
LEFT JOIN sys.dm_hadr_database_replica_states drs ON ar.replica_id = drs.replica_id AND drs.is_local = 1
WHERE ar.replica_server_name = @@SERVERNAME OR ars.is_local = 1
ORDER BY ag.name, ar.replica_server_name;
`

func (adapterInstance *Adapter) Discover(ctx context.Context, request adapter.DiscoverRequest) (adapter.DiscoveryResult, error) {
	if adapterInstance.runner == nil || !adapterInstance.runner.Executable(ctx) {
		return adapter.DiscoveryResult{}, adapter.ErrUnsupported
	}
	started := time.Now()
	output, err := adapterInstance.runner.Exec(ctx, request.Endpoint, request.Credentials, sqlServerDiscoveryQuery)
	if err != nil {
		return adapter.DiscoveryResult{}, err
	}
	rows := parseSQLServerRows(output)
	if len(rows) == 0 {
		return adapter.DiscoveryResult{}, fmt.Errorf("SQL Server Always On discovery returned no local replica")
	}
	row := rows[0]
	groupID := strings.TrimSpace(row["group_id"])
	replicaID := strings.TrimSpace(row["replica_id"])
	if groupID == "" || replicaID == "" {
		return adapter.DiscoveryResult{}, fmt.Errorf("SQL Server Always On discovery requires group_id and replica_id")
	}
	roleDesc := strings.ToUpper(strings.TrimSpace(row["role_desc"]))
	role := model.RoleUnknown
	health := model.HealthDegraded
	summary := "SQL Server Always On replica is reachable but role is not healthy"
	promotionEligible := false
	if roleDesc == "PRIMARY" {
		role = model.RolePrimary
		health = model.HealthHealthy
		summary = "SQL Server Always On primary replica is reachable"
	} else if roleDesc == "SECONDARY" {
		role = model.RoleReplica
		if strings.EqualFold(row["connected_state_desc"], "CONNECTED") &&
			strings.EqualFold(row["synchronization_health_desc"], "HEALTHY") {
			health = model.HealthHealthy
			summary = "SQL Server Always On secondary is connected and synchronizing"
			if sqlServerRowSynchronized(row) {
				summary = "SQL Server Always On secondary is connected and synchronized"
			}
			promotionEligible = sqlServerRowSynchronized(row) && sqlServerRowSynchronous(row)
		} else {
			summary = "SQL Server Always On secondary is not healthy"
		}
	}
	lag := sqlServerQueueLag(row)
	hostname := strings.TrimSpace(row["replica_server_name"])
	if hostname == "" {
		hostname = strings.TrimSpace(request.Endpoint.Hostname)
	}
	instance := model.DatabaseInstance{
		ClusterID: request.ClusterID,
		Engine:    model.EngineSQLServer,
		EngineIdentity: model.EngineIdentity{
			"group_id":   groupID,
			"replica_id": replicaID,
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   request.Endpoint.IPAddress,
		Port:        request.Endpoint.Port,
		Role:        role,
		Health: model.Health{
			State:       health,
			Summary:     summary,
			ObservedAt:  time.Now().UTC(),
			LatencyMS:   time.Since(started).Milliseconds(),
			Replication: strings.ToLower(row["synchronization_state_desc"]),
		},
		Replication: model.ReplicationStatus{
			IOThread:   sqlServerConnectedState(row["connected_state_desc"]),
			SQLThread:  sqlServerSyncState(row["synchronization_state_desc"]),
			LagSeconds: lag,
		},
		PromotionEligible: promotionEligible,
		EngineMetadata: map[string]string{
			"always_on":                   "enabled",
			"availability_group_name":     strings.TrimSpace(row["availability_group_name"]),
			"role_desc":                   strings.TrimSpace(row["role_desc"]),
			"connected_state_desc":        strings.TrimSpace(row["connected_state_desc"]),
			"synchronization_health_desc": strings.TrimSpace(row["synchronization_health_desc"]),
			"synchronization_state_desc":  strings.TrimSpace(row["synchronization_state_desc"]),
			"synchronization_state":       strings.TrimSpace(row["synchronization_state_desc"]),
			"availability_mode_desc":      strings.TrimSpace(row["availability_mode_desc"]),
			"availability_mode":           strings.TrimSpace(row["availability_mode_desc"]),
			"log_send_queue_size":         strings.TrimSpace(row["log_send_queue_size"]),
			"redo_queue_size":             strings.TrimSpace(row["redo_queue_size"]),
		},
	}
	return adapter.DiscoveryResult{Instance: instance}, nil
}

func (adapterInstance *Adapter) Topology(_ context.Context, _ adapter.DiscoverRequest, discovery adapter.DiscoveryResult) (adapter.TopologyResult, error) {
	instance := discovery.Instance
	if instance.Engine != model.EngineSQLServer {
		return adapter.TopologyResult{}, adapter.ErrUnsupported
	}
	// A local secondary probe cannot safely identify the primary replica UUID.
	// Discovery reconciles AG links after all replica identities and roles exist.
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
	if adapterInstance.runner == nil || !adapterInstance.runner.Executable(ctx) {
		return nil, adapter.ErrUnsupported
	}
	output, err := adapterInstance.runner.Exec(ctx, request.Endpoint, request.Credentials, sqlServerDiscoveryQuery)
	if err != nil {
		return nil, err
	}
	rows := parseSQLServerRows(output)
	if len(rows) == 0 {
		return nil, fmt.Errorf("SQL Server Always On metrics returned no local replica")
	}
	row := rows[0]
	values := map[string]float64{
		"always_on_healthy":    booleanFloat(strings.EqualFold(row["synchronization_health_desc"], "HEALTHY")),
		"connected":            booleanFloat(strings.EqualFold(row["connected_state_desc"], "CONNECTED")),
		"synchronized":         booleanFloat(sqlServerRowSynchronized(row)),
		"synchronous_commit":   booleanFloat(sqlServerRowSynchronous(row)),
		"log_send_queue_bytes": parseSQLServerMetric(row["log_send_queue_size"]),
		"redo_queue_bytes":     parseSQLServerMetric(row["redo_queue_size"]),
		"role_primary":         booleanFloat(strings.EqualFold(row["role_desc"], "PRIMARY")),
		"role_secondary":       booleanFloat(strings.EqualFold(row["role_desc"], "SECONDARY")),
	}
	return []model.MetricSample{{ObservedAt: time.Now().UTC(), Values: values}}, nil
}

func (adapterInstance *Adapter) EvaluateCandidates(_ context.Context, request adapter.CandidateRequest) ([]model.CandidateAssessment, error) {
	assessments := make([]model.CandidateAssessment, 0, len(request.Instances))
	for _, instance := range request.Instances {
		if instance.ResourceID == request.Primary.ResourceID || instance.Engine != model.EngineSQLServer {
			continue
		}
		checks := []model.Check{
			{Name: "secondary_role", Status: passFail(instance.Role == model.RoleReplica), Message: "candidate must be an Always On secondary"},
			{Name: "healthy", Status: passFail(instance.Health.State == model.HealthHealthy), Message: "candidate must be healthy"},
			{Name: "synchronized", Status: passFail(sqlServerRowSynchronized(instance.EngineMetadata)), Message: "candidate must be synchronized"},
			{Name: "synchronous_commit", Status: passFail(sqlServerRowSynchronous(instance.EngineMetadata)), Message: "candidate must use synchronous commit"},
		}
		eligible := !hasFailedCheck(checks) && instance.PromotionEligible
		risk := "low"
		dataLoss := "none"
		if !eligible {
			risk = "high"
			dataLoss = "unknown"
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

func (adapterInstance *Adapter) Precheck(_ context.Context, request adapter.OperationRequest) ([]model.Check, error) {
	if request.Operation.Engine != model.EngineSQLServer || request.Resolved == nil {
		return nil, adapter.ErrUnsupported
	}
	switch request.Operation.Kind {
	case model.OperationSwitchover, model.OperationFailover:
	default:
		return nil, adapter.ErrUnsupported
	}
	return sqlServerAlwaysOnChecks(request), nil
}

func (adapterInstance *Adapter) BuildPlan(ctx context.Context, request adapter.OperationRequest) (model.OperationPlan, error) {
	checks, err := adapterInstance.Precheck(ctx, request)
	if err != nil {
		return model.OperationPlan{}, err
	}
	resolved := request.Resolved
	operationName := "planned failover"
	if request.Operation.Kind == model.OperationFailover {
		operationName = "forced failover"
	}
	steps := []model.PlanStep{
		{Index: 1, Name: "validate_availability_group", Owner: "sqlserver", TargetID: resolved.Cluster.ResourceID, Postcondition: "AG metadata and replica health remain valid"},
		{Index: 2, Name: "validate_synchronous_target", Owner: "sqlserver", TargetID: resolved.Target.ResourceID, Postcondition: "target replica is synchronized and failover-ready"},
		{Index: 3, Name: "alwayson_failover", Owner: "sqlserver", TargetID: resolved.Target.ResourceID, Mutating: true, Postcondition: "target replica accepts the primary role through Always On"},
		{Index: 4, Name: "verify_alwayson_role_transition", Owner: "sqlserver", TargetID: resolved.Target.ResourceID, Postcondition: "AG reports one primary and healthy secondary replicas"},
	}
	plan := model.OperationPlan{
		OperationID:       request.Operation.ResourceID,
		ClusterID:         request.Operation.ClusterID,
		SourceID:          resolved.Primary.ResourceID,
		TargetID:          request.TargetID,
		Stage:             model.StagePlan,
		ObservationToken:  sqlServerObservationToken(resolved),
		ResourceRevisions: sqlServerPlanResourceRevisions(resolved),
		Checks:            checks,
		Steps:             steps,
		Mutating:          true,
		Summary:           "SQL Server Always On " + operationName + " plan is ready",
	}
	if hasFailedCheck(checks) {
		plan.Summary = "SQL Server Always On " + operationName + " plan is blocked"
	}
	plan.Digest, err = sqlServerOperationPlanDigest(plan)
	if err != nil {
		return model.OperationPlan{}, err
	}
	return plan, nil
}

func sqlServerObservationToken(resolved *adapter.ResolvedOperation) string {
	if resolved == nil {
		return ""
	}
	if token := strings.TrimSpace(resolved.ObservationToken); token != "" {
		return token
	}
	if resolved.Snapshot.ObservedAt.IsZero() {
		return ""
	}
	return string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano)
}

func sqlServerPlanResourceRevisions(resolved *adapter.ResolvedOperation) map[model.ResourceID]uint64 {
	revisions := map[model.ResourceID]uint64{resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision}
	for _, instance := range resolved.Snapshot.Instances {
		revisions[instance.ResourceID] = instance.MetadataRevision
	}
	revisions[resolved.Primary.ResourceID] = resolved.Primary.MetadataRevision
	revisions[resolved.Target.ResourceID] = resolved.Target.MetadataRevision
	return revisions
}

func sqlServerOperationPlanDigest(plan model.OperationPlan) (string, error) {
	canonical := plan
	canonical.ResourceMeta = model.ResourceMeta{}
	canonical.Digest = ""
	contents, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical SQL Server operation plan: %w", err)
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateSQLServerOperationPlan(request adapter.OperationRequest, verification bool) error {
	if request.Operation.Kind != model.OperationSwitchover || request.Operation.Engine != model.EngineSQLServer ||
		request.Resolved == nil || request.Plan == nil {
		return fmt.Errorf("an immutable SQL Server switchover plan and resolved context are required")
	}
	resolved := request.Resolved
	plan := request.Plan
	if plan.OperationID != request.Operation.ResourceID || plan.ClusterID != request.Operation.ClusterID ||
		plan.SourceID != resolved.Primary.ResourceID || plan.TargetID != request.TargetID ||
		plan.TargetID != resolved.Target.ResourceID {
		return fmt.Errorf("SQL Server operation plan resource scope changed")
	}
	if plan.Stage != model.StagePlan || strings.TrimSpace(plan.ObservationToken) == "" || strings.TrimSpace(plan.Digest) == "" {
		return fmt.Errorf("SQL Server operation plan integrity fields are missing")
	}
	if !verification && plan.ObservationToken != sqlServerObservationToken(resolved) {
		return fmt.Errorf("SQL Server operation observation changed")
	}
	digest, err := sqlServerOperationPlanDigest(*plan)
	if err != nil || digest != plan.Digest {
		return fmt.Errorf("SQL Server operation plan digest changed")
	}
	if hasFailedCheck(plan.Checks) {
		return fmt.Errorf("SQL Server operation plan contains blocking checks")
	}
	semanticObservation := strings.TrimSpace(resolved.ObservationToken) != ""
	expected := sqlServerPlanResourceRevisions(resolved)
	if len(plan.ResourceRevisions) != len(expected) {
		return fmt.Errorf("SQL Server operation resource revisions are incomplete")
	}
	for resourceID, revision := range expected {
		plannedRevision := plan.ResourceRevisions[resourceID]
		if !model.ValidResourceID(resourceID) || revision == 0 || plannedRevision == 0 ||
			(!verification && !semanticObservation && plannedRevision != revision) {
			return fmt.Errorf("SQL Server operation resource revision changed")
		}
	}
	return nil
}

func (adapterInstance *Adapter) Execute(ctx context.Context, request adapter.OperationRequest) (model.Execution, error) {
	started := time.Now().UTC()
	if adapterInstance.runner == nil || !adapterInstance.runner.Executable(ctx) {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationUnsupported, StartedAt: started, Message: "sqlcmd runner is not configured"}, adapter.ErrUnsupported
	}
	if request.Operation.Kind == model.OperationFailover {
		return model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
			StartedAt: started, FinishedAt: time.Now().UTC(),
			Message: "forced SQL Server failover is blocked until explicit data-loss approval policy is configured",
		}, nil
	}
	if err := validateSQLServerOperationPlan(request, false); err != nil {
		return model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationFailed,
			StartedAt: started, FinishedAt: time.Now().UTC(), Message: "SQL Server immutable operation plan is invalid",
		}, err
	}
	if !model.ValidResourceID(adapter.OperationLeaseID(ctx)) {
		err := fmt.Errorf("active durable operation lease is required for SQL Server switchover")
		return model.Execution{
			OperationID: request.Operation.ResourceID, Status: model.OperationBlocked,
			StartedAt: started, FinishedAt: time.Now().UTC(), Message: err.Error(),
		}, err
	}
	checks, err := adapterInstance.Precheck(ctx, request)
	if err != nil {
		return model.Execution{}, err
	}
	if hasFailedCheck(checks) {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationBlocked, StartedAt: started, FinishedAt: time.Now().UTC(), Message: "SQL Server Always On checks blocked execution"}, nil
	}
	statement := "ALTER AVAILABILITY GROUP " + quoteSQLServerIdentifier(sqlServerAGName(request.Resolved.Cluster, request.Resolved.Target)) + " FAILOVER"
	if _, err := adapterInstance.runner.Exec(ctx, sqlServerEndpoint(request.Resolved.Target), request.Credentials, statement); err != nil {
		return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationFailed, StartedAt: started, FinishedAt: time.Now().UTC(), Message: "SQL Server Always On failover command failed"}, err
	}
	completeStep(ctx, request.Progress, "alwayson_failover", "Always On accepted the role transition")
	return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationRunning, StartedAt: started, Message: "SQL Server Always On planned failover executed; verification is required"}, nil
}

func sqlServerRoleVerificationQuery(groupID, targetReplicaID string) string {
	return `
SET NOCOUNT ON;
SELECT
  SUM(CASE WHEN ars.role_desc = 'PRIMARY' THEN 1 ELSE 0 END) AS primary_count,
  MAX(CASE WHEN ar.replica_id = CONVERT(uniqueidentifier, ` + quoteSQLServerLiteral(targetReplicaID) + `)
            AND ars.role_desc = 'PRIMARY' THEN 1 ELSE 0 END) AS target_primary,
  MIN(CASE WHEN ars.role_desc IN ('PRIMARY', 'SECONDARY')
            AND ars.synchronization_health_desc = 'HEALTHY' THEN 1 ELSE 0 END) AS all_healthy
FROM sys.availability_replicas ar
JOIN sys.dm_hadr_availability_replica_states ars ON ar.replica_id = ars.replica_id
WHERE ar.group_id = CONVERT(uniqueidentifier, ` + quoteSQLServerLiteral(groupID) + `);
`
}

func parseSQLServerVerificationEvidence(output string) (int, bool, bool, bool) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 {
			continue
		}
		primaryCount, firstErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		targetPrimary, secondErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		allHealthy, thirdErr := strconv.Atoi(strings.TrimSpace(parts[2]))
		if firstErr == nil && secondErr == nil && thirdErr == nil {
			return primaryCount, targetPrimary == 1, allHealthy == 1, true
		}
	}
	return 0, false, false, false
}

func (adapterInstance *Adapter) Verify(ctx context.Context, request adapter.OperationRequest) (model.Verification, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observed := time.Now().UTC()
	if request.Operation.Engine != model.EngineSQLServer || request.Resolved == nil {
		return model.Verification{}, adapter.ErrUnsupported
	}
	if err := validateSQLServerOperationPlan(request, true); err != nil {
		return model.Verification{
			OperationID: request.Operation.ResourceID, ObservedAt: observed,
			Checks: []model.Check{{Name: "immutable_operation_plan", Status: model.CheckFail, Message: err.Error()}},
		}, err
	}
	if adapterInstance.runner == nil || !adapterInstance.runner.Executable(ctx) {
		return model.Verification{OperationID: request.Operation.ResourceID, ObservedAt: observed, Checks: []model.Check{{Name: "sqlcmd_runner", Status: model.CheckFail, Message: "sqlcmd runner is not configured"}}}, nil
	}
	query := sqlServerRoleVerificationQuery(
		request.Resolved.Cluster.EngineIdentity["group_id"],
		request.Resolved.Target.EngineIdentity["replica_id"],
	)
	checks := []model.Check{
		{Name: "single_primary", Status: model.CheckFail, Message: "Always On does not report exactly one primary"},
		{Name: "target_primary", Status: model.CheckFail, Message: "selected target is not the Always On primary"},
		{Name: "availability_group_healthy", Status: model.CheckFail, Message: "one or more Always On replicas are unhealthy"},
	}
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return model.Verification{OperationID: request.Operation.ResourceID, Checks: checks, ObservedAt: time.Now().UTC()}, err
		}
		output, err := adapterInstance.runner.Exec(ctx, sqlServerEndpoint(request.Resolved.Target), request.Credentials, query)
		if err == nil {
			primaryCount, targetPrimary, allHealthy, parsed := parseSQLServerVerificationEvidence(output)
			if parsed {
				checks[0] = model.Check{Name: "single_primary", Status: passFail(primaryCount == 1), Message: "Always On reports exactly one primary"}
				checks[1] = model.Check{Name: "target_primary", Status: passFail(targetPrimary), Message: "selected target owns the Always On primary role"}
				checks[2] = model.Check{Name: "availability_group_healthy", Status: passFail(allHealthy), Message: "all Always On replica states are healthy"}
				if !hasFailedCheck(checks) {
					return model.Verification{OperationID: request.Operation.ResourceID, Passed: true, Checks: checks, ObservedAt: time.Now().UTC()}, nil
				}
				lastErr = fmt.Errorf("Always On role transition is not complete")
			} else {
				lastErr = fmt.Errorf("Always On verification returned incomplete evidence")
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return model.Verification{OperationID: request.Operation.ResourceID, Passed: false, Checks: checks, ObservedAt: time.Now().UTC()}, lastErr
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (adapterInstance *Adapter) MetadataPrecheck(_ context.Context, request adapter.MetadataRequest) ([]model.Check, error) {
	if request.Instance.Engine != model.EngineSQLServer {
		return []model.Check{{Name: "sqlserver_identity", Status: model.CheckFail, Message: "instance engine must be sqlserver"}}, nil
	}
	if strings.TrimSpace(request.Instance.EngineIdentity["group_id"]) == "" || strings.TrimSpace(request.Instance.EngineIdentity["replica_id"]) == "" {
		return []model.Check{{Name: "sqlserver_identity", Status: model.CheckFail, Message: "group_id and replica_id are required"}}, nil
	}
	return []model.Check{{Name: "sqlserver_identity", Status: model.CheckPass, Message: "SQL Server group_id and replica_id are stable"}}, nil
}

func (adapterInstance *Adapter) ReconcileMetadata(ctx context.Context, request adapter.MetadataRequest) (adapter.MetadataResult, error) {
	checks, err := adapterInstance.MetadataPrecheck(ctx, request)
	if err != nil {
		return adapter.MetadataResult{}, err
	}
	summary := "SQL Server metadata reconciliation may update endpoint aliases without changing resource_id"
	if hasFailedCheck(checks) {
		summary = "SQL Server metadata reconciliation is blocked"
	}
	return adapter.MetadataResult{Checks: checks, Summary: summary}, nil
}

func sqlServerAlwaysOnChecks(request adapter.OperationRequest) []model.Check {
	resolved := request.Resolved
	checks := make([]model.Check, 0, 8)
	add := func(name string, passed bool, pass, fail string) {
		status, message := model.CheckPass, pass
		if !passed {
			status, message = model.CheckFail, fail
		}
		checks = append(checks, model.Check{Name: name, Status: status, Message: message})
	}
	add("sqlserver_cluster_scope", resolved.Cluster.Engine == model.EngineSQLServer && resolved.Primary.ClusterID == resolved.Cluster.ResourceID && resolved.Target.ClusterID == resolved.Cluster.ResourceID,
		"operation resources belong to the selected SQL Server AG", "operation resources are outside the selected SQL Server AG")
	add("availability_group_identity", sameSQLServerAG(resolved.Primary, resolved.Target) && strings.TrimSpace(resolved.Cluster.EngineIdentity["group_id"]) != "",
		"SQL Server AG group_id and replica_id are present", "SQL Server AG identity is incomplete")
	add("alwayson_enabled", sqlServerAlwaysOnEnabled(resolved.Primary) && sqlServerAlwaysOnEnabled(resolved.Target),
		"Always On evidence is enabled for source and target", "Always On evidence is missing")
	add("current_primary", resolved.Primary.Role == model.RolePrimary && resolved.Primary.Health.State == model.HealthHealthy,
		"current SQL Server primary replica is healthy", "current SQL Server primary replica is not healthy")
	add("target_secondary", resolved.Target.Role == model.RoleReplica && resolved.Target.Health.State == model.HealthHealthy && resolved.Target.PromotionEligible,
		"target SQL Server secondary replica is healthy and promotion eligible", "target SQL Server secondary replica is not promotion eligible")
	add("target_synchronized", strings.EqualFold(resolved.Target.EngineMetadata["synchronization_state"], "SYNCHRONIZED") || strings.EqualFold(resolved.Target.EngineMetadata["synchronization_state_desc"], "SYNCHRONIZED"),
		"target replica is synchronized", "target replica is not synchronized")
	add("availability_mode", strings.EqualFold(resolved.Target.EngineMetadata["availability_mode"], "SYNCHRONOUS_COMMIT") || strings.EqualFold(resolved.Target.EngineMetadata["availability_mode_desc"], "SYNCHRONOUS_COMMIT"),
		"target replica uses synchronous commit", "planned failover requires synchronous commit")
	if request.Operation.Kind == model.OperationFailover {
		add("forced_failover_policy", strings.EqualFold(request.Parameters["allow_data_loss"], "true"),
			"explicit data-loss approval parameter is present", "forced failover is blocked without explicit data-loss approval")
	}
	return checks
}

func sameSQLServerAG(left, right model.DatabaseInstance) bool {
	groupID := strings.TrimSpace(left.EngineIdentity["group_id"])
	return groupID != "" && groupID == strings.TrimSpace(right.EngineIdentity["group_id"]) &&
		strings.TrimSpace(left.EngineIdentity["replica_id"]) != "" && strings.TrimSpace(right.EngineIdentity["replica_id"]) != ""
}

func sqlServerAlwaysOnEnabled(instance model.DatabaseInstance) bool {
	for _, key := range []string{"always_on", "availability_group", "hadr_enabled"} {
		value := strings.ToLower(strings.TrimSpace(instance.EngineMetadata[key]))
		if value == "true" || value == "enabled" || value == "yes" || value == "1" {
			return true
		}
	}
	return false
}

func sqlServerAGName(cluster model.DatabaseCluster, target model.DatabaseInstance) string {
	if value := strings.TrimSpace(target.EngineMetadata["availability_group_name"]); value != "" {
		return value
	}
	if value := strings.TrimSpace(cluster.DisplayName); value != "" {
		return value
	}
	return strings.TrimSpace(cluster.EngineIdentity["group_id"])
}

func sqlServerEndpoint(instance model.DatabaseInstance) adapter.Endpoint {
	return adapter.Endpoint{Hostname: instance.Hostname, IPAddress: instance.IPAddress, Port: instance.Port}
}

func quoteSQLServerIdentifier(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "]", "]]")
	return "[" + value + "]"
}

func quoteSQLServerLiteral(value string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(value), "'", "''") + "'"
}

func parseSQLServerRows(output string) []Row {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	rows := make([]Row, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.Contains(strings.ToLower(line), "rows affected") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 11 {
			continue
		}
		row := Row{
			"group_id":                    strings.TrimSpace(parts[0]),
			"replica_id":                  strings.TrimSpace(parts[1]),
			"availability_group_name":     strings.TrimSpace(parts[2]),
			"replica_server_name":         strings.TrimSpace(parts[3]),
			"role_desc":                   strings.TrimSpace(parts[4]),
			"connected_state_desc":        strings.TrimSpace(parts[5]),
			"synchronization_health_desc": strings.TrimSpace(parts[6]),
			"synchronization_state_desc":  strings.TrimSpace(parts[7]),
			"availability_mode_desc":      strings.TrimSpace(parts[8]),
			"log_send_queue_size":         strings.TrimSpace(parts[9]),
			"redo_queue_size":             strings.TrimSpace(parts[10]),
		}
		rows = append(rows, row)
	}
	return rows
}

func sqlServerRowSynchronized(values map[string]string) bool {
	return strings.EqualFold(values["synchronization_state"], "SYNCHRONIZED") ||
		strings.EqualFold(values["synchronization_state_desc"], "SYNCHRONIZED")
}

func sqlServerRowSynchronous(values map[string]string) bool {
	return strings.EqualFold(values["availability_mode"], "SYNCHRONOUS_COMMIT") ||
		strings.EqualFold(values["availability_mode_desc"], "SYNCHRONOUS_COMMIT")
}

func sqlServerConnectedState(value string) model.ThreadState {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CONNECTED":
		return model.ThreadRunning
	case "CONNECTING":
		return model.ThreadConnecting
	case "":
		return model.ThreadUnknown
	default:
		return model.ThreadStopped
	}
}

func sqlServerSyncState(value string) model.ThreadState {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "SYNCHRONIZED", "SYNCHRONIZING":
		return model.ThreadRunning
	case "":
		return model.ThreadUnknown
	default:
		return model.ThreadStopped
	}
}

func sqlServerQueueLag(values map[string]string) *int64 {
	var seen bool
	for _, key := range []string{"log_send_queue_size", "redo_queue_size"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err == nil && parsed >= 0 {
				seen = true
				if parsed > 0 {
					return nil
				}
			}
		}
	}
	if !seen {
		return nil
	}
	zero := int64(0)
	return &zero
}

func parseSQLServerMetric(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed * 1024
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

func (adapterInstance *Adapter) NodeSyncPrecheck(context.Context, adapter.OperationRequest) ([]model.Check, error) {
	return nil, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) BuildNodeSyncPlan(context.Context, adapter.OperationRequest) (model.OperationPlan, error) {
	return model.OperationPlan{}, adapter.ErrUnsupported
}
func (adapterInstance *Adapter) ExecuteNodeSync(_ context.Context, request adapter.OperationRequest) (model.Execution, error) {
	return model.Execution{OperationID: request.Operation.ResourceID, Status: model.OperationUnsupported, StartedAt: time.Now().UTC(), Message: "SQL Server node sync is not implemented"}, adapter.ErrUnsupported
}

func (adapterInstance *Adapter) String() string {
	return fmt.Sprintf("sqlserver.Adapter(executable=%t)", adapterInstance.runner != nil)
}

var _ adapter.DatabaseHAAdapter = (*Adapter)(nil)
