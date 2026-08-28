package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/approval"
	"clusterguard.io/ha/internal/controlstate"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

const testControlToken = "test-control-token"

type apiRunner struct{}

type mutationMaintenanceStub struct{ err error }

func (stub mutationMaintenanceStub) Check(context.Context) error { return stub.err }

type apiMutationAuthorityStub struct {
	err           error
	calls         int
	term          uint64
	leaderID      model.ResourceID
	leaderAddress string
	leaderAPI     string
}

func (authority *apiMutationAuthorityStub) LeadershipEpoch() uint64 { return authority.term }

func (authority *apiMutationAuthorityStub) RequireMutationAuthority(context.Context) error {
	authority.calls++
	return authority.err
}

func (authority *apiMutationAuthorityStub) Leader() (model.ResourceID, string, bool) {
	return authority.leaderID, authority.leaderAddress, authority.leaderID != ""
}

func (authority *apiMutationAuthorityStub) LeaderAPIAddress(model.ResourceID) (string, bool) {
	return authority.leaderAPI, strings.TrimSpace(authority.leaderAPI) != ""
}

type metadataAdapterSpy struct {
	adapter.UnsupportedAdapter
	mu         sync.Mutex
	calls      int
	prechecks  int
	reconciles int
}

type strictAdministrativeApproval struct {
	calls int
}

func (gate *strictAdministrativeApproval) Consume(_ context.Context, operation model.OperationRecord, _ string) (model.ResourceID, model.OperationRecord, error) {
	gate.calls++
	return "", operation, errors.New("explicit approval is required")
}

func (gate *strictAdministrativeApproval) Validate(context.Context, model.Operation, string) error {
	gate.calls++
	return errors.New("explicit administrative approval is required")
}

type apiStageFailingJournal struct {
	repository *store.Repository
	stage      model.WorkflowStage
}

func (journal apiStageFailingJournal) RecordAudit(event model.AuditEvent) error {
	if event.Stage == journal.stage {
		return errors.New("secret journal backend failure")
	}
	return journal.repository.RecordAudit(event)
}

func (journal apiStageFailingJournal) RecordReport(report model.Report) error {
	return journal.repository.RecordReport(report)
}

func newMetadataAdapterSpy() *metadataAdapterSpy {
	return &metadataAdapterSpy{UnsupportedAdapter: adapter.NewUnsupported(model.EngineMySQL)}
}

func (candidate *metadataAdapterSpy) Capabilities(context.Context) adapter.Capabilities {
	return adapter.Capabilities{Engine: model.EngineMySQL, Features: map[adapter.Capability]adapter.CapabilityState{
		adapter.CapabilityMetadataReconcile: {Available: true},
	}}
}

func (candidate *metadataAdapterSpy) MetadataPrecheck(context.Context, adapter.MetadataRequest) ([]model.Check, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.calls++
	candidate.prechecks++
	return nil, nil
}

func (candidate *metadataAdapterSpy) ReconcileMetadata(context.Context, adapter.MetadataRequest) (adapter.MetadataResult, error) {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	candidate.calls++
	candidate.reconciles++
	return adapter.MetadataResult{}, nil
}

func (candidate *metadataAdapterSpy) callCount() int {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return candidate.calls
}

func (candidate *metadataAdapterSpy) reconcileCallCount() int {
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	return candidate.reconciles
}

type fakeRefresher struct {
	mu      sync.Mutex
	calls   []model.ResourceID
	refresh func(context.Context, model.ResourceID) (model.TopologySnapshot, error)
}

func (refresher *fakeRefresher) Refresh(ctx context.Context, clusterID model.ResourceID) (model.TopologySnapshot, error) {
	refresher.mu.Lock()
	refresher.calls = append(refresher.calls, clusterID)
	callback := refresher.refresh
	refresher.mu.Unlock()
	if callback == nil {
		return model.TopologySnapshot{}, nil
	}
	return callback(ctx, clusterID)
}

func (refresher *fakeRefresher) callCount() int {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return len(refresher.calls)
}

func (apiRunner) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, query string) ([]mysql.Row, error) {
	if strings.HasPrefix(query, "SELECT") {
		return []mysql.Row{{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "hostname": "mysql-a", "port": "3306", "server_id": "1",
			"version": "8.0.44", "read_only": "0", "super_read_only": "0", "gtid_mode": "ON", "log_bin": "1", "binlog_format": "ROW",
		}}, nil
	}
	return nil, nil
}

func newTestServer(t *testing.T) (*Server, *store.Repository) {
	t.Helper()
	registry := adapter.NewRegistry()
	for _, candidate := range []adapter.DatabaseHAAdapter{mysql.New(apiRunner{}), postgresql.New(), oracle.New(), sqlserver.New()} {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	repository := store.NewMemory()
	approvalService := approval.New(repository, rand.Reader, time.Now)
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository)
	return NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken), WithApprovalService(approvalService)), repository
}

