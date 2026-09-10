package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// maximumPowerReplicaLagSeconds bounds how much replication lag a replica may
// show before a shutdown precheck blocks (conservative, matching the
// candidate policy default).
const maximumPowerReplicaLagSeconds = int64(60)

// powerShutdownPlanSteps mirrors the PowerShutdownAdapter plan so the API and
// the workflow describe the same shutdown. The persist step is engine-aware,
// exactly like the adapter: it applies only to engines with a
// PERSIST_ONLY-style read-only setting (MySQL).
func powerShutdownPlanSteps(engine model.Engine) []model.PlanStep {
	steps := []model.PlanStep{
		{Index: 1, Name: "freeze_recovery", Owner: "power", Mutating: true, Postcondition: "cluster recovery freeze active"},
		{Index: 2, Name: "set_maintenance", Owner: "power", Mutating: true, Postcondition: "every instance in maintenance"},
	}
	nextIndex := 3
	if engine == model.EngineMySQL {
		steps = append(steps, model.PlanStep{Index: nextIndex, Name: "persist_read_only", Owner: "power",
			Mutating: true, Postcondition: "PERSIST_ONLY read_only active on every node"})
		nextIndex++
	}
	steps = append(steps, model.PlanStep{Index: nextIndex, Name: "prepare_recovery_snapshot", Owner: "power",
		Mutating: true, Postcondition: "recovery snapshot durably stored on every node"})
	nextIndex++
	if engine == model.EngineMySQL {
		steps = append(steps, model.PlanStep{Index: nextIndex, Name: "release_vip_and_isolate", Owner: "power",
			Mutating: true, Postcondition: "VIP absent and every MySQL node durably read-only"})
		nextIndex++
	}
	return append(steps,
		model.PlanStep{Index: nextIndex, Name: "stop_replicas", Owner: "power", Mutating: true,
			Postcondition: "database service stopped on all replicas"},
		model.PlanStep{Index: nextIndex + 1, Name: "stop_primary", Owner: "power", Mutating: true,
			Postcondition: "database service stopped on the primary"},
	)
}

func (server *Server) powerRoute(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID, action string) {
	switch action {
	case "power/precheck":
		server.powerPrecheck(writer, request, clusterID)
	case "power/plan":
		server.powerPlan(writer, request, clusterID)
	case "power/execute":
		server.powerExecute(writer, request, clusterID)
	case "power/cancel":
		server.powerCancel(writer, request, clusterID)
	case "power/boot-detected":
		server.powerBootDetected(writer, request, clusterID)
	case "power/recovering":
		server.powerRecovering(writer, request, clusterID)
	case "power/verify":
		server.powerVerify(writer, request, clusterID)
	case "power/complete":
		server.powerComplete(writer, request, clusterID)
	case "power/fail":
		server.powerFail(writer, request, clusterID)
	case "power/status":
		server.powerStatus(writer, request, clusterID)
	default:
		writeError(writer, http.StatusNotFound, "unknown power action")
	}
}

// powerRequester names the operator driving a power lifecycle action. The
// payload wins; otherwise fall back to the authenticated principal, then to a
// generic label.
func powerRequester(request *http.Request, requested string) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed
	}
	if authentication, platformSession := requestAuthentication(request); platformSession && authentication.viaSession {
		return authentication.principal.Username
	}
	return "power-operator"
}

func (server *Server) activePowerOperationOrError(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) (model.PowerOperation, bool) {
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "no active power operation for this cluster; run power/precheck first")
		return model.PowerOperation{}, false
	}
	return operation, true
}

func powerSemisyncIdleReplicaExceptions(engine model.Engine, snapshot model.TopologySnapshot) map[model.ResourceID]struct{} {
	exceptions := make(map[model.ResourceID]struct{})
	if engine != model.EngineMySQL {
		return exceptions
	}
	var primary model.DatabaseInstance
	primaryCount := 0
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary {
			primary = instance
			primaryCount++
		}
	}
	if primaryCount != 1 || primary.Health.State != model.HealthHealthy {
		return exceptions
	}
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RoleReplica && coordination.MySQLSemisyncIdleReplicaSafe(primary, instance) {
			exceptions[instance.ResourceID] = struct{}{}
		}
	}
	return exceptions
}

