package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func seedRuntimeBindingAPIInstance(t *testing.T, server *Server) (model.DatabaseInstance, model.DatabaseNode) {
	t.Helper()
	node, err := server.store.PutNode(model.DatabaseNode{
		NodeName: "cg-data-0001", Kind: model.NodeData, Hostname: "swarm-a", IPAddress: "192.0.2.10", Active: true,
	})
	if err != nil {
		t.Fatalf("put node: %v", err)
	}
	cluster, _, err := server.store.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "swarm-a", IPAddress: "192.0.2.10", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	result, err := server.store.ReconcileInstance(model.DatabaseInstance{
		ClusterID: cluster.ResourceID, NodeID: node.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName:    "swarm-a:3306", Hostname: "swarm-a", IPAddress: "192.0.2.10", Port: 3306,
	})
	if err != nil {
		t.Fatalf("reconcile instance: %v", err)
	}
	return result.Instance, node
}

func TestRuntimeTargetAndWorkloadBindingAPI(t *testing.T) {
	server, _ := newTestServer(t)
	instance, node := seedRuntimeBindingAPIInstance(t, server)

	createdTarget := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/runtime-targets", map[string]interface{}{
		"display_name": "swarm-production", "kind": "docker", "endpoint": "agent://swarm-a",
		"credential_ref": "secret://docker-local", "labels": map[string]string{"environment": "qualification"}, "active": true,
	})
	if createdTarget.Code != http.StatusCreated {
		t.Fatalf("create runtime target status=%d body=%s", createdTarget.Code, createdTarget.Body.String())
	}
	var targetBody struct {
		Result model.RuntimeTarget `json:"result"`
	}
	if err := json.Unmarshal(createdTarget.Body.Bytes(), &targetBody); err != nil {
		t.Fatal(err)
	}
	if !model.ValidResourceID(targetBody.Result.ResourceID) || targetBody.Result.Kind != model.RuntimeDocker {
		t.Fatalf("runtime target=%+v", targetBody.Result)
	}

	createdBinding := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/workload-bindings", map[string]interface{}{
		"instance_id": instance.ResourceID, "runtime_target_id": targetBody.Result.ResourceID,
		"runtime_kind": "docker", "host_node_id": node.ResourceID, "active": true,
		"docker": map[string]interface{}{"swarm_service_name": "cg-mysql-01", "volume_identity": "mysql01-data"},
	})
	if createdBinding.Code != http.StatusCreated {
		t.Fatalf("create workload binding status=%d body=%s", createdBinding.Code, createdBinding.Body.String())
	}
	var bindingBody struct {
		Result model.WorkloadBinding `json:"result"`
	}
	if err := json.Unmarshal(createdBinding.Body.Bytes(), &bindingBody); err != nil {
		t.Fatal(err)
	}
	if bindingBody.Result.Generation != 1 || bindingBody.Result.Docker == nil || bindingBody.Result.Docker.SwarmServiceName != "cg-mysql-01" {
		t.Fatalf("workload binding=%+v", bindingBody.Result)
	}
	if _, _, err := server.store.PutHAEndpoint(store.HAEndpointSpec{
		ClusterID: instance.ClusterID, Kind: model.EndpointVIP, IPAddress: "192.0.2.250",
		Interface: "eth0", Prefix: 24, Provider: model.EndpointProviderLinuxVIP,
		OwnerID: instance.ResourceID, Active: true,
	}); err != nil {
		t.Fatalf("put HA endpoint: %v", err)
	}

	read := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/workload-bindings?instance_id="+string(instance.ResourceID), nil)
	if read.Code != http.StatusOK || !json.Valid(read.Body.Bytes()) {
		t.Fatalf("read workload binding status=%d body=%s", read.Code, read.Body.String())
	}

	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(instance.ClusterID), nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("cluster detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Result struct {
			HAEndpoints    []model.HAEndpoint    `json:"ha_endpoints"`
			RuntimeProfile clusterRuntimeProfile `json:"runtime_profile"`
		} `json:"result"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	profile := detailBody.Result.RuntimeProfile
	if !profile.Complete || profile.Mixed || len(profile.Kinds) != 1 || profile.Kinds[0] != model.RuntimeDocker {
		t.Fatalf("docker runtime profile=%+v", profile)
	}
	if len(profile.UnboundInstanceIDs) != 0 || len(profile.EndpointProviders) != 1 || profile.EndpointProviders[0] != model.EndpointProviderLinuxVIP {
		t.Fatalf("docker runtime boundary=%+v", profile)
	}
	if len(detailBody.Result.HAEndpoints) != 1 || detailBody.Result.HAEndpoints[0].Provider != model.EndpointProviderLinuxVIP {
		t.Fatalf("HA endpoints=%+v", detailBody.Result.HAEndpoints)
	}
}

func TestClusterRuntimeProfileDoesNotInventHostRuntimeForLegacyInventory(t *testing.T) {
	server, _ := newTestServer(t)
	instance, _ := seedRuntimeBindingAPIInstance(t, server)

	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(instance.ClusterID), nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("cluster detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var body struct {
		Result struct {
			RuntimeProfile clusterRuntimeProfile `json:"runtime_profile"`
		} `json:"result"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	profile := body.Result.RuntimeProfile
	if profile.Complete || profile.Mixed || len(profile.Kinds) != 0 {
		t.Fatalf("legacy inventory was mislabeled as a registered runtime: %+v", profile)
	}
	if len(profile.UnboundInstanceIDs) != 1 || profile.UnboundInstanceIDs[0] != instance.ResourceID {
		t.Fatalf("unbound instances=%+v, want %s", profile.UnboundInstanceIDs, instance.ResourceID)
	}
}

func TestWorkloadBindingAPIRejectsMismatchedRuntime(t *testing.T) {
	server, _ := newTestServer(t)
	instance, _ := seedRuntimeBindingAPIInstance(t, server)
	target := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/runtime-targets", map[string]interface{}{
		"display_name": "swarm-production", "kind": "docker", "active": true,
	})
	var targetBody struct {
		Result model.RuntimeTarget `json:"result"`
	}
	if err := json.Unmarshal(target.Body.Bytes(), &targetBody); err != nil {
		t.Fatal(err)
	}

	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/workload-bindings", map[string]interface{}{
		"instance_id": instance.ResourceID, "runtime_target_id": targetBody.Result.ResourceID,
		"runtime_kind": "kubernetes", "active": true,
		"kubernetes": map[string]interface{}{"cluster_name": "lab", "namespace": "database", "stateful_set": "mysql", "ordinal": 0},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("runtime mismatch status=%d body=%s", response.Code, response.Body.String())
	}
}
