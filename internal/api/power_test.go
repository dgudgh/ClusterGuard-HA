package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type powerAPIAgentTransport struct{}

func (powerAPIAgentTransport) Send(_ context.Context, _ model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	running := false
	response := agent.Response{Status: agent.StatusOK, Message: "ok"}
	if request.Command == agent.CommandMySQLPowerStatus || request.Command == agent.CommandPostgreSQLStatus {
		response.ServiceRunning = &running
	}
	return response, nil
}

// powerAPIServer builds the full server wiring: the mysql candidate adapter,
// the wildcard PowerShutdownAdapter, the durable workflow with resolver, and
// the approval service.
func newPowerAPIServer(t *testing.T) (*Server, *store.Repository, *approval.Service) {
	t.Helper()
	repository := store.NewMemory()
	registry := adapter.NewRegistry()
	if err := registry.Register(newCandidateAdapterSpy()); err != nil {
		t.Fatalf("register mysql adapter: %v", err)
	}
	if err := registry.Register(workflow.NewPowerShutdownAdapter(repository, powerAPIAgentTransport{}, "agent-secret")); err != nil {
		t.Fatalf("register power adapter: %v", err)
	}
	now := time.Now().UTC()
	approvalService := approval.New(repository, bytes.NewReader(bytes.Repeat([]byte{0x51}, 32)), func() time.Time { return now })
	resolver := workflow.OperationResolverFunc(func(_ context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
		cluster, _ := repository.Cluster(request.Operation.ClusterID)
		snapshot, _ := repository.TopologySnapshot(request.Operation.ClusterID)
		primary := model.DatabaseInstance{}
		for _, instance := range snapshot.Instances {
			if instance.Role == model.RolePrimary {
				primary = instance
			}
		}
		request.Resolved = &adapter.ResolvedOperation{Cluster: cluster, Snapshot: snapshot, Primary: primary}
		return request, nil
	})
	service := workflow.New(
		registry,
		workflow.TopologyDiscovery{Reader: repository},
		workflow.AllowAllSafety{},
		workflow.NewMemoryLocks(),
		approvalService,
		repository,
		workflow.WithOperationStore(repository),
		workflow.WithOperationResolver(resolver),
	)
	server := NewServer(
		registry, repository, service, &fakeRefresher{},
		WithControlToken(testControlToken),
		WithApprovalService(approvalService),
	)
	return server, repository, approvalService
}

// seedPowerTopology registers a healthy one-primary two-replica cluster and
// persists a topology observation, returning the primary instance ID.
func seedPowerTopology(t *testing.T, server *Server, repository *store.Repository, name string) (model.DatabaseCluster, model.ResourceID) {
	return seedPowerTopologyAtPort(t, server, repository, name, 3306)
}

