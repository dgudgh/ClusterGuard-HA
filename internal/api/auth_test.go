package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newAuthenticationTestServer(t *testing.T) (*Server, *store.Repository, *platformauth.Service) {
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
	if _, err := service.EnsureBootstrapAdmin(context.Background()); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	registry := adapter.NewRegistry()
	workflowService := workflow.New(
		registry, workflow.TopologyDiscovery{Reader: repository}, workflow.AllowAllSafety{},
		workflow.NewMemoryLocks(), workflow.AllowAllApproval{}, repository,
	)
	server := NewServer(
		registry, repository, workflowService, &fakeRefresher{},
		WithControlToken(testControlToken), WithAuthentication(service),
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
	var failureBody string
	for _, attempt := range []struct{ username, password string }{
		{username: "missing", password: "admin123"},
		{username: "admin", password: "wrong-password"},
	} {
		response := client.login(t, attempt.username, attempt.password)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failed login status=%d body=%s", response.Code, response.Body.String())
		}
		if failureBody == "" {
			failureBody = response.Body.String()
		} else if response.Body.String() != failureBody {
			t.Fatalf("login failure reveals account state: first=%s second=%s", failureBody, response.Body.String())
		}
	}
	response := client.login(t, "admin", "admin123")
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"username":"admin"`, `"role":"admin"`, `"must_change_password":true`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("login response missing %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"password_hash", "token_hash", "csrf_hash", "admin123", "cgs_"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("login response exposes %q: %s", forbidden, body)
		}
	}
	events := repository.SecurityEvents()
	if len(events) != 3 || events[0].Kind != "login_failed" || events[2].Kind != "login_success" {
		t.Fatalf("login security events=%+v", events)
	}
}

func TestPlatformSessionRequiresPasswordChangeAndCSRFFOrMutation(t *testing.T) {
	server, _, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", "admin123"); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
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
	server, _, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", "admin123"); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	response := client.request(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"current_password": "admin123",
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
}

func TestPlatformLogoutRevokesSession(t *testing.T) {
	server, _, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", "admin123"); response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodPost, "/api/v1/auth/logout", map[string]interface{}{}, true); response.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", response.Code, response.Body.String())
	}
	if response := client.request(t, http.MethodGet, "/api/v1/auth/me", nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session remained valid: %d %s", response.Code, response.Body.String())
	}
}
