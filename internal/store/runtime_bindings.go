package store

import (
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func normalizeRuntimeTarget(target model.RuntimeTarget) model.RuntimeTarget {
	target.DisplayName = strings.TrimSpace(target.DisplayName)
	target.Endpoint = strings.TrimSpace(target.Endpoint)
	target.CredentialRef = strings.TrimSpace(target.CredentialRef)
	target.TLSProfile = strings.TrimSpace(target.TLSProfile)
	labels := make(map[string]string, len(target.Labels))
	for key, value := range target.Labels {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" {
			labels[key] = value
		}
	}
	target.Labels = labels
	return target
}

func validRuntimeText(value string, maximum int) bool {
	if len(value) > maximum || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	return true
}

func validateRuntimeTarget(target model.RuntimeTarget) error {
	if !model.ValidResourceID(target.ResourceID) {
		return validationError("runtime target resource ID is invalid")
	}
	if target.DisplayName == "" || !validRuntimeText(target.DisplayName, 128) {
		return validationError("runtime target display name is required and must not exceed 128 characters")
	}
	if !target.Kind.Valid() {
		return validationError("runtime target kind must be linux, docker, or kubernetes")
	}
	if !validRuntimeText(target.Endpoint, 512) || !validRuntimeText(target.CredentialRef, 256) || !validRuntimeText(target.TLSProfile, 256) {
		return validationError("runtime target endpoint or credential reference is invalid")
	}
	if target.Kind == model.RuntimeKubernetes && target.Active {
		endpoint, err := url.Parse(target.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return validationError("active Kubernetes runtime target requires an HTTPS API origin")
		}
		if !filepath.IsAbs(target.CredentialRef) {
			return validationError("active Kubernetes runtime target requires an absolute credential reference")
		}
	}
	if len(target.Labels) > 64 {
		return validationError("runtime target has too many labels")
	}
	for key, value := range target.Labels {
		if key == "" || !validRuntimeText(key, 128) || !validRuntimeText(value, 256) {
			return validationError("runtime target label is invalid")
		}
	}
	return nil
}

func normalizeWorkloadBinding(binding model.WorkloadBinding) model.WorkloadBinding {
	binding.ObservedRuntimeID = strings.TrimSpace(binding.ObservedRuntimeID)
	if binding.Docker != nil {
		copy := *binding.Docker
		copy.SwarmServiceID = strings.TrimSpace(copy.SwarmServiceID)
		copy.SwarmServiceName = strings.TrimSpace(copy.SwarmServiceName)
		copy.NodeID = strings.TrimSpace(copy.NodeID)
		copy.ContainerName = strings.TrimSpace(copy.ContainerName)
		copy.ContainerID = strings.TrimSpace(copy.ContainerID)
		copy.VolumeIdentity = strings.TrimSpace(copy.VolumeIdentity)
		binding.Docker = &copy
	}
	if binding.Kubernetes != nil {
		copy := *binding.Kubernetes
		copy.ClusterName = strings.TrimSpace(copy.ClusterName)
		copy.Namespace = strings.TrimSpace(copy.Namespace)
		copy.StatefulSet = strings.TrimSpace(copy.StatefulSet)
		copy.PodName = strings.TrimSpace(copy.PodName)
		copy.PodUID = strings.TrimSpace(copy.PodUID)
		copy.NodeName = strings.TrimSpace(copy.NodeName)
		copy.PVCUID = strings.TrimSpace(copy.PVCUID)
		binding.Kubernetes = &copy
	}
	return binding
}

func validRuntimeObjectName(value string) bool {
	if len(value) < 1 || len(value) > 253 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (index > 0 && strings.ContainsRune("._-", character)) {
			continue
		}
		return false
	}
	return true
}

func validKubernetesObjectName(value string) bool {
	if len(value) < 1 || len(value) > 253 {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			(index > 0 && (character == '-' || character == '.')) {
			continue
		}
		return false
	}
	last := value[len(value)-1]
	return last >= 'a' && last <= 'z' || last >= '0' && last <= '9'
}

