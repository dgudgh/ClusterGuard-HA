package store

import (
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type ClusterRetirement struct {
	Cluster                 model.DatabaseCluster `json:"cluster"`
	RetiredAt               time.Time             `json:"retired_at"`
	InstancesRemoved        int                   `json:"instances_removed"`
	WorkloadBindingsRemoved int                   `json:"workload_bindings_removed"`
	EndpointsRemoved        int                   `json:"endpoints_removed"`
	HAEndpointsRemoved      int                   `json:"ha_endpoints_removed"`
	ReplicationLinksRemoved int                   `json:"replication_links_removed"`
	MetricSamplesRemoved    int                   `json:"metric_samples_removed"`
	AnomaliesRemoved        int                   `json:"anomalies_removed"`
}

func activeLifecycleStatus(status lifecycle.TaskStatus) bool {
	switch status {
	case lifecycle.TaskPlanned, lifecycle.TaskQueued, lifecycle.TaskRunning, lifecycle.TaskVerifying:
		return true
	default:
		return false
	}
}

func (repository *Repository) RetireCluster(clusterID model.ResourceID, confirmDisplayName, actor string) (ClusterRetirement, error) {
	if !model.ValidResourceID(clusterID) {
		return ClusterRetirement{}, validationError("cluster ID is invalid")
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	cluster, found := repository.snapshot.Clusters[clusterID]
	if !found {
		return ClusterRetirement{}, notFoundError("cluster does not exist")
	}
	if confirmDisplayName != cluster.DisplayName {
		return ClusterRetirement{}, validationError("cluster display name confirmation does not match")
	}
	now := repository.now().UTC()
	for _, operation := range repository.snapshot.Operations {
		if operation.Operation.ClusterID == clusterID && operation.Status == model.OperationRunning {
			return ClusterRetirement{}, conflictError("cluster has a running database operation")
		}
	}
	for _, record := range repository.snapshot.OperationLocks {
		if record.ClusterID == clusterID && record.ExpiresAt.After(now) {
			return ClusterRetirement{}, conflictError("cluster operation lock is active")
		}
	}
	for _, record := range repository.snapshot.CoordinationLeases {
		lease := record.Lease
		if lease.ClusterID == clusterID && lease.Active && lease.ExpiresAt.After(now) {
			return ClusterRetirement{}, conflictError("cluster HA endpoint ownership lease is active")
		}
	}
	for _, task := range repository.snapshot.LifecycleTasks {
		if task.ClusterID == clusterID && activeLifecycleStatus(task.Status) {
			return ClusterRetirement{}, conflictError("cluster node lifecycle task is active")
		}
	}

	result := ClusterRetirement{
		Cluster: cloneCluster(cluster), RetiredAt: now,
		EndpointsRemoved:        len(repository.snapshot.Endpoints[clusterID]),
		ReplicationLinksRemoved: len(repository.snapshot.ReplicationLinks[clusterID]),
		MetricSamplesRemoved:    len(repository.snapshot.MetricSamples[clusterID]),
	}
	for _, instance := range repository.snapshot.Instances {
		if instance.ClusterID == clusterID {
			result.InstancesRemoved++
		}
	}
	for _, binding := range repository.snapshot.WorkloadBindings {
		if instance, found := repository.snapshot.Instances[binding.InstanceID]; found && instance.ClusterID == clusterID {
			result.WorkloadBindingsRemoved++
		}
	}
	for _, resource := range repository.snapshot.HAEndpoints {
		if resource.ClusterID == clusterID {
			result.HAEndpointsRemoved++
		}
	}
	for _, anomaly := range repository.snapshot.Anomalies {
		if anomaly.ClusterID == clusterID {
			result.AnomaliesRemoved++
		}
	}

	next := repository.snapshot
	next.Clusters = cloneClusterMap(repository.snapshot.Clusters)
	delete(next.Clusters, clusterID)
	next.WorkloadBindings = cloneWorkloadBindingMap(repository.snapshot.WorkloadBindings)
	for resourceID, binding := range next.WorkloadBindings {
		if instance, found := repository.snapshot.Instances[binding.InstanceID]; found && instance.ClusterID == clusterID {
			delete(next.WorkloadBindings, resourceID)
		}
	}
	next.Instances = cloneInstanceMap(repository.snapshot.Instances)
	for resourceID, instance := range next.Instances {
		if instance.ClusterID == clusterID {
			delete(next.Instances, resourceID)
		}
	}
	next.Endpoints = cloneEndpointMap(repository.snapshot.Endpoints)
	delete(next.Endpoints, clusterID)
	next.HAEndpoints = cloneHAEndpointMap(repository.snapshot.HAEndpoints)
	for resourceID, resource := range next.HAEndpoints {
		if resource.ClusterID == clusterID {
			delete(next.HAEndpoints, resourceID)
		}
	}
	next.ReplicationLinks = cloneReplicationLinkMap(repository.snapshot.ReplicationLinks)
	delete(next.ReplicationLinks, clusterID)
	next.MetricSamples = cloneMetricSampleMap(repository.snapshot.MetricSamples)
	delete(next.MetricSamples, clusterID)
	next.TopologySnapshots = cloneTopologySnapshotMap(repository.snapshot.TopologySnapshots)
	delete(next.TopologySnapshots, clusterID)
	next.ObservationWatermarks = cloneTimeMap(repository.snapshot.ObservationWatermarks)
	delete(next.ObservationWatermarks, clusterID)
	next.InventoryGenerations = cloneUint64Map(repository.snapshot.InventoryGenerations)
	delete(next.InventoryGenerations, clusterID)
	next.Anomalies = cloneAnomalyMap(repository.snapshot.Anomalies)
	for resourceID, anomaly := range next.Anomalies {
		if anomaly.ClusterID == clusterID {
			delete(next.Anomalies, resourceID)
		}
	}
	next.ApprovalGrants = cloneApprovalGrantMap(repository.snapshot.ApprovalGrants)
	for resourceID, grant := range next.ApprovalGrants {
		if grant.ClusterID == clusterID {
			delete(next.ApprovalGrants, resourceID)
		}
	}
	next.CoordinationLeases = cloneCoordinationLeaseMap(repository.snapshot.CoordinationLeases)
	for resourceID, record := range next.CoordinationLeases {
		if record.Lease.ClusterID == clusterID {
			delete(next.CoordinationLeases, resourceID)
		}
	}
	next.OperationLocks = cloneOperationLockMap(repository.snapshot.OperationLocks)
	for resourceID, record := range next.OperationLocks {
		if record.ClusterID == clusterID {
			delete(next.OperationLocks, resourceID)
		}
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}
	audit := model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  model.NewResourceID(), Stage: model.StageAudit, Actor: actor,
		Message: fmt.Sprintf(
			"cluster %s (%s) retired from active management; instances=%d endpoints=%d ha_endpoints=%d replication_links=%d metric_samples=%d anomalies=%d",
			cluster.DisplayName,
			cluster.ResourceID,
			result.InstancesRemoved,
			result.EndpointsRemoved,
			result.HAEndpointsRemoved,
			result.ReplicationLinksRemoved,
			result.MetricSamplesRemoved,
			result.AnomaliesRemoved,
		),
	}
	next.Audits = append(append([]model.AuditEvent{}, repository.snapshot.Audits...), audit)
	if err := repository.commitSnapshotLocked(next); err != nil {
		return result, err
	}
	return result, nil
}
