package api

import (
	"bytes"
	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/configuration"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type configurationAuthority struct{ Calls int }

func (a *configurationAuthority) RequireMutationAuthority(context.Context) error {
	a.Calls++
	return nil
}
func (a *configurationAuthority) Status(context.Context) consensus.Status {
	return consensus.Status{Enabled: true, Role: "follower", LeaderKnown: true}
}
func (a *configurationAuthority) ControllerMembers(context.Context) ([]consensus.ControllerMember, error) {
	return nil, nil
}
func TestConfigurationCandidateKeepsControlAuthenticationAndStrictDecoding(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{"http_address":"127.0.0.1:3000","allow_insecure_http":true,"metadata_path":"`+filepath.Join(dir, "metadata.json")+`"}`), 0600)
	file, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	a := &configurationAuthority{}
	m := &configuration.Manager{Config: file, ConfigPath: p, Authority: a, Repository: store.NewMemory()}
	if e = m.Initialize(); e != nil {
		t.Fatal(e)
	}
	s := NewServer(adapter.NewRegistry(), m.Repository, nil, nil, WithControlToken("control-test"), WithMutationAuthority(a), WithConfigurationDistribution(m))
	for _, tt := range []struct {
		Token, Body string
		Code        int
	}{{"", `{"changes":{"agent.command_timeout_seconds":10}}`, 401}, {"control-test", `{"changes":{"agent.command_timeout_seconds":10}}`, 200}, {"control-test", `{"changes":{"agent.command_timeout_seconds":10},"unknown":1}`, 400}, {"control-test", `{"changes":{"agent.command_timeout_seconds":1.5}}`, 400}, {"control-test", `{"changes":{"http_address":2}}`, 409}} {
		r := httptest.NewRequest("POST", configurationRoot+"candidate", strings.NewReader(tt.Body))
		if tt.Token != "" {
			r.Header.Set("Authorization", "Bearer "+tt.Token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tt.Code {
			t.Fatalf("candidate %d expected %d: %s", w.Code, tt.Code, w.Body.String())
		}
	}
	if a.Calls != 0 {
		t.Fatal("local read-only candidate invoked Leader mutation forwarding")
	}
	r := httptest.NewRequest("POST", configurationRoot+"dispatch", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer control-test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if a.Calls != 1 || w.Code != 409 {
		t.Fatal("real dispatch did not require authority")
	}
}

func TestConfigurationMutationRoutesRequireAdminAndSessionCSRF(t *testing.T) {
	server, repository, _ := newAuthenticationTestServer(t)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, "admin", apiBootstrapPassword); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	password := "Configuration-admin-password-456"
	response := client.request(t, "POST", "/api/v1/auth/password", map[string]string{"current_password": apiBootstrapPassword, "new_password": password}, true)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response = client.login(t, "admin", password); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for _, suffix := range []string{"candidate", "plan", "dispatch", "permit", "tasks/00000000-0000-4000-8000-000000000001/retry"} {
		if response = client.request(t, "POST", configurationRoot+suffix, map[string]any{}, false); response.Code != 403 {
			t.Fatalf("no CSRF accepted %s: %d", suffix, response.Code)
		}
	}
	if response = client.request(t, "POST", configurationRoot+"candidate", map[string]any{}, true); response.Code != 503 {
		t.Fatalf("authenticated readonly candidate should reach unavailable provider: %d %s", response.Code, response.Body.String())
	}
	hasher := platformauth.Argon2Hasher{Params: platformauth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}, Random: bytes.NewReader(bytes.Repeat([]byte{0x37}, 128))}
	hash, e := hasher.Hash("Configuration-viewer-password-123")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repository.CreatePlatformUser(model.PlatformUser{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Username: "viewer", DisplayName: "Viewer", Role: model.PlatformRoleViewer, PasswordHash: hash, AuthRevision: 1}); e != nil {
		t.Fatal(e)
	}
	viewer := &authTestClient{handler: server.Handler()}
	if response = viewer.login(t, "viewer", "Configuration-viewer-password-123"); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for _, suffix := range []string{"candidate", "plan", "dispatch", "permit", "tasks/00000000-0000-4000-8000-000000000001/rollback"} {
		if response = viewer.request(t, "POST", configurationRoot+suffix, map[string]any{}, true); response.Code != 403 {
			t.Fatalf("viewer accepted %s: %d", suffix, response.Code)
		}
	}
}