func powerAggregateHealthSafe(engine model.Engine, snapshot model.TopologySnapshot, exceptions map[model.ResourceID]struct{}) bool {
	if snapshot.Health.State == model.HealthHealthy {
		return true
	}
	if engine != model.EngineMySQL || snapshot.Health.State != model.HealthDegraded || len(exceptions) == 0 {
		return false
	}
	for _, instance := range snapshot.Instances {
		if instance.Health.State == model.HealthHealthy {
			continue
		}
		if _, allowed := exceptions[instance.ResourceID]; !allowed {
			return false
		}
	}
	return true
}

// powerTopologyChecks inspects the latest persisted topology and returns the
// blocking reasons that would prevent a safe shutdown.
func (server *Server) powerTopologyChecks(request *http.Request, clusterID model.ResourceID) []string {
	reasons := make([]string, 0)
	engine := model.Engine("")
	if cluster, found := server.store.Cluster(clusterID); found {
		engine = cluster.Engine
		switch cluster.Engine {
		case model.EngineMySQL, model.EnginePostgreSQL:
		default:
			reasons = append(reasons, "automatic boot recovery is not qualified for engine "+string(cluster.Engine))
		}
	}
	powerAdapter, found := server.registry.Get(adapter.WildcardEngine)
	if !found || !powerAdapter.Capabilities(request.Context()).Supports(adapter.CapabilityExecute) {
		reasons = append(reasons, "signed Agent transport is unavailable; real shutdown and restart recovery cannot be verified")
	}
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return append(reasons, "cluster has no persisted topology observation; run discover first")
	}
	exceptions := powerSemisyncIdleReplicaExceptions(engine, snapshot)
	if !powerAggregateHealthSafe(engine, snapshot, exceptions) {
		reasons = append(reasons, "cluster health is "+string(snapshot.Health.State))
	}
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary && instance.Health.State != model.HealthHealthy {
			reasons = append(reasons, "primary "+instance.DisplayName+" is "+string(instance.Health.State))
		}
		if instance.Role == model.RoleReplica && instance.Health.State != model.HealthHealthy {
			if _, allowed := exceptions[instance.ResourceID]; !allowed {
				reasons = append(reasons, "replica "+instance.DisplayName+" is "+string(instance.Health.State))
			}
		}
	}
	for _, link := range snapshot.Links {
		if !link.Healthy {
			if _, allowed := exceptions[link.TargetInstanceID]; !allowed {
				reasons = append(reasons, "replication link to "+string(link.TargetInstanceID)+" is unhealthy")
			}
		}
		if link.LagSeconds != nil && *link.LagSeconds > maximumPowerReplicaLagSeconds {
			reasons = append(reasons, "replication to "+string(link.TargetInstanceID)+" lags "+formatLagSeconds(*link.LagSeconds)+"; limit is 60s")
		}
	}
	for _, lock := range server.store.CoordinationOperationLocks() {
		if lock.ClusterID == clusterID && lock.ExpiresAt.After(time.Now().UTC()) {
			reasons = append(reasons, "an operation lock is active on this cluster")
		}
	}
	return reasons
}

type powerCoResidentCluster struct {
	ClusterID        model.ResourceID   `json:"cluster_id"`
	DisplayName      string             `json:"display_name"`
	SharedNodeIDs    []model.ResourceID `json:"shared_node_ids,omitempty"`
	PowerState       model.PowerState   `json:"power_state,omitempty"`
	ReadyForPowerOff bool               `json:"ready_for_poweroff"`
}

// powerInstanceHostKeys resolves immutable node identity first, with endpoint
// coordinates as a compatibility fallback for inventories created before
// node_id was populated.
func powerInstanceHostKeys(instance model.DatabaseInstance) []string {
	keys := make([]string, 0, 3)
	if model.ValidResourceID(instance.NodeID) {
		keys = append(keys, "node:"+string(instance.NodeID))
	}
	if address := strings.TrimSpace(instance.IPAddress); address != "" {
		keys = append(keys, "ip:"+strings.ToLower(address))
	}
	if hostname := strings.TrimSpace(instance.Hostname); hostname != "" {
		keys = append(keys, "host:"+strings.ToLower(hostname))
	}
	return keys
}

