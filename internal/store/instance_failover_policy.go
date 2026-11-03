package store

import (
	"clusterguard.io/ha/pkg/model"
	"fmt"
	"strings"
)

func (r *Repository) PutInstanceFailoverPolicy(id model.ResourceID, expected uint64, policy model.InstanceFailoverPolicy, actor string) (model.InstanceFailoverPolicy, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return model.InstanceFailoverPolicy{}, validationError("policy actor is required")
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	cluster, ok := r.snapshot.Clusters[id]
	if !ok {
		return model.InstanceFailoverPolicy{}, notFoundError("cluster not found")
	}
	if cluster.Engine != model.EngineMySQL && cluster.Engine != model.EnginePostgreSQL {
		return model.InstanceFailoverPolicy{}, validationError("automatic failover policy supports MySQL and PostgreSQL")
	}
	if r.snapshot.SoftwareUpdateGate != nil {
		return model.InstanceFailoverPolicy{}, conflictError("software update maintenance active")
	}
	current := uint64(0)
	if cluster.FailoverPolicy != nil {
		current = cluster.FailoverPolicy.Revision
	}
	if current == ^uint64(0) {
		return model.InstanceFailoverPolicy{}, conflictError("failover policy revision exhausted")
	}
	if expected != current {
		return model.InstanceFailoverPolicy{}, conflictError("failover policy revision changed; reload before saving")
	}
	for _, op := range r.snapshot.Operations {
		if op.Operation.ClusterID == id && op.Status == model.OperationRunning {
			return model.InstanceFailoverPolicy{}, conflictError("cluster operation is running")
		}
	}
	for _, task := range r.snapshot.ConfigurationTasks {
		if task.Active {
			return model.InstanceFailoverPolicy{}, conflictError("configuration distribution maintenance active")
		}
	}
	stored := policy.Clone()
	for instanceID, preference := range stored.Instances {
		instance, found := r.snapshot.Instances[instanceID]
		if !model.ValidResourceID(instanceID) || !found || instance.ClusterID != id || instance.Engine != cluster.Engine {
			return model.InstanceFailoverPolicy{}, validationError("policy instance is outside this database cluster")
		}
		if preference.Weight < 0 || preference.Weight > model.MaximumFailoverWeight {
			return model.InstanceFailoverPolicy{}, validationError("instance weight must be between 0 and 1000")
		}
	}
	now := r.now().UTC()
	stored.Revision = current + 1
	stored.UpdatedAt = now
	stored.UpdatedBy = actor
	audit := redactAudit(model.AuditEvent{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}, OperationID: model.NewResourceID(), Stage: model.StageAudit, Actor: actor, Message: fmt.Sprintf("instance failover policy cluster=%s automatic=%t revision=%d instances=%d", id, stored.AutomaticEnabled, stored.Revision, len(stored.Instances))})
	if err := validateAuditText(audit); err != nil {
		return model.InstanceFailoverPolicy{}, err
	}
	cluster = cloneCluster(cluster)
	cluster.FailoverPolicy = stored
	cluster.MetadataRevision++
	cluster.UpdatedAt = now
	next := r.snapshot
	next.Clusters = cloneClusterMap(next.Clusters)
	next.Clusters[id] = cluster
	floor := model.PolicyCompatibilityFloor()
	next.RuntimeCompatibilityFloor = &floor
	next.Audits = append(append([]model.AuditEvent{}, next.Audits...), audit)
	if err := r.commitSnapshotLocked(next); err != nil {
		return model.InstanceFailoverPolicy{}, fmt.Errorf("persist instance failover policy: %w", err)
	}
	return *stored.Clone(), nil
}
