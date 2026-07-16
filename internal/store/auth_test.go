package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func platformUserFixture(now time.Time) model.PlatformUser {
	return model.PlatformUser{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		Username:           "admin",
		DisplayName:        "Administrator",
		Role:               model.PlatformRoleAdmin,
		PasswordHash:       "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		MustChangePassword: true,
		AuthRevision:       1,
	}
}

func platformSessionFixture(user model.PlatformUser, now time.Time) model.PlatformSession {
	return model.PlatformSession{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		UserID:           user.ResourceID,
		TokenHash:        "sha256:session-token-hash",
		CSRFHash:         "sha256:csrf-token-hash",
		UserAuthRevision: user.AuthRevision,
		IssuedAt:         now,
		ExpiresAt:        now.Add(8 * time.Hour),
	}
}

func TestPlatformUserAndSessionRoundTripWithoutPlaintextSecrets(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	repository := NewMemory()
	user := platformUserFixture(now)
	created, err := repository.CreatePlatformUser(user)
	if err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	session := platformSessionFixture(created, now)
	if err := repository.PutPlatformSession(session); err != nil {
		t.Fatalf("put platform session: %v", err)
	}

	storedUser, found := repository.PlatformUser(created.ResourceID)
	if !found || storedUser.Username != "admin" || storedUser.PasswordHash != user.PasswordHash {
		t.Fatalf("stored user=%+v found=%t", storedUser, found)
	}
	byName, found := repository.PlatformUserByUsername(" ADMIN ")
	if !found || byName.ResourceID != created.ResourceID {
		t.Fatalf("username lookup=%+v found=%t", byName, found)
	}
	storedSession, found := repository.PlatformSession(session.ResourceID)
	if !found || storedSession.TokenHash != session.TokenHash || storedSession.CSRFHash != session.CSRFHash {
		t.Fatalf("stored session=%+v found=%t", storedSession, found)
	}

	raw, err := repository.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}
	for _, plaintext := range []string{"admin123", "session-plaintext", "csrf-plaintext"} {
		if bytes.Contains(raw, []byte(plaintext)) {
			t.Fatalf("replicated auth state contains plaintext %q", plaintext)
		}
	}
}

func TestPlatformUsernamesAreUniqueCaseInsensitively(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	repository := NewMemory()
	if _, err := repository.CreatePlatformUser(platformUserFixture(now)); err != nil {
		t.Fatalf("create first user: %v", err)
	}
	duplicate := platformUserFixture(now)
	duplicate.Username = " ADMIN "
	if _, err := repository.CreatePlatformUser(duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username error=%v", err)
	}
	if users := repository.PlatformUsers(); len(users) != 1 {
		t.Fatalf("duplicate username changed users: %+v", users)
	}
}

