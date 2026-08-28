package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

// integrationPowerTransport is the full-stack fake agent transport: it
// records every request and instance, answering from per-command maps so the
// integration test can assert the exact agent traffic a real SSH transport
// would carry.
type integrationPowerTransport struct {
	responses map[string]agent.Response
	failures  map[string]error
	requests  []agent.Request
	instances []model.DatabaseInstance
}

func (transport *integrationPowerTransport) Send(_ context.Context, instance model.DatabaseInstance, request agent.Request) (agent.Response, error) {
	transport.requests = append(transport.requests, request)
	transport.instances = append(transport.instances, instance)
	if failure := transport.failures[request.Command]; failure != nil {
		return agent.Response{}, failure
	}
	return transport.responses[request.Command], nil
}

func (transport *integrationPowerTransport) commandCount(command string) int {
	count := 0
	for _, request := range transport.requests {
		if request.Command == command {
			count++
		}
	}
	return count
}

// newPowerIntegrationServer mirrors newPowerAPIServer but wires a real
// PowerShutdownAdapter with the fake agent transport, so the execute path
// runs the actual ordered shutdown against recorded agent traffic.
func newPowerIntegrationServer(t *testing.T, transport *integrationPowerTransport, secret string) (*Server, *store.Repository, *approval.Service) {
	t.Helper()
	repository := store.NewMemory()
	registry := adapter.NewRegistry()
	if err := registry.Register(newCandidateAdapterSpy()); err != nil {
		t.Fatalf("register mysql adapter: %v", err)
	}
	if err := registry.Register(workflow.NewPowerShutdownAdapter(repository, transport, secret)); err != nil {
		t.Fatalf("register power adapter: %v", err)
	}
	now := time.Now().UTC()
	approvalService := approval.New(repository, bytes.NewReader(bytes.Repeat([]byte{0x51}, 256)), func() time.Time { return now })
	resolver := workflow.OperationResolverFunc(func(_ context.Context, request adapter.OperationRequest) (adapter.OperationRequest, error) {
		cluster, _ := repository.Cluster(request.Operation.ClusterID)
		snapshot, _ := repository.TopologySnapshot(request.Operation.ClusterID)
		primary := model.DatabaseInstance{}
		for _, instance := range snapshot.Instances {
			if instance.Role == model.RolePrimary {
				primary = instance
			}
		}
		request.Resolved = &adapter.ResolvedOperation{
			OperationID: request.Operation.ResourceID, Cluster: cluster, Snapshot: snapshot, Primary: primary,
		}
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

func issuePowerApproval(t *testing.T, server *Server, clusterID model.ResourceID, primaryID model.ResourceID) string {
	t.Helper()
	issued := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/approvals", map[string]interface{}{
		"cluster_id": string(clusterID), "engine": "mysql",
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
	return grant.Result.ApprovalToken
}

// TestPowerLifecycleIntegrationFullShutdownAndRecovery drives the complete
// lifecycle through the real HTTP surface with a fake agent transport: the
// ordered shutdown (persist read-only, snapshot, isolate VIPs, stop replicas,
// stop primary), the protections landing, and the automatic recovery path back
// to completed with the protections released.
func TestPowerLifecycleIntegrationFullShutdownAndRecovery(t *testing.T) {
	stopped := false
	transport := &integrationPowerTransport{responses: map[string]agent.Response{
		agent.CommandPowerPrepare:     {Status: agent.StatusOK, Message: "snapshot prepared"},
		agent.CommandPersistRole:      {Status: agent.StatusOK, Message: "persisted"},
		agent.CommandSelfIsolate:      {Status: agent.StatusOK, Message: "VIP released and node isolated"},
		agent.CommandMySQLServiceStop: {Status: agent.StatusOK, Message: "stopped"},
		agent.CommandMySQLPowerStatus: {Status: agent.StatusOK, ServiceRunning: &stopped},
	}}
	server, repository, _ := newPowerIntegrationServer(t, transport, "agent-secret")
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-integration")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	if code := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"}).Code; code != http.StatusOK {
		t.Fatalf("precheck: %d", code)
	}
	plan := callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	if plan.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", plan.Code, plan.Body.String())
	}
	token := issuePowerApproval(t, server, cluster.ResourceID, primaryID)
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": token})
	if executed.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", executed.Code, executed.Body.String())
	}
	if powerStateOf(decodePowerResult(t, executed)) != "power_off" {
		t.Fatalf("execute state=%s, want power_off (%s)", powerStateOf(decodePowerResult(t, executed)), executed.Body.String())
	}

	// Agent traffic: persist on every node, then the two replicas, then the
	// primary, all signed and scoped to the operation.
	if count := transport.commandCount(agent.CommandPersistRole); count != 3 {
		t.Fatalf("persist_role sends=%d, want 3", count)
	}
	if count := transport.commandCount(agent.CommandPowerPrepare); count != 3 {
		t.Fatalf("power_prepare sends=%d, want 3", count)
	}
	if count := transport.commandCount(agent.CommandSelfIsolate); count != 3 {
		t.Fatalf("self_isolate sends=%d, want 3", count)
	}
	stopIndices := []int{}
	lastIsolation := -1
	for index, request := range transport.requests {
		if request.Command == agent.CommandSelfIsolate {
			lastIsolation = index
		}
		if request.Command != agent.CommandMySQLServiceStop {
			continue
		}
		stopIndices = append(stopIndices, index)
		if request.Signature == "" || !model.ValidResourceID(request.OperationID) ||
			!bytes.HasPrefix([]byte(request.PlanDigest), []byte("sha256:")) || request.Engine != model.EngineMySQL {
			t.Fatalf("power agent request not signed/scoped: %+v", request)
		}
	}
	if len(stopIndices) != 3 {
		t.Fatalf("mysql_service_stop sends=%d, want 3", len(stopIndices))
	}
	if lastIsolation < 0 || stopIndices[0] <= lastIsolation {
		t.Fatalf("first service stop=%d last isolation=%d, want every VIP isolated before stopping", stopIndices[0], lastIsolation)
	}
	// Both replicas stop before the primary; replica ordering is not
	// guaranteed (snapshot iteration), the quiesce order is.
	if transport.instances[stopIndices[0]].DisplayName == "mysql-a" || transport.instances[stopIndices[1]].DisplayName == "mysql-a" {
		t.Fatalf("replicas must stop before the primary: %s, %s, %s",
			transport.instances[stopIndices[0]].DisplayName, transport.instances[stopIndices[1]].DisplayName, transport.instances[stopIndices[2]].DisplayName)
	}
	if transport.instances[stopIndices[2]].DisplayName != "mysql-a" {
		t.Fatalf("primary must stop last, got %s", transport.instances[stopIndices[2]].DisplayName)
	}
	if count := transport.commandCount(agent.CommandNodePoweroff); count != 0 {
		t.Fatalf("service mode must not power off hosts, got %d node_poweroff", count)
	}

	// Protections are active and the state machine advanced.
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze after execute: frozen=%t err=%v", frozen, err)
	}
	for _, instance := range repository.Instances(cluster.ResourceID) {
		if !instance.Maintenance {
			t.Fatalf("instance %s not in maintenance after execute", instance.ResourceID)
		}
	}

	// Recovery: the boot-time units drive boot-detected -> recovering, then
	// verify -> complete releases the protections.
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
	completed := callJSON(t, server.Handler(), http.MethodPost, path+"complete", nil)
	if completed.Code != http.StatusOK || powerStateOf(decodePowerResult(t, completed)) != "completed" {
		t.Fatalf("complete: %d %s", completed.Code, completed.Body.String())
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

	// Phase 7 contract: the durable workflow persists a terminal report with
	// the audit timeline for the power shutdown, served by the reports route.
	var powerReport model.Report
	for _, report := range repository.Reports() {
		if report.OperationID != "" {
			powerReport = report
		}
	}
	if powerReport.ResourceID == "" || powerReport.Status != model.OperationSucceeded {
		t.Fatalf("power shutdown must produce a terminal report, got %+v", powerReport)
	}
	reportJSON := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(powerReport.ResourceID), nil)
	if reportJSON.Code != http.StatusOK {
		t.Fatalf("report json: %d %s", reportJSON.Code, reportJSON.Body.String())
	}
	var reported struct {
		Result struct {
			Audits []model.AuditEvent `json:"audits"`
		} `json:"result"`
	}
	if err := json.Unmarshal(reportJSON.Body.Bytes(), &reported); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if len(reported.Result.Audits) == 0 {
		t.Fatal("power shutdown report must carry the audit timeline")
	}
	reportHTML := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/reports/"+string(powerReport.ResourceID)+"/html", nil)
	if reportHTML.Code != http.StatusOK {
		t.Fatalf("report html: %d %s", reportHTML.Code, reportHTML.Body.String())
	}
}

