package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

type lifecycleCommitTarget struct {
	plan     lifecycle.TargetPlan
	instance *model.DatabaseInstance
}

func isLifecycleDataTarget(target lifecycle.TargetPlan) bool {
	return target.Kind == model.NodeData || target.Kind == model.NodeMixed
}

func validateLifecycleInstance(target lifecycle.TargetPlan, cluster model.DatabaseCluster, instance model.DatabaseInstance) error {
	if instance.ClusterID != cluster.ResourceID || instance.Engine != cluster.Engine || (cluster.Engine != model.EngineMySQL && cluster.Engine != model.EnginePostgreSQL) {
		return validationError("verified lifecycle instance does not match the database cluster")
	}
	if instance.NodeID != target.NodeID {
		return validationError("verified lifecycle instance node UUID does not match the fixed target")
	}
	if !strings.EqualFold(strings.TrimSpace(instance.Hostname), strings.TrimSpace(target.Hostname)) {
		return validationError("verified lifecycle instance hostname does not match the planned target")
	}
	if target.IPAddress != "" && strings.TrimSpace(instance.IPAddress) != strings.TrimSpace(target.IPAddress) {
		return validationError("verified lifecycle instance IP address does not match the planned target")
	}
	expectedPort, ok := lifecycleTargetPort(target, cluster.Engine)
	if !ok || instance.Port != expectedPort {
		return validationError("verified lifecycle instance port does not match the planned target")
	}
	if !verifiedLifecycleReplicaRole(cluster.Engine, instance.Role) || instance.Health.State != model.HealthHealthy || instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning {
		return validationError("verified lifecycle instance is not a healthy read-only replication target")
	}
	if _, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity); err != nil {
		return validationError("verified lifecycle instance native identity is invalid")
	}
	return nil
}

func lifecycleTargetPort(target lifecycle.TargetPlan, engine model.Engine) (int, bool) {
	switch engine {
	case model.EngineMySQL:
		return target.MySQLPort, target.MySQLPort > 0
	case model.EnginePostgreSQL:
		return target.PostgreSQLPort, target.PostgreSQLPort > 0
	default:
		return 0, false
	}
}

func verifiedLifecycleReplicaRole(engine model.Engine, role model.InstanceRole) bool {
	switch engine {
	case model.EngineMySQL:
		return role == model.RoleReplica
	case model.EnginePostgreSQL:
		return role == model.RoleReplica || role == model.RoleStandby
	default:
		return false
	}
}

func lifecycleCommitTargets(task lifecycle.Task, result lifecycle.ExecutionResult, cluster model.DatabaseCluster) ([]lifecycleCommitTarget, error) {
	if !result.Verified {
		return nil, validationError("lifecycle result is not verified")
	}
	if task.Plan.Blocked || task.Plan.ClusterID != task.ClusterID || task.Request.ClusterID != task.ClusterID || task.Plan.Action != task.Request.Action {
		return nil, validationError("lifecycle task plan does not match its immutable request")
	}
	resultByNode := make(map[model.ResourceID]model.DatabaseInstance, len(result.Instances))
	for _, instance := range result.Instances {
		if !model.ValidResourceID(instance.NodeID) {
			return nil, validationError("verified lifecycle instance has no fixed node UUID")
		}
		if _, duplicate := resultByNode[instance.NodeID]; duplicate {
			return nil, validationError("verified lifecycle result contains duplicate node UUIDs")
		}
		resultByNode[instance.NodeID] = instance
	}

	targets := make([]lifecycleCommitTarget, 0, len(task.Plan.Targets))
	expectedInstances := 0
	seenNodes := make(map[model.ResourceID]struct{}, len(task.Plan.Targets))
	seenNames := make(map[string]struct{}, len(task.Plan.Targets))
	for _, target := range task.Plan.Targets {
		if !model.ValidResourceID(target.NodeID) || !target.Kind.Valid() || !validNodeName(strings.TrimSpace(target.NodeName)) || strings.TrimSpace(target.Hostname) == "" {
			return nil, validationError("lifecycle target fixed identity is invalid")
		}
		if _, duplicate := seenNodes[target.NodeID]; duplicate {
			return nil, validationError("lifecycle plan contains duplicate node UUIDs")
		}
		seenNodes[target.NodeID] = struct{}{}
		nameKey := strings.ToLower(strings.TrimSpace(target.NodeName))
		if _, duplicate := seenNames[nameKey]; duplicate {
			return nil, validationError("lifecycle plan contains duplicate fixed node names")
		}
		seenNames[nameKey] = struct{}{}

		commitTarget := lifecycleCommitTarget{plan: target}
		if isLifecycleDataTarget(target) {
			expectedInstances++
			instance, found := resultByNode[target.NodeID]
			if !found {
				return nil, validationError("verified lifecycle result is missing a data target")
			}
			if err := validateLifecycleInstance(target, cluster, instance); err != nil {
				return nil, err
			}
			instanceCopy := cloneInstance(instance)
			commitTarget.instance = &instanceCopy
			delete(resultByNode, target.NodeID)
		}
		targets = append(targets, commitTarget)
	}
	if len(result.Instances) != expectedInstances || len(resultByNode) != 0 {
		return nil, validationError("verified lifecycle result does not exactly match the planned data targets")
	}
	return targets, nil
}

