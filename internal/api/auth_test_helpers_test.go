package api

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

func attachAuthenticatedTestClient(
	t *testing.T,
	server *Server,
	repository *store.Repository,
	role model.PlatformRole,
) (*authTestClient, string) {
	t.Helper()
	now := time.Now().UTC()
	username := string(role) + "-platform-user"
	password := "Secure-platform-password-123"
	hasher := platformauth.Argon2Hasher{
		Params: platformauth.Argon2Params{
			Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x71}, 256)),
	}
	passwordHash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("hash platform password: %v", err)
	}
	if _, err := repository.CreatePlatformUser(model.PlatformUser{
		ResourceMeta: model.ResourceMeta{
			ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now,
		},
		Username: username, DisplayName: username, Role: role,
		PasswordHash: passwordHash, AuthRevision: 1,
	}); err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	server.authentication = platformauth.New(
		repository,
		hasher,
		bytes.NewReader(bytes.Repeat([]byte{0x72}, 8192)),
		func() time.Time { return now },
		8*time.Hour,
	)
	client := &authTestClient{handler: server.Handler()}
	if response := client.login(t, username, password); response.Code != http.StatusOK {
		t.Fatalf("platform login status=%d body=%s", response.Code, response.Body.String())
	}
	return client, username
}
