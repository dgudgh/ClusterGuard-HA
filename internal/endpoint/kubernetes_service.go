package endpoint

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const (
	kubernetesServiceLabel  = "kubernetes.io/service-name"
	kubernetesManagedLabel  = "endpointslice.kubernetes.io/managed-by"
	kubernetesManagedBy     = "clusterguard.io/ha"
	kubernetesOwnerLabel    = "clusterguard.io/owner-id"
	kubernetesOperationKey  = "clusterguard.io/operation-id"
	kubernetesLeaseKey      = "clusterguard.io/lease-id"
	kubernetesFenceGuardKey = "clusterguard.io/fence-guard"
	kubernetesFencedKey     = "clusterguard.io/fenced"
	kubernetesMySQLRoleKey  = "clusterguard.io/mysql-role"
)

type KubernetesServiceInventory interface {
	Inventory
	WorkloadBindingForInstance(model.ResourceID) (model.WorkloadBinding, bool)
	RuntimeTarget(model.ResourceID) (model.RuntimeTarget, bool)
}

type KubernetesServiceProvider struct {
	inventory               KubernetesServiceInventory
	factory                 kubernetes.Factory
	leases                  LeaseStore
	transitionLeaseTTL      time.Duration
	transitionRenewInterval time.Duration
}

func NewKubernetesServiceProvider(inventory KubernetesServiceInventory, factory kubernetes.Factory, leases LeaseStore) *KubernetesServiceProvider {
	return &KubernetesServiceProvider{
		inventory: inventory, factory: factory, leases: leases,
		transitionLeaseTTL: 30 * time.Second, transitionRenewInterval: 10 * time.Second,
	}
}

func (provider *KubernetesServiceProvider) Executable(context.Context) bool {
	return provider != nil && provider.inventory != nil && provider.factory != nil && provider.leases != nil
}

type kubernetesServiceRef struct {
	namespace     string
	service       string
	endpointSlice string
}

func parseKubernetesServiceRef(value string) (kubernetesServiceRef, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 3 {
		return kubernetesServiceRef{}, fmt.Errorf("Kubernetes provider_ref must be namespace/service/endpoint-slice")
	}
	for _, part := range parts {
		if !validKubernetesName(part) {
			return kubernetesServiceRef{}, fmt.Errorf("Kubernetes provider_ref contains an invalid resource name")
		}
	}
	return kubernetesServiceRef{namespace: parts[0], service: parts[1], endpointSlice: parts[2]}, nil
}