// TestPowerLifecycleIntegrationPoweroffMode verifies the poweroff variant:
// hosts are powered off only after every service stopped, and the post-execute
// verification treats unreachable hosts as the expected state.
func TestPowerLifecycleIntegrationPoweroffMode(t *testing.T) {
	transport := &integrationPowerTransport{
		responses: map[string]agent.Response{
			agent.CommandPowerPrepare:     {Status: agent.StatusOK, Message: "snapshot prepared"},
			agent.CommandPersistRole:      {Status: agent.StatusOK, Message: "persisted"},
			agent.CommandSelfIsolate:      {Status: agent.StatusOK, Message: "VIP released and node isolated"},
			agent.CommandMySQLServiceStop: {Status: agent.StatusOK, Message: "stopped"},
			agent.CommandNodePoweroff:     {Status: agent.StatusOK, Message: "poweroff initiated"},
		},
		// After poweroff the hosts are unreachable: the workflow verify treats
		// that as the expected outcome.
		failures: map[string]error{agent.CommandMySQLPowerStatus: errors.New("ssh: host is down")},
	}
	server, repository, _ := newPowerIntegrationServer(t, transport, "agent-secret")
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-integration-poweroff")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	if code := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "poweroff"}).Code; code != http.StatusOK {
		t.Fatalf("precheck: %d", code)
	}
	if code := callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "poweroff"}).Code; code != http.StatusOK {
		t.Fatalf("plan: %d", code)
	}
	token := issuePowerApproval(t, server, cluster.ResourceID, primaryID)
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": token})
	if executed.Code != http.StatusOK {
		t.Fatalf("execute: %d %s", executed.Code, executed.Body.String())
	}

	// Three hosts powered off, each strictly after its service stop.
	if count := transport.commandCount(agent.CommandNodePoweroff); count != 3 {
		t.Fatalf("node_poweroff sends=%d, want 3", count)
	}
	lastStop, firstPoweroff := -1, -1
	for index, request := range transport.requests {
		switch request.Command {
		case agent.CommandMySQLServiceStop:
			lastStop = index
		case agent.CommandNodePoweroff:
			if firstPoweroff < 0 {
				firstPoweroff = index
			}
		}
	}
	if firstPoweroff < 0 || firstPoweroff <= lastStop {
		t.Fatalf("poweroff must come after every stop: firstPoweroff=%d lastStop=%d", firstPoweroff, lastStop)
	}

	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerPoweredOff {
		t.Fatalf("state=%s found=%t, want power_off", operation.State, found)
	}
}