func callJSON(t *testing.T, handler http.Handler, method string, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestControlAPIPostsRequireConfiguredBearerToken(t *testing.T) {
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	refresher := &fakeRefresher{}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository)
	server := NewServer(registry, repository, service, refresher, WithControlToken("control-secret"))
	payload := []byte(`{"display_name":"secured","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`)

	request := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewReader(payload))
		httpRequest.Header.Set("Content-Type", "application/json")
		if token != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httpRequest)
		return response
	}

	for _, token := range []string{"", "wrong"} {
		response := request(token)
		if response.Code != http.StatusUnauthorized || len(repository.Clusters()) != 0 {
			t.Fatalf("token %q did not fail closed: %d %s", token, response.Code, response.Body.String())
		}
	}
	if response := request("control-secret"); response.Code != http.StatusCreated || len(repository.Clusters()) != 1 {
		t.Fatalf("valid control token did not authorize registration: %d %s", response.Code, response.Body.String())
	}

	cluster := repository.Clusters()[0]
	discovery := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/"+string(cluster.ResourceID)+"/discover", strings.NewReader("{}"))
	discovery.Header.Set("Content-Type", "application/json")
	discoveryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(discoveryResponse, discovery)
	if discoveryResponse.Code != http.StatusUnauthorized || refresher.callCount() != 0 {
		t.Fatalf("anonymous discovery reached refresher: %d %s calls=%d", discoveryResponse.Code, discoveryResponse.Body.String(), refresher.callCount())
	}
}

func TestSoftwareUpdateMaintenanceBlocksMutationsButKeepsReadsAvailable(t *testing.T) {
	repository := store.NewMemory()
	server := NewServer(
		adapter.NewRegistry(), repository, nil, nil,
		WithControlToken(testControlToken),
		WithMutationMaintenance(mutationMaintenanceStub{err: errors.New("software update active")}),
	)

	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "blocked", "engine": "mysql",
	})
	if response.Code != http.StatusLocked || !strings.Contains(response.Body.String(), "software update maintenance") {
		t.Fatalf("maintenance mutation status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.Clusters()) != 0 {
		t.Fatal("maintenance-blocked request changed repository")
	}

	response = callJSON(t, server.Handler(), http.MethodGet, "/api/v1/clusters", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("maintenance blocked read status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlAPIPostsFailClosedWhenTokenIsNotConfigured(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer anything")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured control authentication status = %d, want 503: %s", response.Code, response.Body.String())
	}
}

func TestControlAPIMutationsRequireCurrentQuorumLeader(t *testing.T) {
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	leaderID := model.NewResourceID()
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: leaderID,
		leaderAddress: "192.0.2.10:10009", leaderAPI: "https://192.0.2.10:8443",
	}
	server := NewServer(registry, repository, nil, nil, WithControlToken(testControlToken), WithMutationAuthority(authority))
	payload := map[string]interface{}{"display_name": "secured", "engine": "mysql", "endpoints": []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}}}

	blockedRequest := httptest.NewRequest(http.MethodPost, "http://controller-b:8088/api/v1/clusters", strings.NewReader(`{"display_name":"secured","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`))
	blockedRequest.Header.Set("Authorization", "Bearer "+testControlToken)
	blockedRequest.Header.Set("Content-Type", "application/json")
	blocked := httptest.NewRecorder()
	server.Handler().ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusServiceUnavailable || len(repository.Clusters()) != 0 || authority.calls != 1 || blocked.Header().Get("X-ClusterGuard-Leader-ID") != string(leaderID) || blocked.Header().Get("X-ClusterGuard-Leader-Address") != authority.leaderAddress || blocked.Header().Get("X-ClusterGuard-Leader-API-Address") != authority.leaderAPI {
		t.Fatalf("non-leader mutation was not blocked: %d %s headers=%v", blocked.Code, blocked.Body.String(), blocked.Header())
	}
	read := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/engines", nil)
	if read.Code != http.StatusOK || authority.calls != 1 {
		t.Fatalf("read-only API was incorrectly gated: %d %s calls=%d", read.Code, read.Body.String(), authority.calls)
	}
	authority.err = nil
	allowed := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/clusters", payload)
	if allowed.Code != http.StatusCreated || len(repository.Clusters()) != 1 || authority.calls != 2 {
		t.Fatalf("authoritative mutation failed: %d %s calls=%d", allowed.Code, allowed.Body.String(), authority.calls)
	}
}