// powerCoResidentClusters finds every managed cluster that shares a physical
// or virtual node with the selected cluster. Host power-off affects all of
// them, regardless of which database cluster initiated the request.
func (server *Server) powerCoResidentClusters(request *http.Request, clusterID model.ResourceID) []powerCoResidentCluster {
	selected, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return nil
	}
	selectedHosts := make(map[string]struct{})
	selectedNodeIDs := make(map[model.ResourceID]struct{})
	for _, instance := range selected.Instances {
		for _, key := range powerInstanceHostKeys(instance) {
			selectedHosts[key] = struct{}{}
		}
		if model.ValidResourceID(instance.NodeID) {
			selectedNodeIDs[instance.NodeID] = struct{}{}
		}
	}

	result := make([]powerCoResidentCluster, 0)
	for _, cluster := range server.store.Clusters() {
		if cluster.ResourceID == clusterID {
			continue
		}
		topology, topologyFound := server.store.TopologySnapshot(cluster.ResourceID)
		if !topologyFound {
			continue
		}
		shared := make(map[model.ResourceID]struct{})
		overlaps := false
		for _, instance := range topology.Instances {
			for _, key := range powerInstanceHostKeys(instance) {
				if _, keyFound := selectedHosts[key]; keyFound {
					overlaps = true
					break
				}
			}
			if _, nodeFound := selectedNodeIDs[instance.NodeID]; nodeFound && model.ValidResourceID(instance.NodeID) {
				shared[instance.NodeID] = struct{}{}
			}
		}
		if !overlaps {
			continue
		}
		entry := powerCoResidentCluster{ClusterID: cluster.ResourceID, DisplayName: cluster.DisplayName}
		for nodeID := range shared {
			entry.SharedNodeIDs = append(entry.SharedNodeIDs, nodeID)
		}
		sort.Slice(entry.SharedNodeIDs, func(i, j int) bool { return entry.SharedNodeIDs[i] < entry.SharedNodeIDs[j] })
		if operation, active := server.store.ActivePowerOperation(request.Context(), cluster.ResourceID); active {
			entry.PowerState = operation.State
			entry.ReadyForPowerOff = operation.State == model.PowerPoweredOff
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DisplayName != result[j].DisplayName {
			return result[i].DisplayName < result[j].DisplayName
		}
		return result[i].ClusterID < result[j].ClusterID
	})
	return result
}

func (server *Server) powerShutdownChecks(request *http.Request, clusterID model.ResourceID, mode model.PowerOperationType) ([]string, []powerCoResidentCluster) {
	reasons := server.powerTopologyChecks(request, clusterID)
	coResident := server.powerCoResidentClusters(request, clusterID)
	if mode != model.PowerPowerOff {
		return reasons, coResident
	}
	for _, cluster := range coResident {
		if cluster.ReadyForPowerOff {
			continue
		}
		name := strings.TrimSpace(cluster.DisplayName)
		if name == "" {
			name = string(cluster.ClusterID)
		}
		reasons = append(reasons, "host poweroff affects co-resident cluster "+name+"; complete its planned service shutdown first")
	}
	return reasons, coResident
}

// powerRecoverySnapshotChecks requires the exact role assignment captured
// before shutdown to be restored before protections are released. Aggregate
// topology health alone is insufficient: another healthy node could become
// primary while the designated primary is still starting, which would let a
// late restore process observe COMPLETED and leave that node fenced forever.
func (server *Server) powerRecoverySnapshotChecks(clusterID model.ResourceID, operation model.PowerOperation) []string {
	if operation.Snapshot == nil {
		return []string{"power operation has no pre-shutdown topology snapshot"}
	}
	if operation.Snapshot.Primary.InstanceID == "" {
		return []string{"power operation snapshot has no designated primary"}
	}
	topology, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return []string{"cluster has no persisted topology observation after recovery"}
	}
	instances := make(map[model.ResourceID]model.DatabaseInstance, len(topology.Instances))
	for _, instance := range topology.Instances {
		instances[instance.ResourceID] = instance
	}
	reasons := make([]string, 0)
	engine := operation.Snapshot.Engine
	if engine == "" {
		if cluster, clusterFound := server.store.Cluster(clusterID); clusterFound {
			engine = cluster.Engine
		}
	}
	exceptions := powerSemisyncIdleReplicaExceptions(engine, topology)
	primaryRef := operation.Snapshot.Primary
	primary, found := instances[primaryRef.InstanceID]
	if !found {
		reasons = append(reasons, "snapshot primary "+string(primaryRef.InstanceID)+" is missing after recovery")
	} else {
		if primary.Role != model.RolePrimary {
			reasons = append(reasons, "snapshot primary "+string(primaryRef.InstanceID)+" recovered as "+string(primary.Role)+"; expected primary")
		}
		if primary.Health.State != model.HealthHealthy {
			reasons = append(reasons, "snapshot primary "+string(primaryRef.InstanceID)+" is "+string(primary.Health.State))
		}
	}
	for _, replicaRef := range operation.Snapshot.Replicas {
		replica, exists := instances[replicaRef.InstanceID]
		if !exists {
			reasons = append(reasons, "snapshot replica "+string(replicaRef.InstanceID)+" is missing after recovery")
			continue
		}
		if replica.Role != model.RoleReplica && replica.Role != model.RoleStandby {
			reasons = append(reasons, "snapshot replica "+string(replicaRef.InstanceID)+" recovered as "+string(replica.Role)+"; expected replica")
		}
		if replica.Health.State != model.HealthHealthy {
			if _, allowed := exceptions[replica.ResourceID]; !allowed {
				reasons = append(reasons, "snapshot replica "+string(replicaRef.InstanceID)+" is "+string(replica.Health.State))
			}
		}
	}
	return reasons
}