func endpointBoundToInstance(candidate snapshot, clusterID, instanceID model.ResourceID) (model.Endpoint, bool, error) {
	var selected model.Endpoint
	for _, endpoint := range candidate.Endpoints[clusterID] {
		if !endpoint.Active || endpoint.Kind != model.EndpointDatabase || endpoint.InstanceID != instanceID {
			continue
		}
		if selected.ResourceID != "" {
			return model.Endpoint{}, false, validationError("rebuilt instance has multiple active database endpoints")
		}
		selected = endpoint
	}
	return selected, selected.ResourceID != "", nil
}

func ensureLifecycleEndpointAvailable(candidate snapshot, replacement model.Endpoint) error {
	for _, clusterEndpoints := range candidate.Endpoints {
		for resourceID, endpoint := range clusterEndpoints {
			if resourceID == replacement.ResourceID || !endpoint.Active || endpoint.Kind != model.EndpointDatabase {
				continue
			}
			if endpointAddressCollision(endpoint, replacement) {
				return conflictError("verified database endpoint is already registered")
			}
		}
	}
	return nil
}

func removeLifecycleInstanceReferences(candidate *snapshot, clusterID, instanceID model.ResourceID) {
	delete(candidate.Instances, instanceID)
	links := candidate.ReplicationLinks[clusterID]
	keptLinks := make([]model.ReplicationLink, 0, len(links))
	for _, link := range links {
		if link.SourceInstanceID != instanceID && link.TargetInstanceID != instanceID {
			keptLinks = append(keptLinks, link)
		}
	}
	candidate.ReplicationLinks[clusterID] = keptLinks
	samples := candidate.MetricSamples[clusterID]
	keptSamples := make([]model.MetricSample, 0, len(samples))
	for _, sample := range samples {
		if sample.InstanceID != instanceID {
			keptSamples = append(keptSamples, sample)
		}
	}
	candidate.MetricSamples[clusterID] = keptSamples
}

func existingLifecycleNodeInstance(candidate snapshot, clusterID, nodeID model.ResourceID) (model.DatabaseInstance, error) {
	var selected model.DatabaseInstance
	for _, instance := range candidate.Instances {
		if instance.ClusterID != clusterID || instance.NodeID != nodeID {
			continue
		}
		if selected.ResourceID != "" {
			return model.DatabaseInstance{}, validationError("rebuild target has multiple database instances in the selected cluster")
		}
		selected = instance
	}
	if selected.ResourceID == "" {
		return model.DatabaseInstance{}, validationError("rebuild target has no registered database instance")
	}
	return selected, nil
}