func validKubernetesName(value string) bool {
	if len(value) < 1 || len(value) > 253 || value[0] < 'a' || value[0] > 'z' && (value[0] < '0' || value[0] > '9') {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	last := value[len(value)-1]
	return last >= 'a' && last <= 'z' || last >= '0' && last <= '9'
}

type kubernetesInstanceBinding struct {
	instance model.DatabaseInstance
	binding  model.WorkloadBinding
	ref      model.KubernetesWorkloadRef
	podName  string
}

type kubernetesServiceResource struct {
	haEndpoint model.HAEndpoint
	endpoint   model.Endpoint
	ref        kubernetesServiceRef
	target     model.RuntimeTarget
	api        kubernetes.API
	bindings   map[model.ResourceID]kubernetesInstanceBinding
}

func (provider *KubernetesServiceProvider) resource(ctx context.Context, clusterID model.ResourceID, instances []model.DatabaseInstance) (kubernetesServiceResource, error) {
	if !provider.Executable(ctx) {
		return kubernetesServiceResource{}, fmt.Errorf("Kubernetes Service endpoint provider is not configured")
	}
	var result kubernetesServiceResource
	found := 0
	for _, candidate := range provider.inventory.HAEndpoints(clusterID) {
		endpointValue, exists := provider.inventory.Endpoint(candidate.EndpointID)
		if !exists || !endpointValue.Active || candidate.Provider != model.EndpointProviderKubernetesService ||
			candidate.Kind != model.EndpointService || endpointValue.Kind != model.EndpointService {
			continue
		}
		found++
		result.haEndpoint = candidate
		result.endpoint = endpointValue
	}
	if found != 1 || strings.TrimSpace(result.endpoint.Hostname) == "" || result.endpoint.Port < 1 || result.endpoint.Port > 65535 {
		return kubernetesServiceResource{}, fmt.Errorf("exactly one complete active Kubernetes Service endpoint is required")
	}
	parsed, err := parseKubernetesServiceRef(result.haEndpoint.ProviderRef)
	if err != nil {
		return kubernetesServiceResource{}, err
	}
	result.ref = parsed
	result.bindings = make(map[model.ResourceID]kubernetesInstanceBinding, len(instances))
	statefulSets := make(map[string]model.ResourceID, len(instances))
	podUIDs := make(map[string]model.ResourceID, len(instances))
	pvcUIDs := make(map[string]model.ResourceID, len(instances))
	var runtimeTargetID model.ResourceID
	for _, instance := range instances {
		if instance.ClusterID != clusterID || instance.Engine != model.EngineMySQL || !model.ValidResourceID(instance.ResourceID) {
			return kubernetesServiceResource{}, fmt.Errorf("Kubernetes workload topology contains an invalid instance")
		}
		binding, exists := provider.inventory.WorkloadBindingForInstance(instance.ResourceID)
		if !exists || !binding.Active || binding.RuntimeKind != model.RuntimeKubernetes || binding.Kubernetes == nil {
			return kubernetesServiceResource{}, fmt.Errorf("instance %s has no active Kubernetes workload binding", instance.ResourceID)
		}
		if binding.Kubernetes.Namespace != parsed.namespace || binding.Kubernetes.Ordinal != 0 ||
			strings.TrimSpace(binding.Kubernetes.PodName) != binding.Kubernetes.StatefulSet+"-0" ||
			strings.TrimSpace(binding.Kubernetes.PodUID) == "" || strings.TrimSpace(binding.Kubernetes.PVCUID) == "" {
			return kubernetesServiceResource{}, fmt.Errorf("instance %s Kubernetes ordinal-zero Pod or immutable storage identity is not registered", instance.ResourceID)
		}
		if runtimeTargetID == "" {
			runtimeTargetID = binding.RuntimeTargetID
		} else if runtimeTargetID != binding.RuntimeTargetID {
			return kubernetesServiceResource{}, fmt.Errorf("cluster instances span multiple Kubernetes runtime targets")
		}
		podName := strings.TrimSpace(binding.Kubernetes.PodName)
		statefulKey := binding.Kubernetes.Namespace + "/" + binding.Kubernetes.StatefulSet
		if owner, duplicate := statefulSets[statefulKey]; duplicate && owner != instance.ResourceID {
			return kubernetesServiceResource{}, fmt.Errorf("Kubernetes StatefulSet is bound to multiple database instances")
		}
		if owner, duplicate := podUIDs[binding.Kubernetes.PodUID]; duplicate && owner != instance.ResourceID {
			return kubernetesServiceResource{}, fmt.Errorf("Kubernetes Pod UID is bound to multiple database instances")
		}
		if owner, duplicate := pvcUIDs[binding.Kubernetes.PVCUID]; duplicate && owner != instance.ResourceID {
			return kubernetesServiceResource{}, fmt.Errorf("Kubernetes PVC UID is bound to multiple database instances")
		}
		statefulSets[statefulKey] = instance.ResourceID
		podUIDs[binding.Kubernetes.PodUID] = instance.ResourceID
		pvcUIDs[binding.Kubernetes.PVCUID] = instance.ResourceID
		result.bindings[instance.ResourceID] = kubernetesInstanceBinding{
			instance: instance, binding: binding, ref: *binding.Kubernetes, podName: podName,
		}
	}
	if len(result.bindings) == 0 {
		return kubernetesServiceResource{}, fmt.Errorf("Kubernetes endpoint has no workload bindings")
	}
	target, exists := provider.inventory.RuntimeTarget(runtimeTargetID)
	if !exists || !target.Active || target.Kind != model.RuntimeKubernetes {
		return kubernetesServiceResource{}, fmt.Errorf("Kubernetes runtime target is missing or inactive")
	}
	client, err := provider.factory.ForTarget(ctx, target)
	if err != nil {
		return kubernetesServiceResource{}, fmt.Errorf("create Kubernetes API client: %w", err)
	}
	result.target = target
	result.api = client
	return result, nil
}

func boolValue(value bool) *bool { return &value }

func podReady(pod kubernetes.Pod) bool {
	if pod.Metadata.DeletionTimestamp != nil || pod.Status.Phase != "Running" || net.ParseIP(strings.TrimSpace(pod.Status.PodIP)) == nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func controllerStatefulSet(pod kubernetes.Pod, name string) bool {
	for _, owner := range pod.Metadata.OwnerReferences {
		if owner.Kind == "StatefulSet" && owner.Name == name && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func (provider *KubernetesServiceProvider) validatedPod(ctx context.Context, resource kubernetesServiceResource, instanceID model.ResourceID, expectedRole string) (kubernetes.Pod, error) {
	binding, exists := resource.bindings[instanceID]
	if !exists {
		return kubernetes.Pod{}, fmt.Errorf("instance has no Kubernetes workload binding")
	}
	pod, err := resource.api.GetPod(ctx, resource.ref.namespace, binding.podName)
	if err != nil {
		return kubernetes.Pod{}, fmt.Errorf("read Kubernetes Pod %s: %w", binding.podName, err)
	}
	if pod.Metadata.Namespace != resource.ref.namespace || pod.Metadata.Name != binding.podName || pod.Metadata.UID != binding.ref.PodUID {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes Pod identity does not match the registered immutable UID")
	}
	if binding.ref.NodeName != "" && pod.Spec.NodeName != binding.ref.NodeName {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes Pod node identity changed without metadata reconciliation")
	}
	if !controllerStatefulSet(pod, binding.ref.StatefulSet) {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes Pod is not controlled by the registered StatefulSet")
	}
	if !podReady(pod) {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes Pod is not Running, Ready, and addressable")
	}
	if err := kubernetes.VerifyPodPVCIdentity(ctx, resource.api, pod, binding.ref.PVCUID); err != nil {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes data volume identity does not match the registered workload: %w", err)
	}
	stateful, err := resource.api.GetStatefulSet(ctx, resource.ref.namespace, binding.ref.StatefulSet)
	if err != nil {
		return kubernetes.Pod{}, fmt.Errorf("read Kubernetes StatefulSet role contract: %w", err)
	}
	if stateful.Metadata.Annotations[kubernetesFenceGuardKey] != "enabled" || strings.EqualFold(stateful.Metadata.Annotations[kubernetesFencedKey], "true") {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes StatefulSet is not protected by an active fail-closed start guard")
	}
	if expectedRole != "" && stateful.Metadata.Annotations[kubernetesMySQLRoleKey] != expectedRole {
		return kubernetes.Pod{}, fmt.Errorf("Kubernetes StatefulSet durable MySQL role is not %s", expectedRole)
	}
	return pod, nil
}

func validateKubernetesService(resource kubernetesServiceResource, service kubernetes.Service, slice kubernetes.EndpointSlice) error {
	if service.Metadata.Namespace != resource.ref.namespace || service.Metadata.Name != resource.ref.service {
		return fmt.Errorf("Kubernetes Service identity does not match provider_ref")
	}
	if len(service.Spec.Selector) != 0 {
		return fmt.Errorf("Kubernetes writer Service must be selectorless")
	}
	portFound := false
	for _, port := range service.Spec.Ports {
		if int(port.Port) == resource.endpoint.Port {
			portFound = true
			break
		}
	}
	if !portFound {
		return fmt.Errorf("Kubernetes writer Service does not expose the registered database port")
	}
	if slice.Metadata.Namespace != resource.ref.namespace || slice.Metadata.Name != resource.ref.endpointSlice ||
		slice.Metadata.Labels[kubernetesServiceLabel] != resource.ref.service ||
		slice.Metadata.Labels[kubernetesManagedLabel] != kubernetesManagedBy || slice.AddressType != "IPv4" {
		return fmt.Errorf("Kubernetes EndpointSlice is not an IPv4 ClusterGuard-managed slice for the writer Service")
	}
	portFound = false
	for _, port := range slice.Ports {
		if port.Port != nil && int(*port.Port) == resource.endpoint.Port {
			portFound = true
			break
		}
	}
	if !portFound {
		return fmt.Errorf("Kubernetes EndpointSlice does not expose the registered database port")
	}
	return nil
}

func (provider *KubernetesServiceProvider) serviceState(ctx context.Context, resource kubernetesServiceResource) (kubernetes.Service, kubernetes.EndpointSlice, error) {
	service, err := resource.api.GetService(ctx, resource.ref.namespace, resource.ref.service)
	if err != nil {
		return kubernetes.Service{}, kubernetes.EndpointSlice{}, fmt.Errorf("read Kubernetes writer Service: %w", err)
	}
	slice, err := resource.api.GetEndpointSlice(ctx, resource.ref.namespace, resource.ref.endpointSlice)
	if err != nil {
		return kubernetes.Service{}, kubernetes.EndpointSlice{}, fmt.Errorf("read Kubernetes writer EndpointSlice: %w", err)
	}
	if err := validateKubernetesService(resource, service, slice); err != nil {
		return kubernetes.Service{}, kubernetes.EndpointSlice{}, err
	}
	return service, slice, nil
}

func endpointAvailable(value kubernetes.DiscoveryEndpoint) bool {
	return len(value.Addresses) == 1 && net.ParseIP(strings.TrimSpace(value.Addresses[0])) != nil &&
		(value.Conditions.Ready == nil || *value.Conditions.Ready) &&
		(value.Conditions.Serving == nil || *value.Conditions.Serving) &&
		(value.Conditions.Terminating == nil || !*value.Conditions.Terminating)
}

func ownerFromSlice(resource kubernetesServiceResource, slice kubernetes.EndpointSlice) (model.ResourceID, error) {
	if len(slice.Endpoints) != 1 || !endpointAvailable(slice.Endpoints[0]) || slice.Endpoints[0].TargetRef == nil {
		return "", fmt.Errorf("Kubernetes writer EndpointSlice must contain one ready Pod endpoint")
	}
	target := slice.Endpoints[0].TargetRef
	if target.Kind != "Pod" || target.Namespace != resource.ref.namespace || strings.TrimSpace(target.UID) == "" {
		return "", fmt.Errorf("Kubernetes writer EndpointSlice targetRef is incomplete")
	}
	var owner model.ResourceID
	for instanceID, binding := range resource.bindings {
		if target.Name == binding.podName && target.UID == binding.ref.PodUID {
			if owner != "" {
				return "", fmt.Errorf("Kubernetes writer endpoint maps to duplicate workload identities")
			}
			owner = instanceID
		}
	}
	if owner == "" {
		return "", fmt.Errorf("Kubernetes writer endpoint targets an unregistered Pod identity")
	}
	return owner, nil
}

func (provider *KubernetesServiceProvider) ObserveOwnership(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (OwnershipObservation, error) {
	if cluster.ResourceID == "" || cluster.Engine != model.EngineMySQL || snapshot.ClusterID != cluster.ResourceID {
		return OwnershipObservation{}, fmt.Errorf("Kubernetes ownership observation cluster scope is invalid")
	}
	resource, err := provider.resource(ctx, cluster.ResourceID, snapshot.Instances)
	if err != nil {
		return OwnershipObservation{}, err
	}
	_, slice, err := provider.serviceState(ctx, resource)
	if err != nil {
		return OwnershipObservation{}, err
	}
	owner, err := ownerFromSlice(resource, slice)
	if err != nil {
		return OwnershipObservation{}, err
	}
	observed := make([]model.ResourceID, 0, len(resource.bindings))
	for instanceID := range resource.bindings {
		observed = append(observed, instanceID)
	}
	return OwnershipObservation{
		HAEndpointID: resource.haEndpoint.ResourceID, HAEndpointRevision: resource.haEndpoint.MetadataRevision,
		EndpointRevision: resource.endpoint.MetadataRevision, CanonicalOwnerID: resource.haEndpoint.OwnerID,
		EndpointOwnerID: resource.endpoint.InstanceID, OwnerIDs: []model.ResourceID{owner},
		ObservedInstanceIDs: observed, Complete: true,
	}, nil
}

func (provider *KubernetesServiceProvider) Precheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	fail := func(message string) []model.Check {
		return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: message}}
	}
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return fail(err.Error())
	}
	_, slice, err := provider.serviceState(ctx, resource)
	if err != nil {
		return fail(err.Error())
	}
	owner, err := ownerFromSlice(resource, slice)
	if err != nil {
		return fail(err.Error())
	}
	if owner != resolved.Primary.ResourceID || resource.haEndpoint.OwnerID != resolved.Primary.ResourceID || resource.endpoint.InstanceID != resolved.Primary.ResourceID {
		return fail("Kubernetes Service physical and canonical ownership must identify only the current primary")
	}
	if _, err := provider.validatedPod(ctx, resource, resolved.Target.ResourceID, "replica"); err != nil {
		return fail("candidate Pod validation failed: " + err.Error())
	}
	if resolved.Primary.Health.State == model.HealthHealthy {
		if _, err := provider.validatedPod(ctx, resource, resolved.Primary.ResourceID, "primary"); err != nil {
			return fail("current-primary Pod validation failed: " + err.Error())
		}
	}
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckPass, Message: "selectorless Kubernetes Service has one registered current-primary Pod endpoint and a verified candidate Pod"}}
}