func seedPowerTopologyAtPort(t *testing.T, server *Server, repository *store.Repository, name string, port int) (model.DatabaseCluster, model.ResourceID) {
	t.Helper()
	payload := map[string]interface{}{
		"display_name": name,
		"engine":       "mysql",
		"endpoints": []map[string]interface{}{
			{"hostname": "mysql-a", "ip_address": "192.0.2.10", "port": port},
			{"hostname": "mysql-b", "ip_address": "192.0.2.11", "port": port},
			{"hostname": "mysql-c", "ip_address": "192.0.2.12", "port": port},
		},
	}
	registered := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", payload)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status: %d %s", registered.Code, registered.Body.String())
	}
	var body struct {
		Result struct {
			Cluster model.DatabaseCluster `json:"cluster"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	clusterID := body.Result.Cluster.ResourceID

	refresher := server.refresher.(*fakeRefresher)
	refresher.refresh = func(_ context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
		endpoints := repository.Endpoints(clusterID)
		observations := make([]store.DiscoveryObservation, 0, len(endpoints))
		probes := make([]model.ProbeStatus, 0, len(endpoints))
		for _, endpoint := range endpoints {
			role := model.RoleReplica
			identitySuffix := strings.TrimPrefix(endpoint.Hostname, "mysql-") + "-" + strconv.Itoa(port)
			source := model.EngineIdentity{"server_uuid": "native-a-" + strconv.Itoa(port)}
			nativeID := "native-" + identitySuffix
			if endpoint.Hostname == "mysql-a" {
				role = model.RolePrimary
				source = nil
			}
			instance := model.DatabaseInstance{
				Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": nativeID},
				Hostname: endpoint.Hostname, IPAddress: endpoint.IPAddress, Port: endpoint.Port, Role: role,
				Health:      model.Health{State: model.HealthHealthy},
				Replication: model.ReplicationStatus{SourceIdentity: source, IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning},
			}
			observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
			probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, Health: model.Health{State: model.HealthHealthy}})
		}
		return repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
			ClusterID: clusterID, InventoryGeneration: testInventoryGeneration(t, repository, clusterID), Observations: observations, Probes: probes,
			Health: model.Health{State: model.HealthHealthy}, ObservedAt: time.Now().UTC(),
		})
	}
	refresh := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(clusterID)+"/discover", nil)
	if refresh.Code != http.StatusOK {
		t.Fatalf("discover status: %d %s", refresh.Code, refresh.Body.String())
	}
	primaryID := ""
	for _, instance := range repository.Instances(clusterID) {
		if instance.Role == model.RolePrimary {
			primaryID = string(instance.ResourceID)
		}
	}
	if primaryID == "" {
		t.Fatal("no primary in seeded topology")
	}
	return body.Result.Cluster, model.ResourceID(primaryID)
}

func decodePowerResult(t *testing.T, response *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		Result map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode power response %s: %v", response.Body.String(), err)
	}
	return body.Result
}

func powerStateOf(result map[string]interface{}) string {
	raw, ok := result["power_operation"].(map[string]interface{})
	if !ok {
		return ""
	}
	return raw["state"].(string)
}

func executePowerServiceLifecycle(t *testing.T, server *Server, cluster model.DatabaseCluster, primaryID model.ResourceID) {
	t.Helper()
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"
	if response := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"}); response.Code != http.StatusOK {
		t.Fatalf("service precheck: %d %s", response.Code, response.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"}); response.Code != http.StatusOK {
		t.Fatalf("service plan: %d %s", response.Code, response.Body.String())
	}
	issued := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", map[string]interface{}{
		"cluster_id": string(cluster.ResourceID), "engine": "mysql",
		"operation_kind": "power_shutdown", "target_id": string(primaryID), "issued_by": "tester",
	})
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue service approval: %d %s", issued.Code, issued.Body.String())
	}
	var grant struct {
		Result struct {
			ApprovalToken string `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode service approval: %v", err)
	}
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": grant.Result.ApprovalToken})
	if executed.Code != http.StatusOK || powerStateOf(decodePowerResult(t, executed)) != "power_off" {
		t.Fatalf("service execute: %d %s", executed.Code, executed.Body.String())
	}
}

func TestPowerOffRequiresCoResidentClustersToBePlannedDown(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	serviceCluster, servicePrimaryID := seedPowerTopologyAtPort(t, server, repository, "mysql-3384", 3384)
	poweroffCluster, _ := seedPowerTopologyAtPort(t, server, repository, "mysql-3306", 3306)
	servicePath := "/api/v1/clusters/" + string(serviceCluster.ResourceID) + "/power/"
	poweroffPath := "/api/v1/clusters/" + string(poweroffCluster.ResourceID) + "/power/"

	servicePrecheck := callJSON(t, server.Handler(), http.MethodPost, servicePath+"precheck", map[string]interface{}{"mode": "service"})
	if servicePrecheck.Code != http.StatusOK || strings.Contains(servicePrecheck.Body.String(), "complete its planned service shutdown first") {
		t.Fatalf("service shutdown must remain available for co-resident clusters: %d %s", servicePrecheck.Code, servicePrecheck.Body.String())
	}
	if !strings.Contains(servicePrecheck.Body.String(), `"display_name":"mysql-3306"`) {
		t.Fatalf("service precheck must expose co-resident inventory: %s", servicePrecheck.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, servicePath+"cancel", nil); response.Code != http.StatusOK {
		t.Fatalf("cancel service precheck: %d %s", response.Code, response.Body.String())
	}

	blocked := callJSON(t, server.Handler(), http.MethodPost, poweroffPath+"precheck", map[string]interface{}{"mode": "poweroff"})
	if blocked.Code != http.StatusOK || !strings.Contains(blocked.Body.String(), "co-resident cluster mysql-3384") {
		t.Fatalf("poweroff must expose co-resident blocker: %d %s", blocked.Code, blocked.Body.String())
	}
	if response := callJSON(t, server.Handler(), http.MethodPost, poweroffPath+"cancel", nil); response.Code != http.StatusOK {
		t.Fatalf("cancel blocked poweroff precheck: %d %s", response.Code, response.Body.String())
	}

	executePowerServiceLifecycle(t, server, serviceCluster, servicePrimaryID)
	ready := callJSON(t, server.Handler(), http.MethodPost, poweroffPath+"precheck", map[string]interface{}{"mode": "poweroff"})
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), "complete its planned service shutdown first") {
		t.Fatalf("poweroff must pass after co-resident service shutdown: %d %s", ready.Code, ready.Body.String())
	}
	if !strings.Contains(ready.Body.String(), `"ready_for_poweroff":true`) {
		t.Fatalf("poweroff precheck must report ready co-resident cluster: %s", ready.Body.String())
	}
}

