package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

const testBootstrapPassword = "Test-bootstrap-password-123"

func newTestService(t *testing.T) (*Service, *store.Repository, *time.Time) {
	t.Helper()
	now := time.Date(2026, time.July, 16, 13, 0, 0, 0, time.UTC)
	repository := store.NewMemory()
	random := bytes.NewReader(bytes.Repeat([]byte{0x4d}, 4096))
	hasher := testArgon2Hasher()
	service := New(repository, hasher, random, func() time.Time { return now }, 8*time.Hour)
	return service, repository, &now
}

func TestEnsureBootstrapAdminIsIdempotentAndStoresOnlyHash(t *testing.T) {
	service, repository, _ := newTestService(t)
	first, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	second, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure bootstrap admin again: %v", err)
	}
	if first.ResourceID != second.ResourceID || first.Username != DefaultAdminUsername ||
		first.Role != model.PlatformRoleAdmin || !first.MustChangePassword {
		t.Fatalf("unexpected bootstrap admin: first=%+v second=%+v", first, second)
	}
	if first.PasswordHash == "" || strings.Contains(first.PasswordHash, testBootstrapPassword) {
		t.Fatal("bootstrap password was not stored as a one-way hash")
	}
	if users := repository.PlatformUsers(); len(users) != 1 {
		t.Fatalf("bootstrap duplicated users: %+v", users)
	}
	raw, err := repository.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	if bytes.Contains(raw, []byte(testBootstrapPassword)) {
		t.Fatal("replicated metadata contains bootstrap plaintext password")
	}
}

func TestEnsureBootstrapAdminAcceptsOnlyDocumentedDefaultPasswordException(t *testing.T) {
	service, repository, _ := newTestService(t)
	user, err := service.EnsureBootstrapAdmin(context.Background(), DefaultBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure default bootstrap administrator: %v", err)
	}
	if !user.MustChangePassword || !service.hasher.Verify(user.PasswordHash, DefaultBootstrapPassword) {
		t.Fatalf("default bootstrap account is not forced-reset or hash-verifiable: %+v", user)
	}
	if state, err := repository.ReplicatedState(); err != nil || bytes.Contains(state, []byte(DefaultBootstrapPassword)) {
		t.Fatalf("replicated state exposes default bootstrap password: err=%v", err)
	}
}

func TestLoginUsesGenericFailureAndIssuesHashedSession(t *testing.T) {
	service, repository, _ := newTestService(t)
	if _, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword); err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	for _, attempt := range []struct{ username, password string }{
		{username: "missing", password: "wrong-password"},
		{username: "admin", password: "wrong-password"},
	} {
		if _, err := service.Login(context.Background(), attempt.username, attempt.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("login %q error=%v", attempt.username, err)
		}
	}

	login, err := service.Login(context.Background(), " ADMIN ", testBootstrapPassword)
	if err != nil {
		t.Fatalf("login bootstrap admin: %v", err)
	}
	if login.SessionToken == "" || login.CSRFToken == "" || !login.Principal.MustChangePassword {
		t.Fatalf("login result=%+v", login)
	}
	sessionID, _, err := ParseSessionToken(login.SessionToken)
	if err != nil {
		t.Fatalf("parse session token: %v", err)
	}
	session, found := repository.PlatformSession(sessionID)
	if !found || session.TokenHash == "" || session.CSRFHash == "" {
		t.Fatalf("stored session=%+v found=%t", session, found)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if bytes.Contains(encoded, []byte(login.SessionToken)) || bytes.Contains(encoded, []byte(login.CSRFToken)) {
		t.Fatal("stored session contains plaintext token")
	}
}

func TestLoginThrottlesRepeatedFailuresAndRecoversAfterCooldown(t *testing.T) {
	service, _, now := newTestService(t)
	if _, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword); err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	for attempt := 0; attempt < maximumLoginFailures; attempt++ {
		if _, err := service.Login(context.Background(), DefaultAdminUsername, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed login %d error=%v", attempt, err)
		}
	}
	if _, err := service.Login(context.Background(), DefaultAdminUsername, testBootstrapPassword); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("throttled login error=%v", err)
	}
	*now = now.Add(LoginThrottleRetryAfter + time.Second)
	if _, err := service.Login(context.Background(), DefaultAdminUsername, testBootstrapPassword); err != nil {
		t.Fatalf("login after cooldown: %v", err)
	}
}

