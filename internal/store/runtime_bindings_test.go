package store

import (
	"errors"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func createRuntimeBindingFixture(t *testing.T, repository *Repository) (model.DatabaseInstance, model.DatabaseNode) {
	t.Helper()
	node, err := repository.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "docker-a", IPAddress: "192.0.2.10", Active: true,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "docker-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	instance := mysqlInstance(cluster.ResourceID, "docker-a", "192.0.2.10", 3306)
	instance.NodeID = node.ResourceID
	result, err := repository.ReconcileInstance(instance)
	if err != nil {
		t.Fatalf("reconcile instance: %v", err)
	}
	return result.Instance, node
}

func TestDockerRuntimeTargetAndBindingAreDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	instance, node := createRuntimeBindingFixture(t, repository)
	target, err := repository.PutRuntimeTarget(model.RuntimeTarget{
		DisplayName: "swarm-lab", Kind: model.RuntimeDocker, Endpoint: "agent://docker-a", Active: true,
		Labels: map[string]string{"environment": "qualification"},
	})
	if err != nil {
		t.Fatalf("put runtime target: %v", err)
	}
	binding, err := repository.PutWorkloadBinding(model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeDocker,
		HostNodeID: node.ResourceID, Active: true,
		Docker: &model.DockerWorkloadRef{SwarmServiceName: "cg-mysql-01", VolumeIdentity: "mysql01-data"},
	})
	if err != nil {
		t.Fatalf("put workload binding: %v", err)
	}
	if binding.Generation != 1 || binding.MetadataRevision != 1 || !model.ValidResourceID(binding.ResourceID) {
		t.Fatalf("binding=%+v", binding)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	got, found := reopened.WorkloadBindingForInstance(instance.ResourceID)
	if !found || got.ResourceID != binding.ResourceID || got.Docker == nil || got.Docker.SwarmServiceName != "cg-mysql-01" {
		t.Fatalf("durable binding=%+v found=%t", got, found)
	}
}

func TestWorkloadBindingRejectsRuntimeMismatchAndDuplicateActivePlacement(t *testing.T) {
	repository := NewMemory()
	instance, node := createRuntimeBindingFixture(t, repository)
	target, err := repository.PutRuntimeTarget(model.RuntimeTarget{DisplayName: "swarm-lab", Kind: model.RuntimeDocker, Active: true})
	if err != nil {
		t.Fatalf("put runtime target: %v", err)
	}
	invalid := model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeKubernetes,
		Kubernetes: &model.KubernetesWorkloadRef{ClusterName: "lab", Namespace: "database", StatefulSet: "mysql", Ordinal: 0}, Active: true,
	}
	if _, err := repository.PutWorkloadBinding(invalid); err == nil {
		t.Fatal("runtime kind mismatch was accepted")
	}
	first, err := repository.PutWorkloadBinding(model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeDocker,
		HostNodeID: node.ResourceID, Docker: &model.DockerWorkloadRef{SwarmServiceName: "cg-mysql-01"}, Active: true,
	})
	if err != nil {
		t.Fatalf("put first binding: %v", err)
	}
	if _, err := repository.PutWorkloadBinding(model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeDocker,
		HostNodeID: node.ResourceID, Docker: &model.DockerWorkloadRef{SwarmServiceName: "cg-mysql-replacement"}, Active: true,
	}); err == nil {
		t.Fatal("duplicate active binding was accepted")
	}
	updated := first
	updated.Docker.ContainerID = "container-generation-two"
	updated.ObservedRuntimeID = "container-generation-two"
	updated, err = repository.PutWorkloadBinding(updated)
	if err != nil {
		t.Fatalf("update binding observation: %v", err)
	}
	if updated.Generation != 2 || updated.MetadataRevision != 2 {
		t.Fatalf("updated binding=%+v", updated)
	}
}

func TestLegacySnapshotNormalizesMissingRuntimeCollections(t *testing.T) {
	value, _, err := decodeSnapshotState([]byte(`{"clusters":{},"nodes":{},"instances":{},"endpoints":{},"ha_endpoints":{}}`))
	if err != nil {
		t.Fatalf("decode legacy snapshot: %v", err)
	}
	if value.RuntimeTargets == nil || value.WorkloadBindings == nil {
		t.Fatalf("legacy runtime collections were not normalized: %+v", value)
	}
}