func rewritePowerTopologyPrimaryRole(t *testing.T, repository *store.Repository, clusterID, primaryID model.ResourceID, role model.InstanceRole) {
	t.Helper()
	endpoints := repository.Endpoints(clusterID)
	instances := repository.Instances(clusterID)
	instancesByID := make(map[model.ResourceID]model.DatabaseInstance, len(instances))
	promotedID := model.ResourceID("")
	var promotedIdentity model.EngineIdentity
	var snapshotPrimaryIdentity model.EngineIdentity
	for _, instance := range instances {
		instancesByID[instance.ResourceID] = instance
		if instance.ResourceID == primaryID {
			snapshotPrimaryIdentity = instance.EngineIdentity.Clone()
		}
		if instance.ResourceID != primaryID && promotedID == "" {
			promotedID = instance.ResourceID
			promotedIdentity = instance.EngineIdentity.Clone()
		}
	}
	observations := make([]store.DiscoveryObservation, 0, len(endpoints))
	probes := make([]model.ProbeStatus, 0, len(endpoints))
	for _, endpoint := range endpoints {
		instance, found := instancesByID[endpoint.InstanceID]
		if !found {
			t.Fatalf("endpoint %s has no instance %s", endpoint.ResourceID, endpoint.InstanceID)
		}
		if instance.ResourceID == primaryID {
			instance.Role = role
			if role == model.RoleReplica {
				instance.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
				instance.Replication = model.ReplicationStatus{SourceIdentity: promotedIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning}
			} else {
				instance.EngineMetadata = map[string]string{"read_only": "false", "super_read_only": "false"}
				instance.Replication = model.ReplicationStatus{}
			}
		} else if role == model.RoleReplica && instance.ResourceID == promotedID {
			instance.Role = model.RolePrimary
			instance.Replication = model.ReplicationStatus{}
			instance.EngineMetadata = map[string]string{"read_only": "false", "super_read_only": "false"}
		} else if role == model.RoleReplica {
			instance.Role = model.RoleReplica
			instance.Replication = model.ReplicationStatus{SourceIdentity: promotedIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning}
			instance.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
		} else {
			instance.Role = model.RoleReplica
			instance.Replication = model.ReplicationStatus{SourceIdentity: snapshotPrimaryIdentity.Clone(), IOThread: model.ThreadRunning, SQLThread: model.ThreadRunning}
			instance.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
		}
		instance.Health = model.Health{State: model.HealthHealthy}
		observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
		probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, InstanceID: instance.ResourceID, Health: instance.Health})
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: clusterID, InventoryGeneration: testInventoryGeneration(t, repository, clusterID),
		Observations: observations, Probes: probes, Health: model.Health{State: model.HealthHealthy},
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("rewrite power topology: %v", err)
	}
}