func TestAuthenticateChecksExpiryRevocationAndUserRevision(t *testing.T) {
	service, repository, now := newTestService(t)
	user, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	login, err := service.Login(context.Background(), user.Username, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	principal, err := service.Authenticate(context.Background(), login.SessionToken)
	if err != nil || principal.UserID != user.ResourceID || principal.SessionID == "" {
		t.Fatalf("authenticate principal=%+v err=%v", principal, err)
	}
	if err := service.ValidateCSRF(context.Background(), principal, login.CSRFToken); err != nil {
		t.Fatalf("validate csrf: %v", err)
	}
	if err := service.ValidateCSRF(context.Background(), principal, "wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("wrong csrf error=%v", err)
	}

	*now = now.Add(9 * time.Hour)
	if _, err := service.Authenticate(context.Background(), login.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session error=%v", err)
	}

	*now = now.Add(-9 * time.Hour)
	second, err := service.Login(context.Background(), user.Username, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login second session: %v", err)
	}
	if err := service.Logout(context.Background(), second.SessionToken); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := service.Authenticate(context.Background(), second.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session error=%v", err)
	}

	third, err := service.Login(context.Background(), user.Username, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login third session: %v", err)
	}
	storedUser, _ := repository.PlatformUser(user.ResourceID)
	_, err = repository.ChangePlatformPassword(store.ChangePlatformPasswordRequest{
		UserID:                   storedUser.ResourceID,
		ExpectedMetadataRevision: storedUser.MetadataRevision,
		PasswordHash:             storedUser.PasswordHash,
		ChangedAt:                *now,
	})
	if err != nil {
		t.Fatalf("increment auth revision: %v", err)
	}
	if _, err := service.Authenticate(context.Background(), third.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("stale auth revision error=%v", err)
	}
}

func TestChangePasswordRejectsWeakOrWrongCurrentAndRevokesAllSessions(t *testing.T) {
	service, repository, _ := newTestService(t)
	if _, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword); err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	first, err := service.Login(context.Background(), DefaultAdminUsername, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login first: %v", err)
	}
	second, err := service.Login(context.Background(), DefaultAdminUsername, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login second: %v", err)
	}
	if _, err := service.ChangePassword(context.Background(), first.SessionToken, "wrong", "A-new-secure-password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password error=%v", err)
	}
	if _, err := service.ChangePassword(context.Background(), first.SessionToken, testBootstrapPassword, "short"); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("weak new password error=%v", err)
	}
	changed, err := service.ChangePassword(context.Background(), first.SessionToken, testBootstrapPassword, "A-new-secure-password-123")
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if changed.MustChangePassword || changed.AuthRevision < 2 {
		t.Fatalf("changed user=%+v", changed)
	}
	for _, token := range []string{first.SessionToken, second.SessionToken} {
		if _, err := service.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("old session survived password change: %v", err)
		}
	}
	if _, err := service.Login(context.Background(), DefaultAdminUsername, testBootstrapPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, err := service.Login(context.Background(), DefaultAdminUsername, "A-new-secure-password-123"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	if sessions := repository.PlatformSessions(changed.ResourceID); len(sessions) != 3 {
		t.Fatalf("unexpected session history: %+v", sessions)
	}
}

func TestDisabledUserCannotLoginOrUseExistingSession(t *testing.T) {
	service, repository, now := newTestService(t)
	user, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	login, err := service.Login(context.Background(), user.Username, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	disabled := user
	disabled.Disabled = true
	disabled.MetadataRevision++
	disabled.UpdatedAt = *now
	if err := repository.ReplacePlatformUser(user.MetadataRevision, disabled); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := service.Login(context.Background(), user.Username, testBootstrapPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled login error=%v", err)
	}
	if _, err := service.Authenticate(context.Background(), login.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled session error=%v", err)
	}
}