func formatLagSeconds(lag int64) string {
	return fmt.Sprintf("%ds", lag)
}

// powerSnapshotFromTopology captures the pre-shutdown cluster coordinates so
// restore and verification can detect drift after boot.
func (server *Server) powerSnapshotFromTopology(request *http.Request, clusterID model.ResourceID) (*model.PowerSnapshot, bool) {
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		return nil, false
	}
	snapshot, found := server.store.TopologySnapshot(clusterID)
	if !found {
		return nil, false
	}
	captured := &model.PowerSnapshot{
		ClusterID:   clusterID,
		ClusterName: cluster.DisplayName,
		Engine:      cluster.Engine,
		CapturedAt:  time.Now().UTC(),
	}
	for _, instance := range snapshot.Instances {
		ref := model.PowerInstanceRef{
			InstanceID: instance.ResourceID, Hostname: instance.Hostname,
			IPAddress: instance.IPAddress, Port: instance.Port,
		}
		if instance.Role == model.RolePrimary {
			captured.Primary = ref
		} else {
			captured.Replicas = append(captured.Replicas, ref)
		}
	}
	endpointsByID := make(map[model.ResourceID]model.Endpoint, 0)
	for _, endpoint := range server.store.Endpoints(clusterID) {
		endpointsByID[endpoint.ResourceID] = endpoint
	}
	for _, haEndpoint := range server.store.HAEndpoints(clusterID) {
		if haEndpoint.Kind != model.EndpointVIP {
			continue
		}
		address := ""
		if endpoint, found := endpointsByID[haEndpoint.EndpointID]; found {
			address = endpoint.IPAddress
		}
		captured.VIP = &model.PowerVIPRef{
			EndpointID: haEndpoint.EndpointID, IPAddress: address, Interface: haEndpoint.Interface,
		}
	}
	return captured, true
}

type powerPrecheckPayload struct {
	Mode        model.PowerOperationType `json:"mode"`
	RequestedBy string                   `json:"requested_by,omitempty"`
}

// powerPrecheck creates the power lifecycle operation in PRECHECKING and
// reports the topology conditions that would block a shutdown.
func (server *Server) powerPrecheck(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := powerPrecheckPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid power precheck payload")
		return
	}
	if !payload.Mode.Valid() {
		writeError(writer, http.StatusBadRequest, "power mode must be service or poweroff")
		return
	}
	if _, found := server.store.Cluster(clusterID); !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		created, err := server.store.CreatePowerOperation(request.Context(), model.PowerOperation{
			ClusterID:     clusterID,
			OperationType: payload.Mode,
			Mode:          string(payload.Mode),
			RequestedBy:   powerRequester(request, payload.RequestedBy),
			AutoRecovery:  true,
		})
		if err != nil {
			server.writePowerCreateError(writer, err)
			return
		}
		// The store creates the operation in normal; every state change goes
		// through the transition validator, so walk into prechecking.
		operation, err = server.store.TransitionPowerOperation(request.Context(), created.ResourceID,
			created.MetadataRevision, model.PowerPrechecking, "", "", nil)
		if err != nil {
			server.writePowerCreateError(writer, err)
			return
		}
	}
	// A cancelled operation rests in normal; re-running precheck restarts the
	// lifecycle instead of requiring a fresh plan.
	if operation.State == model.PowerNormal {
		restarted, restartErr := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
			operation.MetadataRevision, model.PowerPrechecking, "", "", nil)
		if restartErr != nil {
			server.writePowerCreateError(writer, restartErr)
			return
		}
		operation = restarted
	}
	if operation.State != model.PowerPrechecking {
		writeError(writer, http.StatusConflict, "active power operation is in state "+string(operation.State)+"; precheck requires prechecking")
		return
	}
	reasons, coResident := server.powerShutdownChecks(request, clusterID, payload.Mode)
	// Planning-time visibility: protections left over from a previous
	// lifecycle stay active until the new lifecycle completes.
	if frozen, _ := server.store.RecoveryFrozen(request.Context(), clusterID); frozen {
		reasons = append(reasons, "cluster recovery is frozen by a previous lifecycle; protections stay active until the lifecycle completes")
	}
	risk := "low"
	if len(reasons) > 0 {
		risk = "high"
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"power_operation": operation, "blocking_reasons": reasons, "risk": risk,
		"co_resident_clusters": coResident,
	}})
}