func TestPowerLifecycleEndToEnd(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-e2e")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	// precheck creates the operation in prechecking.
	precheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	if precheck.Code != http.StatusOK {
		t.Fatalf("precheck: %d %s", precheck.Code, precheck.Body.String())
	}
	result := decodePowerResult(t, precheck)
	if powerStateOf(result) != "prechecking" {
		t.Fatalf("precheck state=%s, want prechecking", powerStateOf(result))
	}

	// plan walks to shutdown_planned and captures the snapshot.
	plan := callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", plan.Code, plan.Body.String())
	}
	result = decodePowerResult(t, plan)
	if powerStateOf(result) != "shutdown_planned" {
		t.Fatalf("plan state=%s, want shutdown_planned", powerStateOf(result))
	}
	if _, ok := result["snapshot"]; !ok {
		t.Fatalf("plan did not capture a snapshot: %s", plan.Body.String())
	}

	// Issue the approval against the primary instance.
	issued := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", map[string]interface{}{
		"cluster_id": string(cluster.ResourceID), "engine": "mysql",
		"operation_kind": "power_shutdown", "target_id": string(primaryID), "issued_by": "tester",
	})
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue approval: %d %s", issued.Code, issued.Body.String())
	}
	var grant struct {
		Result struct {
			ApprovalToken string `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode approval: %v", err)
	}

	// execute runs the standard workflow: protections land, state goes to power_off.
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{
		"approval_token": grant.Result.ApprovalToken,
	})
	if executed.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", executed.Code, executed.Body.String())
	}
	result = decodePowerResult(t, executed)
	if powerStateOf(result) != "power_off" {
		t.Fatalf("execute state=%s, want power_off (%s)", powerStateOf(result), executed.Body.String())
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze after execute: frozen=%t err=%v", frozen, err)
	}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if !instance.Maintenance {
			t.Fatalf("instance %s not in maintenance after execute", instance.ResourceID)
		}
	}

	// recovery lifecycle.
	booted := callJSON(t, server.Handler(), http.MethodPost, path+"boot-detected", nil)
	if booted.Code != http.StatusOK || powerStateOf(decodePowerResult(t, booted)) != "boot_detected" {
		t.Fatalf("boot-detected: %d %s", booted.Code, booted.Body.String())
	}
	recovering := callJSON(t, server.Handler(), http.MethodPost, path+"recovering", nil)
	if recovering.Code != http.StatusOK || powerStateOf(decodePowerResult(t, recovering)) != "recovering" {
		t.Fatalf("recovering: %d %s", recovering.Code, recovering.Body.String())
	}
	verified := callJSON(t, server.Handler(), http.MethodPost, path+"verify", nil)
	if verified.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", verified.Code, verified.Body.String())
	}

	// A healthy aggregate discovery result is not enough to release the
	// protections: the exact primary captured before shutdown must be back in
	// its writable primary role. This protects against a late-starting primary
	// while another controller is already finalizing recovery.
	rewritePowerTopologyPrimaryRole(t, repository, cluster.ResourceID, primaryID, model.RoleReplica)
	blocked := callJSON(t, server.Handler(), http.MethodPost, path+"complete", nil)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "snapshot primary") {
		t.Fatalf("complete without recovered snapshot primary must be blocked: %d %s", blocked.Code, blocked.Body.String())
	}
	if frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID); err != nil || !frozen {
		t.Fatalf("recovery freeze must stay active while primary is not restored: frozen=%t err=%v", frozen, err)
	}
	rewritePowerTopologyPrimaryRole(t, repository, cluster.ResourceID, primaryID, model.RolePrimary)

	completed := callJSON(t, server.Handler(), http.MethodPost, path+"complete", nil)
	if completed.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", completed.Code, completed.Body.String())
	}
	result = decodePowerResult(t, completed)
	if powerStateOf(result) != "completed" {
		t.Fatalf("complete state=%s, want completed", powerStateOf(result))
	}
	frozen, err = repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("recovery freeze must be released: frozen=%t err=%v", frozen, err)
	}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if instance.Maintenance {
			t.Fatalf("instance %s still in maintenance after complete", instance.ResourceID)
		}
	}

	status := callJSON(t, server.Handler(), http.MethodGet, path+"status", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	if !strings.Contains(status.Body.String(), `"state":"completed"`) {
		t.Fatalf("status did not report completed: %s", status.Body.String())
	}
}

func TestPowerRecoveryAcceptsCaughtUpSemisyncIdleReplica(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-semisync-idle")

	instances := repository.Instances(cluster.ResourceID)
	endpoints := repository.Endpoints(cluster.ResourceID)
	instancesByID := make(map[model.ResourceID]model.DatabaseInstance, len(instances))
	replicaID := model.ResourceID("")
	for _, instance := range instances {
		if instance.ResourceID == primaryID {
			instance.EngineMetadata = map[string]string{
				"gtid_executed":                    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa:1-42",
				"semi_sync_required":               "true",
				"semi_sync_available":              "true",
				"semi_sync_source_enabled":         "true",
				"semi_sync_source_status":          "true",
				"semi_sync_source_clients":         "1",
				"semi_sync_wait_for_replica_count": "1",
			}
		} else if replicaID == "" {
			replicaID = instance.ResourceID
			lag := int64(0)
			instance.Health = model.Health{
				State:   model.HealthDegraded,
				Summary: "MySQL replica is not actively acknowledging semi-sync transactions",
			}
			instance.EngineMetadata = map[string]string{
				"semi_sync_required":        "true",
				"semi_sync_available":       "true",
				"semi_sync_source_enabled":  "true",
				"semi_sync_replica_enabled": "true",
				"semi_sync_replica_status":  "false",
				"semi_sync_probe_error":     "false",
			}
			instance.Replication.LagSeconds = &lag
			instance.Replication.ExecutedPosition = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa:1-42"
		}
		instancesByID[instance.ResourceID] = instance
	}
	if replicaID == "" {
		t.Fatal("seeded topology has no replica")
	}
	observations := make([]store.DiscoveryObservation, 0, len(endpoints))
	probes := make([]model.ProbeStatus, 0, len(endpoints))
	for _, endpoint := range endpoints {
		instance := instancesByID[endpoint.InstanceID]
		observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
		probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, InstanceID: instance.ResourceID, Health: instance.Health})
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: observations, Probes: probes,
		Health:     model.Health{State: model.HealthDegraded, Summary: "one replica is not the active semi-sync acknowledger"},
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("persist semi-sync idle topology: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	if reasons := server.powerTopologyChecks(request, cluster.ResourceID); len(reasons) != 0 {
		t.Fatalf("caught-up semi-sync idle replica blocked power recovery: %v", reasons)
	}
	snapshot, ok := server.powerSnapshotFromTopology(request, cluster.ResourceID)
	if !ok {
		t.Fatal("capture recovery snapshot")
	}
	if reasons := server.powerRecoverySnapshotChecks(cluster.ResourceID, model.PowerOperation{Snapshot: snapshot}); len(reasons) != 0 {
		t.Fatalf("caught-up semi-sync idle replica blocked snapshot verification: %v", reasons)
	}
	status := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters/"+string(cluster.ResourceID)+"/power/status", nil)
	result := decodePowerResult(t, status)
	classification, ok := result["outage_classification"].(map[string]interface{})
	if status.Code != http.StatusOK || !ok || classification["kind"] != "normal" || classification["database_state"] != "running" {
		t.Fatalf("safe semi-sync idle replica was misclassified as an outage: %d %s", status.Code, status.Body.String())
	}

	instances = repository.Instances(cluster.ResourceID)
	for index := range instances {
		if instances[index].ResourceID == replicaID {
			instances[index].Replication.ExecutedPosition = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa:1-41"
		}
		instancesByID[instances[index].ResourceID] = instances[index]
	}
	observations = observations[:0]
	probes = probes[:0]
	for _, endpoint := range endpoints {
		instance := instancesByID[endpoint.InstanceID]
		observations = append(observations, store.DiscoveryObservation{EndpointID: endpoint.ResourceID, Instance: instance})
		probes = append(probes, model.ProbeStatus{EndpointID: endpoint.ResourceID, InstanceID: instance.ResourceID, Health: instance.Health})
	}
	if _, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: observations, Probes: probes,
		Health: model.Health{State: model.HealthDegraded}, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("persist divergent semi-sync topology: %v", err)
	}
	if reasons := server.powerTopologyChecks(request, cluster.ResourceID); len(reasons) == 0 {
		t.Fatal("GTID-divergent semi-sync replica must remain blocked")
	}
}

func TestPowerFailKeepsProtectionsActive(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-fail")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	issued := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", map[string]interface{}{
		"cluster_id": string(cluster.ResourceID), "engine": "mysql",
		"operation_kind": "power_shutdown", "target_id": string(primaryID), "issued_by": "tester",
	})
	if issued.Code != http.StatusCreated {
		t.Fatalf("issue approval: %d %s", issued.Code, issued.Body.String())
	}
	var grant struct {
		Result struct {
			ApprovalToken string `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode approval: %v", err)
	}
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{
		"approval_token": grant.Result.ApprovalToken,
	})
	if executed.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", executed.Code, executed.Body.String())
	}

	failed := callJSON(t, server.Handler(), http.MethodPost, path+"fail", map[string]interface{}{"reason": "primary unrecoverable"})
	if failed.Code != http.StatusOK {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body.String())
	}
	result := decodePowerResult(t, failed)
	if powerStateOf(result) != "failed" {
		t.Fatalf("fail state=%s, want failed", powerStateOf(result))
	}
	// Fail-closed: protections stay active, terminal FAILED cannot recover.
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze must stay active after fail: frozen=%t err=%v", frozen, err)
	}
	completed := callJSON(t, server.Handler(), http.MethodPost, path+"complete", nil)
	if completed.Code != http.StatusConflict {
		t.Fatalf("complete after fail must conflict, got %d %s", completed.Code, completed.Body.String())
	}
	// FAILED is terminal, so the operator starts a fresh lifecycle; the
	// surviving protections surface as blocking reasons in the new precheck.
	recheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	if recheck.Code != http.StatusOK {
		t.Fatalf("precheck after fail must start a new lifecycle, got %d %s", recheck.Code, recheck.Body.String())
	}
	result = decodePowerResult(t, recheck)
	if powerStateOf(result) != "prechecking" {
		t.Fatalf("new precheck state=%s, want prechecking", powerStateOf(result))
	}
	if reasons, ok := result["blocking_reasons"].([]interface{}); !ok || len(reasons) == 0 {
		t.Fatalf("precheck after fail must report the active protections, got %s", recheck.Body.String())
	}
}

