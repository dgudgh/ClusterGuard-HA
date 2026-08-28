package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type staticKubernetesFactory struct{ api kubernetes.API }

func (factory staticKubernetesFactory) ForTarget(context.Context, model.RuntimeTarget) (kubernetes.API, error) {
	return factory.api, nil
}

type fakeKubernetesAPI struct {
	service kubernetes.Service
	slice   kubernetes.EndpointSlice
	pods    map[string]kubernetes.Pod
	pvcs    map[string]kubernetes.PersistentVolumeClaim
	sets    map[string]kubernetes.StatefulSet
	updates int
}

func (api *fakeKubernetesAPI) GetService(context.Context, string, string) (kubernetes.Service, error) {
	return api.service, nil
}
func (api *fakeKubernetesAPI) GetEndpointSlice(context.Context, string, string) (kubernetes.EndpointSlice, error) {
	return api.slice, nil
}
func (api *fakeKubernetesAPI) UpdateEndpointSlice(_ context.Context, value kubernetes.EndpointSlice) (kubernetes.EndpointSlice, error) {
	api.slice = value
	api.updates++
	return value, nil
}
func (api *fakeKubernetesAPI) GetPod(_ context.Context, _, name string) (kubernetes.Pod, error) {
	value, found := api.pods[name]
	if !found {
		return kubernetes.Pod{}, kubernetes.ErrNotFound
	}
	return value, nil
}
func (api *fakeKubernetesAPI) GetPersistentVolumeClaim(_ context.Context, _, name string) (kubernetes.PersistentVolumeClaim, error) {
	value, found := api.pvcs[name]
	if !found {
		return kubernetes.PersistentVolumeClaim{}, kubernetes.ErrNotFound
	}
	return value, nil
}
func (*fakeKubernetesAPI) GetNode(context.Context, string) (kubernetes.Node, error) {
	return kubernetes.Node{}, errors.New("unused")
}
func (api *fakeKubernetesAPI) GetStatefulSet(_ context.Context, _, name string) (kubernetes.StatefulSet, error) {
	value, found := api.sets[name]
	if !found {
		return kubernetes.StatefulSet{}, kubernetes.ErrNotFound
	}
	return value, nil
}
func (api *fakeKubernetesAPI) PatchStatefulSetAnnotations(_ context.Context, _, name string, annotations map[string]string) (kubernetes.StatefulSet, error) {
	value, found := api.sets[name]
	if !found {
		return kubernetes.StatefulSet{}, kubernetes.ErrNotFound
	}
	if value.Metadata.Annotations == nil {
		value.Metadata.Annotations = make(map[string]string)
	}
	for key, annotation := range annotations {
		value.Metadata.Annotations[key] = annotation
	}
	api.sets[name] = value
	return value, nil
}
func (*fakeKubernetesAPI) GetStatefulSetScale(context.Context, string, string) (kubernetes.Scale, error) {
	return kubernetes.Scale{}, errors.New("unused")
}
func (*fakeKubernetesAPI) UpdateStatefulSetScale(context.Context, kubernetes.Scale) (kubernetes.Scale, error) {
	return kubernetes.Scale{}, errors.New("unused")
}

type kubernetesInventoryStub struct {
	*vipInventoryStub
	bindings map[model.ResourceID]model.WorkloadBinding
	targets  map[model.ResourceID]model.RuntimeTarget
}

func (inventory *kubernetesInventoryStub) WorkloadBindingForInstance(instanceID model.ResourceID) (model.WorkloadBinding, bool) {
	value, found := inventory.bindings[instanceID]
	return value, found
}
func (inventory *kubernetesInventoryStub) RuntimeTarget(resourceID model.ResourceID) (model.RuntimeTarget, bool) {
	value, found := inventory.targets[resourceID]
	return value, found
}

func readyKubernetesPod(namespace, statefulSet, name, uid, ip, node string) kubernetes.Pod {
	controller := true
	pod := kubernetes.Pod{Metadata: kubernetes.ObjectMeta{
		Namespace: namespace, Name: name, UID: uid,
		OwnerReferences: []kubernetes.OwnerReference{{Kind: "StatefulSet", Name: statefulSet, Controller: &controller}},
	}}
	pod.Spec.NodeName = node
	pod.Spec.Volumes = []kubernetes.Volume{{Name: "mysql-data", PersistentVolumeClaim: &kubernetes.PersistentVolumeClaimVolumeSource{ClaimName: name + "-data"}}}
	pod.Status.Phase = "Running"
	pod.Status.PodIP = ip
	pod.Status.Conditions = []kubernetes.PodCondition{{Type: "Ready", Status: "True"}}
	return pod
}