func (server *Server) writePowerCreateError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "cluster already has an active power operation")
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, "invalid power operation request")
	case errors.Is(err, store.ErrNotFound):
		writeError(writer, http.StatusNotFound, "cluster not found")
	default:
		writeError(writer, http.StatusInternalServerError, "power precheck failed")
	}
}

type powerPlanPayload struct {
	Mode        model.PowerOperationType `json:"mode"`
	RequestedBy string                   `json:"requested_by,omitempty"`
}

// powerPlan walks the operation from PRECHECKING through MAINTENANCE to
// SHUTDOWN_PLANNED and captures the topology snapshot used by restore.
func (server *Server) powerPlan(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := powerPlanPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid power plan payload")
		return
	}
	if !payload.Mode.Valid() {
		writeError(writer, http.StatusBadRequest, "power mode must be service or poweroff")
		return
	}
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "no active power operation; run power/precheck first")
		return
	}
	if operation.State != model.PowerPrechecking {
		writeError(writer, http.StatusConflict, "power plan requires prechecking state, currently "+string(operation.State))
		return
	}
	reasons, coResident := server.powerShutdownChecks(request, clusterID, payload.Mode)
	if frozen, _ := server.store.RecoveryFrozen(request.Context(), clusterID); frozen {
		reasons = append(reasons, "cluster recovery is frozen by a previous lifecycle")
	}
	if len(reasons) > 0 {
		writeError(writer, http.StatusConflict, "power plan is blocked: "+strings.Join(reasons, "; "))
		return
	}
	captured, ok := server.powerSnapshotFromTopology(request, clusterID)
	if !ok {
		writeError(writer, http.StatusConflict, "no topology observation to plan against; run discover first")
		return
	}
	maintenance, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerMaintenance, powerRequester(request, payload.RequestedBy),
		"maintenance protection armed for shutdown", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	planned, err := server.store.TransitionPowerOperation(request.Context(), maintenance.ResourceID,
		maintenance.MetadataRevision, model.PowerShutdownPlanned, powerRequester(request, payload.RequestedBy),
		"shutdown planned; snapshot captured", captured)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	cluster, _ := server.store.Cluster(clusterID)
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"power_operation": planned, "snapshot": planned.Snapshot, "plan_steps": powerShutdownPlanSteps(cluster.Engine),
		"co_resident_clusters": coResident,
	}})
}

func (server *Server) writePowerTransitionError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrValidation):
		writeError(writer, http.StatusBadRequest, "invalid power state transition")
	case errors.Is(err, store.ErrNotFound):
		writeError(writer, http.StatusNotFound, "power operation not found")
	default:
		writeError(writer, http.StatusInternalServerError, "power transition failed")
	}
}

type powerExecutePayload struct {
	ApprovalToken string `json:"approval_token"`
}