func TestPowerCancelReturnsToNormal(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, _ := seedPowerTopology(t, server, repository, "power-cancel")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	cancelled := callJSON(t, server.Handler(), http.MethodPost, path+"cancel", nil)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", cancelled.Code, cancelled.Body.String())
	}
	result := decodePowerResult(t, cancelled)
	if powerStateOf(result) != "normal" {
		t.Fatalf("cancel state=%s, want normal", powerStateOf(result))
	}
	// The cancelled operation rests in normal; re-running precheck restarts
	// the lifecycle through the state machine instead of conflicting.
	recheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	if recheck.Code != http.StatusOK {
		t.Fatalf("precheck after cancel: %d %s", recheck.Code, recheck.Body.String())
	}
	result = decodePowerResult(t, recheck)
	if powerStateOf(result) != "prechecking" {
		t.Fatalf("precheck after cancel state=%s, want prechecking", powerStateOf(result))
	}
}

func TestPowerExecuteRequiresApprovalAndPlannedState(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, _ := seedPowerTopology(t, server, repository, "power-guard")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	// Execute without any plan fails.
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": "cgag_x"})
	if executed.Code != http.StatusConflict {
		t.Fatalf("execute without plan must conflict, got %d %s", executed.Code, executed.Body.String())
	}
	// Execute with a plan but no token fails.
	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	executed = callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{})
	if executed.Code != http.StatusUnauthorized {
		t.Fatalf("execute without token must be unauthorized, got %d %s", executed.Code, executed.Body.String())
	}
}