func (provider *KubernetesServiceProvider) AuthorizeTransition(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return adapter.TransitionAuthorization{}, err
	}
	return authorizeTransitionLease(ctx, provider.leases, resolved, resource.haEndpoint.ResourceID, provider.transitionLeaseTTL, provider.transitionRenewInterval)
}

func (provider *KubernetesServiceProvider) AuthorizeStableOwner(ctx context.Context, resolved adapter.ResolvedOperation) (adapter.StableOwnershipAuthorization, error) {
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return adapter.StableOwnershipAuthorization{}, err
	}
	if resource.haEndpoint.OwnerID != resolved.Primary.ResourceID || resource.endpoint.InstanceID != resolved.Primary.ResourceID {
		return adapter.StableOwnershipAuthorization{}, fmt.Errorf("canonical Kubernetes Service ownership does not identify the current primary")
	}
	return authorizeStableLease(ctx, provider.leases, resolved, resource.haEndpoint.ResourceID, provider.transitionLeaseTTL, provider.transitionRenewInterval)
}

func (provider *KubernetesServiceProvider) Transfer(ctx context.Context, resolved adapter.ResolvedOperation) error {
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return err
	}
	lease, err := provider.leases.Acquire(ctx, LeaseRequest{
		ClusterID: resolved.Cluster.ResourceID, HAEndpointID: resource.haEndpoint.ResourceID,
		OperationID: resolved.OperationID, OwnerID: resolved.Target.ResourceID,
		PreviousOwnerID: resolved.Primary.ResourceID, TTL: provider.transitionLeaseTTL,
	})
	if err != nil {
		return fmt.Errorf("acquire Kubernetes endpoint transition lease: %w", err)
	}
	if err := provider.leases.Validate(ctx, lease); err != nil {
		return fmt.Errorf("validate Kubernetes endpoint transition lease: %w", err)
	}
	pod, err := provider.validatedPod(ctx, resource, resolved.Target.ResourceID, "replica")
	if err != nil {
		return err
	}
	_, slice, err := provider.serviceState(ctx, resource)
	if err != nil {
		return err
	}
	if slice.Metadata.ResourceVersion == "" {
		return fmt.Errorf("Kubernetes EndpointSlice resourceVersion is missing")
	}
	binding := resource.bindings[resolved.Target.ResourceID]
	sourceBinding := resource.bindings[resolved.Primary.ResourceID]
	if _, err := resource.api.PatchStatefulSetAnnotations(ctx, resource.ref.namespace, sourceBinding.ref.StatefulSet, map[string]string{kubernetesMySQLRoleKey: "replica"}); err != nil {
		return fmt.Errorf("persist old-primary Kubernetes restart role: %w", err)
	}
	updatedTarget, err := resource.api.PatchStatefulSetAnnotations(ctx, resource.ref.namespace, binding.ref.StatefulSet, map[string]string{kubernetesMySQLRoleKey: "primary", kubernetesFencedKey: "false"})
	if err != nil {
		return fmt.Errorf("persist target Kubernetes restart role: %w", err)
	}
	if updatedTarget.Metadata.Annotations[kubernetesMySQLRoleKey] != "primary" || updatedTarget.Metadata.Annotations[kubernetesFencedKey] != "false" {
		return fmt.Errorf("target Kubernetes restart role did not persist")
	}
	ready, serving, terminating := true, true, false
	nodeName := pod.Spec.NodeName
	slice.Endpoints = []kubernetes.DiscoveryEndpoint{{
		Addresses: []string{pod.Status.PodIP}, NodeName: &nodeName,
		Conditions: kubernetes.EndpointConditions{Ready: &ready, Serving: &serving, Terminating: &terminating},
		TargetRef:  &kubernetes.ObjectReference{Kind: "Pod", Namespace: resource.ref.namespace, Name: binding.podName, UID: binding.ref.PodUID},
	}}
	if slice.Metadata.Annotations == nil {
		slice.Metadata.Annotations = make(map[string]string)
	}
	slice.Metadata.Annotations[kubernetesOwnerLabel] = string(resolved.Target.ResourceID)
	slice.Metadata.Annotations[kubernetesOperationKey] = string(resolved.OperationID)
	slice.Metadata.Annotations[kubernetesLeaseKey] = string(lease.ResourceID)
	if _, err := resource.api.UpdateEndpointSlice(ctx, slice); err != nil {
		return fmt.Errorf("update Kubernetes writer EndpointSlice: %w", err)
	}
	_, updated, err := provider.serviceState(ctx, resource)
	if err != nil {
		return err
	}
	owner, err := ownerFromSlice(resource, updated)
	if err != nil || owner != resolved.Target.ResourceID {
		return fmt.Errorf("Kubernetes writer endpoint did not converge to the target Pod")
	}
	if err := provider.inventory.CommitHAEndpointOwner(resolved.Cluster.ResourceID, resource.haEndpoint.ResourceID, resolved.Target.ResourceID, true); err != nil {
		return fmt.Errorf("commit Kubernetes Service ownership metadata: %w", err)
	}
	return nil
}