func TestControlAPIMutationsProxyToCurrentQuorumLeader(t *testing.T) {
	var forwardedCalls int
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwardedCalls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read forwarded body: %v", err)
		}
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/api/v1/clusters?source=console" {
			t.Fatalf("forwarded request = %s %s", request.Method, request.URL.RequestURI())
		}
		if string(body) != `{"display_name":"proxied","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}` {
			t.Fatalf("forwarded body = %s", body)
		}
		for name, want := range map[string]string{
			"Authorization":   "Bearer " + testControlToken,
			"Cookie":          "clusterguard_session=session-token",
			"X-CSRF-Token":    "csrf-token",
			"Idempotency-Key": "register-proxied-cluster",
		} {
			if got := request.Header.Get(name); got != want {
				t.Fatalf("forwarded %s = %q, want %q", name, got, want)
			}
		}
		if request.Header.Get(mutationRPCForwardedHeader) == "" {
			t.Fatal("forwarded request is missing loop-prevention marker")
		}
		writer.Header().Set(mutationRPCRevisionHeader, "1")
		writer.Header().Set("X-Leader-Result", "accepted")
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": map[string]bool{"proxied": true}})
	}))
	defer leader.Close()
	leaderURL, err := url.Parse(leader.URL)
	if err != nil {
		t.Fatalf("parse leader URL: %v", err)
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: model.NewResourceID(),
		leaderAddress: leaderURL.Hostname() + ":10009", leaderAPI: leader.URL,
	}
	server := NewServer(
		registry, repository, nil, nil,
		WithControlToken(testControlToken),
		WithMutationAuthority(authority),
		WithMutationRPC(NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1})),
	)
	payload := `{"display_name":"proxied","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`
	request := httptest.NewRequest(http.MethodPost, "http://attacker-controlled.example:65530/api/v1/clusters?source=console", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "clusterguard_session=session-token")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.Header.Set("Idempotency-Key", "register-proxied-cluster")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"proxied":true`) {
		t.Fatalf("proxied mutation response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Leader-Result") != "accepted" || response.Header().Get("X-ClusterGuard-Mutation-RPC-Leader") != leader.URL {
		t.Fatalf("proxied response headers = %v", response.Header())
	}
	if forwardedCalls != 1 || authority.calls != 1 || len(repository.Clusters()) != 0 {
		t.Fatalf("forwarded=%d authority=%d local_clusters=%d", forwardedCalls, authority.calls, len(repository.Clusters()))
	}
}

func TestClusterRetirementProxiesToCurrentQuorumLeader(t *testing.T) {
	clusterID := model.NewResourceID()
	payload := `{"confirm_display_name":"retire-through-leader"}`
	forwardedCalls := 0
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwardedCalls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read forwarded retirement body: %v", err)
		}
		if request.Method != http.MethodDelete || request.URL.Path != "/api/v1/clusters/"+string(clusterID) {
			t.Fatalf("forwarded retirement request = %s %s", request.Method, request.URL.RequestURI())
		}
		if string(body) != payload {
			t.Fatalf("forwarded retirement body = %s", body)
		}
		if request.Header.Get("Authorization") != "Bearer "+testControlToken || request.Header.Get(mutationRPCForwardedHeader) != "1" {
			t.Fatalf("forwarded retirement headers = %v", request.Header)
		}
		writer.Header().Set(mutationRPCRevisionHeader, "1")
		writeJSON(writer, http.StatusOK, map[string]interface{}{
			"status": "ok", "result": map[string]interface{}{"cluster_id": clusterID, "retired": true},
		})
	}))
	defer leader.Close()

	repository := store.NewMemory()
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: model.NewResourceID(),
		leaderAddress: "192.0.2.10:10009", leaderAPI: leader.URL,
	}
	server := NewServer(
		adapter.NewRegistry(), repository, nil, nil,
		WithControlToken(testControlToken),
		WithMutationAuthority(authority),
		WithMutationRPC(NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1})),
	)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/clusters/"+string(clusterID), strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"retired":true`) {
		t.Fatalf("proxied retirement response = %d %s", response.Code, response.Body.String())
	}
	if forwardedCalls != 1 || authority.calls != 1 || len(repository.Clusters()) != 0 {
		t.Fatalf("retirement forwarded=%d authority=%d local_clusters=%d", forwardedCalls, authority.calls, len(repository.Clusters()))
	}
}

func TestForwardedMutationCannotLoopThroughAnotherFollower(t *testing.T) {
	forwardedCalls := 0
	leader := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwardedCalls++ }))
	defer leader.Close()
	leaderURL, err := url.Parse(leader.URL)
	if err != nil {
		t.Fatalf("parse leader URL: %v", err)
	}
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: model.NewResourceID(),
		leaderAddress: leaderURL.Hostname() + ":10009", leaderAPI: leader.URL,
	}
	server := NewServer(
		adapter.NewRegistry(), store.NewMemory(), nil, nil,
		WithControlToken(testControlToken),
		WithMutationAuthority(authority),
		WithMutationRPC(NewLeaderMutationRPCClient(leader.Client())),
	)
	request := httptest.NewRequest(http.MethodPost, "http://controller-c:"+leaderURL.Port()+"/api/v1/clusters", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set(mutationRPCForwardedHeader, "1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || forwardedCalls != 0 || !strings.Contains(response.Body.String(), "current Raft leader") {
		t.Fatalf("looping mutation response=%d %s forwarded=%d", response.Code, response.Body.String(), forwardedCalls)
	}
}