func TestPowerPlanRechecksBlockingConditions(t *testing.T) {
	server, _, _ := newPowerAPIServer(t)
	registered := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "power-blocked", "engine": "mysql",
		"endpoints": []map[string]interface{}{{"hostname": "mysql-a", "ip_address": "192.0.2.10", "port": 3306}},
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", registered.Code, registered.Body.String())
	}
	var body struct {
		Result struct {
			Cluster model.DatabaseCluster `json:"cluster"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cluster: %v", err)
	}
	path := "/api/v1/clusters/" + string(body.Result.Cluster.ResourceID) + "/power/"
	precheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	if precheck.Code != http.StatusOK || !strings.Contains(precheck.Body.String(), "no persisted topology") {
		t.Fatalf("precheck must expose the blocker: %d %s", precheck.Code, precheck.Body.String())
	}
	plan := callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	if plan.Code != http.StatusConflict || !strings.Contains(plan.Body.String(), "blocked") {
		t.Fatalf("plan must reject unresolved blockers: %d %s", plan.Code, plan.Body.String())
	}
}

func TestPowerExecutePlatformAdministratorUsesOneTimeApproval(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, _ := seedPowerTopology(t, server, repository, "power-session")
	client, username := attachAuthenticatedTestClient(t, server, repository, model.PlatformRoleAdmin)
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"
	if response := client.request(t, http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"}, true); response.Code != http.StatusOK {
		t.Fatalf("session precheck: %d %s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"}, true); response.Code != http.StatusOK {
		t.Fatalf("session plan: %d %s", response.Code, response.Body.String())
	}
	executed := client.request(t, http.MethodPost, path+"execute", map[string]interface{}{}, true)
	if executed.Code != http.StatusOK {
		t.Fatalf("session execute without entered token: %d %s", executed.Code, executed.Body.String())
	}
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerPoweredOff || operation.RequestedBy != username {
		t.Fatalf("session power operation=%+v found=%t", operation, found)
	}
}

func TestPowerShutdownExecutionKeyAdvancesAfterRecordedAttempts(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-retry-key")
	powerOperation, err := repository.CreatePowerOperation(context.Background(), model.PowerOperation{
		ClusterID: cluster.ResourceID, OperationType: model.PowerService, RequestedBy: "tester",
	})
	if err != nil {
		t.Fatalf("create power operation: %v", err)
	}
	base := "power-shutdown:" + string(powerOperation.ResourceID)
	if key := server.powerShutdownExecutionKey(powerOperation); key != base {
		t.Fatalf("initial key=%q, want %q", key, base)
	}
	for attempt, key := range []string{base, base + ":retry:1"} {
		if _, _, err := repository.CreateOperation(model.OperationRecord{
			Operation: model.Operation{
				ClusterID: cluster.ResourceID, Engine: cluster.Engine,
				Kind: model.OperationPowerShutdown, RequestedBy: "tester",
			},
			TargetID: primaryID, IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("record attempt %d: %v", attempt, err)
		}
		want := base + ":retry:" + strconv.Itoa(attempt+1)
		if next := server.powerShutdownExecutionKey(powerOperation); next != want {
			t.Fatalf("next key after attempt %d=%q, want %q", attempt, next, want)
		}
	}
}

func TestPowerPrecheckUnknownCluster(t *testing.T) {
	server, _, _ := newPowerAPIServer(t)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters/"+string(model.NewResourceID())+"/power/precheck", map[string]interface{}{"mode": "service"})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
}

func TestPowerPrecheckBlocksEngineWithoutQualifiedBootRecovery(t *testing.T) {
	server, _, _ := newPowerAPIServer(t)
	registered := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "oracle-power-block", "engine": "oracle",
		"endpoints": []map[string]interface{}{{"hostname": "ora-a", "ip_address": "192.0.2.30", "port": 1521}},
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register Oracle cluster: %d %s", registered.Code, registered.Body.String())
	}
	var body struct {
		Result struct {
			Cluster model.DatabaseCluster `json:"cluster"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/clusters/" + string(body.Result.Cluster.ResourceID) + "/power/precheck"
	response := callJSON(t, server.Handler(), http.MethodPost, path, map[string]interface{}{"mode": "service"})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "automatic boot recovery is not qualified for engine oracle") {
		t.Fatalf("unqualified engine must be blocked: %d %s", response.Code, response.Body.String())
	}
}