func commitLifecycleDataTarget(candidate *snapshot, task lifecycle.Task, target lifecycle.TargetPlan, verified model.DatabaseInstance, now time.Time) error {
	rebuild := task.Plan.Action == lifecycle.ActionRebuild || target.Rebuild
	var endpoint model.Endpoint
	var preservedInstanceID model.ResourceID
	if rebuild {
		oldInstance, err := existingLifecycleNodeInstance(*candidate, task.ClusterID, target.NodeID)
		if err != nil {
			return err
		}
		oldKey, err := identity.InstanceKey(oldInstance.Engine, oldInstance.EngineIdentity)
		if err != nil {
			return validationError("registered lifecycle instance native identity is invalid")
		}
		newKey, err := identity.InstanceKey(verified.Engine, verified.EngineIdentity)
		if err != nil {
			return validationError("verified lifecycle instance native identity is invalid")
		}
		endpoint, _, err = endpointBoundToInstance(*candidate, task.ClusterID, oldInstance.ResourceID)
		if err != nil {
			return err
		}
		if endpoint.ResourceID == "" {
			return validationError("rebuild target has no active database endpoint to preserve")
		}
		if oldKey == newKey {
			preservedInstanceID = oldInstance.ResourceID
		} else {
			removeLifecycleInstanceReferences(candidate, task.ClusterID, oldInstance.ResourceID)
		}
	}

	verified.ResourceID = ""
	verified.MetadataRevision = 0
	verified.CreatedAt = time.Time{}
	verified.UpdatedAt = time.Time{}
	result, err := reconcileInstanceCandidate(candidate, verified, now)
	if err != nil {
		return fmt.Errorf("reconcile verified lifecycle instance: %w", err)
	}
	if preservedInstanceID != "" {
		if !result.Updated || result.Created || result.Instance.ResourceID != preservedInstanceID || result.Instance.NodeID != target.NodeID {
			return conflictError("verified database native identity could not be resynchronized in place")
		}
	} else if !result.Created || result.Instance.NodeID != target.NodeID {
		return conflictError("verified database native identity already belongs to active metadata")
	}

	if endpoint.ResourceID == "" {
		endpoint = model.Endpoint{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
			ClusterID:    task.ClusterID, Kind: model.EndpointDatabase, Active: true,
		}
	} else {
		endpoint.MetadataRevision++
		endpoint.UpdatedAt = now
	}
	endpoint.InstanceID = result.Instance.ResourceID
	endpoint.Hostname = result.Instance.Hostname
	endpoint.IPAddress = result.Instance.IPAddress
	endpoint.Port = result.Instance.Port
	endpoint = normalizeEndpoint(endpoint)
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}
	if err := ensureLifecycleEndpointAvailable(*candidate, endpoint); err != nil {
		return err
	}
	if candidate.Endpoints[task.ClusterID] == nil {
		candidate.Endpoints[task.ClusterID] = map[model.ResourceID]model.Endpoint{}
	}
	candidate.Endpoints[task.ClusterID][endpoint.ResourceID] = endpoint
	return nil
}

func lifecycleInstanceByNode(candidate snapshot, clusterID, nodeID model.ResourceID) (model.DatabaseInstance, bool, error) {
	var selected model.DatabaseInstance
	for _, instance := range candidate.Instances {
		if instance.ClusterID != clusterID || instance.NodeID != nodeID {
			continue
		}
		if selected.ResourceID != "" {
			return model.DatabaseInstance{}, false, validationError("lifecycle target has multiple active database instances")
		}
		selected = instance
	}
	return selected, selected.ResourceID != "", nil
}

// patchLifecycleTopology keeps the last published primary visible while a
// verified replica is committed. Node synchronization does not change the
// primary, so deleting the snapshot creates a false fail-closed window where
// the primary agent releases its VIP before discovery can publish again.
func patchLifecycleTopology(candidate *snapshot, task lifecycle.Task, targets []lifecycleCommitTarget) error {
	topology, found := candidate.TopologySnapshots[task.ClusterID]
	if !found {
		return nil
	}
	topology = cloneTopologySnapshot(topology)

	replacements := make(map[model.ResourceID]model.DatabaseInstance)
	for _, target := range targets {
		if target.instance == nil {
			continue
		}
		instance, instanceFound, err := lifecycleInstanceByNode(*candidate, task.ClusterID, target.plan.NodeID)
		if err != nil {
			return err
		}
		if !instanceFound {
			return validationError("verified lifecycle target is missing after metadata commit")
		}
		replacements[target.plan.NodeID] = cloneInstance(instance)
	}
	if len(replacements) == 0 {
		candidate.TopologySnapshots[task.ClusterID] = topology
		return nil
	}

	resourceReplacements := make(map[model.ResourceID]model.ResourceID)
	insertedNodes := make(map[model.ResourceID]struct{}, len(replacements))
	instances := make([]model.DatabaseInstance, 0, len(topology.Instances)+len(replacements))
	for _, observed := range topology.Instances {
		replacement, replace := replacements[observed.NodeID]
		if !replace {
			instances = append(instances, cloneInstance(observed))
			continue
		}
		resourceReplacements[observed.ResourceID] = replacement.ResourceID
		if _, inserted := insertedNodes[observed.NodeID]; inserted {
			continue
		}
		instances = append(instances, replacement)
		insertedNodes[observed.NodeID] = struct{}{}
	}
	for nodeID, replacement := range replacements {
		if _, inserted := insertedNodes[nodeID]; inserted {
			continue
		}
		instances = append(instances, replacement)
	}
	topology.Instances = instances

	activeIDs := make(map[model.ResourceID]struct{}, len(instances))
	for _, instance := range instances {
		activeIDs[instance.ResourceID] = struct{}{}
	}
	type edge struct {
		source model.ResourceID
		target model.ResourceID
	}
	seenEdges := make(map[edge]struct{}, len(topology.Links))
	links := make([]model.ReplicationLink, 0, len(topology.Links))
	for _, link := range topology.Links {
		if replacementID, replace := resourceReplacements[link.SourceInstanceID]; replace {
			link.SourceInstanceID = replacementID
		}
		if replacementID, replace := resourceReplacements[link.TargetInstanceID]; replace {
			link.TargetInstanceID = replacementID
		}
		if _, sourceActive := activeIDs[link.SourceInstanceID]; !sourceActive {
			continue
		}
		if _, targetActive := activeIDs[link.TargetInstanceID]; !targetActive || link.SourceInstanceID == link.TargetInstanceID {
			continue
		}
		key := edge{source: link.SourceInstanceID, target: link.TargetInstanceID}
		if _, duplicate := seenEdges[key]; duplicate {
			continue
		}
		seenEdges[key] = struct{}{}
		links = append(links, cloneReplicationLink(link))
	}
	topology.Links = links

	probes := make([]model.ProbeStatus, 0, len(topology.Probes))
	for _, probe := range topology.Probes {
		if replacementID, replace := resourceReplacements[probe.InstanceID]; replace {
			probe.InstanceID = replacementID
		}
		if probe.InstanceID != "" {
			if _, active := activeIDs[probe.InstanceID]; !active {
				continue
			}
		}
		probes = append(probes, probe)
	}
	topology.Probes = probes
	candidate.TopologySnapshots[task.ClusterID] = topology
	return nil
}

