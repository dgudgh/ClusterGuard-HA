package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/pkg/model"
)

const (
	kubernetesFenceGuardAnnotation     = "clusterguard.io/fence-guard"
	kubernetesFencedAnnotation         = "clusterguard.io/fenced"
	kubernetesFenceOperationAnnotation = "clusterguard.io/fence-operation-id"
	kubernetesFenceInstanceAnnotation  = "clusterguard.io/fence-instance-id"
)

type runtimeWorkloadInventory interface {
	WorkloadBindingForInstance(model.ResourceID) (model.WorkloadBinding, bool)
	RuntimeTarget(model.ResourceID) (model.RuntimeTarget, bool)
}

type kubernetesWorkloadFencer struct {
	inventory runtimeWorkloadInventory
	factory   kubernetes.Factory
	timeout   time.Duration
	interval  time.Duration
}

func newKubernetesWorkloadFencer(inventory runtimeWorkloadInventory, factory kubernetes.Factory, timeout time.Duration) *kubernetesWorkloadFencer {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &kubernetesWorkloadFencer{inventory: inventory, factory: factory, timeout: timeout, interval: 500 * time.Millisecond}
}

type kubernetesFenceResource struct {
	api      kubernetes.API
	binding  model.WorkloadBinding
	workload model.KubernetesWorkloadRef
	podName  string
	nodeName string
	stateful kubernetes.StatefulSet
}

func kubernetesNodeReady(node kubernetes.Node) bool {
	if node.Metadata.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Status == "True"
		}
	}
	return false
}

func (fencer *kubernetesWorkloadFencer) resource(ctx context.Context, request coordination.ExternalFenceRequest) (kubernetesFenceResource, error) {
	if fencer == nil || fencer.inventory == nil || fencer.factory == nil {
		return kubernetesFenceResource{}, fmt.Errorf("Kubernetes workload fencer is not configured")
	}
	binding, found := fencer.inventory.WorkloadBindingForInstance(request.Instance.ResourceID)
	if !found || !binding.Active || binding.RuntimeKind != model.RuntimeKubernetes || binding.Kubernetes == nil {
		return kubernetesFenceResource{}, fmt.Errorf("old primary has no active Kubernetes workload binding")
	}
	workload := *binding.Kubernetes
	if workload.Ordinal != 0 || strings.TrimSpace(workload.PodUID) == "" || strings.TrimSpace(workload.StatefulSet) == "" || strings.TrimSpace(workload.Namespace) == "" {
		return kubernetesFenceResource{}, fmt.Errorf("Kubernetes failover requires a dedicated one-replica StatefulSet at ordinal 0 with an immutable Pod UID")
	}
	target, found := fencer.inventory.RuntimeTarget(binding.RuntimeTargetID)
	if !found || !target.Active || target.Kind != model.RuntimeKubernetes {
		return kubernetesFenceResource{}, fmt.Errorf("Kubernetes runtime target is missing or inactive")
	}
	client, err := fencer.factory.ForTarget(ctx, target)
	if err != nil {
		return kubernetesFenceResource{}, fmt.Errorf("create Kubernetes API client: %w", err)
	}
	stateful, err := client.GetStatefulSet(ctx, workload.Namespace, workload.StatefulSet)
	if err != nil {
		return kubernetesFenceResource{}, fmt.Errorf("read old-primary StatefulSet: %w", err)
	}
	if stateful.Metadata.Namespace != workload.Namespace || stateful.Metadata.Name != workload.StatefulSet ||
		stateful.Metadata.Annotations[kubernetesFenceGuardAnnotation] != "enabled" {
		return kubernetesFenceResource{}, fmt.Errorf("old-primary StatefulSet has no enabled ClusterGuard fail-closed fence guard")
	}
	if !strings.EqualFold(stateful.Metadata.Annotations[kubernetesFencedAnnotation], "true") && stateful.Metadata.Annotations["clusterguard.io/mysql-role"] != "primary" {
		return kubernetesFenceResource{}, fmt.Errorf("old-primary StatefulSet durable MySQL role is not primary")
	}
	if stateful.Spec.Replicas != nil && *stateful.Spec.Replicas > 1 {
		return kubernetesFenceResource{}, fmt.Errorf("Kubernetes failover refuses a StatefulSet with more than one replica")
	}
	podName := strings.TrimSpace(workload.PodName)
	if podName == "" {
		podName = workload.StatefulSet + "-0"
	}
	nodeName := strings.TrimSpace(workload.NodeName)
	pod, podErr := client.GetPod(ctx, workload.Namespace, podName)
	if podErr == nil {
		if pod.Metadata.UID != workload.PodUID || pod.Metadata.Name != podName || pod.Metadata.Namespace != workload.Namespace {
			return kubernetesFenceResource{}, fmt.Errorf("old-primary Pod identity changed without metadata reconciliation")
		}
		if err := kubernetes.VerifyPodPVCIdentity(ctx, client, pod, workload.PVCUID); err != nil {
			return kubernetesFenceResource{}, fmt.Errorf("old-primary data volume identity changed without metadata reconciliation: %w", err)
		}
		nodeName = pod.Spec.NodeName
	} else if !errors.Is(podErr, kubernetes.ErrNotFound) {
		return kubernetesFenceResource{}, fmt.Errorf("read old-primary Pod: %w", podErr)
	}
	if nodeName == "" {
		return kubernetesFenceResource{}, fmt.Errorf("old-primary Kubernetes node identity is unknown")
	}
	node, err := client.GetNode(ctx, nodeName)
	if err != nil || !kubernetesNodeReady(node) {
		return kubernetesFenceResource{}, fmt.Errorf("old-primary Kubernetes node is not proven Ready; external node fencing is required")
	}
	return kubernetesFenceResource{api: client, binding: binding, workload: workload, podName: podName, nodeName: nodeName, stateful: stateful}, nil
}

