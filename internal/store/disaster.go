package store

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

func cloneRecoveryTask(task model.RecoveryTask) model.RecoveryTask {
	copy := task
	copy.Members = make([]model.DatabaseInstance, len(task.Members))
	for i, m := range task.Members {
		copy.Members[i] = cloneInstance(m)
	}
	copy.Evidence = append([]model.RecoveryEvidence{}, task.Evidence...)
	for i, e := range task.Evidence {
		copy.Evidence[i].History = append([]model.RecoveryTimeline{}, e.History...)
	}
	copy.Proofs = append([]model.RecoveryProof{}, task.Proofs...)
	copy.Events = append([]model.RecoveryEvent{}, task.Events...)
	return copy
}

func cloneRecoveryTasks(tasks map[model.ResourceID]model.RecoveryTask) map[model.ResourceID]model.RecoveryTask {
	copy := make(map[model.ResourceID]model.RecoveryTask, len(tasks))
	for id, task := range tasks {
		copy[id] = cloneRecoveryTask(task)
	}
	return copy
}

func (r *Repository) RecoveryTask(id model.ResourceID) (model.RecoveryTask, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	task, found := r.snapshot.RecoveryTasks[id]
	return cloneRecoveryTask(task), found
}

func (r *Repository) RecoveryTasks(clusterID model.ResourceID) []model.RecoveryTask {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var tasks []model.RecoveryTask
	for _, task := range r.snapshot.RecoveryTasks {
		if task.ClusterID == clusterID {
			tasks = append(tasks, cloneRecoveryTask(task))
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return tasks
}

// Only the majority-authorized reconciler calls this method. Keep the freeze;
// an expired executor must never turn into a successful recovery by timeout.
func (r *Repository) BlockAbandonedRecoveries(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	next := cloneDiscoverySnapshot(r.snapshot)
	changed := false
	for id, task := range r.snapshot.RecoveryTasks {
		if task.Stage == model.RecoveryPlanned || task.Stage == model.RecoveryBlocked || task.Stage == model.RecoverySucceeded {
			continue
		}
		if recoveryLeaseValid(r.snapshot, task, now.Add(-15*time.Second)) {
			continue
		}
		// A missing lease can also be an explicitly released failed executor.
		// Let all short Agent permits expire before exposing a retry.
		if now.Sub(task.UpdatedAt) < 15*time.Second {
			continue
		}
		task.Stage = model.RecoveryBlocked
		task.MetadataRevision++
		task.UpdatedAt = now
		task.Message = "recovery executor lease expired; member fencing remains required before retry"
		task.Events = append(task.Events, model.RecoveryEvent{At: now, Stage: task.Stage, Message: task.Message})
		next.RecoveryTasks[id] = cloneRecoveryTask(task)
		cluster := next.Clusters[task.ClusterID]
		if cluster.Recovery != nil && cluster.Recovery.TaskID == id {
			cluster.RecoveryFreeze = true
			cluster.Recovery.LastRecoveryStatus = "blocked"
			cluster.Recovery.ActualState = "unknown"
			cluster.Recovery.IncidentActive = true
			cluster.Recovery.IncidentRecovered = false
			cluster.MetadataRevision++
			cluster.UpdatedAt = now
			next.Clusters[task.ClusterID] = cluster
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return r.commitSnapshotLocked(next)
}

// RecordRecoveryStartFailure records failures before BeginRecovery without
// pretending that fencing or any database mutation has already happened.
func (r *Repository) RecordRecoveryStartFailure(ctx context.Context, id model.ResourceID, revision uint64, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	task, found := r.snapshot.RecoveryTasks[id]
	if !found || task.MetadataRevision != revision || (task.Stage != model.RecoveryPlanned && task.Stage != model.RecoveryBlocked) {
		return nil
	}
	now := r.now().UTC()
	task.MetadataRevision++
	task.UpdatedAt = now
	task.Stage = model.RecoveryBlocked
	task.Message = redact.Bounded(message, 2048)
	task.Events = append(task.Events, model.RecoveryEvent{At: now, Stage: task.Stage, Message: task.Message})
	next := r.snapshot
	next.RecoveryTasks = cloneRecoveryTasks(next.RecoveryTasks)
	next.RecoveryTasks[id] = task
	return r.commitSnapshotLocked(next)
}

func (r *Repository) PlanRecovery(ctx context.Context, clusterID model.ResourceID, actor string) (model.RecoveryTask, error) {
	if err := ctx.Err(); err != nil {
		return model.RecoveryTask{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return model.RecoveryTask{}, validationError("recovery actor is required")
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	cluster, found := r.snapshot.Clusters[clusterID]
	if !found {
		return model.RecoveryTask{}, notFoundError("unknown cluster")
	}
	if cluster.Engine != model.EngineMySQL && cluster.Engine != model.EnginePostgreSQL {
		return model.RecoveryTask{}, validationError("disaster recovery supports MySQL and PostgreSQL")
	}
	for _, task := range r.snapshot.RecoveryTasks {
		if task.ClusterID == clusterID && task.Stage != model.RecoverySucceeded {
			return cloneRecoveryTask(task), nil
		}
	}
	for _, op := range r.snapshot.PowerOperations {
		if op.ClusterID == clusterID && !model.TerminalPowerState(op.State) {
			return model.RecoveryTask{}, conflictError("a planned power lifecycle is active")
		}
	}
	var members []model.DatabaseInstance
	for _, instance := range r.snapshot.Instances {
		if instance.ClusterID == clusterID {
			members = append(members, cloneInstance(instance))
		}
	}
	if len(members) < 2 {
		return model.RecoveryTask{}, validationError("recovery requires all registered database members")
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ResourceID < members[j].ResourceID })
	now := r.now().UTC()
	task := model.RecoveryTask{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}, ClusterID: clusterID, Engine: cluster.Engine, RequestedBy: actor, Stage: model.RecoveryPlanned, InventoryGeneration: r.snapshot.InventoryGenerations[clusterID], Members: members, Events: []model.RecoveryEvent{}, Message: "all members will be fenced before authoritative history selection"}
	task.PlanDigest = disaster.Digest(struct {
		ID, ClusterID model.ResourceID
		Generation    uint64
		Members       []model.DatabaseInstance
	}{task.ResourceID, clusterID, task.InventoryGeneration, members})
	next := r.snapshot
	next.RecoveryTasks = cloneRecoveryTasks(next.RecoveryTasks)
	next.RecoveryTasks[task.ResourceID] = task
	if err := r.commitSnapshotLocked(next); err != nil {
		return model.RecoveryTask{}, err
	}
	return cloneRecoveryTask(task), nil
}

func recoveryLeaseValid(s snapshot, task model.RecoveryTask, now time.Time) bool {
	lock, found := s.OperationLocks[task.LeaseID]
	return found && lock.ClusterID == task.ClusterID && lock.OperationID == task.ResourceID && lock.ExpiresAt.After(now)
}

// RecoveryAuthorization never derives a writer from bootstrap configuration or
// discovery. The selected member and the operation lease must share one durable
// inventory snapshot; losing any part of that proof revokes the permit.
func (r *Repository) RecoveryAuthorization(clusterID, instanceID model.ResourceID, now time.Time) (model.RecoveryTask, time.Time, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cluster, found := r.snapshot.Clusters[clusterID]
	if !found || !cluster.DisasterRecoveryActive() {
		return model.RecoveryTask{}, time.Time{}, false
	}
	task, found := r.snapshot.RecoveryTasks[cluster.Recovery.TaskID]
	if !found || !recoveryLeaseValid(r.snapshot, task, now) || !recoveryInventoryValid(r.snapshot, task) {
		return model.RecoveryTask{}, time.Time{}, false
	}
	if task.PrimaryID != instanceID {
		if task.Stage == model.RecoveryRebuilding || (task.Engine == model.EngineMySQL && (task.Stage == model.RecoveryFencing || task.Stage == model.RecoveryInspecting) && task.PrimaryID == "") {
			for _, member := range task.Members {
				if member.ResourceID == instanceID {
					return cloneRecoveryTask(task), r.snapshot.OperationLocks[task.LeaseID].ExpiresAt, true
				}
			}
		}
		return model.RecoveryTask{}, time.Time{}, false
	}
	switch task.Stage {
	case model.RecoveryStarting, model.RecoveryRebuilding, model.RecoveryVerifying, model.RecoveryCommitted:
		return cloneRecoveryTask(task), r.snapshot.OperationLocks[task.LeaseID].ExpiresAt, true
	default:
		return model.RecoveryTask{}, time.Time{}, false
	}
}

func recoveryWriterLeaseAllowed(s snapshot, record coordination.LeaseRecord, now time.Time) bool {
	lease := record.Lease
	cluster := s.Clusters[lease.ClusterID]
	if !cluster.DisasterRecoveryActive() || !lease.Active || !lease.ExpiresAt.After(now) {
		return true
	}
	task, found := s.RecoveryTasks[cluster.Recovery.TaskID]
	return found && task.Stage == model.RecoveryCommitted && !task.CommittedAt.IsZero() &&
		cluster.Recovery.LastRecoveryStatus == "committed" && task.PrimaryID == lease.OwnerID &&
		lease.OperationID == lease.HAEndpointID && recoveryLeaseValid(s, task, now)
}

func recoveryInventoryValid(s snapshot, task model.RecoveryTask) bool {
	if s.InventoryGenerations[task.ClusterID] != task.InventoryGeneration {
		return false
	}
	count := 0
	for _, current := range s.Instances {
		if current.ClusterID == task.ClusterID {
			count++
		}
	}
	if count != len(task.Members) {
		return false
	}
	for _, member := range task.Members {
		current, found := s.Instances[member.ResourceID]
		if !found || current.ClusterID != member.ClusterID || current.Engine != member.Engine || !reflect.DeepEqual(current.EngineIdentity, member.EngineIdentity) || current.NodeID != member.NodeID || current.Hostname != member.Hostname || current.IPAddress != member.IPAddress || current.Port != member.Port {
			return false
		}
	}
	return true
}

func (r *Repository) BeginRecovery(ctx context.Context, id model.ResourceID, revision uint64, leaseID model.ResourceID) (model.RecoveryTask, error) {
	if err := ctx.Err(); err != nil {
		return model.RecoveryTask{}, err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	task, found := r.snapshot.RecoveryTasks[id]
	if !found {
		return task, notFoundError("unknown recovery task")
	}
	if task.MetadataRevision != revision || task.Stage == model.RecoverySucceeded || (task.Stage != model.RecoveryPlanned && task.Stage != model.RecoveryBlocked && recoveryLeaseValid(r.snapshot, task, r.now())) {
		return task, conflictError("recovery task changed or already has an executor")
	}
	task.LeaseID = leaseID
	now := r.now().UTC()
	if !recoveryLeaseValid(r.snapshot, task, now) || !recoveryInventoryValid(r.snapshot, task) {
		return task, conflictError("recovery lease or immutable inventory changed")
	}
	if r.snapshot.SoftwareUpdateGate != nil {
		return task, conflictError("software upgrade maintenance is active")
	}
	for _, lock := range r.snapshot.OperationLocks {
		if lock.ClusterID == task.ClusterID && lock.ResourceID != leaseID && lock.ExpiresAt.After(now) {
			return task, conflictError("another cluster operation lock is active")
		}
	}
	for _, op := range r.snapshot.PowerOperations {
		if op.ClusterID == task.ClusterID && !model.TerminalPowerState(op.State) {
			return task, conflictError("a planned power lifecycle became active")
		}
	}
	for _, op := range r.snapshot.Operations {
		if op.Operation.ClusterID == task.ClusterID && (op.Status == model.OperationRunning || op.Status == model.OperationIndeterminate) {
			return task, conflictError("database operation is running or needs verification")
		}
	}
	task.Stage = model.RecoveryFencing
	task.AttemptStartedAt = now
	// A new executor re-fences and re-collects all evidence, even if the old
	// executor persisted a commit before losing activation acknowledgement.
	task.CommittedAt = time.Time{}
	task.VerifiedAt = time.Time{}
	task.PrimaryID = ""
	task.Evidence = nil
	task.Proofs = nil
	task.MetadataRevision++
	task.UpdatedAt = now
	task.Message = "recovery freeze acquired; every member must be fenced"
	task.Events = append(task.Events, model.RecoveryEvent{At: now, Stage: task.Stage, Message: task.Message})
	next := cloneDiscoverySnapshot(r.snapshot)
	next.RecoveryTasks[id] = cloneRecoveryTask(task)
	cluster := next.Clusters[task.ClusterID]
	cluster.RecoveryFreeze = true
	cluster.MetadataRevision++
	cluster.UpdatedAt = now
	cluster.Recovery = &model.RecoveryState{TaskID: id, ExpectedState: "running", ActualState: "recovering", IncidentActive: true, LastRecoveryStatus: "running", LastShutdownClassification: "unplanned"}
	next.Clusters[task.ClusterID] = cluster
	for id, instance := range next.Instances {
		if instance.ClusterID == task.ClusterID {
			instance.Maintenance = true
			instance.MetadataRevision++
			instance.UpdatedAt = now
			next.Instances[id] = instance
		}
	}
	// Revoke previous writer grants in the same commit as the freeze. An old
	// leader's bounded authorization must expire before the executor scans WAL.
	for id, lease := range next.CoordinationLeases {
		if lease.Lease.ClusterID == task.ClusterID {
			delete(next.CoordinationLeases, id)
		}
	}
	if err := r.commitSnapshotLocked(next); err != nil {
		return model.RecoveryTask{}, err
	}
	return cloneRecoveryTask(task), nil
}

func recoveryTransition(from, to model.RecoveryStage) bool {
	if to == model.RecoveryBlocked {
		return from != model.RecoverySucceeded && from != model.RecoveryPlanned
	}
	if from == to {
		return from != model.RecoverySucceeded && from != model.RecoveryPlanned
	}
	return (from == model.RecoveryFencing && to == model.RecoveryInspecting) || (from == model.RecoveryInspecting && to == model.RecoverySelecting) || (from == model.RecoverySelecting && to == model.RecoveryStarting) || (from == model.RecoveryStarting && to == model.RecoveryRebuilding) || (from == model.RecoveryRebuilding && to == model.RecoveryVerifying)
}

func (r *Repository) AdvanceRecovery(ctx context.Context, task model.RecoveryTask, event model.RecoveryEvent) (model.RecoveryTask, error) {
	if err := ctx.Err(); err != nil {
		return task, err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	current, found := r.snapshot.RecoveryTasks[task.ResourceID]
	if !found {
		return task, notFoundError("unknown recovery task")
	}
	if current.MetadataRevision != task.MetadataRevision || !recoveryTransition(current.Stage, event.Stage) || current.LeaseID != task.LeaseID || !recoveryLeaseValid(r.snapshot, current, r.now()) {
		return current, conflictError("recovery executor or stage changed")
	}
	if !recoveryInventoryValid(r.snapshot, current) {
		return current, conflictError("recovery inventory changed")
	}
	if event.Stage == model.RecoveryStarting {
		for _, evidence := range task.Evidence {
			if evidence.ObservedAt.Before(current.AttemptStartedAt) || evidence.ObservedAt.After(r.now()) || r.now().Sub(evidence.ObservedAt) > 5*time.Minute {
				return current, validationError("recovery evidence is stale or from another attempt")
			}
		}
		selected, err := disaster.Select(current.Members, task.Evidence, task.Proofs)
		if err != nil || selected != task.PrimaryID {
			return current, validationError("recovery primary lacks complete authoritative evidence")
		}
		current.PrimaryID = selected
		current.Evidence = task.Evidence
		current.Proofs = task.Proofs
	}
	now := r.now().UTC()
	event.At = now
	event.Message = redact.Bounded(event.Message, 2048)
	current.Stage = event.Stage
	current.Message = event.Message
	current.UpdatedAt = now
	current.MetadataRevision++
	current.Events = append(current.Events, event)
	next := cloneDiscoverySnapshot(r.snapshot)
	next.RecoveryTasks[current.ResourceID] = cloneRecoveryTask(current)
	cluster := next.Clusters[current.ClusterID]
	if cluster.Recovery != nil {
		cluster.Recovery.CurrentPrimaryID = current.PrimaryID
		if event.Stage == model.RecoveryBlocked {
			cluster.Recovery.LastRecoveryStatus = "blocked"
			cluster.Recovery.ActualState = "unknown"
			cluster.Recovery.IncidentActive = true
			cluster.Recovery.IncidentRecovered = false
		}
		next.Clusters[current.ClusterID] = cluster
	}
	if err := r.commitSnapshotLocked(next); err != nil {
		return current, err
	}
	return cloneRecoveryTask(current), nil
}

func verifyRecoveryTopology(task model.RecoveryTask, topology model.TopologySnapshot, now time.Time) error {
	if topology.ClusterID != task.ClusterID {
		return validationError("recovery topology cluster does not match the task")
	}
	if len(topology.Instances) != len(task.Members) || len(topology.Links) != len(task.Members)-1 {
		return validationError("recovery topology is incomplete: members=%d/%d links=%d/%d", len(topology.Instances), len(task.Members), len(topology.Links), len(task.Members)-1)
	}
	if topology.Health.State != model.HealthHealthy {
		return validationError("recovery topology is not healthy: state=%s", topology.Health.State)
	}
	if topology.ObservedAt.Before(task.UpdatedAt) || topology.ObservedAt.After(now) || now.Sub(topology.ObservedAt) > 30*time.Second {
		return validationError("recovery topology is not fresh: observed=%s required_after=%s now=%s", topology.ObservedAt.Format(time.RFC3339Nano), task.UpdatedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	}
	seen := map[model.ResourceID]bool{}
	var primary model.DatabaseInstance
	for _, instance := range topology.Instances {
		if seen[instance.ResourceID] || instance.Health.State != model.HealthHealthy {
			return validationError("duplicate or unhealthy recovery member")
		}
		if instance.ClusterID != task.ClusterID || instance.Engine != task.Engine {
			return validationError("recovery topology has a different cluster or engine")
		}
		seen[instance.ResourceID] = true
		found := false
		for _, member := range task.Members {
			if member.ResourceID == instance.ResourceID {
				found = true
				if !reflect.DeepEqual(member.EngineIdentity, instance.EngineIdentity) || member.NodeID != instance.NodeID || member.IPAddress != instance.IPAddress || member.Port != instance.Port {
					return validationError("recovery member identity changed")
				}
			}
		}
		if !found {
			return validationError("unknown recovery member")
		}
		if instance.ResourceID == task.PrimaryID {
			if instance.Role != model.RolePrimary {
				return validationError("selected primary is not primary")
			}
			primary = instance
		} else {
			role := model.RoleReplica
			if task.Engine == model.EnginePostgreSQL {
				role = model.RoleStandby
			}
			if instance.Role != role || instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning || instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds != 0 {
				return validationError("replica is not fully caught up")
			}
		}
	}
	if primary.ResourceID == "" {
		return validationError("recovery primary is missing")
	}
	probed := map[model.ResourceID]bool{}
	for _, probe := range topology.Probes {
		if !seen[probe.InstanceID] || probed[probe.InstanceID] || probe.Outcome != model.ProbeOutcomeReachable || probe.Health.State != model.HealthHealthy || probe.DiscoveryObservedAt.Before(task.UpdatedAt) || probe.DiscoveryObservedAt.After(now) {
			return validationError("recovery requires current successful probes for every member")
		}
		probed[probe.InstanceID] = true
	}
	if len(probed) != len(seen) {
		return validationError("recovery probe evidence is incomplete")
	}
	key, err := identity.InstanceKey(primary.Engine, primary.EngineIdentity)
	if err != nil {
		return err
	}
	targets := map[model.ResourceID]bool{}
	for _, link := range topology.Links {
		if link.ClusterID != task.ClusterID || link.SourceInstanceID != task.PrimaryID || link.TargetInstanceID == task.PrimaryID || !seen[link.TargetInstanceID] || targets[link.TargetInstanceID] || !link.Healthy || link.LagSeconds == nil || *link.LagSeconds != 0 {
			return validationError("recovery replication links are incomplete or inconsistent")
		}
		targets[link.TargetInstanceID] = true
	}
	for _, instance := range topology.Instances {
		if instance.ResourceID != task.PrimaryID {
			source, err := identity.InstanceKey(instance.Engine, instance.Replication.SourceIdentity)
			if err != nil || source != key {
				return validationError("replica upstream identity is not the selected primary")
			}
		}
	}
	return nil
}

// CommitRecovery publishes roles, links and the recovered incident atomically.
// Freeze remains active until physical writer/VIP activation is verified.
func (r *Repository) CommitRecovery(ctx context.Context, task model.RecoveryTask, topology model.TopologySnapshot) (model.RecoveryTask, error) {
	if err := ctx.Err(); err != nil {
		return task, err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	current, found := r.snapshot.RecoveryTasks[task.ResourceID]
	now := r.now().UTC()
	if !found || current.MetadataRevision != task.MetadataRevision || current.Stage != model.RecoveryVerifying || !recoveryLeaseValid(r.snapshot, current, now) || !recoveryInventoryValid(r.snapshot, current) {
		return current, conflictError("recovery commit lost its executor or inventory")
	}
	if err := verifyRecoveryTopology(current, topology, now); err != nil {
		return current, err
	}
	next := cloneDiscoverySnapshot(r.snapshot)
	for i, instance := range topology.Instances {
		instance.DesiredRole = instance.Role
		instance.Maintenance = true
		instance.MetadataRevision = next.Instances[instance.ResourceID].MetadataRevision + 1
		instance.UpdatedAt = now
		next.Instances[instance.ResourceID] = cloneInstance(instance)
		topology.Instances[i] = instance
	}
	next.ReplicationLinks[current.ClusterID] = append([]model.ReplicationLink{}, topology.Links...)
	next.TopologySnapshots[current.ClusterID] = cloneTopologySnapshot(topology)
	next.ObservationWatermarks[current.ClusterID] = now
	cluster := next.Clusters[current.ClusterID]
	cluster.MetadataRevision++
	cluster.UpdatedAt = now
	cluster.Health = topology.Health
	cluster.Recovery = &model.RecoveryState{TaskID: current.ResourceID, CurrentPrimaryID: current.PrimaryID, ExpectedState: "running", ActualState: "running", IncidentRecovered: true, LastRecoveryStatus: "committed", LastShutdownClassification: "unplanned"}
	next.Clusters[current.ClusterID] = cluster
	for id, endpoint := range next.HAEndpoints {
		if endpoint.ClusterID == current.ClusterID {
			registered, found := next.Endpoints[current.ClusterID][endpoint.EndpointID]
			if !found || registered.ClusterID != current.ClusterID {
				return task, validationError("recovery endpoint registration is incomplete")
			}
			endpoint.OwnerID = current.PrimaryID
			endpoint.Healthy = false
			endpoint.MetadataRevision++
			endpoint.UpdatedAt = now
			next.HAEndpoints[id] = endpoint
			registered.InstanceID = current.PrimaryID
			registered.MetadataRevision++
			registered.UpdatedAt = now
			next.Endpoints[current.ClusterID][endpoint.EndpointID] = registered
		}
	}
	current.Stage = model.RecoveryCommitted
	current.CommittedAt = now
	current.VerifiedAt = topology.ObservedAt
	current.UpdatedAt = now
	current.MetadataRevision++
	current.Message = "Recovery Commit persisted; business entry remains protected until activation verification"
	current.Events = append(current.Events, model.RecoveryEvent{At: now, Stage: current.Stage, Message: current.Message})
	next.RecoveryTasks[current.ResourceID] = current
	if err := r.commitSnapshotLocked(next); err != nil {
		return task, err
	}
	return cloneRecoveryTask(current), nil
}

func (r *Repository) CompleteRecovery(ctx context.Context, task model.RecoveryTask) (model.RecoveryTask, error) {
	if err := ctx.Err(); err != nil {
		return task, err
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	current, found := r.snapshot.RecoveryTasks[task.ResourceID]
	now := r.now().UTC()
	if !found || current.MetadataRevision != task.MetadataRevision || current.Stage != model.RecoveryCommitted || current.CommittedAt.IsZero() || !recoveryLeaseValid(r.snapshot, current, now) || !recoveryInventoryValid(r.snapshot, current) {
		return current, conflictError("recovery completion lost its committed task or executor")
	}
	verified := current
	verified.UpdatedAt = current.VerifiedAt
	if current.VerifiedAt.IsZero() {
		return current, conflictError("recovery has no committed verification")
	}
	if err := verifyRecoveryTopology(verified, r.snapshot.TopologySnapshots[current.ClusterID], now); err != nil {
		return current, err
	}
	next := cloneDiscoverySnapshot(r.snapshot)
	cluster := next.Clusters[current.ClusterID]
	cluster.RecoveryFreeze = false
	cluster.MetadataRevision++
	cluster.UpdatedAt = now
	cluster.Recovery.LastRecoveryStatus = "succeeded"
	cluster.Recovery.RecoveredAt = now
	next.Clusters[current.ClusterID] = cluster
	for id, instance := range next.Instances {
		if instance.ClusterID == current.ClusterID {
			instance.Maintenance = false
			instance.MetadataRevision++
			instance.UpdatedAt = now
			next.Instances[id] = instance
		}
	}
	if topology, found := next.TopologySnapshots[current.ClusterID]; found {
		for i := range topology.Instances {
			topology.Instances[i].Maintenance = false
		}
		next.TopologySnapshots[current.ClusterID] = topology
	}
	current.Stage = model.RecoverySucceeded
	current.CompletedAt = now
	current.UpdatedAt = now
	current.MetadataRevision++
	current.Message = "all members, replication, writer and business entry verified; recovery protections released"
	current.Events = append(current.Events, model.RecoveryEvent{At: now, Stage: current.Stage, Message: current.Message})
	next.RecoveryTasks[current.ResourceID] = current
	if err := r.commitSnapshotLocked(next); err != nil {
		return task, fmt.Errorf("persist recovery completion: %w", err)
	}
	return cloneRecoveryTask(current), nil
}