func TestPowerStatusDistinguishesPlannedShutdownFromUnexpectedFailure(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, _ := seedPowerTopology(t, server, repository, "power-classification")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	classification := func() map[string]interface{} {
		t.Helper()
		response := callJSON(t, server.Handler(), http.MethodGet, path+"status", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("power status: %d %s", response.Code, response.Body.String())
		}
		result := decodePowerResult(t, response)
		value, ok := result["outage_classification"].(map[string]interface{})
		if !ok {
			t.Fatalf("power status has no outage classification: %s", response.Body.String())
		}
		return value
	}

	healthy := classification()
	if healthy["kind"] != "normal" || healthy["database_state"] != "running" || healthy["control_plane"] != "online_independent" {
		t.Fatalf("healthy classification=%+v", healthy)
	}
	if healthy["operator_initiated"] != false || healthy["automatic_failover_suppressed"] != false {
		t.Fatalf("healthy intent/failover classification=%+v", healthy)
	}

	degraded, found := repository.Cluster(cluster.ResourceID)
	if !found {
		t.Fatal("cluster disappeared")
	}
	degraded.Health = model.Health{State: model.HealthUnhealthy, Summary: "primary probe failed", ObservedAt: time.Now().UTC()}
	if _, err := repository.UpsertCluster(degraded); err != nil {
		t.Fatalf("degrade cluster: %v", err)
	}
	unexpected := classification()
	if unexpected["kind"] != "unexpected_failure" || unexpected["database_state"] != "failed" {
		t.Fatalf("unexpected failure classification=%+v", unexpected)
	}
	if unexpected["operator_initiated"] != false || unexpected["automatic_failover_suppressed"] != false {
		t.Fatalf("unexpected failure must keep automatic recovery eligible: %+v", unexpected)
	}

	degraded.Health = model.Health{State: model.HealthHealthy, Summary: "healthy", ObservedAt: time.Now().UTC()}
	if _, err := repository.UpsertCluster(degraded); err != nil {
		t.Fatalf("restore cluster health: %v", err)
	}
	precheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{
		"mode": "service", "requested_by": "database-operator",
	})
	if precheck.Code != http.StatusOK {
		t.Fatalf("power precheck: %d %s", precheck.Code, precheck.Body.String())
	}
	planned := classification()
	if planned["kind"] != "planned_shutdown" || planned["database_state"] != "running" || planned["requested_by"] != "database-operator" {
		t.Fatalf("planned shutdown classification=%+v", planned)
	}
	if planned["operator_initiated"] != true || planned["automatic_failover_suppressed"] != false {
		t.Fatalf("precheck intent must be visible before protection is armed: %+v", planned)
	}
}