func validateWorkloadBinding(value snapshot, binding model.WorkloadBinding) error {
	if !model.ValidResourceID(binding.ResourceID) || !model.ValidResourceID(binding.InstanceID) || !model.ValidResourceID(binding.RuntimeTargetID) {
		return validationError("workload binding resource, instance, and runtime target IDs are required")
	}
	instance, instanceFound := value.Instances[binding.InstanceID]
	if !instanceFound {
		return validationError("workload binding instance does not exist")
	}
	target, targetFound := value.RuntimeTargets[binding.RuntimeTargetID]
	if !targetFound || !target.Active {
		return validationError("workload binding runtime target is not active")
	}
	if binding.RuntimeKind != target.Kind || !binding.RuntimeKind.Valid() {
		return validationError("workload binding runtime kind does not match its target")
	}
	if binding.HostNodeID != "" {
		node, found := value.Nodes[binding.HostNodeID]
		if !found || !node.Active || (instance.NodeID != "" && instance.NodeID != binding.HostNodeID) {
			return validationError("workload binding host node is invalid")
		}
	}
	if !validRuntimeText(binding.ObservedRuntimeID, 256) {
		return validationError("observed runtime ID is invalid")
	}
	switch binding.RuntimeKind {
	case model.RuntimeLinux:
		if binding.Docker != nil || binding.Kubernetes != nil || !model.ValidResourceID(binding.HostNodeID) {
			return validationError("linux workload binding requires one host node and no container reference")
		}
	case model.RuntimeDocker:
		if binding.Docker == nil || binding.Kubernetes != nil || !model.ValidResourceID(binding.HostNodeID) {
			return validationError("docker workload binding requires one host node and one Docker reference")
		}
		if !validRuntimeObjectName(binding.Docker.SwarmServiceName) {
			return validationError("Docker Swarm service name is invalid")
		}
		for _, value := range []string{binding.Docker.SwarmServiceID, binding.Docker.NodeID, binding.Docker.ContainerName, binding.Docker.ContainerID, binding.Docker.VolumeIdentity} {
			if !validRuntimeText(value, 256) {
				return validationError("Docker workload reference is invalid")
			}
		}
	case model.RuntimeKubernetes:
		if binding.Kubernetes == nil || binding.Docker != nil {
			return validationError("kubernetes workload binding requires one Kubernetes reference")
		}
		if !validRuntimeObjectName(binding.Kubernetes.ClusterName) || !validKubernetesObjectName(binding.Kubernetes.Namespace) ||
			!validKubernetesObjectName(binding.Kubernetes.StatefulSet) || binding.Kubernetes.Ordinal < 0 {
			return validationError("Kubernetes workload reference is invalid")
		}
		if binding.Kubernetes.PodName != "" && !validKubernetesObjectName(binding.Kubernetes.PodName) {
			return validationError("Kubernetes Pod name is invalid")
		}
		if binding.Kubernetes.NodeName != "" && !validKubernetesObjectName(binding.Kubernetes.NodeName) {
			return validationError("Kubernetes node name is invalid")
		}
		if !validRuntimeText(binding.Kubernetes.PodUID, 256) || !validRuntimeText(binding.Kubernetes.PVCUID, 256) {
			return validationError("Kubernetes observed workload identity is invalid")
		}
		if binding.Active && (binding.Kubernetes.Ordinal != 0 || binding.Kubernetes.PodName != binding.Kubernetes.StatefulSet+"-0") {
			return validationError("active Kubernetes workload binding requires a dedicated ordinal-zero StatefulSet Pod")
		}
		if binding.Active && (binding.Kubernetes.PodUID == "" || binding.Kubernetes.NodeName == "" || binding.Kubernetes.PVCUID == "") {
			return validationError("active Kubernetes workload binding requires Pod UID, Node name, and PVC UID")
		}
	}
	if binding.Generation == 0 {
		return validationError("workload binding generation must be positive")
	}
	return nil
}