// powerExecute runs the shutdown through the standard workflow: the approval
// token gates execution, the PowerShutdownAdapter applies the fail-closed
// protections (recovery freeze + maintenance) and advances the power state
// machine to POWER_OFF.
func (server *Server) powerExecute(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := powerExecutePayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid power execute payload")
		return
	}
	payload.ApprovalToken = strings.TrimSpace(payload.ApprovalToken)
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "no active power operation; run power/plan first")
		return
	}
	if operation.State != model.PowerShutdownPlanned && operation.State != model.PowerShuttingDown {
		writeError(writer, http.StatusConflict, "power execute requires shutdown_planned or shutting_down state, currently "+string(operation.State))
		return
	}
	if operation.Snapshot == nil {
		writeError(writer, http.StatusConflict, "power operation has no snapshot; run power/plan first")
		return
	}
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	targetID := operation.Snapshot.Primary.InstanceID
	if !model.ValidResourceID(targetID) {
		writeError(writer, http.StatusConflict, "power snapshot has no primary; re-run power/plan")
		return
	}
	requestOperation := model.Operation{
		ClusterID: clusterID, Engine: cluster.Engine,
		Kind: model.OperationPowerShutdown, RequestedBy: operation.RequestedBy,
	}
	authentication, platformSession := requestAuthentication(request)
	var record model.OperationRecord
	var err error
	if platformSession && authentication.viaSession {
		requestOperation.RequestedBy = authentication.principal.Username
		record, payload.ApprovalToken, err = server.issuePlatformSessionOperationApproval(request.Context(), authentication, adapter.OperationRequest{
			Operation: requestOperation, TargetID: targetID,
			IdempotencyKey: server.powerShutdownExecutionKey(operation),
		})
		if err != nil {
			server.writeOperationActionError(writer, err, record)
			return
		}
	} else {
		if payload.ApprovalToken == "" {
			writeError(writer, http.StatusUnauthorized, "approval_token is required")
			return
		}
		record, err = server.approvedOperation(request.Context(), payload.ApprovalToken, requestOperation, targetID)
		if err != nil {
			if errors.Is(err, approval.ErrMismatch) {
				writeError(writer, http.StatusUnauthorized,
					"approval token does not match this cluster and target "+string(targetID)+"; issue the approval against that target")
				return
			}
			server.writeApprovalError(writer, err)
			return
		}
	}
	execution, err := server.workflow.Execute(request.Context(), adapter.OperationRequest{
		Operation: record.Operation, TargetID: record.TargetID, IdempotencyKey: record.IdempotencyKey,
	}, payload.ApprovalToken)
	current, _ := server.store.Operation(record.ResourceID)
	result := map[string]interface{}{"operation_record": publicOperationRecord(current)}
	if operation, found := server.store.ActivePowerOperation(request.Context(), clusterID); found {
		result["power_operation"] = operation
	}
	frozen, _ := server.store.RecoveryFrozen(request.Context(), clusterID)
	maintenance := make([]model.ResourceID, 0)
	for _, instance := range server.store.Instances(clusterID) {
		if instance.Maintenance {
			maintenance = append(maintenance, instance.ResourceID)
		}
	}
	result["protection"] = map[string]interface{}{
		"recovery_freeze": frozen, "instances_in_maintenance": maintenance,
	}
	classified := classifyOperationExecution(err, execution, current)
	response := map[string]interface{}{"status": classified.status, "result": result}
	if classified.message != "" {
		response["message"] = classified.message
	}
	writeJSON(writer, classified.code, response)
}

// powerShutdownExecutionKey gives every protected retry a fresh durable
// workflow record while keeping concurrent duplicate requests idempotent.
// The power operation itself remains the stable lifecycle identity.
func (server *Server) powerShutdownExecutionKey(operation model.PowerOperation) string {
	base := "power-shutdown:" + string(operation.ResourceID)
	attempts := 0
	for _, record := range server.store.Operations(operation.ClusterID) {
		if record.Operation.Kind == model.OperationPowerShutdown && strings.HasPrefix(record.IdempotencyKey, base) {
			attempts++
		}
	}
	if attempts == 0 {
		return base
	}
	return fmt.Sprintf("%s:retry:%d", base, attempts)
}

type powerCancelPayload struct {
	RequestedBy string `json:"requested_by,omitempty"`
}

// powerCancel aborts a shutdown before it executes: PRECHECKING, MAINTENANCE,
// and SHUTDOWN_PLANNED all return to NORMAL without touching protections.
func (server *Server) powerCancel(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := powerCancelPayload{}
	if err := decode(request, &payload); err != nil && err != io.EOF {
		writeError(writer, http.StatusBadRequest, "invalid power cancel payload")
		return
	}
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "no active power operation")
		return
	}
	switch operation.State {
	case model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned:
	default:
		writeError(writer, http.StatusConflict, "power operation in state "+string(operation.State)+" cannot be cancelled")
		return
	}
	cancelled, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerNormal, powerRequester(request, payload.RequestedBy),
		"shutdown cancelled by operator", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"power_operation": cancelled}})
}