func TestMutationRPCLeaderResponseIncludesCommittedMetadataRevision(t *testing.T) {
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register MySQL adapter: %v", err)
	}
	repository := store.NewMemory()
	server := NewServer(registry, repository, nil, nil, WithControlToken(testControlToken))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{"display_name":"revision","engine":"mysql","endpoints":[{"hostname":"mysql-a","port":3306}]}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(mutationRPCForwardedHeader, "1")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("X-ClusterGuard-Metadata-Revision") != "1" {
		t.Fatalf("leader mutation response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

type mutationRevisionStub struct {
	mu       sync.Mutex
	revision uint64
}

func (stub *mutationRevisionStub) StateRevision() uint64 {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.revision
}

func (stub *mutationRevisionStub) set(revision uint64) {
	stub.mu.Lock()
	stub.revision = revision
	stub.mu.Unlock()
}

func TestMutationRPCWaitsForFollowerMetadataBeforeReturningSuccess(t *testing.T) {
	called := make(chan struct{})
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-ClusterGuard-Metadata-Revision", "2")
		close(called)
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	}))
	defer leader.Close()
	revisions := &mutationRevisionStub{revision: 1}
	updated := make(chan struct{})
	go func() {
		<-called
		time.Sleep(100 * time.Millisecond)
		revisions.set(2)
		close(updated)
	}()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/clusters/00000000-0000-4000-8000-000000000001", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	if err := NewLeaderMutationRPCClient(leader.Client(), revisions).Forward(response, request, leader.URL); err != nil {
		t.Fatalf("forward mutation RPC: %v", err)
	}
	select {
	case <-updated:
	default:
		t.Fatal("mutation RPC returned before follower applied the leader metadata revision")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("mutation RPC response=%d %s", response.Code, response.Body.String())
	}
}

func TestMutationRPCRejectsLeaderSuccessWhenFollowerDoesNotCatchUp(t *testing.T) {
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(mutationRPCRevisionHeader, "2")
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	}))
	defer leader.Close()

	revisions := &mutationRevisionStub{revision: 1}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/clusters/00000000-0000-4000-8000-000000000001", nil)
	ctx, cancel := context.WithTimeout(request.Context(), 25*time.Millisecond)
	defer cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	err := NewLeaderMutationRPCClient(leader.Client(), revisions).Forward(response, request, leader.URL)
	if err == nil || !strings.Contains(err.Error(), "metadata revision") {
		t.Fatalf("stale follower mutation RPC error=%v response=%d %s", err, response.Code, response.Body.String())
	}
}

func TestMutationRPCRejectsLeaderSuccessWithoutRevisionEvidence(t *testing.T) {
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	}))
	defer leader.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	err := NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1}).Forward(response, request, leader.URL)
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("missing leader revision error=%v response=%d %s", err, response.Code, response.Body.String())
	}
}

func TestMutationRPCRejectsRedirectWithoutForwardingCredentials(t *testing.T) {
	redirectedCalls := 0
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectedCalls++ }))
	defer redirected.Close()
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, redirected.URL, http.StatusTemporaryRedirect)
	}))
	defer leader.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	err := NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1}).Forward(response, request, leader.URL)
	if err == nil || redirectedCalls != 0 {
		t.Fatalf("redirect error=%v redirected_calls=%d", err, redirectedCalls)
	}
}

func TestMutationRPCRejectsOversizedRequestBeforeCallingLeader(t *testing.T) {
	leaderCalls := 0
	leader := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaderCalls++ }))
	defer leader.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(strings.Repeat("x", maximumJSONBodyBytes+1)))
	request.ContentLength = 0
	response := httptest.NewRecorder()
	err := NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1}).Forward(response, request, leader.URL)
	if err == nil || !strings.Contains(err.Error(), "maximum") || leaderCalls != 0 {
		t.Fatalf("oversized request error=%v leader_calls=%d", err, leaderCalls)
	}
}

func TestLeaderMutationRPCStreamsSoftwareUpdateUploadBeyondJSONLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), maximumJSONBodyBytes+4096)
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contents, err := io.ReadAll(request.Body)
		if err != nil || !bytes.Equal(contents, payload) {
			t.Errorf("forwarded upload bytes=%d err=%v", len(contents), err)
		}
		writer.Header().Set(mutationRPCRevisionHeader, "1")
		writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok"})
	}))
	defer leader.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=update")
	response := httptest.NewRecorder()
	if err := NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1}).Forward(response, request, leader.URL); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMutationRPCRejectsOversizedLeaderResponseBeforeWritingFollowerResponse(t *testing.T) {
	leader := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(mutationRPCRevisionHeader, "1")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, strings.Repeat("x", controlstate.MaximumBytes+1))
	}))
	defer leader.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	err := NewLeaderMutationRPCClient(leader.Client(), &mutationRevisionStub{revision: 1}).Forward(response, request, leader.URL)
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized response error=%v", err)
	}
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("partial oversized response leaked: code=%d body_bytes=%d", response.Code, response.Body.Len())
	}
}