func TestPlatformPasswordChangeAtomicallyRevokesAllSessions(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	repository := NewMemory()
	user, err := repository.CreatePlatformUser(platformUserFixture(now))
	if err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	first := platformSessionFixture(user, now)
	second := platformSessionFixture(user, now.Add(time.Minute))
	if err := repository.PutPlatformSession(first); err != nil {
		t.Fatalf("put first session: %v", err)
	}
	if err := repository.PutPlatformSession(second); err != nil {
		t.Fatalf("put second session: %v", err)
	}

	changed, err := repository.ChangePlatformPassword(ChangePlatformPasswordRequest{
		UserID:                   user.ResourceID,
		ExpectedMetadataRevision: user.MetadataRevision,
		PasswordHash:             "$argon2id$v=19$m=65536,t=3,p=2$bmV3c2FsdA$bmV3aGFzaA",
		ChangedAt:                now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("change platform password: %v", err)
	}
	if changed.MustChangePassword || changed.AuthRevision != user.AuthRevision+1 ||
		changed.MetadataRevision != user.MetadataRevision+1 || changed.PasswordChangedAt.IsZero() {
		t.Fatalf("changed user=%+v", changed)
	}
	for _, sessionID := range []model.ResourceID{first.ResourceID, second.ResourceID} {
		session, found := repository.PlatformSession(sessionID)
		if !found || session.RevokedAt.IsZero() || session.MetadataRevision != 2 {
			t.Fatalf("revoked session=%+v found=%t", session, found)
		}
	}
}

func TestPlatformPasswordChangeConflictLeavesUserAndSessionsUnchanged(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	repository := NewMemory()
	user, err := repository.CreatePlatformUser(platformUserFixture(now))
	if err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	session := platformSessionFixture(user, now)
	if err := repository.PutPlatformSession(session); err != nil {
		t.Fatalf("put session: %v", err)
	}

	_, err = repository.ChangePlatformPassword(ChangePlatformPasswordRequest{
		UserID:                   user.ResourceID,
		ExpectedMetadataRevision: user.MetadataRevision + 1,
		PasswordHash:             "$argon2id$v=19$m=65536,t=3,p=2$bmV3c2FsdA$bmV3aGFzaA",
		ChangedAt:                now.Add(time.Minute),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale password change error=%v", err)
	}
	storedUser, _ := repository.PlatformUser(user.ResourceID)
	storedSession, _ := repository.PlatformSession(session.ResourceID)
	if storedUser.PasswordHash != user.PasswordHash || !storedSession.RevokedAt.IsZero() {
		t.Fatalf("failed password change mutated state: user=%+v session=%+v", storedUser, storedSession)
	}
}

func TestPlatformAuthStateReplicatesAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	leader := NewMemory()
	user, err := leader.CreatePlatformUser(platformUserFixture(now))
	if err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	session := platformSessionFixture(user, now)
	if err := leader.PutPlatformSession(session); err != nil {
		t.Fatalf("put platform session: %v", err)
	}
	event := model.SecurityEvent{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		UserID: user.ResourceID, Username: user.Username,
		Kind: "login_success", Outcome: "success", Message: "platform login succeeded",
	}
	if err := leader.RecordSecurityEvent(event); err != nil {
		t.Fatalf("record security event: %v", err)
	}
	state, err := leader.ReplicatedState()
	if err != nil {
		t.Fatalf("encode replicated state: %v", err)
	}

	path := filepath.Join(t.TempDir(), "metadata.json")
	follower, err := Open(path)
	if err != nil {
		t.Fatalf("open follower: %v", err)
	}
	if err := follower.ApplyReplicatedState(state); err != nil {
		t.Fatalf("apply replicated auth state: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen follower: %v", err)
	}
	if replicated, found := reopened.PlatformUser(user.ResourceID); !found || replicated.Username != user.Username {
		t.Fatalf("replicated user=%+v found=%t", replicated, found)
	}
	if replicated, found := reopened.PlatformSession(session.ResourceID); !found || replicated.UserID != user.ResourceID {
		t.Fatalf("replicated session=%+v found=%t", replicated, found)
	}
	if events := reopened.SecurityEvents(); len(events) != 1 || events[0].Kind != event.Kind {
		t.Fatalf("replicated security events=%+v", events)
	}
}

func TestPlatformAuthValidationFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*model.PlatformUser)
	}{
		{name: "invalid id", mutate: func(user *model.PlatformUser) { user.ResourceID = "" }},
		{name: "empty username", mutate: func(user *model.PlatformUser) { user.Username = "" }},
		{name: "invalid role", mutate: func(user *model.PlatformUser) { user.Role = "root" }},
		{name: "missing password hash", mutate: func(user *model.PlatformUser) { user.PasswordHash = "" }},
		{name: "missing auth revision", mutate: func(user *model.PlatformUser) { user.AuthRevision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := NewMemory()
			user := platformUserFixture(now)
			test.mutate(&user)
			if _, err := repository.CreatePlatformUser(user); !errors.Is(err, ErrValidation) {
				t.Fatalf("invalid user error=%v", err)
			}
		})
	}

	repository := NewMemory()
	user, err := repository.CreatePlatformUser(platformUserFixture(now))
	if err != nil {
		t.Fatalf("create platform user: %v", err)
	}
	session := platformSessionFixture(user, now)
	session.ExpiresAt = session.IssuedAt
	if err := repository.PutPlatformSession(session); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid session error=%v", err)
	}
	event := model.SecurityEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1},
		Kind:         "login_failed",
		Outcome:      "failure",
		Message:      strings.Repeat("x", maximumSecurityEventMessageLength+1),
	}
	if err := repository.RecordSecurityEvent(event); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized security event error=%v", err)
	}
}