func TestPowerStatusFailedLifecycleStaysProtectedAndIdentifiable(t *testing.T) {
	server, repository, _ := newPowerAPIServer(t)
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-failed-classification")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service", "requested_by": "operator-a"})
	callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service", "requested_by": "operator-a"})
	issued := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", map[string]interface{}{
		"cluster_id": string(cluster.ResourceID), "engine": "mysql",
		"operation_kind": "power_shutdown", "target_id": string(primaryID), "issued_by": "operator-a",
	})
	var grant struct {
		Result struct {
			ApprovalToken string `json:"approval_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode approval: %v", err)
	}
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": grant.Result.ApprovalToken})
	if executed.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", executed.Code, executed.Body.String())
	}
	failed := callJSON(t, server.Handler(), http.MethodPost, path+"fail", map[string]interface{}{"reason": "restart verification failed"})
	if failed.Code != http.StatusOK {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body.String())
	}

	status := callJSON(t, server.Handler(), http.MethodGet, path+"status", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	result := decodePowerResult(t, status)
	classification, ok := result["outage_classification"].(map[string]interface{})
	if !ok || classification["kind"] != "planned_shutdown_failed" || classification["automatic_failover_suppressed"] != true {
		t.Fatalf("failed classification=%+v body=%s", classification, status.Body.String())
	}
	if result["protected"] != true || result["protected_state"] != "failed" || result["recovery_freeze"] != true {
		t.Fatalf("failed lifecycle must remain protected: %s", status.Body.String())
	}
	if maintenance, ok := result["instances_in_maintenance"].([]interface{}); !ok || len(maintenance) != 3 {
		t.Fatalf("failed lifecycle must expose all protected database instances: %s", status.Body.String())
	}
}