// Commit publishes a verified lifecycle result as one metadata transaction.
// Physical node identity is preserved while a rebuilt database either keeps
// its native identity for an in-place resync or receives a replacement native
// identity. The existing endpoint is rebound atomically in both cases.
func (repository *Repository) Commit(ctx context.Context, task lifecycle.Task, result lifecycle.ExecutionResult) error {
	if ctx == nil {
		return validationError("lifecycle commit context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !model.ValidResourceID(task.ResourceID) || task.Status != lifecycle.TaskVerifying {
		return validationError("lifecycle task must be in verifying state")
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, found := repository.snapshot.LifecycleTasks[task.ResourceID]
	if !found || stored.Status != lifecycle.TaskVerifying || stored.MetadataRevision != task.MetadataRevision || !reflect.DeepEqual(stored.Request, task.Request) || !reflect.DeepEqual(stored.Plan, task.Plan) {
		return conflictError("lifecycle task changed before metadata commit")
	}
	cluster, found := repository.snapshot.Clusters[task.ClusterID]
	if !found {
		return validationError("lifecycle cluster is not registered")
	}
	targets, err := lifecycleCommitTargets(stored, result, cluster)
	if err != nil {
		return err
	}

	now := repository.now().UTC()
	next := cloneDiscoverySnapshot(repository.snapshot)
	for _, target := range targets {
		existingNode, existed := next.Nodes[target.plan.NodeID]
		rebuild := stored.Plan.Action == lifecycle.ActionRebuild || target.plan.Rebuild
		if rebuild {
			if !existed || existingNode.NodeName != target.plan.NodeName || existingNode.Kind != target.plan.Kind {
				return conflictError("rebuild target does not match its fixed registered node identity")
			}
		} else if existed {
			return conflictError("new lifecycle target node UUID is already registered")
		}
		node := existingNode
		node.ResourceID = target.plan.NodeID
		node.NodeName = target.plan.NodeName
		if node.DisplayName == "" {
			node.DisplayName = target.plan.NodeName
		}
		node.Kind = target.plan.Kind
		node.Hostname = target.plan.Hostname
		node.IPAddress = target.plan.IPAddress
		node.Active = true
		if _, err := putNodeCandidate(next.Nodes, node, now); err != nil {
			return fmt.Errorf("commit fixed lifecycle node: %w", err)
		}
		if target.instance != nil {
			if err := commitLifecycleDataTarget(&next, stored, target.plan, *target.instance, now); err != nil {
				return err
			}
		}
	}

	if err := patchLifecycleTopology(&next, stored, targets); err != nil {
		return fmt.Errorf("preserve lifecycle topology continuity: %w", err)
	}
	next.InventoryGenerations[task.ClusterID]++
	updatedCluster := next.Clusters[task.ClusterID]
	updatedCluster.Health = model.Health{State: model.HealthUnknown}
	updatedCluster.MetadataRevision++
	updatedCluster.UpdatedAt = now
	next.Clusters[task.ClusterID] = updatedCluster
	for anomalyID, anomaly := range next.Anomalies {
		if anomaly.ClusterID == task.ClusterID {
			delete(next.Anomalies, anomalyID)
		}
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return err
	}
	return nil
}
