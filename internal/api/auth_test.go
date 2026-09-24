package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type authTestClient struct {
	handler http.Handler
	cookies []*http.Cookie
	csrf    string
}

const apiBootstrapPassword = "API-bootstrap-password-123"

type authMutationRPCStub struct {
	calls         int
	path          string
	leaderAddress string
}

func (stub *authMutationRPCStub) Forward(writer http.ResponseWriter, request *http.Request, leaderAddress string) error {
	stub.calls++
	stub.path = request.URL.Path
	stub.leaderAddress = leaderAddress
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]bool{"proxied": true}})
	return nil
}

func newAuthenticationTestServer(t *testing.T, options ...ServerOption) (*Server, *store.Repository, *platformauth.Service) {
	t.Helper()
	repository := store.NewMemory()
	hasher := platformauth.Argon2Hasher{
		Params: platformauth.Argon2Params{
			Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 4096)),
	}
	service := platformauth.New(
		repository, hasher, bytes.NewReader(bytes.Repeat([]byte{0x4d}, 8192)),
		func() time.Time { return time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC) },
		8*time.Hour,
	)
	if _, err := service.EnsureBootstrapAdmin(context.Background(), apiBootstrapPassword); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	registry := adapter.NewRegistry()
	workflowService := workflow.New(
		registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{},
		workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository,
	)
	server := NewServer(
		registry, repository, workflowService, &fakeRefresher{},
		append([]ServerOption{WithControlToken(testControlToken), WithAuthentication(service)}, options...)...,
	)
	return server, repository, service
}

func (client *authTestClient) request(t *testing.T, method, path string, body interface{}, csrf bool) *httptest.ResponseRecorder {
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
	for _, cookie := range client.cookies {
		request.AddCookie(cookie)
	}
	if csrf {
		request.Header.Set("X-CSRF-Token", client.csrf)
	}
	response := httptest.NewRecorder()
	client.handler.ServeHTTP(response, request)
	return response
}

func (client *authTestClient) login(t *testing.T, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	response := client.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, false)
	if response.Code == http.StatusOK {
		client.cookies = response.Result().Cookies()
		for _, cookie := range client.cookies {
			if cookie.Name == csrfCookieName {
				client.csrf = cookie.Value
			}
		}
	}
	return response
}

func TestPlatformLoginUsesGenericFailureAndReturnsSanitizedUser(t *testing.T) {
	server, repository, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	var failureMessage string
	for _, attempt := range []struct{ username, password string }{
		{username: "missing", password: apiBootstrapPassword},
		{username: "admin", password: "wrong-password"},
	} {
		response := client.login(t, attempt.username, attempt.password)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failed login status=%d body=%s", response.Code, response.Body.String())
		}
		var failure struct {
			Status    string `json:"status"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatalf("decode login failure: %v", err)
		}
		if failure.Status != "error" || failure.Message == "" || failure.RequestID == "" {
			t.Fatalf("invalid login failure envelope: %+v", failure)
		}
		if failureMessage == "" {
			failureMessage = failure.Message
		} else if failure.Message != failureMessage {
			t.Fatalf("login failure reveals account state: first=%q second=%q", failureMessage, failure.Message)
		}
	}
	response := client.login(t, "admin", apiBootstrapPassword)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"username":"admin"`, `"role":"admin"`, `"must_change_password":true`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("login response missing %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"password_hash", "token_hash", "csrf_hash", apiBootstrapPassword, "cgs_"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("login response exposes %q: %s", forbidden, body)
		}
	}
	events := repository.SecurityEvents()
	if len(events) != 3 || events[0].Kind != "login_failed" || events[2].Kind != "login_success" {
		t.Fatalf("login security events=%+v", events)
	}
}