func kubernetesProviderFixture(t *testing.T) (*KubernetesServiceProvider, adapter.ResolvedOperation, *fakeKubernetesAPI, *kubernetesInventoryStub, *MemoryLeaseStore) {
	t.Helper()
	clusterID, primaryID, targetID := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	haID, endpointID, runtimeID := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	instances := []model.DatabaseInstance{
		{ResourceMeta: model.ResourceMeta{ResourceID: primaryID}, ClusterID: clusterID, Engine: model.EngineMySQL, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}},
		{ResourceMeta: model.ResourceMeta{ResourceID: targetID}, ClusterID: clusterID, Engine: model.EngineMySQL, Role: model.RoleReplica, Health: model.Health{State: model.HealthHealthy}},
	}
	base := &vipInventoryStub{
		resources: []model.HAEndpoint{{ResourceMeta: model.ResourceMeta{ResourceID: haID, MetadataRevision: 3}, ClusterID: clusterID, EndpointID: endpointID, Kind: model.EndpointService, DesiredRole: model.RolePrimary, OwnerID: primaryID, Provider: model.EndpointProviderKubernetesService, ProviderRef: "database/mysql-writer/mysql-writer-cg"}},
		endpoints: map[model.ResourceID]model.Endpoint{endpointID: {ResourceMeta: model.ResourceMeta{ResourceID: endpointID, MetadataRevision: 5}, ClusterID: clusterID, InstanceID: primaryID, Kind: model.EndpointService, Hostname: "mysql-writer.database.svc", Port: 3306, Active: true}},
	}
	inventory := &kubernetesInventoryStub{vipInventoryStub: base, bindings: map[model.ResourceID]model.WorkloadBinding{}, targets: map[model.ResourceID]model.RuntimeTarget{}}
	inventory.targets[runtimeID] = model.RuntimeTarget{ResourceMeta: model.ResourceMeta{ResourceID: runtimeID}, Kind: model.RuntimeKubernetes, Active: true}
	for index, instance := range instances {
		podName := "mysql-" + string(rune('a'+index)) + "-0"
		statefulSet := "mysql-" + string(rune('a'+index))
		uid := "pod-uid-" + string(rune('a'+index))
		inventory.bindings[instance.ResourceID] = model.WorkloadBinding{
			ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, InstanceID: instance.ResourceID,
			RuntimeTargetID: runtimeID, RuntimeKind: model.RuntimeKubernetes, Active: true,
			Kubernetes: &model.KubernetesWorkloadRef{ClusterName: "lab", Namespace: "database", StatefulSet: statefulSet, Ordinal: 0, PodName: podName, PodUID: uid, NodeName: "worker-" + string(rune('a'+index)), PVCUID: "pvc-uid-" + string(rune('a'+index))},
		}
	}
	api := &fakeKubernetesAPI{pods: map[string]kubernetes.Pod{}, pvcs: map[string]kubernetes.PersistentVolumeClaim{}, sets: map[string]kubernetes.StatefulSet{}}
	api.service.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-writer"}
	api.service.Spec.Ports = []kubernetes.ServicePort{{Port: 3306}}
	port := int32(3306)
	api.slice.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-writer-cg", ResourceVersion: "7", Labels: map[string]string{kubernetesServiceLabel: "mysql-writer", kubernetesManagedLabel: kubernetesManagedBy}}
	api.slice.AddressType = "IPv4"
	api.slice.Ports = []kubernetes.EndpointPort{{Port: &port}}
	for index, instance := range instances {
		binding := inventory.bindings[instance.ResourceID]
		api.pods[binding.Kubernetes.PodName] = readyKubernetesPod("database", binding.Kubernetes.StatefulSet, binding.Kubernetes.PodName, binding.Kubernetes.PodUID, "10.20.0."+string(rune('1'+index)), binding.Kubernetes.NodeName)
		api.pvcs[binding.Kubernetes.PodName+"-data"] = kubernetes.PersistentVolumeClaim{Metadata: kubernetes.ObjectMeta{Namespace: "database", Name: binding.Kubernetes.PodName + "-data", UID: binding.Kubernetes.PVCUID}}
		role := "replica"
		if index == 0 {
			role = "primary"
		}
		api.sets[binding.Kubernetes.StatefulSet] = kubernetes.StatefulSet{Metadata: kubernetes.ObjectMeta{
			Namespace: "database", Name: binding.Kubernetes.StatefulSet,
			Annotations: map[string]string{kubernetesFenceGuardKey: "enabled", kubernetesFencedKey: "false", kubernetesMySQLRoleKey: role},
		}}
	}
	primaryBinding := inventory.bindings[primaryID].Kubernetes
	ready := true
	api.slice.Endpoints = []kubernetes.DiscoveryEndpoint{{
		Addresses: []string{api.pods[primaryBinding.PodName].Status.PodIP}, Conditions: kubernetes.EndpointConditions{Ready: &ready},
		TargetRef: &kubernetes.ObjectReference{Kind: "Pod", Namespace: "database", Name: primaryBinding.PodName, UID: primaryBinding.PodUID},
	}}
	now := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	leases := NewMemoryLeaseStore(func() time.Time { return now })
	provider := NewKubernetesServiceProvider(inventory, staticKubernetesFactory{api: api}, leases)
	resolved := adapter.ResolvedOperation{
		OperationID: model.NewResourceID(), Cluster: model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: clusterID}, Engine: model.EngineMySQL},
		Snapshot: model.TopologySnapshot{ClusterID: clusterID, Instances: instances, ObservedAt: now}, Primary: instances[0], Target: instances[1],
	}
	if _, err := leases.Acquire(context.Background(), LeaseRequest{ClusterID: clusterID, HAEndpointID: haID, OperationID: haID, OwnerID: primaryID, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	return provider, resolved, api, inventory, leases
}

func TestKubernetesServiceProviderTransfersSingleRegisteredPodEndpoint(t *testing.T) {
	provider, resolved, api, inventory, _ := kubernetesProviderFixture(t)
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckPass {
		t.Fatalf("precheck=%+v", checks)
	}
	authorization, err := provider.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Cancel()
	if err := provider.Transfer(authorization.Context, resolved); err != nil {
		t.Fatal(err)
	}
	if check := provider.Verify(authorization.Context, resolved); check.Status != model.CheckPass {
		t.Fatalf("verify=%+v", check)
	}
	if err := authorization.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.updates != 1 || len(api.slice.Endpoints) != 1 || api.slice.Endpoints[0].TargetRef.UID != inventory.bindings[resolved.Target.ResourceID].Kubernetes.PodUID {
		t.Fatalf("EndpointSlice did not converge: updates=%d slice=%+v", api.updates, api.slice)
	}
}

func TestKubernetesServiceProviderRejectsSelectorManagedService(t *testing.T) {
	provider, resolved, api, _, _ := kubernetesProviderFixture(t)
	api.service.Spec.Selector = map[string]string{"app": "mysql"}
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("selector-managed Service was accepted: %+v", checks)
	}
}

func TestKubernetesServiceProviderRejectsChangedPVCIdentity(t *testing.T) {
	provider, resolved, api, inventory, _ := kubernetesProviderFixture(t)
	binding := inventory.bindings[resolved.Target.ResourceID]
	claimName := binding.Kubernetes.PodName + "-data"
	claim := api.pvcs[claimName]
	claim.Metadata.UID = "replacement-pvc"
	api.pvcs[claimName] = claim
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("changed PVC identity was accepted: %+v", checks)
	}
}

func TestKubernetesServiceProviderRejectsDuplicateRegisteredWorkloadIdentity(t *testing.T) {
	provider, resolved, _, inventory, _ := kubernetesProviderFixture(t)
	primary := inventory.bindings[resolved.Primary.ResourceID]
	target := inventory.bindings[resolved.Target.ResourceID]
	target.Kubernetes.PVCUID = primary.Kubernetes.PVCUID
	inventory.bindings[resolved.Target.ResourceID] = target
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("duplicate Kubernetes PVC identity was accepted: %+v", checks)
	}
}