func testInventoryGeneration(t *testing.T, repository *store.Repository, clusterID model.ResourceID) uint64 {
	t.Helper()
	inventory, found := repository.DiscoveryInventory(clusterID)
	if !found || inventory.Generation == 0 {
		t.Fatalf("missing discovery inventory for %s: %+v found=%t", clusterID, inventory, found)
	}
	return inventory.Generation
}

func TestEnginesEndpointListsAllRegisteredEngines(t *testing.T) {
	server, _ := newTestServer(t)
	response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/engines", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("engines status: %d %s", response.Code, response.Body.String())
	}
	for _, engine := range []string{"mysql", "postgresql", "oracle", "sqlserver"} {
		if !strings.Contains(response.Body.String(), `"engine":"`+engine+`"`) {
			t.Fatalf("engine response missing %s: %s", engine, response.Body.String())
		}
	}
}

func TestDirectDiscoveryRouteIsRemoved(t *testing.T) {
	server, _ := newTestServer(t)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/discovery", map[string]interface{}{
		"credentials": map[string]string{"username": "monitor", "password": "hidden-secret"},
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("direct discovery status: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "hidden-secret") {
		t.Fatalf("direct discovery error exposed request secret: %s", response.Body.String())
	}
}

func TestGenericJSONRoutesRejectOversizedBodiesIndependentOfContentLength(t *testing.T) {
	server, repository := newTestServer(t)
	for _, path := range []string{"/api/v1/clusters", "/api/v1/operations/execute", "/api/v1/metadata/reconcile/execute"} {
		t.Run(path, func(t *testing.T) {
			body := strings.Repeat(" ", maximumJSONBodyBytes+1) + `{}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			request.ContentLength = 0
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("oversized body status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
	if len(repository.Clusters()) != 0 {
		t.Fatalf("oversized registration changed inventory: %+v", repository.Clusters())
	}
}

func TestExecuteEndpointFailsClosedWhenMutationIsUnsupported(t *testing.T) {
	server, _ := newTestServer(t)
	payload := map[string]interface{}{
		"operation":       map[string]interface{}{"cluster_id": model.NewResourceID(), "engine": "mysql", "kind": "failover", "requested_by": "dba"},
		"target_id":       model.NewResourceID(),
		"idempotency_key": "unsupported-failover",
		"approval_token":  "approved",
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/operations/execute", payload)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "approval grant") {
		t.Fatalf("unsupported execute must fail closed: %d %s", response.Code, response.Body.String())
	}
}

func TestClusterTopologyAndHealthEndpointsUsePlatformResourceIDs(t *testing.T) {
	server, repository := newTestServer(t)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "platform-ids"}, []model.Endpoint{
		{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true},
	})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID:           cluster.ResourceID,
		InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			DisplayName: "mysql-a", Hostname: "mysql-a", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
		}}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}},
		Health: model.Health{State: model.HealthHealthy},
	})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	for _, path := range []string{"/api/v1/clusters/" + string(cluster.ResourceID) + "/topology", "/api/v1/clusters/" + string(cluster.ResourceID) + "/health"} {
		response := callJSON(t, server.Handler(), http.MethodGet, path, nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), string(cluster.ResourceID)) || !strings.Contains(response.Body.String(), string(snapshot.Instances[0].ResourceID)) {
			t.Fatalf("endpoint %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestMetadataExecuteReusesResourceIDForRenamedMySQLEndpoint(t *testing.T) {
	server, repository := newTestServer(t)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	seed, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		DisplayName: "mysql-old", Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
	}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	payload := map[string]interface{}{
		"operation":      map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"},
		"approval_token": "approved",
		"instance": map[string]interface{}{
			"resource_id": seed.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			"display_name": "mysql-new", "hostname": "mysql-new", "ip_address": "192.0.2.20", "port": 3310, "role": "replica", "health": map[string]string{"state": "unhealthy"},
		},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("metadata execute: %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].ResourceID != seed.Instances[0].ResourceID || instances[0].Hostname != "mysql-new" || instances[0].Role != model.RolePrimary || instances[0].Health.State != model.HealthHealthy {
		t.Fatalf("metadata execute did not reconcile existing resource: %+v", instances)
	}
	updatedEndpoints := repository.Endpoints(cluster.ResourceID)
	if updatedEndpoints[0].Hostname != "mysql-new" || updatedEndpoints[0].IPAddress != "192.0.2.20" || updatedEndpoints[0].Port != 3310 {
		t.Fatalf("metadata execute did not update discovery endpoint: %+v", updatedEndpoints)
	}
	if _, found := repository.TopologySnapshot(cluster.ResourceID); found {
		t.Fatal("metadata execute must invalidate topology")
	}
}

func TestMetadataVerifyUsesLiveDiscoveryInsteadOfRebuildingPlan(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-live-verify"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-new", IPAddress: "192.0.2.20", Port: 3310, Active: true}},
	)
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	instance := model.DatabaseInstance{
		ResourceMeta:   model.ResourceMeta{ResourceID: model.NewResourceID()},
		ClusterID:      cluster.ResourceID,
		Engine:         model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{"server_uuid": "metadata-live-native"},
		DisplayName:    "mysql-new",
		Hostname:       "mysql-new",
		IPAddress:      "192.0.2.20",
		Port:           3310,
		Role:           model.RolePrimary,
		Health:         model.Health{State: model.HealthHealthy},
	}
	observedAt := time.Now().UTC()
	seed, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: instance}},
		Probes:       []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	instance = seed.Instances[0]
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: []model.DatabaseInstance{instance}, ObservedAt: time.Now().UTC()}, nil
	}}
	candidate := newMetadataAdapterSpy()
	server := newAPIServer(t, repository, candidate, refresher)
	payload := map[string]interface{}{
		"operation":   map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"},
		"endpoint_id": endpoints[0].ResourceID,
		"instance":    instance,
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/verify", payload)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "metadata_live_identity") || !strings.Contains(response.Body.String(), "metadata_live_endpoint") {
		t.Fatalf("live metadata verify = %d %s", response.Code, response.Body.String())
	}
	if refresher.callCount() != 1 || candidate.reconcileCallCount() != 0 {
		t.Fatalf("verify calls: refresh=%d reconcile=%d", refresher.callCount(), candidate.reconcileCallCount())
	}
}

func TestMetadataVerifyBlocksLiveIdentityMismatch(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-live-mismatch"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	seed, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "expected-native"}, Hostname: "mysql-a", Port: 3306,
		}}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	expected := seed.Instances[0]
	unexpected := expected
	unexpected.EngineIdentity = model.EngineIdentity{"server_uuid": "unexpected-native"}
	refresher := &fakeRefresher{refresh: func(context.Context, model.ResourceID) (model.TopologySnapshot, error) {
		return model.TopologySnapshot{ClusterID: cluster.ResourceID, Instances: []model.DatabaseInstance{unexpected}}, nil
	}}
	server := newAPIServer(t, repository, newMetadataAdapterSpy(), refresher)
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/verify", map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"},
		"instance":  expected,
	})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "metadata_live_identity") {
		t.Fatalf("identity mismatch verify = %d %s", response.Code, response.Body.String())
	}
}

func TestSessionMetadataUsesPlatformAdminAuthorizationWithoutClientToken(t *testing.T) {
	registry := adapter.NewRegistry()
	candidate := newMetadataAdapterSpy()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register metadata adapter: %v", err)
	}
	repository := store.NewMemory()
	approvalGate := &strictAdministrativeApproval{}
	service := workflow.New(
		registry,
		workflow.TopologyDiscovery{Reader: repository},
		workflow.AllowAllSafety{},
		workflow.NewMemoryLocks(),
		approvalGate,
		repository,
	)
	server := NewServer(
		registry,
		repository,
		service,
		&fakeRefresher{},
		WithControlToken(testControlToken),
	)
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "session-metadata"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create metadata inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{
		ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID),
		ObservedAt: observedAt,
		Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
			ClusterID: cluster.ResourceID, Engine: model.EngineMySQL,
			EngineIdentity: model.EngineIdentity{"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
			DisplayName:    "mysql-old", Hostname: "mysql-old", IPAddress: "192.0.2.10", Port: 3306,
			Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}, PromotionEligible: true,
		}}},
		Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, DiscoveryObservedAt: observedAt, Health: model.Health{State: model.HealthHealthy}}},
	})
	if err != nil {
		t.Fatalf("seed metadata topology: %v", err)
	}
	client, username := attachAuthenticatedTestClient(t, server, repository, model.PlatformRoleAdmin)
	response := client.request(t, http.MethodPost, "/api/v1/metadata/reconcile/execute", map[string]interface{}{
		"operation": map[string]interface{}{
			"engine":       "mysql",
			"kind":         "metadata_reconciliation",
			"requested_by": "forged-browser-actor",
		},
		"instance": map[string]interface{}{
			"resource_id": snapshot.Instances[0].ResourceID,
			"cluster_id":  cluster.ResourceID,
			"engine":      "mysql",
			"engine_identity": map[string]string{
				"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			},
			"display_name": "mysql-new",
			"hostname":     "mysql-new",
			"ip_address":   "192.0.2.20",
			"port":         3310,
			"role":         "replica",
			"health":       map[string]string{"state": "unhealthy"},
		},
	}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("session metadata execute: %d %s", response.Code, response.Body.String())
	}
	if approvalGate.calls != 0 {
		t.Fatalf("session metadata called external approval gate: %d", approvalGate.calls)
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].ResourceID != snapshot.Instances[0].ResourceID || instances[0].Hostname != "mysql-new" {
		t.Fatalf("session metadata reconciliation=%+v", instances)
	}
	foundApprovalAudit := false
	for _, event := range repository.Audits() {
		if event.Stage == model.StageApprove {
			foundApprovalAudit = event.Actor == username && strings.Contains(event.Message, "platform")
		}
	}
	if !foundApprovalAudit {
		t.Fatalf("session metadata approval audit missing: %+v", repository.Audits())
	}
}

func TestMetadataExecutePersistenceFailureIsSanitizedAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register mysql: %v", err)
	}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository)
	server := NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken))
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-failure"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "failure-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	beforeInstances := repository.Instances(cluster.ResourceID)
	beforeEndpoints := repository.Endpoints(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("block snapshot path: %v", err)
	}
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "failure-native"}, "hostname": "mysql-new", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), path) || strings.Contains(response.Body.String(), "rename metadata snapshot") {
		t.Fatalf("metadata persistence mapping = %d %s", response.Code, response.Body.String())
	}
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) {
		t.Fatalf("failed metadata persistence published partial state")
	}
}

func TestMetadataExecuteReturnsIndeterminateResultAfterPostCommitJournalFailure(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-indeterminate"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-old", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Now().UTC()
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{
		ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "indeterminate-native"}, Hostname: "mysql-old", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy},
	}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(mysql.New(apiRunner{})); err != nil {
		t.Fatalf("register mysql: %v", err)
	}
	journal := apiStageFailingJournal{repository: repository, stage: model.StageExecute}
	service := workflow.New(registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{}, workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, journal)
	server := NewServer(registry, repository, service, &fakeRefresher{}, WithControlToken(testControlToken))
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "indeterminate-native"}, "hostname": "mysql-new", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"status":"indeterminate"`) || strings.Contains(response.Body.String(), "secret journal") {
		t.Fatalf("post-commit journal response = %d %s", response.Code, response.Body.String())
	}
	instances := repository.Instances(cluster.ResourceID)
	if len(instances) != 1 || instances[0].Hostname != "mysql-new" {
		t.Fatalf("metadata commit was not preserved: %+v", instances)
	}
	reports := repository.Reports()
	if len(reports) != 1 || !strings.Contains(reports[0].Summary, "journal persistence failed") {
		t.Fatalf("durable report did not preserve indeterminate outcome: %+v", reports)
	}
}

func TestMetadataPostCommitDurabilityWarningReturnsCommittedResult(t *testing.T) {
	recorder := httptest.NewRecorder()
	instanceID := model.NewResourceID()
	endpointID := model.NewResourceID()
	execution := model.Execution{OperationID: model.NewResourceID(), Status: model.OperationIndeterminate, Message: "operation committed but persistence durability could not be confirmed"}
	reconciled := model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: instanceID}, Hostname: "mysql-new", Port: 4406}
	endpoint := model.Endpoint{ResourceMeta: model.ResourceMeta{ResourceID: endpointID}, Hostname: "mysql-new", Port: 4406}
	commitErr := fmt.Errorf("%w: secret filesystem detail", store.ErrPostCommitDurability)
	if !writeMetadataExecutionFailure(recorder, execution, reconciled, endpoint, commitErr, commitErr) {
		t.Fatal("post-commit durability warning was not handled")
	}
	body := recorder.Body.String()
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(body, `"status":"indeterminate"`) || !strings.Contains(body, string(instanceID)) || !strings.Contains(body, string(endpointID)) {
		t.Fatalf("post-commit response = %d %s", recorder.Code, body)
	}
	if strings.Contains(body, "secret filesystem detail") {
		t.Fatalf("post-commit response leaked persistence details: %s", body)
	}
}