func TestPlatformAuthenticationMutationsProxyToRaftLeader(t *testing.T) {
	server, repository, _ := newAuthenticationTestServer(t)
	leaderAPI := "https://controller-leader.example:3000"
	authority := &apiMutationAuthorityStub{
		err: errors.New("not leader"), leaderID: model.NewResourceID(),
		leaderAddress: "controller-leader.example:10009", leaderAPI: leaderAPI,
	}
	rpc := &authMutationRPCStub{}
	WithMutationAuthority(authority)(server)
	WithMutationRPC(rpc)(server)

	client := &authTestClient{handler: server.Handler()}
	response := client.login(t, "admin", apiBootstrapPassword)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"proxied":true`) {
		t.Fatalf("proxied login status=%d body=%s", response.Code, response.Body.String())
	}
	if rpc.calls != 1 || rpc.path != "/api/v1/auth/login" || rpc.leaderAddress != leaderAPI {
		t.Fatalf("authentication RPC calls=%d path=%q leader=%q", rpc.calls, rpc.path, rpc.leaderAddress)
	}
	if len(repository.PlatformSessions("")) != 0 || len(repository.SecurityEvents()) != 0 {
		t.Fatalf("follower mutated authentication state: sessions=%+v events=%+v", repository.PlatformSessions(""), repository.SecurityEvents())
	}
}

func TestPlatformLoginThrottlingReturnsRetryableStatus(t *testing.T) {
	server, repository, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	for attempt := 0; attempt < 5; attempt++ {
		response := client.login(t, "admin", "wrong-password")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	response := client.login(t, "admin", "wrong-password")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled login status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "60" {
		t.Fatalf("throttled login Retry-After=%q", response.Header().Get("Retry-After"))
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "admin") {
		t.Fatalf("throttled response exposes account identity: %s", response.Body.String())
	}
	events := repository.SecurityEvents()
	if len(events) != 6 || events[len(events)-1].Kind != "login_throttled" {
		t.Fatalf("login security events=%+v", events)
	}
}

func TestPlatformSessionCookiesFollowTrustedTransportConfiguration(t *testing.T) {
	server, _, _ := newAuthenticationTestServer(t)
	WithSecureCookies(true)(server)
	client := &authTestClient{handler: server.Handler()}
	response := client.login(t, "admin", apiBootstrapPassword)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	secureCookies := 0
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name != sessionCookieName && cookie.Name != csrfCookieName {
			continue
		}
		secureCookies++
		if !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("unsafe authentication cookie: %+v", cookie)
		}
	}
	if secureCookies != 2 {
		t.Fatalf("authentication cookies=%v", response.Result().Cookies())
	}
}

func TestPlatformSessionRequiresPasswordChangeAndCSRFFOrMutation(t *testing.T) {
	server, _, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", apiBootstrapPassword); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodGet, "/api/v1/clusters", nil, false); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"password_change_required":true`) {
		t.Fatalf("bootstrap password read status=%d body=%s", response.Code, response.Body.String())
	}
	cluster := map[string]interface{}{
		"display_name": "secured",
		"engine":       "mysql",
		"endpoints":    []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}},
	}
	if response := client.request(t, http.MethodPost, "/api/v1/clusters", cluster, false); response.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", response.Code, response.Body.String())
	}
	client.csrf = "wrong"
	if response := client.request(t, http.MethodPost, "/api/v1/clusters", cluster, true); response.Code != http.StatusForbidden {
		t.Fatalf("wrong csrf status=%d body=%s", response.Code, response.Body.String())
	}
	for _, cookie := range client.cookies {
		if cookie.Name == csrfCookieName {
			client.csrf = cookie.Value
		}
	}
	response := client.request(t, http.MethodPost, "/api/v1/clusters", cluster, true)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"password_change_required":true`) {
		t.Fatalf("bootstrap password mutation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPlatformPasswordChangeRevokesSessionAndAllowsRelogin(t *testing.T) {
	server, repository, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", apiBootstrapPassword); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	response := client.request(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"current_password": apiBootstrapPassword,
		"new_password":     "A-new-secure-password-123",
	}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodGet, "/api/v1/auth/me", nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("old session survived password change: %d %s", response.Code, response.Body.String())
	}
	client.cookies = nil
	client.csrf = ""
	if response := client.login(t, "admin", "A-new-secure-password-123"); response.Code != http.StatusOK {
		t.Fatalf("new password login status=%d body=%s", response.Code, response.Body.String())
	}
	response = client.request(t, http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "secured",
		"engine":       "mysql",
		"endpoints":    []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}},
	}, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("authenticated admin mutation status=%d body=%s", response.Code, response.Body.String())
	}
	cluster := repository.Clusters()[0]
	response = client.request(t, http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), map[string]string{"confirm_display_name": cluster.DisplayName}, true)
	if response.Code != http.StatusOK || len(repository.Clusters()) != 0 {
		t.Fatalf("authenticated admin retirement status=%d body=%s clusters=%+v", response.Code, response.Body.String(), repository.Clusters())
	}
}

func TestServerResolvesBootstrapPasswordFile(t *testing.T) {
	defaulted := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil)
	if defaulted.bootstrapPasswordFile != platformauth.DefaultBootstrapPasswordFile {
		t.Fatalf("bootstrap password file=%q want the platform default %q", defaulted.bootstrapPasswordFile, platformauth.DefaultBootstrapPasswordFile)
	}
	custom := filepath.Join(t.TempDir(), "bootstrap-admin-password")
	configured := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithBootstrapPasswordFile(custom))
	if configured.bootstrapPasswordFile != custom {
		t.Fatalf("bootstrap password file=%q want %q", configured.bootstrapPasswordFile, custom)
	}
	trimmed := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithBootstrapPasswordFile("  "+custom+"  "))
	if trimmed.bootstrapPasswordFile != custom {
		t.Fatalf("bootstrap password file=%q want the trimmed %q", trimmed.bootstrapPasswordFile, custom)
	}
	blanked := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithBootstrapPasswordFile("   "))
	if blanked.bootstrapPasswordFile != platformauth.DefaultBootstrapPasswordFile {
		t.Fatalf("a blank override must keep the default, got %q", blanked.bootstrapPasswordFile)
	}
}

// A custom MetadataPath relocates the generated administrator credential to sit
// beside the metadata. The password-change handler has to remove *that* artifact
// and not the published default, otherwise the plaintext credential survives the
// first password change for as long as the controller keeps running.
func TestPasswordChangeRemovesBootstrapArtifactAtConfiguredPath(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), filepath.Base(platformauth.DefaultBootstrapPasswordFile))
	if err := os.WriteFile(artifactPath, []byte(apiBootstrapPassword+"\n"), 0o600); err != nil {
		t.Fatalf("seed bootstrap artifact: %v", err)
	}
	server, _, _ := newAuthenticationTestServer(t, WithBootstrapPasswordFile(artifactPath))
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", apiBootstrapPassword); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	response := client.request(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"current_password": apiBootstrapPassword,
		"new_password":     "A-new-secure-password-456",
	}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap artifact survived the password change: %v", err)
	}
}

func TestPlatformViewerCanReadButCannotMutate(t *testing.T) {
	server, repository, service := newAuthenticationTestServer(t)
	hasher := platformauth.Argon2Hasher{
		Params: platformauth.Argon2Params{
			Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x37}, 128)),
	}
	hash, err := hasher.Hash("Viewer-secure-password-123")
	if err != nil {
		t.Fatalf("hash viewer password: %v", err)
	}
	now := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)
	if _, err := repository.CreatePlatformUser(model.PlatformUser{
		ResourceMeta: model.ResourceMeta{
			ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now,
		},
		Username: "viewer", DisplayName: "Viewer", Role: model.PlatformRoleViewer,
		PasswordHash: hash, AuthRevision: 1,
	}); err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if service == nil {
		t.Fatal("authentication service is nil")
	}
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "viewer", "Viewer-secure-password-123"); response.Code != http.StatusOK {
		t.Fatalf("viewer login status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodGet, "/api/v1/clusters", nil, false); response.Code != http.StatusOK {
		t.Fatalf("viewer read status=%d body=%s", response.Code, response.Body.String())
	}
	response := client.request(t, http.MethodPost, "/api/v1/clusters", map[string]interface{}{
		"display_name": "blocked",
		"engine":       "mysql",
		"endpoints":    []map[string]interface{}{{"hostname": "mysql-a", "port": 3306}},
	}, true)
	if response.Code != http.StatusForbidden || len(repository.Clusters()) != 0 {
		t.Fatalf("viewer mutation status=%d body=%s clusters=%+v", response.Code, response.Body.String(), repository.Clusters())
	}
	cluster, _, err := repository.CreateClusterWithEndpoints(
		model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "viewer-cannot-retire"},
		[]model.Endpoint{{Kind: model.EndpointDatabase, Hostname: "mysql-a", Port: 3306, Active: true}},
	)
	if err != nil {
		t.Fatalf("create cluster for retirement authorization: %v", err)
	}
	response = client.request(t, http.MethodDelete, "/api/v1/clusters/"+string(cluster.ResourceID), map[string]string{"confirm_display_name": cluster.DisplayName}, true)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer retirement status=%d body=%s", response.Code, response.Body.String())
	}
	if _, found := repository.Cluster(cluster.ResourceID); !found {
		t.Fatal("viewer retired a cluster")
	}
}

func TestPlatformLogoutRevokesSession(t *testing.T) {
	server, _, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", apiBootstrapPassword); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodPost, "/api/v1/auth/logout", map[string]interface{}{}, true); response.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodGet, "/api/v1/auth/me", nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session remained valid: %d %s", response.Code, response.Body.String())
	}
}