func (fencer *kubernetesWorkloadFencer) Fence(ctx context.Context, request coordination.ExternalFenceRequest) error {
	resource, err := fencer.resource(ctx, request)
	if err != nil {
		return err
	}
	annotations := map[string]string{
		kubernetesFencedAnnotation:         "true",
		kubernetesFenceOperationAnnotation: string(request.OperationID),
		kubernetesFenceInstanceAnnotation:  string(request.Instance.ResourceID),
	}
	updated, err := resource.api.PatchStatefulSetAnnotations(ctx, resource.workload.Namespace, resource.workload.StatefulSet, annotations)
	if err != nil {
		return fmt.Errorf("persist Kubernetes restart fence: %w", err)
	}
	if updated.Metadata.Annotations[kubernetesFencedAnnotation] != "true" ||
		updated.Metadata.Annotations[kubernetesFenceInstanceAnnotation] != string(request.Instance.ResourceID) {
		return fmt.Errorf("Kubernetes restart fence annotation did not persist")
	}
	scale, err := resource.api.GetStatefulSetScale(ctx, resource.workload.Namespace, resource.workload.StatefulSet)
	if err != nil {
		return fmt.Errorf("read old-primary StatefulSet scale: %w", err)
	}
	if scale.Spec.Replicas > 1 {
		return fmt.Errorf("Kubernetes failover refuses a StatefulSet with more than one replica")
	}
	scale.Spec.Replicas = 0
	if _, err := resource.api.UpdateStatefulSetScale(ctx, scale); err != nil {
		return fmt.Errorf("scale old-primary StatefulSet to zero: %w", err)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, fencer.timeout)
	defer cancel()
	for {
		fenced, statusErr := fencer.statusWithResource(deadlineCtx, request, resource)
		if statusErr == nil && fenced {
			return nil
		}
		select {
		case <-deadlineCtx.Done():
			if statusErr != nil {
				return fmt.Errorf("verify Kubernetes old-primary isolation: %w", statusErr)
			}
			return fmt.Errorf("verify Kubernetes old-primary isolation: %w", deadlineCtx.Err())
		case <-time.After(fencer.interval):
		}
	}
}

func (fencer *kubernetesWorkloadFencer) Status(ctx context.Context, request coordination.ExternalFenceRequest) (bool, error) {
	resource, err := fencer.resource(ctx, request)
	if err != nil {
		return false, err
	}
	return fencer.statusWithResource(ctx, request, resource)
}

func (fencer *kubernetesWorkloadFencer) statusWithResource(ctx context.Context, request coordination.ExternalFenceRequest, resource kubernetesFenceResource) (bool, error) {
	stateful, err := resource.api.GetStatefulSet(ctx, resource.workload.Namespace, resource.workload.StatefulSet)
	if err != nil {
		return false, err
	}
	if stateful.Metadata.Annotations[kubernetesFencedAnnotation] != "true" ||
		stateful.Metadata.Annotations[kubernetesFenceInstanceAnnotation] != string(request.Instance.ResourceID) {
		return false, nil
	}
	scale, err := resource.api.GetStatefulSetScale(ctx, resource.workload.Namespace, resource.workload.StatefulSet)
	if err != nil {
		return false, err
	}
	if scale.Spec.Replicas != 0 || scale.Status.Replicas != 0 {
		return false, nil
	}
	_, err = resource.api.GetPod(ctx, resource.workload.Namespace, resource.podName)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, kubernetes.ErrNotFound) {
		return false, err
	}
	node, err := resource.api.GetNode(ctx, resource.nodeName)
	if err != nil || !kubernetesNodeReady(node) {
		return false, fmt.Errorf("old-primary node readiness cannot be proven after scale-down")
	}
	return true, nil
}

type runtimeFencerRouter struct {
	inventory  runtimeWorkloadInventory
	kubernetes coordination.ExternalFencer
	fallback   coordination.ExternalFencer
}

func (router runtimeFencerRouter) Available(instanceID model.ResourceID) bool {
	_, err := router.provider(instanceID)
	return err == nil
}

func (router runtimeFencerRouter) provider(instanceID model.ResourceID) (coordination.ExternalFencer, error) {
	if router.inventory != nil {
		if binding, found := router.inventory.WorkloadBindingForInstance(instanceID); found && binding.Active && binding.RuntimeKind == model.RuntimeKubernetes {
			if router.kubernetes == nil {
				return nil, fmt.Errorf("Kubernetes workload fencing is not enabled")
			}
			return router.kubernetes, nil
		}
	}
	if router.fallback == nil {
		return nil, fmt.Errorf("external workload fencing is not configured")
	}
	return router.fallback, nil
}

func (router runtimeFencerRouter) Fence(ctx context.Context, request coordination.ExternalFenceRequest) error {
	provider, err := router.provider(request.Instance.ResourceID)
	if err != nil {
		return err
	}
	return provider.Fence(ctx, request)
}

func (router runtimeFencerRouter) Status(ctx context.Context, request coordination.ExternalFenceRequest) (bool, error) {
	provider, err := router.provider(request.Instance.ResourceID)
	if err != nil {
		return false, err
	}
	return provider.Status(ctx, request)
}