func TestMetadataRouteRejectsEngineAndIdentityTrustMismatchBeforeAdapterOrWorkflow(t *testing.T) {
	tests := []struct {
		name               string
		payloadEngine      model.Engine
		operationEngine    model.Engine
		operationClusterID model.ResourceID
		serverUUID         string
	}{
		{name: "payload engine", payloadEngine: model.EnginePostgreSQL, operationEngine: model.EnginePostgreSQL, serverUUID: "trust-native"},
		{name: "operation engine", payloadEngine: model.EngineMySQL, operationEngine: model.EnginePostgreSQL, serverUUID: "trust-native"},
		{name: "operation cluster", payloadEngine: model.EngineMySQL, operationEngine: model.EngineMySQL, operationClusterID: model.NewResourceID(), serverUUID: "trust-native"},
		{name: "native identity", payloadEngine: model.EngineMySQL, operationEngine: model.EngineMySQL, serverUUID: "different-native"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := store.NewMemory()
			cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "metadata-trust-" + test.name}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
			if err != nil {
				t.Fatalf("create inventory: %v", err)
			}
			observedAt := time.Date(2026, time.July, 12, 15, 0, 0, 0, time.UTC)
			snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "trust-native"}, Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
			if err != nil {
				t.Fatalf("seed topology: %v", err)
			}
			candidate := newMetadataAdapterSpy()
			server := newAPIServer(t, repository, candidate, &fakeRefresher{})
			beforeInstances := repository.Instances(cluster.ResourceID)
			beforeEndpoints := repository.Endpoints(cluster.ResourceID)
			beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
			beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
			beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
			payload := map[string]interface{}{
				"operation": map[string]interface{}{"engine": test.operationEngine, "cluster_id": test.operationClusterID, "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
				"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": test.payloadEngine, "engine_identity": map[string]string{"server_uuid": test.serverUUID}, "hostname": "must-not-publish", "port": 4406},
			}
			response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("trust mismatch status = %d: %s", response.Code, response.Body.String())
			}
			if candidate.callCount() != 0 || len(repository.Audits()) != 0 || len(repository.Reports()) != 0 {
				t.Fatalf("trust mismatch reached adapter/workflow: calls=%d audits=%d reports=%d", candidate.callCount(), len(repository.Audits()), len(repository.Reports()))
			}
			afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
			afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
			afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
			if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) || afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
				t.Fatal("trust mismatch changed repository state")
			}
		})
	}
}