func putRuntimeTargetCandidate(targets map[model.ResourceID]model.RuntimeTarget, target model.RuntimeTarget, now time.Time) (model.RuntimeTarget, error) {
	target = normalizeRuntimeTarget(target)
	if target.ResourceID == "" {
		target.ResourceID = model.NewResourceID()
	}
	if err := validateRuntimeTarget(target); err != nil {
		return model.RuntimeTarget{}, err
	}
	existing, found := targets[target.ResourceID]
	for resourceID, candidate := range targets {
		if resourceID != target.ResourceID && candidate.Active && target.Active && strings.EqualFold(candidate.DisplayName, target.DisplayName) {
			return model.RuntimeTarget{}, conflictError("active runtime target display name already exists")
		}
	}
	if found {
		target.CreatedAt = existing.CreatedAt
		target.MetadataRevision = existing.MetadataRevision + 1
	} else {
		target.CreatedAt = now
		target.MetadataRevision = 1
	}
	target.UpdatedAt = now
	targets[target.ResourceID] = cloneRuntimeTarget(target)
	return target, nil
}

func (repository *Repository) PutRuntimeTarget(target model.RuntimeTarget) (model.RuntimeTarget, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.RuntimeTargets = cloneRuntimeTargetMap(repository.snapshot.RuntimeTargets)
	var err error
	target, err = putRuntimeTargetCandidate(next.RuntimeTargets, target, repository.now().UTC())
	if err != nil {
		return model.RuntimeTarget{}, err
	}
	for _, binding := range repository.snapshot.WorkloadBindings {
		if binding.Active && binding.RuntimeTargetID == target.ResourceID && (!target.Active || binding.RuntimeKind != target.Kind) {
			return model.RuntimeTarget{}, conflictError("runtime target has active workload bindings")
		}
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneRuntimeTarget(target), err
		}
		return model.RuntimeTarget{}, err
	}
	return cloneRuntimeTarget(target), nil
}

func (repository *Repository) RuntimeTarget(resourceID model.ResourceID) (model.RuntimeTarget, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	target, found := repository.snapshot.RuntimeTargets[resourceID]
	return cloneRuntimeTarget(target), found
}

func (repository *Repository) RuntimeTargets() []model.RuntimeTarget {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.RuntimeTarget, 0, len(repository.snapshot.RuntimeTargets))
	for _, target := range repository.snapshot.RuntimeTargets {
		result = append(result, cloneRuntimeTarget(target))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].DisplayName != result[right].DisplayName {
			return result[left].DisplayName < result[right].DisplayName
		}
		return result[left].ResourceID < result[right].ResourceID
	})
	return result
}