func TestRuntimeTargetWithActiveBindingCannotBeRetiredOrRetyped(t *testing.T) {
	repository := NewMemory()
	instance, node := createRuntimeBindingFixture(t, repository)
	target, err := repository.PutRuntimeTarget(model.RuntimeTarget{DisplayName: "swarm-lab", Kind: model.RuntimeDocker, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutWorkloadBinding(model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeDocker,
		HostNodeID: node.ResourceID, Docker: &model.DockerWorkloadRef{SwarmServiceName: "cg-mysql-01"}, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	retired := target
	retired.Active = false
	if _, err := repository.PutRuntimeTarget(retired); !errors.Is(err, ErrConflict) {
		t.Fatalf("retire target with active binding err=%v", err)
	}
	retyped := target
	retyped.Kind = model.RuntimeLinux
	if _, err := repository.PutRuntimeTarget(retyped); !errors.Is(err, ErrConflict) {
		t.Fatalf("retype target with active binding err=%v", err)
	}
}

func TestKubernetesRuntimeBindingRequiresExecutableIdentity(t *testing.T) {
	repository := NewMemory()
	instance, _ := createRuntimeBindingFixture(t, repository)
	if _, err := repository.PutRuntimeTarget(model.RuntimeTarget{DisplayName: "k8s-invalid", Kind: model.RuntimeKubernetes, Endpoint: "http://kubernetes.local", Active: true}); err == nil {
		t.Fatal("active Kubernetes target without HTTPS and an absolute credential reference was accepted")
	}
	target, err := repository.PutRuntimeTarget(model.RuntimeTarget{
		DisplayName: "k8s-lab", Kind: model.RuntimeKubernetes, Endpoint: "https://kubernetes.local:6443",
		CredentialRef: "/etc/clusterguard/kubernetes/credentials.json", Active: true,
	})
	if err != nil {
		t.Fatalf("put Kubernetes runtime target: %v", err)
	}
	base := model.WorkloadBinding{
		InstanceID: instance.ResourceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeKubernetes, Active: true,
		Kubernetes: &model.KubernetesWorkloadRef{ClusterName: "lab", Namespace: "database", StatefulSet: "mysql-a", Ordinal: 0},
	}
	if _, err := repository.PutWorkloadBinding(base); err == nil {
		t.Fatal("active Kubernetes binding without immutable workload identity was accepted")
	}
	base.Kubernetes.PodName = "mysql-a-0"
	base.Kubernetes.PodUID = "pod-uid-a"
	base.Kubernetes.NodeName = "worker-a"
	base.Kubernetes.PVCUID = "pvc-uid-a"
	if _, err := repository.PutWorkloadBinding(base); err != nil {
		t.Fatalf("put complete Kubernetes workload binding: %v", err)
	}
}

func TestKubernetesRuntimeBindingsRequireDedicatedUniqueWorkloads(t *testing.T) {
	repository := NewMemory()
	first, _ := createRuntimeBindingFixture(t, repository)
	secondResult, err := repository.ReconcileInstance(mysqlInstance(first.ClusterID, "docker-b", "192.0.2.11", 3306))
	if err != nil {
		t.Fatalf("reconcile second instance: %v", err)
	}
	target, err := repository.PutRuntimeTarget(model.RuntimeTarget{
		DisplayName: "k8s-lab", Kind: model.RuntimeKubernetes, Endpoint: "https://kubernetes.local:6443",
		CredentialRef: "/etc/clusterguard/kubernetes/credentials.json", Active: true,
	})
	if err != nil {
		t.Fatalf("put Kubernetes runtime target: %v", err)
	}
	binding := func(instanceID model.ResourceID, statefulSet, podName, podUID, pvcUID string) model.WorkloadBinding {
		return model.WorkloadBinding{
			InstanceID: instanceID, RuntimeTargetID: target.ResourceID, RuntimeKind: model.RuntimeKubernetes, Active: true,
			Kubernetes: &model.KubernetesWorkloadRef{
				ClusterName: "lab", Namespace: "database", StatefulSet: statefulSet, Ordinal: 0,
				PodName: podName, PodUID: podUID, NodeName: "worker-a", PVCUID: pvcUID,
			},
		}
	}

	invalidOrdinal := binding(first.ResourceID, "mysql-a", "mysql-a-1", "pod-uid-a", "pvc-uid-a")
	invalidOrdinal.Kubernetes.Ordinal = 1
	if _, err := repository.PutWorkloadBinding(invalidOrdinal); err == nil {
		t.Fatal("non-zero Kubernetes StatefulSet ordinal was accepted")
	}
	invalidName := binding(first.ResourceID, "MySQL-A", "MySQL-A-0", "pod-uid-a", "pvc-uid-a")
	if _, err := repository.PutWorkloadBinding(invalidName); err == nil {
		t.Fatal("non-DNS Kubernetes object name was accepted")
	}
	if _, err := repository.PutWorkloadBinding(binding(first.ResourceID, "mysql-a", "mysql-a-0", "pod-uid-a", "pvc-uid-a")); err != nil {
		t.Fatalf("put first Kubernetes workload binding: %v", err)
	}

	conflicts := []model.WorkloadBinding{
		binding(secondResult.Instance.ResourceID, "mysql-a", "mysql-a-0", "pod-uid-b", "pvc-uid-b"),
		binding(secondResult.Instance.ResourceID, "mysql-b", "mysql-b-0", "pod-uid-a", "pvc-uid-b"),
		binding(secondResult.Instance.ResourceID, "mysql-b", "mysql-b-0", "pod-uid-b", "pvc-uid-a"),
	}
	for _, conflict := range conflicts {
		if _, err := repository.PutWorkloadBinding(conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate Kubernetes workload identity err=%v", err)
		}
	}
}