// TestPowerLifecycleIntegrationAgentFailureIsFailClosed verifies the operator
// failure path end to end: an agent step failing mid-shutdown leaves the
// operation in shutting_down with every protection active; power/fail moves it
// to FAILED (terminal), complete is refused, and the new precheck surfaces the
// active protections as blocking reasons.
func TestPowerLifecycleIntegrationAgentFailureIsFailClosed(t *testing.T) {
	transport := &integrationPowerTransport{
		responses: map[string]agent.Response{
			agent.CommandPowerPrepare: {Status: agent.StatusOK, Message: "snapshot prepared"},
			agent.CommandPersistRole:  {Status: agent.StatusOK, Message: "persisted"},
			agent.CommandSelfIsolate:  {Status: agent.StatusOK, Message: "VIP released and node isolated"},
		},
		failures: map[string]error{agent.CommandMySQLServiceStop: errors.New("ssh: connection refused")},
	}
	server, repository, _ := newPowerIntegrationServer(t, transport, "agent-secret")
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-integration-fail")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	token := issuePowerApproval(t, server, cluster.ResourceID, primaryID)
	executed := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": token})
	if executed.Code != http.StatusConflict {
		t.Fatalf("execute with agent failure: code=%d %s, want 409", executed.Code, executed.Body.String())
	}

	// The state machine paused in shutting_down with protections active.
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerShuttingDown {
		t.Fatalf("state=%s found=%t, want shutting_down", operation.State, found)
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("recovery freeze must stay active: frozen=%t err=%v", frozen, err)
	}
	// The replica stop failed before the primary: exactly one replica got the
	// stop command and nothing after it.
	if count := transport.commandCount(agent.CommandMySQLServiceStop); count != 1 {
		t.Fatalf("mysql_service_stop sends=%d, want 1 (first replica only)", count)
	}

	failed := callJSON(t, server.Handler(), http.MethodPost, path+"fail", map[string]interface{}{"reason": "ssh transport down"})
	if failed.Code != http.StatusOK || powerStateOf(decodePowerResult(t, failed)) != "failed" {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body.String())
	}
	if code := callJSON(t, server.Handler(), http.MethodPost, path+"complete", nil).Code; code != http.StatusConflict {
		t.Fatalf("complete after fail must conflict, got %d", code)
	}
	recheck := callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	if recheck.Code != http.StatusOK || powerStateOf(decodePowerResult(t, recheck)) != "prechecking" {
		t.Fatalf("fresh precheck after fail: %d %s", recheck.Code, recheck.Body.String())
	}
	if reasons, ok := decodePowerResult(t, recheck)["blocking_reasons"].([]interface{}); !ok || len(reasons) == 0 {
		t.Fatalf("fresh precheck must report the active protections, got %s", recheck.Body.String())
	}
}

