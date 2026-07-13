package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type nodeLifecycleManagerSpy struct {
	calls         int
	request       lifecycle.Request
	plan          lifecycle.Plan
	secrets       lifecycle.ExecutionSecrets
	approvalToken string
	task          lifecycle.Task
	err           error
}

func (manager *nodeLifecycleManagerSpy) Execute(_ context.Context, request lifecycle.Request, plan lifecycle.Plan, secrets lifecycle.ExecutionSecrets, approvalToken string) (lifecycle.Task, error) {
	manager.calls++
	manager.request = request
	manager.plan = plan
	manager.secrets = secrets
	manager.approvalToken = approvalToken
	if manager.task.ResourceID == "" {
		manager.task = lifecycle.Task{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: request.ClusterID, Request: request, Plan: plan, Status: lifecycle.TaskSucceeded}
	}
	return manager.task, manager.err
}

func prepareNodeSyncAPI(t *testing.T) (*Server, *store.Repository, model.DatabaseCluster, *nodeLifecycleManagerSpy) {
	t.Helper()
	server, repository := newTestServer(t)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "orders"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-primary", IPAddress: "192.0.2.10", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create node-sync inventory: %v", err)
	}
	primary := model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName:    "mysql-primary", Hostname: "mysql-primary", IPAddress: "192.0.2.10", Port: 3306,
		Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
		EngineMetadata: map[string]string{"version": "8.0.44"},
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: time.Now().UTC(),
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: primary}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
	}); err != nil {
		t.Fatalf("publish node-sync topology: %v", err)
	}
	manager := &nodeLifecycleManagerSpy{}
	WithNodeLifecycle(manager, lifecycle.Capabilities{CloneAvailable: true, XtraBackupVersions: map[string]bool{"8.0": true}, LogicalDumpAllowed: true}, LifecycleSecretProviderFunc(func(context.Context, lifecycle.Request) (lifecycle.ExecutionSecrets, error) {
		return lifecycle.ExecutionSecrets{SSHPassword: "ssh-write-only", MySQLRootPassword: "root-write-only", ReplicationPassword: "replication-write-only"}, nil
	}))(server)
	return server, repository, cluster, manager
}

func nodeSyncRequestBody(clusterID model.ResourceID) map[string]interface{} {
	return map[string]interface{}{
		"cluster_id":   clusterID,
		"action":       "add",
		"sync_method":  "auto",
		"requested_by": "dba",
		"targets": []map[string]interface{}{{
			"node_name": "cg-data-0002", "kind": "data", "hostname": "mysql-replica", "ip_address": "192.0.2.11",
			"ssh_user": "root", "ssh_port": 22, "mysql_version": "8.0.44", "mysql_port": 3306, "package_name": "mysql-8.0.44.tar.xz",
		}},
	}
}

func TestNodeSyncAPIPlansFromCanonicalPrimaryAndExecutesWithoutEchoingSecrets(t *testing.T) {
	server, _, cluster, manager := prepareNodeSyncAPI(t)
	payload := nodeSyncRequestBody(cluster.ResourceID)

	precheck := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/precheck", payload)
	if precheck.Code != http.StatusOK || !strings.Contains(precheck.Body.String(), `"target_identity"`) || !strings.Contains(precheck.Body.String(), `"blocked":false`) {
		t.Fatalf("node sync precheck: %d %s", precheck.Code, precheck.Body.String())
	}
	plan := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/plan", payload)
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"sync_method":"clone"`) || !strings.Contains(plan.Body.String(), `"node_id":"`) {
		t.Fatalf("node sync plan: %d %s", plan.Code, plan.Body.String())
	}
	payload["approval_token"] = "approved-lifecycle"
	executed := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/execute", payload)
	if executed.Code != http.StatusOK || manager.calls != 1 || manager.request.Donor.InstanceID == "" || manager.request.Donor.Hostname != "mysql-primary" || manager.request.Donor.Version != "8.0.44" || manager.approvalToken != "approved-lifecycle" {
		t.Fatalf("node sync execute: %d %s manager=%+v", executed.Code, executed.Body.String(), manager)
	}
	for _, secret := range []string{"ssh-write-only", "root-write-only", "replication-write-only"} {
		if strings.Contains(executed.Body.String(), secret) {
			t.Fatalf("node sync response exposed %q: %s", secret, executed.Body.String())
		}
	}
}