// powerBootDetected marks the cluster reachable again after power-off.
func (server *Server) powerBootDetected(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	operation, found := server.activePowerOperationOrError(writer, request, clusterID)
	if !found {
		return
	}
	if operation.State != model.PowerPoweredOff {
		writeError(writer, http.StatusConflict, "power boot-detected requires power_off state, currently "+string(operation.State))
		return
	}
	next, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerBootDetected, "", "server boot detected", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"power_operation": next}})
}

// powerRecovering marks the automatic recovery workflow underway.
func (server *Server) powerRecovering(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	operation, found := server.activePowerOperationOrError(writer, request, clusterID)
	if !found {
		return
	}
	if operation.State != model.PowerBootDetected {
		writeError(writer, http.StatusConflict, "power recovering requires boot_detected state, currently "+string(operation.State))
		return
	}
	next, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerRecovering, "", "automatic recovery started", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"power_operation": next}})
}

// powerVerify transitions into VERIFYING and evaluates the recovered topology.
func (server *Server) powerVerify(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	operation, found := server.activePowerOperationOrError(writer, request, clusterID)
	if !found {
		return
	}
	if operation.State != model.PowerRecovering {
		writeError(writer, http.StatusConflict, "power verify requires recovering state, currently "+string(operation.State))
		return
	}
	next, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerVerifying, "", "recovery verification started", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	reasons := append(server.powerTopologyChecks(request, clusterID), server.powerRecoverySnapshotChecks(clusterID, operation)...)
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"power_operation": next, "blocking_reasons": reasons, "healthy": len(reasons) == 0,
	}})
}

// powerComplete releases the fail-closed protections and moves the operation
// to COMPLETED — but only when the recovered topology verifies healthy.
func (server *Server) powerComplete(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	operation, found := server.activePowerOperationOrError(writer, request, clusterID)
	if !found {
		return
	}
	if operation.State != model.PowerVerifying {
		writeError(writer, http.StatusConflict, "power complete requires verifying state, currently "+string(operation.State))
		return
	}
	verifiedTopology, _ := server.store.TopologySnapshot(clusterID)
	reasons := append(server.powerTopologyChecks(request, clusterID), server.powerRecoverySnapshotChecks(clusterID, operation)...)
	if len(reasons) > 0 {
		writeError(writer, http.StatusConflict, "recovery verification failed; protections stay active: "+strings.Join(reasons, "; "))
		return
	}
	next, err := server.store.CompletePowerRecovery(request.Context(), operation.ResourceID,
		operation.MetadataRevision, verifiedTopology.ObservedAt)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"power_operation": next}})
}

type powerFailPayload struct {
	Reason      string `json:"reason"`
	RequestedBy string `json:"requested_by,omitempty"`
}

type powerOutageClassification struct {
	Kind                        string     `json:"kind"`
	OperatorInitiated           bool       `json:"operator_initiated"`
	DatabaseState               string     `json:"database_state"`
	AutomaticFailoverSuppressed bool       `json:"automatic_failover_suppressed"`
	ControlPlane                string     `json:"control_plane"`
	RequestedBy                 string     `json:"requested_by,omitempty"`
	RequestedAt                 *time.Time `json:"requested_at,omitempty"`
	Reason                      string     `json:"reason"`
}