func putWorkloadBindingCandidate(value snapshot, binding model.WorkloadBinding, now time.Time) (model.WorkloadBinding, error) {
	binding = normalizeWorkloadBinding(binding)
	if binding.ResourceID == "" {
		binding.ResourceID = model.NewResourceID()
	}
	existing, found := value.WorkloadBindings[binding.ResourceID]
	if found && existing.InstanceID != binding.InstanceID {
		return model.WorkloadBinding{}, conflictError("workload binding instance is immutable")
	}
	if found {
		binding.CreatedAt = existing.CreatedAt
		binding.MetadataRevision = existing.MetadataRevision + 1
		binding.Generation = existing.Generation + 1
		binding.ObservedAt = existing.ObservedAt
	} else {
		binding.CreatedAt = now
		binding.MetadataRevision = 1
		binding.Generation = 1
	}
	if binding.ObservedRuntimeID != "" && (!found || binding.ObservedRuntimeID != existing.ObservedRuntimeID) {
		binding.ObservedAt = now
	}
	binding.UpdatedAt = now
	for resourceID, candidate := range value.WorkloadBindings {
		if resourceID != binding.ResourceID && candidate.Active && binding.Active && candidate.InstanceID == binding.InstanceID {
			return model.WorkloadBinding{}, conflictError("instance already has an active workload binding")
		}
		if resourceID != binding.ResourceID && candidate.Active && binding.Active &&
			candidate.RuntimeKind == model.RuntimeKubernetes && binding.RuntimeKind == model.RuntimeKubernetes &&
			candidate.RuntimeTargetID == binding.RuntimeTargetID && candidate.Kubernetes != nil && binding.Kubernetes != nil {
			sameNamespace := candidate.Kubernetes.Namespace == binding.Kubernetes.Namespace
			if sameNamespace && candidate.Kubernetes.StatefulSet == binding.Kubernetes.StatefulSet {
				return model.WorkloadBinding{}, conflictError("Kubernetes StatefulSet is already bound to another database instance")
			}
			if sameNamespace && candidate.Kubernetes.PodName == binding.Kubernetes.PodName {
				return model.WorkloadBinding{}, conflictError("Kubernetes Pod is already bound to another database instance")
			}
			if candidate.Kubernetes.PodUID == binding.Kubernetes.PodUID {
				return model.WorkloadBinding{}, conflictError("Kubernetes Pod UID is already bound to another database instance")
			}
			if candidate.Kubernetes.PVCUID == binding.Kubernetes.PVCUID {
				return model.WorkloadBinding{}, conflictError("Kubernetes PVC UID is already bound to another database instance")
			}
		}
	}
	if err := validateWorkloadBinding(value, binding); err != nil {
		return model.WorkloadBinding{}, err
	}
	value.WorkloadBindings[binding.ResourceID] = cloneWorkloadBinding(binding)
	return binding, nil
}

func (repository *Repository) PutWorkloadBinding(binding model.WorkloadBinding) (model.WorkloadBinding, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.WorkloadBindings = cloneWorkloadBindingMap(repository.snapshot.WorkloadBindings)
	var err error
	binding, err = putWorkloadBindingCandidate(next, binding, repository.now().UTC())
	if err != nil {
		return model.WorkloadBinding{}, err
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		if errors.Is(err, ErrPostCommitDurability) {
			return cloneWorkloadBinding(binding), err
		}
		return model.WorkloadBinding{}, err
	}
	return cloneWorkloadBinding(binding), nil
}

func (repository *Repository) WorkloadBinding(resourceID model.ResourceID) (model.WorkloadBinding, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	binding, found := repository.snapshot.WorkloadBindings[resourceID]
	return cloneWorkloadBinding(binding), found
}

func (repository *Repository) WorkloadBindingForInstance(instanceID model.ResourceID) (model.WorkloadBinding, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, binding := range repository.snapshot.WorkloadBindings {
		if binding.InstanceID == instanceID && binding.Active {
			return cloneWorkloadBinding(binding), true
		}
	}
	return model.WorkloadBinding{}, false
}

func (repository *Repository) WorkloadBindings() []model.WorkloadBinding {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]model.WorkloadBinding, 0, len(repository.snapshot.WorkloadBindings))
	for _, binding := range repository.snapshot.WorkloadBindings {
		result = append(result, cloneWorkloadBinding(binding))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].InstanceID != result[right].InstanceID {
			return result[left].InstanceID < result[right].InstanceID
		}
		return result[left].ResourceID < result[right].ResourceID
	})
	return result
}

func (repository *Repository) WorkloadBindingsForCluster(clusterID model.ResourceID) []model.WorkloadBinding {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	instanceIDs := make(map[model.ResourceID]struct{})
	for resourceID, instance := range repository.snapshot.Instances {
		if instance.ClusterID == clusterID {
			instanceIDs[resourceID] = struct{}{}
		}
	}
	result := make([]model.WorkloadBinding, 0)
	for _, binding := range repository.snapshot.WorkloadBindings {
		if _, found := instanceIDs[binding.InstanceID]; found {
			result = append(result, cloneWorkloadBinding(binding))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].InstanceID != result[right].InstanceID {
			return result[left].InstanceID < result[right].InstanceID
		}
		return result[left].ResourceID < result[right].ResourceID
	})
	return result
}