func TestNodeSyncCapabilitiesReportWhetherRealExecutionIsConfigured(t *testing.T) {
	configured, _, _, _ := prepareNodeSyncAPI(t)
	response := callJSON(t, configured.Handler(), http.MethodGet, "/api/v1/nodes/sync/capabilities", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"available":true`) || !strings.Contains(response.Body.String(), `"clone_available":true`) {
		t.Fatalf("configured lifecycle capabilities: %d %s", response.Code, response.Body.String())
	}

	unconfigured, _ := newTestServer(t)
	response = callJSON(t, unconfigured.Handler(), http.MethodGet, "/api/v1/nodes/sync/capabilities", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"available":false`) || !strings.Contains(response.Body.String(), "not configured") {
		t.Fatalf("unconfigured lifecycle capabilities: %d %s", response.Code, response.Body.String())
	}
}

func TestNodeSyncAPIBlocksUnknownOrDuplicateInventoryBeforeExecutor(t *testing.T) {
	server, repository, cluster, manager := prepareNodeSyncAPI(t)
	if _, err := repository.PutNode(model.DatabaseNode{NodeName: "cg-data-0002", Kind: model.NodeData, Hostname: "different-host", Active: true}); err != nil {
		t.Fatalf("register fixed node: %v", err)
	}
	payload := nodeSyncRequestBody(cluster.ResourceID)
	payload["approval_token"] = "approved-lifecycle"
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/execute", payload)
	if response.Code != http.StatusConflict || manager.calls != 0 || !strings.Contains(response.Body.String(), "blocked") {
		t.Fatalf("duplicate inventory execute: %d %s calls=%d", response.Code, response.Body.String(), manager.calls)
	}
}

func TestNodeSyncVerifyAndTaskRoutesReturnDurableTaskWithoutSecrets(t *testing.T) {
	server, repository, cluster, _ := prepareNodeSyncAPI(t)
	task, err := repository.PutLifecycleTask(lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: cluster.ResourceID,
		Status: lifecycle.TaskSucceeded, Request: lifecycle.Request{ClusterID: cluster.ResourceID, Action: lifecycle.ActionAdd, RequestedBy: "dba"},
		Checks: []model.Check{{Name: "replication", Status: model.CheckPass, Message: "healthy"}},
	})
	if err != nil {
		t.Fatalf("persist lifecycle task: %v", err)
	}
	verified := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/verify", map[string]interface{}{"task_id": task.ResourceID})
	if verified.Code != http.StatusOK || !strings.Contains(verified.Body.String(), `"verified":true`) || !strings.Contains(verified.Body.String(), `"replication"`) {
		t.Fatalf("verify lifecycle task: %d %s", verified.Code, verified.Body.String())
	}
	listed := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/nodes/sync/tasks", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), string(task.ResourceID)) {
		t.Fatalf("list lifecycle tasks: %d %s", listed.Code, listed.Body.String())
	}
	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/nodes/sync/tasks/"+string(task.ResourceID), nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), string(task.ResourceID)) {
		t.Fatalf("get lifecycle task: %d %s", detail.Code, detail.Body.String())
	}
}

func TestNodeSyncAPIRejectsClientSuppliedCredentials(t *testing.T) {
	server, _, cluster, manager := prepareNodeSyncAPI(t)
	payload := nodeSyncRequestBody(cluster.ResourceID)
	payload["mysql_root_password"] = "must-never-enter-api"
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/nodes/sync/execute", payload)
	if response.Code != http.StatusBadRequest || manager.calls != 0 || strings.Contains(response.Body.String(), "must-never-enter-api") {
		t.Fatalf("client secret field was accepted or reflected: %d %s", response.Code, response.Body.String())
	}
}