func classifyPowerOutage(cluster model.DatabaseCluster, topology model.TopologySnapshot, hasTopology bool, operation *model.PowerOperation, protected, recoveryFrozen bool) powerOutageClassification {
	if recovery := cluster.Recovery; recovery != nil && recovery.LastRecoveryStatus == "succeeded" && recovery.IncidentRecovered && !recoveryFrozen && !protected && operation != nil && recovery.RecoveredAt.After(operation.UpdatedAt) {
		operation = nil
	}
	classification := powerOutageClassification{
		Kind:                        "normal",
		DatabaseState:               "running",
		AutomaticFailoverSuppressed: protected || recoveryFrozen,
		ControlPlane:                "online_independent",
		Reason:                      "database topology is healthy and no planned shutdown is active",
	}
	if operation != nil && operation.State != model.PowerNormal && operation.State != model.PowerCompleted {
		startedAt := operation.StartedAt
		classification.Kind = "planned_shutdown"
		classification.OperatorInitiated = true
		classification.RequestedBy = operation.RequestedBy
		classification.RequestedAt = &startedAt
		classification.Reason = "operator-initiated power lifecycle is active"
		switch operation.State {
		case model.PowerPrechecking, model.PowerMaintenance, model.PowerShutdownPlanned:
			classification.DatabaseState = "running"
		case model.PowerShuttingDown:
			classification.DatabaseState = "stopping"
		case model.PowerPoweredOff:
			classification.DatabaseState = "stopped"
		case model.PowerBootDetected, model.PowerRecovering, model.PowerVerifying:
			classification.DatabaseState = "recovering"
		case model.PowerFailed:
			classification.Kind = "planned_shutdown_failed"
			classification.DatabaseState = "unknown"
			classification.Reason = "operator-initiated power lifecycle failed and protections remain active"
		}
		return classification
	}
	if protected || recoveryFrozen {
		classification.Kind = "planned_shutdown_failed"
		classification.OperatorInitiated = true
		classification.DatabaseState = "unknown"
		classification.Reason = "planned-shutdown protection is active without a recoverable lifecycle"
		return classification
	}
	if !hasTopology {
		classification.Kind = "unknown"
		classification.DatabaseState = "unknown"
		classification.Reason = "no topology observation is available"
		return classification
	}
	primaryCount := 0
	healthyPrimary := false
	instanceFailure := false
	exceptions := powerSemisyncIdleReplicaExceptions(cluster.Engine, topology)
	for _, instance := range topology.Instances {
		if instance.Role == model.RolePrimary {
			primaryCount++
			if instance.Health.State == model.HealthHealthy {
				healthyPrimary = true
			}
		}
		if instance.Health.State != model.HealthHealthy {
			if _, allowed := exceptions[instance.ResourceID]; !allowed {
				instanceFailure = true
			}
		}
	}
	topologyHealthSafe := powerAggregateHealthSafe(cluster.Engine, topology, exceptions)
	clusterHealthSafe := cluster.Health.State == model.HealthHealthy || (cluster.Health.State == model.HealthDegraded && topologyHealthSafe)
	if !clusterHealthSafe || !topologyHealthSafe || primaryCount != 1 || !healthyPrimary || instanceFailure {
		classification.Kind = "unexpected_failure"
		classification.DatabaseState = "failed"
		classification.Reason = "database health degraded without an operator-initiated shutdown lifecycle"
	}
	return classification
}

// powerFail marks the operation FAILED. Fail-closed: every protection stays
// active and recovery must be re-established by an operator.
func (server *Server) powerFail(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := powerFailPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid power fail payload")
		return
	}
	operation, found := server.store.ActivePowerOperation(request.Context(), clusterID)
	if !found {
		writeError(writer, http.StatusConflict, "no active power operation")
		return
	}
	message := strings.TrimSpace(payload.Reason)
	if message == "" {
		message = "power lifecycle failed"
	}
	next, err := server.store.TransitionPowerOperation(request.Context(), operation.ResourceID,
		operation.MetadataRevision, model.PowerFailed, powerRequester(request, payload.RequestedBy),
		message+"; protections stay active until an operator intervenes", nil)
	if err != nil {
		server.writePowerTransitionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"power_operation": next, "manual_recovery_required": true,
	}})
}

// powerStatus reports the active (or most recent) power operation, the cluster
// protection state, and the persisted topology.
func (server *Server) powerStatus(writer http.ResponseWriter, request *http.Request, clusterID model.ResourceID) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cluster, found := server.store.Cluster(clusterID)
	if !found {
		writeError(writer, http.StatusNotFound, "cluster not found")
		return
	}
	operations := server.store.PowerOperationsByCluster(clusterID)
	var latest *model.PowerOperation
	if len(operations) > 0 {
		latest = &operations[0]
	}
	topology, hasTopology := server.store.TopologySnapshot(clusterID)
	protected, protectedState := server.store.IsClusterPowerProtected(clusterID)
	recoveryFrozen := mustBool(server.store.RecoveryFrozen(request.Context(), clusterID))
	classification := classifyPowerOutage(cluster, topology, hasTopology, latest, protected, recoveryFrozen)
	maintenanceInstances := make([]model.ResourceID, 0)
	for _, instance := range server.store.Instances(clusterID) {
		if instance.Maintenance {
			maintenanceInstances = append(maintenanceInstances, instance.ResourceID)
		}
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
		"cluster":                  cluster,
		"power_operation":          latest,
		"outage_classification":    classification,
		"protected":                protected,
		"protected_state":          protectedState,
		"recovery_freeze":          recoveryFrozen,
		"instances_in_maintenance": maintenanceInstances,
		"topology":                 topology,
		"has_topology":             hasTopology,
		"operation_history":        operations,
	}})
}

func mustBool(value bool, _ error) bool {
	return value
}
