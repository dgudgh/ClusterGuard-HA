package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/pkg/model"
)

type runtimeKubernetesFactory struct{ api kubernetes.API }

func (factory runtimeKubernetesFactory) ForTarget(context.Context, model.RuntimeTarget) (kubernetes.API, error) {
	return factory.api, nil
}

type runtimeKubernetesInventory struct {
	binding model.WorkloadBinding
	target  model.RuntimeTarget
}

func (inventory runtimeKubernetesInventory) WorkloadBindingForInstance(instanceID model.ResourceID) (model.WorkloadBinding, bool) {
	return inventory.binding, inventory.binding.InstanceID == instanceID
}
func (inventory runtimeKubernetesInventory) RuntimeTarget(resourceID model.ResourceID) (model.RuntimeTarget, bool) {
	return inventory.target, inventory.target.ResourceID == resourceID
}

type fencerKubernetesAPI struct {
	stateful  kubernetes.StatefulSet
	scale     kubernetes.Scale
	pod       kubernetes.Pod
	pvc       kubernetes.PersistentVolumeClaim
	node      kubernetes.Node
	podExists bool
	patches   int
	scales    int
}

func (*fencerKubernetesAPI) GetService(context.Context, string, string) (kubernetes.Service, error) {
	return kubernetes.Service{}, errors.New("unused")
}
func (*fencerKubernetesAPI) GetEndpointSlice(context.Context, string, string) (kubernetes.EndpointSlice, error) {
	return kubernetes.EndpointSlice{}, errors.New("unused")
}
func (*fencerKubernetesAPI) UpdateEndpointSlice(context.Context, kubernetes.EndpointSlice) (kubernetes.EndpointSlice, error) {
	return kubernetes.EndpointSlice{}, errors.New("unused")
}
func (api *fencerKubernetesAPI) GetPod(context.Context, string, string) (kubernetes.Pod, error) {
	if !api.podExists {
		return kubernetes.Pod{}, kubernetes.ErrNotFound
	}
	return api.pod, nil
}
func (api *fencerKubernetesAPI) GetPersistentVolumeClaim(context.Context, string, string) (kubernetes.PersistentVolumeClaim, error) {
	return api.pvc, nil
}
func (api *fencerKubernetesAPI) GetNode(context.Context, string) (kubernetes.Node, error) {
	return api.node, nil
}
func (api *fencerKubernetesAPI) GetStatefulSet(context.Context, string, string) (kubernetes.StatefulSet, error) {
	return api.stateful, nil
}
func (api *fencerKubernetesAPI) PatchStatefulSetAnnotations(_ context.Context, _, _ string, annotations map[string]string) (kubernetes.StatefulSet, error) {
	if api.stateful.Metadata.Annotations == nil {
		api.stateful.Metadata.Annotations = make(map[string]string)
	}
	for key, value := range annotations {
		api.stateful.Metadata.Annotations[key] = value
	}
	api.patches++
	return api.stateful, nil
}
func (api *fencerKubernetesAPI) GetStatefulSetScale(context.Context, string, string) (kubernetes.Scale, error) {
	return api.scale, nil
}
func (api *fencerKubernetesAPI) UpdateStatefulSetScale(_ context.Context, value kubernetes.Scale) (kubernetes.Scale, error) {
	api.scale = value
	api.scale.Status.Replicas = value.Spec.Replicas
	api.podExists = value.Spec.Replicas != 0
	api.scales++
	return api.scale, nil
}