func (provider *KubernetesServiceProvider) Verify(ctx context.Context, resolved adapter.ResolvedOperation) model.Check {
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	_, slice, err := provider.serviceState(ctx, resource)
	if err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: err.Error()}
	}
	owner, err := ownerFromSlice(resource, slice)
	if err != nil || owner != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "Kubernetes Service does not target only the operation target Pod"}
	}
	if resource.haEndpoint.OwnerID != resolved.Target.ResourceID || resource.endpoint.InstanceID != resolved.Target.ResourceID {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "Kubernetes Service ownership is correct but canonical metadata is stale"}
	}
	if _, err := provider.validatedPod(ctx, resource, resolved.Target.ResourceID, "primary"); err != nil {
		return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "target durable runtime role is not verified: " + err.Error()}
	}
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckPass, Message: "Kubernetes Service and canonical ownership match only the operation target"}
}

func (provider *KubernetesServiceProvider) FormerPrimaryPrecheck(ctx context.Context, resolved adapter.ResolvedOperation) []model.Check {
	fail := func(message string) []model.Check {
		return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckFail, Message: message}}
	}
	resource, err := provider.resource(ctx, resolved.Cluster.ResourceID, resolved.Snapshot.Instances)
	if err != nil {
		return fail(err.Error())
	}
	_, slice, err := provider.serviceState(ctx, resource)
	if err != nil {
		return fail(err.Error())
	}
	owner, err := ownerFromSlice(resource, slice)
	if err != nil || owner != resolved.Primary.ResourceID || owner == resolved.Target.ResourceID {
		return fail("Kubernetes Service does not target only the current primary")
	}
	if resource.haEndpoint.OwnerID != owner || resource.endpoint.InstanceID != owner {
		return fail("canonical Kubernetes Service ownership is stale")
	}
	authorization, err := authorizeStableLease(ctx, provider.leases, resolved, resource.haEndpoint.ResourceID, provider.transitionLeaseTTL, provider.transitionRenewInterval)
	if err != nil {
		return fail(err.Error())
	}
	authorization.Cancel()
	return []model.Check{{Name: "former_primary_endpoint_absent", Status: model.CheckPass, Message: "former primary is absent from the single Kubernetes writer endpoint and stable ownership remains majority-backed"}}
}