func TestMetadataRouteRejectsEmptyActiveEndpointAddressAtomically(t *testing.T) {
	repository := store.NewMemory()
	cluster, endpoints, err := repository.CreateClusterWithEndpoints(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "empty-metadata-address"}, []model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}})
	if err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	observedAt := time.Date(2026, time.July, 12, 16, 0, 0, 0, time.UTC)
	snapshot, err := repository.ApplyDiscoveryRefresh(store.DiscoveryRefresh{ClusterID: cluster.ResourceID, InventoryGeneration: testInventoryGeneration(t, repository, cluster.ResourceID), ObservedAt: observedAt, Observations: []store.DiscoveryObservation{{EndpointID: endpoints[0].ResourceID, Instance: model.DatabaseInstance{ClusterID: cluster.ResourceID, Engine: model.EngineMySQL, EngineIdentity: model.EngineIdentity{"server_uuid": "empty-address-native"}, Hostname: "mysql-a", Port: 3306, Role: model.RolePrimary, Health: model.Health{State: model.HealthHealthy}}}}, Probes: []model.ProbeStatus{{EndpointID: endpoints[0].ResourceID, Health: model.Health{State: model.HealthHealthy}}}})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	candidate := newMetadataAdapterSpy()
	server := newAPIServer(t, repository, candidate, &fakeRefresher{})
	beforeInstances := repository.Instances(cluster.ResourceID)
	beforeEndpoints := repository.Endpoints(cluster.ResourceID)
	beforeTopology, _ := repository.TopologySnapshot(cluster.ResourceID)
	beforeInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	beforeWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	payload := map[string]interface{}{
		"operation": map[string]interface{}{"engine": "mysql", "kind": "metadata_reconciliation", "requested_by": "dba"}, "approval_token": "approved", "endpoint_id": endpoints[0].ResourceID,
		"instance": map[string]interface{}{"resource_id": snapshot.Instances[0].ResourceID, "cluster_id": cluster.ResourceID, "engine": "mysql", "engine_identity": map[string]string{"server_uuid": "empty-address-native"}, "hostname": "   ", "ip_address": "\t", "port": 4406},
	}
	response := callJSON(t, server.Handler(), http.MethodPost, "/api/v1/metadata/reconcile/execute", payload)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty metadata address status = %d: %s", response.Code, response.Body.String())
	}
	if candidate.callCount() != 0 || len(repository.Audits()) != 0 || len(repository.Reports()) != 0 {
		t.Fatalf("empty address reached adapter/workflow: calls=%d audits=%d reports=%d", candidate.callCount(), len(repository.Audits()), len(repository.Reports()))
	}
	afterTopology, found := repository.TopologySnapshot(cluster.ResourceID)
	afterInventory, _ := repository.DiscoveryInventory(cluster.ResourceID)
	afterWatermark, _ := repository.ObservationWatermark(cluster.ResourceID)
	if !reflect.DeepEqual(repository.Instances(cluster.ResourceID), beforeInstances) || !reflect.DeepEqual(repository.Endpoints(cluster.ResourceID), beforeEndpoints) || !found || !reflect.DeepEqual(afterTopology, beforeTopology) || afterInventory.Generation != beforeInventory.Generation || !afterWatermark.Equal(beforeWatermark) {
		t.Fatal("empty metadata address changed repository state")
	}
}

func TestConsoleIsServedAtRoot(t *testing.T) {
	server, _ := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected console response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "ClusterGuard HA") {
		t.Fatalf("expected standalone product console, got %s", response.Body.String())
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("console Cache-Control=%q want no-store", cacheControl)
	}
	head := httptest.NewRecorder()
	server.Handler().ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/", nil))
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("expected console HEAD response, got %d %q", head.Code, head.Header().Get("Content-Type"))
	}
}

func TestHandlerAddsBrowserSecurityHeaders(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithSecureCookies(true))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	for name, expected := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if value := response.Header().Get(name); value != expected {
			t.Fatalf("%s=%q want %q", name, value, expected)
		}
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'self'") {
		t.Fatalf("Content-Security-Policy=%q", policy)
	}
}