func kubernetesFencerFixture(t *testing.T) (*kubernetesWorkloadFencer, coordination.ExternalFenceRequest, *fencerKubernetesAPI) {
	t.Helper()
	instanceID, runtimeID := model.NewResourceID(), model.NewResourceID()
	binding := model.WorkloadBinding{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, InstanceID: instanceID,
		RuntimeTargetID: runtimeID, RuntimeKind: model.RuntimeKubernetes, Active: true,
		Kubernetes: &model.KubernetesWorkloadRef{ClusterName: "lab", Namespace: "database", StatefulSet: "mysql-a", Ordinal: 0, PodName: "mysql-a-0", PodUID: "pod-a", NodeName: "worker-a", PVCUID: "pvc-a"},
	}
	inventory := runtimeKubernetesInventory{binding: binding, target: model.RuntimeTarget{ResourceMeta: model.ResourceMeta{ResourceID: runtimeID}, Kind: model.RuntimeKubernetes, Active: true}}
	replicas := int32(1)
	api := &fencerKubernetesAPI{podExists: true}
	api.stateful.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-a", Annotations: map[string]string{kubernetesFenceGuardAnnotation: "enabled", "clusterguard.io/mysql-role": "primary"}}
	api.stateful.Spec.Replicas = &replicas
	api.scale.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-a", ResourceVersion: "9"}
	api.scale.Spec.Replicas, api.scale.Status.Replicas = 1, 1
	api.pod.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-a-0", UID: "pod-a"}
	api.pod.Spec.NodeName = "worker-a"
	api.pod.Spec.Volumes = []kubernetes.Volume{{Name: "mysql-data", PersistentVolumeClaim: &kubernetes.PersistentVolumeClaimVolumeSource{ClaimName: "mysql-a-data"}}}
	api.pvc.Metadata = kubernetes.ObjectMeta{Namespace: "database", Name: "mysql-a-data", UID: "pvc-a"}
	api.node.Metadata = kubernetes.ObjectMeta{Name: "worker-a"}
	api.node.Status.Conditions = []kubernetes.NodeCondition{{Type: "Ready", Status: "True"}}
	fencer := newKubernetesWorkloadFencer(inventory, runtimeKubernetesFactory{api: api}, time.Second)
	fencer.interval = time.Millisecond
	request := coordination.ExternalFenceRequest{ClusterID: model.NewResourceID(), OperationID: model.NewResourceID(), Instance: model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: instanceID}}}
	return fencer, request, api
}

func TestKubernetesWorkloadFencerPersistsFenceAndWaitsForPodDeletion(t *testing.T) {
	fencer, request, api := kubernetesFencerFixture(t)
	if err := fencer.Fence(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if api.patches != 1 || api.scales != 1 || api.podExists || api.scale.Spec.Replicas != 0 {
		t.Fatalf("fence did not converge: patches=%d scales=%d pod=%t scale=%+v", api.patches, api.scales, api.podExists, api.scale)
	}
	fenced, err := fencer.Status(context.Background(), request)
	if err != nil || !fenced {
		t.Fatalf("fence status=%t err=%v", fenced, err)
	}
}

func TestKubernetesWorkloadFencerBlocksWhenNodeIsNotReady(t *testing.T) {
	fencer, request, api := kubernetesFencerFixture(t)
	api.node.Status.Conditions[0].Status = "False"
	if err := fencer.Fence(context.Background(), request); err == nil {
		t.Fatal("NotReady node was accepted without external node fencing")
	}
	if api.patches != 0 || api.scales != 0 {
		t.Fatalf("unsafe fence mutated workload: patches=%d scales=%d", api.patches, api.scales)
	}
}

func TestRuntimeFencerRouterDoesNotRouteDockerToKubernetes(t *testing.T) {
	instanceID, runtimeID := model.NewResourceID(), model.NewResourceID()
	inventory := runtimeKubernetesInventory{
		binding: model.WorkloadBinding{InstanceID: instanceID, RuntimeTargetID: runtimeID, RuntimeKind: model.RuntimeDocker, Active: true},
		target:  model.RuntimeTarget{ResourceMeta: model.ResourceMeta{ResourceID: runtimeID}, Kind: model.RuntimeDocker, Active: true},
	}
	router := runtimeFencerRouter{inventory: inventory, kubernetes: runtimeExternalFencer{}}
	if router.Available(instanceID) {
		t.Fatal("Docker workload was incorrectly routed to the Kubernetes fencer")
	}
	router.fallback = runtimeExternalFencer{}
	if !router.Available(instanceID) {
		t.Fatal("Docker workload did not use the configured host-level fallback fencer")
	}
}