func TestPowerLifecycleIntegrationRetriesProtectedShuttingDownState(t *testing.T) {
	stopped := false
	transport := &integrationPowerTransport{
		responses: map[string]agent.Response{
			agent.CommandPowerPrepare:     {Status: agent.StatusOK, Message: "snapshot prepared"},
			agent.CommandPersistRole:      {Status: agent.StatusOK, Message: "persisted"},
			agent.CommandSelfIsolate:      {Status: agent.StatusOK, Message: "VIP released and node isolated"},
			agent.CommandMySQLServiceStop: {Status: agent.StatusOK, Message: "stopped"},
			agent.CommandMySQLPowerStatus: {Status: agent.StatusOK, ServiceRunning: &stopped},
		},
		failures: map[string]error{agent.CommandMySQLServiceStop: errors.New("ssh: connection refused")},
	}
	server, repository, _ := newPowerIntegrationServer(t, transport, "agent-secret")
	cluster, primaryID := seedPowerTopology(t, server, repository, "power-integration-retry")
	path := "/api/v1/clusters/" + string(cluster.ResourceID) + "/power/"

	callJSON(t, server.Handler(), http.MethodPost, path+"precheck", map[string]interface{}{"mode": "service"})
	callJSON(t, server.Handler(), http.MethodPost, path+"plan", map[string]interface{}{"mode": "service"})
	firstToken := issuePowerApproval(t, server, cluster.ResourceID, primaryID)
	first := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": firstToken})
	if first.Code != http.StatusConflict {
		t.Fatalf("first execute code=%d, want 409: %s", first.Code, first.Body.String())
	}
	operation, found := repository.ActivePowerOperation(context.Background(), cluster.ResourceID)
	if !found || operation.State != model.PowerShuttingDown {
		t.Fatalf("state=%s found=%t, want protected shutting_down", operation.State, found)
	}

	delete(transport.failures, agent.CommandMySQLServiceStop)
	retryToken := issuePowerApproval(t, server, cluster.ResourceID, primaryID)
	retried := callJSON(t, server.Handler(), http.MethodPost, path+"execute", map[string]interface{}{"approval_token": retryToken})
	if retried.Code != http.StatusOK || powerStateOf(decodePowerResult(t, retried)) != "power_off" {
		t.Fatalf("retry execute: %d %s", retried.Code, retried.Body.String())
	}
	for _, request := range transport.requests {
		if agentMutationCommandForTest(request.Command) && !model.ValidResourceID(request.LeaseID) {
			t.Fatalf("retry mutation request is missing its operation lease: %+v", request)
		}
	}
}

func agentMutationCommandForTest(command string) bool {
	switch command {
	case agent.CommandPowerPrepare, agent.CommandPersistRole, agent.CommandSelfIsolate,
		agent.CommandMySQLServiceStop, agent.CommandNodePoweroff:
		return true
	default:
		return false
	}
}
